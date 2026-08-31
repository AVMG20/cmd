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

const version = "0.3.0"

const usage = `cmd - turn plain English into a shell command.

Usage:
  cmd                               Open the harness and type a request there
  cmd [flags] "what you want to do"
  <command producing data> | cmd [flags] "what to do with it"

Flags:
  -c, --copy           Copy the command to the clipboard instead of running it
  -f, --file <path>    Include this file as context (repeatable)
  -t, --think          Let the model reason before answering (slower, better on hard requests)
  -m, --model <name>   Model to use for this run
  -p, --provider <p>   Backend for this run: claude, antigravity, openrouter
  -q, --quiet          Print only the command; never prompt, never execute
  -y, --yes            Skip the confirmation for commands that are not flagged as risky
      --configure      Interactive setup: pick a backend, a model and a key
      --config         Show the config file path and the settings in force
      --debug          Print the underlying request to stderr
  -h, --help           Show this help
  -v, --version        Show version

At the confirmation prompt a single keypress decides: y runs, c copies, anything
else cancels. Risky commands still need the word "yes" typed out.

Run cmd with no request to open the harness: @ completes file paths from the
current directory, / opens the command palette, up and down recall past
requests, and e at the prompt edits the command before running it.

Examples:
  cmd "show the 10 biggest files in this folder"
  cmd "list all titles in todo.json"          # reads todo.json to get the keys right
  cmd "strip the email column from users.csv" # names the file, so it can be edited in place
  cmd "what's using port 3000"
  cat access.log | cmd "count requests per status code"

Exit codes: 0 success, 1 error, 2 aborted by user.
`

func main() {
	os.Exit(run())
}

func run() int {
	var (
		think       bool
		quiet       bool
		yes         bool
		copyOnly    bool
		configure   bool
		showCfg     bool
		debug       bool
		showVer     bool
		modelOvr    string
		providerOvr string
		fileArgs    repeatedFlag
	)

	fs := flag.NewFlagSet("cmd", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own usage
	boolVar(fs, &think, "think", "t", false)
	boolVar(fs, &quiet, "quiet", "q", false)
	boolVar(fs, &yes, "yes", "y", false)
	boolVar(fs, &copyOnly, "copy", "c", false)
	boolVar(fs, &showVer, "version", "v", false)
	fs.BoolVar(&configure, "configure", false, "")
	fs.BoolVar(&showCfg, "config", false, "")
	fs.BoolVar(&debug, "debug", false, "")
	fs.StringVar(&modelOvr, "model", "", "")
	fs.StringVar(&modelOvr, "m", "", "")
	fs.StringVar(&providerOvr, "provider", "", "")
	fs.StringVar(&providerOvr, "p", "", "")
	fs.Var(&fileArgs, "file", "")
	fs.Var(&fileArgs, "f", "")

	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, usage)
		return 1
	}

	if showVer {
		fmt.Println("cmd " + version)
		return 0
	}
	if configure {
		in, cleanup := promptSource()
		defer cleanup()
		return Configure(os.Stderr, in, palette{enabled: colorsEnabled()})
	}

	if showCfg {
		printEffectiveConfig(os.Stdout)
		return 0
	}

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))

	cfg, err := LoadConfig()
	if err != nil {
		// A broken config is a warning, not a fatal error: defaults still work.
		fmt.Fprintf(os.Stderr, "Warning: %v (using defaults)\n", err)
	}
	if providerOvr != "" {
		if !validProviders[providerOvr] {
			fmt.Fprintf(os.Stderr, "Unknown provider %q (expected claude, antigravity or openrouter)\n", providerOvr)
			return 1
		}
		cfg.Provider = providerOvr
	}
	cfg = cfg.withModelOverride(modelOvr)
	if think {
		cfg.EnableThink = true
	}

	// When stdout is redirected, behave like a well-mannered unix tool: emit
	// the command and nothing else, and never execute anything.
	if !isTerminal(os.Stdout) {
		quiet = true
	}

	p := palette{enabled: colorsEnabled() && !quiet}

	// No request on the command line: open the harness, where the request can
	// be typed with file completion instead of fought through shell quoting.
	// Without a terminal there is nothing to open, so the usage text stands in.
	if query == "" {
		if quiet || !isTerminal(os.Stdin) {
			fmt.Fprint(os.Stderr, usage)
			return 1
		}
		return Interactive(cfg, p)
	}

	// A redirect (`cmd "..." < users.csv`) carries a real path even though it
	// arrives on stdin, so prefer naming the file over describing a stream:
	// the command can then target it, and stays re-runnable. It goes through
	// the same collector as -f so that naming the file in the request as well
	// does not send it twice.
	var sample Sample
	named := append([]string(nil), fileArgs...)
	if path, ok := StdinPath(os.Stdin); ok && cfg.ReadsFiles() {
		named = append(named, path)
	} else {
		sample = readPipedInput(os.Stdin, cfg.SampleReadBytes, cfg.MaxPipeChars)
	}
	files := CollectFiles(query, named, cfg)
	userMessage := buildUserMessage(query, sample, files)

	if debug {
		fmt.Fprintf(os.Stderr, "%s\n", p.Dim(describeRequest(cfg, files)))
	}

	cmdText, err := generateCommand(os.Stderr, cfg, userMessage, quiet, p)
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

	// -c is an explicit instruction, so it is honoured even when output is
	// redirected: the command still goes to stdout, and a copy is still made.
	if copyOnly {
		return reportCopy(os.Stderr, cmdText, p)
	}

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
		switch Confirm(os.Stderr, in, p, risks) {
		case ActionCopy:
			return reportCopy(os.Stderr, cmdText, p)
		case ActionAbort:
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

// reportCopy puts the command on the clipboard and says where it went.
func reportCopy(out io.Writer, cmdText string, p palette) int {
	via, err := Copy(cmdText)
	if err != nil {
		fmt.Fprintf(out, "%s could not copy: %v\n", p.Red("Error:"), err)
		return 1
	}
	fmt.Fprintf(out, "%s copied to the clipboard %s\n", p.Green("✓"), p.Dim("("+via+")"))
	return 0
}

// describeRequest summarises what is about to be sent, for --debug.
func describeRequest(cfg Config, files []FileInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "provider=%s model=%s think=%t", cfg.Provider, cfg.ActiveModel(), cfg.EnableThink)
	if cfg.Provider == ProviderClaude {
		fmt.Fprintf(&b, "\nclaude %s", strings.Join(buildArgs(cfg, "<user message omitted>", cfg.EnableThink), " "))
	}
	if cfg.Provider == ProviderAgy {
		fmt.Fprintf(&b, "\n%s %s", cfg.AgyPath, strings.Join(buildAgyArgs(cfg, "<user message omitted>", cfg.EnableThink), " "))
	}
	for _, f := range files {
		fmt.Fprintf(&b, "\nfile: %s (%s, %d bytes read)", f.Path, f.Sample.Format, f.Sample.BytesRead)
	}
	return b.String()
}

// repeatedFlag collects a string flag that may be given more than once.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }

func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
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
	fmt.Fprintf(w, "provider          %s\n", cfg.Provider)
	fmt.Fprintf(w, "model             %s (for this provider)\n", cfg.ActiveModel())
	fmt.Fprintf(w, "claude_path       %s\n", cfg.ClaudePath)
	fmt.Fprintf(w, "agy_path          %s\n", cfg.AgyPath)
	fmt.Fprintf(w, "openrouter key    %s\n", describeKey(cfg))
	fmt.Fprintf(w, "auto_read_files   %t\n", cfg.ReadsFiles())
	fmt.Fprintf(w, "max_auto_files    %d\n", cfg.MaxAutoFiles)
	fmt.Fprintf(w, "max_pipe_chars    %d (sent to the model)\n", cfg.MaxPipeChars)
	fmt.Fprintf(w, "sample_read_bytes %d (max ever read from stdin)\n", cfg.SampleReadBytes)
	fmt.Fprintf(w, "enable_think      %t\n", cfg.EnableThink)
	fmt.Fprintf(w, "effort            %s (pinned to \"low\" unless -t)\n", cfg.Effort)
	fmt.Fprintf(w, "timeout_seconds   %d\n", cfg.TimeoutSeconds)
	fmt.Fprintf(w, "show_thinking     %t (only applies with -t)\n", cfg.ShowThinking)
	fmt.Fprintf(w, "dangerous_patterns %d custom\n", len(cfg.DangerousPatterns))
}

// describeKey says whether a key is available and where it came from, without
// printing it.
func describeKey(cfg Config) string {
	if os.Getenv("OPENROUTER_API_KEY") != "" {
		return masked(os.Getenv("OPENROUTER_API_KEY")) + " (from OPENROUTER_API_KEY)"
	}
	if cfg.OpenRouterAPIKey != "" {
		return masked(cfg.OpenRouterAPIKey) + " (from the config file)"
	}
	return "not set"
}

// generateCommand streams a response from Claude, showing a live single-line
// preview on stderr, and returns the sanitized command.
func generateCommand(out io.Writer, cfg Config, userMessage string, quiet bool, p palette) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	interactive := !quiet && isTerminal(os.Stderr)
	// Backends that answer in one shot have nothing to preview, so the spinner
	// carries the whole wait.
	streaming := Streams(cfg.Provider)

	// Reasoning is only ever displayed when it was explicitly asked for. The
	// model may emit thinking blocks even at low effort, so gating on
	// show_thinking alone would leak reasoning into a plain run -- including
	// for anyone whose config file predates the current defaults.
	showReasoning := interactive && cfg.EnableThink && cfg.ShowThinking

	// "Loading" until the model actually says something: until the first event
	// arrives we are waiting on the request, not on any reasoning. The label
	// switches to "Thinking" only once a reasoning block really starts.
	spinner := NewSpinner(out, interactive)
	spinner.Start("Loading")

	var (
		answer       bytes.Buffer
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
				fmt.Fprintf(out, "%s\n%s", p.Dim("reasoning:"), p.dimOn())
				startedThink = true
			}
			// Written raw: the dim attribute is opened once above and closed
			// when the block ends.
			fmt.Fprint(out, ev.Text)

		case EventText:
			answer.WriteString(ev.Text)
			if !interactive || !streaming {
				return
			}
			if !startedText {
				spinner.Stop()
				if startedThink {
					fmt.Fprintln(out, p.reset())
				}
				startedText = true
			}
			// Live preview: rewrite one line so the final, sanitized command
			// can replace it cleanly without leaving duplicated output.
			fmt.Fprintf(out, "\r\033[K%s%s", p.Dim("> "), p.Dim(previewLine(answer.String())))

		case EventError:
			streamErr = ev.Text
		}
	}

	genErr := Generate(ctx, cfg, userMessage, cfg.EnableThink, emit)
	spinner.Stop()
	if interactive && startedText {
		fmt.Fprint(out, "\r\033[K") // erase the preview
	}
	if interactive && startedThink && !startedText {
		// Reasoning ran but no command followed; close the dim attribute so it
		// does not bleed into the shell prompt.
		fmt.Fprintln(out, p.reset())
	}

	if genErr != nil {
		return "", genErr
	}
	if streamErr != "" {
		return "", errorFromClaude(streamErr)
	}
	return Sanitize(answer.String()), nil
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

// fileArg matches an argument that names a file: either it contains a path
// separator, or it ends in an extension. The extension bound is generous
// enough for the likes of .sqlite3 and .markdown; over-matching is cheap,
// because every caller confirms the file exists before acting on it.
var fileArg = regexp.MustCompile(`/|\.[A-Za-z0-9]{1,8}$`)

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
