package main

import "testing"

func TestHistoryWalksBackAndForward(t *testing.T) {
	h := &History{entries: []string{"first", "second", "third"}}
	h.pos = len(h.entries)

	if got, ok := h.Prev("draft"); !ok || got != "third" {
		t.Fatalf("Prev = %q, %v; want third", got, ok)
	}
	if got, ok := h.Prev("draft"); !ok || got != "second" {
		t.Fatalf("Prev = %q, %v; want second", got, ok)
	}
	if got, ok := h.Next(); !ok || got != "third" {
		t.Fatalf("Next = %q, %v; want third", got, ok)
	}
	// Walking back down past the newest entry restores what was being typed,
	// rather than leaving the recalled entry in the line.
	if got, ok := h.Next(); !ok || got != "draft" {
		t.Fatalf("Next = %q, %v; want the pending draft", got, ok)
	}
	if _, ok := h.Next(); ok {
		t.Error("there is nothing past the line being typed")
	}
}

func TestHistoryStopsAtTheOldest(t *testing.T) {
	h := &History{entries: []string{"only"}}
	h.pos = len(h.entries)
	if _, ok := h.Prev(""); !ok {
		t.Fatal("the one entry should be reachable")
	}
	if _, ok := h.Prev(""); ok {
		t.Error("there is nothing before the oldest entry")
	}
}

func TestHistorySkipsConsecutiveDuplicates(t *testing.T) {
	h := &History{path: ""} // no path: Add must not try to write
	h.Add("same")
	h.Add("same")
	h.Add("  same  ")
	if len(h.entries) != 1 {
		t.Errorf("entries = %v, want the repeat collapsed", h.entries)
	}
	h.Add("different")
	if len(h.entries) != 2 {
		t.Errorf("entries = %v, want the new request kept", h.entries)
	}
}

func TestHistoryAddRewindsTheCursor(t *testing.T) {
	// After submitting, Up must reach the request just made rather than
	// continuing from wherever browsing left off.
	h := &History{path: "", entries: []string{"a", "b"}}
	h.pos = 0
	h.Add("c")
	if got, ok := h.Prev(""); !ok || got != "c" {
		t.Errorf("Prev after Add = %q, %v; want the newest entry", got, ok)
	}
}

func TestHistoryIgnoresBlankRequests(t *testing.T) {
	h := &History{path: ""}
	h.Add("   ")
	h.Add("")
	if len(h.entries) != 0 {
		t.Errorf("entries = %v, want nothing recorded", h.entries)
	}
}
