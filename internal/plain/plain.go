// Package plain turns text from outside keelbox into printable text: what an
// account on a box, a box's snapshot or a program's output put in a string
// reaches a terminal, a notification or a log only as printable characters.
// Every control character (the escape that starts a terminal sequence, a
// bell, a carriage return that rewrites a line) and every other character
// unicode does not count as printable (bidirectional overrides, zero-width
// marks) is shown as an escape such as \x1b or \u202e, so a status line can
// be neither rewritten nor hidden by what it shows.
package plain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Line is s as one line: newlines and tabs become spaces, runs of spaces
// one, and every other character that is not printable is escaped.
func Line(s string) string {
	return strings.Join(strings.Fields(escape(s, false)), " ")
}

// Text is s with its newlines and tabs kept and every other character that
// is not printable escaped, for output of several lines.
func Text(s string) string { return escape(s, true) }

// Lines is Line applied to each element.
func Lines(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = Line(s)
	}
	return out
}

// Safe reports whether s needs no escaping as one line.
func Safe(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return utf8.ValidString(s)
}

func escape(s string, keepLines bool) string {
	if Safe(s) {
		return s
	}
	var b strings.Builder
	for i, w := 0, 0; i < len(s); i += w {
		r, n := utf8.DecodeRuneInString(s[i:])
		w = n
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n' || r == '\t':
			if keepLines {
				b.WriteRune(r)
			} else {
				b.WriteByte(' ')
			}
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}
