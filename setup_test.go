package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSelectFallsBackToNumberedList(t *testing.T) {
	// With no terminal to put in raw mode -- a test, a pipe, a CI job -- the
	// picker must still be answerable.
	var out bytes.Buffer
	choices := []Choice{
		{Label: "OpenRouter", Value: "openrouter"},
		{Label: "Claude", Value: "claude"},
		{Label: "Antigravity", Value: "antigravity"},
	}
	w := newPrompter(&out, strings.NewReader("2\n"), palette{})
	got, ok := w.Select("Which backend?", choices, 0)
	if !ok || got != "claude" {
		t.Errorf("Select = %q, %v; want claude, true", got, ok)
	}
	if !strings.Contains(out.String(), "[2] Claude") {
		t.Errorf("the numbered list should be rendered, got:\n%s", out.String())
	}
}

func TestSelectEmptyAnswerKeepsCurrent(t *testing.T) {
	var out bytes.Buffer
	choices := []Choice{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}}
	w := newPrompter(&out, strings.NewReader("\n"), palette{})
	got, ok := w.Select("pick", choices, 1)
	if !ok || got != "b" {
		t.Errorf("Select = %q, %v; want the current entry b", got, ok)
	}
}

func TestSelectRejectsOutOfRange(t *testing.T) {
	var out bytes.Buffer
	choices := []Choice{{Label: "a", Value: "a"}}
	w := newPrompter(&out, strings.NewReader("9\n"), palette{})
	if _, ok := w.Select("pick", choices, 0); ok {
		t.Error("an out-of-range answer must not resolve to a choice")
	}
}

func TestPromptDefaultsOnEmptyInput(t *testing.T) {
	var out bytes.Buffer
	if got := newPrompter(&out, strings.NewReader("\n"), palette{}).Prompt("Model:", "haiku"); got != "haiku" {
		t.Errorf("Prompt = %q, want the default", got)
	}
	if got := newPrompter(&out, strings.NewReader("sonnet\n"), palette{}).Prompt("Model:", "haiku"); got != "sonnet" {
		t.Errorf("Prompt = %q, want the typed answer", got)
	}
}

func TestMaskedHidesTheMiddle(t *testing.T) {
	key := "sk-or-v1-0123456789abcdef"
	got := masked(key)
	if strings.Contains(got, "0123456789abcdef") {
		t.Errorf("masked(%q) = %q, the secret must not survive", key, got)
	}
	if !strings.HasPrefix(got, "sk-o") {
		t.Errorf("masked = %q, want a recognisable prefix", got)
	}
}

func TestActionForKey(t *testing.T) {
	tests := []struct {
		key  Key
		want Action
	}{
		{Key{Rune: 'y'}, ActionRun},
		{Key{Rune: 'Y'}, ActionRun},
		{Key{Rune: 'r'}, ActionRun},
		{Key{Rune: 'c'}, ActionCopy},
		{Key{Rune: 'C'}, ActionCopy},
		{Key{Rune: 'e'}, ActionEdit},
		{Key{Rune: 'n'}, ActionAbort},
		{Key{Rune: 'q'}, ActionAbort},
		{Key{Name: KeyEsc}, ActionAbort},
		{Key{Name: KeyInterrupt}, ActionAbort},
		{Key{Name: KeyEOF}, ActionAbort},
		// Enter is as likely to be left over from typing the request as it is
		// to be an answer, so it must not run anything.
		{Key{Name: KeyEnter}, ActionAbort},
	}
	for _, tt := range tests {
		if got := actionForKey(tt.key, false); got != tt.want {
			t.Errorf("actionForKey(%+v) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestActionForKeyWithCopyMode(t *testing.T) {
	// /copy changes what the affirmative key does, but an explicit key must
	// keep meaning what it says.
	if got := actionForKey(Key{Rune: 'y'}, true); got != ActionCopy {
		t.Errorf("y in copy mode = %v, want copy", got)
	}
	if got := actionForKey(Key{Rune: 'r'}, true); got != ActionRun {
		t.Errorf("r in copy mode = %v, want run: an explicit key must not be remapped", got)
	}
	if got := actionForKey(Key{Rune: 'c'}, true); got != ActionCopy {
		t.Errorf("c in copy mode = %v, want copy", got)
	}
}

func TestSelectWithCustomKeepsUnlistedCurrentValue(t *testing.T) {
	// A model that is not on the shortlist must not be silently dropped when
	// the wizard is re-run.
	var out bytes.Buffer
	w := newPrompter(&out, strings.NewReader("1\n"), palette{})
	got, ok := selectWithCustom(w, "Which model?", suggestedOpenRouterModels, "some/exotic-model", "Model slug:")
	if !ok {
		t.Fatal("selection failed")
	}
	if !strings.Contains(out.String(), "some/exotic-model") {
		t.Errorf("the configured model should be offered, got:\n%s", out.String())
	}
	_ = got
}

func TestPrompterKeepsStreamAcrossQuestions(t *testing.T) {
	// A separate reader per question would read ahead and discard the rest of
	// the stream, so the second answer would come back empty.
	var out bytes.Buffer
	w := newPrompter(&out, strings.NewReader("2\nsecond\nthird\n"), palette{})

	choice, ok := w.Select("pick", []Choice{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}}, 0)
	if !ok || choice != "b" {
		t.Fatalf("Select = %q, %v", choice, ok)
	}
	if got := w.Prompt("second?", ""); got != "second" {
		t.Errorf("second answer = %q, want %q", got, "second")
	}
	if got := w.Prompt("third?", ""); got != "third" {
		t.Errorf("third answer = %q, want %q", got, "third")
	}
}
