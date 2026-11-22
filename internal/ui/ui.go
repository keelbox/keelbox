// Package ui styles keelbox's terminal output: the brand palette, marks,
// headings, cards and the banner, and a live progress view for apply. It
// decides from the terminal and the environment (Detect) and writes plain
// text, exactly as readable, when the output is not a terminal, TERM is
// dumb or the no-color.org variable is set. Output that goes to a file, a pipe or an agent
// stays plain text, one line per event. Meaning never rests on colour
// alone: every coloured mark is also a glyph or a word.
package ui

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/term"
)

// Style is what a writer can take: Colour for SGR codes, Live for cursor
// movement (rewriting the current line, menus). Live without Colour is a
// capable terminal with colour turned off: the progress view and menus
// still update in place.
type Style struct {
	Colour bool
	Live   bool
	UTF8   bool  // box drawing, marks and spinner glyphs; ASCII otherwise
	Depth  Depth // colours the terminal shows, when Colour is set
	Light  bool  // the terminal's background is light
	Links  bool  // the terminal shows OSC 8 hyperlinks
	// Width is the terminal's columns, 0 when the output is not a
	// terminal. It is known on a terminal that takes plain text only
	// (TERM=dumb, a screen reader) as well: text is wrapped to it there
	// too, and left as one line only where nobody reads it on a screen.
	Width int
	// Accessible is a screen reader at work (ACCESSIBLE set): no art,
	// no redrawn lines, questions asked one plain line at a time.
	Accessible bool
}

// Depth is how many colours a terminal shows.
type Depth int

// The depths, from the environment: COLORTERM says truecolor, TERM says
// 256, everything else gets the 16 its theme defines.
const (
	Depth16 Depth = iota
	Depth256
	DepthTrue
)

// For is the style for writing to f, from the terminal and the environment.
func For(f *os.File) Style {
	terminal := f != nil && term.IsTerminal(f)
	st := Detect(terminal, os.Getenv)
	if terminal {
		st.Width = term.Width(f)
	}
	return st
}

// Detect decides the style for a writer that is or is not a terminal:
// none off a terminal or with TERM=dumb, and no colour when the no-color.org
// variable is set, whatever its value. ACCESSIBLE (any value) asks for
// no art and no lines redrawn in place, for screen readers.
func Detect(terminal bool, getenv func(string) string) Style {
	tm := getenv("TERM")
	if !terminal || tm == "dumb" {
		return Style{}
	}
	s := Style{Live: true, Colour: getenv("NO_COLOR") == ""} //nolint:misspell // the variable's name
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			v = strings.ToLower(v)
			s.UTF8 = strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
			break
		}
	}
	if s.Colour {
		switch ct := strings.ToLower(getenv("COLORTERM")); {
		case ct == "truecolor" || ct == "24bit":
			s.Depth = DepthTrue
		case strings.Contains(tm, "256color") || strings.Contains(tm, "direct"):
			s.Depth = Depth256
		}
		s.Light = lightFromEnv(getenv("COLORFGBG"))
	}
	s.Links = hyperlinks(getenv)
	if getenv("ACCESSIBLE") != "" {
		s.Accessible, s.Live = true, false
	}
	return s
}

// lightFromEnv reads COLORFGBG, "fg;bg" in the 16 colour numbers some
// terminals set: a background of 7, or 9 to 15, is light.
func lightFromEnv(v string) bool {
	i := strings.LastIndexByte(v, ';')
	if i < 0 {
		return false
	}
	bg, err := strconv.Atoi(v[i+1:])
	return err == nil && (bg == 7 || bg >= 9 && bg <= 15)
}

// hyperlinks reports whether the terminal is one known to show OSC 8
// links; elsewhere a URL is printed as text only. FORCE_HYPERLINK
// overrides either way.
func hyperlinks(getenv func(string) string) bool {
	if v := getenv("FORCE_HYPERLINK"); v != "" {
		return v != "0"
	}
	if getenv("TMUX") != "" || getenv("STY") != "" {
		return false // multiplexers drop or mangle them unless set up to pass them
	}
	switch getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "vscode", "ghostty", "Hyper", "Tabby", "rio":
		return true
	}
	if getenv("WT_SESSION") != "" || getenv("KONSOLE_VERSION") != "" || getenv("KITTY_WINDOW_ID") != "" {
		return true
	}
	if v, err := strconv.Atoi(getenv("VTE_VERSION")); err == nil && v >= 5000 {
		return true
	}
	t := getenv("TERM")
	return t == "xterm-kitty" || t == "foot" || t == "alacritty"
}

// WithBackground asks a terminal on in and out for its background when the
// environment did not say, so the palette suits a light theme as well as a
// dark one. It costs one round trip to the terminal, and is skipped in 16
// colours, where the terminal's own theme picks the colours.
func (s Style) WithBackground(in, out *os.File) Style {
	if !s.Colour || s.Depth == Depth16 || os.Getenv("COLORFGBG") != "" || in == nil || out == nil || !term.IsTerminal(in) {
		return s
	}
	if light, ok := term.Background(in, out, 400*time.Millisecond); ok {
		s.Light = light
	}
	return s
}

// Cols is the width to lay text out in: the terminal's less one column,
// so a full line never wraps by itself, or 80 when it is not known.
func (s Style) Cols() int {
	if s.Width <= 0 {
		return 80
	}
	return max(s.Width-1, 20)
}
