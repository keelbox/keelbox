package ui

import (
	"strconv"
	"strings"
)

// Screen is what a terminal shows after s, for the moves this package and
// the questions make: carriage return, newline, erase line (ESC [ K),
// erase down (ESC [ J), cursor up (ESC [ n A) and hide or show the cursor.
// Style sequences are kept as text. Tests and the screenshot tool use it
// to see the settled screen rather than every redraw.
func Screen(s string) string {
	lines := []string{""}
	row := 0
	cr := false
	put := func(t string) {
		if cr {
			lines[row] = ""
			cr = false
		}
		lines[row] += t
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\r':
			cr = true
			i++
		case c == '\n':
			cr = false
			row++
			if row == len(lines) {
				lines = append(lines, "")
			}
			i++
		case c == 0x1b && i+1 < len(s) && s[i+1] == '[':
			n := escapeLen(s[i:])
			seq := s[i : i+n]
			arg, final := seq[2:len(seq)-1], seq[len(seq)-1]
			switch final {
			case 'K':
				lines[row], cr = "", false
			case 'J':
				lines[row], cr = "", false
				lines = lines[:row+1]
			case 'A':
				k, err := strconv.Atoi(arg)
				if err != nil {
					k = 1
				}
				row = max(0, row-k)
			case 'm':
				put(seq)
			}
			i += n
		default:
			j := i + 1
			for j < len(s) && s[j] != '\r' && s[j] != '\n' && s[j] != 0x1b {
				j++
			}
			put(s[i:j])
			i = j
		}
	}
	return strings.Join(lines, "\n")
}
