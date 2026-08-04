package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// All decoration goes to stderr; only the generated command goes to stdout.
// That split is what makes `cmd -q "..." > script.sh` and
// `eval "$(cmd -q "...")"` behave sensibly.

// colorsEnabled reports whether ANSI escapes should be emitted. Honors the
// NO_COLOR convention and disables itself when stderr is not a terminal.
func colorsEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(os.Stderr)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

type palette struct {
	enabled bool
}

func (p palette) wrap(code, s string) string {
	if !p.enabled {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

// dimOn and reset bracket a long streamed run. Wrapping every delta with
// p.Dim would emit a full escape pair per character.
func (p palette) dimOn() string {
	if !p.enabled {
		return ""
	}
	return "\033[2m"
}

func (p palette) reset() string {
	if !p.enabled {
		return ""
	}
	return "\033[0m"
}

func (p palette) Cyan(s string) string   { return p.wrap("36", s) }
func (p palette) Yellow(s string) string { return p.wrap("1;33", s) }
func (p palette) Red(s string) string    { return p.wrap("1;31", s) }
func (p palette) Dim(s string) string    { return p.wrap("2", s) }
func (p palette) Green(s string) string  { return p.wrap("32", s) }

// Spinner renders an animated status line on stderr, cycling one to three
// trailing dots. It is a no-op when the output is not a terminal, so logs and
// pipes stay clean.
type Spinner struct {
	w        io.Writer
	enabled  bool
	stopCh   chan struct{}
	doneCh   chan struct{}
	mu       sync.Mutex
	running  bool
	interval time.Duration
	// msg is read by the render goroutine on every tick, so it is kept in an
	// atomic rather than under mu: Stop holds mu while waiting for that
	// goroutine to exit, and sharing the lock would deadlock.
	msg atomic.Value // string
}

func NewSpinner(w io.Writer, enabled bool) *Spinner {
	return &Spinner{
		w:        w,
		enabled:  enabled,
		interval: 300 * time.Millisecond,
	}
}

// SetMessage changes the label without restarting the animation. The dot count
// keeps cycling, so the line reads as one continuous progress indicator.
func (s *Spinner) SetMessage(msg string) {
	s.msg.Store(msg)
}

func (s *Spinner) message() string {
	if v, ok := s.msg.Load().(string); ok {
		return v
	}
	return ""
}

func (s *Spinner) Start(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.running {
		return
	}
	s.msg.Store(msg)
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})

	go func(stop <-chan struct{}, done chan<- struct{}) {
		defer close(done)
		dots := 1
		draw := func() {
			fmt.Fprintf(s.w, "\r\033[36m%s%s\033[0m\033[K", s.message(), strings.Repeat(".", dots))
		}
		// Draw immediately so there is no blank pause before the first tick.
		draw()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				fmt.Fprint(s.w, "\r\033[K")
				return
			case <-ticker.C:
				dots = dots%3 + 1
				draw()
			}
		}
	}(s.stopCh, s.doneCh)
}

func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	close(s.stopCh)
	<-s.doneCh
	s.running = false
}
