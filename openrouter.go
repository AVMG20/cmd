package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// A direct HTTPS call to OpenRouter's OpenAI-compatible chat completions API.
//
// This is the fast backend. The CLI providers each boot a Node process and set
// up an agent session before the model is even reached; here the only cost is
// one request. For a twenty-token shell command that difference dominates the
// total latency.

const openRouterURL = "https://openrouter.ai/api/v1/chat/completions"

// maxCommandTokens bounds the answer. A shell command is short, and a low cap
// keeps a confused model from billing for an essay.
const maxCommandTokens = 400

type orMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type orReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Exclude bool   `json:"exclude,omitempty"`
}

type orRequest struct {
	Model       string       `json:"model"`
	Messages    []orMessage  `json:"messages"`
	MaxTokens   int          `json:"max_tokens"`
	Temperature float64      `json:"temperature"`
	Stream      bool         `json:"stream"`
	Reasoning   *orReasoning `json:"reasoning,omitempty"`
}

// buildOpenRouterRequest assembles the request body.
//
// The reasoning block matters more than it looks. Several cheap models --
// google/gemini-3.7-flash among them -- have reasoning switched on by default
// at medium effort, which is precisely the slow, over-thought behaviour this
// tool is trying to avoid. Asking for low effort and excluding the reasoning
// from the response keeps a plain run fast. "none" is not used because models
// with mandatory reasoning reject it.
func buildOpenRouterRequest(cfg Config, userMessage string, think bool) orRequest {
	req := orRequest{
		Model: cfg.OpenRouterModel,
		Messages: []orMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		MaxTokens:   maxCommandTokens,
		Temperature: 0,
		Stream:      true,
		Reasoning:   &orReasoning{Effort: "low", Exclude: true},
	}
	if think {
		effort := cfg.Effort
		if !validEfforts[effort] {
			effort = "medium"
		}
		req.Reasoning = &orReasoning{Effort: effort}
	}
	return req
}

// orChunk is one streamed delta.
type orChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"delta"`
	} `json:"choices"`
	Error *orError `json:"error"`
}

type orError struct {
	Message string `json:"message"`
	Code    any    `json:"code"`
}

func (e *orError) String() string {
	if e == nil {
		return ""
	}
	if e.Code == nil || e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s (code %v)", e.Message, e.Code)
}

// ParseSSE reads an OpenAI-style event stream and invokes emit for each delta.
//
// Two details are load-bearing. OpenRouter injects `: OPENROUTER PROCESSING`
// comment lines to keep the connection alive, and feeding one to a JSON decoder
// would end the stream on a parse error. And an error can arrive mid-stream, in
// a data frame, long after the response headers said 200.
func ParseSSE(r io.Reader, emit func(Event)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Comments and blank separators carry no payload.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			break
		}

		var chunk orChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			emit(Event{Kind: EventError, Text: chunk.Error.String()})
			return nil
		}
		for _, c := range chunk.Choices {
			if c.Delta.Reasoning != "" {
				emit(Event{Kind: EventThinking, Text: c.Delta.Reasoning})
			}
			if c.Delta.Content != "" {
				emit(Event{Kind: EventText, Text: c.Delta.Content})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading openrouter stream: %w", err)
	}
	return nil
}

// errorFromResponse turns a non-200 into advice rather than a status code.
func errorFromResponse(status int, body []byte) error {
	var wrapper struct {
		Error *orError `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.Error != nil {
		msg = wrapper.Error.Message
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("openrouter rejected the API key: %s\nRun `cmd --configure` to set one, or export OPENROUTER_API_KEY", msg)
	case http.StatusPaymentRequired:
		return fmt.Errorf("openrouter reports no credit: %s", msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("openrouter rate-limited this key: %s", msg)
	case http.StatusNotFound:
		return fmt.Errorf("openrouter does not know that model: %s\nPick one from https://openrouter.ai/models and set it with `cmd --configure`", msg)
	}
	return fmt.Errorf("openrouter returned %d: %s", status, truncate(msg, 300))
}

// generateOpenRouter performs the request and streams the answer.
func generateOpenRouter(ctx context.Context, cfg Config, userMessage string, think bool, emit func(Event)) error {
	key := cfg.APIKey()
	if key == "" {
		return fmt.Errorf("no OpenRouter API key: run `cmd --configure`, or export OPENROUTER_API_KEY")
	}

	body, err := json.Marshal(buildOpenRouterRequest(cfg, userMessage, think))
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// Attribution headers; OpenRouter uses them to label the traffic.
	req.Header.Set("HTTP-Referer", "https://github.com/avmg20/cmd")
	req.Header.Set("X-Title", "cmd")

	client := &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("openrouter timed out after %d seconds", cfg.TimeoutSeconds)
		}
		return fmt.Errorf("calling openrouter: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return errorFromResponse(resp.StatusCode, payload)
	}
	return ParseSSE(resp.Body, emit)
}
