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

func TestHistoryKeepsSixHoursAndFilters(t *testing.T) {
	st := NewStore()
	now := time.Now().UTC()
	for i := historyMax + 10; i > 0; i-- { // 10 more than fit
		var snap Snapshot
		snap.Cluster, snap.Ts = "c", now.Add(-time.Duration(i)*15*time.Second)
		snap.Usage = &UsageTotals{CPUUsage: int64(i)}
		st.PutSnapshot(snap)
	}
	if n := len(st.history["c"]); n != historyMax {
		t.Fatalf("kept %d points, want %d", n, historyMax)
	}
	last := st.History("c", time.Minute)
	if len(last) != 3 || last[2].Usage.CPUUsage != 1 { // -45 s, -30 s, -15 s; newest last
		t.Fatalf("last minute = %+v", last)
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
