package main

import (
	"strings"
	"testing"
)

func TestParseConfigDefaults(t *testing.T) {
	tests := []struct {
		name string
		json string
		want Config
	}{
		{
			name: "empty file yields defaults",
			json: "",
			want: DefaultConfig(),
		},
		{
			name: "empty object yields defaults",
			json: `{}`,
			want: DefaultConfig(),
		},
		{
			name: "partial config keeps defaults for the rest",
			json: `{"model":"sonnet"}`,
			want: Config{
				Model: "sonnet", ClaudePath: "claude", MaxPipeChars: 4000,
				Effort: "medium", TimeoutSeconds: 120, ShowThinking: false,
			},
		},
		{
			name: "zero and negative numbers are repaired",
			json: `{"max_pipe_chars":0,"timeout_seconds":-5}`,
			want: DefaultConfig(),
		},
		{
			name: "invalid effort falls back to medium",
			json: `{"effort":"turbo"}`,
			want: DefaultConfig(),
		},
		{
			name: "valid effort is kept",
			json: `{"effort":"xhigh"}`,
			want: Config{
				Model: "haiku", ClaudePath: "claude", MaxPipeChars: 4000,
				Effort: "xhigh", TimeoutSeconds: 120, ShowThinking: false,
			},
		},
		{
			name: "custom claude path is kept",
			json: `{"claude_path":"/opt/bin/claude"}`,
			want: Config{
				Model: "haiku", ClaudePath: "/opt/bin/claude", MaxPipeChars: 4000,
				Effort: "medium", TimeoutSeconds: 120, ShowThinking: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseConfig(strings.NewReader(tt.json))
			if err != nil {
				t.Fatalf("parseConfig() error = %v", err)
			}
			if got.Model != tt.want.Model {
				t.Errorf("Model = %q, want %q", got.Model, tt.want.Model)
			}
			if got.ClaudePath != tt.want.ClaudePath {
				t.Errorf("ClaudePath = %q, want %q", got.ClaudePath, tt.want.ClaudePath)
			}
			if got.MaxPipeChars != tt.want.MaxPipeChars {
				t.Errorf("MaxPipeChars = %d, want %d", got.MaxPipeChars, tt.want.MaxPipeChars)
			}
			if got.Effort != tt.want.Effort {
				t.Errorf("Effort = %q, want %q", got.Effort, tt.want.Effort)
			}
			if got.TimeoutSeconds != tt.want.TimeoutSeconds {
				t.Errorf("TimeoutSeconds = %d, want %d", got.TimeoutSeconds, tt.want.TimeoutSeconds)
			}
		})
	}
}

func TestDefaultConfigHidesReasoning(t *testing.T) {
	// Reasoning is long and buries the command; a plain run should show only
	// the spinner.
	if DefaultConfig().ShowThinking {
		t.Error("ShowThinking must default to false")
	}
	if DefaultConfig().EnableThink {
		t.Error("EnableThink must default to false")
	}
}

func TestParseConfigRejectsMalformedJSON(t *testing.T) {
	if _, err := parseConfig(strings.NewReader(`{"model":`)); err == nil {
		t.Fatal("expected an error for malformed JSON, got nil")
	}
}

func TestParseConfigNoAPIKeyField(t *testing.T) {
	// Unknown legacy fields must be ignored rather than breaking the load,
	// so old OpenRouter configs keep working after the switch to claude.
	got, err := parseConfig(strings.NewReader(`{"api_key":"sk-old","model":"haiku"}`))
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if got.Model != "haiku" {
		t.Errorf("Model = %q, want haiku", got.Model)
	}
}

func TestParseConfigCustomDangerousPatterns(t *testing.T) {
	got, err := parseConfig(strings.NewReader(`{"dangerous_patterns":["\\bmy-prod-db\\b"]}`))
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if len(got.DangerousPatterns) != 1 || got.DangerousPatterns[0] != `\bmy-prod-db\b` {
		t.Errorf("DangerousPatterns = %v", got.DangerousPatterns)
	}
}
