package main

import (
	"os"
	"os/exec"
	"strings"
	"time"
)

// Raw-mode terminal input, used by the confirmation prompt and the configure
// wizard so both answer to a single keypress instead of a typed line.
//
// This is done by shelling out to stty rather than by calling tcsetattr,
// because the termios constants differ per platform and the alternative is a
// dependency on golang.org/x/term. Keeping the module free of dependencies is
// worth two ~1ms subprocesses on a prompt a human is about to look at anyway.

// KeyName labels the keys that are not ordinary characters.
type KeyName string

const (
	KeyNone      KeyName = ""
	KeyEnter     KeyName = "enter"
	KeyUp        KeyName = "up"
	KeyDown      KeyName = "down"
	KeyEsc       KeyName = "esc"
	KeyInterrupt KeyName = "interrupt"
	KeyBackspace KeyName = "backspace"
)

// Key is one keypress: either a printable rune or a named control key.
type Key struct {
	Rune rune
	Name KeyName
}

// rawTerminal holds a terminal switched into raw mode, and the state needed to
// put it back.
type rawTerminal struct {
	tty   *os.File
	saved string
	// bytes is fed by a single pump goroutine. Reading through a channel
	// rather than calling Read directly is what makes a timed read safe: a
	// byte that arrives after the deadline stays queued for the next read
	// instead of being consumed by an abandoned Read call.
	bytes chan byte
}

// enterRaw switches the controlling terminal to raw mode.
//
// It reports false when there is no terminal to switch, or when stty is
// unavailable, so callers can fall back to reading a line.
func enterRaw() (*rawTerminal, bool) {
	tty, err := os.OpenFile(ttyPath(), os.O_RDWR, 0)
	if err != nil {
		return nil, false
	}
	saved, err := stty(tty, "-g")
	if err != nil {
		tty.Close()
		return nil, false
	}
	if _, err := stty(tty, "raw", "-echo"); err != nil {
		tty.Close()
		return nil, false
	}
	r := &rawTerminal{tty: tty, saved: strings.TrimSpace(saved), bytes: make(chan byte, 64)}
	go r.pump()
	return r, true
}

// pump reads the terminal until it closes, which happens when Restore closes
// the descriptor.
func (r *rawTerminal) pump() {
	defer close(r.bytes)
	var buf [16]byte
	for {
		n, err := r.tty.Read(buf[:])
		for i := 0; i < n; i++ {
			r.bytes <- buf[i]
		}
		if err != nil {
			return
		}
	}
}

// Restore returns the terminal to the state it was in. It is safe to call more
// than once.
func (r *rawTerminal) Restore() {
	if r == nil || r.tty == nil {
		return
	}
	if r.saved != "" {
		_, _ = stty(r.tty, r.saved)
	} else {
		_, _ = stty(r.tty, "-raw", "echo")
	}
	r.tty.Close()
	r.tty = nil
}

func stty(tty *os.File, args ...string) (string, error) {
	c := exec.Command("stty", args...)
	c.Stdin = tty
	out, err := c.Output()
	return string(out), err
}

// escapeGrace is how long to wait for the rest of an escape sequence before
// deciding the user pressed a bare Escape. Terminals send the whole sequence in
// one burst, so this only ever costs time on an actual Escape press.
const escapeGrace = 40 * time.Millisecond

// ReadKey blocks for one keypress.
func (r *rawTerminal) ReadKey() Key {
	b, ok := r.readByte(0)
	if !ok {
		return Key{Name: KeyInterrupt}
	}
	switch b {
	case 3, 4: // ctrl-c, ctrl-d
		return Key{Name: KeyInterrupt}
	case '\r', '\n':
		return Key{Name: KeyEnter}
	case 127, 8:
		return Key{Name: KeyBackspace}
	case 27:
		return r.readEscape()
	}
	return Key{Rune: rune(b)}
}

// readEscape distinguishes a bare Escape from an arrow key.
func (r *rawTerminal) readEscape() Key {
	next, ok := r.readByte(escapeGrace)
	if !ok || next != '[' {
		return Key{Name: KeyEsc}
	}
	final, ok := r.readByte(escapeGrace)
	if !ok {
		return Key{Name: KeyEsc}
	}
	switch final {
	case 'A':
		return Key{Name: KeyUp}
	case 'B':
		return Key{Name: KeyDown}
	}
	// Some other sequence (page keys, modifiers). Treat it as no-op input
	// rather than guessing at a meaning.
	return Key{Name: KeyNone}
}

// readByte takes one byte from the pump, optionally giving up after a timeout.
// A zero timeout blocks until a byte arrives or the terminal closes.
func (r *rawTerminal) readByte(timeout time.Duration) (byte, bool) {
	if timeout <= 0 {
		b, ok := <-r.bytes
		return b, ok
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case b, ok := <-r.bytes:
		return b, ok
	case <-timer.C:
		return 0, false
	}
}
