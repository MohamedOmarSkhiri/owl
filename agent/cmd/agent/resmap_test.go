package main

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func ptr[T any](v T) *T { return &v }

func owned(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: ptr(true)}}
}

// shop: Ingress -> Service -> Deployment -> ReplicaSet -> 2 Pods, using a ConfigMap, a Secret
// and a PVC; an HTTPRoute to a Service that does not exist; an old ReplicaSet at 0.
func shopInput() mapInput {
	template := corev1.PodSpec{
		Volumes: []corev1.Volume{
			{Name: "conf", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "api-conf"}}}},
			{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "api-data"}}},
		},
		Containers: []corev1.Container{{
			Name: "api",
			EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "api-db"}}}},
			Env: []corev1.EnvVar{{Name: "FLAG", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "flags"}, Key: "x"}}}},
		}},
	}
	labels := map[string]string{"app": "api"}
	running := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, OwnerReferences: owned("ReplicaSet", "api-7c9d")},
			Spec:       template,
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
				{Name: "api", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
		}
	}
	crashing := running("api-7c9d-b")
	crashing.Status.ContainerStatuses[0].Ready = false
	crashing.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}

	route := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "api-route"},
		"spec": map[string]any{
			"hostnames": []any{"api.example.com"},
			"rules":     []any{map[string]any{"backendRefs": []any{map[string]any{"name": "api-v2", "port": int64(80)}}}},
		},
	}}

	return mapInput{
		namespace:   "shop",
		pods:        []*corev1.Pod{running("api-7c9d-a"), crashing},
		deployments: []appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "api"}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(2)), Template: corev1.PodTemplateSpec{Spec: template}}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}},
		replicaSets: []appsv1.ReplicaSet{
			{ObjectMeta: metav1.ObjectMeta{Name: "api-7c9d", OwnerReferences: owned("Deployment", "api")}, Spec: appsv1.ReplicaSetSpec{Replicas: ptr(int32(2))}, Status: appsv1.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 1}},
			{ObjectMeta: metav1.ObjectMeta{Name: "api-old1", OwnerReferences: owned("Deployment", "api")}, Spec: appsv1.ReplicaSetSpec{Replicas: ptr(int32(0))}},
		},
		services: []corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "api"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: labels}}},
		ingresses: []networkingv1.Ingress{{ObjectMeta: metav1.ObjectMeta{Name: "api"}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: "shop.example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
				Path: "/", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api"}}}}}},
		}}}}},
		httpRoutes: []unstructured.Unstructured{route},
		hpas: []autoscalingv2.HorizontalPodAutoscaler{{ObjectMeta: metav1.ObjectMeta{Name: "api"}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api"}, MaxReplicas: 5}}},
		configMaps: map[string]bool{"api-conf": true}, // "flags" is missing
		pvcs: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "api-data"},
			Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-123"}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}},
		pvs: map[string]*corev1.PersistentVolume{"pvc-123": {ObjectMeta: metav1.ObjectMeta{Name: "pvc-123"},
			Spec: corev1.PersistentVolumeSpec{StorageClassName: "local-path"}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}},
	}
}

func TestBuildMapEdges(t *testing.T) {
	m := buildMap(shopInput())
	edges := map[string]bool{}
	for _, e := range m.Edges {
		edges[e.From+" > "+e.To] = true
	}
	for _, want := range []string{
		"Ingress/api > Service/api",
		"Service/api > Deployment/api", // the Service points at the workload, not at each pod
		"Deployment/api > ReplicaSet/api-7c9d",
		"ReplicaSet/api-7c9d > Pod/api-7c9d-a",
		"ReplicaSet/api-7c9d > Pod/api-7c9d-b",
		"Deployment/api > ConfigMap/api-conf",
		"Deployment/api > ConfigMap/flags",
		"Deployment/api > Secret/api-db",
		"Deployment/api > PersistentVolumeClaim/api-data",
		"PersistentVolumeClaim/api-data > PersistentVolume/pvc-123",
		"HorizontalPodAutoscaler/api > Deployment/api",
		"HTTPRoute/api-route > Service/api-v2",
	} {
		if !edges[want] {
			t.Errorf("missing edge %s", want)
		}
	}
	if len(edges) != 12 {
		t.Errorf("%d edges, want 12: %v", len(edges), m.Edges)
	}
}

func TestBuildMapHealth(t *testing.T) {
	nodes := map[string]MapNode{}
	for _, n := range buildMap(shopInput()).Nodes {
		nodes[n.ID] = n
	}
	check := func(id, health string) {
		t.Helper()
		n, ok := nodes[id]
		if !ok {
			t.Errorf("%s not on the map", id)
			return
		}
		if n.Health != health {
			t.Errorf("%s health %s (%s), want %s", id, n.Health, n.Status, health)
		}
	}
	check("Deployment/api", "warn") // 1/2 ready
	check("Pod/api-7c9d-a", "ok")
	check("Pod/api-7c9d-b", "bad")  // CrashLoopBackOff
	check("ConfigMap/flags", "bad") // referenced, not found: the pods cannot start
	check("ConfigMap/api-conf", "none")
	check("Secret/api-db", "none")
	check("Service/api-v2", "bad") // the route's backend does not exist
	check("HTTPRoute/api-route", "warn")
	check("PersistentVolumeClaim/api-data", "ok")
	if _, ok := nodes["ReplicaSet/api-old1"]; ok {
		t.Error("an old ReplicaSet at 0 replicas must be left out")
	}
	if !nodes["Secret/api-db"].Missing || !nodes["ConfigMap/flags"].Missing || nodes["ConfigMap/api-conf"].Missing {
		t.Error("Missing must be set for Secrets (never read) and for ConfigMaps that do not exist only")
	}
	if n := nodes["PersistentVolume/pvc-123"]; n.Namespaced || n.Resource != "persistentvolumes" {
		t.Errorf("PersistentVolume ref = %+v", n)
	}
}

func TestBuildMapKeepsFailingPodsWhenTruncated(t *testing.T) {
	in := shopInput()
	healthy := in.pods[0]
	for i := 0; i < mapMaxNodes; i++ {
		p := healthy.DeepCopy()
		p.Name = healthy.Name + "-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		in.pods = append(in.pods, p)
	}
	m := buildMap(in)
	if !m.Truncated {
		t.Fatal("expected truncated")
	}
	found := false
	for _, n := range m.Nodes {
		found = found || n.ID == "Pod/api-7c9d-b"
	}
	if !found {
		t.Error("the crashing pod must be kept when pods are left out")
	}
}
