package ui

import (
	"strings"
	"testing"
)

// A card that fills is as wide as it may be whatever is in it: the
// terminal's width less one column, or 88. Without Fill it is as wide as
// its content. Plain text has no box to fill.
func TestCardFill(t *testing.T) {
	rows := []Line{Row("come back", "keelbox resume acme")}
	for width, want := range map[int]int{60: 59, 80: 79, 120: 88} {
		st := Style{Live: true, UTF8: true, Width: width}
		for _, l := range strings.Split(strings.TrimRight(st.Card(Card{Title: "Parked", Lines: rows, Fill: true}), "\n"), "\n") {
			if Width(l) != want {
				t.Errorf("%d columns: a line of a card that fills is %d wide, want %d: %q", width, Width(l), want, l)
			}
		}
		if l, _, _ := strings.Cut(st.Card(Card{Title: "Parked", Lines: rows}), "\n"); Width(l) >= want {
			t.Errorf("%d columns: a card that does not fill is %d wide: %q", width, Width(l), l)
		}
	}
	if got, want := (Style{}).Card(Card{Title: "Parked", Lines: rows, Fill: true}), "Parked:\n  come back  keelbox resume acme\n"; got != want {
		t.Errorf("plain: %q", got)
	}
}
