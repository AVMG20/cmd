package main

import (
	"os"
	"strings"
	"testing"
)

func TestActiveModelFollowsProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model = "haiku"
	cfg.OpenRouterModel = "google/gemini-3.7-flash"
	cfg.AgyModel = "gemini-3.5-flash-medium"

	cfg.Provider = ProviderClaude
	if cfg.ActiveModel() != "haiku" {
		t.Errorf("claude model = %q", cfg.ActiveModel())
	}
	cfg.Provider = ProviderOpenRouter
	if cfg.ActiveModel() != "google/gemini-3.7-flash" {
		t.Errorf("openrouter model = %q", cfg.ActiveModel())
	}
	cfg.Provider = ProviderAgy
	if cfg.ActiveModel() != "gemini-3.5-flash-medium" {
		t.Errorf("agy model = %q", cfg.ActiveModel())
	}
}

func TestModelOverrideAppliesToActiveProvider(t *testing.T) {
	// -m must not quietly set the wrong provider's model.
	cfg := DefaultConfig()
	cfg.Provider = ProviderOpenRouter
	got := cfg.withModelOverride("qwen/qwen3.7-flash")
	if got.OpenRouterModel != "qwen/qwen3.7-flash" {
		t.Errorf("openrouter model = %q", got.OpenRouterModel)
	}
	if got.Model != cfg.Model {
		t.Errorf("the claude model should be untouched, got %q", got.Model)
	}

	cfg.Provider = ProviderClaude
	if got := cfg.withModelOverride("sonnet"); got.Model != "sonnet" {
		t.Errorf("claude model = %q", got.Model)
	}
}

func TestAPIKeyPrefersEnvironment(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OpenRouterAPIKey = "from-file"

	t.Setenv("OPENROUTER_API_KEY", "")
	if cfg.APIKey() != "from-file" {
		t.Errorf("APIKey = %q, want the file value when the env is empty", cfg.APIKey())
	}

	t.Setenv("OPENROUTER_API_KEY", "from-env")
	if cfg.APIKey() != "from-env" {
		t.Errorf("APIKey = %q, want the environment to win", cfg.APIKey())
	}
}

func TestReadsFilesDefaultsOnForOldConfigs(t *testing.T) {
	// A config file written before auto_read_files existed has no such key.
	// Decoding it must not leave the feature off.
	cfg, err := parseConfig(strings.NewReader(`{"model":"haiku"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ReadsFiles() {
		t.Error("auto_read_files must default to on when the key is absent")
	}

	cfg, err = parseConfig(strings.NewReader(`{"auto_read_files":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReadsFiles() {
		t.Error("an explicit false must be honoured")
	}
}

func TestUnknownProviderNormalizesToClaude(t *testing.T) {
	cfg, err := parseConfig(strings.NewReader(`{"provider":"chatgpt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderClaude {
		t.Errorf("provider = %q, want a safe fallback", cfg.Provider)
	}
}

func TestSaveConfigIsNotWorldReadable(t *testing.T) {
	// The file can hold an API key.
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := DefaultConfig()
	cfg.OpenRouterAPIKey = "sk-or-v1-secret"
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	path, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %o, want 600", perm)
	}
}

func TestStreamsOnlyDisabledForAgy(t *testing.T) {
	if Streams(ProviderAgy) {
		t.Error("agy answers in one shot; there is nothing to preview")
	}
	if !Streams(ProviderOpenRouter) || !Streams(ProviderClaude) {
		t.Error("the streaming backends should preview")
	}
}
