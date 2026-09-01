package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// The interactive harness: run `cmd` with no request and it opens, takes one
// request with @file completion and a /command palette, shows the command it
// produced, and exits on run, copy or cancel.
//
// It is not a shell and not an agent. It answers one question and gets out of
// the way; `cmd "..."` remains the faster path when the request is already in
// your head.

// requestPrompt is the marker shown while a request is being typed.
const requestPrompt = "› "

// atRef matches an @-prefixed file reference in a request.
var atRef = regexp.MustCompile(`@([^\s]+)`)

// session is the mutable state of one interactive run.
type session struct {
	cfg  Config
	p    palette
	term *rawTerminal
	// out wraps stderr so ordinary "\n" output still renders correctly while
	// the terminal is in raw mode.
	out     io.Writer
	history *History
	// copyMode makes an accepted command go to the clipboard rather than the
	// shell, toggled with /copy.
	copyMode bool
}

// Interactive runs the harness and returns the process exit code.
//
// Raw mode is entered exactly once and held for the whole session. That is not
// a stylistic choice: a read already parked in the kernel on a terminal
// survives closing the descriptor and consumes the next keypress, so tearing
// raw mode down and building it back up between requests silently swallows a
// keystroke every time. Output is wrapped instead, so the rest of the program
// does not have to know which mode the terminal is in.
func Interactive(cfg Config, p palette) int {
	term, ok := enterRaw()
	if !ok {
		fmt.Fprintf(os.Stderr, "%s this needs a terminal. Pass your request as an argument instead:\n  %s\n",
			p.Red("Error:"), p.Dim(`cmd "what you want to do"`))
		return 1
	}
	defer term.Restore()

	s := &session{
		cfg:     cfg,
		p:       p,
		term:    term,
		out:     crlfWriter{os.Stderr},
		history: LoadHistory(),
	}
	s.banner()

	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	editor := NewEditor(os.Stderr, term, p, requestPrompt, root, s.history)

	for {
		line, result := editor.Read("")
		if result == EditQuit || (result == EditCancel && line == "") {
			fmt.Fprintf(s.out, "%s\n", p.Dim("Bye."))
			return 0
		}
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "/") {
			if quit := s.runSlash(line); quit {
				return 0
			}
			continue
		}

		s.history.Add(line)
		s.history.Reset()

		if code, done := s.handle(line, editor); done {
			return code
		}
	}
}

// handle generates a command for one request and offers it. It reports the
// exit code and whether the session should end.
func (s *session) handle(request string, editor *Editor) (int, bool) {
	query, files := parseRefs(request)

	collected := CollectFiles(query, files, s.cfg)
	userMessage := buildUserMessage(query, Sample{}, collected)

	cmdText, err := generateCommand(s.out, s.cfg, userMessage, false, s.p)
	if err != nil {
		fmt.Fprintf(s.out, "%s %v\n", s.p.Red("Error:"), err)
		return 1, false
	}
	if cmdText == "" {
		fmt.Fprintf(s.out, "%s no command was returned.\n", s.p.Red("Error:"))
		return 1, false
	}
	if IsRefusal(cmdText) {
		fmt.Fprintf(s.out, "%s\n", s.p.Yellow(cmdText))
		return 1, false
	}

	return s.offer(cmdText, editor)
}

// offer shows a command and acts on the answer, looping while the user keeps
// editing it.
//
// Every answer that is a decision -- run, copy, cancel -- ends the session.
// One request is the whole job, and a cancel is an answer to it, not a step
// back into the prompt: the user who wants a different command reaches for
// cmd again, which is cheaper than a harness that lingers.
func (s *session) offer(cmdText string, editor *Editor) (int, bool) {
	for {
		fmt.Fprintf(s.out, "\n%s%s\n", s.p.Yellow("> "), cmdText)

		if ph := Placeholders(cmdText); len(ph) > 0 {
			fmt.Fprintf(s.out, "\n%s replace %s before running.\n",
				s.p.Yellow("Note:"), strings.Join(ph, ", "))
		}
		risks := Risks(cmdText, s.cfg.DangerousPatterns)

		action := ConfirmInteractive(s.out, s.term, s.p, risks, s.copyMode)
		if action == ActionEdit {
			edited, ok := s.editCommand(cmdText, editor)
			if ok && strings.TrimSpace(edited) != "" {
				cmdText = edited
				continue
			}
			// Abandoning the edit abandons the command with it: an edit is a
			// step towards an answer, so giving up on one is the same answer
			// as cancelling outright.
			action = ActionAbort
		}

		switch action {
		case ActionCopy:
			return reportCopy(s.out, cmdText, s.p), true
		case ActionAbort:
			fmt.Fprintln(s.out, s.p.Dim("Aborted."))
			return abortExit, true
		}

		// The command inherits this terminal, so it needs a normal one. The
		// session always ends here, so raw mode is not reclaimed.
		fmt.Fprintln(s.out)
		s.term.Restore()

		runIn, cleanupRun := promptSource()
		code, runErr := Run(cmdText, runIn)
		cleanupRun()
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", s.p.Red("Execution failed:"), runErr)
			return 1, true
		}
		return code, true
	}
}

// editCommand hands the generated command back to the line editor.
func (s *session) editCommand(cmdText string, editor *Editor) (string, bool) {
	// History would be the wrong thing to browse here: this is a command, not
	// a request, and mixing the two would corrupt both. The prompt changes for
	// the same reason -- what is being edited is no longer English.
	saved := editor.history
	editor.history = nil
	editor.SetPrompt("$ ")
	line, result := editor.Read(cmdText)
	editor.SetPrompt(requestPrompt)
	editor.history = saved
	return line, result == EditSubmit
}

// parseRefs splits @file references out of a request.
//
// The marker is stripped before the text reaches the model: it is input
// syntax, not something the model should reason about. The paths are passed
// along as explicit files, which is the same route -f uses.
func parseRefs(request string) (query string, files []string) {
	query = atRef.ReplaceAllStringFunc(request, func(m string) string {
		path := strings.TrimSuffix(m[1:], "/")
		files = append(files, path)
		return path
	})
	return strings.TrimSpace(query), files
}

// banner prints what is in force, so the first thing on screen answers "which
// backend and model am I about to spend?".
func (s *session) banner() {
	fmt.Fprintf(s.out, "%s %s\n", s.p.Yellow("cmd"), s.p.Dim(version))
	fmt.Fprintf(s.out, "%s\n", s.p.Dim(s.cfg.Provider+" · "+s.cfg.ActiveModel()))
	fmt.Fprintf(s.out, "%s\n\n", s.p.Dim("@ for files · / for commands · ctrl-d to leave"))
}

// runSlash executes a palette command. It reports whether to leave.
func (s *session) runSlash(line string) bool {
	name := strings.Fields(line)[0]

	switch name {
	case "/exit", "/quit":
		fmt.Fprintf(s.out, "%s\n", s.p.Dim("Bye."))
		return true

	case "/help":
		s.printHelp()

	case "/think":
		s.cfg.EnableThink = !s.cfg.EnableThink
		s.report("reasoning", onOff(s.cfg.EnableThink))

	case "/copy":
		s.copyMode = !s.copyMode
		s.report("copy instead of run", onOff(s.copyMode))

	case "/config":
		// The wizard reads through this same terminal rather than switching
		// modes, because giving raw mode up and taking it back would cost a
		// keypress.
		ConfigureRaw(s.out, s.term, s.p)
		if cfg, err := LoadConfig(); err == nil {
			s.cfg = cfg
		}
		// The banner named the backend and model, and it has scrolled away by
		// now. Saying what is in force again is what makes /config the only
		// place either of them is chosen.
		s.report("now using", s.cfg.Provider+" · "+s.cfg.ActiveModel())

	default:
		s.warn(fmt.Sprintf("unknown command %q; /help lists them", name))
	}
	return false
}

func (s *session) printHelp() {
	fmt.Fprintf(s.out, "\n%s\n", s.p.Yellow("Commands"))
	for _, c := range slashCommands {
		fmt.Fprintf(s.out, "  %-20s %s\n", c.name, s.p.Dim(c.summary))
	}
	fmt.Fprintf(s.out, "\n%s\n", s.p.Yellow("Editing"))
	for _, l := range [][2]string{
		{"@name", "complete a file path from here"},
		{"tab / enter", "accept the highlighted completion"},
		{"up / down", "move through completions, or previous requests"},
		{"ctrl-w / ctrl-u", "delete the last word / the line"},
		{"ctrl-d", "leave"},
	} {
		fmt.Fprintf(s.out, "  %-20s %s\n", l[0], s.p.Dim(l[1]))
	}
	fmt.Fprint(s.out, "\n")
}

func (s *session) report(what, value string) {
	fmt.Fprintf(s.out, "%s %s\n", s.p.Dim(what+":"), value)
}

func (s *session) warn(msg string) {
	fmt.Fprintf(s.out, "%s %s\n", s.p.Red("!"), msg)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
