package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathCandidatesIgnoresProse(t *testing.T) {
	// Ordinary words must not be proposed, or every request would stat a
	// dozen paths that cannot exist.
	got := PathCandidates("list all the titles sorted by date")
	if len(got) != 0 {
		t.Errorf("expected no candidates from plain prose, got %v", got)
	}
}

func TestPathCandidatesFindsPathShapes(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"list all titles in todo.json", "todo.json"},
		{"count rows in data/users.csv", "data/users.csv"},
		{"tail ./var/log/app.log", "./var/log/app.log"},
		{"read ~/notes.md", "~/notes.md"},
		{"dump the schema from db.sqlite3.", "db.sqlite3"},
		{`quote "config.yaml" please`, "config.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := PathCandidates(tt.query)
			for _, c := range got {
				if c == tt.want {
					return
				}
			}
			t.Errorf("PathCandidates(%q) = %v, want it to include %q", tt.query, got, tt.want)
		})
	}
}

func TestCollectFilesReadsOnlyRealFiles(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "todo.json")
	if err := os.WriteFile(real, []byte(`[{"id":1,"title":"first"}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	files := CollectFiles("list titles in "+real+" and ignore missing.json", nil, cfg)

	if len(files) != 1 {
		t.Fatalf("expected exactly the file that exists, got %d: %+v", len(files), files)
	}
	if files[0].Path != real {
		t.Errorf("path = %q, want %q", files[0].Path, real)
	}
	if !strings.Contains(files[0].Sample.Summary, "title") {
		t.Errorf("the sample should describe the real contents, got %q", files[0].Sample.Summary)
	}
}

func TestCollectFilesRespectsAutoReadToggle(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(real, []byte("TOKEN=hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.AutoReadFiles = boolPtr(false)
	if files := CollectFiles("show the token in "+real, nil, cfg); len(files) != 0 {
		t.Errorf("auto_read_files=false must open nothing, got %+v", files)
	}

	// -f is an explicit instruction, so it still works with auto-read off.
	files := CollectFiles("show the token", []string{real}, cfg)
	if len(files) != 1 {
		t.Fatalf("an explicit -f must be honoured, got %+v", files)
	}
}

func TestCollectFilesKeepsUnreadableExplicitPath(t *testing.T) {
	// Naming a file that is not there is still worth passing on: the command
	// should target that name rather than a placeholder.
	files := CollectFiles("compress it", []string{"/definitely/not/here.tar"}, DefaultConfig())
	if len(files) != 1 || files[0].Path != "/definitely/not/here.tar" {
		t.Fatalf("expected the path to survive, got %+v", files)
	}
	if !files[0].Sample.Empty() {
		t.Error("a file that could not be read must not carry a sample")
	}
}

func TestCollectFilesHonoursTheCap(t *testing.T) {
	dir := t.TempDir()
	var names []string
	for _, n := range []string{"a.json", "b.json", "c.json", "d.json"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		names = append(names, p)
	}
	cfg := DefaultConfig()
	cfg.MaxAutoFiles = 2
	if files := CollectFiles(strings.Join(names, " "), nil, cfg); len(files) != 2 {
		t.Errorf("expected the cap to hold at 2, got %d", len(files))
	}
}

func TestCollectFilesDeduplicates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(p, []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The same file named both explicitly and in the query must be read once.
	files := CollectFiles("clean up "+p, []string{p}, DefaultConfig())
	if len(files) != 1 {
		t.Errorf("expected one entry for one file, got %d: %+v", len(files), files)
	}
}

func TestStdinPathRejectsAPipe(t *testing.T) {
	// The filename in `cat users.csv | cmd` lives in the other process's argv,
	// not in the pipe. Resolving one to a name would be a guess.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	if path, ok := StdinPath(r); ok {
		t.Errorf("a pipe must not resolve to a path, got %q", path)
	}
}

func TestStdinPathRecoversARedirect(t *testing.T) {
	// The whole point: `cmd "..." < users.csv` should be as good as naming it.
	dir := t.TempDir()
	p := filepath.Join(dir, "users.csv")
	if err := os.WriteFile(p, []byte("id,email\n1,a@b.c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, ok := StdinPath(f)
	if !ok {
		t.Skipf("this platform cannot recover a descriptor's path")
	}
	if !strings.HasSuffix(got, "users.csv") {
		t.Errorf("StdinPath = %q, want the real file", got)
	}
}

func TestCollectFilesNeverDropsAnExplicitFile(t *testing.T) {
	// The cap guards against a rambling request opening half the tree. A file
	// named with -f was asked for on purpose and must survive it.
	dir := t.TempDir()
	var paths []string
	for _, n := range []string{"a.json", "b.json", "c.json", "d.json", "e.json"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	cfg := DefaultConfig()
	cfg.MaxAutoFiles = 2

	files := CollectFiles("tidy these up", paths, cfg)
	if len(files) != len(paths) {
		t.Errorf("got %d files, want all %d explicit ones kept", len(files), len(paths))
	}
}

func TestCollectFilesStillCapsAutoDetection(t *testing.T) {
	dir := t.TempDir()
	var mentioned []string
	for _, n := range []string{"a.json", "b.json", "c.json", "d.json"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		mentioned = append(mentioned, p)
	}
	cfg := DefaultConfig()
	cfg.MaxAutoFiles = 2
	if files := CollectFiles(strings.Join(mentioned, " "), nil, cfg); len(files) != 2 {
		t.Errorf("got %d files, want the cap to hold at 2", len(files))
	}
}
