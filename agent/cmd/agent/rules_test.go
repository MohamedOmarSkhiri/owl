package main

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pod(phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
		Status:     corev1.PodStatus{Phase: phase, ContainerStatuses: statuses},
	}
	return p
}

func waiting(reason string, restarts int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:         "web",
		RestartCount: restarts,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "back-off"}},
	}
}

func TestHealthyPodHasNoProblems(t *testing.T) {
	ok := pod(corev1.PodRunning, corev1.ContainerStatus{Name: "web", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})
	if got := podProblems(ok); len(got) != 0 {
		t.Fatalf("problems = %+v, want none", got)
	}
	status, healthy := podStatus(ok)
	if !healthy || status != "Running · 1/1 · 0 restarts" {
		t.Fatalf("status = %q healthy=%v", status, healthy)
	}
}

func TestCrashLoopIsCritical(t *testing.T) {
	p := pod(corev1.PodRunning, waiting("CrashLoopBackOff", 7))
	got := podProblems(p)
	if len(got) != 1 || got[0].Reason != "CrashLoopBackOff" || got[0].Severity != "critical" {
		t.Fatalf("problems = %+v", got)
	}
	status, healthy := podStatus(p)
	if healthy || status != "CrashLoopBackOff · 0/1 · 7 restarts" {
		t.Fatalf("status = %q healthy=%v", status, healthy)
	}
}

func TestImagePullAndOOMKilled(t *testing.T) {
	pull := podProblems(pod(corev1.PodPending, waiting("ImagePullBackOff", 0)))
	found := false
	for _, p := range pull {
		found = found || (p.Reason == "ImagePullBackOff" && p.Severity == "critical")
	}
	if !found {
		t.Fatalf("image pull: %+v", pull)
	}
	oom := corev1.ContainerStatus{
		Name:                 "web",
		RestartCount:         2,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
	}
	if got := podProblems(pod(corev1.PodRunning, oom)); len(got) != 1 || got[0].Reason != "OOMKilled" {
		t.Fatalf("oom: %+v", got)
	}
}

func TestLongPendingIsWarning(t *testing.T) {
	got := podProblems(pod(corev1.PodPending))
	if len(got) != 1 || got[0].Reason != "PendingTooLong" || got[0].Severity != "warning" {
		t.Fatalf("problems = %+v", got)
	}
}

func TestSecretsAreNeverServed(t *testing.T) {
	if _, err := gvrFrom(map[string]string{"version": "v1", "resource": "secrets"}); err != errSecretsBlocked {
		t.Fatalf("err = %v, want errSecretsBlocked", err)
	}
	if _, err := gvrFrom(map[string]string{"version": "v1", "resource": "pods"}); err != nil {
		t.Fatalf("pods: %v", err)
	}
}
