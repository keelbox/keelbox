package ui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/golden/name, or rewrites it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/ui -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file:\n%s\nwant:\n%s", name, got, want)
	}
}

// goldenStyles are the outputs a rendering is checked in: plain text (a
// pipe), a terminal with NO_COLOR, and a 256 colour terminal.
func goldenStyles(width int) map[string]Style {
	return map[string]Style{
		"plain":    {},
		"nocolour": {Live: true, UTF8: true, Width: width},
		"colour":   {Live: true, Colour: true, UTF8: true, Depth: Depth256, Width: width},
	}
}

var goldenWidths = []int{60, 80, 120}

func goAheadCard(st Style) Card {
	return Card{Title: "About to set up acme", KeyWidth: 10, Lines: []Line{
		Row("where", "a new Hetzner server, cx23 in fsn1, ubuntu-24.04"),
		Row("cost", st.Bold("EUR 3.79 a month")+" or EUR 0.0060 an hour with VAT, billed by Hetzner until "+st.Cmd("keelbox destroy acme")),
		Row("agents", "claude-code, codex"),
		Row("access", "Tailscale; SSH from outside the tailnet, your LAN included, closes once keelbox reaches the box over it"),
		Row("", st.Dim("(a value carried on under the row before)")),
		Blank(),
		Text(st.Dim("keelbox hardens the server: no SSH passwords or root login, a firewall, an admin and an agent account, the image's default account locked.")),
	}}
}

func TestGoldenRenderings(t *testing.T) {
	for _, w := range goldenWidths {
		for name, st := range goldenStyles(w) {
			var b strings.Builder
			b.WriteString(st.Banner("0.1.0", Tagline))
			b.WriteString(st.Step(2, 7, "Hetzner token"))
			b.WriteString(st.Card(goAheadCard(st)))
			b.WriteString(st.Section("Access"))
			b.WriteString(st.Problem(Failure, "the last apply did not finish; fix the cause above, then: keelbox up acme"))
			b.WriteString(st.Problem(Stopped, "stopped between two steps, so nothing is half-done. Continue: keelbox up acme"))
			b.WriteString(st.Problem(NeedsYou, "Claude Code Remote Control does not stay running; keelbox doctor says why: keelbox doctor acme"))
			b.WriteString(st.Table([]string{"BOX", "STATUS"}, [][]string{{st.Bold("acme"), st.Tick() + " ready"}, {"lab-server", "waiting"}}))
			got := b.String()
			golden(t, fmt.Sprintf("render-%d-%s.txt", w, name), got)
			if st.Live {
				for _, l := range strings.Split(got, "\n") {
					if Width(l) >= w {
						t.Errorf("%d %s: a line is %d columns: %q", w, name, Width(l), l)
					}
				}
			}
			if !st.Colour && strings.Contains(got, "\x1b[") {
				t.Errorf("%d %s: colour without Colour", w, name)
			}
		}
	}
}

// The banner's mark is the sailboat, glyph for glyph, with each part in its
// role's colour; a narrow terminal keeps the one-line mark.
func TestBannerBoat(t *testing.T) {
	want := "       │▙\n" +
		"      ▟│█▙         keelbox  0.1.0\n" +
		"     ▟█│██▙        " + Tagline + "\n" +
		"   ▜█████████▛     Charted at https://keelbox.dev\n" +
		"  ╌╌╌╌╌╌╌╌╌╌╌╌╌\n"
	st := Style{Live: true, UTF8: true, Width: 100}
	if got := st.Banner("0.1.0", Tagline); got != want {
		t.Errorf("banner:\n%s\nwant:\n%s", got, want)
	}
	st.Colour, st.Depth = true, Depth256
	got := st.Banner("0.1.0", Tagline)
	if Strip(got) != want {
		t.Errorf("the colours change the banner's text:\n%s", Strip(got))
	}
	for part, styled := range map[string]string{
		"mast":  st.Faint("│"),
		"sail":  st.Dim("▟█") + st.Faint("│") + st.Dim("██▙"),
		"hull":  st.Accent("▜█████████▛"),
		"water": st.Info("╌╌╌╌╌╌╌╌╌╌╌╌╌"),
	} {
		if !strings.Contains(got, styled) {
			t.Errorf("the %s is not in its colour: %q", part, got)
		}
	}
	st.Width = 79
	if got := Strip(st.Banner("0.1.0", Tagline)); !strings.HasPrefix(got, "✦ keelbox 0.1.0") || strings.Count(got, "\n") != 1 {
		t.Errorf("narrow banner: %q", got)
	}
}

// Every line of a card fits every width from 60 to 200 columns.
func TestCardFitsEveryWidth(t *testing.T) {
	for w := 40; w <= 200; w++ {
		st := Style{Live: true, UTF8: true, Colour: true, Width: w}
		for _, l := range strings.Split(st.Card(goAheadCard(st)), "\n") {
			if Width(l) >= w {
				t.Fatalf("%d columns: a card line is %d: %q", w, Width(l), l)
			}
		}
	}
}

// The plain card is the old summary: the title with a colon, the rows
// indented under it.
func TestCardPlain(t *testing.T) {
	got := Style{}.Card(Card{Title: "About to set up acme", KeyWidth: 10, Lines: []Line{Row("where", "here"), Row("", "more")}})
	if got != "About to set up acme:\n  where       here\n              more\n" {
		t.Errorf("plain card: %q", got)
	}
}

// A card with MinValue puts each key over its value where the values would
// get narrower than that beside the keys, and nowhere else.
func TestCardStacksNarrowValues(t *testing.T) {
	c := Card{Title: "Start", KeyWidth: 8, MinValue: 25, Lines: []Line{
		Row("Docs", "read the guide first:"), Row("", "https://example.com/guide"), Blank(), Row("Check", "run it")}}
	beside := "╭─ Start ─────────────────────────────╮\n" +
		"│ Docs      read the guide first:     │\n" +
		"│           https://example.com/guide │\n" +
		"│                                     │\n" +
		"│ Check     run it                    │\n" +
		"╰─────────────────────────────────────╯\n"
	if got := (Style{Live: true, UTF8: true, Width: 40}).Card(c); got != beside {
		t.Errorf("40 columns:\n%s\nwant:\n%s", got, beside)
	}
	stacked := "╭─ Start ─────────────────────╮\n" +
		"│ Docs                        │\n" +
		"│   read the guide first:     │\n" +
		"│   https://example.com/guide │\n" +
		"│                             │\n" +
		"│ Check                       │\n" +
		"│   run it                    │\n" +
		"╰─────────────────────────────╯\n"
	if got := (Style{Live: true, UTF8: true, Width: 39}).Card(c); got != stacked {
		t.Errorf("39 columns:\n%s\nwant:\n%s", got, stacked)
	}
	c.MinValue = 0
	if got := (Style{Live: true, UTF8: true, Width: 39}).Card(c); !strings.Contains(got, "│ Docs      ") {
		t.Errorf("without MinValue the keys stay beside the values:\n%s", got)
	}
	if got := (Style{}).Card(Card{Title: "Start", KeyWidth: 8, MinValue: 24, Lines: []Line{Row("Docs", "read")}}); got != "Start:\n  Docs      read\n" {
		t.Errorf("plain text never stacks: %q", got)
	}
}

// A word wider than the value column, a URL on a narrow terminal, is not
// broken and does not push the border out: it gets a line of its own from
// the card's edge, styled or not.
func TestCardKeepsALongWordWhole(t *testing.T) {
	const url = "https://keelbox.dev/docs/guides/public-ssh"
	for name, st := range map[string]Style{
		"plain":  {Live: true, UTF8: true, Width: 60},
		"colour": {Live: true, UTF8: true, Colour: true, Links: true, Width: 60},
		"ascii":  {Live: true, Width: 60},
	} {
		got := st.Card(Card{Title: "Park acme", Lines: []Line{
			Row("check first", "the way in is then the cloud's rescue console ("+st.URL(url)+"). Check the key's expiry first"),
			Row("come back", "keelbox resume acme"),
		}})
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		whole := false
		for _, l := range lines {
			if Width(l) >= 60 || st.UTF8 && Width(l) != Width(lines[0]) {
				t.Errorf("%s: a line is %d columns, the first %d: %q", name, Width(l), Width(lines[0]), Strip(l))
			}
			whole = whole || strings.Contains(Strip(l), "("+url+").")
		}
		if !whole {
			t.Errorf("%s: the link is broken:\n%s", name, Strip(got))
		}
		if !strings.Contains(Strip(got), " console") || !strings.Contains(Strip(got), "  Check the key's expiry first") {
			t.Errorf("%s: the text around the link left its column:\n%s", name, Strip(got))
		}
	}
	// Where the value column holds it, it stays in the column.
	st := Style{Live: true, UTF8: true, Width: 80}
	got := st.Card(Card{Lines: []Line{Row("check first", "the way in is then the cloud's rescue console ("+url+"). Check the key's expiry first")}})
	if !strings.Contains(got, "\n│              ("+url+"). Check the") {
		t.Errorf("80 columns:\n%s", got)
	}
}

// Unbroken words stay on one line, written with plain spaces.
func TestUnbroken(t *testing.T) {
	text := "4 GB, 80 GB disk, " + Unbroken("EUR 7.49/month") + " (" + Unbroken("EUR 0.0120/h") + ")"
	for n := 16; n <= 60; n++ {
		lines := Wrap(text, n)
		for _, l := range lines {
			if strings.HasSuffix(l, "EUR") || strings.ContainsRune(l, noBreak) || Width(l) > n {
				t.Fatalf("%d columns: %q", n, lines)
			}
		}
		if strings.Join(lines, " ") != "4 GB, 80 GB disk, EUR 7.49/month (EUR 0.0120/h)" {
			t.Fatalf("%d columns: %q", n, lines)
		}
	}
	for _, st := range []Style{{}, {Live: true, UTF8: true, Width: 40}} {
		if got := st.Card(Card{Lines: []Line{Row("to", text), Text(text)}}); strings.ContainsRune(got, noBreak) || strings.Contains(got, "EUR\n") || strings.Contains(got, "EUR │") {
			t.Errorf("card: %q", got)
		}
	}
}

func TestWrapKeepsWordsAndStyle(t *testing.T) {
	st := Style{Colour: true}
	got := Wrap("a "+st.Bold("bold phrase that wraps")+" end", 12)
	if len(got) < 2 || Strip(strings.Join(got, " ")) != "a bold phrase that wraps end" {
		t.Fatalf("wrap: %q", got)
	}
	for _, l := range got {
		if strings.Count(l, "\x1b[1m") != strings.Count(l, "\x1b[0m") {
			t.Errorf("style not closed on its line: %q", l)
		}
	}
	for _, l := range Wrap("fine-grained token keeps your other repositories out of reach", 20) {
		if strings.HasPrefix(l, "ken") || Width(l) > 20 {
			t.Errorf("broken mid-word or too wide: %q", l)
		}
	}
}

func TestNearest256(t *testing.T) {
	for c, want := range map[rgb]int{hex(0xe0a458): 179, hex(0x6ee7b7): 79, hex(0x000000): 16, hex(0x808080): 244} {
		if got := nearest256(c); got != want {
			t.Errorf("nearest256(%v) = %d, want %d", c, got, want)
		}
	}
}

func TestReportDoctor(t *testing.T) {
	var b strings.Builder
	st := Style{Live: true, UTF8: true}
	r := NewReport(&b, st, false)
	_, _ = r.Write([]byte("keelbox doctor: acme (release 2026.11.3, Ubuntu 24.04 amd64)\n\nhost\n  ok    packages                 installed\n  warn  disk                     81% used\n  FAIL  backup                   repository locked\nsomething else\n2 ok, 1 warning, 1 failure.\n"))
	r.Close()
	want := "Doctor · acme release 2026.11.3, Ubuntu 24.04 amd64\n\nhost\n  ✓  packages                 installed\n  !  disk                     81% used\n  ✗  backup                   repository locked\nsomething else\n✓ 2 ok · ! 1 warning · ✗ 1 failure\n"
	if b.String() != want {
		t.Errorf("doctor report:\n%s\nwant:\n%s", b.String(), want)
	}
}

// A long detail wraps under its own column, on a narrow terminal under the
// check's name, and a count of none carries no mark.
func TestReportWrapsLongDetails(t *testing.T) {
	in := "  warn  claude-remote-control    needs you: signed in but not running. Answer both questions: keelbox login acme claude-code\n1 ok, 1 warning, 0 failures.\n"
	for width, want := range map[int]string{
		80: "  !  claude-remote-control    needs you: signed in but not running. Answer both\n" +
			"                              questions: keelbox login acme claude-code\n" +
			"✓ 1 ok · ! 1 warning · 0 failures\n",
		50: "  !  claude-remote-control\n" +
			"      needs you: signed in but not running.\n" +
			"      Answer both questions: keelbox login acme\n" +
			"      claude-code\n" +
			"✓ 1 ok · ! 1 warning · 0 failures\n",
	} {
		var b strings.Builder
		r := NewReport(&b, Style{Live: true, UTF8: true, Width: width}, false)
		_, _ = r.Write([]byte(in))
		r.Close()
		if b.String() != want {
			t.Errorf("width %d:\n%s\nwant:\n%s", width, b.String(), want)
		}
	}
}

// Plain text on a terminal wraps to its width, hanging under itself, at
// every width; where the width is not known it is one line.
func TestPlainWrapsToTheTerminal(t *testing.T) {
	text := "keelbox: no answer over the tailnet at devbox.example.ts.net (dial tcp: i/o timeout); trying the public address 203.0.113.7"
	if got := (Style{}).Plain(text, 2); got != text {
		t.Errorf("off a terminal: %q", got)
	}
	for _, w := range []int{60, 80, 100, 120} {
		got := Style{Width: w}.Plain(text, 2)
		lines := strings.Split(got, "\n")
		if w < len(text) && len(lines) < 2 {
			t.Errorf("%d: not wrapped: %q", w, got)
		}
		for i, l := range lines {
			if Width(l) >= w {
				t.Errorf("%d: a line is %d columns: %q", w, Width(l), l)
			}
			if i > 0 && !strings.HasPrefix(l, "  ") {
				t.Errorf("%d: a line after the first does not hang: %q", w, l)
			}
		}
		if strings.Join(strings.Fields(got), " ") != text {
			t.Errorf("%d: the words changed: %q", w, got)
		}
	}
	// A card in plain text wraps as well, its rows under their values.
	for _, w := range []int{60, 80, 100, 120} {
		st := Style{Width: w}
		for _, l := range strings.Split(st.Card(goAheadCard(st)), "\n") {
			if Width(l) >= w {
				t.Errorf("%d: a card line is %d columns: %q", w, Width(l), l)
			}
		}
	}
}

// The rules of a step and of a section end where the cards do, so a page
// has one right edge on a terminal wider than a card.
func TestRulesAndCardsShareOneWidth(t *testing.T) {
	for _, w := range []int{100, 120, 200} {
		st := Style{Live: true, UTF8: true, Width: w}
		card := strings.Split(st.Card(goAheadCard(st)), "\n")[0]
		step := strings.Trim(st.Step(2, 7, "Hetzner token"), "\n")
		section := strings.Trim(st.Section("Access"), "\n")
		if Width(card) != MaxWidth || Width(step) != MaxWidth || Width(section) != MaxWidth {
			t.Errorf("%d columns: the card is %d wide, the step rule %d, the section rule %d; want %d each", w, Width(card), Width(step), Width(section), MaxWidth)
		}
	}
	// Narrower than a card, each takes the terminal's width.
	st := Style{Live: true, UTF8: true, Width: 60}
	if step := strings.Trim(st.Step(2, 7, "Hetzner token"), "\n"); Width(step) != st.Cols() {
		t.Errorf("60 columns: the step rule is %d wide", Width(step))
	}
}
