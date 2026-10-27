package main

// History for the Overview charts, kept in three layers that get coarser with age:
//
//	every snapshot (15 s)   for 6 hours
//	5-minute average + peak for 7 days
//	1-hour average + peak   for 60 days
//
// About 5,000 points per cluster in all. With RELAY_DATA_DIR set, it is saved to a file
// (persist.go) and survives restarts; without it, it lives in memory only.

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

// HistoryPoint is one point of a chart. For 5-minute and hourly points, Usage is the
// average over the period and Peak the highest value seen in it.
type HistoryPoint struct {
	Ts       time.Time    `json:"ts"`
	Usage    *UsageTotals `json:"usage,omitempty"` // missing = no metrics then (gap in the line)
	Peak     *UsageTotals `json:"peak,omitempty"`
	Running  int          `json:"running"`  // last value in the period
	Failing  int          `json:"failing"`  // highest value in the period
	Problems int          `json:"problems"` // highest value in the period
}

// The three layers. Step 0 = keep every snapshot as it is.
var tiers = [3]struct{ step, keep time.Duration }{
	{0, 6 * time.Hour},
	{5 * time.Minute, 7 * 24 * time.Hour},
	{time.Hour, 60 * 24 * time.Hour},
}

// clusterHistory holds one cluster's layers, plus the period each rolled-up layer is filling.
// Exported fields: this is also the format of the saved file.
type clusterHistory struct {
	Tiers [3][]HistoryPoint `json:"tiers"`
	Open  [3]*bucket        `json:"open"` // Open[0] is unused: layer 0 has no periods
}

// bucket collects the points of one period (5 minutes or 1 hour) until it closes.
type bucket struct {
	Start    time.Time   `json:"start"`
	N        int64       `json:"n"` // points that had usage
	Sum      UsageTotals `json:"sum"`
	Max      UsageTotals `json:"max"`
	Running  int         `json:"running"`
	Failing  int         `json:"failing"`
	Problems int         `json:"problems"`
}

func (b *bucket) add(p HistoryPoint) {
	b.Running = p.Running
	b.Failing = max(b.Failing, p.Failing)
	b.Problems = max(b.Problems, p.Problems)
	if p.Usage == nil {
		return
	}
	b.N++
	b.Sum.CPUUsage += p.Usage.CPUUsage
	b.Sum.CPUAllocatable += p.Usage.CPUAllocatable
	b.Sum.MemUsage += p.Usage.MemUsage
	b.Sum.MemAllocatable += p.Usage.MemAllocatable
	peak := p.Usage
	if p.Peak != nil { // an already rolled-up point brings its own peak
		peak = p.Peak
	}
	b.Max.CPUUsage = max(b.Max.CPUUsage, peak.CPUUsage)
	b.Max.MemUsage = max(b.Max.MemUsage, peak.MemUsage)
	b.Max.CPUAllocatable = max(b.Max.CPUAllocatable, peak.CPUAllocatable)
	b.Max.MemAllocatable = max(b.Max.MemAllocatable, peak.MemAllocatable)
}

// point turns the period into one chart point: averages, peak, worst counts.
func (b *bucket) point() HistoryPoint {
	p := HistoryPoint{Ts: b.Start, Running: b.Running, Failing: b.Failing, Problems: b.Problems}
	if b.N > 0 {
		p.Usage = &UsageTotals{
			CPUUsage: b.Sum.CPUUsage / b.N, CPUAllocatable: b.Sum.CPUAllocatable / b.N,
			MemUsage: b.Sum.MemUsage / b.N, MemAllocatable: b.Sum.MemAllocatable / b.N,
		}
		peak := b.Max
		p.Peak = &peak
	}
	return p
}

// record adds one snapshot: to layer 0 as is, and into the open 5-minute period.
func (h *clusterHistory) record(p HistoryPoint) {
	h.Tiers[0] = append(h.Tiers[0], p)
	h.feed(1, p)
	h.prune(p.Ts)
}

// feed puts a point into layer i's open period. When the point starts a new period, the
// finished one becomes a point of layer i, and is fed in turn to the next, coarser layer.
func (h *clusterHistory) feed(i int, p HistoryPoint) {
	start := p.Ts.Truncate(tiers[i].step)
	if b := h.Open[i]; b != nil && !b.Start.Equal(start) {
		done := b.point()
		h.Tiers[i] = append(h.Tiers[i], done)
		h.Open[i] = nil
		if i+1 < len(tiers) {
			h.feed(i+1, done)
		}
	}
	if h.Open[i] == nil {
		h.Open[i] = &bucket{Start: start}
	}
	h.Open[i].add(p)
}

// prune drops the points older than each layer keeps, counting back from the newest point.
func (h *clusterHistory) prune(newest time.Time) {
	for i := range tiers {
		cut := newest.Add(-tiers[i].keep)
		n := 0
		for n < len(h.Tiers[i]) && h.Tiers[i][n].Ts.Before(cut) {
			n++
		}
		h.Tiers[i] = h.Tiers[i][n:]
	}
}

// recordLocked is called for every agent snapshot. Caller holds s.mu.
func (s *Store) recordLocked(snap Snapshot) {
	h := s.history[snap.Cluster]
	if h == nil {
		h = &clusterHistory{}
		s.history[snap.Cluster] = h
	}
	h.record(HistoryPoint{
		Ts: snap.Ts, Usage: snap.Usage,
		Running: snap.Pods.Running, Failing: snap.Pods.Failing,
		Problems: s.openProblemsLocked(snap.Cluster),
	})
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

// History returns the last d of a cluster, oldest first, from the finest layer that covers d.
// The period still filling is added at the end, so the chart reaches "now".
func (s *Store) History(cluster string, d time.Duration) []HistoryPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []HistoryPoint{}
	h := s.history[cluster]
	if h == nil {
		return out
	}
	i := 0
	for i < len(tiers)-1 && d > tiers[i].keep {
		i++
	}
	since := time.Now().Add(-d)
	for _, p := range h.Tiers[i] {
		if p.Ts.After(since) {
			out = append(out, p)
		}
	}
	if b := h.Open[i]; i > 0 && b != nil {
		out = append(out, b.point())
	}
	return out
}

// HasHistory: the relay has history for this cluster, reported since it started or loaded
// from disk. Right after a restart, history is there before the agent's next report.
func (s *Store) HasHistory(cluster string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.history[cluster] != nil
}

// GET /v1/clusters/{id}/history?minutes=60 (1 minute to 60 days).
func (s *server) getHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.store.ClusterExists(id) && !s.store.HasHistory(id) {
		writeErr(w, http.StatusNotFound, "unknown cluster")
		return
	}
	minutes := 60
	if v, err := strconv.Atoi(r.URL.Query().Get("minutes")); err == nil {
		minutes = min(max(v, 1), int(tiers[2].keep/time.Minute))
	}
	writeJSON(w, http.StatusOK, s.store.History(id, time.Duration(minutes)*time.Minute))
}

// GET /v1/info: relay version (footer, bug reports) and whether history survives restarts.
func (s *server) getInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "persistent": s.dataDir != ""})
}

// seedHistory fills 60 days of made-up snapshots for the --seed demo: a daily rhythm,
// a slow upward drift, and a spike every few days, so every range has a shape.
func seedHistory(st *Store, cluster string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	const gi = int64(1) << 30
	h := &clusterHistory{}
	st.history[cluster] = h
	now := time.Now().UTC().Truncate(15 * time.Second)
	for t := now.Add(-tiers[2].keep); !t.After(now); t = t.Add(15 * time.Second) {
		hours := float64(t.Unix()) / 3600
		day := (1 + math.Sin(hours/24*2*math.Pi)) / 2 // 0..1 over a day
		drift := 1 - now.Sub(t).Hours()/(60*24)       // older = lower
		cpu := 900 + int64(day*2400) + int64(drift*800)
		if int64(hours)%71 == 0 { // a busy hour now and then
			cpu += 4200
		}
		h.record(HistoryPoint{
			Ts: t,
			Usage: &UsageTotals{
				CPUUsage: cpu, CPUAllocatable: 10000,
				MemUsage: 9*gi + int64(day*float64(3*gi)) + int64(drift*float64(2*gi)), MemAllocatable: 23 * gi,
			},
			Running: 81, Failing: 3, Problems: 4,
		})
	}
}
