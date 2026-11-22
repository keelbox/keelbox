package ui

import "fmt"

// The brand palette (keelbox.dev, brand/terminal-palette.json): Night
// Chart on a dark terminal, Paper Chart on a light one. Each role has its
// truecolor value, the nearest of the 256 xterm colours, and the 16 colour
// code a terminal's own theme fills in.
type role int

const (
	roleAccent role = iota // brass: choices, defaults, commands to type
	roleOK                 // signal green: done, works
	roleWarn               // brass: needs a look
	roleErr                // flare: failed
	roleInfo               // sounding blue: links, the wordmark's water
	roleDim                // mist: detail, hints, timings
	roleFaint              // rope: rules, borders, ticks
)

type rgb struct{ r, g, b uint8 }

func hex(v uint32) rgb { return rgb{uint8(v >> 16 & 0xff), uint8(v >> 8 & 0xff), uint8(v & 0xff)} } //nolint:gosec // G115: masked to a byte

var night = map[role]rgb{
	roleAccent: hex(0xe0a458), // Brass
	roleOK:     hex(0x6ee7b7), // Signal
	roleWarn:   hex(0xefc98e), // Buff
	roleErr:    hex(0xf47c5c), // Flare
	roleInfo:   hex(0x86a9d6), // Sounding
	roleDim:    hex(0x95a1b5), // Mist
	roleFaint:  hex(0x4a5a73), // Rope
}

var paper = map[role]rgb{
	roleAccent: hex(0x986513), // Buff ink
	roleOK:     hex(0x16825c), // Signal
	roleWarn:   hex(0x986513), // Buff ink
	roleErr:    hex(0xc2452c), // Flare
	roleInfo:   hex(0x2e5d9a), // Sounding
	roleDim:    hex(0x5b6a80), // Rope
	roleFaint:  hex(0x8a8f99), // Mist
}

// basic is the 16 colour SGR code for each role; 2 is faint.
var basic = map[role]string{
	roleAccent: "33",
	roleOK:     "32",
	roleWarn:   "33",
	roleErr:    "31",
	roleInfo:   "34",
	roleDim:    "2",
	roleFaint:  "2",
}

func (s Style) code(r role) string {
	p := night
	if s.Light {
		p = paper
	}
	c := p[r]
	switch s.Depth {
	case DepthTrue:
		return fmt.Sprintf("38;2;%d;%d;%d", c.r, c.g, c.b)
	case Depth256:
		return fmt.Sprintf("38;5;%d", nearest256(c))
	}
	return basic[r]
}

// cube are the six levels of each channel in the 256 colour cube.
var cube = [6]int{0, 95, 135, 175, 215, 255}

// nearest256 is the xterm colour closest to c, from the 6x6x6 cube and
// the 24 greys (the first 16 vary with the theme, so they are not used).
func nearest256(c rgb) int {
	level := func(v uint8) int {
		best := 0
		for i, l := range cube {
			if abs(int(v)-l) < abs(int(v)-cube[best]) {
				best = i
			}
		}
		return best
	}
	r, g, b := level(c.r), level(c.g), level(c.b)
	idx, d := 16+36*r+6*g+b, dist(c, cube[r], cube[g], cube[b])
	for i := 0; i < 24; i++ {
		v := 8 + 10*i
		if gd := dist(c, v, v, v); gd < d {
			idx, d = 232+i, gd
		}
	}
	return idx
}

func dist(c rgb, r, g, b int) int {
	dr, dg, db := int(c.r)-r, int(c.g)-g, int(c.b)-b
	return 2*dr*dr + 4*dg*dg + 3*db*db
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (s Style) sgr(code, x string) string {
	if !s.Colour || x == "" {
		return x
	}
	return "\x1b[" + code + "m" + x + "\x1b[0m"
}

func (s Style) paint(r role, x string) string { return s.sgr(s.code(r), x) }

// Bold is for what the reader should see first: headings, the box name.
func (s Style) Bold(x string) string { return s.sgr("1", x) }

// Dim is for detail: paths, timings, hints.
func (s Style) Dim(x string) string { return s.paint(roleDim, x) }

// Faint is for rules and borders, quieter than Dim.
func (s Style) Faint(x string) string { return s.paint(roleFaint, x) }

// Accent is brass: the selected choice, a default, the command to run next.
func (s Style) Accent(x string) string { return s.paint(roleAccent, x) }

// Cmd is a command the user can type.
func (s Style) Cmd(x string) string { return s.Accent(x) }

// Good is signal green, for what is done or works.
func (s Style) Good(x string) string { return s.paint(roleOK, x) }

// Warn is for what needs a look.
func (s Style) Warn(x string) string { return s.paint(roleWarn, x) }

// Bad is flare, for what failed.
func (s Style) Bad(x string) string { return s.paint(roleErr, x) }

// Info is sounding blue, for links and the banner's water.
func (s Style) Info(x string) string { return s.paint(roleInfo, x) }

// BoldAccent is a heading in brass.
func (s Style) BoldAccent(x string) string { return s.sgr("1;"+s.code(roleAccent), x) }
