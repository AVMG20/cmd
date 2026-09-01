package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Completion sources for the interactive editor: "@" finds files, "/" finds
// commands. Both answer the same question -- given what has been typed so far,
// what could it become -- so they share one shape.

// maxCompletions bounds what is shown at once. A list longer than this is
// faster to narrow by typing than to read.
const maxCompletions = 8

// fileWalkLimit bounds a directory scan so completing "@" in a huge tree stays
// instant. Typing one more character narrows the search far more effectively
// than raising this would.
const fileWalkLimit = 4000

// Completion is one candidate.
type Completion struct {
	// Text replaces the token being completed.
	Text string
	// Hint is dim trailing detail, such as "dir" or a command's purpose.
	Hint string
}

// CompleteFiles returns paths under the working directory matching prefix.
//
// Matching is case-insensitive and looks at the whole relative path, so
// "@user" finds src/models/users.go as readily as ./users.csv. Directories are
// offered too, because narrowing by directory first is often the quickest way
// to reach a file.
func CompleteFiles(root, prefix string) []Completion {
	// A prefix naming a directory ("src/") lists that directory rather than
	// searching for it.
	searchRoot, needle := splitFilePrefix(root, prefix)

	type scored struct {
		Completion
		rank  int
		depth int
	}
	var dirs, files []scored
	seen := 0
	lower := strings.ToLower(needle)

	_ = filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable directory is skipped, not fatal
		}
		if seen++; seen > fileWalkLimit {
			return filepath.SkipAll
		}
		if path == searchRoot {
			return nil
		}
		name := d.Name()
		// Hidden files and the usual heavy directories are noise here. They
		// stay reachable by typing the prefix explicitly.
		if strings.HasPrefix(name, ".") || isVendorDir(name) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rank, matched := matchRank(rel, name, lower)
		if !matched {
			return nil
		}
		entry := scored{rank: rank, depth: strings.Count(rel, "/")}
		if d.IsDir() {
			entry.Completion = Completion{Text: rel + "/", Hint: "dir"}
			dirs = append(dirs, entry)
		} else {
			entry.Completion = Completion{Text: rel}
			files = append(files, entry)
		}
		return nil
	})

	// Rank first, then depth: a name that starts with what was typed is what
	// was meant, and a shallower path beats a nested namesake. Without this a
	// one-letter prefix returns whatever the walk happened to reach first.
	byRank := func(c []scored) {
		sort.SliceStable(c, func(i, j int) bool {
			if c[i].rank != c[j].rank {
				return c[i].rank < c[j].rank
			}
			if c[i].depth != c[j].depth {
				return c[i].depth < c[j].depth
			}
			return c[i].Text < c[j].Text
		})
	}
	byRank(files)
	byRank(dirs)

	out := make([]Completion, 0, len(files)+len(dirs))
	for _, c := range append(files, dirs...) {
		out = append(out, c.Completion)
	}
	if len(out) > maxCompletions {
		out = out[:maxCompletions]
	}
	return out
}

// Match ranks, best first. A filename that begins with what was typed is the
// strongest signal; a substring anywhere in the path is the weakest but still
// worth offering, so "@user" can find src/models/users.go.
const (
	rankNamePrefix = iota
	rankPathPrefix
	rankNameSubstring
	rankPathSubstring
)

// matchRank scores how well an entry matches, and reports whether it matches
// at all. An empty needle matches everything equally.
func matchRank(rel, name, needle string) (int, bool) {
	if needle == "" {
		return rankNamePrefix, true
	}
	lowerName, lowerRel := strings.ToLower(name), strings.ToLower(rel)
	switch {
	case strings.HasPrefix(lowerName, needle):
		return rankNamePrefix, true
	case strings.HasPrefix(lowerRel, needle):
		return rankPathPrefix, true
	case strings.Contains(lowerName, needle):
		return rankNameSubstring, true
	case strings.Contains(lowerRel, needle):
		return rankPathSubstring, true
	}
	return 0, false
}

// splitFilePrefix decides where to search and what to search for. A prefix
// ending in a separator is a directory to list; otherwise the last segment is
// the thing being matched.
func splitFilePrefix(root, prefix string) (searchRoot, needle string) {
	if prefix == "" {
		return root, ""
	}
	if strings.HasSuffix(prefix, "/") {
		candidate := filepath.Join(root, prefix)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, ""
		}
	}
	if dir := filepath.Dir(prefix); dir != "." && dir != "/" {
		candidate := filepath.Join(root, dir)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, filepath.Base(prefix)
		}
	}
	return root, prefix
}

func isVendorDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "target", "dist", "build", "__pycache__":
		return true
	}
	return false
}

// slashCommand is one thing typeable at the "/" palette. Every command is a
// bare word: anything needing an argument belongs in /config, where the wizard
// can show what the choices are.
type slashCommand struct {
	name    string
	summary string
}

// slashCommands is the palette. Keeping it a plain list means /help and the
// completion popup can never disagree about what exists.
var slashCommands = []slashCommand{
	{name: "/config", summary: "Change backend, model or API key"},
	{name: "/think", summary: "Toggle reasoning before answering"},
	{name: "/copy", summary: "Toggle copying instead of running"},
	{name: "/help", summary: "Show what this understands"},
	{name: "/exit", summary: "Leave"},
}

// CompleteSlash returns the palette entries matching prefix, which includes
// the leading slash.
func CompleteSlash(prefix string) []Completion {
	var out []Completion
	for _, c := range slashCommands {
		if !strings.HasPrefix(c.name, prefix) {
			continue
		}
		out = append(out, Completion{Text: c.name, Hint: c.summary})
	}
	return out
}

// activeToken finds the word the cursor sits in when it starts with marker,
// returning the token's start offset and the text between the marker and the
// cursor. A slash only counts at the very start of the line, so a path inside
// a request cannot open the command palette.
func activeToken(line string, cursor int, marker byte) (start int, prefix string, ok bool) {
	if cursor > len(line) {
		cursor = len(line)
	}
	start = strings.LastIndexByte(line[:cursor], marker)
	if start < 0 {
		return 0, "", false
	}
	if marker == '/' && start != 0 {
		return 0, "", false
	}
	// A marker only opens a token at the start of a word.
	if start > 0 && !isTokenBreak(line[start-1]) {
		return 0, "", false
	}
	prefix = line[start+1 : cursor]
	// Whitespace closes the token; the one exception is a slash command, whose
	// argument is still part of the same completion context.
	if marker != '/' && strings.ContainsAny(prefix, " \t") {
		return 0, "", false
	}
	return start, prefix, true
}

func isTokenBreak(c byte) bool { return c == ' ' || c == '\t' }
