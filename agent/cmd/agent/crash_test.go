package main

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func crashed(restarts int32) corev1.ContainerStatus {
	cs := waiting("CrashLoopBackOff", restarts)
	cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		ExitCode: 1, Reason: "Error", FinishedAt: metav1.NewTime(time.Now().Add(-time.Minute)),
	}}
	return cs
}

func TestCrashLoopCarriesTheCrash(t *testing.T) {
	got := podProblems(pod(corev1.PodRunning, crashed(4)))
	if len(got) != 1 || got[0].Crash == nil {
		t.Fatalf("problems = %+v", got)
	}
	c := got[0].Crash
	if c.Container != "web" || c.ExitCode != 1 || c.Reason != "Error" || c.Restarts != 4 {
		t.Fatalf("crash = %+v", c)
	}
	// An image that cannot be pulled never ran: no crash to report.
	for _, p := range podProblems(pod(corev1.PodPending, waiting("ImagePullBackOff", 0))) {
		if p.Crash != nil {
			t.Fatalf("%s has a crash: %+v", p.Reason, p.Crash)
		}
	}
}

func TestErrorBetweenRestartsKeepsTheCrashLoopOpen(t *testing.T) {
	cs := crashed(4)
	cs.State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 3, Reason: "Error"}}
	got := podProblems(pod(corev1.PodRunning, cs))
	if len(got) != 1 || got[0].Reason != "CrashLoopBackOff" || got[0].Crash == nil {
		t.Fatalf("problems = %+v, want CrashLoopBackOff with its crash", got)
	}
	// A Job pod that is not restarted is not a loop.
	p := pod(corev1.PodFailed, cs)
	p.Spec.RestartPolicy = corev1.RestartPolicyNever
	for _, pr := range podProblems(p) {
		if pr.Reason == "CrashLoopBackOff" {
			t.Fatal("restartPolicy Never must not be a crash loop")
		}
	}
}

func TestCrashRecorderReadsOncePerRestart(t *testing.T) {
	cs := fake.NewClientset()
	r := newCrashRecorder(cs)
	c := &Crash{Container: "web", Restarts: 4}
	r.fill(context.Background(), "default", "web", c)
	if len(c.Log) != 1 || c.Log[0] != "fake logs" { // the fake clientset answers every log call with this
		t.Fatalf("log = %q, err = %q", c.Log, c.LogError)
	}
	calls := func() (n int) {
		for _, a := range cs.Actions() {
			if a.GetSubresource() == "log" {
				n++
			}
		}
		return n
	}
	r.fill(context.Background(), "default", "web", &Crash{Container: "web", Restarts: 4})
	if calls() != 1 {
		t.Fatalf("log read %d times for one crash, want 1", calls())
	}
	r.fill(context.Background(), "default", "web", &Crash{Container: "web", Restarts: 5}) // crashed again
	if calls() != 2 {
		t.Fatalf("log read %d times for two crashes, want 2", calls())
	}
	r.forget("default", "web")
	if len(r.logs) != 0 {
		t.Fatalf("forget left %d entries", len(r.logs))
	}
}

func TestCrashLines(t *testing.T) {
	long := "a" + strings.Repeat("é", crashLineMax) // "é" is 2 bytes: the cut falls inside one
	lines := crashLines([]byte("panic: boom\r\n" + long + "\n\n"))
	if len(lines) != 2 || lines[0] != "panic: boom" {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.HasSuffix(lines[1], "…") || !strings.HasPrefix(lines[1], "aé") || len(lines[1]) > crashLineMax+len("…") {
		t.Fatalf("long line not cut cleanly: %d bytes", len(lines[1]))
	}
	if crashLines(nil) != nil {
		t.Fatal("empty log must give no lines")
	}
}

func TestCrashChangedResendsTheProblem(t *testing.T) {
	a := &Crash{Restarts: 4, Log: []string{"x"}}
	if crashChanged(a, &Crash{Restarts: 4, Log: []string{"x"}}) {
		t.Fatal("same crash must not re-send")
	}
	if !crashChanged(a, &Crash{Restarts: 5, Log: []string{"x"}}) || !crashChanged(nil, a) {
		t.Fatal("a new crash must re-send")
	}
	if crashChanged(nil, nil) {
		t.Fatal("no crash, no change")
	}
}
