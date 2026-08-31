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

// Action is what the user chose to do with a generated command.
type Action int

const (
	ActionAbort Action = iota
	ActionRun
	ActionCopy
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
	if len(risks) > 0 {
		fmt.Fprintf(out, "\n%s\n", p.Red("⚠  This command is potentially destructive:"))
		for _, r := range risks {
			fmt.Fprintf(out, "   %s %s\n", p.Red("•"), r)
		}
		fmt.Fprint(out, "\n"+p.Red("Type 'yes' to run it, 'c' to copy, anything else aborts: "))
		switch strings.ToLower(strings.TrimSpace(readLine(in))) {
		case "yes":
			return ActionRun
		case "c", "copy":
			return ActionCopy
		}
		return ActionAbort
	}

	prompt := "\n" + p.Yellow("Execute?") + p.Dim(" [y] run  [c] copy  [n] cancel ")
	fmt.Fprint(out, prompt)

	if term, ok := enterRaw(); ok {
		defer term.Restore()
		key := term.ReadKey()
		action := actionForKey(key)
		// Raw mode swallowed the echo, so the choice is printed back to keep
		// the transcript readable.
		fmt.Fprintf(out, "%s\n", p.Dim(actionLabel(action)))
		return action
	}

	// No terminal to put in raw mode (a test, a pipe, an odd environment):
	// fall back to a typed line so the prompt is still answerable.
	answer := strings.ToLower(strings.TrimSpace(readLine(in)))
	switch answer {
	case "y", "yes":
		return ActionRun
	case "c", "copy":
		return ActionCopy
	}
	return ActionAbort
}

// actionForKey maps a keypress to a choice. Anything unrecognised aborts,
// which is the safe direction to resolve a misplaced keystroke.
func actionForKey(k Key) Action {
	if k.Name == KeyEnter {
		// Enter alone is ambiguous -- it is as likely to be a leftover from
		// typing the request as it is to be an answer -- so it does not run.
		return ActionAbort
	}
	switch k.Rune {
	case 'y', 'Y':
		return ActionRun
	case 'c', 'C':
		return ActionCopy
	}
	return ActionAbort
}

func actionLabel(a Action) string {
	switch a {
	case ActionRun:
		return "run"
	case ActionCopy:
		return "copy"
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
