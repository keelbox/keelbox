package ui

import (
	"strings"
	"testing"
)

// A table's last column wraps under itself on a terminal, or under its
// row where the columns before it leave too little room; a command joined
// with Unbroken stays whole and is written with plain spaces. Off a
// terminal a row is one line.
func TestTableWrapsItsLastColumn(t *testing.T) {
	header := []string{"BOX", "PROVIDER", "ADDRESS", "STATUS"}
	rows := [][]string{
		{"alpha", "ssh", "203.0.113.7", "ready"},
		{"echo", "hetzner", "-", "server destroyed 2026-11-01: " + Unbroken("keelbox up echo") + " or " + Unbroken("keelbox forget echo")},
	}
	plain := Style{}.Table(header, rows)
	if want := "BOX    PROVIDER  ADDRESS      STATUS\nalpha  ssh       203.0.113.7  ready\necho   hetzner   -            server destroyed 2026-11-01: keelbox up echo or keelbox forget echo\n"; plain != want {
		t.Errorf("off a terminal:\n%q\nwant\n%q", plain, want)
	}
	beside := Style{Width: 80}.Table(header, rows)
	if want := "echo   hetzner   -            server destroyed 2026-11-01: keelbox up echo or\n                              keelbox forget echo\n"; !strings.Contains(beside, want) {
		t.Errorf("80 columns: the status does not hang under itself:\n%s", beside)
	}
	under := Style{Width: 50}.Table(header, rows)
	if want := "echo   hetzner   -\n    server destroyed 2026-11-01: keelbox up echo\n    or keelbox forget echo\n"; !strings.Contains(under, want) {
		t.Errorf("50 columns: the status is not under its row:\n%s", under)
	}
	for _, w := range []int{50, 60, 80, 100, 120} {
		got := Style{Width: w}.Table(header, rows)
		for _, l := range strings.Split(got, "\n") {
			if Width(l) >= w {
				t.Errorf("%d: a line is %d columns: %q", w, Width(l), l)
			}
		}
		if strings.ContainsRune(got, noBreak) || !strings.Contains(got, "keelbox up echo") || !strings.Contains(got, "keelbox forget echo") {
			t.Errorf("%d: a command is broken, or not written with plain spaces:\n%q", w, got)
		}
	}
}
