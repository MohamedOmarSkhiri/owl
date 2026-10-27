package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	agentTok = "agent-test-token"
	appTok   = "app-test-token"
)

// newTestServer builds the same routes as main() on an httptest server.
func newTestServer(t *testing.T, webDir string) (*server, *httptest.Server) {
	t.Helper()
	s := &server{store: NewStore(), broker: NewBroker()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /v1/agent/snapshot", requireToken(agentTok, s.postSnapshot))
	mux.HandleFunc("POST /v1/agent/problems", requireToken(agentTok, s.postProblem))
	mux.HandleFunc("GET /v1/agent/requests", requireToken(agentTok, s.agentPoll))
	mux.HandleFunc("POST /v1/agent/responses/{reqId}", requireToken(agentTok, s.agentRespond))
	mux.HandleFunc("GET /v1/clusters", requireToken(appTok, s.getClusters))
	mux.HandleFunc("GET /v1/clusters/{id}/problems", requireToken(appTok, s.getProblems))
	mux.HandleFunc("GET /v1/clusters/{id}/pods/{ns}/{name}/logs", requireToken(appTok, s.proxy("logs", logParams)))
	if webDir != "" {
		mux.Handle("GET /", webApp(webDir))
	}
	ts := httptest.NewServer(securityHeaders(mux))
	t.Cleanup(ts.Close)
	return s, ts
}

func do(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestTokensAreRequiredAndSeparate(t *testing.T) {
	_, ts := newTestServer(t, "")
	if r := do(t, "GET", ts.URL+"/v1/clusters", "", ""); r.StatusCode != 401 {
		t.Fatalf("no token: got %d, want 401", r.StatusCode)
	}
	if r := do(t, "GET", ts.URL+"/v1/clusters", agentTok, ""); r.StatusCode != 401 {
		t.Fatalf("agent token on app API: got %d, want 401", r.StatusCode)
	}
	if r := do(t, "POST", ts.URL+"/v1/agent/snapshot", appTok, `{"cluster":"x"}`); r.StatusCode != 401 {
		t.Fatalf("app token on agent API: got %d, want 401", r.StatusCode)
	}
	if r := do(t, "GET", ts.URL+"/healthz", "", ""); r.StatusCode != 200 {
		t.Fatalf("healthz: got %d", r.StatusCode)
	}
}

func TestSnapshotMakesClusterVisibleWithHealth(t *testing.T) {
	_, ts := newTestServer(t, "")
	snap := `{"cluster":"prod","k8sVersion":"v1.36.0","nodes":{"total":3,"ready":3},"pods":{"total":10,"running":9,"failing":1}}`
	if r := do(t, "POST", ts.URL+"/v1/agent/snapshot", agentTok, snap); r.StatusCode != 204 {
		t.Fatalf("snapshot: got %d", r.StatusCode)
	}
	var clusters []ClusterView
	json.NewDecoder(do(t, "GET", ts.URL+"/v1/clusters", appTok, "").Body).Decode(&clusters)
	if len(clusters) != 1 || clusters[0].ID != "prod" || clusters[0].Health != "degraded" {
		t.Fatalf("clusters = %+v, want prod degraded", clusters)
	}
}

func TestProblemLifecycleNotifiesOnceAndResolves(t *testing.T) {
	st := NewStore()
	p := Problem{Cluster: "c", Source: "pod", Severity: "critical", Namespace: "ns", Object: "pod/a", Reason: "CrashLoopBackOff"}
	if !st.UpsertProblem(p) {
		t.Fatal("new problem should notify")
	}
	if st.UpsertProblem(p) {
		t.Fatal("repeat of an open problem must not notify again")
	}
	p.Resolved = true
	if !st.UpsertProblem(p) {
		t.Fatal("resolution should notify")
	}
	if got := len(st.Problems("c", true)); got != 0 {
		t.Fatalf("open problems = %d, want 0", got)
	}
	if got := len(st.Problems("c", false)); got != 1 {
		t.Fatalf("all problems = %d, want 1", got)
	}
}

func TestStaleClusterIsDown(t *testing.T) {
	st := NewStore()
	var snap Snapshot
	snap.Cluster, snap.Ts = "old", time.Now().Add(-10*time.Minute)
	snap.Nodes.Total, snap.Nodes.Ready = 1, 1
	st.PutSnapshot(snap)
	if h := st.Clusters()[0].Health; h != "down" {
		t.Fatalf("health = %s, want down", h)
	}
}

func TestLiveRequestRoundTripThroughAgent(t *testing.T) {
	s, ts := newTestServer(t, "")
	do(t, "POST", ts.URL+"/v1/agent/snapshot", agentTok, `{"cluster":"prod"}`)

	// Agent offline: the app gets 503, not a hang.
	if r := do(t, "GET", ts.URL+"/v1/clusters/prod/pods/default/web/logs", appTok, ""); r.StatusCode != 503 {
		t.Fatalf("offline agent: got %d, want 503", r.StatusCode)
	}

	// Fake agent: long-poll one request and answer it.
	go func() {
		res := do(t, "GET", ts.URL+"/v1/agent/requests?cluster=prod", agentTok, "")
		var req AgentRequest
		json.NewDecoder(res.Body).Decode(&req)
		if req.Kind != "logs" || req.Params["name"] != "web" || req.Params["tail"] != "5" {
			t.Errorf("agent got %+v", req)
		}
		do(t, "POST", ts.URL+"/v1/agent/responses/"+req.ID, agentTok, `{"status":200,"body":{"lines":["hello"]}}`)
	}()
	waitFor(t, func() bool { return s.broker.Online("prod") })

	res := do(t, "GET", ts.URL+"/v1/clusters/prod/pods/default/web/logs?tail=5", appTok, "")
	var body struct{ Lines []string }
	json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != 200 || len(body.Lines) != 1 || body.Lines[0] != "hello" {
		t.Fatalf("logs: status %d body %+v", res.StatusCode, body)
	}
}

func TestBrokerTimesOutWithoutAnswer(t *testing.T) {
	b := NewBroker()
	b.mu.Lock()
	b.lastPoll["c"] = time.Now() // agent counts as online
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Call(ctx, "c", "kinds", nil); err != errAgentTimeout {
		t.Fatalf("err = %v, want errAgentTimeout", err)
	}
}

func TestWebAppFallbackAndHeaders(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>owl</html>"), 0o644)
	os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644)
	_, ts := newTestServer(t, dir)

	for _, path := range []string{"/", "/c/prod/pods"} {
		r := do(t, "GET", ts.URL+path, "", "")
		if r.StatusCode != 200 || r.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: status %d, CSP %q", path, r.StatusCode, r.Header.Get("Content-Security-Policy"))
		}
	}
	if r := do(t, "GET", ts.URL+"/assets/app.js", "", ""); !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("assets should be cached, got %q", r.Header.Get("Cache-Control"))
	}
	if got := filepathClean("/../../etc/passwd"); got != "/etc/passwd" {
		t.Fatalf("clean = %q", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

// feed records a snapshot every 15 s from start for d, with the CPU usage given by cpu(t).
func feed(st *Store, cluster string, start time.Time, d time.Duration, cpu func(t time.Time) int64) {
	for t := start; t.Before(start.Add(d)); t = t.Add(15 * time.Second) {
		var snap Snapshot
		snap.Cluster, snap.Ts = cluster, t
		snap.Usage = &UsageTotals{CPUUsage: cpu(t), CPUAllocatable: 4000, MemUsage: 1, MemAllocatable: 2}
		st.PutSnapshot(snap)
	}
}

func TestHistoryRollsUpWithAverageAndPeak(t *testing.T) {
	st := NewStore()
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	spike := start.Add(10 * time.Minute)
	feed(st, "c", start, 2*time.Hour, func(t time.Time) int64 {
		if t.Equal(spike) {
			return 4000 // one 15-second spike
		}
		return 1000
	})
	h := st.history["c"]
	if n := len(h.Tiers[1]); n != 23 { // 24 periods of 5 min, the last one still open
		t.Fatalf("5-minute points = %d, want 23", n)
	}
	p := h.Tiers[1][2] // 10:00-10:05 after the hour: holds the spike
	if p.Peak.CPUUsage != 4000 || p.Usage.CPUUsage != (19*1000+4000)/20 {
		t.Fatalf("5-minute point = avg %d peak %d", p.Usage.CPUUsage, p.Peak.CPUUsage)
	}
	if len(h.Tiers[2]) != 1 || h.Tiers[2][0].Peak.CPUUsage != 4000 { // first hour closed, peak kept
		t.Fatalf("hourly points = %+v", h.Tiers[2])
	}
}

func TestHistoryPicksTheLayerAndPrunes(t *testing.T) {
	st := NewStore()
	feed(st, "c", time.Now().UTC().Add(-8*time.Hour), 8*time.Hour, func(time.Time) int64 { return 1 })
	if got := len(st.History("c", time.Hour)); got < 239 || got > 241 { // raw 15 s points
		t.Fatalf("1 h = %d points, want ~240", got)
	}
	if got := len(st.History("c", 24*time.Hour)); got < 96 || got > 98 { // 8 h of 5-minute points
		t.Fatalf("24 h = %d points, want ~97", got)
	}
	oldest := st.history["c"].Tiers[0][0].Ts
	if age := time.Since(oldest); age > 6*time.Hour+time.Minute {
		t.Fatalf("raw layer keeps %v, want 6 h", age)
	}
}

func TestHistorySurvivesSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	st := NewStore()
	feed(st, "c", time.Now().UTC().Add(-30*time.Minute), 30*time.Minute, func(time.Time) int64 { return 7 })
	if err := st.SaveHistory(dir); err != nil {
		t.Fatal(err)
	}
	fresh := NewStore() // a restarted relay
	if n, err := fresh.LoadHistory(dir); err != nil || n != 1 {
		t.Fatalf("load: %d clusters, %v", n, err)
	}
	if a, b := len(st.History("c", time.Hour)), len(fresh.History("c", time.Hour)); a != b || a < 100 {
		t.Fatalf("points before %d, after %d", a, b)
	}
	if n, err := NewStore().LoadHistory(t.TempDir()); err != nil || n != 0 { // first start: no file yet
		t.Fatalf("empty dir: %d, %v", n, err)
	}
}

func TestHistoryEndpoint(t *testing.T) {
	s, ts := newTestServer(t, "")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/clusters/{id}/history", requireToken(appTok, s.getHistory))
	hs := httptest.NewServer(mux)
	defer hs.Close()

	do(t, "POST", ts.URL+"/v1/agent/snapshot", agentTok,
		`{"cluster":"dev","pods":{"total":3,"running":2,"completed":1},"usage":{"cpuUsage":500,"cpuAllocatable":4000,"memUsage":1,"memAllocatable":2}}`)
	r := do(t, "GET", hs.URL+"/v1/clusters/dev/history?minutes=5", appTok, "")
	var pts []HistoryPoint
	if err := json.NewDecoder(r.Body).Decode(&pts); err != nil || len(pts) != 1 || pts[0].Usage.CPUAllocatable != 4000 {
		t.Fatalf("history = %+v (%v)", pts, err)
	}
	if r := do(t, "GET", hs.URL+"/v1/clusters/nope/history", appTok, ""); r.StatusCode != 404 {
		t.Fatalf("unknown cluster: %d", r.StatusCode)
	}
}

func TestCheckWritable(t *testing.T) {
	if err := CheckWritable(t.TempDir()); err != nil {
		t.Fatalf("writable dir: %v", err)
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	if err := CheckWritable(ro); err == nil || !strings.Contains(err.Error(), "65532") {
		t.Fatalf("read-only dir: %v", err)
	}
}
