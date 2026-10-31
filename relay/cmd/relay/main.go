// KubePulse relay — step 2.
// Agents POST snapshots and problems; the app reads clusters and problems.
// Stdlib only, in-memory store. Pairing, WebSocket, SQLite and push come in later steps.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "p-" + hex.EncodeToString(b)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// requireToken checks "Authorization: Bearer <token>" in constant time.
func requireToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid or missing bearer token")
			return
		}
		next(w, r)
	}
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20) // 1 MiB
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

type server struct {
	store      *Store
	broker     *Broker
	dataDir    string    // where the history is saved; empty = memory only
	persistent bool      // dataDir is on a real volume (the chart says false for an emptyDir)
	notify     *Notifier // nil or without channels = notifications off
}

// --- agent API ---

func (s *server) postSnapshot(w http.ResponseWriter, r *http.Request) {
	var snap Snapshot
	if err := decode(r, &snap); err != nil {
		writeErr(w, http.StatusBadRequest, "bad snapshot: "+err.Error())
		return
	}
	if snap.Cluster == "" {
		writeErr(w, http.StatusBadRequest, "cluster is required")
		return
	}
	s.store.PutSnapshot(snap)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) postProblem(w http.ResponseWriter, r *http.Request) {
	var p Problem
	if err := decode(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "bad problem: "+err.Error())
		return
	}
	if p.Cluster == "" || p.Object == "" || p.Reason == "" {
		writeErr(w, http.StatusBadRequest, "cluster, object and reason are required")
		return
	}
	if p.Severity != "critical" && p.Severity != "warning" {
		writeErr(w, http.StatusBadRequest, "severity must be critical or warning")
		return
	}
	if s.store.UpsertProblem(p) {
		state := "OPEN"
		if p.Resolved {
			state = "RESOLVED"
		}
		// Push notification hook goes here in the push step.
		log.Printf("problem %s %s %s/%s %s", state, p.Cluster, p.Namespace, p.Object, p.Reason)
		s.notify.Problem(p) // filtered (severity, cooldown) and sent in the background

	}
	w.WriteHeader(http.StatusNoContent)
}

// --- app API ---

func (s *server) getClusters(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Clusters())
}

func (s *server) getProblems(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.store.ClusterExists(id) {
		writeErr(w, http.StatusNotFound, "unknown cluster")
		return
	}
	openOnly := r.URL.Query().Get("open") != "false"
	writeJSON(w, http.StatusOK, s.store.Problems(id, openOnly))
}

// --- agent request channel (long-poll) ---

func (s *server) agentPoll(w http.ResponseWriter, r *http.Request) {
	cluster := r.URL.Query().Get("cluster")
	if cluster == "" {
		writeErr(w, http.StatusBadRequest, "cluster query parameter is required")
		return
	}
	req := s.broker.Next(r.Context(), cluster)
	if req == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *server) agentRespond(w http.ResponseWriter, r *http.Request) {
	var resp AgentResponse
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20) // logs and large lists
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		writeErr(w, http.StatusBadRequest, "bad response: "+err.Error())
		return
	}
	if !s.broker.Deliver(r.PathValue("reqId"), resp) {
		writeErr(w, http.StatusNotFound, "no app is waiting for this request (timed out)")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// proxy forwards an app request to the cluster's agent and returns the agent's answer.
func (s *server) proxy(kind string, params func(r *http.Request) map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("id")
		if !s.store.ClusterExists(cluster) {
			writeErr(w, http.StatusNotFound, "unknown cluster")
			return
		}
		var p map[string]string
		if params != nil {
			p = params(r)
		}
		resp, err := s.broker.Call(r.Context(), cluster, kind, p)
		switch {
		case errors.Is(err, errAgentOffline):
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		case errors.Is(err, errAgentTimeout):
			writeErr(w, http.StatusGatewayTimeout, err.Error())
			return
		case err != nil:
			writeErr(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if resp.Status != http.StatusOK {
			status := resp.Status
			if status == 0 {
				status = http.StatusBadGateway
			}
			writeErr(w, status, resp.Error)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Body)
	}
}

// queryParams copies the listed query parameters into a map.
func queryParams(names ...string) func(r *http.Request) map[string]string {
	return func(r *http.Request) map[string]string {
		out := map[string]string{}
		q := r.URL.Query()
		for _, n := range names {
			if v := q.Get(n); v != "" {
				out[n] = v
			}
		}
		return out
	}
}

func logParams(r *http.Request) map[string]string {
	p := queryParams("container", "tail", "since")(r) // since: live mode cursor
	p["namespace"] = r.PathValue("ns")
	p["name"] = r.PathValue("name")
	return p
}

// logRequests prints method, path, status and duration for every request.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/v1/agent/requests" && rec.status == http.StatusNoContent {
			return // idle long-polls are noise
		}
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// version is set at build time: -ldflags "-X main.version=1.2.3"
var version = "dev"

func main() {
	listen := flag.String("listen", env("RELAY_LISTEN", ":2300"), "HTTP listen address")
	tlsListen := flag.String("tls-listen", env("RELAY_TLS_LISTEN", ""), "HTTPS listen address, e.g. :8443 (empty = no HTTPS)")
	tlsCert := flag.String("tls-cert", env("RELAY_TLS_CERT", "/tls/tls.crt"), "TLS certificate (PEM)")
	tlsKey := flag.String("tls-key", env("RELAY_TLS_KEY", "/tls/tls.key"), "TLS private key (PEM)")
	seed := flag.Bool("seed", false, "load demo clusters and problems")
	webDir := flag.String("web-dir", env("RELAY_WEB_DIR", ""), "serve the Owl web app from this folder (empty = API only)")
	dataDir := flag.String("data-dir", env("RELAY_DATA_DIR", ""), "save the history here so it survives restarts (empty = memory only)")
	flag.Parse()

	log.Printf("owl relay %s", version)

	agentToken := os.Getenv("RELAY_AGENT_TOKEN")
	appToken := os.Getenv("RELAY_APP_TOKEN")
	if agentToken == "" || appToken == "" {
		log.Fatal("set RELAY_AGENT_TOKEN and RELAY_APP_TOKEN (e.g. export RELAY_APP_TOKEN=$(openssl rand -hex 16))")
	}

	s := &server{store: NewStore(), broker: NewBroker(), dataDir: *dataDir, notify: notifierFromEnv()}
	s.persistent = s.dataDir != "" && os.Getenv("RELAY_PERSISTENT") != "false"
	if s.dataDir != "" {
		if err := CheckWritable(s.dataDir); err != nil {
			log.Fatalf("history: %v", err)
		}
		n, err := s.store.LoadHistory(s.dataDir)
		if err != nil {
			log.Fatalf("history: %v (move the file away to start with an empty history)", err)
		}
		log.Printf("history: %d clusters loaded from %s, saved every minute", n, s.dataDir)
		go func() {
			for range time.Tick(time.Minute) {
				if err := s.store.SaveHistory(s.dataDir); err != nil {
					log.Printf("history: save failed: %v", err)
				}
			}
		}()
	} else {
		log.Print("history: memory only (set RELAY_DATA_DIR to keep it across restarts)")
	}
	if *seed {
		seedDemo(s.store)
		seedHistory(s.store, "homelab-k3s")
		go demoHeartbeat(s.store, "homelab-k3s", "edge-pi")
		log.Print("demo data loaded (homelab-k3s and edge-pi kept alive, lab-old stays down)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })

	mux.HandleFunc("POST /v1/agent/snapshot", requireToken(agentToken, s.postSnapshot))
	mux.HandleFunc("POST /v1/agent/problems", requireToken(agentToken, s.postProblem))
	mux.HandleFunc("GET /v1/agent/requests", requireToken(agentToken, s.agentPoll))
	mux.HandleFunc("POST /v1/agent/responses/{reqId}", requireToken(agentToken, s.agentRespond))

	mux.HandleFunc("GET /v1/clusters", requireToken(appToken, s.getClusters))
	mux.HandleFunc("GET /v1/clusters/{id}/problems", requireToken(appToken, s.getProblems))
	mux.HandleFunc("GET /v1/clusters/{id}/history", requireToken(appToken, s.getHistory))
	mux.HandleFunc("GET /v1/info", requireToken(appToken, s.getInfo))
	mux.HandleFunc("GET /v1/notifications", requireToken(appToken, s.getNotifications))
	mux.HandleFunc("POST /v1/notifications/test", requireToken(appToken, s.testNotifications))
	// Live data, answered by the cluster's agent:
	mux.HandleFunc("GET /v1/clusters/{id}/kinds", requireToken(appToken, s.proxy("kinds", nil)))
	mux.HandleFunc("GET /v1/clusters/{id}/resources", requireToken(appToken,
		s.proxy("list", queryParams("group", "version", "resource", "namespace"))))
	mux.HandleFunc("GET /v1/clusters/{id}/resource", requireToken(appToken,
		s.proxy("get", queryParams("group", "version", "resource", "namespace", "name"))))
	mux.HandleFunc("GET /v1/clusters/{id}/pods/{ns}/{name}/logs", requireToken(appToken, s.proxy("logs", logParams)))
	mux.HandleFunc("GET /v1/clusters/{id}/usage", requireToken(appToken, s.proxy("usage", queryParams("scope", "namespace"))))
	if *webDir != "" {
		mux.Handle("GET /", webApp(*webDir))
		log.Printf("serving the web app from %s", *webDir)
	}

	handler := securityHeaders(logRequests(mux))
	newServer := func(addr string) *http.Server {
		return &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second, // agent long-polls are held up to 25s
		}
	}

	servers := []*http.Server{newServer(*listen)}
	go func() {
		log.Printf("relay listening on %s (http)", *listen)
		if err := servers[0].ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	if *tlsListen != "" {
		certs, err := newCertReloader(*tlsCert, *tlsKey)
		if err != nil {
			log.Fatal(err)
		}
		tlsSrv := newServer(*tlsListen)
		tlsSrv.TLSConfig = certs.tlsConfig()
		servers = append(servers, tlsSrv)
		go func() {
			log.Printf("relay listening on %s (https, certificate %s)", *tlsListen, *tlsCert)
			if err := tlsSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if s.notify.enabled() {
		names := []string{}
		for _, ch := range s.notify.channels {
			names = append(names, ch.Name())
		}
		log.Printf("notifications: %s, from severity %s", strings.Join(names, ", "), s.notify.minSeverity)
		go s.notify.run(ctx)                    // sends what is queued
		go s.notify.watchClusters(ctx, s.store) // cluster down / back, every 30 s
	} else {
		log.Print("notifications: off (set notifications.* in the chart to turn them on)")
	}
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdown)
	}
	if s.dataDir != "" { // last save, so an upgrade loses at most nothing
		if err := s.store.SaveHistory(s.dataDir); err != nil {
			log.Printf("history: final save failed: %v", err)
		}
	}
	log.Print("relay stopped")
}

// demoHeartbeat refreshes the demo clusters' snapshots every 30s, standing in for agents
// until real ones exist; otherwise they would turn "down" after staleAfter.
func demoHeartbeat(st *Store, clusters ...string) {
	for range time.Tick(30 * time.Second) {
		for _, id := range clusters {
			st.Touch(id)
		}
	}
}

// seedDemo loads the same demo data the app used in step 1, through the store API.
func seedDemo(st *Store) {
	now := time.Now().UTC()

	add := func(id, ver string, nt, nr, pt, pr, pf int, age time.Duration) {
		var snap Snapshot
		snap.Cluster, snap.K8sVersion, snap.Ts = id, ver, now.Add(-age)
		snap.Nodes.Total, snap.Nodes.Ready = nt, nr
		snap.Pods.Total, snap.Pods.Running, snap.Pods.Failing = pt, pr, pf
		st.PutSnapshot(snap)
	}
	add("homelab-k3s", "v1.33.4+k3s1", 3, 3, 84, 81, 3, 12*time.Second)
	add("edge-pi", "v1.32.7+k3s1", 2, 2, 23, 23, 0, 30*time.Second)
	add("lab-old", "v1.31.2", 1, 0, 0, 0, 0, 3*time.Hour)

	prob := func(cluster, src, sev, ns, obj, reason, msg string, first, last time.Duration) {
		st.UpsertProblem(Problem{
			Cluster: cluster, Source: src, Severity: sev, Namespace: ns, Object: obj,
			Reason: reason, Message: msg, FirstSeen: now.Add(-first), LastSeen: now.Add(-last),
		})
	}
	prob("homelab-k3s", "pod", "critical", "wso2", "pod/apim-gw-0", "CrashLoopBackOff",
		"container gateway: back-off 5m0s restarting failed container", 42*time.Minute, 2*time.Minute)
	prob("homelab-k3s", "alertmanager", "critical", "monitoring", "alert/KubePodCrashLooping", "KubePodCrashLooping",
		`Pod wso2/apim-gw-0 is in waiting state (reason: "CrashLoopBackOff")`, 30*time.Minute, 5*time.Minute)
	prob("homelab-k3s", "pod", "critical", "default", "pod/web-7c9d8-xk2lp", "ImagePullBackOff",
		`container web: Back-off pulling image "nginx:doesnotexist"`, 18*time.Minute, time.Minute)
	prob("homelab-k3s", "event", "warning", "argocd", "pod/argocd-repo-server-5f7b9-qq8sd", "Unhealthy",
		`Readiness probe failed: Get "http://10.42.0.31:8084/healthz": context deadline exceeded`, 9*time.Minute, 8*time.Minute)
	prob("lab-old", "event", "critical", "", "node/lab-old-1", "NodeNotReady",
		"Node lab-old-1 status is now: NodeNotReady", 3*time.Hour, 3*time.Hour)
}
