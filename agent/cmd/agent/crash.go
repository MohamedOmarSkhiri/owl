package main

// Crash recorder. When a container crashes (CrashLoopBackOff, OOMKilled), Kubernetes keeps
// the log of the run that died only until the container restarts again: by the time someone
// looks, it is often gone. The agent reads it right away, with the same read-only permission
// as the Logs view (GET pods/log?previous=true), and attaches it to the problem. The app and
// the notifications then show how the container ended: exit code, reason and its last lines.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	crashLogLines = 100      // last lines kept from the run that crashed
	crashLogBytes = 32 << 10 // and at most 32 KiB of them
	crashLineMax  = 1000     // characters per line (a JSON log line can be huge)
)

// Crash is how a container's last run ended, sent with its problem.
type Crash struct {
	Container  string    `json:"container"`
	ExitCode   int32     `json:"exitCode"`
	Reason     string    `json:"reason,omitempty"` // Error, OOMKilled, ...
	Signal     int32     `json:"signal,omitempty"` // 9 = killed, 15 = terminated
	FinishedAt time.Time `json:"finishedAt"`
	Restarts   int32     `json:"restarts"`
	Log        []string  `json:"log,omitempty"`      // last lines of that run, oldest first
	LogError   string    `json:"logError,omitempty"` // why there is no log, if there is none
}

// crashOf reads the container's previous run from its status; nil if it never ended.
func crashOf(cs corev1.ContainerStatus) *Crash {
	t := cs.LastTerminationState.Terminated
	if t == nil {
		return nil
	}
	return &Crash{
		Container: cs.Name, ExitCode: t.ExitCode, Reason: t.Reason, Signal: t.Signal,
		FinishedAt: t.FinishedAt.Time.UTC(), Restarts: cs.RestartCount,
	}
}

// crashRecorder reads each crashed run's log once and remembers it.
type crashRecorder struct {
	cs   kubernetes.Interface
	mu   sync.Mutex
	logs map[string]crashLog // namespace/pod/container/restarts -> log
}

type crashLog struct {
	lines []string
	err   string
}

func newCrashRecorder(cs kubernetes.Interface) *crashRecorder {
	return &crashRecorder{cs: cs, logs: map[string]crashLog{}}
}

// fill adds the log to c. Each restart has its own key, so the API is called once per crash,
// not every time the pod is re-checked (every 15 s).
func (r *crashRecorder) fill(ctx context.Context, namespace, pod string, c *Crash) {
	if r == nil || c == nil {
		return
	}
	key := fmt.Sprintf("%s/%s/%s/%d", namespace, pod, c.Container, c.Restarts)
	r.mu.Lock()
	l, ok := r.logs[key]
	r.mu.Unlock()
	if !ok {
		l = r.read(ctx, namespace, pod, c.Container)
		r.mu.Lock()
		if len(r.logs) >= 1000 { // many crashing pods for a long time: start over, it only costs a re-read
			r.logs = map[string]crashLog{}
		}
		r.logs[key] = l
		r.mu.Unlock()
	}
	c.Log, c.LogError = l.lines, l.err
}

func (r *crashRecorder) read(ctx context.Context, namespace, pod, container string) crashLog {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tail, limit := int64(crashLogLines), int64(crashLogBytes)
	raw, err := r.cs.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container, Previous: true, TailLines: &tail, LimitBytes: &limit,
	}).DoRaw(ctx)
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return crashLog{err: msg}
	}
	return crashLog{lines: crashLines(raw)}
}

// forget drops a deleted pod's logs.
func (r *crashRecorder) forget(namespace, pod string) {
	if r == nil {
		return
	}
	prefix := namespace + "/" + pod + "/"
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.logs {
		if strings.HasPrefix(k, prefix) {
			delete(r.logs, k)
		}
	}
}

// crashLines splits raw log output into lines, without "\r", each cut to crashLineMax.
func crashLines(raw []byte) []string {
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if len(l) > crashLineMax {
			l = strings.ToValidUTF8(l[:crashLineMax], "") + "…" // never cut a character in half
		}
		lines[i] = l
	}
	return lines
}

// crashChanged: the container crashed again, or its log arrived. Worth re-sending the problem.
func crashChanged(a, b *Crash) bool {
	if a == nil || b == nil {
		return a != b
	}
	return a.Restarts != b.Restarts || len(a.Log) != len(b.Log) || a.LogError != b.LogError
}
