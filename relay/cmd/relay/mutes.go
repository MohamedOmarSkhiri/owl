package main

// Mutes: silence the notifications of known problems. A mute matches a cluster's problems by
// namespace, object and reason (empty = any), for a while (1 h, 1 day…) or until the problem
// resolves. Muted problems still show in the app, marked as muted, with the mute's note:
// "until resolved" with a note is how a problem is acknowledged ("known, fixing Monday").
// Saved to dataDir/mutes.json when the relay has a volume.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	mutesFile   = "mutes.json"
	maxMutes    = 500
	maxMuteFor  = 30 * 24 * time.Hour
	maxMuteNote = 500
)

type Mute struct {
	ID            string    `json:"id"`
	Cluster       string    `json:"cluster"`
	Namespace     string    `json:"namespace,omitempty"` // empty: every namespace
	Object        string    `json:"object,omitempty"`    // empty: every object ("pod/web-1")
	Reason        string    `json:"reason,omitempty"`    // empty: every reason
	Until         time.Time `json:"until,omitzero"`      // zero with UntilResolved
	UntilResolved bool      `json:"untilResolved,omitempty"`
	Note          string    `json:"note,omitempty"`
	Created       time.Time `json:"created"`
}

// MuteRequest is what the app sends to create a mute.
type MuteRequest struct {
	Namespace     string `json:"namespace"`
	Object        string `json:"object"`
	Reason        string `json:"reason"`
	For           string `json:"for"` // a duration like "1h" or "168h"; empty with untilResolved
	UntilResolved bool   `json:"untilResolved"`
	Note          string `json:"note"`
}

func (m Mute) active(now time.Time) bool { return m.UntilResolved || now.Before(m.Until) }

func (m Mute) matches(p Problem) bool {
	return m.Cluster == p.Cluster &&
		(m.Namespace == "" || m.Namespace == p.Namespace) &&
		(m.Object == "" || m.Object == p.Object) &&
		(m.Reason == "" || m.Reason == p.Reason)
}

type Mutes struct {
	mu    sync.Mutex
	dir   string // empty: memory only
	mutes map[string]Mute
}

func NewMutes(dir string) *Mutes { return &Mutes{dir: dir, mutes: map[string]Mute{}} }

// newMute checks a request and turns it into a mute.
func newMute(cluster string, r MuteRequest, now time.Time) (Mute, error) {
	m := Mute{
		ID: "m-" + strings.TrimPrefix(newID(), "p-"), Cluster: cluster, Created: now.UTC(),
		Namespace: strings.TrimSpace(r.Namespace), Object: strings.TrimSpace(r.Object), Reason: strings.TrimSpace(r.Reason),
		Note: strings.TrimSpace(r.Note), UntilResolved: r.UntilResolved,
	}
	if len(m.Note) > maxMuteNote {
		return m, fmt.Errorf("note: at most %d characters", maxMuteNote)
	}
	if r.UntilResolved {
		if m.Object == "" || m.Reason == "" {
			return m, errors.New("untilResolved needs one problem: object and reason")
		}
		if r.For != "" {
			return m, errors.New("give either for or untilResolved")
		}
		return m, nil
	}
	d, err := time.ParseDuration(r.For)
	if err != nil || d <= 0 || d > maxMuteFor {
		return m, errors.New(`for: a duration from "1m" to "720h" (30 days)`)
	}
	m.Until = now.Add(d).UTC()
	return m, nil
}

func (s *Mutes) Add(m Mute) error {
	s.mu.Lock()
	s.dropExpiredLocked(time.Now())
	if len(s.mutes) >= maxMutes {
		s.mu.Unlock()
		return fmt.Errorf("too many mutes (%d): remove some first", maxMutes)
	}
	s.mutes[m.ID] = m
	s.mu.Unlock()
	return s.save()
}

// Remove deletes a cluster's mute; false if there is none with that id.
func (s *Mutes) Remove(cluster, id string) (bool, error) {
	s.mu.Lock()
	m, ok := s.mutes[id]
	if ok && m.Cluster == cluster {
		delete(s.mutes, id)
	}
	s.mu.Unlock()
	if !ok || m.Cluster != cluster {
		return false, nil
	}
	return true, s.save()
}

// List returns a cluster's active mutes, newest first.
func (s *Mutes) List(cluster string) []Mute {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Mute{}
	for _, m := range s.mutes {
		if m.Cluster == cluster && m.active(now) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// For returns the mute that silences p, nil if none. The narrowest one wins (it holds the
// note about this very problem), then the one that lasts longest.
func (s *Mutes) For(p Problem) *Mute {
	if s == nil {
		return nil
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Mute
	for _, m := range s.mutes {
		if !m.active(now) || !m.matches(p) {
			continue
		}
		if best == nil || narrower(m, *best) || (width(m) == width(*best) && lasts(m) > lasts(*best)) {
			mm := m
			best = &mm
		}
	}
	return best
}

func width(m Mute) int {
	w := 0
	for _, f := range []string{m.Namespace, m.Object, m.Reason} {
		if f == "" {
			w++
		}
	}
	return w
}

func narrower(a, b Mute) bool { return width(a) < width(b) }

func lasts(m Mute) time.Duration {
	if m.UntilResolved {
		return maxMuteFor + time.Hour // longer than any timed mute
	}
	return time.Until(m.Until)
}

// Resolved ends the "until resolved" mutes of a problem that just resolved.
func (s *Mutes) Resolved(p Problem) {
	if s == nil {
		return
	}
	changed := false
	s.mu.Lock()
	for id, m := range s.mutes {
		if m.UntilResolved && m.Cluster == p.Cluster && m.Namespace == p.Namespace && m.Object == p.Object && m.Reason == p.Reason {
			delete(s.mutes, id)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		if err := s.save(); err != nil {
			log.Printf("mutes: save failed: %v", err)
		}
	}
}

func (s *Mutes) dropExpiredLocked(now time.Time) {
	for id, m := range s.mutes {
		if !m.active(now) {
			delete(s.mutes, id)
		}
	}
}

type savedMutes struct {
	Version int    `json:"version"`
	Mutes   []Mute `json:"mutes"`
}

func (s *Mutes) save() error {
	if s.dir == "" {
		return nil
	}
	s.mu.Lock()
	s.dropExpiredLocked(time.Now())
	all := make([]Mute, 0, len(s.mutes))
	for _, m := range s.mutes {
		all = append(all, m)
	}
	s.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	data, err := json.Marshal(savedMutes{Version: 1, Mutes: all})
	if err != nil {
		return err
	}
	return writeFileAtomic(s.dir, mutesFile, data)
}

// Load reads dir/mutes.json. A missing file is fine: no mutes yet.
func (s *Mutes) Load() (int, error) {
	if s.dir == "" {
		return 0, nil
	}
	data, err := os.ReadFile(filepath.Join(s.dir, mutesFile))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var saved savedMutes
	if err := json.Unmarshal(data, &saved); err != nil {
		return 0, fmt.Errorf("%s is not valid: %w", mutesFile, err)
	}
	if saved.Version != 1 {
		return 0, fmt.Errorf("%s has format version %d, this relay reads version 1", mutesFile, saved.Version)
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range saved.Mutes {
		if m.ID != "" && m.active(now) {
			s.mutes[m.ID] = m
		}
	}
	return len(s.mutes), nil
}
