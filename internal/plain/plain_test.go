package plain

import "testing"

// A tmux session name an agent could plant: clear the screen, home the
// cursor, set the title, print a green ALL OK.
const planted = "\x1b[2J\x1b[H\x1b]0;owned\a\x1b[32mALL OK\x1b[0m"

func TestLine(t *testing.T) {
	for in, want := range map[string]string{
		"main":                     "main",
		planted:                    `\x1b[2J\x1b[H\x1b]0;owned\x07\x1b[32mALL OK\x1b[0m`,
		"a\r\nb\tc  d":             `a\x0d b c d`,
		"over\u202ewrite":          `over\u202ewrite`,
		"zero\u200bwidth":          `zero\u200bwidth`,
		"bad \xff byte":            `bad \xff byte`,
		"\x9b31m C1":               `\x9b31m C1`,
		"caf\u00e9 \u2713 \u00b7":  "caf\u00e9 \u2713 \u00b7",
		"\x1b]52;c;cGF5bG9hZA==\a": `\x1b]52;c;cGF5bG9hZA==\x07`,
	} {
		if got := Line(in); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestText(t *testing.T) {
	if got := Text("one\n\ttwo\x1b[1A"); got != "one\n\ttwo\\x1b[1A" {
		t.Errorf("Text = %q", got)
	}
	for _, s := range []string{Line(planted), Text(planted)} {
		for _, r := range s {
			if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
				t.Errorf("%q kept a control character", s)
			}
		}
	}
}
