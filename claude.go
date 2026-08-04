package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// EventKind classifies a decoded item from the claude stream.
type EventKind int

const (
	// EventText is a chunk of the answer (the command being generated).
	EventText EventKind = iota
	// EventThinking is a chunk of reasoning, only present with --effort.
	EventThinking
	// EventError is a fatal message reported by the CLI or the API.
	EventError
)

// Event is one decoded piece of the stream.
type Event struct {
	Kind EventKind
	Text string
}

// buildArgs constructs the argv for the claude CLI.
//
// Ordering matters: `--tools` is variadic in the CLI's parser, so it is placed
// last (where its empty-string value cannot swallow a following argument), and
// the prompt is passed as the first positional so no variadic flag can absorb
// it.
func buildArgs(cfg Config, userMessage string, think bool) []string {
	args := []string{
		"-p", userMessage,
		"--model", cfg.Model,
		"--system-prompt", systemPrompt,
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--disable-slash-commands",
	}
	// --effort is always sent. Omitting it leaves the CLI on its own default,
	// which lets the model reason at length even when thinking was not asked
	// for; pinning it to "low" keeps a plain run fast.
	effort := "low"
	if think {
		effort = cfg.Effort
		if !validEfforts[effort] {
			effort = "medium"
		}
	}
	args = append(args, "--effort", effort)
	// Disable every built-in tool: this tool wants text, not an agent that
	// goes off and reads files or runs bash on its own.
	args = append(args, "--tools", "")
	return args
}

// envelope is the outer object the CLI emits, one JSON object per line.
type envelope struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	Event             json.RawMessage `json:"event"`
	Message           *apiMessage     `json:"message"`
	IsAPIErrorMessage bool            `json:"is_api_error_message"`
	IsError           bool            `json:"is_error"`
	Result            string          `json:"result"`
	Error             string          `json:"error"`
}

type apiMessage struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// sseEvent is the raw Anthropic streaming event nested under "event" when
// --include-partial-messages is used.
type sseEvent struct {
	Type  string `json:"type"`
	Delta *struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

// ParseStream reads newline-delimited JSON from r and invokes emit for each
// decoded event.
//
// It handles both shapes the CLI can produce: incremental `stream_event`
// deltas, and whole `assistant` messages. If any incremental text arrived, the
// final assistant message is ignored so text is never emitted twice.
//
// Unparseable lines are skipped rather than treated as fatal: the CLI may
// interleave warnings with the JSON stream.
func ParseStream(r io.Reader, emit func(Event)) error {
	sc := bufio.NewScanner(r)
	// Individual lines can be large (a whole assistant message).
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	// Text and thinking are tracked separately: the CLI emits a complete
	// assistant message for the thinking block *before* any text delta arrives,
	// so a single shared flag would let reasoning through twice.
	sawPartialText := false
	sawPartialThinking := false
	sawAnyText := false
	var apiErr string

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var env envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}

		switch env.Type {
		case "stream_event":
			if len(env.Event) == 0 {
				continue
			}
			var ev sseEvent
			if err := json.Unmarshal(env.Event, &ev); err != nil {
				continue
			}
			if ev.Type != "content_block_delta" || ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					sawPartialText = true
					sawAnyText = true
					emit(Event{Kind: EventText, Text: ev.Delta.Text})
				}
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					sawPartialThinking = true
					emit(Event{Kind: EventThinking, Text: ev.Delta.Thinking})
				}
			}

		case "assistant":
			if env.Message == nil {
				continue
			}
			for _, block := range env.Message.Content {
				switch block.Type {
				case "text":
					if block.Text == "" {
						continue
					}
					if env.IsAPIErrorMessage {
						apiErr = block.Text
						continue
					}
					// Already streamed incrementally; don't duplicate.
					if sawPartialText {
						continue
					}
					sawAnyText = true
					emit(Event{Kind: EventText, Text: block.Text})
				case "thinking":
					if block.Thinking != "" && !sawPartialThinking {
						emit(Event{Kind: EventThinking, Text: block.Thinking})
					}
				}
			}

		case "result":
			if env.IsError && !sawAnyText {
				msg := env.Result
				if msg == "" {
					msg = env.Error
				}
				if msg == "" {
					msg = "claude reported an error (" + env.Subtype + ")"
				}
				apiErr = msg
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading claude output: %w", err)
	}
	if apiErr != "" && !sawAnyText {
		emit(Event{Kind: EventError, Text: apiErr})
	}
	return nil
}

// Generate runs the claude CLI and streams decoded events to emit.
//
// The child's stdin is wired to an empty reader so it sees EOF immediately.
// Without this the CLI waits several seconds for stdin data, which would add a
// fixed delay to every invocation of this tool (our own stdin has already been
// consumed as context).
func Generate(ctx context.Context, cfg Config, userMessage string, think bool, emit func(Event)) error {
	bin, err := exec.LookPath(cfg.ClaudePath)
	if err != nil {
		return fmt.Errorf("claude CLI not found (%q): install Claude Code, or set \"claude_path\" in your config", cfg.ClaudePath)
	}

	cmd := exec.CommandContext(ctx, bin, buildArgs(cfg, userMessage, think)...)
	cmd.Stdin = strings.NewReader("")

	var stderr strings.Builder
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting claude: %w", err)
	}

	parseErr := ParseStream(stdout, emit)
	waitErr := cmd.Wait()

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("claude timed out after %d seconds", cfg.TimeoutSeconds)
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("claude exited with error: %v: %s", waitErr, truncate(detail, 500))
		}
		return fmt.Errorf("claude exited with error: %w", waitErr)
	}
	return parseErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
