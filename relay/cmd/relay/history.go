package main

// History: the relay keeps the last 6 hours of every cluster's snapshots in memory,
// for the Overview charts. It is lost when the relay restarts; that is the trade-off
// for needing no database and no Prometheus.

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// UsageTotals is the whole cluster's CPU (millicores) and memory (bytes), sent by the agent.
type UsageTotals struct {
	CPUUsage       int64 `json:"cpuUsage"`
	CPUAllocatable int64 `json:"cpuAllocatable"`
	MemUsage       int64 `json:"memUsage"`
	MemAllocatable int64 `json:"memAllocatable"`
}

// HistoryPoint is one snapshot, reduced to what the charts draw.
type HistoryPoint struct {
	Ts       time.Time    `json:"ts"`
	Usage    *UsageTotals `json:"usage,omitempty"` // missing = gap in the CPU/memory line
	Running  int          `json:"running"`
	Failing  int          `json:"failing"`
	Problems int          `json:"problems"`
}

// One point per agent snapshot (every 15 s): 6 h = 1440 points, about 100 KiB per cluster.
const historyMax = 6 * 60 * 4

// recordLocked appends a point and drops the oldest past historyMax. Caller holds s.mu.
func (s *Store) recordLocked(snap Snapshot) {
	h := append(s.history[snap.Cluster], HistoryPoint{
		Ts: snap.Ts, Usage: snap.Usage,
		Running: snap.Pods.Running, Failing: snap.Pods.Failing,
		Problems: s.openProblemsLocked(snap.Cluster),
	})
	if len(h) > historyMax {
		h = append(h[:0], h[len(h)-historyMax:]...) // shift down, reuse the same array
	}
	s.history[snap.Cluster] = h
}

func (s *Store) openProblemsLocked(cluster string) int {
	n := 0
	for _, p := range s.problems[cluster] {
		if !p.Resolved {
			n++
		}
	}
	return n
}

// History returns the points of the last d, oldest first (a copy: safe to use after unlock).
func (s *Store) History(cluster string, d time.Duration) []HistoryPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	since := time.Now().Add(-d)
	out := []HistoryPoint{}
	for _, p := range s.history[cluster] {
		if p.Ts.After(since) {
			out = append(out, p)
		}
	}
	return out
}

// GET /v1/clusters/{id}/history?minutes=60 (1 to 360).
func (s *server) getHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.store.ClusterExists(id) {
		writeErr(w, http.StatusNotFound, "unknown cluster")
		return
	}
	minutes := 60
	if v, err := strconv.Atoi(r.URL.Query().Get("minutes")); err == nil {
		minutes = min(max(v, 1), 360)
	}
	writeJSON(w, http.StatusOK, s.store.History(id, time.Duration(minutes)*time.Minute))
}

// GET /v1/info: the relay version, shown in the app's footer and useful in bug reports.
func getInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version})
}

// seedHistory fills 6 h of made-up points for the --seed demo, so the charts have a shape.
func seedHistory(st *Store, cluster string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now().UTC()
	const gi = int64(1) << 30
	for i := historyMax; i > 0; i-- {
		t := now.Add(-time.Duration(i) * 15 * time.Second)
		m := float64(t.Minute()) + float64(t.Hour()*60)
		wave := func(period, amp float64) float64 { return amp * (1 + math.Sin(m/period)) / 2 }
		st.history[cluster] = append(st.history[cluster], HistoryPoint{
			Ts: t,
			Usage: &UsageTotals{
				CPUUsage: 900 + int64(wave(6, 2600)), CPUAllocatable: 10000,
				MemUsage: 9*gi + int64(wave(23, float64(5*gi))), MemAllocatable: 23 * gi,
			},
			Running: 81, Failing: 3, Problems: 4,
		})
	}
}
