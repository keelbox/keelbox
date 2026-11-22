package ui

import (
	"strings"
)

// MaxWidth is the widest a card or a rule is drawn, so that a page has one
// right edge on a wide terminal: the step and section rules end where the
// cards do.
const MaxWidth = 88

// Card is a titled block of rows and text: a summary to read before a
// go-ahead, the steps to create a token, the box ready to use. On a
// capable terminal it is drawn in a rounded box fitted to the width; in
// plain text it is the title with a colon and the rows indented under it,
// so a log or a screen reader gets the same words in the same order.
type Card struct {
	Title    string
	Lines    []Line
	KeyWidth int // the key column; 0 fits the longest key
	MaxWidth int // the widest the box gets; 0 is MaxWidth columns
	// MinValue is the narrowest the value column may get, such as the
	// width of a URL no line should break. On a terminal that leaves less
	// beside the keys, each key goes on a line of its own with its value
	// under it. 0 never does.
	MinValue int
	// Fill draws the box as wide as it may be even when its content is
	// narrower, so that it ends where the lines printed around it do.
	Fill bool
}

// Line is one entry of a card: a key and its value, aligned with the
// other keys and wrapped under the value; text alone (no key), wrapped to
// the card; or nothing, a blank line. A word of a value too wide for its
// column, a URL on a narrow terminal, goes on a line of its own from the
// card's edge when it fits there.
type Line struct {
	Key, Text string
	Mark      string // a mark before the key, such as a Tick; its own column
	row       bool   // a row with an empty key: the text stays in the value column
}

// Row is a key and value line; with an empty key, the value goes on under
// the row before.
func Row(k, v string) Line { return Line{Key: k, Text: v, row: true} }

// Text is a paragraph line.
func Text(t string) Line { return Line{Text: t} }

// Blank is an empty line.
func Blank() Line { return Line{} }

// Card renders c, ending with a newline.
func (s Style) Card(c Card) string {
	kw := c.KeyWidth
	marks := false
	for _, l := range c.Lines {
		if c.KeyWidth == 0 && l.Key != "" {
			kw = max(kw, Width(l.Key))
		}
		marks = marks || l.Mark != ""
	}
	boxed := s.Live && s.UTF8
	maxW := c.MaxWidth
	if maxW == 0 {
		maxW = MaxWidth
	}
	inner := 0 // the text width inside the box or after the indent; 0 is no wrapping
	switch {
	case boxed:
		inner = min(s.Cols(), maxW) - 4
	case s.Width > 0:
		inner = s.Cols() - 2
	}
	stack := c.MinValue > 0 && Stacks(inner, kw, c.MinValue)
	var body []string
	for _, l := range c.Lines {
		if stack && l.row {
			body = append(body, s.stackedLine(l, inner)...)
			continue
		}
		body = append(body, s.cardLine(l, kw, marks, inner)...)
	}
	var b strings.Builder
	if !boxed {
		switch {
		case c.Title == "":
		case strings.HasSuffix(c.Title, ".") || strings.HasSuffix(c.Title, ":"):
			b.WriteString(s.Bold(c.Title) + "\n")
		default:
			b.WriteString(s.Bold(c.Title) + ":\n")
		}
		for _, l := range body {
			if l == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString("  " + l + "\n")
		}
		return b.String()
	}
	// Shrink the box to its content when that is narrower.
	width := 0
	for _, l := range body {
		width = max(width, Width(l))
	}
	width = max(width, Width(c.Title)+3)
	width = min(width, inner)
	if c.Fill {
		width = inner
	}
	top := s.Faint("╭─")
	if c.Title != "" {
		top += " " + s.Heading(c.Title) + " " + s.Faint(strings.Repeat("─", max(0, width-Width(c.Title)-1))+"╮")
	} else {
		top += s.Faint(strings.Repeat("─", width+1) + "╮")
	}
	b.WriteString(top + "\n")
	side := s.Faint("│")
	for _, l := range body {
		b.WriteString(side + " " + Pad(l, width) + " " + side + "\n")
	}
	b.WriteString(s.Faint("╰"+strings.Repeat("─", width+2)+"╯") + "\n")
	return b.String()
}

// Stacks reports whether rows with a key column of kw, in a text width of
// inner (0 is no wrapping), leave their values fewer than minValue columns,
// so that the keys go over the values instead.
func Stacks(inner, kw, minValue int) bool {
	return inner > 0 && inner-kw-2 < minValue
}

// StackIndent is how far a value is indented under its key when stacked.
const StackIndent = 2

// stackedLine is a row with its key on a line of its own and the value
// under it, indented.
func (s Style) stackedLine(l Line, inner int) []string {
	var out []string
	if l.Key != "" {
		k := l.Key
		if !strings.Contains(k, "\x1b") {
			k = s.Dim(k)
		}
		out = append(out, k)
	}
	if l.Text == "" {
		return out
	}
	for _, x := range Wrap(l.Text, max(inner-StackIndent, 16)) {
		out = append(out, strings.Repeat(" ", StackIndent)+x)
	}
	return out
}

func (s Style) cardLine(l Line, kw int, marks bool, inner int) []string {
	if l.Key == "" && l.Text == "" && l.Mark == "" {
		return []string{""}
	}
	lead := ""
	if marks {
		lead = Pad(l.Mark, 2) + " "
	}
	if l.Key == "" && l.Mark == "" && !l.row {
		if inner == 0 {
			return []string{spaced(l.Text)}
		}
		return Wrap(l.Text, inner)
	}
	key := ""
	if kw > 0 {
		k := l.Key
		if !strings.Contains(k, "\x1b") {
			k = s.Dim(k)
		}
		key = Pad(k, kw) + "  "
	}
	indent := Width(lead) + Width(key)
	if inner == 0 || l.Text == "" {
		return []string{strings.TrimRight(lead+key+spaced(l.Text), " ")}
	}
	lines, wide := rowLines(l.Text, max(inner-indent, 16), inner)
	var out []string
	if wide[0] {
		out = append(out, strings.TrimRight(lead+key, " "))
	}
	for i, x := range lines {
		switch {
		case wide[i]:
			out = append(out, x)
		case i == 0:
			out = append(out, lead+key+x)
		default:
			out = append(out, strings.Repeat(" ", indent)+x)
		}
	}
	return out
}

// rowLines wraps a row's value to its column, n wide. A word wider than
// the column that still fits the card (a URL on a narrow terminal) gets a
// line of its own from the card's edge, marked in wide, so it is neither
// broken nor pushed past the border.
func rowLines(text string, n, inner int) (lines []string, wide []bool) {
	var run []string
	flush := func() {
		if len(run) == 0 {
			return
		}
		for _, l := range wrapWords(run, n) {
			lines, wide = append(lines, l), append(wide, false)
		}
		run = nil
	}
	for _, w := range words(text) {
		if ww := Width(w); ww > n && ww <= inner {
			flush()
			lines, wide = append(lines, spaced(w)), append(wide, true)
			continue
		}
		run = append(run, w)
	}
	flush()
	if len(lines) == 0 {
		lines, wide = []string{""}, []bool{false}
	}
	return carryStyle(lines), wide
}
