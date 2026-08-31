package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// prompter asks a series of questions on one stream.
//
// The shared reader is the point. A fresh bufio.Scanner per question reads
// ahead and then throws the unconsumed remainder away with itself, so the
// second question of a wizard would silently receive nothing -- which is
// exactly what happens when the answers arrive down a pipe rather than from a
// terminal.
type prompter struct {
	out io.Writer
	in  *bufio.Reader
	p   palette
}

func newPrompter(out io.Writer, in io.Reader, p palette) *prompter {
	return &prompter{out: out, in: bufio.NewReader(in), p: p}
}

// readLine returns the next line without its terminator, and false at EOF.
func (w *prompter) readLine() (string, bool) {
	line, err := w.in.ReadString('\n')
	if line == "" && err != nil {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}

// Choice is one entry in a selection list.
type Choice struct {
	// Label is the entry as shown.
	Label string
	// Hint is dim trailing text: a price, a caveat, what the option implies.
	Hint string
	// Value is what the caller gets back.
	Value string
}

// Select renders a list and returns the chosen value.
//
// On a real terminal the list is navigable with the arrow keys and confirmed
// with Enter, redrawing in place. Everywhere else -- a pipe, a CI job, a
// terminal that will not go raw -- it degrades to a numbered list read from
// stdin, so the wizard is still usable when it cannot be pretty.
func (w *prompter) Select(question string, choices []Choice, current int) (string, bool) {
	if len(choices) == 0 {
		return "", false
	}
	if current < 0 || current >= len(choices) {
		current = 0
	}
	out, p := w.out, w.p

	term, ok := enterRaw()
	if !ok {
		return w.selectByNumber(question, choices, current)
	}
	defer term.Restore()

	fmt.Fprintf(out, "%s\r\n", p.Cyan(question))
	drawChoices(out, p, choices, current, false)

	for {
		key := term.ReadKey()
		switch {
		case key.Name == KeyUp || key.Rune == 'k':
			current = (current - 1 + len(choices)) % len(choices)
		case key.Name == KeyDown || key.Rune == 'j':
			current = (current + 1) % len(choices)
		case key.Name == KeyEnter:
			redrawChoices(out, p, choices, current, true)
			return choices[current].Value, true
		case key.Name == KeyEsc || key.Name == KeyInterrupt:
			redrawChoices(out, p, choices, current, true)
			return "", false
		case key.Rune >= '1' && key.Rune <= '9':
			if i := int(key.Rune - '1'); i < len(choices) {
				current = i
				redrawChoices(out, p, choices, current, true)
				return choices[current].Value, true
			}
			continue
		default:
			continue
		}
		redrawChoices(out, p, choices, current, false)
	}
}

// drawChoices writes the list. In raw mode the terminal does not translate
// newlines, so every line ends with an explicit carriage return.
func drawChoices(out io.Writer, p palette, choices []Choice, current int, final bool) {
	for i, c := range choices {
		marker := "  "
		label := c.Label
		if i == current {
			marker = p.Cyan("❯ ")
			label = p.Cyan(label)
		}
		line := marker + label
		if c.Hint != "" && !final {
			line += "  " + p.Dim(c.Hint)
		}
		fmt.Fprintf(out, "%s\r\n", line)
	}
}

// redrawChoices moves back over the list and paints it again, so the selection
// moves without the list scrolling away.
func redrawChoices(out io.Writer, p palette, choices []Choice, current int, final bool) {
	fmt.Fprintf(out, "\033[%dA", len(choices)) // up over the previous render
	fmt.Fprint(out, "\r\033[J")                // clear from here down
	drawChoices(out, p, choices, current, final)
}

// selectByNumber is the fallback for anything that is not an interactive
// terminal.
func (w *prompter) selectByNumber(question string, choices []Choice, current int) (string, bool) {
	out, p := w.out, w.p
	fmt.Fprintf(out, "%s\n", p.Cyan(question))
	for i, c := range choices {
		line := fmt.Sprintf("  [%d] %s", i+1, c.Label)
		if c.Hint != "" {
			line += "  " + p.Dim(c.Hint)
		}
		fmt.Fprintln(out, line)
	}
	fmt.Fprintf(out, "> [%d] ", current+1)

	answer, ok := w.readLine()
	if !ok {
		return "", false
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return choices[current].Value, true
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(choices) {
		return "", false
	}
	return choices[n-1].Value, true
}

// Prompt asks for a line of text, offering a default that Enter accepts.
//
// The terminal is left in its normal cooked mode here on purpose: this is the
// one place the user needs echo, backspace and paste to behave as usual.
func (w *prompter) Prompt(question, def string) string {
	if def != "" {
		fmt.Fprintf(w.out, "%s %s ", w.p.Cyan(question), w.p.Dim("["+def+"]"))
	} else {
		fmt.Fprintf(w.out, "%s ", w.p.Cyan(question))
	}
	answer, ok := w.readLine()
	if !ok {
		fmt.Fprintln(w.out)
		return def
	}
	if answer = strings.TrimSpace(answer); answer == "" {
		return def
	}
	return answer
}

// masked renders a secret so it can be recognised without being disclosed --
// enough to answer "is the right key in there?" and nothing more.
func masked(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return strings.Repeat("•", len(secret))
	}
	return secret[:4] + strings.Repeat("•", 8) + secret[len(secret)-4:]
}
