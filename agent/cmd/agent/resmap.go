package main

// Resource map: one namespace as a graph, like Argo CD's application tree.
//
//	Ingress / HTTPRoute ─▶ Service ─▶ Deployment ─▶ ReplicaSet ─▶ Pod
//	                                      ├─▶ ConfigMap, Secret (name only), PVC ─▶ PV
//	HorizontalPodAutoscaler ─▶ Deployment
//
// Edges come from what Kubernetes itself records: ownerReferences (Deployment owns
// ReplicaSet owns Pod), Service selectors, Ingress and HTTPRoute backends, the volumes
// and env of pod templates, and HPA targets. Read-only: lists only, Secrets are never read
// (a Secret shows as a name referenced by a pod template, nothing more).

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// mapMaxNodes keeps a huge namespace readable and the answer small.
const mapMaxNodes = 400

// MapNode is one box on the map.
type MapNode struct {
	ID         string `json:"id"` // Kind/name, unique in the namespace
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Group      string `json:"group"` // with version and resource: what the app needs to open it
	Version    string `json:"version"`
	Resource   string `json:"resource"`
	Namespaced bool   `json:"namespaced"`
	Status     string `json:"status,omitempty"`
	Health     string `json:"health"`            // ok | warn | bad | none
	Missing    bool   `json:"missing,omitempty"` // referenced, but not found (or not readable: Secrets)
}

// MapEdge points from the thing that uses or owns to the thing used or owned.
type MapEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ResourceMap is the answer to a "map" request.
type ResourceMap struct {
	Namespace string    `json:"namespace"`
	Nodes     []MapNode `json:"nodes"`
	Edges     []MapEdge `json:"edges"`
	Rev       uint64    `json:"rev"`                 // changes seen in the namespace: send it back as "since"
	Truncated bool      `json:"truncated,omitempty"` // more than mapMaxNodes: pods were left out
	Notes     []string  `json:"notes,omitempty"`     // e.g. a resource type the agent may not read
}

// kindRefs: the API resource of each kind on the map.
var kindRefs = map[string]struct {
	group, version, resource string
	namespaced               bool
}{
	"Ingress":                 {"networking.k8s.io", "v1", "ingresses", true},
	"HTTPRoute":               {"gateway.networking.k8s.io", "v1", "httproutes", true},
	"Service":                 {"", "v1", "services", true},
	"HorizontalPodAutoscaler": {"autoscaling", "v2", "horizontalpodautoscalers", true},
	"Deployment":              {"apps", "v1", "deployments", true},
	"StatefulSet":             {"apps", "v1", "statefulsets", true},
	"DaemonSet":               {"apps", "v1", "daemonsets", true},
	"CronJob":                 {"batch", "v1", "cronjobs", true},
	"ReplicaSet":              {"apps", "v1", "replicasets", true},
	"Job":                     {"batch", "v1", "jobs", true},
	"Pod":                     {"", "v1", "pods", true},
	"ConfigMap":               {"", "v1", "configmaps", true},
	"Secret":                  {"", "v1", "secrets", true},
	"PersistentVolumeClaim":   {"", "v1", "persistentvolumeclaims", true},
	"PersistentVolume":        {"", "v1", "persistentvolumes", false},
}

// mapInput is everything read from the API. buildMap only reads it, so tests fill it directly.
type mapInput struct {
	namespace    string
	pods         []*corev1.Pod
	deployments  []appsv1.Deployment
	replicaSets  []appsv1.ReplicaSet
	statefulSets []appsv1.StatefulSet
	daemonSets   []appsv1.DaemonSet
	jobs         []batchv1.Job
	cronJobs     []batchv1.CronJob
	services     []corev1.Service
	ingresses    []networkingv1.Ingress
	httpRoutes   []unstructured.Unstructured
	hpas         []autoscalingv2.HorizontalPodAutoscaler
	configMaps   map[string]bool
	pvcs         []corev1.PersistentVolumeClaim
	pvs          map[string]*corev1.PersistentVolume
	notes        []string
}

// resourceMap answers {namespace}: it lists what the map needs, then builds the graph.
func (h *requestHandler) resourceMap(ctx context.Context, p map[string]string) (*ResourceMap, error) {
	ns := p["namespace"]
	if ns == "" {
		return nil, apierrors.NewBadRequest("namespace is required")
	}
	// Live map: with since = the rev of the app's last answer, wait for the next change (or 12 s).
	// Then a short pause, because one action is several changes: deleting a pod also updates
	// its ReplicaSet and Deployment. The relay gives up after 20 s, so 12 s leaves room.
	if v := p["since"]; v != "" && h.changes != nil {
		if since, err := strconv.ParseUint(v, 10, 64); err == nil {
			h.changes.wait(ctx, ns, since, 12*time.Second)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	rev := h.changes.current(ns) // read before listing: a change during the lists wakes the next request
	in := mapInput{namespace: ns, configMaps: map[string]bool{}, pvs: map[string]*corev1.PersistentVolume{}}
	opts := metav1.ListOptions{}

	// skip records a type the agent could not list (RBAC, or the API is not installed) and goes on:
	// a partial map is more useful than an error.
	skip := func(what string, err error) bool {
		if err == nil {
			return false
		}
		if apierrors.IsForbidden(err) {
			in.notes = append(in.notes, what+": not allowed for Owl's agent (see rbac.extraApiGroups)")
		} else if !apierrors.IsNotFound(err) {
			in.notes = append(in.notes, what+": "+err.Error())
		}
		return true
	}

	pods, err := h.pods.Pods(ns).List(labels.Everything()) // informer cache: no API call
	if err != nil {
		return nil, err
	}
	in.pods = pods
	if l, err := h.cs.AppsV1().Deployments(ns).List(ctx, opts); !skip("Deployments", err) {
		in.deployments = l.Items
	}
	if l, err := h.cs.AppsV1().ReplicaSets(ns).List(ctx, opts); !skip("ReplicaSets", err) {
		in.replicaSets = l.Items
	}
	if l, err := h.cs.AppsV1().StatefulSets(ns).List(ctx, opts); !skip("StatefulSets", err) {
		in.statefulSets = l.Items
	}
	if l, err := h.cs.AppsV1().DaemonSets(ns).List(ctx, opts); !skip("DaemonSets", err) {
		in.daemonSets = l.Items
	}
	if l, err := h.cs.BatchV1().Jobs(ns).List(ctx, opts); !skip("Jobs", err) {
		in.jobs = l.Items
	}
	if l, err := h.cs.BatchV1().CronJobs(ns).List(ctx, opts); !skip("CronJobs", err) {
		in.cronJobs = l.Items
	}
	if l, err := h.cs.CoreV1().Services(ns).List(ctx, opts); !skip("Services", err) {
		in.services = l.Items
	}
	if l, err := h.cs.NetworkingV1().Ingresses(ns).List(ctx, opts); !skip("Ingresses", err) {
		in.ingresses = l.Items
	}
	if l, err := h.cs.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, opts); !skip("HorizontalPodAutoscalers", err) {
		in.hpas = l.Items
	}
	if l, err := h.cs.CoreV1().ConfigMaps(ns).List(ctx, opts); !skip("ConfigMaps", err) {
		for _, cm := range l.Items {
			in.configMaps[cm.Name] = true
		}
	}
	if l, err := h.cs.CoreV1().PersistentVolumeClaims(ns).List(ctx, opts); !skip("PersistentVolumeClaims", err) {
		in.pvcs = l.Items
		for _, c := range l.Items {
			if c.Spec.VolumeName == "" {
				continue
			}
			if pv, err := h.cs.CoreV1().PersistentVolumes().Get(ctx, c.Spec.VolumeName, metav1.GetOptions{}); err == nil {
				in.pvs[pv.Name] = pv
			}
		}
	}
	// Gateway API is a CRD: absent on many clusters, so "not found" is silent.
	routes := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	if l, err := h.dyn.Resource(routes).Namespace(ns).List(ctx, opts); !skip("HTTPRoutes", err) {
		in.httpRoutes = l.Items
	}
	m := buildMap(in)
	m.Rev = rev
	return m, nil
}

// graph collects nodes and edges without duplicates.
type graph struct {
	nodes  map[string]*MapNode
	edges  map[MapEdge]bool
	parent map[string]string // child -> owner, from ownerReferences
}

// state is a node's status text and health, kept together so helpers can return both.
type state struct{ status, health string }

func (g *graph) add(kind, name string, st state) *MapNode {
	id := kind + "/" + name
	if n, ok := g.nodes[id]; ok {
		return n
	}
	ref := kindRefs[kind]
	n := &MapNode{ID: id, Kind: kind, Name: name, Group: ref.group, Version: ref.version,
		Resource: ref.resource, Namespaced: ref.namespaced, Status: st.status, Health: st.health}
	g.nodes[id] = n
	return n
}

func (g *graph) link(from, to string) {
	if from != "" && to != "" && from != to {
		g.edges[MapEdge{From: from, To: to}] = true
	}
}

// own links an object to its controller, when that controller is on the map.
func (g *graph) own(child string, refs []metav1.OwnerReference) {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			if _, ok := kindRefs[r.Kind]; ok {
				owner := r.Kind + "/" + r.Name
				g.parent[child] = owner
				g.link(owner, child)
			}
			return
		}
	}
}

// top is the highest owner on the map: Pod -> ReplicaSet -> Deployment gives the Deployment.
func (g *graph) top(id string) string {
	for i := 0; i < 5; i++ { // ownership chains are short; the bound guards against a cycle
		p, ok := g.parent[id]
		if !ok || g.nodes[p] == nil {
			break
		}
		id = p
	}
	return id
}

func buildMap(in mapInput) *ResourceMap {
	g := &graph{nodes: map[string]*MapNode{}, edges: map[MapEdge]bool{}, parent: map[string]string{}}
	templates := map[string]corev1.PodSpec{} // workload -> pod template, for ConfigMaps, Secrets, PVCs

	for _, d := range in.deployments {
		g.add("Deployment", d.Name, replicasStatus(d.Status.ReadyReplicas, d.Spec.Replicas))
		templates["Deployment/"+d.Name] = d.Spec.Template.Spec
	}
	for _, s := range in.statefulSets {
		g.add("StatefulSet", s.Name, replicasStatus(s.Status.ReadyReplicas, s.Spec.Replicas))
		templates["StatefulSet/"+s.Name] = s.Spec.Template.Spec
	}
	for _, d := range in.daemonSets {
		want := d.Status.DesiredNumberScheduled
		g.add("DaemonSet", d.Name, replicasStatus(d.Status.NumberReady, &want))
		templates["DaemonSet/"+d.Name] = d.Spec.Template.Spec
	}
	for _, c := range in.cronJobs {
		status, health := "schedule "+c.Spec.Schedule, "ok"
		if c.Spec.Suspend != nil && *c.Spec.Suspend {
			status, health = "suspended", "none"
		} else if t := c.Status.LastScheduleTime; t != nil {
			status += ", last run " + ago(t.Time)
		}
		g.add("CronJob", c.Name, state{status, health})
		templates["CronJob/"+c.Name] = c.Spec.JobTemplate.Spec.Template.Spec
	}
	for _, rs := range in.replicaSets {
		// Old revisions of a Deployment stay at 0 replicas: noise on a map, so they are left out.
		if rs.Status.Replicas == 0 && (rs.Spec.Replicas == nil || *rs.Spec.Replicas == 0) {
			continue
		}
		id := g.add("ReplicaSet", rs.Name, replicasStatus(rs.Status.ReadyReplicas, rs.Spec.Replicas)).ID
		g.own(id, rs.OwnerReferences)
		if g.parent[id] == "" {
			templates[id] = rs.Spec.Template.Spec
		}
	}
	for _, j := range in.jobs {
		id := g.add("Job", j.Name, jobStatus(j)).ID
		g.own(id, j.OwnerReferences)
		if g.parent[id] == "" {
			templates[id] = j.Spec.Template.Spec
		}
	}

	// Pods, worst first, so a truncated map keeps the ones that matter.
	pods := append([]*corev1.Pod{}, in.pods...)
	sort.SliceStable(pods, func(i, j int) bool { return podRank(pods[i]) < podRank(pods[j]) })
	truncated := false
	for _, p := range pods {
		if len(g.nodes) >= mapMaxNodes {
			truncated = true
			break
		}
		status, healthy := podStatus(p)
		health := "ok"
		switch {
		case p.DeletionTimestamp != nil:
			health = "warn" // Terminating: on its way out
		case p.Status.Phase == corev1.PodPending && !healthy && !podHasBadWait(p):
			health = "warn" // starting: scheduling, pulling, init containers
		case !healthy:
			health = "bad"
		}
		id := g.add("Pod", p.Name, state{status, health}).ID
		g.own(id, p.OwnerReferences)
		if g.parent[id] == "" {
			templates[id] = p.Spec // a bare pod is its own workload
		}
	}

	// Services -> the workloads whose pods they select.
	for _, s := range in.services {
		sel := s.Spec.Selector
		targets := map[string]bool{}
		if len(sel) > 0 {
			match := labels.SelectorFromSet(sel)
			for _, p := range in.pods {
				if match.Matches(labels.Set(p.Labels)) && g.nodes["Pod/"+p.Name] != nil {
					targets[g.top("Pod/"+p.Name)] = true
				}
			}
		}
		status, health := string(s.Spec.Type), "ok"
		switch {
		case s.Spec.Type == corev1.ServiceTypeExternalName:
			status, health = "ExternalName "+s.Spec.ExternalName, "none"
		case len(sel) == 0:
			status, health = status+", no selector", "none" // endpoints managed by hand or by an operator
		case len(targets) == 0:
			status, health = status+", no pod matches its selector", "warn"
		}
		id := g.add("Service", s.Name, state{status, health}).ID
		for t := range targets {
			g.link(id, t)
		}
	}

	// Ingresses and HTTPRoutes -> Services. A backend that does not exist shows as missing.
	service := func(from, name string) {
		if name == "" {
			return
		}
		id := "Service/" + name
		if g.nodes[id] == nil {
			n := g.add("Service", name, state{"not found", "bad"})
			n.Missing = true
			if f := g.nodes[from]; f != nil && f.Health == "ok" {
				f.Health, f.Status = "warn", f.Status+", backend "+name+" not found"
			}
		}
		g.link(from, id)
	}
	for _, ing := range in.ingresses {
		hosts := []string{}
		for _, r := range ing.Spec.Rules {
			if r.Host != "" {
				hosts = append(hosts, r.Host)
			}
		}
		status := strings.Join(hosts, ", ")
		if status == "" {
			status = "any host"
		}
		id := g.add("Ingress", ing.Name, state{status, "ok"}).ID
		if b := ing.Spec.DefaultBackend; b != nil && b.Service != nil {
			service(id, b.Service.Name)
		}
		for _, r := range ing.Spec.Rules {
			if r.HTTP == nil {
				continue
			}
			for _, path := range r.HTTP.Paths {
				if path.Backend.Service != nil {
					service(id, path.Backend.Service.Name)
				}
			}
		}
	}
	for _, rt := range in.httpRoutes {
		hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
		status := strings.Join(hosts, ", ")
		if status == "" {
			status = "any host"
		}
		id := g.add("HTTPRoute", rt.GetName(), state{status, "ok"}).ID
		rules, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
		for _, r := range rules {
			rule, _ := r.(map[string]any)
			refs, _, _ := unstructured.NestedSlice(rule, "backendRefs")
			for _, b := range refs {
				ref, _ := b.(map[string]any)
				kind, _, _ := unstructured.NestedString(ref, "kind")
				group, _, _ := unstructured.NestedString(ref, "group")
				ns, _, _ := unstructured.NestedString(ref, "namespace")
				name, _, _ := unstructured.NestedString(ref, "name")
				if (kind == "" || kind == "Service") && group == "" && (ns == "" || ns == in.namespace) {
					service(id, name)
				}
			}
		}
	}

	// HPAs -> what they scale.
	for _, a := range in.hpas {
		lo := int32(1) // the API default
		if a.Spec.MinReplicas != nil {
			lo = *a.Spec.MinReplicas
		}
		status := fmt.Sprintf("%d replicas (min %d, max %d)", a.Status.CurrentReplicas, lo, a.Spec.MaxReplicas)
		id := g.add("HorizontalPodAutoscaler", a.Name, state{status, "ok"}).ID
		target := a.Spec.ScaleTargetRef.Kind + "/" + a.Spec.ScaleTargetRef.Name
		if g.nodes[target] != nil {
			g.link(id, target)
		}
	}

	// What each workload's pod template uses: ConfigMaps, Secrets (name only), PVCs.
	pvcs := map[string]corev1.PersistentVolumeClaim{}
	for _, c := range in.pvcs {
		pvcs[c.Name] = c
	}
	ids := make([]string, 0, len(templates))
	for id := range templates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, owner := range ids {
		if g.nodes[owner] == nil {
			continue
		}
		for _, r := range specRefs(templates[owner]) {
			var n *MapNode
			switch r.kind {
			case "ConfigMap":
				if in.configMaps[r.name] {
					n = g.add("ConfigMap", r.name, state{"", "none"})
				} else {
					health := "bad" // pods using it cannot start (CreateContainerConfigError)
					if r.optional {
						health = "none"
					}
					n = g.add("ConfigMap", r.name, state{"not found", health})
					n.Missing = true
				}
			case "Secret":
				n = g.add("Secret", r.name, state{"name only: Owl never reads Secrets", "none"})
				n.Missing = true // nothing to open
			case "PersistentVolumeClaim":
				c, ok := pvcs[r.name]
				if !ok {
					n = g.add("PersistentVolumeClaim", r.name, state{"not found", "bad"})
					n.Missing = true
					break
				}
				n = g.add("PersistentVolumeClaim", c.Name, pvcStatus(c))
				if pv := in.pvs[c.Spec.VolumeName]; pv != nil {
					health := "ok"
					if pv.Status.Phase == corev1.VolumeFailed {
						health = "bad"
					}
					status := string(pv.Status.Phase)
					if pv.Spec.StorageClassName != "" {
						status += ", " + pv.Spec.StorageClassName
					}
					g.link(n.ID, g.add("PersistentVolume", pv.Name, state{status, health}).ID)
				}
			}
			g.link(owner, n.ID)
		}
	}
	// PVCs that no workload uses still belong on the map (a volume left behind).
	for _, c := range in.pvcs {
		g.add("PersistentVolumeClaim", c.Name, pvcStatus(c))
	}

	out := &ResourceMap{Namespace: in.namespace, Nodes: []MapNode{}, Edges: []MapEdge{}, Truncated: truncated, Notes: in.notes}
	for _, n := range g.nodes {
		out.Nodes = append(out.Nodes, *n)
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	for e := range g.edges {
		if g.nodes[e.From] != nil && g.nodes[e.To] != nil {
			out.Edges = append(out.Edges, e)
		}
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		return out.Edges[i].From+">"+out.Edges[i].To < out.Edges[j].From+">"+out.Edges[j].To
	})
	return out
}

// replicasStatus: "2/3 ready", ok when all are ready, bad when none, warn in between.
func replicasStatus(ready int32, want *int32) state {
	w := int32(1) // the API default
	if want != nil {
		w = *want
	}
	switch {
	case w == 0:
		return state{"scaled to 0", "none"}
	case ready >= w:
		return state{fmt.Sprintf("%d/%d ready", ready, w), "ok"}
	case ready == 0:
		return state{fmt.Sprintf("0/%d ready", w), "bad"}
	default:
		return state{fmt.Sprintf("%d/%d ready", ready, w), "warn"}
	}
}

func jobStatus(j batchv1.Job) state {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return state{"Complete", "ok"}
		case batchv1.JobFailed:
			return state{"Failed: " + c.Reason, "bad"}
		}
	}
	if j.Status.Active > 0 {
		return state{fmt.Sprintf("%d running", j.Status.Active), "ok"}
	}
	return state{"waiting", "warn"}
}

func pvcStatus(c corev1.PersistentVolumeClaim) state {
	status := string(c.Status.Phase)
	if q, ok := c.Status.Capacity[corev1.ResourceStorage]; ok {
		status += ", " + q.String()
	}
	switch c.Status.Phase {
	case corev1.ClaimBound:
		return state{status, "ok"}
	case corev1.ClaimLost:
		return state{status, "bad"}
	default:
		return state{status, "warn"}
	}
}

// podRank sorts failing pods first, then starting ones, then the rest.
func podRank(p *corev1.Pod) int {
	_, healthy := podStatus(p)
	switch {
	case !healthy && p.Status.Phase != corev1.PodPending:
		return 0
	case p.Status.Phase == corev1.PodPending:
		return 1
	default:
		return 2
	}
}

// podHasBadWait: a pending pod stuck on an error (ImagePullBackOff...) is bad, not just starting.
func podHasBadWait(p *corev1.Pod) bool {
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil && badWaiting[w.Reason] {
			return true
		}
	}
	return false
}

type specRef struct {
	kind, name string
	optional   bool
}

// specRefs lists the ConfigMaps, Secrets and PVCs a pod spec uses: volumes, envFrom and env.
func specRefs(spec corev1.PodSpec) []specRef {
	var out []specRef
	add := func(kind, name string, optional *bool) {
		if name != "" {
			out = append(out, specRef{kind, name, optional != nil && *optional})
		}
	}
	for _, v := range spec.Volumes {
		switch {
		case v.ConfigMap != nil:
			add("ConfigMap", v.ConfigMap.Name, v.ConfigMap.Optional)
		case v.Secret != nil:
			add("Secret", v.Secret.SecretName, v.Secret.Optional)
		case v.PersistentVolumeClaim != nil:
			add("PersistentVolumeClaim", v.PersistentVolumeClaim.ClaimName, nil)
		case v.Projected != nil:
			for _, s := range v.Projected.Sources {
				if s.ConfigMap != nil {
					add("ConfigMap", s.ConfigMap.Name, s.ConfigMap.Optional)
				}
				if s.Secret != nil {
					add("Secret", s.Secret.Name, s.Secret.Optional)
				}
			}
		}
	}
	containers := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	for _, c := range containers {
		for _, e := range c.EnvFrom {
			if e.ConfigMapRef != nil {
				add("ConfigMap", e.ConfigMapRef.Name, e.ConfigMapRef.Optional)
			}
			if e.SecretRef != nil {
				add("Secret", e.SecretRef.Name, e.SecretRef.Optional)
			}
		}
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				continue
			}
			if r := e.ValueFrom.ConfigMapKeyRef; r != nil {
				add("ConfigMap", r.Name, r.Optional)
			}
			if r := e.ValueFrom.SecretKeyRef; r != nil {
				add("Secret", r.Name, r.Optional)
			}
		}
	}
	return out
}

// ago: "5m ago", "3h ago", "2d ago".
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
