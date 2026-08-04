package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for concurrent use, since the spinner
// renders from its own goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSpinnerCyclesOneToThreeDots(t *testing.T) {
	buf := &syncBuffer{}
	sp := NewSpinner(buf, true)
	sp.interval = 5 * time.Millisecond
	sp.Start("Loading")
	time.Sleep(80 * time.Millisecond)
	sp.Stop()

	got := buf.String()
	for _, want := range []string{"Loading.", "Loading..", "Loading..."} {
		if !strings.Contains(got, want) {
			t.Errorf("spinner output missing %q", want)
		}
	}
	if strings.Contains(got, "Loading....") {
		t.Error("dots must wrap at three")
	}
}

func TestSpinnerStartsWithLoadingNotThinking(t *testing.T) {
	// Before the model emits anything we are waiting on the request, not on
	// reasoning; claiming "Thinking" there is a lie.
	buf := &syncBuffer{}
	sp := NewSpinner(buf, true)
	sp.interval = 5 * time.Millisecond
	sp.Start("Loading")
	time.Sleep(20 * time.Millisecond)
	sp.Stop()

	if strings.Contains(buf.String(), "Thinking") {
		t.Error("spinner should not say Thinking before any reasoning arrives")
	}
}

func TestSpinnerSetMessageSwitchesLabel(t *testing.T) {
	buf := &syncBuffer{}
	sp := NewSpinner(buf, true)
	sp.interval = 5 * time.Millisecond
	sp.Start("Loading")
	time.Sleep(20 * time.Millisecond)
	sp.SetMessage("Thinking")
	time.Sleep(40 * time.Millisecond)
	sp.Stop()

	got := buf.String()
	if !strings.Contains(got, "Loading") {
		t.Error("expected the initial Loading label")
	}
	if !strings.Contains(got, "Thinking") {
		t.Error("expected the label to switch to Thinking")
	}
	if strings.Index(got, "Loading") > strings.Index(got, "Thinking") {
		t.Error("Loading must appear before Thinking")
	}
}

func TestSpinnerClearsLineOnStop(t *testing.T) {
	buf := &syncBuffer{}
	sp := NewSpinner(buf, true)
	sp.interval = 5 * time.Millisecond
	sp.Start("Loading")
	time.Sleep(20 * time.Millisecond)
	sp.Stop()

	if !strings.HasSuffix(buf.String(), "\r\033[K") {
		t.Error("spinner must erase its line when stopped")
	}
}

func TestSpinnerDisabledWritesNothing(t *testing.T) {
	buf := &syncBuffer{}
	sp := NewSpinner(buf, false)
	sp.Start("Loading")
	sp.SetMessage("Thinking")
	time.Sleep(20 * time.Millisecond)
	sp.Stop()

	if buf.String() != "" {
		t.Errorf("disabled spinner wrote %q, want nothing", buf.String())
	}
}

func TestSpinnerStopIsIdempotent(t *testing.T) {
	buf := &syncBuffer{}
	sp := NewSpinner(buf, true)
	sp.interval = 5 * time.Millisecond
	sp.Start("Loading")
	time.Sleep(10 * time.Millisecond)
	sp.Stop()
	sp.Stop() // must not panic or deadlock
}

func TestSpinnerSetMessageWhileStoppingDoesNotDeadlock(t *testing.T) {
	// Stop holds the mutex while waiting for the render goroutine, so the label
	// must not be guarded by that same mutex.
	buf := &syncBuffer{}
	sp := NewSpinner(buf, true)
	sp.interval = time.Millisecond
	sp.Start("Loading")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			sp.SetMessage("Thinking")
		}
	}()
	sp.Stop()
	<-done

	select {
	case <-time.After(time.Second):
		t.Fatal("timed out")
	default:
	}
}
