package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// historyLimit is how many requests are kept. Requests are short and this is
// read once at startup, so the bound is about keeping the file tidy rather
// than about performance.
const historyLimit = 500

// History is the list of past requests, oldest first, with a cursor for
// up/down navigation.
type History struct {
	entries []string
	path    string
	// pos is the index being shown; len(entries) means "the line being typed".
	pos int
	// pending holds the partially typed line while browsing, so walking up
	// through history and back down returns what was actually being written.
	pending string
}

// LoadHistory reads the history file. A missing or unreadable file is not an
// error: history is a convenience, and losing it must never stop the tool.
func LoadHistory() *History {
	h := &History{path: historyPath()}
	f, err := os.Open(h.path)
	if err != nil {
		h.pos = len(h.entries)
		return h
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		if line := strings.TrimRight(sc.Text(), "\r\n"); line != "" {
			h.entries = append(h.entries, line)
		}
	}
	if len(h.entries) > historyLimit {
		h.entries = h.entries[len(h.entries)-historyLimit:]
	}
	h.pos = len(h.entries)
	return h
}

func historyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cmd-history"
	}
	return filepath.Join(home, ".cmd-history")
}

// Add records a request and rewinds the cursor. A repeat of the most recent
// entry is not stored twice.
func (h *History) Add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == line {
		h.pos = len(h.entries)
		return
	}
	h.entries = append(h.entries, line)
	if len(h.entries) > historyLimit {
		h.entries = h.entries[len(h.entries)-historyLimit:]
	}
	h.pos = len(h.entries)
	h.save()
}

// Prev walks back through history, returning the entry to show.
func (h *History) Prev(current string) (string, bool) {
	if h.pos == len(h.entries) {
		h.pending = current
	}
	if h.pos == 0 {
		return "", false
	}
	h.pos--
	return h.entries[h.pos], true
}

// Next walks forward, ending at the line that was being typed.
func (h *History) Next() (string, bool) {
	if h.pos >= len(h.entries) {
		return "", false
	}
	h.pos++
	if h.pos == len(h.entries) {
		return h.pending, true
	}
	return h.entries[h.pos], true
}

// Reset points the cursor back at the line being typed.
func (h *History) Reset() {
	h.pos = len(h.entries)
	h.pending = ""
}

// save rewrites the file. Failures are ignored on purpose: a read-only home
// directory is a reason to lose history, not a reason to fail a command.
func (h *History) save() {
	if h.path == "" {
		return
	}
	var b strings.Builder
	for _, e := range h.entries {
		b.WriteString(e)
		b.WriteByte('\n')
	}
	_ = os.WriteFile(h.path, []byte(b.String()), 0o600)
}
