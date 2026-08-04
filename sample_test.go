package main

import (
	"io"
	"strings"
	"testing"
)

// countingReader reports how many bytes were actually pulled from the source.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// hugeReader emits a repeating pattern forever without allocating it, standing
// in for a very large file.
type hugeReader struct {
	pattern []byte
	off     int
	left    int64
}

func (h *hugeReader) Read(p []byte) (int, error) {
	if h.left <= 0 {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && h.left > 0 {
		p[n] = h.pattern[h.off%len(h.pattern)]
		h.off++
		h.left--
		n++
	}
	return n, nil
}

func TestBuildSampleNeverReadsBeyondCap(t *testing.T) {
	// A 50 MB input must cost one bounded read, not a slurp.
	const fiftyMB = 50 * 1024 * 1024
	const readCap = 64 * 1024

	src := &countingReader{r: &hugeReader{pattern: []byte(`{"id":1,"title":"abc"},`), left: fiftyMB}}
	s := BuildSample(src, readCap, 2000)

	if src.n > int64(readCap)+1 {
		t.Fatalf("read %d bytes from a %d byte source, cap is %d", src.n, fiftyMB, readCap)
	}
	if !s.Truncated {
		t.Error("Truncated should be set for an input larger than the cap")
	}
	if s.Verbatim {
		t.Error("a truncated input must never be marked verbatim")
	}
	if len(s.Summary) > 2000+4 {
		t.Errorf("summary is %d chars, exceeds the send cap", len(s.Summary))
	}
}

func TestBuildSampleSummaryStaysWithinSendCap(t *testing.T) {
	big := "[" + strings.Repeat(`{"userId":1,"id":1,"title":"delectus aut autem","completed":false},`, 5000)
	for _, sendCap := range []int{200, 500, 4000} {
		s := BuildSample(strings.NewReader(big), 256*1024, sendCap)
		if len([]rune(s.Summary)) > sendCap+1 {
			t.Errorf("sendCap %d: summary is %d runes", sendCap, len([]rune(s.Summary)))
		}
	}
}

func TestBuildSampleEmpty(t *testing.T) {
	s := BuildSample(strings.NewReader(""), 1024, 100)
	if !s.Empty() {
		t.Errorf("expected an empty sample, got %+v", s)
	}
}

func TestBuildSampleSmallInputIsVerbatim(t *testing.T) {
	in := `[{"id":1,"title":"walk the dog"}]`
	s := BuildSample(strings.NewReader(in), 1024, 4000)
	if !s.Verbatim {
		t.Fatal("a small input should be sent as-is")
	}
	if s.Summary != in {
		t.Errorf("Summary = %q, want the exact input", s.Summary)
	}
	if s.Truncated {
		t.Error("Truncated should be false")
	}
}

func TestSummarizeJSONReportsPathsAndTypes(t *testing.T) {
	// Shaped like the user's todo.json, but too big to send verbatim.
	in := "[" + strings.Repeat(`{"userId":1,"id":1,"title":"delectus aut autem","completed":false},`, 400)
	in = strings.TrimSuffix(in, ",") + "]"

	s := BuildSample(strings.NewReader(in), 256*1024, 4000)
	if s.Verbatim {
		t.Fatal("expected a summary, not verbatim")
	}
	for _, want := range []string{".[].userId", ".[].id", ".[].title", ".[].completed"} {
		if !strings.Contains(s.Summary, want) {
			t.Errorf("summary missing path %q:\n%s", want, s.Summary)
		}
	}
	for _, want := range []string{"number", "string", "boolean"} {
		if !strings.Contains(s.Summary, want) {
			t.Errorf("summary missing type %q", want)
		}
	}
	if !strings.Contains(s.Summary, "delectus aut autem") {
		t.Error("summary should carry an example value")
	}
	if !strings.Contains(s.Summary, "Root: array") {
		t.Error("summary should describe the root as an array")
	}
}

func TestSummarizeJSONSurvivesTruncation(t *testing.T) {
	// Cut mid-record: the walker must keep whatever schema it learned.
	full := "[" + strings.Repeat(`{"a":1,"b":"two","c":true},`, 200)
	s := BuildSample(strings.NewReader(full), 4096, 4000)

	if !s.Truncated {
		t.Fatal("expected Truncated")
	}
	for _, want := range []string{".[].a", ".[].b", ".[].c"} {
		if !strings.Contains(s.Summary, want) {
			t.Errorf("summary missing %q from a truncated document:\n%s", want, s.Summary)
		}
	}
	if !strings.Contains(s.Summary, "at least") {
		t.Error("a truncated array should be reported as a lower bound")
	}
}

func TestSummarizeJSONNestedPaths(t *testing.T) {
	in := `{"meta":{"page":1},"items":[{"name":"a","tags":["x"]}]}`
	in = in + strings.Repeat(" ", 0)
	s := BuildSample(strings.NewReader(in), 256*1024, 20) // force summarizing
	for _, want := range []string{".meta.page", ".items[].name"} {
		if !strings.Contains(summarizeJSON([]byte(in), false), want) {
			t.Errorf("summary missing nested path %q", want)
		}
	}
	_ = s
}

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"json array", `[{"a":1}]`, "json"},
		{"json object", `{"a":1}`, "json"},
		{"ndjson", "{\"a\":1}\n{\"a\":2}\n{\"a\":3}\n", "ndjson"},
		{"csv", "name,age\nada,36\n", "csv"},
		{"tsv", "name\tage\nada\t36\n", "tsv"},
		{"plain text", "hello world\nsecond line\n", "text"},
		{"log lines", "2026-01-01 ERROR boom\n2026-01-01 INFO ok\n", "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectFormat([]byte(tt.in)); got != tt.want {
				t.Errorf("detectFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeDelimitedGivesColumnPositions(t *testing.T) {
	in := "name,age,city\n" + strings.Repeat("ada,36,london\n", 2000)
	s := BuildSample(strings.NewReader(in), 8192, 2000)

	if s.Format != "csv" {
		t.Fatalf("Format = %q, want csv", s.Format)
	}
	for _, want := range []string{"$1", "$2", "$3", "name", "age", "city"} {
		if !strings.Contains(s.Summary, want) {
			t.Errorf("summary missing %q:\n%s", want, s.Summary)
		}
	}
	if !strings.Contains(s.Summary, "3 columns") {
		t.Error("summary should state the column count")
	}
}

func TestSummarizeNDJSON(t *testing.T) {
	in := strings.Repeat(`{"level":"info","msg":"started"}`+"\n", 3000)
	s := BuildSample(strings.NewReader(in), 8192, 2000)

	if s.Format != "ndjson" {
		t.Fatalf("Format = %q, want ndjson", s.Format)
	}
	if !strings.Contains(s.Summary, "newline-delimited JSON") {
		t.Error("summary should identify the format")
	}
	if !strings.Contains(s.Summary, ".level") {
		t.Errorf("summary should describe a record:\n%s", s.Summary)
	}
}

func TestSummarizeTextFallback(t *testing.T) {
	in := strings.Repeat("2026-01-01 12:00:00 ERROR something failed\n", 5000)
	s := BuildSample(strings.NewReader(in), 8192, 1000)

	if s.Format != "text" {
		t.Fatalf("Format = %q, want text", s.Format)
	}
	if !strings.Contains(s.Summary, "plain text") {
		t.Error("summary should identify the format")
	}
}

func TestBuildSampleHandlesBinaryWithoutPanicking(t *testing.T) {
	in := string([]byte{0x00, 0x01, 0xff, 0xfe, 0x42, 0x00})
	s := BuildSample(strings.NewReader(in), 1024, 4000)
	if s.Empty() {
		t.Error("binary input should still produce a sample")
	}
	if s.Verbatim {
		t.Error("invalid UTF-8 must not be sent verbatim")
	}
}

func TestClipStaysValidUTF8(t *testing.T) {
	s := clip(strings.Repeat("é", 50), 11)
	if !strings.HasSuffix(s, "…") {
		t.Error("clip should mark truncation")
	}
	for _, r := range s {
		if r == '\uFFFD' {
			t.Fatal("clip produced an invalid rune")
		}
	}
}
