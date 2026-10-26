package main

// Answers live-data requests from the relay (resource kinds, lists, objects, pod logs).
// The agent long-polls the relay for requests, so the cluster still only makes outbound calls.
// Everything here is read-only: discovery, list, get and pod logs. Secrets are never served.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
)

type agentRequest struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"`
	Params map[string]string `json:"params"`
}

type agentResponse struct {
	Status int             `json:"status"`
	Error  string          `json:"error,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// ResourceKind is one listable API resource, e.g. apps/v1 deployments.
type ResourceKind struct {
	Group      string `json:"group"`
	Version    string `json:"version"`
	Resource   string `json:"resource"`
	Kind       string `json:"kind"`
	Namespaced bool   `json:"namespaced"`
}

// ResourceItem is one object in a list, summarised for a phone screen.
type ResourceItem struct {
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace,omitempty"`
	Created    time.Time `json:"created"`
	Status     string    `json:"status,omitempty"`
	Healthy    bool      `json:"healthy"`
	Containers []string  `json:"containers,omitempty"`
}

var errSecretsBlocked = errors.New("secrets are not exposed by the KubePulse agent")

type requestHandler struct {
	cs    kubernetes.Interface
	dyn   dynamic.Interface
	pods  listerscorev1.PodLister  // informer caches: requests, limits, owners, node placement
	nodes listerscorev1.NodeLister // without extra API calls
}

// serveRequests long-polls the relay and answers each request in its own goroutine.
func serveRequests(ctx context.Context, cfg *rest.Config, cs kubernetes.Interface,
	pods listerscorev1.PodLister, nodes listerscorev1.NodeLister, relay *relayClient, cluster string) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Printf("live requests disabled: %v", err)
		return
	}
	h := &requestHandler{cs: cs, dyn: dyn, pods: pods, nodes: nodes}
	poll := &http.Client{Timeout: 40 * time.Second} // relay holds a poll up to 25s
	pollURL := relay.base + "/v1/agent/requests?cluster=" + url.QueryEscape(cluster)

	for ctx.Err() == nil {
		req, err := nextRequest(ctx, poll, pollURL, relay.token)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("poll relay failed: %v (retrying in 3s)", err)
				select {
				case <-ctx.Done():
				case <-time.After(3 * time.Second):
				}
			}
			continue
		}
		if req == nil {
			continue // idle poll, ask again
		}
		go func(req *agentRequest) {
			resp := h.handle(ctx, req)
			if err := relay.post("/v1/agent/responses/"+url.PathEscape(req.ID), resp); err != nil {
				log.Printf("answer %s failed: %v", req.Kind, err)
			}
		}(req)
	}
}

func nextRequest(ctx context.Context, client *http.Client, pollURL, token string) (*agentRequest, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var req agentRequest
		if err := json.NewDecoder(res.Body).Decode(&req); err != nil {
			return nil, err
		}
		return &req, nil
	default:
		return nil, fmt.Errorf("relay answered HTTP %d", res.StatusCode)
	}
}

func (h *requestHandler) handle(parent context.Context, req *agentRequest) agentResponse {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	p := req.Params
	if p == nil {
		p = map[string]string{}
	}

	var (
		out any
		err error
	)
	switch req.Kind {
	case "kinds":
		out, err = h.kinds()
	case "list":
		out, err = h.list(ctx, p)
	case "get":
		out, err = h.get(ctx, p)
	case "logs":
		out, err = h.logs(ctx, p)
	case "usage":
		out, err = h.usage(ctx, p)
	default:
		return agentResponse{Status: http.StatusBadRequest, Error: "unknown request kind " + req.Kind}
	}
	if err != nil {
		return agentResponse{Status: statusFor(err), Error: err.Error()}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return agentResponse{Status: http.StatusInternalServerError, Error: err.Error()}
	}
	return agentResponse{Status: http.StatusOK, Body: body}
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, errSecretsBlocked), apierrors.IsForbidden(err):
		return http.StatusForbidden
	case apierrors.IsNotFound(err):
		return http.StatusNotFound
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// kinds lists every resource type that supports "list", like `kubectl api-resources`.
func (h *requestHandler) kinds() ([]ResourceKind, error) {
	lists, err := h.cs.Discovery().ServerPreferredResources()
	if len(lists) == 0 && err != nil {
		return nil, err // partial failures (e.g. a broken metrics API) are tolerated
	}
	out := []ResourceKind{}
	for _, l := range lists {
		gv, perr := schema.ParseGroupVersion(l.GroupVersion)
		if perr != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") || !hasVerb(r.Verbs, "list") {
				continue // subresources like pods/log, or not listable
			}
			if gv.Group == "" && r.Name == "secrets" {
				continue
			}
			out = append(out, ResourceKind{Group: gv.Group, Version: gv.Version, Resource: r.Name, Kind: r.Kind, Namespaced: r.Namespaced})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group // core ("") first
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

func hasVerb(verbs []string, v string) bool {
	for _, x := range verbs {
		if x == v {
			return true
		}
	}
	return false
}

func gvrFrom(p map[string]string) (schema.GroupVersionResource, error) {
	gvr := schema.GroupVersionResource{Group: p["group"], Version: p["version"], Resource: p["resource"]}
	if gvr.Version == "" || gvr.Resource == "" {
		return gvr, apierrors.NewBadRequest("version and resource are required")
	}
	if gvr.Group == "" && gvr.Resource == "secrets" {
		return gvr, errSecretsBlocked
	}
	return gvr, nil
}

func (h *requestHandler) resourceClient(gvr schema.GroupVersionResource, namespace string) dynamic.ResourceInterface {
	if namespace != "" {
		return h.dyn.Resource(gvr).Namespace(namespace)
	}
	return h.dyn.Resource(gvr)
}

// list returns up to 500 objects of one kind, summarised (all namespaces unless one is given).
func (h *requestHandler) list(ctx context.Context, p map[string]string) ([]ResourceItem, error) {
	gvr, err := gvrFrom(p)
	if err != nil {
		return nil, err
	}
	ul, err := h.resourceClient(gvr, p["namespace"]).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	out := make([]ResourceItem, 0, len(ul.Items))
	for i := range ul.Items {
		out = append(out, summarize(gvr, &ul.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// get returns one full object (managedFields removed), for the JSON detail view.
func (h *requestHandler) get(ctx context.Context, p map[string]string) (map[string]any, error) {
	gvr, err := gvrFrom(p)
	if err != nil {
		return nil, err
	}
	if p["name"] == "" {
		return nil, apierrors.NewBadRequest("name is required")
	}
	u, err := h.resourceClient(gvr, p["namespace"]).Get(ctx, p["name"], metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")
	return u.Object, nil
}

// logs returns the last N lines of one container (default 200, max 2000).
func (h *requestHandler) logs(ctx context.Context, p map[string]string) (map[string]any, error) {
	if p["namespace"] == "" || p["name"] == "" {
		return nil, apierrors.NewBadRequest("namespace and name are required")
	}
	tail := int64(200)
	if v, err := strconv.ParseInt(p["tail"], 10, 64); err == nil && v > 0 {
		tail = min(v, 2000)
	}
	opts := &corev1.PodLogOptions{TailLines: &tail, Container: p["container"]}
	raw, err := h.cs.CoreV1().Pods(p["namespace"]).GetLogs(p["name"], opts).DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(raw), "\n")
	lines := []string{}
	if text != "" {
		lines = strings.Split(text, "\n")
	}
	return map[string]any{"lines": lines}, nil
}

// summarize turns an object into a one-line status for the list screens.
func summarize(gvr schema.GroupVersionResource, u *unstructured.Unstructured) ResourceItem {
	it := ResourceItem{
		Name:      u.GetName(),
		Namespace: u.GetNamespace(),
		Created:   u.GetCreationTimestamp().Time,
		Healthy:   true,
	}
	obj := u.Object

	switch {
	case gvr.Group == "" && gvr.Resource == "pods":
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &pod); err == nil {
			it.Status, it.Healthy = podStatus(&pod)
			for _, c := range pod.Spec.Containers {
				it.Containers = append(it.Containers, c.Name)
			}
		}

	case gvr.Group == "" && gvr.Resource == "nodes":
		var node corev1.Node
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &node); err == nil {
			it.Healthy = nodeReady(&node)
			it.Status = "NotReady"
			if it.Healthy {
				it.Status = "Ready"
			}
			it.Status += " · " + node.Status.NodeInfo.KubeletVersion
		}

	case gvr.Group == "apps" && (gvr.Resource == "deployments" || gvr.Resource == "statefulsets" || gvr.Resource == "replicasets"):
		want, _, _ := unstructured.NestedInt64(obj, "spec", "replicas")
		ready, _, _ := unstructured.NestedInt64(obj, "status", "readyReplicas")
		it.Status = fmt.Sprintf("%d/%d ready", ready, want)
		it.Healthy = ready >= want

	case gvr.Group == "apps" && gvr.Resource == "daemonsets":
		want, _, _ := unstructured.NestedInt64(obj, "status", "desiredNumberScheduled")
		ready, _, _ := unstructured.NestedInt64(obj, "status", "numberReady")
		it.Status = fmt.Sprintf("%d/%d ready", ready, want)
		it.Healthy = ready >= want

	case gvr.Group == "" && gvr.Resource == "services":
		typ, _, _ := unstructured.NestedString(obj, "spec", "type")
		ip, _, _ := unstructured.NestedString(obj, "spec", "clusterIP")
		it.Status = typ + " · " + ip

	case gvr.Group == "" && gvr.Resource == "persistentvolumeclaims":
		phase, _, _ := unstructured.NestedString(obj, "status", "phase")
		it.Status = phase
		it.Healthy = phase == "Bound"

	case gvr.Group == "batch" && gvr.Resource == "jobs":
		succeeded, _, _ := unstructured.NestedInt64(obj, "status", "succeeded")
		failed, _, _ := unstructured.NestedInt64(obj, "status", "failed")
		it.Status = fmt.Sprintf("%d succeeded · %d failed", succeeded, failed)
		it.Healthy = failed == 0
	}
	return it
}

// podStatus mirrors the STATUS column of `kubectl get pods`, plus ready count and restarts.
func podStatus(pod *corev1.Pod) (string, bool) {
	reason := string(pod.Status.Phase)
	if pod.Status.Reason != "" {
		reason = pod.Status.Reason
	}
	ready, restarts := 0, int32(0)
	healthy := pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodSucceeded
	for _, cs := range pod.Status.ContainerStatuses {
		restarts += cs.RestartCount
		if cs.Ready {
			ready++
		}
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			reason = w.Reason
			if badWaiting[w.Reason] {
				healthy = false
			}
		} else if t := cs.State.Terminated; t != nil && t.Reason != "" && pod.Status.Phase != corev1.PodSucceeded {
			reason = t.Reason
		}
	}
	if pod.DeletionTimestamp != nil {
		reason = "Terminating"
	}
	if pod.Status.Phase == corev1.PodFailed {
		healthy = false
	}
	return fmt.Sprintf("%s · %d/%d · %d restarts", reason, ready, len(pod.Spec.Containers), restarts), healthy
}
