package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// completionTree builds a small directory to complete against.
func completionTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{
		"users.csv",
		"todo.json",
		"src/models/users.go",
		"src/handler.go",
		".hidden/secret.txt",
		"node_modules/pkg/index.js",
	} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func texts(cs []Completion) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Text)
	}
	return out
}

func TestCompleteFilesRanksNamePrefixFirst(t *testing.T) {
	// "u" matches users.csv by name and src/models/users.go by name too, but
	// also anything whose path merely contains a u. The direct hits must lead.
	got := texts(CompleteFiles(completionTree(t), "u"))
	if len(got) == 0 {
		t.Fatal("expected matches")
	}
	if got[0] != "users.csv" {
		t.Errorf("first = %q, want users.csv; got %v", got[0], got)
	}
}

func TestCompleteFilesMatchesInsideAPath(t *testing.T) {
	// A file only reachable through a directory should still be findable by
	// name alone, which is the point of not requiring the full path.
	got := texts(CompleteFiles(completionTree(t), "handler"))
	if len(got) != 1 || got[0] != "src/handler.go" {
		t.Errorf("got %v, want [src/handler.go]", got)
	}
}

func TestCompleteFilesSkipsHiddenAndVendorTrees(t *testing.T) {
	got := texts(CompleteFiles(completionTree(t), ""))
	for _, g := range got {
		if strings.HasPrefix(g, ".") || strings.Contains(g, "node_modules") {
			t.Errorf("%q should not be offered", g)
		}
	}
}

func TestCompleteFilesListsADirectory(t *testing.T) {
	// A prefix ending in a separator means "show me what is in here".
	got := texts(CompleteFiles(completionTree(t), "src/"))
	if len(got) == 0 {
		t.Fatal("expected the directory's contents")
	}
	for _, g := range got {
		if !strings.HasPrefix(g, "src/") {
			t.Errorf("%q is not inside src/", g)
		}
	}
}

func TestCompleteFilesMarksDirectories(t *testing.T) {
	var dir *Completion
	for _, c := range CompleteFiles(completionTree(t), "src") {
		if c.Text == "src/" {
			c := c
			dir = &c
		}
	}
	if dir == nil {
		t.Fatal("the directory should be offered")
	}
	if dir.Hint != "dir" {
		t.Errorf("hint = %q, want it labelled as a directory", dir.Hint)
	}
}

func TestCompleteFilesIsCaseInsensitive(t *testing.T) {
	if got := texts(CompleteFiles(completionTree(t), "USERS")); len(got) == 0 {
		t.Error("completion should not depend on matching case")
	}
}

func TestCompleteFilesBoundsTheList(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50; i++ {
		if err := os.WriteFile(filepath.Join(dir, string(rune('a'+i%26))+"file.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := CompleteFiles(dir, ""); len(got) > maxCompletions {
		t.Errorf("got %d entries, want at most %d", len(got), maxCompletions)
	}
}

func TestMatchRank(t *testing.T) {
	tests := []struct {
		rel, name, needle string
		want              int
		match             bool
	}{
		{"users.csv", "users.csv", "us", rankNamePrefix, true},
		{"src/users.go", "users.go", "src", rankPathPrefix, true},
		{"src/myusers.go", "myusers.go", "users", rankNameSubstring, true},
		{"src/models/x.go", "x.go", "models", rankPathSubstring, true},
		{"users.csv", "users.csv", "zzz", 0, false},
		{"users.csv", "users.csv", "", rankNamePrefix, true},
	}
	for _, tt := range tests {
		got, ok := matchRank(tt.rel, tt.name, tt.needle)
		if ok != tt.match || (ok && got != tt.want) {
			t.Errorf("matchRank(%q, %q, %q) = %d, %v; want %d, %v",
				tt.rel, tt.name, tt.needle, got, ok, tt.want, tt.match)
		}
	}
}

func TestCompleteFilesFindsHiddenEntriesByName(t *testing.T) {
	// Hidden entries are skipped from a bare "@" but must be reachable when
	// the prefix asks for them.
	got := texts(CompleteFiles(completionTree(t), ".hid"))
	if len(got) != 1 || got[0] != ".hidden/" {
		t.Errorf("got %v, want [.hidden/]", got)
	}
}

func TestCompleteFilesPrefersShallowEntriesUnderTheLimit(t *testing.T) {
	// Breadth first: a top-level file that sorts after a large subtree must
	// still be offered once the walk limit trims the tree.
	dir := t.TempDir()
	for i := 0; i < fileWalkLimit+10; i++ {
		p := filepath.Join(dir, "a", "deep", "f"+strconv.Itoa(i)+".txt")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "zzz.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := texts(CompleteFiles(dir, "zzz"))
	if len(got) != 1 || got[0] != "zzz.txt" {
		t.Errorf("got %v, want [zzz.txt]", got)
	}
}
