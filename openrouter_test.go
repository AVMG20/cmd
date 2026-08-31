package main

import (
	"strings"
	"testing"
)

func collectSSE(t *testing.T, stream string) (text, thinking string, errText string) {
	t.Helper()
	var b, think, e strings.Builder
	if err := ParseSSE(strings.NewReader(stream), func(ev Event) {
		switch ev.Kind {
		case EventText:
			b.WriteString(ev.Text)
		case EventThinking:
			think.WriteString(ev.Text)
		case EventError:
			e.WriteString(ev.Text)
		}
	}); err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	return b.String(), think.String(), e.String()
}

func TestParseSSEAssemblesDeltas(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"content":"jq -r "}}]}
data: {"choices":[{"delta":{"content":"'.[].title'"}}]}
data: [DONE]
`
	text, _, errText := collectSSE(t, stream)
	if text != `jq -r '.[].title'` {
		t.Errorf("text = %q", text)
	}
	if errText != "" {
		t.Errorf("unexpected error %q", errText)
	}
}

func TestParseSSESkipsKeepaliveComments(t *testing.T) {
	// OpenRouter injects these to hold the connection open. Handing one to a
	// JSON decoder would end the stream early, losing the answer.
	stream := `: OPENROUTER PROCESSING
data: {"choices":[{"delta":{"content":"ls"}}]}
: OPENROUTER PROCESSING

data: {"choices":[{"delta":{"content":" -la"}}]}
data: [DONE]
`
	text, _, _ := collectSSE(t, stream)
	if text != "ls -la" {
		t.Errorf("text = %q, want %q", text, "ls -la")
	}
}

func TestParseSSEReportsMidStreamError(t *testing.T) {
	// An error can arrive in a data frame long after a 200 response header.
	stream := `data: {"choices":[{"delta":{"content":"ls"}}]}
data: {"error":{"message":"upstream provider is down","code":502}}
data: [DONE]
`
	_, _, errText := collectSSE(t, stream)
	if !strings.Contains(errText, "upstream provider is down") {
		t.Errorf("error = %q, want the provider message", errText)
	}
	if !strings.Contains(errText, "502") {
		t.Errorf("error = %q, want the code included", errText)
	}
}

func TestParseSSESeparatesReasoning(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"reasoning":"they want the biggest files"}}]}
data: {"choices":[{"delta":{"content":"du -ah . | sort -rh | head"}}]}
data: [DONE]
`
	text, thinking, _ := collectSSE(t, stream)
	if strings.Contains(text, "biggest files") {
		t.Error("reasoning must not leak into the command")
	}
	if thinking != "they want the biggest files" {
		t.Errorf("thinking = %q", thinking)
	}
}

func TestOpenRouterRequestPinsReasoningLowByDefault(t *testing.T) {
	// Several cheap models default to reasoning at medium effort, which is the
	// slowness this backend exists to avoid.
	cfg := DefaultConfig()
	req := buildOpenRouterRequest(cfg, "list files", false)

	if req.Reasoning == nil || req.Reasoning.Effort != "low" {
		t.Errorf("reasoning = %+v, want low effort", req.Reasoning)
	}
	if !req.Reasoning.Exclude {
		t.Error("reasoning tokens should be excluded from a plain run")
	}
	if req.Temperature != 0 {
		t.Errorf("temperature = %v, want 0 for a deterministic command", req.Temperature)
	}
	if req.MaxTokens <= 0 || req.MaxTokens > 1000 {
		t.Errorf("max_tokens = %d, want a small bound", req.MaxTokens)
	}
	if !req.Stream {
		t.Error("the response should stream so the preview can render")
	}
}

func TestOpenRouterRequestHonoursThink(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Effort = "high"
	req := buildOpenRouterRequest(cfg, "something hard", true)

	if req.Reasoning.Effort != "high" {
		t.Errorf("effort = %q, want high", req.Reasoning.Effort)
	}
	if req.Reasoning.Exclude {
		t.Error("reasoning was asked for, so it must not be excluded")
	}
}

func TestOpenRouterRequestSendsBothMessages(t *testing.T) {
	req := buildOpenRouterRequest(DefaultConfig(), "count the rows", false)
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if !strings.Contains(req.Messages[0].Content, "Output ONLY the command") {
		t.Error("the system prompt must be sent")
	}
	if req.Messages[1].Content != "count the rows" {
		t.Errorf("user message = %q", req.Messages[1].Content)
	}
}
