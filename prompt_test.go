package main

import (
	"os"
	"strings"
	"testing"
)

func TestBuildUserMessageWithoutInput(t *testing.T) {
	msg := buildUserMessage("list big files", Sample{})
	if !strings.Contains(msg, "list big files") {
		t.Error("the request must appear in the message")
	}
	if !strings.Contains(msg, "Environment:") {
		t.Error("the environment block must be present")
	}
	if strings.Contains(msg, "Input:") {
		t.Error("no input section should be added when nothing was piped")
	}
}

func TestBuildUserMessageVerbatimInput(t *testing.T) {
	s := Sample{Format: "json", Summary: `{"users":[{"id":1}]}`, Verbatim: true}
	msg := buildUserMessage("extract user ids", s)
	if !strings.Contains(msg, "<<<INPUT") || !strings.Contains(msg, `{"users":[{"id":1}]}`) {
		t.Error("the complete sample must be delimited and included")
	}
	if strings.Contains(msg, "not only the part described above") {
		t.Error("no truncation warning belongs on a complete sample")
	}
}

func TestBuildUserMessageSummarizedInput(t *testing.T) {
	s := Sample{Format: "json", Summary: "Root: array (at least 500 elements)", Truncated: true}
	msg := buildUserMessage("count records", s)
	if !strings.Contains(msg, "Root: array") {
		t.Error("the structure summary must be included")
	}
	if strings.Contains(msg, "<<<INPUT") {
		t.Error("a summary is not raw data and must not be fenced as such")
	}
	if !strings.Contains(msg, "must handle the entire input") {
		t.Error("the model must be told the summary covers a larger input")
	}
}

func TestReadPipedInputFromFile(t *testing.T) {
	f := tempFileWith(t, `{"a":1}`)
	s := readPipedInput(f, 256*1024, 4000)
	if s.Empty() {
		t.Fatal("expected a sample")
	}
	if !s.Verbatim || s.Summary != `{"a":1}` {
		t.Errorf("small input should be sent verbatim, got %+v", s)
	}
}

func TestReadPipedInputLargeIsSummarized(t *testing.T) {
	f := tempFileWith(t, "["+strings.Repeat(`{"id":1,"title":"x"},`, 500)+`{"id":2,"title":"y"}]`)
	s := readPipedInput(f, 256*1024, 200)
	if s.Verbatim {
		t.Error("input larger than the send cap must be summarized, not sent raw")
	}
	if len(s.Summary) > 220 {
		t.Errorf("summary of %d chars exceeds the send cap", len(s.Summary))
	}
}

func tempFileWith(t *testing.T, content string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "piped")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSystemPromptRules(t *testing.T) {
	// The downstream pipeline assumes a bare command, and the file-vs-stdin
	// rule is the whole reason a named path behaves differently.
	for _, want := range []string{
		"Output ONLY the command",
		"markdown code fences",
		"names a file or path",
		"MUST read from stdin",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("system prompt is missing the %q rule", want)
		}
	}
}

func TestReadsStdin(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"jq -r '.[].title'", true},
		{"wc -l", true},
		{"awk '{print $1}'", true},
		{"jq -r '.[].title' todo.json", false},
		{"grep ERROR app.log", false},
		{"wc -l < data.csv", false},
		{"cat /var/log/system.log", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := readsStdin(tt.cmd); got != tt.want {
				t.Errorf("readsStdin(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestDescribeShellFallback(t *testing.T) {
	t.Setenv("SHELL", "")
	if got := describeShell(); got == "" {
		t.Error("describeShell() must never return an empty string")
	}
}
