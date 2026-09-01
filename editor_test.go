package main

import (
	"strings"
	"testing"
)

// newTestEditor builds an editor with no terminal behind it, for exercising
// the pure text manipulation.
func newTestEditor(line string, cursorRunes int) *Editor {
	e := &Editor{p: palette{}, prompt: "› ", root: ".", width: 80}
	e.line = []rune(line)
	e.cursor = cursorRunes
	return e
}

func TestLineAndOffsetConvertsRunesToBytes(t *testing.T) {
	// The cursor is a rune index; activeToken and friends want a byte offset.
	// Conflating them misreads every line holding a character outside ASCII.
	e := newTestEditor("café @us", 8)
	line, at := e.lineAndOffset()
	if at != len(line) {
		t.Fatalf("offset = %d, want %d (the byte length)", at, len(line))
	}
	_, prefix, ok := activeToken(line, at, '@')
	if !ok || prefix != "us" {
		t.Errorf("prefix = %q, %v; want \"us\", true", prefix, ok)
	}
}

func TestAcceptCompletionWithMultibyteText(t *testing.T) {
	e := newTestEditor("café @us", 8)
	e.suggestions = []Completion{{Text: "users.csv"}}
	e.acceptCompletion()

	if got := string(e.line); got != "café @users.csv" {
		t.Errorf("line = %q, want %q", got, "café @users.csv")
	}
	if e.cursor != len([]rune("café @users.csv")) {
		t.Errorf("cursor = %d, want the end of the line", e.cursor)
	}
}

func TestAcceptCompletionKeepsTheTail(t *testing.T) {
	// Completing mid-line must not eat what follows the cursor.
	e := newTestEditor("count @us and stop", 9)
	e.suggestions = []Completion{{Text: "users.csv"}}
	e.acceptCompletion()

	if got := string(e.line); got != "count @users.csv and stop" {
		t.Errorf("line = %q", got)
	}
}

func TestAcceptSlashCompletionDropsTheDuplicateMarker(t *testing.T) {
	e := newTestEditor("/con", 4)
	e.suggestions = []Completion{{Text: "/config"}}
	e.acceptCompletion()
	if got := string(e.line); got != "/config" {
		t.Errorf("line = %q, want /config", got)
	}
}

func TestWindowScrollsALongLine(t *testing.T) {
	// A line wider than the terminal has to scroll, not wrap: the cursor is
	// positioned by column, and a wrapped line puts it on the wrong row.
	long := strings.Repeat("x", 200)
	e := newTestEditor(long, 200)
	e.width = 40

	visible, col := e.window()
	if len([]rune(visible)) > e.width {
		t.Errorf("visible width %d exceeds the terminal", len([]rune(visible)))
	}
	if col >= e.width {
		t.Errorf("cursor column %d is off the edge of a %d-column terminal", col, e.width)
	}
}

func TestWindowLeavesShortLinesAlone(t *testing.T) {
	e := newTestEditor("short", 5)
	visible, col := e.window()
	if visible != "short" {
		t.Errorf("visible = %q, want the whole line", visible)
	}
	if want := visibleWidth("› ") + 5; col != want {
		t.Errorf("column = %d, want %d", col, want)
	}
}

func TestEditingOperations(t *testing.T) {
	e := newTestEditor("hello world", 11)

	e.deleteWord()
	if string(e.line) != "hello " {
		t.Errorf("after ctrl-w: %q, want %q", string(e.line), "hello ")
	}

	e.cursor = 0
	e.insert('X')
	if string(e.line) != "Xhello " {
		t.Errorf("after insert at start: %q", string(e.line))
	}

	e.deleteForward()
	if string(e.line) != "Xello " {
		t.Errorf("after delete: %q", string(e.line))
	}

	e.cursor = len(e.line)
	e.deleteBackward()
	if string(e.line) != "Xello" {
		t.Errorf("after backspace: %q", string(e.line))
	}
}

func TestDeleteWordSkipsTrailingSpaces(t *testing.T) {
	e := newTestEditor("one two   ", 10)
	e.deleteWord()
	if got := string(e.line); got != "one " {
		t.Errorf("got %q, want %q", got, "one ")
	}
}

func TestEditingAtTheEdgesIsSafe(t *testing.T) {
	// Backspace at the start and delete at the end must not panic.
	e := newTestEditor("", 0)
	e.deleteBackward()
	e.deleteForward()
	e.deleteWord()
	if len(e.line) != 0 {
		t.Errorf("line = %q, want it still empty", string(e.line))
	}
}
