package main

import (
	"fmt"
	"io"
	"strings"
)

// A single-line editor with inline completion, built on the raw-mode terminal.
//
// It is deliberately small: this tool asks one question and exits, so it needs
// enough editing to fix a typo and enough completion to name a file without
// typing the whole path -- not a shell.

// Editor holds the state of one editing session.
type Editor struct {
	out    io.Writer
	term   *rawTerminal
	p      palette
	prompt string
	// root is where "@" completion searches from.
	root    string
	history *History

	line   []rune
	cursor int

	// suggestions is the completion list currently on screen.
	suggestions []Completion
	selected    int
	// drawnLines is how many lines the last render occupied, so the next one
	// can erase exactly that much.
	drawnLines int
	// width is the terminal's column count, sampled once per session of
	// editing. The input is kept to a single line by scrolling it sideways,
	// which is what makes the cursor arithmetic below correct: a line allowed
	// to wrap would put the cursor on the wrong row entirely.
	width int
}

// minInputWidth is the narrowest the input area is allowed to get, so an
// unreadable terminal size cannot collapse it to nothing.
const minInputWidth = 20

// EditResult says how an editing session ended.
type EditResult int

const (
	// EditSubmit means Enter was pressed; the text is returned.
	EditSubmit EditResult = iota
	// EditCancel means the line was abandoned (Escape, or ctrl-c).
	EditCancel
	// EditQuit means the session should end (ctrl-d on an empty line).
	EditQuit
)

// NewEditor prepares an editor on an already-raw terminal.
func NewEditor(out io.Writer, term *rawTerminal, p palette, prompt, root string, history *History) *Editor {
	return &Editor{out: out, term: term, p: p, prompt: prompt, root: root, history: history}
}

// SetPrompt changes the leading marker, so an editor reused for a different
// kind of input says which kind it is.
func (e *Editor) SetPrompt(prompt string) { e.prompt = prompt }

// Read runs the editing loop until the line is submitted or abandoned. initial
// pre-fills the line, which is how "edit this command before running it" works.
func (e *Editor) Read(initial string) (string, EditResult) {
	e.line = []rune(initial)
	e.cursor = len(e.line)
	e.suggestions = nil
	e.selected = 0
	e.drawnLines = 0
	e.width = e.term.Width()
	e.render()

	for {
		key := e.term.ReadKey()

		// While a completion list is open it owns the navigation keys.
		if len(e.suggestions) > 0 {
			if done := e.handleCompletionKey(key); done {
				continue
			}
		}

		switch key.Name {
		case KeyEnter:
			e.finish()
			return strings.TrimSpace(string(e.line)), EditSubmit
		case KeyInterrupt:
			e.finish()
			return "", EditCancel
		case KeyEOF:
			if len(e.line) == 0 {
				e.finish()
				return "", EditQuit
			}
			e.deleteForward()
		case KeyEsc:
			if len(e.line) == 0 {
				e.finish()
				return "", EditCancel
			}
			e.setLine(nil)
		case KeyBackspace:
			e.deleteBackward()
		case KeyDelete:
			e.deleteForward()
		case KeyLeft:
			if e.cursor > 0 {
				e.cursor--
			}
		case KeyRight:
			if e.cursor < len(e.line) {
				e.cursor++
			}
		case KeyHome:
			e.cursor = 0
		case KeyEnd:
			e.cursor = len(e.line)
		case KeyUp:
			e.recallPrev()
		case KeyDown:
			e.recallNext()
		case KeyTab:
			e.openCompletions()
		default:
			switch key.Rune {
			case 0:
				// An unbound sequence; nothing to insert.
			case ctrlU:
				e.setLine(nil)
			case ctrlK:
				e.setLine(e.line[:e.cursor])
			case ctrlW:
				e.deleteWord()
			default:
				if key.Rune >= ' ' {
					e.insert(key.Rune)
				}
			}
		}
		e.refreshCompletions()
		e.render()
	}
}

// handleCompletionKey routes a keypress to the open completion list. It
// reports whether the key was consumed.
func (e *Editor) handleCompletionKey(key Key) bool {
	switch key.Name {
	case KeyUp:
		e.selected = (e.selected - 1 + len(e.suggestions)) % len(e.suggestions)
	case KeyDown:
		e.selected = (e.selected + 1) % len(e.suggestions)
	case KeyTab:
		// Tab with a list open moves through it, so repeated presses browse
		// rather than re-opening the same list.
		e.selected = (e.selected + 1) % len(e.suggestions)
	case KeyEnter, KeyRight:
		// When the line already reads exactly like the selection, accepting it
		// would do nothing and Enter would appear to be ignored. Close the
		// list and let the key mean what it normally means.
		if e.completionIsNoop() {
			e.suggestions = nil
			return false
		}
		e.acceptCompletion()
	case KeyEsc:
		e.suggestions = nil
	default:
		return false
	}
	e.render()
	return true
}

// openCompletions builds a list for whatever the cursor is sitting in.
func (e *Editor) openCompletions() {
	e.selected = 0
	e.suggestions = e.completionsForCursor()
}

// refreshCompletions keeps an open list in step with the text, so it narrows
// as you type instead of going stale.
func (e *Editor) refreshCompletions() {
	if len(e.suggestions) == 0 {
		// A list opens by itself the moment a marker is typed: needing Tab to
		// discover that "@" does anything would defeat the point.
		if _, _, ok := activeToken(string(e.line), e.cursor, '@'); ok {
			e.openCompletions()
			return
		}
		if _, _, ok := activeToken(string(e.line), e.cursor, '/'); ok {
			e.openCompletions()
		}
		return
	}
	e.suggestions = e.completionsForCursor()
	if e.selected >= len(e.suggestions) {
		e.selected = 0
	}
}

// completionIsNoop reports whether accepting the selection would leave the
// line unchanged.
func (e *Editor) completionIsNoop() bool {
	if e.selected >= len(e.suggestions) {
		return true
	}
	choice := e.suggestions[e.selected]
	line, at := e.lineAndOffset()
	if _, prefix, ok := activeToken(line, at, '@'); ok {
		return prefix == choice.Text
	}
	if start, prefix, ok := activeToken(line, at, '/'); ok {
		return start == 0 && "/"+prefix == strings.TrimSpace(choice.Text)
	}
	return false
}

func (e *Editor) completionsForCursor() []Completion {
	line, at := e.lineAndOffset()
	if _, prefix, ok := activeToken(line, at, '@'); ok {
		return CompleteFiles(e.root, prefix)
	}
	if _, prefix, ok := activeToken(line, at, '/'); ok {
		return CompleteSlash("/" + prefix)
	}
	return nil
}

// lineAndOffset renders the line and converts the cursor from a rune index to
// the byte offset the string-based helpers expect. Mixing the two silently
// misreads any line containing a character outside ASCII.
func (e *Editor) lineAndOffset() (string, int) {
	return string(e.line), len(string(e.line[:e.cursor]))
}

// acceptCompletion replaces the token under the cursor with the selection.
func (e *Editor) acceptCompletion() {
	if e.selected >= len(e.suggestions) {
		return
	}
	choice := e.suggestions[e.selected]
	line, at := e.lineAndOffset()

	marker := byte('@')
	start, _, ok := activeToken(line, at, '@')
	if !ok {
		marker = '/'
		start, _, ok = activeToken(line, at, '/')
	}
	if !ok {
		return
	}

	// The marker is kept: it is what tells the request builder this word is a
	// file, and what makes a slash command recognisable.
	replacement := string(marker) + choice.Text
	if marker == '/' {
		replacement = choice.Text
	}
	head := line[:start] + replacement
	e.line = []rune(head + line[at:])
	e.cursor = len([]rune(head))

	// A directory is a step on the way somewhere, so its list stays open.
	if strings.HasSuffix(choice.Text, "/") {
		e.openCompletions()
		return
	}
	e.suggestions = nil
}

func (e *Editor) insert(r rune) {
	e.line = append(e.line, 0)
	copy(e.line[e.cursor+1:], e.line[e.cursor:])
	e.line[e.cursor] = r
	e.cursor++
}

func (e *Editor) deleteBackward() {
	if e.cursor == 0 {
		return
	}
	e.line = append(e.line[:e.cursor-1], e.line[e.cursor:]...)
	e.cursor--
}

func (e *Editor) deleteForward() {
	if e.cursor >= len(e.line) {
		return
	}
	e.line = append(e.line[:e.cursor], e.line[e.cursor+1:]...)
}

// deleteWord removes the word before the cursor, skipping any spaces first so
// that ctrl-w at the end of "foo bar " removes "bar".
func (e *Editor) deleteWord() {
	i := e.cursor
	for i > 0 && e.line[i-1] == ' ' {
		i--
	}
	for i > 0 && e.line[i-1] != ' ' {
		i--
	}
	e.line = append(e.line[:i], e.line[e.cursor:]...)
	e.cursor = i
}

func (e *Editor) setLine(r []rune) {
	e.line = append([]rune(nil), r...)
	e.cursor = len(e.line)
}

func (e *Editor) recallPrev() {
	if e.history == nil {
		return
	}
	if entry, ok := e.history.Prev(string(e.line)); ok {
		e.setLine([]rune(entry))
	}
}

func (e *Editor) recallNext() {
	if e.history == nil {
		return
	}
	if entry, ok := e.history.Next(); ok {
		e.setLine([]rune(entry))
	}
}

// render repaints the input line and any completion list below it.
//
// Raw mode does no newline translation, so every line break is an explicit
// carriage return plus line feed.
func (e *Editor) render() {
	// Every render both starts and ends with the cursor on the input line, so
	// there is nothing to walk up here: clearing from this point down removes
	// the old input line and whatever list was drawn under it. Moving up first
	// would eat the lines above the prompt, which is not this editor's screen.
	fmt.Fprint(e.out, "\r\033[J")

	visible, cursorCol := e.window()
	fmt.Fprintf(e.out, "%s%s", e.p.Yellow(e.prompt), visible)

	lines := 1
	for i, s := range e.suggestions {
		marker, body := "  ", s.Text
		if i == e.selected {
			marker = e.p.Cyan("❯ ")
			body = e.p.Cyan(s.Text)
		}
		if s.Hint != "" {
			body += "  " + e.p.Dim(s.Hint)
		}
		// A row wider than the terminal wraps, and a wrapped row makes the
		// line count below wrong -- which is what turns a repaint into a
		// scribble further up the screen.
		fmt.Fprintf(e.out, "\r\n%s%s", marker, e.truncate(body, e.width-2))
		lines++
	}
	e.drawnLines = lines

	// Put the cursor back where the text says it is: up out of the list, then
	// across from the start of the input line.
	if lines > 1 {
		fmt.Fprintf(e.out, "\033[%dA", lines-1)
	}
	if cursorCol > 0 {
		fmt.Fprintf(e.out, "\r\033[%dC", cursorCol)
	} else {
		fmt.Fprint(e.out, "\r")
	}
}

// window returns the slice of the line that fits on screen, and the column the
// cursor belongs in. A line longer than the terminal scrolls under the cursor
// rather than wrapping.
func (e *Editor) window() (string, int) {
	promptWidth := visibleWidth(e.prompt)
	avail := e.width - promptWidth - 1
	if avail < minInputWidth {
		avail = minInputWidth
	}
	if len(e.line) <= avail {
		return string(e.line), promptWidth + e.cursor
	}
	// Keep the cursor in view, preferring to show what comes before it.
	start := e.cursor - avail
	if start < 0 {
		start = 0
	}
	end := start + avail
	if end > len(e.line) {
		end = len(e.line)
	}
	return string(e.line[start:end]), promptWidth + (e.cursor - start)
}

// finish clears the completion list and leaves the cursor on a fresh line.
func (e *Editor) finish() {
	e.suggestions = nil
	e.render()
	fmt.Fprint(e.out, "\r\n")
	e.drawnLines = 0
}

// truncate cuts a decorated string to a column budget, leaving any ANSI
// escapes it passes through intact so the row cannot wrap.
func (e *Editor) truncate(s string, max int) string {
	if max < 1 {
		max = 1
	}
	if visibleWidth(s) <= max {
		return s
	}
	var b strings.Builder
	width, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == '\033':
			inEscape = true
			b.WriteRune(r)
		case inEscape:
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
		default:
			if width >= max {
				continue
			}
			b.WriteRune(r)
			width++
		}
	}
	return b.String()
}

// visibleWidth counts printable columns, ignoring ANSI escapes, so the cursor
// is positioned by what is on screen rather than by byte count.
func visibleWidth(s string) int {
	width, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == '\033':
			inEscape = true
		case inEscape:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
		default:
			width++
		}
	}
	return width
}
