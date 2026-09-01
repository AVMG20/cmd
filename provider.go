package main

import (
	"context"
	"fmt"
)

// Generate produces a command using whichever backend the config selects.
//
// Every backend reports through the same Event stream, so the UI does not care
// which one ran. They differ mainly in latency: the CLI backends each start a
// Node process and pay several hundred milliseconds before the first token,
// while openrouter is a single HTTPS request.
func Generate(ctx context.Context, cfg Config, userMessage string, think bool, emit func(Event)) error {
	switch cfg.Provider {
	case ProviderOpenRouter:
		return generateOpenRouter(ctx, cfg, userMessage, think, emit)
	case ProviderAgy:
		return generateAgy(ctx, cfg, userMessage, think, emit)
	case ProviderClaude:
		return generateClaude(ctx, cfg, userMessage, think, emit)
	default:
		return fmt.Errorf("unknown provider %q (expected one of: claude, antigravity, openrouter)", cfg.Provider)
	}
}

// Streams reports whether the provider delivers text incrementally. Backends
// that answer in one shot get a spinner instead of a live preview.
func Streams(provider string) bool {
	return provider != ProviderAgy
}
