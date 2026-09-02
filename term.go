package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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
	KeyLeft      KeyName = "left"
	KeyRight     KeyName = "right"
	KeyHome      KeyName = "home"
	KeyEnd       KeyName = "end"
	KeyDelete    KeyName = "delete"
	KeyTab       KeyName = "tab"
	KeyEsc       KeyName = "esc"
	KeyInterrupt KeyName = "interrupt"
	KeyEOF       KeyName = "eof"
	KeyBackspace KeyName = "backspace"
)

// Control characters the line editor binds, named so the switch that handles
// them reads as something other than a list of small integers.
const (
	ctrlA = 1
	ctrlB = 2
	ctrlD = 4
	ctrlE = 5
	ctrlF = 6
	ctrlK = 11
	ctrlU = 21
	ctrlW = 23
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
	// closeOnce guards teardown. The descriptor is deliberately not cleared
	// afterwards: the pump goroutine reads it every iteration, and clearing it
	// would be a data race with a nil dereference at the end of it.
	closeOnce sync.Once
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
// than once, and safe to call while the pump is mid-read.
func (r *rawTerminal) Restore() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.saved != "" {
			_, _ = stty(r.tty, r.saved)
		} else {
			_, _ = stty(r.tty, "-raw", "echo")
		}
		// Closing unblocks the pump, which then exits on the read error.
		r.tty.Close()
	})
}

// Width reports the terminal's column count, falling back to a conventional 80
// when it cannot be determined.
func (r *rawTerminal) Width() int {
	if r == nil || r.tty == nil {
		return fallbackWidth
	}
	return widthOf(r.tty)
}

const fallbackWidth = 80

// terminalWidth reports the controlling terminal's column count without
// needing a raw terminal in hand.
func terminalWidth() int {
	tty, err := os.OpenFile(ttyPath(), os.O_RDONLY, 0)
	if err != nil {
		return fallbackWidth
	}
	defer tty.Close()
	return widthOf(tty)
}

func widthOf(tty *os.File) int {
	out, err := stty(tty, "size")
	if err != nil {
		return fallbackWidth
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return fallbackWidth
	}
	cols, err := strconv.Atoi(fields[1])
	if err != nil || cols < minInputWidth {
		return fallbackWidth
	}
	return cols
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

// drainSettle is how long Drain waits for the pump to catch up. A terminal
// that has only just been switched to raw mode may still have type-ahead in
// the kernel's line buffer that the pump has not read yet.
const drainSettle = 10 * time.Millisecond

// Drain discards input that arrived before now.
//
// It is what keeps typing during a slow generation from answering the prompt
// that appears afterwards: those keystrokes were aimed at a question that had
// not been asked.
func (r *rawTerminal) Drain() {
	for {
		if _, ok := r.readByte(drainSettle); !ok {
			return
		}
	}
}

// WatchInterrupt calls cancel if ctrl-c is pressed before stop is called.
//
// While the model is working nothing reads the terminal, and raw mode means
// ctrl-c is a byte rather than a signal, so without this a slow request could
// not be given up on. Other keys pressed meanwhile are discarded, as Drain
// would discard them anyway.
func (r *rawTerminal) WatchInterrupt(cancel func()) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case b, ok := <-r.bytes:
				if !ok {
					return
				}
				if b == 3 {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// ReadKey blocks for one keypress.
func (r *rawTerminal) ReadKey() Key {
	b, ok := r.readByte(0)
	if !ok {
		return Key{Name: KeyInterrupt}
	}
	switch b {
	case 3: // ctrl-c
		return Key{Name: KeyInterrupt}
	case ctrlD:
		// Distinct from ctrl-c: on an empty line it means "no more input",
		// which the editor treats as leaving rather than cancelling.
		return Key{Name: KeyEOF}
	case '\r', '\n':
		return Key{Name: KeyEnter}
	case '\t':
		return Key{Name: KeyTab}
	case 127, 8:
		return Key{Name: KeyBackspace}
	case ctrlA:
		return Key{Name: KeyHome}
	case ctrlE:
		return Key{Name: KeyEnd}
	case ctrlB:
		return Key{Name: KeyLeft}
	case ctrlF:
		return Key{Name: KeyRight}
	case 27:
		return r.readEscape()
	}
	if b >= utf8.RuneSelf {
		return r.readMultibyte(b)
	}
	return Key{Rune: rune(b)}
}

// readMultibyte assembles one UTF-8 character from its bytes.
//
// The terminal delivers bytes, not characters. Treating each one as a rune
// turns every accented letter, dash and emoji into mojibake the moment it is
// echoed back, so the continuation bytes are gathered here instead.
func (r *rawTerminal) readMultibyte(first byte) Key {
	size := utf8SequenceLen(first)
	if size == 0 {
		// A stray continuation byte or an invalid leader: nothing sensible to
		// insert, so it is dropped rather than corrupting the line.
		return Key{Name: KeyNone}
	}
	buf := make([]byte, 1, size)
	buf[0] = first
	for len(buf) < size {
		b, ok := r.readByte(escapeGrace)
		if !ok {
			return Key{Name: KeyNone}
		}
		buf = append(buf, b)
	}
	rn, n := utf8.DecodeRune(buf)
	if rn == utf8.RuneError && n <= 1 {
		return Key{Name: KeyNone}
	}
	return Key{Rune: rn}
}

// utf8SequenceLen reports how many bytes the character starting with b
// occupies, or zero when b cannot start one.
func utf8SequenceLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	}
	return 0
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
	// Numeric sequences carry their meaning in digits before a trailing "~".
	if final >= '0' && final <= '9' {
		return r.readNumericEscape(final)
	}
	switch final {
	case 'A':
		return Key{Name: KeyUp}
	case 'B':
		return Key{Name: KeyDown}
	case 'C':
		return Key{Name: KeyRight}
	case 'D':
		return Key{Name: KeyLeft}
	case 'H':
		return Key{Name: KeyHome}
	case 'F':
		return Key{Name: KeyEnd}
	}
	// Some other sequence (modifiers, mouse). Treat it as no-op input rather
	// than guessing at a meaning.
	return Key{Name: KeyNone}
}

// readNumericEscape consumes the rest of a CSI sequence of the form ESC [ N ~.
func (r *rawTerminal) readNumericEscape(first byte) Key {
	digits := []byte{first}
	for len(digits) < 4 {
		b, ok := r.readByte(escapeGrace)
		if !ok {
			return Key{Name: KeyNone}
		}
		if b == '~' {
			break
		}
		if b < '0' || b > '9' {
			// A modifier form such as ESC [ 1 ; 5 C. Not bound; swallow it.
			return Key{Name: KeyNone}
		}
		digits = append(digits, b)
	}
	switch string(digits) {
	case "1", "7":
		return Key{Name: KeyHome}
	case "3":
		return Key{Name: KeyDelete}
	case "4", "8":
		return Key{Name: KeyEnd}
	}
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
