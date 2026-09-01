package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// clipTool is an external program that can take text on stdin and put it on
// the system clipboard.
type clipTool struct {
	bin  string
	args []string
	// name is what the user is told was used.
	name string
}

// clipTools are tried in order. The list covers macOS, Wayland, X11 and WSL.
var clipTools = []clipTool{
	{bin: "pbcopy", name: "pbcopy"},
	{bin: "wl-copy", name: "wl-copy"},
	{bin: "xclip", args: []string{"-selection", "clipboard"}, name: "xclip"},
	{bin: "xsel", args: []string{"--clipboard", "--input"}, name: "xsel"},
	{bin: "clip.exe", name: "clip.exe"},
	{bin: "clip", name: "clip"},
}

// Copy places text on the system clipboard and reports how it got there.
//
// A local clipboard program is preferred, but the fallback is the interesting
// one: OSC 52 asks the *terminal emulator* to set the clipboard, which is the
// only thing that works over SSH. That matters here, because wanting the
// command somewhere other than this shell is the whole reason to copy it.
func Copy(text string) (string, error) {
	for _, t := range clipTools {
		bin, err := exec.LookPath(t.bin)
		if err != nil {
			continue
		}
		c := exec.Command(bin, t.args...)
		c.Stdin = strings.NewReader(text)
		if err := c.Run(); err != nil {
			continue // try the next one rather than giving up
		}
		return t.name, nil
	}
	if err := copyOSC52(text); err != nil {
		return "", err
	}
	return "terminal (OSC 52)", nil
}

// osc52Limit guards against emitting an escape sequence larger than terminals
// will accept. Anything near it is not a shell command.
const osc52Limit = 100_000

// copyOSC52 writes the terminal escape sequence that sets the clipboard.
//
// It goes to the terminal device rather than stderr so that a redirected
// stderr cannot swallow it, and so the sequence never lands in a file.
func copyOSC52(text string) error {
	if len(text) > osc52Limit {
		return fmt.Errorf("no clipboard program found (install xclip, wl-copy or pbcopy)")
	}
	tty, err := os.OpenFile(ttyPath(), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("no clipboard program found (install xclip, wl-copy or pbcopy)")
	}
	defer tty.Close()

	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	// tmux does not forward OSC sequences to the outer terminal unless they
	// are wrapped, and the wrapper is harmless to a terminal that is not tmux
	// only when tmux is actually present -- so it is applied conditionally.
	seq := "\033]52;c;" + encoded + "\a"
	if os.Getenv("TMUX") != "" {
		seq = "\033Ptmux;\033" + seq + "\033\\"
	}
	if _, err := tty.WriteString(seq); err != nil {
		return fmt.Errorf("writing to the terminal: %w", err)
	}
	return nil
}
