package term

import "testing"

func TestParseBackground(t *testing.T) {
	for in, want := range map[string][2]bool{
		"\x1b]11;rgb:0707/0d0d/1717\x1b\\\x1b[?62;22c": {false, true},
		"\x1b]11;rgb:f3f3/eeee/e2e2\x07\x1b[?1;2c":     {true, true},
		"\x1b]11;rgb:ff/ff/ff\x07":                     {true, true},
		"\x1b[?1;2c":                                   {false, false},
		"\x1b]11;rgb:zz/00/00\x07":                     {false, false},
	} {
		light, ok := parseBackground([]byte(in))
		if light != want[0] || ok != want[1] {
			t.Errorf("parseBackground(%q) = %v %v, want %v %v", in, light, ok, want[0], want[1])
		}
	}
	if !deviceAttributes([]byte("x\x1b[?62;22c")) || deviceAttributes([]byte("\x1b]11;rgb:0/0/0")) {
		t.Error("device attributes answer not recognised")
	}
}
