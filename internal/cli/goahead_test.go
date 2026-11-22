package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/ui"
)

// flat is what a command printed as one line of words, without styling
// and without a card's border: a sentence reads the same in it however
// the card wrapped it, and a word broken in two does not.
func flat(out string) string {
	var words []string
	for _, l := range strings.Split(ui.Strip(out), "\n") {
		words = append(words, strings.Fields(strings.Trim(l, "│╭╮╰╯─ "))...)
	}
	return strings.Join(words, " ")
}

// drawnCard is the boxed card in what a command printed, border included.
func drawnCard(t *testing.T, out string) []string {
	t.Helper()
	var card []string
	for _, l := range strings.Split(out, "\n") {
		switch p := ui.Strip(l); {
		case strings.HasPrefix(p, "╭"), strings.HasPrefix(p, "│"), strings.HasPrefix(p, "╰"):
			card = append(card, l)
		}
	}
	if len(card) < 3 {
		t.Fatalf("no card in:\n%s", out)
	}
	return card
}

// goAheadWorlds are the four clouds, each with the box acme on it and the
// size the resize in these tests asks for.
var goAheadWorlds = []struct {
	cloud, size string
	world       func(t *testing.T) func(t *testing.T, cmd string, o options, input *string) (int, string)
}{
	{"hetzner", "cpx22", func(t *testing.T) func(*testing.T, string, options, *string) (int, string) { return newParkWorld(t).do }},
	{"aws", "medium_3_0", func(t *testing.T) func(*testing.T, string, options, *string) (int, string) { return newAWSWorld(t).do }},
	{"digitalocean", "s-4vcpu-8gb", func(t *testing.T) func(*testing.T, string, options, *string) (int, string) { return newDOWorld(t).do }},
	{"gcp", "e2-standard-2", func(t *testing.T) func(*testing.T, string, options, *string) (int, string) { return newGCPWorld(t).do }},
}

// The card before park's and resize's question fits the terminal on every
// cloud: no line reaches the width, the border is straight, and it holds
// the words of the plain card, none lost and none broken in two.
func TestGoAheadCardsFitTheTerminal(t *testing.T) {
	for _, c := range goAheadWorlds {
		for _, tailnetOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, public SSH closed %v", c.cloud, tailnetOnly), func(t *testing.T) {
				do := c.world(t)
				// The worlds put their own stand-in back when the test ends.
				publicSSHClosed = func(context.Context, *config.Config, *cloudServer) bool { return tailnetOnly }
				for cmd, o := range map[string]options{"park": {}, "resize": {size: c.size}} {
					_, plain := do(t, cmd, o, str("\n"))
					want := strings.Replace(flat(plain), ": ", " ", 1) // the plain title ends with a colon
					if tailnetOnly != strings.Contains(plain, "check first") && cmd == "park" {
						t.Errorf("%s: the row about the tailnet:\n%s", cmd, plain)
					}
					for _, width := range []int{60, 80, 120} {
						o.style = ui.Style{Live: true, UTF8: true, Colour: true, Links: true, Depth: ui.DepthTrue, Width: width}
						code, out := do(t, cmd, o, str("\n"))
						if code != 1 {
							t.Fatalf("%s at %d columns: exit %d\n%s", cmd, width, code, out)
						}
						card := drawnCard(t, out)
						for _, l := range card {
							if w := ui.Width(l); w != min(width-1, stepWidth) || w != ui.Width(card[0]) {
								t.Errorf("%s at %d columns: a card line is %d wide, the first %d: %q", cmd, width, w, ui.Width(card[0]), ui.Strip(l))
							}
							if end := strings.TrimRight(ui.Strip(l), " │"); strings.HasSuffix(end, "USD") || strings.HasSuffix(end, "EUR") {
								t.Errorf("%s at %d columns: an amount is broken after its currency: %q", cmd, width, ui.Strip(l))
							}
						}
						if got := flat(out); got != want {
							t.Errorf("%s at %d columns says\n%s\nand the plain card\n%s\n%s", cmd, width, got, want, ui.Strip(out))
						}
					}
				}
			})
		}
	}
}

// What a reader looks for stands out: the amount billed in bold, the
// command to come back in the accent colour, the row to check first in the
// warning colour, and the links as links.
func TestGoAheadCardsMarkTheAmountAndTheCommand(t *testing.T) {
	w := newParkWorld(t)
	w.tailnetOnly = true
	st := ui.Style{Live: true, UTF8: true, Colour: true, Links: true, Depth: ui.DepthTrue, Width: 120}
	_, out := w.do(t, "park", options{style: st}, str("\n"))
	wantAll(t, "park", out, st.Bold("EUR 3.79 a month"), st.Bold("EUR 0.57 a month"), st.Cmd("keelbox resume acme"), st.Warn("check first"),
		"("+st.URL("https://keelbox.dev/docs/guides/public-ssh")+").")
	_, out = w.do(t, "resize", options{style: st, size: "cpx22"}, str("\n"))
	wantAll(t, "resize", out, st.Bold("EUR 7.49/month"), st.Cmd("keelbox resize acme --size cx23"))
	if strings.Contains(out, st.Bold("EUR 3.79/month")) {
		t.Errorf("the price of the size the box leaves is in bold:\n%s", out)
	}
	// On Lightsail the row about the instance being replaced is the warning.
	aws := newAWSWorld(t)
	_, out = aws.do(t, "resize", options{style: st, size: "medium_3_0"}, str("\n"))
	wantAll(t, "resize on AWS", out, st.Warn("warning"), st.Bold("USD 24.00/month"))
	// A cloud that gives no price has nothing in bold, and its links are links.
	gcp := newGCPWorld(t)
	_, out = gcp.do(t, "park", options{style: st}, str("\n"))
	wantAll(t, "park on Google Cloud", out, st.URL("https://cloud.google.com/compute/disks-image-pricing"), st.URL("https://cloud.google.com/compute/vm-instance-pricing"))
	if strings.Contains(out, "\x1b[1m") {
		t.Errorf("something is in bold without an amount:\n%s", out)
	}
}

func TestBilledAndLinked(t *testing.T) {
	st := ui.Style{Live: true, Colour: true, Links: true}
	for in, want := range map[string]string{
		"the snapshot: USD 0.06 per GB a month for what the disk holds, so up to about USD 4.80 a month for its 80 GB": "the snapshot: USD\u00a00.06 per GB a month for what the disk holds, so up to about " + st.Bold("USD\u00a04.80 a month") + " for its 80\u00a0GB",
		"the server's price, about EUR 3.79 a month (EUR 0.14 a day)":                                                  "the server's price, about " + st.Bold("EUR\u00a03.79 a month") + " (EUR\u00a00.14 a day)",
		"2 vCPU (shared), 4 GB, 160 GB disk, 5 TB transfer, EUR 7.49/month":                                            "2\u00a0vCPU (shared), 4\u00a0GB, 160\u00a0GB disk, 5\u00a0TB transfer, " + st.Bold("EUR\u00a07.49/month"),
		"the snapshot, at Hetzner's price per GB a month for what the disk holds":                                      "the snapshot, at Hetzner's price per GB a month for what the disk holds",
		"its address and everything on its disk":                                                                       "its address and everything on its disk",
	} {
		if got := billed(st, in); got != want {
			t.Errorf("billed(%q) = %q", in, got)
		}
		if got := amounts(in); got != ui.Strip(want) {
			t.Errorf("amounts(%q) = %q", in, got)
		}
	}
	const u = "https://keelbox.dev/docs/guides/public-ssh"
	for in, want := range map[string]string{
		"console (" + u + "). Check": "console (" + st.URL(u) + "). Check",
		"are at " + u + ", billed":   "are at " + st.URL(u) + ", billed",
		"see " + u:                   "see " + st.URL(u),
		"no link here":               "no link here",
	} {
		if got := linked(st, in); got != want {
			t.Errorf("linked(%q) = %q", in, got)
		}
	}
	if got := linked(ui.Style{}, "see "+u+"."); got != "see "+u+"." {
		t.Errorf("a link in plain text: %q", got)
	}
}

// With --yes a note is one line where the output is not a terminal, and
// wrapped under itself on one.
func TestSayNote(t *testing.T) {
	const text = "the snapshot holds everything on the disk, credentials and the agent's sign-ins included: whoever can read snapshots in this cloud account can read them"
	var out strings.Builder
	logf := func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) }
	sayNote(logf, ui.Style{}, text)
	if out.String() != "keelbox: note: "+text+"\n" {
		t.Errorf("off a terminal: %q", out.String())
	}
	for _, width := range []int{60, 80, 120} {
		out.Reset()
		sayNote(logf, ui.Style{Live: true, Width: width}, text)
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		for i, l := range lines {
			if ui.Width(l) >= width || i > 0 && !strings.HasPrefix(l, "         ") {
				t.Errorf("%d columns: line %d is %d wide: %q", width, i, ui.Width(l), l)
			}
		}
		if len(lines) < 2 || flat(out.String()) != "keelbox: note: "+text {
			t.Errorf("%d columns: %q", width, out.String())
		}
	}
}
