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

// Confirm asks the user whether to run cmdText.
//
// A command flagged as risky requires typing the full word "yes"; anything else
// accepts a plain "y". Returning false means do not run.
func Confirm(out io.Writer, in io.Reader, p palette, risks []string) bool {
	question := "Execute? [y/N]: "
	if len(risks) > 0 {
		fmt.Fprintf(out, "\n%s\n", p.Red("⚠  This command is potentially destructive:"))
		for _, r := range risks {
			fmt.Fprintf(out, "   %s %s\n", p.Red("•"), r)
		}
		question = p.Red("Type 'yes' to run it, anything else aborts: ")
	}
	fmt.Fprint(out, "\n"+question)

	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		// No terminal available to answer on: default to not running.
		fmt.Fprintln(out)
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))

	if len(risks) > 0 {
		return answer == "yes"
	}
	return answer == "y" || answer == "yes"
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
