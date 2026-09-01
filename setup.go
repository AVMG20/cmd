package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The `cmd --configure` wizard.
//
// It exists because the config file has grown more knobs than anyone should
// have to discover by reading a struct: a provider, a model per provider, a
// key, a binary path. Almost all of them only matter for one provider, so the
// wizard asks about the provider first and then only about what that choice
// actually needs.

// suggestedOpenRouterModels are a few models that are quick and reliably good
// at one-line shell commands. Any slug from https://openrouter.ai/models works;
// this list is a starting point, not a limit.
var suggestedOpenRouterModels = []Choice{
	{Label: "google/gemini-3.7-flash", Hint: "$0.75/$3.75 per M · a good default", Value: "google/gemini-3.7-flash"},
	{Label: "google/gemini-2.5-flash-lite", Hint: "$0.10/$0.40 per M · cheaper, still solid", Value: "google/gemini-2.5-flash-lite"},
	{Label: "qwen/qwen3.7-flash", Hint: "$0.03/$0.13 per M · cheapest of these", Value: "qwen/qwen3.7-flash"},
	{Label: "openai/gpt-5-nano", Hint: "$0.05/$0.40 per M", Value: "openai/gpt-5-nano"},
}

var suggestedClaudeModels = []Choice{
	{Label: "haiku", Hint: "fastest", Value: "haiku"},
	{Label: "sonnet", Hint: "better on awkward requests", Value: "sonnet"},
	{Label: "opus", Hint: "slowest, rarely worth it here", Value: "opus"},
}

const customChoice = "\x00custom"

// Configure walks the user through the settings that matter and writes them.
// It returns the exit code for the process.
// Configure runs the wizard from the command line.
//
// Raw mode is taken once for the whole wizard rather than per question,
// because each teardown leaves a read parked on the terminal that swallows the
// first keystroke of whatever asks next.
func Configure(out io.Writer, in io.Reader, p palette) int {
	// Only drive the terminal when the answers are actually coming from it.
	// A controlling terminal exists even when stdin is a pipe, so reaching for
	// it unconditionally would ignore piped answers and wait for a keypress
	// nobody is there to give.
	if isTerminal(os.Stdin) {
		if term, ok := enterRaw(); ok {
			defer term.Restore()
			return configure(newRawPrompter(out, term, p), crlfWriter{out}, p)
		}
	}
	return configure(newPrompter(out, in, p), out, p)
}

// ConfigureRaw runs the wizard from inside the harness, over a terminal that is
// already raw. The caller owns both the terminal and the newline translation.
func ConfigureRaw(out io.Writer, term *rawTerminal, p palette) int {
	return configure(newRawPrompter(out, term, p), out, p)
}

func configure(w *prompter, out io.Writer, p palette) int {
	w.out = out
	path, err := ConfigPath()
	if err != nil {
		fmt.Fprintf(out, "%s could not work out where the config lives: %v\n", p.Red("Error:"), err)
		return 1
	}
	cfg, loadErr := LoadConfig()
	if loadErr != nil {
		fmt.Fprintf(out, "%s %v\n", p.Yellow("Warning:"), loadErr)
		cfg = DefaultConfig()
	}

	fmt.Fprintf(out, "%s\n%s\n\n", p.Yellow("cmd setup"), p.Dim(path))

	provider, ok := w.Select("Which backend should generate commands?", []Choice{
		{Label: "OpenRouter", Hint: "an API key · fastest, costs a fraction of a cent per command", Value: ProviderOpenRouter},
		{Label: "Claude Code CLI", Hint: "uses your Claude subscription · no API key", Value: ProviderClaude},
		{Label: "Antigravity CLI (agy)", Hint: "uses your Google account · no API key", Value: ProviderAgy},
	}, indexOfProvider(cfg.Provider))
	if !ok {
		fmt.Fprintln(out, p.Dim("Cancelled; nothing was written."))
		return 2
	}
	cfg.Provider = provider
	fmt.Fprintln(out)

	switch provider {
	case ProviderOpenRouter:
		if !configureOpenRouter(w, &cfg) {
			fmt.Fprintln(out, p.Dim("Cancelled; nothing was written."))
			return 2
		}
	case ProviderClaude:
		model, ok := selectWithCustom(w, "Which Claude model?", suggestedClaudeModels, cfg.Model, "Model name:")
		if !ok {
			fmt.Fprintln(out, p.Dim("Cancelled; nothing was written."))
			return 2
		}
		cfg.Model = model
	case ProviderAgy:
		cfg.AgyPath = w.Prompt("Path to the agy binary:", cfg.AgyPath)
		fmt.Fprintf(out, "%s\n", p.Dim("Leave the model blank to let agy choose; run `agy models` to see the options."))
		cfg.AgyModel = w.Prompt("Model for agy:", cfg.AgyModel)
	}

	if err := SaveConfig(cfg); err != nil {
		fmt.Fprintf(out, "\n%s could not write %s: %v\n", p.Red("Error:"), path, err)
		return 1
	}

	fmt.Fprintf(out, "\n%s %s\n", p.Green("✓"), "Saved to "+path)
	fmt.Fprintf(out, "  provider  %s\n", cfg.Provider)
	fmt.Fprintf(out, "  model     %s\n", cfg.ActiveModel())
	if w.term == nil {
		// Inside the harness there is already a prompt waiting; telling the
		// user to go and run cmd would be advice to do what they are doing.
		fmt.Fprintf(out, "\n%s\n", p.Dim(`Try it:  cmd "show the 10 biggest files here"`))
	}
	return 0
}

// configureOpenRouter collects the key and the model. It reports false if the
// user backed out.
func configureOpenRouter(w *prompter, cfg *Config) bool {
	out, p := w.out, w.p
	if env := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); env != "" {
		// An environment variable always wins at run time, so writing a key to
		// the file here would be quietly ignored later.
		fmt.Fprintf(out, "%s OPENROUTER_API_KEY is set in your environment (%s); that key will be used.\n\n",
			p.Green("✓"), masked(env))
	} else {
		fmt.Fprintf(out, "%s\n", p.Dim("Create a key at https://openrouter.ai/keys"))
		question := "Paste your OpenRouter API key:"
		if cfg.OpenRouterAPIKey != "" {
			question = "OpenRouter API key (Enter keeps the saved one):"
		}
		key := w.Prompt(question, cfg.OpenRouterAPIKey)
		if strings.TrimSpace(key) == "" {
			fmt.Fprintf(out, "\n%s a key is required for OpenRouter.\n", p.Red("Error:"))
			return false
		}
		cfg.OpenRouterAPIKey = strings.TrimSpace(key)
		fmt.Fprintln(out)
	}

	model, ok := selectWithCustom(w, "Which model?", suggestedOpenRouterModels, cfg.OpenRouterModel, "Model slug:")
	if !ok {
		return false
	}
	cfg.OpenRouterModel = model
	return true
}

// selectWithCustom offers a shortlist plus a free-text escape hatch, so a
// curated list never becomes a cage.
func selectWithCustom(w *prompter, question string, choices []Choice, current, customPrompt string) (string, bool) {
	list := make([]Choice, len(choices), len(choices)+1)
	copy(list, choices)

	selected := indexOfValue(list, current)
	if selected < 0 && current != "" {
		// Whatever is configured now is not on the shortlist; show it so the
		// wizard never silently discards a deliberate choice.
		list = append(list, Choice{Label: current, Hint: "current", Value: current})
		selected = len(list) - 1
	}
	list = append(list, Choice{Label: "Something else…", Hint: "type a name", Value: customChoice})
	if selected < 0 {
		selected = 0
	}

	value, ok := w.Select(question, list, selected)
	if !ok {
		return "", false
	}
	if value != customChoice {
		return value, true
	}
	custom := strings.TrimSpace(w.Prompt(customPrompt, current))
	if custom == "" {
		return "", false
	}
	return custom, true
}

func indexOfValue(choices []Choice, value string) int {
	for i, c := range choices {
		if c.Value == value {
			return i
		}
	}
	return -1
}

func indexOfProvider(provider string) int {
	switch provider {
	case ProviderClaude:
		return 1
	case ProviderAgy:
		return 2
	}
	return 0
}

// SaveConfig writes the config file, 0600 because it can hold an API key.
func SaveConfig(cfg Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if cfg.DangerousPatterns == nil {
		cfg.DangerousPatterns = []string{}
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}
