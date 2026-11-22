package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Width is how many columns s takes on a terminal: its characters, less
// the SGR and OSC 8 sequences this package writes.
func Width(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i += escapeLen(s[i:])
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}

// escapeLen is the length of the escape sequence at the start of s: CSI
// (ESC [ ... final byte) or OSC (ESC ] ... ST or BEL).
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	default:
		return 2
	}
	return len(s)
}

// Strip is s without escape sequences: what a plain reader sees.
func Strip(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i += escapeLen(s[i:])
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// Pad fills s with spaces to n columns.
func Pad(s string, n int) string {
	if w := Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// Wrap breaks s at spaces into lines of at most n columns. A word longer
// than n (a path, a URL) is broken where it must be. Styled words keep
// their style.
func Wrap(s string, n int) []string {
	return carryStyle(wrapWords(words(s), n))
}

// noBreak is the space Unbroken joins words with.
const noBreak = '\u00a0'

// Unbroken is s with its spaces as ones no line is broken at, for words
// that read as one, an amount of money such as "EUR 7.49": Wrap and Card
// keep them on a line together and write plain spaces.
func Unbroken(s string) string { return strings.ReplaceAll(s, " ", string(noBreak)) }

// words are the words of s, those Unbroken joined as one.
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r != noBreak && unicode.IsSpace(r) })
}

// spaced is s with Unbroken's spaces as plain ones.
func spaced(s string) string { return strings.ReplaceAll(s, string(noBreak), " ") }

// wrapWords is Wrap for words already split, without the style carried
// from line to line.
func wrapWords(ws []string, n int) []string {
	n = max(n, 8)
	var lines []string
	line, lw := "", 0
	for _, w := range ws {
		ww := Width(w)
		for ww > n && !strings.Contains(w, "\x1b") {
			if lw > 0 {
				lines = append(lines, line)
				line, lw = "", 0
			}
			r := []rune(w)
			lines = append(lines, string(r[:n]))
			w = string(r[n:])
			ww = Width(w)
		}
		switch {
		case lw == 0:
			line, lw = w, ww
		case lw+1+ww <= n:
			line, lw = line+" "+w, lw+1+ww
		default:
			lines = append(lines, line)
			line, lw = w, ww
		}
	}
	if lw > 0 || len(lines) == 0 {
		lines = append(lines, line)
	}
	for i, l := range lines {
		lines[i] = spaced(l)
	}
	return lines
}

// carryStyle closes a style left open at the end of a wrapped line and
// opens it again on the next, so a phrase in bold stays bold across the
// break and a border after the line is not drawn in it.
func carryStyle(lines []string) []string {
	open := ""
	for i, l := range lines {
		prefix := open
		for j := 0; j < len(l); {
			if l[j] != 0x1b {
				j++
				continue
			}
			n := escapeLen(l[j:])
			if seq := l[j : j+n]; strings.HasSuffix(seq, "m") && strings.HasPrefix(seq, "\x1b[") {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					open = ""
				} else {
					open += seq
				}
			}
			j += n
		}
		l = prefix + l
		if open != "" {
			l += "\x1b[0m"
		}
		lines[i] = l
	}
	return lines
}

// Plain is s as plain text for this style's output: wrapped to the
// terminal with the lines after the first indented by indent spaces, or s
// as it is where the width is not known (a pipe, a log), which keeps one
// line per message there.
func (s Style) Plain(text string, indent int) string {
	if s.Width <= 0 {
		return spaced(text)
	}
	return Hang(text, s.Cols()-indent, indent)
}

// Hang wraps s to n columns with the lines after the first indented by
// indent spaces, and joins them.
func Hang(s string, n, indent int) string {
	return strings.Join(Wrap(s, n), "\n"+strings.Repeat(" ", indent))
}

// glyph picks the UTF-8 or the ASCII form.
func (s Style) glyph(utf, ascii string) string {
	if s.UTF8 {
		return utf
	}
	return ascii
}

// Tick marks something done or working.
func (s Style) Tick() string { return s.Good(s.glyph("✓", "ok")) }

// Cross marks a failure.
func (s Style) Cross() string { return s.Bad(s.glyph("✗", "!!")) }

// Bang marks something that needs a look but did not fail.
func (s Style) Bang() string { return s.Warn("!") }

// Pointer marks the selected choice in a menu.
func (s Style) Pointer() string { return s.Accent(s.glyph("›", ">")) }

// Sep is the separator between items on one line.
func (s Style) Sep() string { return s.Faint(s.glyph(" · ", " - ")) }

// Bullet marks an item of a list.
func (s Style) Bullet() string { return s.glyph("•", "-") }

// Ask marks a question.
func (s Style) Ask() string { return s.Accent("?") }

// Heading is a section heading: bold, in brass.
func (s Style) Heading(x string) string { return s.BoldAccent(x) }

// Step is the step indicator of keelbox up, "Step 2 of 5 · Hetzner token",
// on a line of its own after a blank one, ruled to the width on a capable
// terminal.
func (s Style) Step(i, n int, what string) string {
	label := fmt.Sprintf("Step %d of %d", i, n)
	if !s.Live || !s.UTF8 {
		return "\n" + s.Dim(label+": ") + s.Bold(what) + "\n"
	}
	head := s.Dim(label) + s.Sep() + s.Bold(what) + " "
	rule := max(0, min(s.Cols(), MaxWidth)-Width(head))
	return "\n" + head + s.Faint(strings.Repeat("─", rule)) + "\n"
}

// Link is text that opens url, where the terminal shows OSC 8 links; the
// text alone elsewhere, so it should say where it goes (usually the URL).
func (s Style) Link(text, url string) string {
	if !s.Links || !s.Live {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// URL is a URL as a link, in sounding blue.
func (s Style) URL(url string) string { return s.Link(s.Info(url), url) }

// Rule is a horizontal rule n columns wide.
func (s Style) Rule(n int) string {
	return s.Faint(strings.Repeat(s.glyph("─", "-"), max(n, 0)))
}

// Section is a heading that starts a part of the output, after a blank
// line.
func (s Style) Section(title string) string {
	if !s.Live || !s.UTF8 {
		return "\n" + s.Heading(title) + "\n"
	}
	head := s.Heading(title) + " "
	return "\n" + head + s.Faint(strings.Repeat("─", max(0, min(s.Cols(), MaxWidth)-Width(head)))) + "\n"
}
