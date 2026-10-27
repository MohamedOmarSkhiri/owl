package main

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// restarted builds a container status that restarted n times, the last run ending at end.
func restarted(n int32, end time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: "web", Ready: true, RestartCount: n,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 1, FinishedAt: metav1.NewTime(end),
		}},
	}
}

func TestLastRestartPicksTheNewest(t *testing.T) {
	older := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 26, 21, 40, 0, 0, time.UTC)
	p := pod(corev1.PodRunning, restarted(3, older), restarted(1, newer))
	got := lastRestart(p)
	if got == nil || !got.Equal(newer) {
		t.Fatalf("lastRestart = %v, want %v", got, newer)
	}
	if lastRestart(pod(corev1.PodRunning, restarted(0, newer))) != nil {
		t.Fatal("a container that never restarted must give nil")
	}
}

func TestSucceededPodIsCompleted(t *testing.T) {
	done := corev1.ContainerStatus{Name: "web", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0},
	}}
	status, healthy := podStatus(pod(corev1.PodSucceeded, done))
	if !healthy || status != "Completed · 0/1 · 0 restarts" {
		t.Fatalf("status = %q healthy=%v", status, healthy)
	}
}

func TestSumNodes(t *testing.T) {
	got := sumNodes([]NodeUsage{
		{CPUUsage: 250, CPUAllocatable: 4000, MemUsage: 1 << 30, MemAllocatable: 8 << 30},
		{CPUUsage: 750, CPUAllocatable: 4000, MemUsage: 3 << 30, MemAllocatable: 8 << 30},
	})
	if got.CPUUsage != 1000 || got.CPUAllocatable != 8000 || got.MemUsage != 4<<30 || got.MemAllocatable != 16<<30 {
		t.Fatalf("totals = %+v", got)
	}
}
