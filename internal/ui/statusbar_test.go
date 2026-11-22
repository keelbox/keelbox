package ui

import (
	"math"
	"testing"
)

// xterm256 is colour i of the 6x6x6 cube and the 24 greys.
func xterm256(i int) rgb {
	if i >= 232 {
		v := uint8(8 + 10*(i-232)) //nolint:gosec // G115: at most 238
		return rgb{v, v, v}
	}
	i -= 16
	return rgb{uint8(cube[i/36]), uint8(cube[i/6%6]), uint8(cube[i%6])} //nolint:gosec // G115: cube levels are bytes
}

// contrast is the WCAG contrast ratio of two colours.
func contrast(a, b rgb) float64 {
	lum := func(c rgb) float64 {
		ch := func(v uint8) float64 {
			x := float64(v) / 255
			if x <= 0.03928 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*ch(c.r) + 0.7152*ch(c.g) + 0.0722*ch(c.b)
	}
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// The status line's text reads on its ground with truecolor, and still
// does when a terminal with 256 colours gets the nearest of them.
func TestStatusBarReadsInTruecolorAndIn256(t *testing.T) {
	text := map[string]rgb{"the box name": night[roleAccent], "the hint": night[roleDim], "the current window": night[roleInfo]}
	for what, fg := range text {
		if c := contrast(fg, barGround); c < 4.5 {
			t.Errorf("%s on the bar: contrast %.1f, want at least 4.5", what, c)
		}
		fg256, bg256 := xterm256(nearest256(fg)), xterm256(nearest256(barGround))
		if c := contrast(fg256, bg256); c < 4.5 {
			t.Errorf("%s on the bar in 256 colours (%d on %d): contrast %.1f, want at least 4.5", what, nearest256(fg), nearest256(barGround), c)
		}
	}
	// A grey, not the cube's navy or black: the bar stays apart from a
	// black terminal without turning blue.
	if got := nearest256(barGround); got < 233 || got > 237 {
		t.Errorf("the bar's ground is colour %d in 256 colours, want a dark grey", got)
	}
	b := NightStatusBar()
	if b.Ground != "#1b2330" || b.Name != "#e0a458" || b.Text != "#95a1b5" || b.Here != "#86a9d6" || b.Rule != "#4a5a73" {
		t.Errorf("status bar colours: %+v", b)
	}
}
