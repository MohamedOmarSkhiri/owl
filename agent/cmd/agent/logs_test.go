package main

import (
	"testing"
	"time"
)

func TestSplitLog(t *testing.T) {
	raw := "2026-10-26T20:00:01.100000000Z first\n" +
		"2026-10-26T20:00:01.200000000Z second\n" +
		"2026-10-26T20:00:02.000000000Z third line, with spaces\n"

	// First call: every line, timestamps removed, cursor on the newest line.
	lines, last := splitLog(raw, time.Time{})
	if len(lines) != 3 || lines[0] != "first" || lines[2] != "third line, with spaces" {
		t.Fatalf("lines = %q", lines)
	}
	if last != "2026-10-26T20:00:02.000000000Z" {
		t.Fatalf("last = %q", last)
	}

	// Live call: the API rounds since down to the second and returns lines we already have.
	since, _ := time.Parse(time.RFC3339Nano, "2026-10-26T20:00:01.100000000Z")
	lines, last = splitLog(raw, since)
	if len(lines) != 2 || lines[0] != "second" {
		t.Fatalf("after since: %q", lines)
	}
	if last != "2026-10-26T20:00:02.000000000Z" {
		t.Fatalf("last = %q", last)
	}

	// Nothing new: no lines, empty cursor (the handler keeps the previous one).
	if lines, last = splitLog("", since); len(lines) != 0 || last != "" {
		t.Fatalf("empty: %q %q", lines, last)
	}
}
