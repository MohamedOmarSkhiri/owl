package main

// Live map: the informers count changes per namespace, so a map request can wait for the
// next change instead of being asked again and again. The app asks "the map, once it
// changes after revision N"; the agent answers as soon as a pod, workload or Service in that
// namespace changes, or after a while with nothing new. That is a long-poll: the map updates
// within about a second, and a quiet namespace costs one request every 12 seconds.

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

type nsChanges struct {
	mu   sync.Mutex
	rev  map[string]uint64        // namespace -> number of changes seen
	wake map[string]chan struct{} // namespace -> closed at its next change
}

func newNSChanges() *nsChanges {
	return &nsChanges{rev: map[string]uint64{}, wake: map[string]chan struct{}{}}
}

func (c *nsChanges) bump(ns string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rev[ns]++
	if ch := c.wake[ns]; ch != nil {
		close(ch) // wakes every request waiting on this namespace
		delete(c.wake, ns)
	}
}

func (c *nsChanges) current(ns string) uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rev[ns]
}

// wait returns once the namespace has changed since revision `since`, after `max`,
// or when ctx ends (the app went away).
func (c *nsChanges) wait(ctx context.Context, ns string, since uint64, max time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.rev[ns] != since {
		c.mu.Unlock()
		return // changed already, while the app was drawing the last answer
	}
	ch := c.wake[ns]
	if ch == nil {
		ch = make(chan struct{})
		c.wake[ns] = ch
	}
	c.mu.Unlock()

	t := time.NewTimer(max)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-ctx.Done():
	}
}

// handler counts adds, real updates and deletes. Informers also replay every object every
// few minutes (resync) with the same resourceVersion: those are not changes.
func (c *nsChanges) handler() cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(o any) { c.bump(namespaceOf(o)) },
		UpdateFunc: func(old, o any) {
			a, okA := old.(metav1.Object)
			b, okB := o.(metav1.Object)
			if okA && okB && a.GetResourceVersion() == b.GetResourceVersion() {
				return
			}
			c.bump(namespaceOf(o))
		},
		DeleteFunc: func(o any) { c.bump(namespaceOf(o)) },
	}
}

func namespaceOf(o any) string {
	if t, ok := o.(cache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	if m, ok := o.(metav1.Object); ok {
		return m.GetNamespace()
	}
	return ""
}
