package main

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func TestWaitWakesOnChange(t *testing.T) {
	c := newNSChanges()
	h := c.handler()
	since := c.current("shop")

	done := make(chan time.Duration)
	go func() {
		start := time.Now()
		c.wait(context.Background(), "shop", since, 5*time.Second)
		done <- time.Since(start)
	}()
	time.Sleep(50 * time.Millisecond)
	h.OnAdd(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "x"}}, false) // another namespace: keeps waiting
	time.Sleep(50 * time.Millisecond)
	h.OnDelete(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}})

	select {
	case d := <-done:
		if d > time.Second {
			t.Fatalf("woke after %s, want right after the change", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a change in the namespace did not wake the waiting request")
	}
	if c.current("shop") != since+1 {
		t.Fatalf("rev = %d, want %d", c.current("shop"), since+1)
	}
}

func TestWaitReturnsAtOnceWhenAlreadyChanged(t *testing.T) {
	c := newNSChanges()
	c.bump("shop")
	start := time.Now()
	c.wait(context.Background(), "shop", 0, 5*time.Second) // the app's rev 0 is already old
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("must not wait when the namespace changed since the app's revision")
	}
}

func TestResyncIsNotAChange(t *testing.T) {
	c := newNSChanges()
	h := c.handler()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api", ResourceVersion: "7"}}
	h.OnUpdate(pod, pod.DeepCopy()) // same resourceVersion: the informer's periodic replay
	if c.current("shop") != 0 {
		t.Fatal("a resync must not count as a change")
	}
	newer := pod.DeepCopy()
	newer.ResourceVersion = "8"
	h.OnUpdate(pod, newer)
	h.OnDelete(cache.DeletedFinalStateUnknown{Key: "shop/api", Obj: newer}) // a delete the watch missed
	if c.current("shop") != 2 {
		t.Fatalf("rev = %d, want 2", c.current("shop"))
	}
}

func TestEventTimes(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 4, 8, m, 0, 0, time.UTC) }
	classic := &corev1.Event{FirstTimestamp: metav1.NewTime(at(1)), LastTimestamp: metav1.NewTime(at(9))}
	if last, first := eventTimes(classic); !last.Equal(at(9)) || !first.Equal(at(1)) {
		t.Fatalf("classic: %s %s", last, first)
	}
	series := &corev1.Event{EventTime: metav1.NewMicroTime(at(2)),
		Series: &corev1.EventSeries{Count: 4, LastObservedTime: metav1.NewMicroTime(at(7))}}
	if last, first := eventTimes(series); !last.Equal(at(7)) || !first.Equal(at(2)) {
		t.Fatalf("series: %s %s", last, first)
	}
	if last, first := eventTimes(&corev1.Event{}); !last.IsZero() || !first.IsZero() {
		t.Fatal("no time at all must stay zero (raise uses now)")
	}
}
