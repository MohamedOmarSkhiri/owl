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
	// Pods only: when a container last restarted, so the app can tell an old restart from a crash now.
	LastRestart *time.Time `json:"lastRestart,omitempty"`
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

// logs returns one container's log lines.
//   - first call (no "since"): the last N lines (default 200, max 2000), or "tail=all" for the whole log
//   - live calls ("since" = the "last" value of the previous answer): only the lines written after it
//
// "last" is the timestamp of the newest line; the web app sends it back on the next call.
func (h *requestHandler) logs(ctx context.Context, p map[string]string) (map[string]any, error) {
	if p["namespace"] == "" || p["name"] == "" {
		return nil, apierrors.NewBadRequest("namespace and name are required")
	}
	limit := int64(logLimitBytes)
	if p["tail"] == "all" {
		limit = downloadLimitBytes // download: the whole log, with a larger cap
	}
	opts := &corev1.PodLogOptions{
		Container:  p["container"],
		Timestamps: true,   // each line starts with its RFC 3339 time: that is our cursor
		LimitBytes: &limit, // a noisy pod cannot flood the relay
	}
	var since time.Time
	if s := p["since"]; s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, apierrors.NewBadRequest("since must be an RFC 3339 time")
		}
		since = t
		st := metav1.NewTime(t) // the API only keeps whole seconds, splitLog drops what we already have
		opts.SinceTime = &st
	} else if p["tail"] != "all" {
		tail := int64(200)
		if v, err := strconv.ParseInt(p["tail"], 10, 64); err == nil && v > 0 {
			tail = min(v, 2000)
		}
		opts.TailLines = &tail
	}
	raw, err := h.cs.CoreV1().Pods(p["namespace"]).GetLogs(p["name"], opts).DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	lines, last := splitLog(string(raw), since)
	if last == "" {
		// Nothing new: keep the caller's cursor, or start from now (agent clock, close to the node's).
		last = p["since"]
		if last == "" {
			last = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
	return map[string]any{"lines": lines, "last": last}, nil
}

const (
	logLimitBytes      = 1 << 20 // 1 MiB per answer
	downloadLimitBytes = 4 << 20 // 4 MiB for "tail=all" (the relay accepts answers up to 8 MiB)
)

// splitLog removes the timestamp in front of each line and keeps only lines newer than since
// (zero since = keep all). It returns the text lines and the timestamp of the newest one.
func splitLog(raw string, since time.Time) (lines []string, last string) {
	lines = []string{}
	for _, l := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if l == "" {
			continue
		}
		ts, text, _ := strings.Cut(l, " ")
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			lines = append(lines, l) // no timestamp (should not happen): keep the line as is
			continue
		}
		if !since.IsZero() && !t.After(since) {
			continue // already sent in a previous answer
		}
		lines = append(lines, text)
		last = ts
	}
	return lines, last
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
			it.LastRestart = lastRestart(&pod)
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
	if pod.Status.Phase == corev1.PodSucceeded {
		reason = "Completed" // the word kubectl uses for a pod that finished its work
	}
	if pod.DeletionTimestamp != nil {
		reason = "Terminating"
	}
	if pod.Status.Phase == corev1.PodFailed {
		healthy = false
	}
	return fmt.Sprintf("%s · %d/%d · %d restarts", reason, ready, len(pod.Spec.Containers), restarts), healthy
}

// lastRestart is when the most recent container restart happened: the end of the previous run
// (lastState.terminated.finishedAt) of the container that restarted last. nil = never restarted.
func lastRestart(pod *corev1.Pod) *time.Time {
	var last *time.Time
	for _, cs := range pod.Status.ContainerStatuses {
		t := cs.LastTerminationState.Terminated
		if cs.RestartCount == 0 || t == nil || t.FinishedAt.IsZero() {
			continue
		}
		if last == nil || t.FinishedAt.After(*last) {
			at := t.FinishedAt.UTC()
			last = &at
		}
	}
	return last
}
