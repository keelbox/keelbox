package ui

import (
	"strings"
)

// Tagline says what keelbox is, in one sentence, wherever it says so: under
// the wordmark, on the plain start screen and at the top of keelbox help.
const Tagline = "Sets up Ubuntu machines for AI coding agents."

// Banner is the mark keelbox up and the start screen open with: a small
// sailboat beside the wordmark, the version and the tagline. Under 80
// columns it is one line; off a terminal, without UTF-8 or for a screen
// reader it is nothing, and the caller prints its plain first line instead.
func (s Style) Banner(version, tagline string) string {
	return s.banner(version, tagline)
}

func (s Style) banner(version, tagline string) string {
	if !s.Live || !s.UTF8 || s.Accessible {
		return ""
	}
	cols := s.Cols()
	if cols < 79 {
		line := s.Accent("✦") + " " + s.Bold("keelbox") + " " + s.Dim(version)
		if Width(line)+3+Width(tagline) <= cols {
			line += s.Sep() + s.Dim(tagline)
		}
		return line + "\n"
	}
	return s.bannerBoat(version, tagline)
}

// boatWidth is the columns the sailboat takes, its margin included.
const boatWidth = 15

// bannerBoat is a five-line sailboat: the mast in rope, the sails in mist,
// the hull in brass, the water in sounding blue. Block and box-drawing
// glyphs only, which no terminal draws as emoji.
func (s Style) bannerBoat(version, tagline string) string {
	mast, sail := s.Faint("│"), s.Dim
	boat := []string{
		"       " + mast + sail("▙"),
		"      " + sail("▟") + mast + sail("█▙"),
		"     " + sail("▟█") + mast + sail("██▙"),
		"   " + s.Accent("▜█████████▛"),
		"  " + s.Info("╌╌╌╌╌╌╌╌╌╌╌╌╌"),
	}
	text := []string{
		"",
		s.Bold("keelbox") + "  " + s.Dim(version),
		s.Dim(tagline),
		s.Faint("Charted at ") + s.URL("https://keelbox.dev"),
		"",
	}
	var out strings.Builder
	for i := range boat {
		out.WriteString(Pad(boat[i], boatWidth) + "    " + text[i])
		out.WriteString("\n")
	}
	return trimLines(out.String())
}

func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}
