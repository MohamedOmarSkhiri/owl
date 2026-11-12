package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewMuteValidates(t *testing.T) {
	now := time.Now()
	bad := []MuteRequest{
		{For: "soon"},
		{For: "0s"},
		{For: "721h"},
		{UntilResolved: true, Reason: "CrashLoopBackOff"},              // no object
		{UntilResolved: true, Object: "pod/a", Reason: "X", For: "1h"}, // both
		{For: "1h", Note: string(make([]byte, maxMuteNote+1))},         // note too long
	}
	for _, r := range bad {
		if _, err := newMute("dev", r, now); err == nil {
			t.Errorf("%+v: want an error", r)
		}
	}
	m, err := newMute("dev", MuteRequest{Namespace: " demo ", For: "1h", Note: " deploy "}, now)
	if err != nil || m.Namespace != "demo" || m.Note != "deploy" || !m.Until.Equal(now.Add(time.Hour).UTC()) || m.ID == "" {
		t.Fatalf("mute = %+v, %v", m, err)
	}
}

func TestMuteMatchingNarrowestWins(t *testing.T) {
	ms := NewMutes("")
	now := time.Now()
	add := func(r MuteRequest) Mute {
		m, err := newMute("dev", r, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := ms.Add(m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	p := Problem{Cluster: "dev", Namespace: "demo", Object: "pod/a", Reason: "CrashLoopBackOff"}
	if ms.For(p) != nil {
		t.Fatal("no mutes yet")
	}
	ns := add(MuteRequest{Namespace: "demo", For: "24h"})
	if m := ms.For(p); m == nil || m.ID != ns.ID {
		t.Fatalf("namespace mute must match: %+v", m)
	}
	ack := add(MuteRequest{Namespace: "demo", Object: "pod/a", Reason: "CrashLoopBackOff", UntilResolved: true, Note: "known"})
	if m := ms.For(p); m == nil || m.ID != ack.ID {
		t.Fatalf("the problem's own mute (with its note) must win: %+v", m)
	}
	if ms.For(Problem{Cluster: "dev", Namespace: "other", Object: "pod/a", Reason: "CrashLoopBackOff"}) != nil {
		t.Fatal("other namespace must not match")
	}
	if ms.For(Problem{Cluster: "prod", Namespace: "demo", Object: "pod/a", Reason: "CrashLoopBackOff"}) != nil {
		t.Fatal("other cluster must not match")
	}

	// The problem resolves: its "until resolved" mute ends, the namespace mute stays.
	ms.Resolved(p)
	if m := ms.For(p); m == nil || m.ID != ns.ID {
		t.Fatalf("after resolve: %+v", m)
	}
	if got := len(ms.List("dev")); got != 1 {
		t.Fatalf("mutes = %d, want 1", got)
	}

	// Expired mutes stop matching and are not listed.
	ms.mu.Lock()
	m := ms.mutes[ns.ID]
	m.Until = now.Add(-time.Second)
	ms.mutes[ns.ID] = m
	ms.mu.Unlock()
	if ms.For(p) != nil || len(ms.List("dev")) != 0 {
		t.Fatal("expired mute must not match")
	}
}

func TestMutesSurviveSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	ms := NewMutes(dir)
	m, _ := newMute("dev", MuteRequest{Namespace: "demo", For: "1h", Note: "upgrade"}, time.Now())
	if err := ms.Add(m); err != nil {
		t.Fatal(err)
	}
	again := NewMutes(dir)
	if n, err := again.Load(); err != nil || n != 1 {
		t.Fatalf("loaded %d, %v", n, err)
	}
	if got := again.List("dev"); len(got) != 1 || got[0].Note != "upgrade" {
		t.Fatalf("mutes = %+v", got)
	}
	if ok, err := again.Remove("prod", m.ID); ok || err != nil {
		t.Fatal("another cluster's mute must not be removable")
	}
	if ok, err := again.Remove("dev", m.ID); !ok || err != nil {
		t.Fatal(err)
	}
	third := NewMutes(dir)
	if n, _ := third.Load(); n != 0 {
		t.Fatalf("removal must be saved, loaded %d", n)
	}
}

func TestMutedProblemSendsNothingAndShowsAsMuted(t *testing.T) {
	s, ts := newTestServer(t, "")
	rec := &recorder{}
	s.notify = testNotifier(rec)
	do(t, "POST", ts.URL+"/v1/agent/snapshot", agentTok, `{"cluster":"dev"}`)

	if r := do(t, "POST", ts.URL+"/v1/clusters/dev/mutes", appTok, `{"namespace":"demo","for":"bad"}`); r.StatusCode != 400 {
		t.Fatalf("bad mute: %d", r.StatusCode)
	}
	if r := do(t, "POST", ts.URL+"/v1/clusters/nope/mutes", appTok, `{"for":"1h"}`); r.StatusCode != 404 {
		t.Fatalf("unknown cluster: %d", r.StatusCode)
	}
	r := do(t, "POST", ts.URL+"/v1/clusters/dev/mutes", appTok,
		`{"namespace":"demo","object":"pod/a","reason":"CrashLoopBackOff","untilResolved":true,"note":"known"}`)
	if r.StatusCode != 201 {
		t.Fatalf("mute: %d", r.StatusCode)
	}
	var m Mute
	json.NewDecoder(r.Body).Decode(&m)

	prob := `{"cluster":"dev","source":"pod","severity":"critical","namespace":"demo","object":"pod/a","reason":"CrashLoopBackOff"}`
	do(t, "POST", ts.URL+"/v1/agent/problems", agentTok, prob)
	other := `{"cluster":"dev","source":"pod","severity":"critical","namespace":"demo","object":"pod/b","reason":"CrashLoopBackOff"}`
	do(t, "POST", ts.URL+"/v1/agent/problems", agentTok, other)
	if len(s.notify.queue) != 1 {
		t.Fatalf("queued %d, want 1 (pod/b only)", len(s.notify.queue))
	}

	var ps []Problem
	json.NewDecoder(do(t, "GET", ts.URL+"/v1/clusters/dev/problems", appTok, "").Body).Decode(&ps)
	muted := map[string]string{}
	for _, p := range ps {
		if p.Muted != nil {
			muted[p.Object] = p.Muted.Note
		}
	}
	if len(muted) != 1 || muted["pod/a"] != "known" {
		t.Fatalf("muted = %v", muted)
	}

	var cs []ClusterView
	json.NewDecoder(do(t, "GET", ts.URL+"/v1/clusters", appTok, "").Body).Decode(&cs)
	if len(cs) != 1 || cs[0].ProblemsOpen != 1 || cs[0].ProblemsMuted != 1 {
		t.Fatalf("clusters = %+v, want 1 open and 1 muted", cs)
	}

	// Resolving: no "resolved" message either, and the acknowledgement ends.
	do(t, "POST", ts.URL+"/v1/agent/problems", agentTok, prob[:len(prob)-1]+`,"resolved":true}`)
	if len(s.notify.queue) != 1 {
		t.Fatalf("queued %d, the resolution of a muted problem must stay quiet", len(s.notify.queue))
	}
	var left []Mute
	json.NewDecoder(do(t, "GET", ts.URL+"/v1/clusters/dev/mutes", appTok, "").Body).Decode(&left)
	if len(left) != 0 {
		t.Fatalf("mutes = %+v, want none", left)
	}
	if r := do(t, "DELETE", ts.URL+"/v1/clusters/dev/mutes/"+m.ID, appTok, ""); r.StatusCode != 404 {
		t.Fatalf("already gone: %d", r.StatusCode)
	}
}
