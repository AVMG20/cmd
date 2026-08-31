package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FileInput is a file named by the request, together with a description of
// what is inside it.
//
// This exists because of the central awkwardness of piping: `cat users.csv |
// cmd "redact the email column"` gives the model the data but not the name, so
// the best it can do is write a command that reads stdin -- useless for an
// in-place mutation, and not even replayable, because the pipe was consumed to
// build the prompt. Naming the file instead lets the command target the real
// path.
type FileInput struct {
	// Path is written into the prompt exactly as the user typed it, so the
	// generated command is one they can paste anywhere.
	Path string
	// Abs is the resolved path that was actually opened.
	Abs    string
	Sample Sample
}

// CollectFiles resolves the files a request refers to and samples each one.
//
// Paths from -f are always used. Paths found in the query text are used only
// when they resolve to a regular file that exists, so ordinary prose cannot
// trigger a read: "list all titles in todo.json" opens todo.json, while "sort
// by created.at descending" opens nothing.
func CollectFiles(query string, explicit []string, cfg Config) []FileInput {
	var out []FileInput
	seen := map[string]bool{}

	add := func(raw string, required bool) {
		if len(out) >= cfg.MaxAutoFiles {
			return
		}
		path, abs, ok := resolveFile(raw)
		if !ok {
			// -f named something unreadable. Keep it as a bare path rather than
			// dropping it, so the command still targets the right name.
			if required && !seen[raw] {
				seen[raw] = true
				out = append(out, FileInput{Path: raw})
			}
			return
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, FileInput{Path: path, Abs: abs, Sample: sampleFile(abs, cfg)})
	}

	for _, p := range explicit {
		add(p, true)
	}
	if cfg.ReadsFiles() {
		for _, cand := range PathCandidates(query) {
			add(cand, false)
		}
	}
	return out
}

// sampleFile describes a file with the same bounded reader used for stdin, so
// a 50 MB file still costs one capped read.
func sampleFile(abs string, cfg Config) Sample {
	f, err := os.Open(abs)
	if err != nil {
		return Sample{}
	}
	defer f.Close()
	return BuildSample(f, cfg.SampleReadBytes, cfg.MaxPipeChars)
}

// Characters that routinely sit against a path in a sentence but are not part
// of it. The two ends are trimmed with different sets on purpose: a trailing
// dot is sentence punctuation ("titles in todo.json."), but a leading dot is
// the path itself, and stripping it would turn ./var/log/app.log into an
// absolute path pointing somewhere else entirely.
const (
	trimLeading  = "\"'`([{<"
	trimTrailing = "\"'`,;:!?.)]}>"
)

// PathCandidates extracts the words of a request that could name a file.
//
// It is deliberately permissive -- os.Stat is the real filter -- but a word
// still has to look like a path (contain a separator, start with ~, or carry a
// short extension) to be considered, so "list the titles" does not stat "list",
// "the" and "titles".
func PathCandidates(query string) []string {
	var out []string
	seen := map[string]bool{}
	for _, field := range strings.Fields(query) {
		for _, tok := range tokenVariants(field) {
			if tok == "" || seen[tok] || !looksLikePath(tok) {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}

// tokenVariants yields the forms of a word worth testing: as written, and with
// surrounding punctuation stripped. Both are tried because a filename can end
// in a character that also reads as punctuation.
func tokenVariants(field string) []string {
	variants := []string{field}
	t := strings.TrimRight(strings.TrimLeft(field, trimLeading), trimTrailing)
	if t != field && t != "" {
		variants = append(variants, t)
	}
	return variants
}

// resolveFile reports whether raw names an existing regular file, returning the
// path to show the model and the absolute path to read.
func resolveFile(raw string) (display, abs string, ok bool) {
	for _, cand := range tokenVariants(raw) {
		expanded := expandHome(cand)
		info, err := os.Stat(expanded)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		a, err := filepath.Abs(expanded)
		if err != nil {
			a = expanded
		}
		return cand, a, true
	}
	return "", "", false
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// StdinPath recovers the filename behind a redirect.
//
// `cmd "..." < users.csv` hands us a file descriptor with no name attached, but
// on Linux the kernel exposes the path through /proc/self/fd/0. When that
// works, a redirect becomes as good as naming the file. A pipe
// (`cat users.csv | cmd`) has no path, so this reports false there.
func StdinPath(stdin *os.File) (string, bool) {
	if runtime.GOOS != "linux" {
		return "", false
	}
	info, err := stdin.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	target, err := os.Readlink("/proc/self/fd/0")
	if err != nil || !filepath.IsAbs(target) {
		return "", false
	}
	// A deleted or anonymous file resolves to a decorated string rather than a
	// usable path, and pipes resolve to pipe:[12345].
	if strings.HasSuffix(target, " (deleted)") || strings.Contains(target, ":[") {
		return "", false
	}
	if st, err := os.Stat(target); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return relativeIfUnder(target), true
}

// relativeIfUnder shortens a path that lives under the working directory, so
// the generated command reads the way the user would have written it.
func relativeIfUnder(abs string) string {
	wd, err := os.Getwd()
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(wd, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return rel
}
