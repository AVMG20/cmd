package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// promptSource returns a reader for interactive answers plus a cleanup func.
//
// This exists because the tool's own stdin is frequently a pipe
// (`cat data.json | cmd "..."`), and at that point stdin is at EOF: reading the
// confirmation from it would silently answer "no" every time. Opening the
// controlling terminal directly avoids that.
func promptSource() (io.Reader, func()) {
	if isTerminal(os.Stdin) {
		return os.Stdin, func() {}
	}
	tty, err := os.Open(ttyPath())
	if err != nil {
		return os.Stdin, func() {}
	}
	return tty, func() { tty.Close() }
}

func ttyPath() string {
	if runtime.GOOS == "windows" {
		return "CONIN$"
	}
	return "/dev/tty"
}

// abortExit is the exit status for a command the user declined. It is distinct
// from 1 so a script can tell "you said no" apart from "it went wrong".
const abortExit = 2

// Action is what the user chose to do with a generated command.
type Action int

const (
	ActionAbort Action = iota
	ActionRun
	ActionCopy
	// ActionEdit hands the command back to the line editor. Only the
	// interactive harness offers it, because only it has an editor to hand.
	ActionEdit
)

// Confirm asks the user what to do with cmdText.
//
// An ordinary command answers to a single keypress -- y to run, c to copy, any
// other key to abort -- with no Enter needed, because the whole point of this
// tool is to be quicker than typing the command yourself.
//
// A risky command deliberately does not. It falls back to reading a line and
// demands the full word "yes", so that the one case where a stray keystroke
// would be expensive is the one case that cannot be triggered by a stray
// keystroke.
func Confirm(out io.Writer, in io.Reader, p palette, risks []string) Action {
	return confirm(out, in, p, risks, false, false)
}

// ConfirmInteractive is Confirm for the harness, which already holds the
// terminal in raw mode and so lends it rather than opening its own. It adds one
// choice: e to edit the command before deciding. copyDefault reflects /copy,
// which swaps which of run and copy the prompt leads with.
func ConfirmInteractive(out io.Writer, term *rawTerminal, p palette, risks []string, copyDefault bool) Action {
	if len(risks) > 0 {
		// A risky command needs a typed word, which needs a line editor.
		return confirmRisky(out, term, p, risks, true)
	}
	fmt.Fprint(out, "\n"+p.Yellow(confirmQuestion(copyDefault))+p.Dim(confirmChoices(true, copyDefault)))
	// Anything typed while the model was working is not an answer to a
	// question that had not been asked yet. Dropping it stops a stray "y" from
	// running a command nobody looked at.
	term.Drain()
	action := actionForKey(term.ReadKey(), copyDefault)
	fmt.Fprintf(out, "%s\n", p.Dim(actionLabel(action)))
	return action
}

// confirmRisky reads a typed word through the harness's own line editor.
//
// The question goes on its own line above the input rather than in front of
// it. The editor redraws its line from column zero, so anything printed on the
// same line would be erased by the first keystroke.
func confirmRisky(out io.Writer, term *rawTerminal, p palette, risks []string, allowEdit bool) Action {
	showRisks(out, p, risks)
	fmt.Fprintf(out, "\n%s\n", p.Red("Type 'yes' to run it, 'c' to copy, 'e' to edit, anything else aborts."))
	term.Drain()

	editor := NewEditor(out, term, p, "› ", ".", nil)
	line, result := editor.Read("")
	if result != EditSubmit {
		return ActionAbort
	}
	return riskyAnswer(strings.ToLower(strings.TrimSpace(line)), allowEdit)
}

func riskyAnswer(answer string, allowEdit bool) Action {
	switch answer {
	case "yes":
		return ActionRun
	case "c", "copy":
		return ActionCopy
	case "e", "edit":
		if allowEdit {
			return ActionEdit
		}
	}
	return ActionAbort
}

func showRisks(out io.Writer, p palette, risks []string) {
	fmt.Fprintf(out, "\n%s\n", p.Red("⚠  This command is potentially destructive:"))
	for _, r := range risks {
		fmt.Fprintf(out, "   %s %s\n", p.Red("•"), r)
	}
}

func confirm(out io.Writer, in io.Reader, p palette, risks []string, allowEdit, copyDefault bool) Action {
	if len(risks) > 0 {
		showRisks(out, p, risks)
		question := "Type 'yes' to run it, 'c' to copy, anything else aborts: "
		if allowEdit {
			question = "Type 'yes' to run it, 'c' to copy, 'e' to edit, anything else aborts: "
		}
		fmt.Fprint(out, "\n"+p.Red(question))
		return riskyAnswer(strings.ToLower(strings.TrimSpace(readLine(in))), allowEdit)
	}

	fmt.Fprint(out, "\n"+p.Yellow(confirmQuestion(copyDefault))+p.Dim(confirmChoices(allowEdit, copyDefault)))

	if term, ok := enterRaw(); ok {
		defer term.Restore()
		key := term.ReadKey()
		action := actionForKey(key, copyDefault)
		if action == ActionEdit && !allowEdit {
			action = ActionAbort
		}
		// Raw mode swallowed the echo, so the choice is printed back to keep
		// the transcript readable.
		fmt.Fprintf(out, "%s\n", p.Dim(actionLabel(action)))
		return action
	}

	// No terminal to put in raw mode (a test, a pipe, an odd environment):
	// fall back to a typed line so the prompt is still answerable.
	switch strings.ToLower(strings.TrimSpace(readLine(in))) {
	case "y", "yes":
		if copyDefault {
			return ActionCopy
		}
		return ActionRun
	case "c", "copy":
		return ActionCopy
	case "e", "edit":
		if allowEdit {
			return ActionEdit
		}
	}
	return ActionAbort
}

// confirmQuestion and confirmChoices render the prompt. With /copy on, copying
// is what the affirmative key does, and the prompt has to say so.
func confirmQuestion(copyDefault bool) string {
	if copyDefault {
		return "Copy?"
	}
	return "Execute?"
}

func confirmChoices(allowEdit, copyDefault bool) string {
	first := " [y] run  [c] copy"
	if copyDefault {
		first = " [y] copy  [r] run"
	}
	if allowEdit {
		return first + "  [e] edit  [n] cancel "
	}
	return first + "  [n] cancel "
}

// actionForKey maps a keypress to a choice. Anything unrecognised aborts,
// which is the safe direction to resolve a misplaced keystroke.
//
// copyDefault is /copy: it changes what the affirmative key means, but never
// what an explicit key means -- r still runs and c still copies either way, so
// muscle memory does not become a trap when the mode is on.
func actionForKey(k Key, copyDefault bool) Action {
	if k.Name == KeyEnter {
		// Enter alone is ambiguous -- it is as likely to be a leftover from
		// typing the request as it is to be an answer -- so it does not run.
		return ActionAbort
	}
	switch k.Rune {
	case 'y', 'Y':
		if copyDefault {
			return ActionCopy
		}
		return ActionRun
	case 'r', 'R':
		return ActionRun
	case 'c', 'C':
		return ActionCopy
	case 'e', 'E':
		return ActionEdit
	}
	return ActionAbort
}

func actionLabel(a Action) string {
	switch a {
	case ActionRun:
		return "run"
	case ActionCopy:
		return "copy"
	case ActionEdit:
		return "edit"
	}
	return "cancel"
}

// readLine reads a single line, returning "" at EOF.
func readLine(in io.Reader) string {
	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		return ""
	}
	return sc.Text()
}

// shellInvocation returns the shell and flag used to run a generated command.
func shellInvocation() (string, string) {
	if runtime.GOOS == "windows" {
		if comspec := os.Getenv("COMSPEC"); comspec != "" {
			return comspec, "/C"
		}
		return "cmd", "/C"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, "-c"
	}
	return "/bin/sh", "-c"
}

// Run executes the generated command in the user's shell and returns its exit
// code. stdin is inherited from tty when available so interactive commands
// (git commit, less, sudo) still work after our own stdin was consumed.
func Run(cmdText string, stdin io.Reader) (int, error) {
	shell, flag := shellInvocation()
	c := exec.Command(shell, flag, cmdText)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = stdin

	err := c.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
