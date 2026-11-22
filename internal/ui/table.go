package ui

import "strings"

// Table lays out rows in columns two spaces apart, measured by what shows
// (styled cells line up), the header dimmed. The last column is not
// padded. On a terminal it wraps to the width under itself, or, where the
// columns before it leave it too narrow to read, under its row; a word
// that Unbroken joined (a command to copy) stays whole. In plain text off
// a terminal it is what text/tabwriter writes. Every table keelbox prints
// is laid out here.
func (s Style) Table(header []string, rows [][]string) string {
	width := make([]int, len(header))
	all := append([][]string{header}, rows...)
	for _, r := range all {
		for i, c := range r {
			if i < len(width) {
				width[i] = max(width[i], Width(c))
			}
		}
	}
	// Where the last column starts, and what the terminal leaves it.
	offset := 0
	for _, w := range width[:max(len(width)-1, 0)] {
		offset += w + 2
	}
	const narrow, under = 24, 4
	room := 0 // 0: not wrapped
	if s.Width > 0 {
		room = s.Cols() - offset
	}
	var b strings.Builder
	for n, r := range all {
		for i, c := range r {
			if n == 0 {
				c = s.Dim(c)
			}
			if i < len(r)-1 {
				b.WriteString(Pad(spaced(c), width[i]) + "  ")
				continue
			}
			switch {
			case room == 0 || i < len(header)-1 || Width(c) <= room:
				b.WriteString(spaced(c))
			case room >= narrow:
				b.WriteString(Hang(c, room, offset))
			default:
				// Under the row: the columns before it take the width.
				line := strings.TrimRight(b.String(), " ")
				b.Reset()
				b.WriteString(line + "\n" + strings.Repeat(" ", under) + Hang(c, s.Cols()-under, under))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
