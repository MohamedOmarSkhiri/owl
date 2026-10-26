package main

// Resource usage: live CPU and memory from metrics-server (metrics.k8s.io, the API behind
// `kubectl top`), combined with requests, limits and node capacity from the informer caches.
// If metrics-server is missing, the report still carries requests, limits and capacity.

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The metrics API is read with the dynamic client (already used for Explore),
// so no extra Go module is needed.
var (
	nodeMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	podMetricsGVR  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
)

const noMetricsServer = "metrics-server is not installed, so live CPU and memory are not available. " +
	"K3s includes it; elsewhere: kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"

// UsageReport is the answer to a "usage" request. CPU is in millicores, memory in bytes.
type UsageReport struct {
	Metrics bool        `json:"metrics"`           // false when metrics-server gave no data
	Message string      `json:"message,omitempty"` // why Metrics is false
	Nodes   []NodeUsage `json:"nodes,omitempty"`
	Pods    []PodUsage  `json:"pods,omitempty"`
}

type NodeUsage struct {
	Name           string `json:"name"`
	Ready          bool   `json:"ready"`
	Pods           int    `json:"pods"`
	CPUUsage       int64  `json:"cpuUsage"`
	MemUsage       int64  `json:"memUsage"`
	CPUAllocatable int64  `json:"cpuAllocatable"` // what pods may use on this node
	MemAllocatable int64  `json:"memAllocatable"`
	CPURequests    int64  `json:"cpuRequests"` // sum of the requests of the pods on it
	MemRequests    int64  `json:"memRequests"`
}

type PodUsage struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Node         string `json:"node"`
	WorkloadKind string `json:"workloadKind"` // Deployment, StatefulSet, DaemonSet, Job, Pod...
	WorkloadName string `json:"workloadName"`
	Sampled      bool   `json:"sampled"` // metrics-server has a sample for this pod
	CPUUsage     int64  `json:"cpuUsage"`
	MemUsage     int64  `json:"memUsage"`
	CPURequest   int64  `json:"cpuRequest"`
	CPULimit     int64  `json:"cpuLimit"` // 0 = at least one container has no limit
	MemRequest   int64  `json:"memRequest"`
	MemLimit     int64  `json:"memLimit"`
}

// usage answers {scope: nodes|pods, namespace?}.
func (h *requestHandler) usage(ctx context.Context, p map[string]string) (any, error) {
	switch p["scope"] {
	case "nodes":
		return h.nodeUsage(ctx)
	case "pods", "":
		return h.podUsage(ctx, p["namespace"])
	default:
		return nil, apierrors.NewBadRequest("scope must be nodes or pods")
	}
}

func (h *requestHandler) nodeUsage(ctx context.Context) (*UsageReport, error) {
	// Nodes and pods come from the informer caches: no extra API calls.
	nodes, err := h.nodes.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	pods, err := h.pods.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	// Live usage. If it fails we still answer, with Metrics=false and the reason.
	report := &UsageReport{}
	samples, err := h.metrics(ctx, nodeMetricsGVR, "")
	if err != nil {
		report.Message = err.Error()
	} else {
		report.Metrics = true
	}

	byNode := map[string]*NodeUsage{}
	for _, n := range nodes {
		u := &NodeUsage{
			Name:           n.Name,
			Ready:          nodeReady(n),
			CPUAllocatable: n.Status.Allocatable.Cpu().MilliValue(),
			MemAllocatable: n.Status.Allocatable.Memory().Value(),
		}
		if s, ok := samples[n.Name]; ok {
			u.CPUUsage, u.MemUsage = s.cpu, s.mem
		}
		byNode[n.Name] = u
	}
	// Add up the requests of the pods running on each node.
	for _, pod := range pods {
		u, ok := byNode[pod.Spec.NodeName]
		if !ok || podFinished(pod) {
			continue
		}
		r := podResources(pod)
		u.Pods++
		u.CPURequests += r.cpuRequest
		u.MemRequests += r.memRequest
	}
	for _, u := range byNode {
		report.Nodes = append(report.Nodes, *u)
	}
	sort.Slice(report.Nodes, func(i, j int) bool { return report.Nodes[i].Name < report.Nodes[j].Name })
	return report, nil
}

func (h *requestHandler) podUsage(ctx context.Context, namespace string) (*UsageReport, error) {
	var (
		pods []*corev1.Pod
		err  error
	)
	if namespace != "" {
		pods, err = h.pods.Pods(namespace).List(labels.Everything())
	} else {
		pods, err = h.pods.List(labels.Everything())
	}
	if err != nil {
		return nil, err
	}

	report := &UsageReport{}
	samples, err := h.metrics(ctx, podMetricsGVR, namespace)
	if err != nil {
		report.Message = err.Error()
	} else {
		report.Metrics = true
	}

	for _, pod := range pods {
		if podFinished(pod) {
			continue // completed Jobs use nothing
		}
		r := podResources(pod)
		kind, name := workloadOf(pod)
		u := PodUsage{
			Name: pod.Name, Namespace: pod.Namespace, Node: pod.Spec.NodeName,
			WorkloadKind: kind, WorkloadName: name,
			CPURequest: r.cpuRequest, CPULimit: r.cpuLimit, MemRequest: r.memRequest, MemLimit: r.memLimit,
		}
		if s, ok := samples[pod.Namespace+"/"+pod.Name]; ok {
			u.Sampled, u.CPUUsage, u.MemUsage = true, s.cpu, s.mem
		}
		report.Pods = append(report.Pods, u)
	}
	sort.Slice(report.Pods, func(i, j int) bool { return report.Pods[i].CPUUsage > report.Pods[j].CPUUsage })
	return report, nil
}

type sample struct{ cpu, mem int64 }

// metrics lists node or pod metrics; keys are "node" or "namespace/pod".
// A missing or unavailable metrics API is turned into a readable message.
func (h *requestHandler) metrics(ctx context.Context, gvr schema.GroupVersionResource, namespace string) (map[string]sample, error) {
	var (
		list *unstructured.UnstructuredList
		err  error
	)
	if namespace != "" {
		list, err = h.dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = h.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		// 404: metrics.k8s.io does not exist; 503: metrics-server is down.
		if apierrors.IsNotFound(err) || apierrors.IsServiceUnavailable(err) {
			return nil, errNoMetrics
		}
		return nil, err
	}
	return parseMetrics(list.Items), nil
}

type metricsError string

func (e metricsError) Error() string { return string(e) }

var errNoMetrics error = metricsError(noMetricsServer)

// parseMetrics reads NodeMetrics (usage) and PodMetrics (containers[].usage) items.
// Values are Kubernetes quantities like "250m", "123456789n" or "512Mi".
func parseMetrics(items []unstructured.Unstructured) map[string]sample {
	out := make(map[string]sample, len(items))
	for _, it := range items {
		key := it.GetName()
		if ns := it.GetNamespace(); ns != "" {
			key = ns + "/" + key
		}
		var s sample
		if usage, ok, _ := unstructured.NestedStringMap(it.Object, "usage"); ok { // node
			s.cpu, s.mem = milli(usage["cpu"]), bytesOf(usage["memory"])
		}
		containers, _, _ := unstructured.NestedSlice(it.Object, "containers") // pod: sum containers
		for _, c := range containers {
			cm, _ := c.(map[string]any)
			usage, _, _ := unstructured.NestedStringMap(cm, "usage")
			s.cpu += milli(usage["cpu"])
			s.mem += bytesOf(usage["memory"])
		}
		out[key] = s
	}
	return out
}

// milli turns "250m" or "1" (core) into millicores.
func milli(q string) int64 {
	v, err := resource.ParseQuantity(q)
	if err != nil {
		return 0
	}
	return v.MilliValue()
}

// bytesOf turns "512Mi" or "1Gi" into bytes.
func bytesOf(q string) int64 {
	v, err := resource.ParseQuantity(q)
	if err != nil {
		return 0
	}
	return v.Value()
}

type podRes struct{ cpuRequest, cpuLimit, memRequest, memLimit int64 }

// podResources sums requests and limits over the pod's containers. A limit is reported as 0
// (unlimited) when any container has none, because a partial sum would be misleading.
func podResources(pod *corev1.Pod) podRes {
	var r podRes
	cpuAll, memAll := true, true
	for _, c := range pod.Spec.Containers {
		r.cpuRequest += c.Resources.Requests.Cpu().MilliValue()
		r.memRequest += c.Resources.Requests.Memory().Value()
		if l, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			r.cpuLimit += l.MilliValue()
		} else {
			cpuAll = false
		}
		if l, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			r.memLimit += l.Value()
		} else {
			memAll = false
		}
	}
	if !cpuAll {
		r.cpuLimit = 0
	}
	if !memAll {
		r.memLimit = 0
	}
	return r
}

// workloadOf names the object that owns the pod. Deployment pods are owned by a ReplicaSet
// named <deployment>-<pod-template-hash>, so the hash is stripped to get the Deployment.
func workloadOf(pod *corev1.Pod) (kind, name string) {
	ref := metav1.GetControllerOf(pod)
	if ref == nil {
		return "Pod", pod.Name
	}
	if ref.Kind == "ReplicaSet" {
		if hash := pod.Labels["pod-template-hash"]; hash != "" && strings.HasSuffix(ref.Name, "-"+hash) {
			return "Deployment", strings.TrimSuffix(ref.Name, "-"+hash)
		}
	}
	return ref.Kind, ref.Name
}

func podFinished(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}
