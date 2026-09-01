package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func boolPtr(b bool) *bool { return &b }

// Config is the on-disk configuration, read from ~/.cmd-config.json.
//
// There is deliberately no API key: authentication is delegated entirely to the
// `claude` CLI, which uses the machine's existing Claude subscription login.
type Config struct {
	// Provider selects the backend: "claude" (the local Claude Code CLI, no
	// API key) or "openrouter" (a direct HTTPS call). The CLI is a Node
	// process and pays several hundred ms of start-up on every run, so
	// openrouter is the faster of the two by a wide margin.
	Provider string `json:"provider"`
	// Model alias or full model name passed to `claude --model`.
	Model string `json:"model"`
	// OpenRouterModel is the model slug used when Provider is "openrouter",
	// e.g. "google/gemini-3.7-flash".
	OpenRouterModel string `json:"openrouter_model"`
	// AgyPath is the Antigravity CLI binary, installed as "agy".
	AgyPath string `json:"agy_path"`
	// AgyModel is passed to --model. Empty means the flag is omitted, which is
	// the right default: headless agy fails loudly on an unknown model, and the
	// available slugs depend on the account. Run `agy models` to list them.
	AgyModel string `json:"agy_model"`
	// OpenRouterAPIKey is read only when the OPENROUTER_API_KEY environment
	// variable is unset. The file is written 0600, but an env var is still the
	// better place for a secret.
	OpenRouterAPIKey string `json:"openrouter_api_key,omitempty"`
	// ClaudePath is the claude binary to invoke. Looked up on PATH if relative.
	ClaudePath string `json:"claude_path"`
	// MaxPipeChars caps how much of the sample reaches the prompt.
	MaxPipeChars int `json:"max_pipe_chars"`
	// SampleReadBytes caps how much is read from stdin at all. Piping a 50 MB
	// file must cost one bounded read, not a full slurp, so this is the hard
	// ceiling on memory and the structure summary is inferred from it.
	SampleReadBytes int `json:"sample_read_bytes"`
	// EnableThink turns on extended reasoning by default (same as -t).
	EnableThink bool `json:"enable_think"`
	// Effort maps to `claude --effort` when thinking is on.
	Effort string `json:"effort"`
	// TimeoutSeconds bounds a single generation.
	TimeoutSeconds int `json:"timeout_seconds"`
	// ShowThinking streams reasoning text to stderr. Off by default: reasoning
	// is long and buries the one line you actually asked for, so the spinner
	// stands in for it.
	ShowThinking bool `json:"show_thinking"`
	// AutoReadFiles lets the tool open files whose paths appear in the request
	// and describe them to the model, so a command can name the file instead of
	// reading a pipe. A pointer because an absent field must mean "on": a
	// config written before this option existed would otherwise silently
	// disable it.
	AutoReadFiles *bool `json:"auto_read_files"`
	// MaxAutoFiles caps how many files one request may open.
	MaxAutoFiles int `json:"max_auto_files"`
	// DangerousPatterns are extra regexes treated as destructive, on top of the
	// built-in list. Anything matching requires typing the full word "yes".
	DangerousPatterns []string `json:"dangerous_patterns"`
}

// Providers are the backends Generate knows how to drive.
const (
	ProviderClaude     = "claude"
	ProviderAgy        = "antigravity"
	ProviderOpenRouter = "openrouter"
)

var validProviders = map[string]bool{
	ProviderClaude: true, ProviderAgy: true, ProviderOpenRouter: true,
}

// ProviderNames lists the providers in the order the setup wizard offers them.
var ProviderNames = []string{ProviderOpenRouter, ProviderClaude, ProviderAgy}

// DefaultOpenRouterModel is a fast, inexpensive model that is reliable at
// one-line shell commands. Any slug from https://openrouter.ai/models works.
const DefaultOpenRouterModel = "google/gemini-3.7-flash"

// APIKey returns the OpenRouter key, preferring the environment over the file.
func (c Config) APIKey() string {
	if k := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); k != "" {
		return k
	}
	return strings.TrimSpace(c.OpenRouterAPIKey)
}

// ActiveModel is the model that will actually be used, which depends on the
// provider in force.
func (c Config) ActiveModel() string {
	switch c.Provider {
	case ProviderOpenRouter:
		return c.OpenRouterModel
	case ProviderAgy:
		if c.AgyModel == "" {
			return "(the agy default)"
		}
		return c.AgyModel
	}
	return c.Model
}

// withModelOverride applies a -m flag to whichever provider is in force.
func (c Config) withModelOverride(model string) Config {
	if model == "" {
		return c
	}
	switch c.Provider {
	case ProviderOpenRouter:
		c.OpenRouterModel = model
	case ProviderAgy:
		c.AgyModel = model
	default:
		c.Model = model
	}
	return c
}

// ReadsFiles reports whether paths named in a request may be opened.
func (c Config) ReadsFiles() bool {
	return c.AutoReadFiles == nil || *c.AutoReadFiles
}

// validEfforts mirrors the levels accepted by `claude --effort`.
var validEfforts = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// DefaultConfig returns the configuration used when no file exists.
func DefaultConfig() Config {
	return Config{
		Provider:        ProviderClaude,
		Model:           "haiku",
		AgyPath:         "agy",
		OpenRouterModel: DefaultOpenRouterModel,
		AutoReadFiles:   boolPtr(true),
		MaxAutoFiles:    3,
		ClaudePath:      "claude",
		MaxPipeChars:    4000,
		SampleReadBytes: 256 * 1024,
		EnableThink:     false,
		Effort:          "medium",
		TimeoutSeconds:  120,
		ShowThinking:    false,
	}
}

// ConfigPath returns the path of the config file.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cmd-config.json"), nil
}

// LoadConfig reads the config file, filling in defaults for missing or invalid
// fields. A missing file is not an error: defaults are returned instead, so the
// tool works on a fresh machine with zero setup.
func LoadConfig() (Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return DefaultConfig(), err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return DefaultConfig(), fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	cfg, err := parseConfig(f)
	if err != nil {
		return DefaultConfig(), fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// parseConfig decodes JSON and normalizes it. Split out from LoadConfig so it
// can be tested without touching the filesystem.
func parseConfig(r io.Reader) (Config, error) {
	cfg := DefaultConfig()
	raw, err := io.ReadAll(r)
	if err != nil {
		return DefaultConfig(), err
	}
	// An empty file is treated as "all defaults" rather than a syntax error.
	if len(trimSpaceBytes(raw)) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return DefaultConfig(), err
	}
	cfg.normalize()
	return cfg, nil
}

// normalize repairs empty or nonsensical values so the rest of the program can
// assume the config is usable.
func (c *Config) normalize() {
	if !validProviders[c.Provider] {
		c.Provider = ProviderClaude
	}
	if c.Model == "" {
		c.Model = "haiku"
	}
	if c.OpenRouterModel == "" {
		c.OpenRouterModel = DefaultOpenRouterModel
	}
	if c.AgyPath == "" {
		c.AgyPath = "agy"
	}
	if c.AutoReadFiles == nil {
		c.AutoReadFiles = boolPtr(true)
	}
	if c.MaxAutoFiles <= 0 {
		c.MaxAutoFiles = 3
	}
	if c.ClaudePath == "" {
		c.ClaudePath = "claude"
	}
	if c.MaxPipeChars <= 0 {
		c.MaxPipeChars = 4000
	}
	if c.SampleReadBytes <= 0 {
		c.SampleReadBytes = 256 * 1024
	}
	// Reading less than we intend to send makes no sense.
	if c.SampleReadBytes < c.MaxPipeChars {
		c.SampleReadBytes = c.MaxPipeChars
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 120
	}
	if !validEfforts[c.Effort] {
		c.Effort = "medium"
	}
}

func trimSpaceBytes(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpaceByte(b[start]) {
		start++
	}
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
