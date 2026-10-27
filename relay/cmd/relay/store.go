package main

import (
	"sort"
	"sync"
	"time"
)

// Snapshot is what an agent sends periodically (JSON field names match the design doc).
type Snapshot struct {
	Cluster    string    `json:"cluster"`
	Ts         time.Time `json:"ts"`
	K8sVersion string    `json:"k8sVersion"`
	Nodes      struct {
		Total int `json:"total"`
		Ready int `json:"ready"`
	} `json:"nodes"`
	Pods struct {
		Total     int `json:"total"`
		Running   int `json:"running"`
		Completed int `json:"completed"`
		Failing   int `json:"failing"`
	} `json:"pods"`
	Usage *UsageTotals `json:"usage,omitempty"` // nil when the cluster has no metrics-server
}

// Problem is one open or resolved issue reported by an agent.
type Problem struct {
	ID        string    `json:"id"`
	Cluster   string    `json:"cluster"`
	Source    string    `json:"source"`   // pod | event | alertmanager
	Severity  string    `json:"severity"` // critical | warning
	Namespace string    `json:"namespace"`
	Object    string    `json:"object"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Resolved  bool      `json:"resolved"`
}

// ClusterView is what the app receives from GET /v1/clusters.
type ClusterView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	K8sVersion  string    `json:"k8sVersion"`
	Health      string    `json:"health"` // healthy | degraded | down
	NodesTotal  int       `json:"nodesTotal"`
	NodesReady  int       `json:"nodesReady"`
	PodsTotal   int       `json:"podsTotal"`
	PodsRunning int       `json:"podsRunning"`
	PodsFailing int       `json:"podsFailing"`
	LastSeen    time.Time `json:"lastSeen"`
	// Added in 0.6.0 for the sidebar and the Overview.
	PodsCompleted int          `json:"podsCompleted"`
	ProblemsOpen  int          `json:"problemsOpen"`
	Usage         *UsageTotals `json:"usage,omitempty"`
}

// staleAfter: a cluster whose agent has been silent this long is shown as down.
const staleAfter = 2 * time.Minute

// Store is an in-memory store. SQLite replaces it in a later step.
type Store struct {
	mu        sync.RWMutex
	snapshots map[string]Snapshot
	problems  map[string]map[string]Problem // cluster -> problem key -> problem
	history   map[string][]HistoryPoint     // cluster -> last 6 hours, oldest first (history.go)
}

func NewStore() *Store {
	return &Store{
		snapshots: map[string]Snapshot{},
		problems:  map[string]map[string]Problem{},
		history:   map[string][]HistoryPoint{},
	}
}

func problemKey(p Problem) string {
	return p.Source + "|" + p.Namespace + "|" + p.Object + "|" + p.Reason
}

func (s *Store) PutSnapshot(snap Snapshot) {
	if snap.Ts.IsZero() {
		snap.Ts = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[snap.Cluster] = snap
	s.recordLocked(snap)
}

// Touch marks a cluster's latest snapshot as fresh (used by the demo heartbeat).
func (s *Store) Touch(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.snapshots[cluster]; ok {
		snap.Ts = time.Now().UTC()
		s.snapshots[cluster] = snap
	}
}

// UpsertProblem stores a problem, keeping its original firstSeen and id.
// It returns true when the problem is new or has just been resolved (worth a push later).
func (s *Store) UpsertProblem(p Problem) (notify bool) {
	now := time.Now().UTC()
	if p.LastSeen.IsZero() {
		p.LastSeen = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	byKey, ok := s.problems[p.Cluster]
	if !ok {
		byKey = map[string]Problem{}
		s.problems[p.Cluster] = byKey
	}
	k := problemKey(p)
	old, exists := byKey[k]
	switch {
	case !exists:
		if p.FirstSeen.IsZero() {
			p.FirstSeen = p.LastSeen
		}
		if p.ID == "" {
			p.ID = newID()
		}
		notify = !p.Resolved
	default:
		p.ID = old.ID
		p.FirstSeen = old.FirstSeen
		notify = p.Resolved && !old.Resolved
		if !p.Resolved && old.Resolved {
			notify = true // it came back
			p.FirstSeen = p.LastSeen
		}
	}
	byKey[k] = p
	return notify
}

func (s *Store) Clusters() []ClusterView {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]ClusterView, 0, len(s.snapshots))
	for id, snap := range s.snapshots {
		v := ClusterView{
			ID: id, Name: id, K8sVersion: snap.K8sVersion,
			NodesTotal: snap.Nodes.Total, NodesReady: snap.Nodes.Ready,
			PodsTotal: snap.Pods.Total, PodsRunning: snap.Pods.Running, PodsFailing: snap.Pods.Failing,
			LastSeen:      snap.Ts,
			PodsCompleted: snap.Pods.Completed,
			ProblemsOpen:  s.openProblemsLocked(id),
			Usage:         snap.Usage,
		}
		v.Health = s.healthLocked(id, snap)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) healthLocked(id string, snap Snapshot) string {
	if time.Since(snap.Ts) > staleAfter || (snap.Nodes.Total > 0 && snap.Nodes.Ready == 0) {
		return "down"
	}
	if snap.Nodes.Ready < snap.Nodes.Total || snap.Pods.Failing > 0 {
		return "degraded"
	}
	for _, p := range s.problems[id] {
		if !p.Resolved && p.Severity == "critical" {
			return "degraded"
		}
	}
	return "healthy"
}

func (s *Store) ClusterExists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.snapshots[id]
	return ok
}

// Problems returns a cluster's problems, newest first. openOnly drops resolved ones.
func (s *Store) Problems(cluster string, openOnly bool) []Problem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := []Problem{}
	for _, p := range s.problems[cluster] {
		if openOnly && p.Resolved {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}
