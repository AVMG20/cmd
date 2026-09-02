package main

import (
	"regexp"
	"strings"
)

var (
	fenceLine     = regexp.MustCompile("^\\s*```[a-zA-Z0-9_+-]*\\s*$")
	leadingPrompt = regexp.MustCompile(`^\s*(\$|>|#\s*\$)\s+`)
	// A placeholder may contain spaces ("<your file>") but never ends in one,
	// which is what separates it from a redirect pair such as "<in.txt >out".
	placeholderExpr = regexp.MustCompile(`<[a-zA-Z](?:[a-zA-Z0-9 _.\-/]{0,39}[a-zA-Z0-9_.\-/])?>`)
)

// Sanitize turns raw model output into something safe to hand to a shell.
//
// Models still emit markdown fences and shell prompts occasionally despite
// instructions not to, so this is a belt-and-braces cleanup rather than a
// nicety.
func Sanitize(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(s, "\n")

	// When the model fenced its answer, the fence is the boundary: whatever it
	// wrote around it is commentary, and a line of prose after the command
	// would otherwise run as a second command.
	lines = insideFence(lines)

	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if fenceLine.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}

	// Drop blank lines at the edges, keep interior ones (multi-line commands).
	for len(kept) > 0 && strings.TrimSpace(kept[0]) == "" {
		kept = kept[1:]
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) == 0 {
		return ""
	}

	// A leading "$ " or "> " is a copied shell prompt, not part of the command.
	kept[0] = leadingPrompt.ReplaceAllString(kept[0], "")

	// Strip a stray unmatched backtick pair wrapping the whole single line.
	if len(kept) == 1 {
		t := strings.TrimSpace(kept[0])
		if len(t) > 1 && strings.HasPrefix(t, "`") && strings.HasSuffix(t, "`") {
			t = strings.TrimSuffix(strings.TrimPrefix(t, "`"), "`")
		}
		kept[0] = t
	}

	return strings.TrimRight(strings.Join(kept, "\n"), " \t\n")
}

// insideFence returns the lines of the first fenced block, or all the lines
// when there is no fence. An unterminated fence runs to the end.
func insideFence(lines []string) []string {
	open := -1
	for i, line := range lines {
		if !fenceLine.MatchString(line) {
			continue
		}
		if open < 0 {
			open = i
			continue
		}
		return lines[open+1 : i]
	}
	if open >= 0 {
		return lines[open+1:]
	}
	return lines
}

// IsRefusal reports whether the model answered with an explanation instead of a
// command. The system prompt asks it to prefix such answers with "# ".
func IsRefusal(cmd string) bool {
	t := strings.TrimSpace(cmd)
	return t == "" || strings.HasPrefix(t, "# ")
}

// Placeholders returns any <angle-bracket> placeholders left in the command.
// Running one of these verbatim would not do what the user wants, so the caller
// warns before offering to execute.
func Placeholders(cmd string) []string {
	found := placeholderExpr.FindAllString(cmd, -1)
	if len(found) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(found))
	for _, f := range found {
		// Ignore shell redirections such as `2>&1` or heredoc-ish tokens.
		if strings.Contains(f, "&") {
			continue
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
