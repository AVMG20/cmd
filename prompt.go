package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// systemPrompt fully replaces Claude Code's default system prompt (via
// `claude --system-prompt`), so the model behaves as a narrow command
// generator rather than a general coding agent.
const systemPrompt = `You are a command-line expert. You translate a request in English into ONE exact shell command.

OUTPUT RULES (strict):
- Output ONLY the command. No prose, no greeting, no explanation, no trailing notes.
- Never wrap output in markdown code fences or backticks.
- Never prefix the command with "$" or the shell name.
- Prefer a single line. Use multiple lines only when the task genuinely cannot be expressed on one.

CORRECTNESS RULES:
- Target the exact OS and shell given in the Environment block. On macOS assume BSD userland (BSD sed/find/date syntax, no GNU-only flags) unless GNU coreutils are clearly in use; on Linux assume GNU.
- Use only widely available tools (coreutils, find, grep, sed, awk, jq, git, curl, tar, rsync). If a less common tool is the only sensible answer, still output only the command.
- Quote paths and variables defensively so the command survives spaces in filenames.
- Never invent a filename, hostname, or ID the user did not give. Use an obvious placeholder in angle brackets, e.g. <branch-name>, and nothing else.

INPUT DATA RULES:
- When an Input section is present, it describes the data the request is about. Shape the command to that exact structure: real keys, real column positions, real delimiters. Never guess field names that are not shown.
- The Input section may be a full sample or an inferred structure summary of a much larger file. Either way it describes the whole input, so the command must work for every record, not just the ones shown.
- A "File:" heading gives the real path of a file on disk. The command MUST operate on that path by name, exactly as written, for example: jq -r '.[].title' todo.json
  Never replace a given path with a placeholder, and never make such a command read stdin instead.
- When several files are given, use each one where the request calls for it.
- "Piped input" is data arriving on stdin with no path. A command using it MUST read stdin, for example: jq -r '.[].title'. Do not invent a filename in that case.
- With no Input section at all, assume nothing about file contents and write the command the request asks for.

MUTATION RULES:
- When the request edits a file in place, prefer a form that cannot destroy the original if it goes wrong: sed -i.bak on BSD, sed -i.bak on GNU too when a backup is cheap, or write to a temporary file and move it into place.
- Never redirect a command's output into the same file it is reading; the shell truncates it first. Write to a temporary file and mv it over the original.

SAFETY RULES:
- Choose the least destructive command that satisfies the request. Prefer a dry run or a listing when the request is ambiguous about deletion.
- Never add rm, kill, chmod, chown, force-push, or history rewriting unless the user explicitly asked for that effect.
- Do not chain unrequested extra steps onto the command.

IF IMPOSSIBLE:
- If the request cannot be satisfied by a shell command, output a single line starting with "# " that says why, and nothing else.`

// buildUserMessage assembles the single user turn sent to the model: the
// environment, the request, and (optionally) a description of piped input.
func buildUserMessage(query string, s Sample, files []FileInput) string {
	var b strings.Builder
	b.WriteString("Environment:\n")
	fmt.Fprintf(&b, "- OS: %s\n", describeOS())
	fmt.Fprintf(&b, "- Arch: %s\n", runtime.GOARCH)
	fmt.Fprintf(&b, "- Shell: %s\n", describeShell())
	if wd, err := os.Getwd(); err == nil {
		fmt.Fprintf(&b, "- Working directory: %s\n", wd)
	}
	b.WriteString("\nRequest:\n")
	b.WriteString(query)
	b.WriteString("\n")

	for _, f := range files {
		b.WriteString("\nInput:\n")
		fmt.Fprintf(&b, "File: %s\n", f.Path)
		if f.Sample.Empty() {
			b.WriteString("(contents unavailable; use the path as given)\n")
			continue
		}
		b.WriteString(describeSample(f.Sample))
	}

	if !s.Empty() {
		b.WriteString("\nInput:\n")
		b.WriteString("Piped input on stdin (no path available).\n")
		b.WriteString(describeSample(s))
	}
	return b.String()
}

// describeSample renders a sample as either the data itself or a description
// of its shape, and says which it is.
func describeSample(s Sample) string {
	var b strings.Builder
	if s.Verbatim {
		b.WriteString("Complete contents:\n<<<INPUT\n")
		b.WriteString(s.Summary)
		if !strings.HasSuffix(s.Summary, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("INPUT\n")
	} else {
		// Only a bounded prefix of a large input was ever read, so what follows
		// is a description rather than the data itself.
		b.WriteString(s.Summary)
		if !strings.HasSuffix(s.Summary, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("The command must handle the entire input, not only the part described above.\n")
	}
	return b.String()
}

// describeOS gives the model a friendlier OS name than runtime.GOOS alone.
func describeOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS (darwin, BSD userland)"
	case "linux":
		return "Linux (GNU userland)"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

// describeShell reports the user's shell, falling back to a sane default.
func describeShell() string {
	if runtime.GOOS == "windows" {
		if os.Getenv("PSModulePath") != "" {
			return "powershell"
		}
		return "cmd.exe"
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}
