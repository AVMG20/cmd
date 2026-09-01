package main

import (
	"strings"
	"testing"
)

func TestParseAgyOutputSuccess(t *testing.T) {
	out := `{"conversation_id":"abc","status":"SUCCESS","response":"jq -r '.[].title' todo.json\n","duration_seconds":1.2,"num_turns":1}`
	ev, err := ParseAgyOutput(out)
	if err != nil {
		t.Fatalf("ParseAgyOutput: %v", err)
	}
	if ev.Kind != EventText {
		t.Errorf("kind = %v, want text", ev.Kind)
	}
	if !strings.Contains(ev.Text, "jq -r") {
		t.Errorf("text = %q", ev.Text)
	}
}

func TestParseAgyOutputReportsError(t *testing.T) {
	// A bad --model exits non-zero but still prints the envelope, and its
	// error field says far more than the exit status does.
	out := `{"conversation_id":"","status":"ERROR","response":"","error":"model does-not-exist is not recognized"}`
	ev, err := ParseAgyOutput(out)
	if err != nil {
		t.Fatalf("ParseAgyOutput: %v", err)
	}
	if ev.Kind != EventError || !strings.Contains(ev.Text, "not recognized") {
		t.Errorf("event = %+v, want the CLI's own error message", ev)
	}
}

func TestParseAgyOutputToleratesLeadingNoise(t *testing.T) {
	out := "warning: something on stdout\n" + `{"status":"SUCCESS","response":"ls -la"}`
	ev, err := ParseAgyOutput(out)
	if err != nil {
		t.Fatalf("ParseAgyOutput: %v", err)
	}
	if ev.Text != "ls -la" {
		t.Errorf("text = %q", ev.Text)
	}
}

func TestParseAgyOutputEmpty(t *testing.T) {
	if _, err := ParseAgyOutput("   "); err == nil {
		t.Error("empty output should be an error, not an empty command")
	}
}

func TestBuildAgyArgsPinsEffortLow(t *testing.T) {
	args := strings.Join(buildAgyArgs(DefaultConfig(), "list files", false), " ")
	if !strings.Contains(args, "--effort low") {
		t.Errorf("args = %q, want effort pinned low on a plain run", args)
	}
	if !strings.Contains(args, "--output-format json") {
		t.Errorf("args = %q, want the json envelope", args)
	}
	if strings.Contains(args, "--dangerously-skip-permissions") {
		t.Error("tool permissions must not be waived; soft-denial is what keeps agy from acting on its own")
	}
}

func TestBuildAgyArgsOmitsModelWhenUnset(t *testing.T) {
	// Headless agy fails loudly on an unknown model, so saying nothing is
	// safer than guessing a slug.
	cfg := DefaultConfig()
	cfg.AgyModel = ""
	if strings.Contains(strings.Join(buildAgyArgs(cfg, "x", false), " "), "--model") {
		t.Error("no --model flag should be sent when none is configured")
	}

	cfg.AgyModel = "gemini-3.5-flash-medium"
	if !strings.Contains(strings.Join(buildAgyArgs(cfg, "x", false), " "), "--model gemini-3.5-flash-medium") {
		t.Error("a configured model must be passed through")
	}
}

func TestAgyEffortClampsToSupportedLevels(t *testing.T) {
	// The CLI accepts only low, medium and high.
	for _, tt := range []struct{ in, want string }{
		{"low", "low"}, {"medium", "medium"}, {"high", "high"},
		{"xhigh", "high"}, {"max", "high"}, {"nonsense", "medium"},
	} {
		if got := agyEffort(tt.in); got != tt.want {
			t.Errorf("agyEffort(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
