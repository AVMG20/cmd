package main

import (
	"bytes"
	"strings"
	"testing"
)

// screen is enough of a terminal to answer the only question these tests ask:
// which rows did a repaint touch? Row 0 is where the cursor sat when the
// render began, and a negative row means the editor wrote above its own input
// line -- over the banner, which is not its screen to erase.
type screen struct {
	row    int
	minRow int
}

func (s *screen) feed(out string) {
	r := []rune(out)
	for i := 0; i < len(r); i++ {
		switch {
		case r[i] == '\n':
			s.row++
		case r[i] == '\033' && i+1 < len(r) && r[i+1] == '[':
			j := i + 2
			num := 0
			for j < len(r) && r[j] >= '0' && r[j] <= '9' {
				num = num*10 + int(r[j]-'0')
				j++
			}
			if j < len(r) {
				switch r[j] {
				case 'A':
					if num == 0 {
						num = 1
					}
					s.row -= num
				case 'B':
					if num == 0 {
						num = 1
					}
					s.row += num
				}
			}
			i = j
		}
		if s.row < s.minRow {
			s.minRow = s.row
		}
	}
}

func testEditor(out *bytes.Buffer) *Editor {
	e := &Editor{out: out, p: palette{}, prompt: requestPrompt, width: 80}
	e.line = []rune("/")
	e.cursor = 1
	return e
}

func suggestions(n int) []Completion {
	out := make([]Completion, n)
	for i := range out {
		out[i] = Completion{Text: "/command", Hint: "does a thing"}
	}
	return out
}

// A repaint with a completion list open must not walk up past its own input
// line. It used to: the previous render already left the cursor there, so
// moving up drawnLines-1 again erased the banner above it.
func TestRenderStaysBelowItsOwnLine(t *testing.T) {
	var buf bytes.Buffer
	e := testEditor(&buf)
	e.suggestions = suggestions(6)

	e.render()
	buf.Reset()

	// Backspace, then a narrowing keystroke -- the two actions that showed the
	// bug.
	e.suggestions = suggestions(3)
	e.render()
	e.suggestions = suggestions(1)
	e.render()

	var s screen
	s.feed(buf.String())
	if s.minRow < 0 {
		t.Fatalf("render moved %d lines above the input line", -s.minRow)
	}
}

// Every render ends where it began, so the next one can clear from there.
func TestRenderEndsOnInputLine(t *testing.T) {
	var buf bytes.Buffer
	e := testEditor(&buf)

	for _, n := range []int{0, 5, 2, 0, 8} {
		buf.Reset()
		e.suggestions = suggestions(n)
		e.render()

		var s screen
		s.feed(buf.String())
		if s.row != 0 {
			t.Fatalf("with %d suggestions the cursor ended %d lines off the input line", n, s.row)
		}
	}
}

// A row wider than the terminal wraps, and a wrapped row makes drawnLines lie.
func TestRenderRowsFitTheTerminal(t *testing.T) {
	var buf bytes.Buffer
	e := testEditor(&buf)
	e.width = 30
	e.suggestions = []Completion{{Text: strings.Repeat("x", 200), Hint: strings.Repeat("y", 200)}}
	e.render()

	for _, line := range strings.Split(buf.String(), "\r\n")[1:] {
		// The cursor-restore sequence trails the last row; it is not content.
		line, _, _ = strings.Cut(line, "\r")
		if w := visibleWidth(line); w > e.width {
			t.Fatalf("row is %d columns wide on a %d column terminal", w, e.width)
		}
	}
}
