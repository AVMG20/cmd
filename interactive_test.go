package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParseRefsStripsTheMarker(t *testing.T) {
	// The model should see a path, not the input syntax that produced it.
	query, files := parseRefs("count rows in @users.csv please")
	if query != "count rows in users.csv please" {
		t.Errorf("query = %q, want the marker gone", query)
	}
	if !reflect.DeepEqual(files, []string{"users.csv"}) {
		t.Errorf("files = %v, want [users.csv]", files)
	}
}

func TestParseRefsHandlesSeveralFiles(t *testing.T) {
	query, files := parseRefs("diff @old/a.json against @new/b.json")
	if strings.Contains(query, "@") {
		t.Errorf("query = %q, want no markers left", query)
	}
	if !reflect.DeepEqual(files, []string{"old/a.json", "new/b.json"}) {
		t.Errorf("files = %v", files)
	}
}

func TestParseRefsWithoutReferences(t *testing.T) {
	query, files := parseRefs("what is using port 3000")
	if query != "what is using port 3000" {
		t.Errorf("query = %q, want it untouched", query)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none", files)
	}
}

func TestParseRefsDropsATrailingSlash(t *testing.T) {
	// Completing a directory leaves the separator on; as a reference it is the
	// directory itself that is meant.
	_, files := parseRefs("clean up @build/")
	if !reflect.DeepEqual(files, []string{"build"}) {
		t.Errorf("files = %v, want [build]", files)
	}
}

func TestActiveTokenFindsFileReference(t *testing.T) {
	start, prefix, ok := activeToken("count rows in @use", 18, '@')
	if !ok {
		t.Fatal("the reference under the cursor should be found")
	}
	if start != 14 || prefix != "use" {
		t.Errorf("start=%d prefix=%q, want 14 and \"use\"", start, prefix)
	}
}

func TestActiveTokenClosesOnWhitespace(t *testing.T) {
	// Once a space is typed the reference is finished; completions must stop.
	if _, _, ok := activeToken("read @users.csv and count", 25, '@'); ok {
		t.Error("a completed reference should not still be open")
	}
}

func TestActiveTokenRequiresAWordBoundary(t *testing.T) {
	// An @ inside a word is an email or a host, not a file reference.
	if _, _, ok := activeToken("mail me@example.com", 19, '@'); ok {
		t.Error("an @ mid-word must not open file completion")
	}
}

func TestActiveTokenSlashOnlyAtLineStart(t *testing.T) {
	if _, _, ok := activeToken("/con", 4, '/'); !ok {
		t.Error("a leading slash should open the palette")
	}
	// Otherwise every path in a request would open the command palette.
	if _, _, ok := activeToken("look in src/models", 18, '/'); ok {
		t.Error("a slash inside a request must not open the palette")
	}
}

func TestCompleteSlashFilters(t *testing.T) {
	got := CompleteSlash("/co")
	var names []string
	for _, c := range got {
		names = append(names, strings.TrimSpace(c.Text))
	}
	if !reflect.DeepEqual(names, []string{"/config", "/copy"}) {
		t.Errorf("names = %v, want /config and /copy", names)
	}
	for _, c := range got {
		if c.Hint == "" {
			t.Errorf("%q has no description; the palette is where they are discovered", c.Text)
		}
	}
}

func TestCompleteSlashUnknownPrefix(t *testing.T) {
	if got := CompleteSlash("/zzz"); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

func TestCrlfWriterFixesBareNewlines(t *testing.T) {
	// A raw-mode terminal does no translation, so a bare \n staircases.
	var buf bytes.Buffer
	w := crlfWriter{&buf}
	n, err := w.Write([]byte("one\ntwo\r\nthree\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "one\r\ntwo\r\nthree\r\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
	// The count must be the caller's, not the encoded length, or a correct
	// write looks short.
	if n != len("one\ntwo\r\nthree\n") {
		t.Errorf("n = %d, want the input length", n)
	}
}

func TestVisibleWidthIgnoresColour(t *testing.T) {
	// The cursor is positioned by columns on screen, not bytes written.
	if got := visibleWidth("\033[1;33m› \033[0m"); got != 2 {
		t.Errorf("visibleWidth = %d, want 2", got)
	}
	if got := visibleWidth("$ "); got != 2 {
		t.Errorf("visibleWidth = %d, want 2", got)
	}
}

func TestUTF8SequenceLen(t *testing.T) {
	// The terminal hands over bytes. Treating each as a rune turns every
	// accented letter into mojibake as soon as it is echoed back.
	tests := []struct {
		b    byte
		want int
	}{
		{'a', 0},  // ASCII never reaches this path
		{0xC3, 2}, // é
		{0xE2, 3}, // ›
		{0xF0, 4}, // emoji
		{0xA9, 0}, // a continuation byte cannot start a character
		{0xFF, 0}, // not a valid leader at all
	}
	for _, tt := range tests {
		if got := utf8SequenceLen(tt.b); got != tt.want {
			t.Errorf("utf8SequenceLen(%#x) = %d, want %d", tt.b, got, tt.want)
		}
	}
}

func TestOpenRouterLeavesRoomForReasoning(t *testing.T) {
	// Reasoning tokens count against max_tokens, and the models this backend
	// exists for cannot always switch reasoning off. A cap sized for the
	// command alone would truncate the answer away entirely.
	req := buildOpenRouterRequest(DefaultConfig(), "list files", false)
	if req.MaxTokens < 1000 {
		t.Errorf("max_tokens = %d, too tight to survive a reasoning model", req.MaxTokens)
	}
}
