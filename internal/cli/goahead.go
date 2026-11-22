package cli

import (
	"io"
	"regexp"

	"github.com/keelbox/keelbox/internal/ui"
)

// What keelbox resize and keelbox park show before their question: the
// card keelbox up shows before its go-ahead (ui.Card), with one labelled
// row per thing to know, and with --yes the same sentences as notes.

// showGoAhead prints the card a command's question follows: boxed and
// fitted to the width on a capable terminal, and otherwise the title and
// the rows under it as plain lines. A person reads it either way, so where
// the width is not known (TERM=dumb, a screen reader) the lines wrap to 80
// columns instead of being left to the terminal to break.
func showGoAhead(out io.Writer, st ui.Style, title string, rows []ui.Line) {
	if st.Width == 0 {
		st.Width = st.Cols() + 1
	}
	_, _ = io.WriteString(out, st.Card(ui.Card{Title: title, Lines: rows, Fill: true}))
}

// sayNote says, with --yes, a sentence the card would have shown: one line
// where the output is not a terminal, as every line of a log is, and
// wrapped under itself on one. logf is the command's, which starts each
// line with "keelbox: ".
func sayNote(logf func(string, ...any), st ui.Style, text string) {
	text = "note: " + text
	if st.Live {
		const lead = len("keelbox: ")
		text = ui.Hang(text, st.Cols()-lead, lead)
	}
	logf("%s", text)
}

var (
	// A figure that reads as one word in a cloud's wording: an amount of
	// money, "USD 0.06" or "EUR 7.49", and a count with its unit, "40 GB"
	// or "2 vCPU".
	moneyRe = regexp.MustCompile(`\b(?:USD|EUR) \d|\b\d+(?:\.\d+)? (?:GB|TB|vCPU)\b`)
	// What a month costs, after ui.Unbroken: "EUR 3.79 a month",
	// "EUR 7.49/month".
	monthlyRe = regexp.MustCompile(`\b(?:USD|EUR)\x{a0}\d+(?:\.\d+)?(?: a month|/month)`)
	// A URL, without the bracket or the full stop that follows it.
	urlRe = regexp.MustCompile(`https://[^\s)]*[^\s).,;:]`)
)

// amounts is a cloud's wording for text that is wrapped, a card or a line
// on a terminal, with no line broken between a currency and its number or
// between a count and its unit.
func amounts(text string) string {
	return moneyRe.ReplaceAllStringFunc(text, ui.Unbroken)
}

// billed is amounts with what a month costs in bold, the first such
// amount where there are several: it is what a card's reader looks for.
func billed(st ui.Style, text string) string {
	text = amounts(text)
	loc := monthlyRe.FindStringIndex(text)
	if loc == nil {
		return text
	}
	return text[:loc[0]] + st.Bold(text[loc[0]:loc[1]]) + text[loc[1]:]
}

// linked is text with each URL in it as a link.
func linked(st ui.Style, text string) string {
	return urlRe.ReplaceAllStringFunc(text, st.URL)
}

// sayInfo says in one line how a command found the box when there is
// nothing for it to do: "keelbox: ..." in a log, and on a terminal a line
// under a bullet, wrapped to the width with no amount broken in two.
func sayInfo(out io.Writer, st ui.Style, text string) {
	if st.Live {
		text = amounts(text)
	}
	sayAsIs(out, st, st.Faint(st.Bullet()), text)
}

// command is a command to run, in a card: in the accent colour, and whole
// on one line, so that it can be copied, however long the box's name or the
// path of its config is.
func command(st ui.Style, text string) string { return st.Cmd(ui.Unbroken(text)) }
