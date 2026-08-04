package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Config is the on-disk configuration, read from ~/.cmd-config.json.
//
// There is deliberately no API key: authentication is delegated entirely to the
// `claude` CLI, which uses the machine's existing Claude subscription login.
type Config struct {
	// Model alias or full model name passed to `claude --model`.
	Model string `json:"model"`
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
	// DangerousPatterns are extra regexes treated as destructive, on top of the
	// built-in list. Anything matching requires typing the full word "yes".
	DangerousPatterns []string `json:"dangerous_patterns"`
}

// validEfforts mirrors the levels accepted by `claude --effort`.
var validEfforts = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// DefaultConfig returns the configuration used when no file exists.
func DefaultConfig() Config {
	return Config{
		Model:           "haiku",
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
	if c.Model == "" {
		c.Model = "haiku"
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

// WriteDefaultConfig creates the config file if it does not already exist.
// It reports whether a file was written.
func WriteDefaultConfig() (string, bool, error) {
	path, err := ConfigPath()
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	}
	def := DefaultConfig()
	// Emit [] rather than null, so the file is a usable starting point to edit.
	def.DangerousPatterns = []string{}
	body, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return path, false, err
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return path, false, err
	}
	return path, true, nil
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
