package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// The Antigravity CLI (`agy`) in headless mode. Verified against the flags
// documented at https://antigravity.google/docs/cli/headless:
//
//	agy -p <prompt> --output-format json [--model X] [--effort low|medium|high]
//
// It answers with one JSON envelope on stdout and puts diagnostics on stderr.
// `--output-format stream-json` also exists, but its per-step event shape is
// only loosely specified, so the single envelope is used and the spinner stands
// in for a live preview.
//
// Tool permissions are deliberately left alone: in headless mode a tool that
// would need approval is soft-denied, so `agy` answers with text instead of
// going off and running things. Passing --dangerously-skip-permissions would
// undo exactly the property this tool wants.

// agyEnvelope is the object printed by --output-format json.
type agyEnvelope struct {
	Status   string `json:"status"`
	Response string `json:"response"`
	Error    string `json:"error"`
}

// agyEfforts are the only levels the CLI accepts; the richer set understood by
// claude is clamped into it.
func agyEffort(effort string) string {
	switch effort {
	case "low", "medium", "high":
		return effort
	case "xhigh", "max":
		return "high"
	}
	return "medium"
}

// buildAgyArgs constructs the argv for the Antigravity CLI.
//
// There is no --system-prompt equivalent, so the rules are prepended to the
// prompt itself.
func buildAgyArgs(cfg Config, userMessage string, think bool) []string {
	args := []string{
		"-p", systemPrompt + "\n\n" + userMessage,
		"--output-format", "json",
	}
	if cfg.AgyModel != "" {
		args = append(args, "--model", cfg.AgyModel)
	}
	// Reasoning is pinned low unless it was asked for, for the same reason it
	// is with claude: a one-line shell command does not need a plan.
	effort := "low"
	if think {
		effort = agyEffort(cfg.Effort)
	}
	args = append(args, "--effort", effort)
	// The CLI's own default is 5m, which would outlive our context.
	args = append(args, "--print-timeout", fmt.Sprintf("%ds", cfg.TimeoutSeconds))
	return args
}

// ParseAgyOutput decodes the CLI's JSON envelope into an event.
//
// The CLI is not guaranteed to be the only thing writing to stdout, so the
// envelope is located rather than assumed to be the whole stream.
func ParseAgyOutput(out string) (Event, error) {
	raw := strings.TrimSpace(out)
	if raw == "" {
		return Event{}, fmt.Errorf("agy produced no output")
	}
	if i := strings.Index(raw, "{"); i > 0 {
		raw = raw[i:]
	}
	if j := strings.LastIndex(raw, "}"); j >= 0 && j < len(raw)-1 {
		raw = raw[:j+1]
	}

	var env agyEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return Event{}, fmt.Errorf("could not parse agy output: %w", err)
	}
	if env.Error != "" {
		return Event{Kind: EventError, Text: env.Error}, nil
	}
	if env.Status != "" && env.Status != "SUCCESS" {
		return Event{Kind: EventError, Text: "agy reported status " + env.Status}, nil
	}
	if strings.TrimSpace(env.Response) == "" {
		return Event{}, fmt.Errorf("agy returned an empty response")
	}
	return Event{Kind: EventText, Text: env.Response}, nil
}

// generateAgy runs the Antigravity CLI and emits its single answer.
func generateAgy(ctx context.Context, cfg Config, userMessage string, think bool, emit func(Event)) error {
	bin, err := exec.LookPath(cfg.AgyPath)
	if err != nil {
		return fmt.Errorf("antigravity CLI not found (%q): install it, or set \"agy_path\" in your config", cfg.AgyPath)
	}

	cmd := exec.CommandContext(ctx, bin, buildAgyArgs(cfg, userMessage, think)...)
	// An empty stdin so the CLI sees EOF at once rather than waiting on a
	// terminal; ours has already been consumed as context.
	cmd.Stdin = strings.NewReader("")

	var stderr strings.Builder
	cmd.Stderr = &stderr

	stdout, runErr := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("agy timed out after %d seconds", cfg.TimeoutSeconds)
	}

	// A failed run still prints the envelope, and its error field says more
	// than the exit status does, so parse before reporting the failure.
	ev, parseErr := ParseAgyOutput(string(stdout))
	if parseErr == nil {
		emit(ev)
		return nil
	}
	if runErr != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("agy exited with error: %v: %s", runErr, truncate(detail, 500))
		}
		return fmt.Errorf("agy exited with error: %w", runErr)
	}
	return parseErr
}
