package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// Sample is a bounded description of piped input.
//
// The guiding constraint: the input may be a 50 MB file, and neither the whole
// file nor a large fraction of it may be held in memory or sent to the model.
// At most ReadCap bytes are ever read from the source, and only Summary --
// itself capped -- reaches the prompt.
type Sample struct {
	// Format is one of "json", "ndjson", "csv", "tsv", "text", or "" when
	// nothing was piped.
	Format string
	// Summary is the text handed to the model: either the raw input (when it
	// is small enough to send verbatim) or an inferred structure description.
	Summary string
	// Verbatim reports whether Summary is the complete input rather than a
	// summary of it.
	Verbatim bool
	// Truncated reports that the source had more bytes than were read.
	Truncated bool
	// BytesRead is how much was actually pulled from the source.
	BytesRead int
}

// Empty reports whether anything was piped in at all.
func (s Sample) Empty() bool { return s.Format == "" }

const (
	// maxSummaryFields caps how many distinct paths the JSON walker reports.
	maxSummaryFields = 60
	// maxJSONNodes bounds the token walk so a pathological document cannot
	// spin for long even within the byte cap.
	maxJSONNodes = 20000
	// arrayProbeElements is how many elements of an array are inspected for
	// structure; the rest are only counted.
	arrayProbeElements = 3
)

// BuildSample reads at most readCap bytes from r and describes them.
//
// It never reads beyond readCap regardless of how much data r holds, so piping
// a huge file costs a single bounded read.
func BuildSample(r io.Reader, readCap, sendCap int) Sample {
	if readCap <= 0 {
		readCap = 256 * 1024
	}
	if sendCap <= 0 {
		sendCap = 4000
	}

	// Read one extra byte purely to detect that more data exists.
	buf := make([]byte, 0, min(readCap+1, 64*1024))
	w := bytes.NewBuffer(buf)
	n, _ := io.Copy(w, io.LimitReader(r, int64(readCap)+1))
	if n == 0 {
		return Sample{}
	}

	data := w.Bytes()
	truncated := len(data) > readCap
	if truncated {
		data = data[:readCap]
	}

	format := detectFormat(data)

	// Small enough to send as-is: nothing beats showing the model the real
	// thing, and most piped input is small.
	if !truncated && len(data) <= sendCap && utf8.Valid(data) {
		return Sample{
			Format:    format,
			Summary:   string(data),
			Verbatim:  true,
			Truncated: false,
			BytesRead: len(data),
		}
	}

	s := Sample{Format: format, Truncated: truncated, BytesRead: len(data)}
	switch format {
	case "json":
		s.Summary = summarizeJSON(data, truncated)
	case "ndjson":
		s.Summary = summarizeNDJSON(data, truncated)
	case "csv", "tsv":
		s.Summary = summarizeDelimited(data, format, truncated)
	default:
		s.Summary = summarizeText(data, sendCap)
	}
	s.Summary = clip(s.Summary, sendCap)
	return s
}

// detectFormat classifies the prefix. It is deliberately cheap and only looks
// at the first few lines.
func detectFormat(data []byte) string {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return "text"
	}

	lines := splitLines(trimmed, 5)
	jsonLines := 0
	for _, ln := range lines {
		t := bytes.TrimSpace(ln)
		if len(t) == 0 {
			continue
		}
		if t[0] == '{' && t[len(t)-1] == '}' && json.Valid(t) {
			jsonLines++
		}
	}
	if jsonLines >= 2 {
		return "ndjson"
	}

	if trimmed[0] == '{' || trimmed[0] == '[' {
		return "json"
	}

	if len(lines) > 0 {
		header := lines[0]
		if bytes.Count(header, []byte("\t")) >= 1 {
			return "tsv"
		}
		if bytes.Count(header, []byte(",")) >= 1 {
			return "csv"
		}
	}
	return "text"
}

// ---------- JSON ----------

// fieldInfo accumulates what was observed at one path.
type fieldInfo struct {
	types   []string
	example string
	seen    int
}

// jsonWalker infers a schema from a token stream. It is built to survive a
// truncated document: whatever was learned before the stream ran out is kept.
type jsonWalker struct {
	dec    *json.Decoder
	fields map[string]*fieldInfo
	order  []string
	nodes  int
	// topLevelItems counts elements of a root-level array.
	topLevelItems int
	rootKind      string
}

var errNodeBudget = errors.New("node budget exhausted")

func summarizeJSON(data []byte, truncated bool) string {
	var b strings.Builder
	b.WriteString("The input is JSON. Structure inferred from the sample")
	if truncated {
		b.WriteString(" (the real input is larger than the sample)")
	}
	b.WriteString(":\n")
	b.WriteString(jsonSchema(data, truncated))
	return b.String()
}

// jsonSchema renders just the shape of a JSON document, with no framing text,
// so it can be embedded in other summaries.
func jsonSchema(data []byte, truncated bool) string {
	w := &jsonWalker{
		dec:    json.NewDecoder(bytes.NewReader(data)),
		fields: map[string]*fieldInfo{},
	}
	// A truncated document always ends in an error; that is expected and the
	// partial schema is still useful.
	_ = w.value("", 0)

	var b strings.Builder
	switch {
	case w.rootKind == "array" && w.topLevelItems > 0:
		if truncated {
			fmt.Fprintf(&b, "Root: array (at least %d elements)\n", w.topLevelItems)
		} else {
			fmt.Fprintf(&b, "Root: array (%d elements)\n", w.topLevelItems)
		}
	case w.rootKind != "":
		fmt.Fprintf(&b, "Root: %s\n", w.rootKind)
	}

	if len(w.order) == 0 {
		b.WriteString("(structure could not be determined from the sample)\n")
		return b.String()
	}

	b.WriteString("Paths (jq syntax), types and example values:\n")
	width := 0
	for _, p := range w.order {
		if n := len(renderPath(p)); n > width {
			width = n
		}
	}
	if width > 44 {
		width = 44
	}
	for _, p := range w.order {
		f := w.fields[p]
		path := renderPath(p)
		line := fmt.Sprintf("  %-*s  %-8s", width, path, strings.Join(f.types, "|"))
		if f.example != "" {
			line += "  e.g. " + f.example
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	return b.String()
}

// value consumes exactly one JSON value from the decoder.
func (w *jsonWalker) value(path string, depth int) error {
	if w.nodes >= maxJSONNodes || depth > 24 {
		return errNodeBudget
	}
	t, err := w.dec.Token()
	if err != nil {
		return err
	}
	w.nodes++

	delim, isDelim := t.(json.Delim)
	if !isDelim {
		w.record(path, typeName(t), exampleOf(t))
		return nil
	}

	switch delim {
	case '{':
		if depth == 0 {
			w.rootKind = "object"
		}
		w.record(path, "object", "")
		for w.dec.More() {
			kt, err := w.dec.Token()
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			if err := w.value(path+"."+key, depth+1); err != nil {
				return err
			}
		}
		_, err := w.dec.Token() // closing brace
		return err

	case '[':
		if depth == 0 {
			w.rootKind = "array"
		}
		w.record(path, "array", "")
		i := 0
		for w.dec.More() {
			if depth == 0 {
				w.topLevelItems++
			}
			if i < arrayProbeElements {
				if err := w.value(path+"[]", depth+1); err != nil {
					return err
				}
			} else {
				// Beyond the probe, consume without inspecting so element
				// count stays accurate but the schema map stays small.
				var raw json.RawMessage
				if err := w.dec.Decode(&raw); err != nil {
					return err
				}
				w.nodes++
			}
			i++
		}
		_, err := w.dec.Token() // closing bracket
		return err
	}
	return nil
}

func (w *jsonWalker) record(path, typ, example string) {
	if path == "" {
		return // the root itself is reported separately
	}
	f, ok := w.fields[path]
	if !ok {
		if len(w.order) >= maxSummaryFields {
			return
		}
		f = &fieldInfo{}
		w.fields[path] = f
		w.order = append(w.order, path)
	}
	f.seen++
	if !containsStr(f.types, typ) {
		f.types = append(f.types, typ)
	}
	if f.example == "" && example != "" {
		f.example = example
	}
}

// renderPath turns an internal path into jq syntax.
func renderPath(p string) string {
	if strings.HasPrefix(p, ".") {
		return p
	}
	return "." + p
}

func typeName(t any) string {
	switch t.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, json.Number:
		return "number"
	case string:
		return "string"
	}
	return "unknown"
}

func exampleOf(t any) string {
	switch v := t.(type) {
	case nil:
		return "null"
	case bool:
		return fmt.Sprintf("%t", v)
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case string:
		return fmt.Sprintf("%q", clip(v, 40))
	}
	return ""
}

// ---------- NDJSON ----------

func summarizeNDJSON(data []byte, truncated bool) string {
	lines := splitLines(data, 2000)
	complete := 0
	var first []byte
	for _, ln := range lines {
		t := bytes.TrimSpace(ln)
		if len(t) == 0 || !json.Valid(t) {
			continue
		}
		complete++
		if first == nil {
			first = t
		}
	}

	var b strings.Builder
	b.WriteString("The input is newline-delimited JSON (one object per line)")
	if truncated {
		fmt.Fprintf(&b, "; at least %d records, the real input is larger", complete)
	} else {
		fmt.Fprintf(&b, "; %d records", complete)
	}
	b.WriteString(".\n")
	if first != nil {
		b.WriteString("Structure of one record:\n")
		b.WriteString(jsonSchema(first, false))
	}
	return b.String()
}

// ---------- CSV / TSV ----------

func summarizeDelimited(data []byte, format string, truncated bool) string {
	// Drop a trailing partial line so the parser is not fed half a record.
	if truncated {
		if i := bytes.LastIndexByte(data, '\n'); i > 0 {
			data = data[:i]
		}
	}

	rd := csv.NewReader(bytes.NewReader(data))
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	if format == "tsv" {
		rd.Comma = '\t'
	}

	records := make([][]string, 0, 8)
	total := 0
	for {
		rec, err := rd.Read()
		if err != nil {
			break
		}
		total++
		if len(records) < 4 {
			records = append(records, rec)
		}
	}
	if len(records) == 0 {
		return summarizeText(data, 2000)
	}

	var b strings.Builder
	name := strings.ToUpper(format)
	fmt.Fprintf(&b, "The input is %s with %d columns", name, len(records[0]))
	if truncated {
		fmt.Fprintf(&b, "; at least %d rows, the real input is larger", total)
	} else {
		fmt.Fprintf(&b, "; %d rows including the header", total)
	}
	b.WriteString(".\nColumns (1-indexed, for awk/cut):\n")
	for i, col := range records[0] {
		fmt.Fprintf(&b, "  $%d  %s\n", i+1, clip(col, 40))
	}
	if len(records) > 1 {
		b.WriteString("First data rows:\n")
		for _, rec := range records[1:] {
			b.WriteString("  " + clip(strings.Join(rec, string(delimiterOf(format))), 160) + "\n")
		}
	}
	return b.String()
}

func delimiterOf(format string) rune {
	if format == "tsv" {
		return '\t'
	}
	return ','
}

// ---------- text ----------

func summarizeText(data []byte, sendCap int) string {
	lines := splitLines(data, 12)
	var b strings.Builder
	fmt.Fprintf(&b, "The input is plain text. First %d lines:\n", len(lines))
	for _, ln := range lines {
		b.WriteString("  " + clip(strings.TrimRight(string(ln), "\r"), 200) + "\n")
	}
	return clip(b.String(), sendCap)
}

// ---------- helpers ----------

func splitLines(data []byte, maxLines int) [][]byte {
	out := make([][]byte, 0, maxLines)
	for len(data) > 0 && len(out) < maxLines {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			out = append(out, data)
			break
		}
		out = append(out, data[:i])
		data = data[i+1:]
	}
	return out
}

// clip truncates on a rune boundary so the result is always valid UTF-8.
func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sortedKeys is used by tests and keeps output deterministic where order does
// not otherwise matter.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
