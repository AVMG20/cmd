package main

import (
	"strings"
	"testing"
)

// collect runs ParseStream over a canned transcript and groups the results.
func collect(t *testing.T, transcript string) (text, thinking string, errs []string) {
	t.Helper()
	var tb, kb strings.Builder
	err := ParseStream(strings.NewReader(transcript), func(ev Event) {
		switch ev.Kind {
		case EventText:
			tb.WriteString(ev.Text)
		case EventThinking:
			kb.WriteString(ev.Text)
		case EventError:
			errs = append(errs, ev.Text)
		}
	})
	if err != nil {
		t.Fatalf("ParseStream() error = %v", err)
	}
	return tb.String(), kb.String(), errs
}

func TestParseStreamPartialDeltas(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"abc"}`,
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ls "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"-la"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"ls -la"}`,
	}, "\n")

	text, _, errs := collect(t, transcript)
	if text != "ls -la" {
		t.Errorf("text = %q, want %q", text, "ls -la")
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestParseStreamDoesNotDuplicateFinalMessage(t *testing.T) {
	// The CLI emits partial deltas AND the assembled assistant message. Emitting
	// both would double the command, which is the bug this guards against.
	transcript := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"echo hi"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"echo hi"}]}}`,
		`{"type":"result","subtype":"success","is_error":false}`,
	}, "\n")

	text, _, _ := collect(t, transcript)
	if text != "echo hi" {
		t.Errorf("text = %q, want %q (duplicated?)", text, "echo hi")
	}
}

func TestParseStreamFallsBackToAssistantMessage(t *testing.T) {
	// Without --include-partial-messages support there are no deltas at all,
	// and the whole message must still be recovered.
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"find . -name '*.go'"}]}}`,
		`{"type":"result","subtype":"success","is_error":false}`,
	}, "\n")

	text, _, _ := collect(t, transcript)
	if text != "find . -name '*.go'" {
		t.Errorf("text = %q", text)
	}
}

func TestParseStreamThinkingDeltas(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"needs find"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"find ."}}}`,
	}, "\n")

	text, thinking, _ := collect(t, transcript)
	if thinking != "needs find" {
		t.Errorf("thinking = %q", thinking)
	}
	if text != "find ." {
		t.Errorf("text = %q", text)
	}
}

func TestParseStreamDoesNotDuplicateThinking(t *testing.T) {
	// The CLI closes out the thinking block with a full assistant message
	// BEFORE the first text delta arrives, so a flag that only tracks text
	// deltas lets the reasoning through a second time.
	transcript := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"the user wants X"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"the user wants X"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"history -c"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"history -c"}]}}`,
		`{"type":"result","subtype":"success","is_error":false}`,
	}, "\n")

	text, thinking, _ := collect(t, transcript)
	if thinking != "the user wants X" {
		t.Errorf("thinking = %q, want it exactly once", thinking)
	}
	if text != "history -c" {
		t.Errorf("text = %q, want it exactly once", text)
	}
}

func TestParseStreamThinkingOnlyInFinalMessage(t *testing.T) {
	// No thinking deltas at all: the block must still surface once.
	transcript := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"reasoning here"},{"type":"text","text":"pwd"}]}}`,
	}, "\n")

	text, thinking, _ := collect(t, transcript)
	if thinking != "reasoning here" {
		t.Errorf("thinking = %q", thinking)
	}
	if text != "pwd" {
		t.Errorf("text = %q", text)
	}
}

func TestParseStreamAuthError(t *testing.T) {
	// Captured from a real run of the CLI without a login.
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Not logged in · Please run /login"}],"model":"<synthetic>"},"error":"authentication_failed","is_api_error_message":true}`,
		`{"is_error":true,"subtype":"success","result":"Not logged in · Please run /login","type":"result"}`,
	}, "\n")

	text, _, errs := collect(t, transcript)
	if text != "" {
		t.Errorf("text = %q, want empty on auth failure", text)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "Not logged in") {
		t.Fatalf("errs = %v, want one auth error", errs)
	}
}

func TestParseStreamIgnoresGarbageLines(t *testing.T) {
	transcript := strings.Join([]string{
		`Warning: no stdin data received in 3s, proceeding without it.`,
		``,
		`not json at all`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"pwd"}}}`,
	}, "\n")

	text, _, errs := collect(t, transcript)
	if text != "pwd" {
		t.Errorf("text = %q, want pwd", text)
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestParseStreamErrorResultWithText(t *testing.T) {
	// If usable text arrived, a trailing error result should not discard it.
	transcript := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"uptime"}}}`,
		`{"type":"result","is_error":true,"result":"some late failure"}`,
	}, "\n")

	text, _, errs := collect(t, transcript)
	if text != "uptime" {
		t.Errorf("text = %q", text)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none when text was produced", errs)
	}
}

func TestBuildArgs(t *testing.T) {
	cfg := DefaultConfig()
	args := buildArgs(cfg, "do a thing", false)

	if args[0] != "-p" || args[1] != "do a thing" {
		t.Errorf("prompt must be the first positional, got %v", args[:2])
	}
	if !containsPair(args, "--model", "haiku") {
		t.Error("missing --model haiku")
	}
	if !containsPair(args, "--output-format", "stream-json") {
		t.Error("missing --output-format stream-json")
	}
	if !contains(args, "--verbose") {
		t.Error("--include-partial-messages requires --verbose")
	}
	if !contains(args, "--include-partial-messages") {
		t.Error("missing --include-partial-messages")
	}
	// Leaving --effort off entirely lets the CLI's own default kick in and the
	// model reasons at length even for a trivial request.
	if !containsPair(args, "--effort", "low") {
		t.Errorf("expected --effort low when thinking is off, got %v", args)
	}
	// --tools is variadic in the CLI parser, so an empty value must sit last
	// or it will swallow the following argument.
	if args[len(args)-2] != "--tools" || args[len(args)-1] != "" {
		t.Errorf("--tools \"\" must be the final pair, got %v", args[len(args)-2:])
	}
}

func TestBuildArgsThinking(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Effort = "high"
	args := buildArgs(cfg, "q", true)
	if !containsPair(args, "--effort", "high") {
		t.Errorf("missing --effort high in %v", args)
	}
}

func TestBuildArgsInvalidEffortFallsBack(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Effort = "nonsense" // e.g. hand-edited config
	args := buildArgs(cfg, "q", true)
	if !containsPair(args, "--effort", "medium") {
		t.Errorf("expected fallback to medium, got %v", args)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func containsPair(list []string, flag, value string) bool {
	for i := 0; i < len(list)-1; i++ {
		if list[i] == flag && list[i+1] == value {
			return true
		}
	}
	return false
}
