package main

import (
	"reflect"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain command", "ls -la", "ls -la"},
		{"trailing newline", "ls -la\n", "ls -la"},
		{"surrounding whitespace", "  \n ls -la \n\n", "ls -la"},
		{"bash fence", "```bash\nls -la\n```", "ls -la"},
		{"bare fence", "```\nls -la\n```", "ls -la"},
		{"sh fence", "```sh\nfind . -name '*.go'\n```", "find . -name '*.go'"},
		{"unterminated fence", "```bash\nls -la", "ls -la"},
		{"shell prompt prefix", "$ ls -la", "ls -la"},
		{"angle prompt prefix", "> ls -la", "ls -la"},
		{"inline backticks", "`ls -la`", "ls -la"},
		{
			"multiline command preserved",
			"for f in *.txt; do\n  echo \"$f\"\ndone",
			"for f in *.txt; do\n  echo \"$f\"\ndone",
		},
		{
			"multiline inside fence",
			"```bash\nfor f in *.txt; do\n  echo \"$f\"\ndone\n```",
			"for f in *.txt; do\n  echo \"$f\"\ndone",
		},
		{"empty", "", ""},
		{"only a fence", "```\n```", ""},
		{"windows line endings", "ls -la\r\n", "ls -la"},
		{
			"pipeline with redirect is untouched",
			"grep -c ERROR app.log 2>/dev/null",
			"grep -c ERROR app.log 2>/dev/null",
		},
		{"refusal line kept intact", "# that needs a GUI, not a shell command", "# that needs a GUI, not a shell command"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.in); got != tt.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsRefusal(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"# cannot be done in a shell", true},
		{"", true},
		{"   ", true},
		{"ls -la", false},
		{"#!/bin/bash", false}, // shebang is a real command, not a refusal
		{"grep '#' file.txt", false},
	}
	for _, tt := range tests {
		if got := IsRefusal(tt.in); got != tt.want {
			t.Errorf("IsRefusal(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "ls -la", nil},
		{"one", "git checkout <branch-name>", []string{"<branch-name>"}},
		{
			"several",
			"scp <local-file> <user>@host:/tmp",
			[]string{"<local-file>", "<user>"},
		},
		{"deduplicated", "cp <file> <file>.bak", []string{"<file>"}},
		{"redirect is not a placeholder", "cmd 2>&1", nil},
		{"heredoc is not a placeholder", "cat <<EOF\nhi\nEOF", nil},
		{"less-than comparison ignored", "awk '$1 < 5'", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Placeholders(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Placeholders(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestPreviewLine(t *testing.T) {
	if got := previewLine("line one\nline two"); got != "line two" {
		t.Errorf("previewLine = %q, want last line", got)
	}
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	if got := previewLine(long); len([]rune(got)) > 91 {
		t.Errorf("previewLine did not truncate: len = %d", len([]rune(got)))
	}
}
