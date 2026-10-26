package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ownedPod builds a pod whose controller is kind/owner.
func ownedPod(kind, owner string, labels map[string]string) *corev1.Pod {
	yes := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "p", Namespace: "default", Labels: labels,
		OwnerReferences: []metav1.OwnerReference{{Kind: kind, Name: owner, Controller: &yes}},
	}}
}

func TestWorkloadOf(t *testing.T) {
	cases := []struct {
		pod        *corev1.Pod
		kind, name string
	}{
		{ownedPod("ReplicaSet", "web-7c9d8f6b5", map[string]string{"pod-template-hash": "7c9d8f6b5"}), "Deployment", "web"},
		{ownedPod("ReplicaSet", "lonely-rs", nil), "ReplicaSet", "lonely-rs"},
		{ownedPod("StatefulSet", "db", nil), "StatefulSet", "db"},
		{ownedPod("DaemonSet", "node-exporter", nil), "DaemonSet", "node-exporter"},
		{&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare"}}, "Pod", "bare"},
	}
	for _, c := range cases {
		if k, n := workloadOf(c.pod); k != c.kind || n != c.name {
			t.Errorf("workloadOf = %s/%s, want %s/%s", k, n, c.kind, c.name)
		}
	}
}

func TestPodResourcesSumsAndUnlimited(t *testing.T) {
	q := resource.MustParse
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: q("100m"), corev1.ResourceMemory: q("64Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: q("500m"), corev1.ResourceMemory: q("128Mi")},
		}},
		{Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: q("50m"), corev1.ResourceMemory: q("32Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: q("64Mi")}, // no CPU limit
		}},
	}}}
	r := podResources(pod)
	if r.cpuRequest != 150 || r.memRequest != 96*1024*1024 {
		t.Fatalf("requests = %dm / %d bytes", r.cpuRequest, r.memRequest)
	}
	if r.cpuLimit != 0 {
		t.Fatalf("cpu limit = %d, want 0 because one container is unlimited", r.cpuLimit)
	}
	if r.memLimit != 192*1024*1024 {
		t.Fatalf("mem limit = %d", r.memLimit)
	}
}

func TestParseMetrics(t *testing.T) {
	node := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "node-1"},
		"usage":    map[string]any{"cpu": "250000000n", "memory": "1Gi"}, // 250000000n = 250m
	}}
	pod := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "web", "namespace": "default"},
		"containers": []any{
			map[string]any{"name": "a", "usage": map[string]any{"cpu": "10m", "memory": "20Mi"}},
			map[string]any{"name": "b", "usage": map[string]any{"cpu": "5m", "memory": "10Mi"}},
		},
	}}
	got := parseMetrics([]unstructured.Unstructured{node, pod})
	if s := got["node-1"]; s.cpu != 250 || s.mem != 1<<30 {
		t.Fatalf("node = %+v", s)
	}
	if s := got["default/web"]; s.cpu != 15 || s.mem != 30*1024*1024 {
		t.Fatalf("pod = %+v", s)
	}
}
