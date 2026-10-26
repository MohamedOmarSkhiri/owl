// KubePulse agent — step 3.
// Watches Nodes, Pods and Warning Events with shared informers (read-only: get/list/watch)
// and reports a health snapshot + problems to the KubePulse relay.
//
// Run from a laptop:  AGENT_TOKEN=... ./bin/agent --cluster mycluster --relay http://localhost:8080
// Run in-cluster:     same flags; it uses the pod's ServiceAccount automatically.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// ---------- relay client ----------

type Problem struct {
	Cluster   string    `json:"cluster"`
	Source    string    `json:"source"`   // pod | event
	Severity  string    `json:"severity"` // critical | warning
	Namespace string    `json:"namespace"`
	Object    string    `json:"object"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Resolved  bool      `json:"resolved"`
}

func (p Problem) key() string {
	return p.Source + "|" + p.Namespace + "|" + p.Object + "|" + p.Reason
}

type Count struct {
	Total int `json:"total"`
	Ready int `json:"ready"`
}

type PodCount struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Failing int `json:"failing"`
}

type Snapshot struct {
	Cluster    string    `json:"cluster"`
	Ts         time.Time `json:"ts"`
	K8sVersion string    `json:"k8sVersion"`
	Nodes      Count     `json:"nodes"`
	Pods       PodCount  `json:"pods"`
}

type relayClient struct {
	base  string
	token string
	http  *http.Client
}

func (c *relayClient) post(path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		return fmt.Errorf("relay %s: HTTP %d %s", path, res.StatusCode, e.Error)
	}
	return nil
}

// ---------- problem tracking ----------

// eventTTL: a warning event that has not repeated for this long is considered resolved.
const eventTTL = 15 * time.Minute

type tracker struct {
	mu      sync.Mutex
	cluster string
	relay   *relayClient
	open    map[string]Problem
}

func (t *tracker) send(p Problem) {
	if err := t.relay.post("/v1/agent/problems", p); err != nil {
		log.Printf("send problem failed: %v", err)
	}
}

// raise opens a problem, or refreshes lastSeen if it is already open.
func (t *tracker) raise(p Problem) {
	now := time.Now().UTC()
	p.Cluster = t.cluster
	p.LastSeen = now

	t.mu.Lock()
	old, exists := t.open[p.key()]
	if exists {
		p.FirstSeen = old.FirstSeen
	} else {
		p.FirstSeen = now
	}
	t.open[p.key()] = p
	t.mu.Unlock()

	if !exists {
		log.Printf("OPEN     %-8s %s/%s %s: %s", p.Severity, p.Namespace, p.Object, p.Reason, p.Message)
		t.send(p)
	}
}

// resolveWhere closes every open problem matching the filter.
func (t *tracker) resolveWhere(match func(Problem) bool) {
	t.mu.Lock()
	var closed []Problem
	for k, p := range t.open {
		if match(p) {
			delete(t.open, k)
			p.Resolved = true
			p.LastSeen = time.Now().UTC()
			closed = append(closed, p)
		}
	}
	t.mu.Unlock()

	for _, p := range closed {
		log.Printf("RESOLVED %-8s %s/%s %s", p.Severity, p.Namespace, p.Object, p.Reason)
		t.send(p)
	}
}

// resyncAll re-sends every open problem. The relay upserts, so this is idempotent;
// it repopulates a relay that restarted (its store is in memory for now).
func (t *tracker) resyncAll() {
	t.mu.Lock()
	all := make([]Problem, 0, len(t.open))
	for _, p := range t.open {
		all = append(all, p)
	}
	t.mu.Unlock()
	for _, p := range all {
		t.send(p)
	}
}

func (t *tracker) failingPods() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	pods := map[string]bool{}
	for _, p := range t.open {
		if p.Source == "pod" {
			pods[p.Namespace+"/"+p.Object] = true
		}
	}
	return len(pods)
}

// ---------- detection rules ----------

var badWaiting = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"CreateContainerConfigError": true,
	"InvalidImageName":           true,
}

func podProblems(pod *corev1.Pod) []Problem {
	var out []Problem
	obj := "pod/" + pod.Name
	add := func(sev, reason, msg string) {
		out = append(out, Problem{Source: "pod", Severity: sev, Namespace: pod.Namespace, Object: obj, Reason: reason, Message: msg})
	}

	if pod.Status.Phase == corev1.PodFailed {
		add("critical", "Failed", pod.Status.Reason+" "+pod.Status.Message)
	}
	if pod.Status.Phase == corev1.PodPending && time.Since(pod.CreationTimestamp.Time) > 5*time.Minute {
		add("warning", "PendingTooLong", "pending for more than 5m")
	}
	statuses := append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	for _, cs := range statuses {
		if w := cs.State.Waiting; w != nil && badWaiting[w.Reason] {
			add("critical", w.Reason, fmt.Sprintf("container %s: %s", cs.Name, w.Message))
		}
		if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" && cs.RestartCount > 0 {
			add("critical", "OOMKilled", fmt.Sprintf("container %s restarted %d times", cs.Name, cs.RestartCount))
		}
	}
	return out
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// ---------- main ----------

// buildConfig: no kubeconfig = the pod's ServiceAccount (or ~/.kube/config outside a cluster);
// a kubeconfig = that file, using kubeContext if given, else its current-context.
func buildConfig(kubeconfig, kubeContext string) (*rest.Config, error) {
	if kubeconfig == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
		if home, err := os.UserHomeDir(); err == nil {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

// version is set at build time: -ldflags "-X main.version=1.2.3"
var version = "dev"

func main() {
	kubeconfig := flag.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig path (empty = in-cluster, then ~/.kube/config)")
	kubeContext := flag.String("context", os.Getenv("AGENT_KUBE_CONTEXT"), "context to use inside the kubeconfig (empty = its current-context)")
	clusterName := flag.String("cluster", os.Getenv("AGENT_CLUSTER"), "cluster name shown in the app (required)")
	relayURL := flag.String("relay", envOr("AGENT_RELAY", "http://localhost:2300"), "relay base URL")
	interval := flag.Duration("interval", 15*time.Second, "snapshot interval")
	flag.Parse()

	log.Printf("owl agent %s", version)
	token := os.Getenv("AGENT_TOKEN")
	if *clusterName == "" || token == "" {
		log.Fatal("set --cluster (or AGENT_CLUSTER) and AGENT_TOKEN (the relay's RELAY_AGENT_TOKEN)")
	}

	cfg, err := buildConfig(*kubeconfig, *kubeContext)
	if err != nil {
		log.Fatalf("kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("kube client: %v", err)
	}
	ver, err := client.Discovery().ServerVersion()
	if err != nil {
		log.Fatalf("cannot reach API server %s: %v", cfg.Host, err)
	}

	relay := &relayClient{base: strings.TrimRight(*relayURL, "/"), token: token, http: &http.Client{Timeout: 10 * time.Second}}
	tr := &tracker{cluster: *clusterName, relay: relay, open: map[string]Problem{}}
	log.Printf("cluster %q: API %s (%s) -> relay %s", *clusterName, cfg.Host, ver.GitVersion, relay.base)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	factory := informers.NewSharedInformerFactory(client, 10*time.Minute)
	podInf := factory.Core().V1().Pods()
	nodeInf := factory.Core().V1().Nodes()
	evInf := factory.Core().V1().Events()

	// evaluatePod raises the pod's current problems and resolves the ones that cleared.
	evaluatePod := func(pod *corev1.Pod) {
		obj := "pod/" + pod.Name
		current := map[string]bool{}
		for _, p := range podProblems(pod) {
			current[p.Reason] = true
			tr.raise(p)
		}
		tr.resolveWhere(func(p Problem) bool {
			return p.Source == "pod" && p.Namespace == pod.Namespace && p.Object == obj && !current[p.Reason]
		})
	}

	podInf.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(o any) {
			if pod, ok := o.(*corev1.Pod); ok {
				evaluatePod(pod)
			}
		},
		UpdateFunc: func(_, o any) {
			if pod, ok := o.(*corev1.Pod); ok {
				evaluatePod(pod)
			}
		},
		DeleteFunc: func(o any) {
			if t, ok := o.(cache.DeletedFinalStateUnknown); ok {
				o = t.Obj
			}
			if pod, ok := o.(*corev1.Pod); ok {
				obj := "pod/" + pod.Name
				tr.resolveWhere(func(p Problem) bool {
					return p.Namespace == pod.Namespace && p.Object == obj
				})
			}
		},
	})

	started := time.Now()
	onEvent := func(o any) {
		ev, ok := o.(*corev1.Event)
		if !ok || ev.Type != corev1.EventTypeWarning {
			return
		}
		last := ev.LastTimestamp.Time
		if last.IsZero() {
			last = ev.EventTime.Time
		}
		if !last.IsZero() && last.Before(started.Add(-eventTTL)) {
			return // old backlog from before the agent started
		}
		tr.raise(Problem{
			Source: "event", Severity: "warning", Namespace: ev.Namespace,
			Object: strings.ToLower(ev.InvolvedObject.Kind) + "/" + ev.InvolvedObject.Name,
			Reason: ev.Reason, Message: ev.Message,
		})
	}
	evInf.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    onEvent,
		UpdateFunc: func(_, o any) { onEvent(o) },
	})
	nodeInf.Informer() // register so the factory starts it

	factory.Start(ctx.Done())
	for typ, ok := range factory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			log.Fatalf("cache sync failed for %v", typ)
		}
	}
	log.Print("caches synced, reporting")

	// Answer live requests from the app (resource browser, pod logs) via the relay.
	go serveRequests(ctx, cfg, client, relay, *clusterName)

	podLister := podInf.Lister()
	nodeLister := nodeInf.Lister()
	sendSnapshot := func() {
		snap := buildSnapshot(*clusterName, ver.GitVersion, nodeLister, podLister, tr)
		if err := relay.post("/v1/agent/snapshot", snap); err != nil {
			log.Printf("send snapshot failed: %v", err)
		}
	}

	sendSnapshot()
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return
		case <-tick.C:
			// Re-check pods (time-based rules like PendingTooLong) and expire quiet events.
			if pods, err := podLister.List(labels.Everything()); err == nil {
				for _, p := range pods {
					evaluatePod(p)
				}
			}
			tr.resolveWhere(func(p Problem) bool {
				return p.Source == "event" && time.Since(p.LastSeen) > eventTTL
			})
			sendSnapshot()
			if n%4 == 0 {
				tr.resyncAll()
			}
		}
	}
}

func buildSnapshot(cluster, version string, nodes listerscorev1.NodeLister, pods listerscorev1.PodLister, tr *tracker) Snapshot {
	s := Snapshot{Cluster: cluster, Ts: time.Now().UTC(), K8sVersion: version}
	if ns, err := nodes.List(labels.Everything()); err == nil {
		s.Nodes.Total = len(ns)
		for _, n := range ns {
			if nodeReady(n) {
				s.Nodes.Ready++
			}
		}
	}
	if ps, err := pods.List(labels.Everything()); err == nil {
		s.Pods.Total = len(ps)
		for _, p := range ps {
			if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodSucceeded {
				s.Pods.Running++
			}
		}
	}
	s.Pods.Failing = tr.failingPods()
	return s
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
