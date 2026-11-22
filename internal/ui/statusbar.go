package ui

import "fmt"

// StatusBar is the Night Chart as tmux colours ("#rrggbb"), for the status
// line keelbox gives the agent's sessions. The bar brings its own dark
// ground, so it reads the same on a light terminal theme. tmux sends these
// as they are to a terminal with truecolor and picks the nearest of the 256
// xterm colours itself on one without.
type StatusBar struct {
	Ground string // the bar's background
	Name   string // brass: the box name
	Text   string // mist: the hint, the other windows
	Here   string // sounding blue: the window you are in
	Rule   string // rope: separators and pane borders
}

// barGround is a dark slate under the Night Chart: darker than every colour
// drawn on it and a quiet grey (235) in 256 colours.
var barGround = hex(0x1b2330)

func (c rgb) tmux() string { return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b) }

// NightStatusBar is the status line's colours.
func NightStatusBar() StatusBar {
	return StatusBar{
		Ground: barGround.tmux(),
		Name:   night[roleAccent].tmux(),
		Text:   night[roleDim].tmux(),
		Here:   night[roleInfo].tmux(),
		Rule:   night[roleFaint].tmux(),
	}
}
