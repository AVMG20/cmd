package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

const version = "0.2.0"

const usage = `cmd - turn plain English into shell commands, powered by your Claude subscription.

Usage:
  cmd [flags] "what you want to do"
  <command producing data> | cmd [flags] "what to do with it"

Flags:
  -t, --think          Let the model reason before answering (slower, better on hard requests)
  -m, --model <name>   Model to use for this run (default: from config, "haiku")
  -q, --quiet          Print only the command; never prompt, never execute
  -y, --yes            Skip the confirmation for commands that are not flagged as risky
      --init           Write a default config to ~/.cmd-config.json
      --config         Show the config file path and the settings in force
      --debug          Print the underlying claude invocation to stderr
  -h, --help           Show this help
  -v, --version        Show version

Examples:
  cmd "show the 10 biggest files in this folder"
  cmd "find TODO comments in src, with line numbers"
  cmd "what's using port 3000"
  cat package.json | cmd "list the dependency names"
  cat users.json | cmd "emails of everyone who is active"

Exit codes: 0 success, 1 error, 2 aborted by user.
`

func main() {
	os.Exit(run())
}

func run() int {
	var (
		think    bool
		quiet    bool
		yes      bool
		initCfg  bool
		showCfg  bool
		debug    bool
		showVer  bool
		modelOvr string
	)

	fs := flag.NewFlagSet("cmd", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own usage
	boolVar(fs, &think, "think", "t", false)
	boolVar(fs, &quiet, "quiet", "q", false)
	boolVar(fs, &yes, "yes", "y", false)
	boolVar(fs, &showVer, "version", "v", false)
	fs.BoolVar(&initCfg, "init", false, "")
	fs.BoolVar(&showCfg, "config", false, "")
	fs.BoolVar(&debug, "debug", false, "")
	fs.StringVar(&modelOvr, "model", "", "")
	fs.StringVar(&modelOvr, "m", "", "")

	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, usage)
		return 1
	}

	if showVer {
		fmt.Println("cmd " + version)
		return 0
	}
	if initCfg {
		path, written, err := WriteDefaultConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not write config: %v\n", err)
			return 1
		}
		if written {
			fmt.Fprintf(os.Stderr, "Wrote default config to %s\n", path)
		} else {
			fmt.Fprintf(os.Stderr, "Config already exists at %s (left untouched)\n", path)
		}
		return 0
	}

	if showCfg {
		printEffectiveConfig(os.Stdout)
		return 0
	}

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		fmt.Fprint(os.Stderr, usage)
		return 1
	}

	cfg, err := LoadConfig()
	if err != nil {
		// A broken config is a warning, not a fatal error: defaults still work.
		fmt.Fprintf(os.Stderr, "Warning: %v (using defaults)\n", err)
	}
	if modelOvr != "" {
		cfg.Model = modelOvr
	}
	if think {
		cfg.EnableThink = true
	}

	// When stdout is redirected, behave like a well-mannered unix tool: emit
	// the command and nothing else, and never execute anything.
	if !isTerminal(os.Stdout) {
		quiet = true
	}

	p := palette{enabled: colorsEnabled() && !quiet}

	sample := readPipedInput(os.Stdin, cfg.SampleReadBytes, cfg.MaxPipeChars)
	userMessage := buildUserMessage(query, sample)

	if debug {
		args := buildArgs(cfg, "<user message omitted>", cfg.EnableThink)
		fmt.Fprintf(os.Stderr, "%s %s\n\n", p.Dim("claude"), p.Dim(strings.Join(args, " ")))
	}

	cmdText, err := generateCommand(cfg, userMessage, quiet, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", p.Red("Error:"), err)
		return 1
	}
	if cmdText == "" {
		fmt.Fprintf(os.Stderr, "%s no command was returned.\n", p.Red("Error:"))
		return 1
	}

	// The model was told to answer with a "# " line when the request cannot be
	// expressed as a command. Show it, but never offer to run it.
	if IsRefusal(cmdText) {
		fmt.Fprintf(os.Stderr, "%s\n", p.Yellow(cmdText))
		return 1
	}

	// Decoration on stderr, the command itself on stdout.
	if !quiet {
		fmt.Fprint(os.Stderr, "\n"+p.Yellow("> "))
	}
	fmt.Fprint(os.Stdout, cmdText)
	if !quiet {
		fmt.Fprint(os.Stderr, p.wrap("0", ""))
	}
	fmt.Fprintln(os.Stdout)

	if quiet {
		return 0
	}

	// The piped data was consumed to build the sample and cannot be replayed,
	// so a stdin-reading command would just block on the terminal here.
	if !sample.Empty() && readsStdin(cmdText) {
		fmt.Fprintf(os.Stderr, "\n%s the piped input was consumed as context, so this reads from your terminal.\n      Re-run it against your data:  %s\n",
			p.Yellow("Note:"), p.Dim("<your pipe> | "+strings.ReplaceAll(cmdText, "\n", " ")))
	}

	if ph := Placeholders(cmdText); len(ph) > 0 {
		fmt.Fprintf(os.Stderr, "\n%s replace %s before running.\n",
			p.Yellow("Note:"), strings.Join(ph, ", "))
	}

	risks := Risks(cmdText, cfg.DangerousPatterns)

	in, cleanup := promptSource()
	defer cleanup()

	if !yes || len(risks) > 0 {
		if !Confirm(os.Stderr, in, p, risks) {
			fmt.Fprintln(os.Stderr, p.Dim("Aborted."))
			return 2
		}
	}

	fmt.Fprintln(os.Stderr)
	code, err := Run(cmdText, in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", p.Red("Execution failed:"), err)
		return 1
	}
	return code
}

// printEffectiveConfig reports where the config was read from and what values
// are actually in force. A config file written by an older version silently
// overrides newer defaults, and this is how you find out.
func printEffectiveConfig(w io.Writer) {
	path, err := ConfigPath()
	if err != nil {
		fmt.Fprintf(w, "Could not resolve config path: %v\n", err)
		return
	}
	source := "defaults (no file)"
	if _, statErr := os.Stat(path); statErr == nil {
		source = path
	}
	cfg, loadErr := LoadConfig()
	if loadErr != nil {
		fmt.Fprintf(w, "Warning: %v\n", loadErr)
	}

	fmt.Fprintf(w, "config file:  %s\n", path)
	fmt.Fprintf(w, "in use:       %s\n\n", source)
	fmt.Fprintf(w, "model             %s\n", cfg.Model)
	fmt.Fprintf(w, "claude_path       %s\n", cfg.ClaudePath)
	fmt.Fprintf(w, "max_pipe_chars    %d (sent to the model)\n", cfg.MaxPipeChars)
	fmt.Fprintf(w, "sample_read_bytes %d (max ever read from stdin)\n", cfg.SampleReadBytes)
	fmt.Fprintf(w, "enable_think      %t\n", cfg.EnableThink)
	fmt.Fprintf(w, "effort            %s (pinned to \"low\" unless -t)\n", cfg.Effort)
	fmt.Fprintf(w, "timeout_seconds   %d\n", cfg.TimeoutSeconds)
	fmt.Fprintf(w, "show_thinking     %t (only applies with -t)\n", cfg.ShowThinking)
	fmt.Fprintf(w, "dangerous_patterns %d custom\n", len(cfg.DangerousPatterns))
}

// generateCommand streams a response from Claude, showing a live single-line
// preview on stderr, and returns the sanitized command.
func generateCommand(cfg Config, userMessage string, quiet bool, p palette) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	interactive := !quiet && isTerminal(os.Stderr)

	// Reasoning is only ever displayed when it was explicitly asked for. The
	// model may emit thinking blocks even at low effort, so gating on
	// show_thinking alone would leak reasoning into a plain run -- including
	// for anyone whose config file predates the current defaults.
	showReasoning := interactive && cfg.EnableThink && cfg.ShowThinking

	// "Loading" until the model actually says something: until the first event
	// arrives we are waiting on the request, not on any reasoning. The label
	// switches to "Thinking" only once a reasoning block really starts.
	spinner := NewSpinner(os.Stderr, interactive)
	spinner.Start("Loading")

	var (
		out          bytes.Buffer
		streamErr    string
		startedText  bool
		startedThink bool
	)

	emit := func(ev Event) {
		switch ev.Kind {
		case EventThinking:
			if !showReasoning {
				// Reasoning is hidden, but the fact that it started is still
				// worth reflecting in the label.
				spinner.SetMessage("Thinking")
				return
			}
			if !startedThink {
				spinner.Stop()
				fmt.Fprintf(os.Stderr, "%s\n%s", p.Dim("reasoning:"), p.dimOn())
				startedThink = true
			}
			// Written raw: the dim attribute is opened once above and closed
			// when the block ends.
			fmt.Fprint(os.Stderr, ev.Text)

		case EventText:
			out.WriteString(ev.Text)
			if !interactive {
				return
			}
			if !startedText {
				spinner.Stop()
				if startedThink {
					fmt.Fprintln(os.Stderr, p.reset())
				}
				startedText = true
			}
			// Live preview: rewrite one line so the final, sanitized command
			// can replace it cleanly without leaving duplicated output.
			fmt.Fprintf(os.Stderr, "\r\033[K%s%s", p.Dim("> "), p.Dim(previewLine(out.String())))

		case EventError:
			streamErr = ev.Text
		}
	}

	genErr := Generate(ctx, cfg, userMessage, cfg.EnableThink, emit)
	spinner.Stop()
	if interactive && startedText {
		fmt.Fprint(os.Stderr, "\r\033[K") // erase the preview
	}
	if interactive && startedThink && !startedText {
		// Reasoning ran but no command followed; close the dim attribute so it
		// does not bleed into the shell prompt.
		fmt.Fprintln(os.Stderr, p.reset())
	}

	if genErr != nil {
		return "", genErr
	}
	if streamErr != "" {
		return "", errorFromClaude(streamErr)
	}
	return Sanitize(out.String()), nil
}

// previewLine renders the tail of the streamed text on a single line.
func previewLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimPrefix(strings.TrimSpace(s), "```")
	const maxWidth = 90
	if len(s) > maxWidth {
		s = "…" + s[len(s)-maxWidth:]
	}
	return s
}

// errorFromClaude turns a CLI error string into actionable advice.
func errorFromClaude(msg string) error {
	if strings.Contains(strings.ToLower(msg), "not logged in") ||
		strings.Contains(strings.ToLower(msg), "authentication") {
		return fmt.Errorf("%s\nRun `claude` once and sign in, then try again", msg)
	}
	return fmt.Errorf("%s", msg)
}

// readPipedInput describes stdin when it is a pipe or a redirected file.
// Nothing is read when stdin is a terminal.
func readPipedInput(stdin *os.File, readCap, sendCap int) Sample {
	if isTerminal(stdin) {
		return Sample{}
	}
	return BuildSample(stdin, readCap, sendCap)
}

// fileArg matches an unquoted argument that names a file: either it contains a
// path separator, or it ends in a short extension.
var fileArg = regexp.MustCompile(`/|\.[A-Za-z0-9]{1,6}$`)

func looksLikePath(s string) bool { return fileArg.MatchString(s) }

// readsStdin reports whether a command consumes stdin rather than naming its
// own input. Used to warn that piped data cannot be replayed to it.
func readsStdin(cmdText string) bool {
	// A redirect or an explicit path means the command opens its own input.
	if strings.Contains(cmdText, "<") && !strings.Contains(cmdText, "<<") {
		return false
	}
	fields := strings.Fields(cmdText)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue
		}
		// Quoted arguments are programs (jq filters, awk scripts), not paths.
		if strings.HasPrefix(f, "'") || strings.HasPrefix(f, `"`) {
			continue
		}
		if looksLikePath(f) {
			return false
		}
	}
	return true
}

// boolVar registers a long and short name for the same bool flag.
func boolVar(fs *flag.FlagSet, target *bool, long, short string, def bool) {
	fs.BoolVar(target, long, def, "")
	fs.BoolVar(target, short, def, "")
}
