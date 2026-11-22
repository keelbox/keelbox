package cli

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/ui"
)

// runToEnd runs one of the three commands to its end in a world of its
// own, with a parked box for resume, and returns what it printed.
func runToEnd(t *testing.T, cloud, cmd string, st ui.Style) string {
	t.Helper()
	for _, c := range goAheadWorlds {
		if c.cloud != cloud {
			continue
		}
		do := c.world(t)
		o, input := options{style: st}, str("y\n")
		switch cmd {
		case "resume":
			if code, out := do(t, "park", options{yes: true}, nil); code != 0 {
				t.Fatalf("%s: park: exit %d\n%s", cloud, code, out)
			}
			input = nil
		case "resize":
			o.size = c.size
		}
		code, out := do(t, cmd, o, input)
		if code != 0 {
			t.Fatalf("%s %s: exit %d\n%s", cloud, cmd, code, out)
		}
		return out
	}
	t.Fatalf("no cloud %s", cloud)
	return ""
}

// What differs between two runs of the same command: a time in a
// snapshot's name and how long the run took.
var runStamp = regexp.MustCompile(`\d{8}T\d{6}Z|\d{8}-\d{6}|\bin \d+s\b`)

// Each command ends on a headline with the time it took and a card, on
// every cloud. On a terminal the card fits at any width, with a straight
// border and the time where the card ends, and says the words of the
// plain end, none lost and none broken in two.
func TestOutcomesFitTheTerminal(t *testing.T) {
	for _, c := range goAheadWorlds {
		for _, cmd := range []string{"park", "resume", "resize"} {
			// How the end begins.
			headline := map[string]string{"park": "acme is parked", "resume": "acme is back", "resize": "acme is " + c.size + " now"}[cmd]
			plain := runToEnd(t, c.cloud, cmd, ui.Style{})
			i := strings.LastIndex(plain, "\n"+headline)
			if i < 0 {
				t.Fatalf("%s %s: no headline:\n%s", c.cloud, cmd, plain)
			}
			// The plain end: a sentence, then the title with a colon and its rows,
			// none past 80 columns.
			end := plain[i+1:]
			if !regexp.MustCompile(`^acme is [^\n]* in \d+s\.\n[A-Z][^\n]*:\n  `).MatchString(end) {
				t.Errorf("%s %s: the plain end:\n%s", c.cloud, cmd, end)
			}
			for _, l := range strings.Split(end, "\n") {
				if ui.Width(l) > 80 {
					t.Errorf("%s %s: a line of the plain end is %d columns: %q", c.cloud, cmd, ui.Width(l), l)
				}
			}
			want := runStamp.ReplaceAllString(strings.Replace(strings.Replace(flat(end), ". ", " ", 1), ": ", " ", 1), "T")
			for _, width := range []int{60, 80, 120} {
				name := fmt.Sprintf("%s %s at %d columns", c.cloud, cmd, width)
				st := ui.Style{Live: true, UTF8: true, Colour: true, Links: true, Depth: ui.DepthTrue, Width: width}
				out := runToEnd(t, c.cloud, cmd, st)
				j := strings.LastIndex(out, st.Bold(headline))
				if j < 0 {
					t.Fatalf("%s: no headline in bold:\n%s", name, out)
				}
				end := out[strings.LastIndex(out[:j], "\n")+1:]
				head, _, _ := strings.Cut(end, "\n")
				card := drawnCard(t, end)
				for _, l := range card {
					if w := ui.Width(l); w != min(width-1, stepWidth) || w != ui.Width(card[0]) {
						t.Errorf("%s: a card line is %d wide, the first %d: %q", name, w, ui.Width(card[0]), ui.Strip(l))
					}
					if last := strings.TrimRight(ui.Strip(l), " │"); strings.HasSuffix(last, "USD") || strings.HasSuffix(last, "EUR") || regexp.MustCompile(` \d+$`).MatchString(last) && strings.Contains(l, "GB") {
						t.Errorf("%s: an amount is broken from its currency or its unit: %q", name, ui.Strip(l))
					}
				}
				// The headline: a tick, and the time at the card's right edge
				// or, where there is no room beside it, under it.
				if h := ui.Strip(head); !strings.HasPrefix(h, "  ✓ acme is ") || ui.Width(head) > ui.Width(card[0]) ||
					regexp.MustCompile(` in \d+s$`).MatchString(h) && ui.Width(head) != ui.Width(card[0]) {
					t.Errorf("%s: the headline %q beside a card %d wide", name, h, ui.Width(card[0]))
				}
				if got := runStamp.ReplaceAllString(strings.Replace(flat(end), "✓ ", "", 1), "T"); got != want {
					t.Errorf("%s says\n%s\nand the plain end\n%s\n%s", name, got, want, ui.Strip(end))
				}
			}
		}
	}
}

// What a reader looks for at the end stands out: the amount in bold and
// the commands in the accent colour.
func TestOutcomesMarkTheAmountAndTheCommands(t *testing.T) {
	st := ui.Style{Live: true, UTF8: true, Colour: true, Links: true, Depth: ui.DepthTrue, Width: 120}
	out := runToEnd(t, "hetzner", "park", st)
	wantAll(t, "park", out, st.Tick()+" "+st.Bold("acme is parked"), st.Bold("EUR 0.04 a month"), st.Cmd("keelbox resume acme"))
	out = runToEnd(t, "hetzner", "resume", st)
	wantAll(t, "resume", out, st.Tick()+" "+st.Bold("acme is back"), st.Cmd("keelbox attach acme"), st.Cmd("keelbox login acme tailscale"), st.Cmd("keelbox doctor acme"))
	out = runToEnd(t, "hetzner", "resize", st)
	wantAll(t, "resize", out, st.Tick()+" "+st.Bold("acme is cpx22 now"), st.Bold("EUR 7.49/month"), st.Cmd("keelbox attach acme"), st.Cmd("keelbox resize acme --size cx23"))
	if strings.Contains(out, st.Bold("EUR 3.79/month")) {
		t.Errorf("the price of the size the box left is in bold:\n%s", out)
	}
	// A cloud without prices has no amount in bold, only the headline.
	out = runToEnd(t, "gcp", "park", st)
	if i := strings.LastIndex(out, st.Bold("acme is parked")); i < 0 || strings.Contains(out[i+len(st.Bold("acme is parked")):], "\x1b[1m") {
		t.Errorf("something is in bold at the end of a park without prices:\n%s", out)
	}
}

// A box without tmux is opened with keelbox ssh, and one without
// Tailscale has no row about the tailnet; an address that did not change
// is not said to have.
func TestResumedOutcomeFollowsTheConfig(t *testing.T) {
	w := newParkWorld(t)
	srv, pk := &cloudServer{ref: "server 1002", ip: "203.0.113.7"}, &cloudParked{ref: "snapshot 1001"}
	rows := func() string {
		var b strings.Builder
		showOutcome(&b, ui.Style{}, resumedOutcome(ui.Style{}, w.cfg, "acme", 0, srv, pk, "203.0.113.7"))
		return b.String()
	}
	got := rows()
	wantAll(t, "the same address", got, "  address       203.0.113.7\n", "keelbox attach acme", "  tailnet ")
	if strings.Contains(got, "(it was") || strings.Contains(got, "DNS") {
		t.Errorf("an address that stayed is said to have changed:\n%s", got)
	}
	// A box named by the path of its config: each command stays whole on a
	// line, to be copied, wherever the card has the room for it.
	const path = "/home/sam/keelbox/boxes/acme-staging.yaml"
	for _, st := range []ui.Style{{}, termStyle(80), termStyle(120)} {
		var b strings.Builder
		showOutcome(&b, st, resumedOutcome(st, w.cfg, path, 0, srv, pk, "198.51.100.3"))
		for _, cmd := range []string{"keelbox attach " + path, "keelbox login " + path + " tailscale", "keelbox doctor " + path} {
			if !strings.Contains(b.String(), cmd) {
				t.Errorf("at %d columns %q is broken over two lines:\n%s", st.Width, cmd, b.String())
			}
		}
	}
	off := false
	w.cfg.Sessions.Tmux, w.cfg.Access.Tailscale.Enabled = &off, &off
	got = rows()
	wantAll(t, "no tmux, no Tailscale", got, "keelbox ssh acme", "opens a shell as the agent")
	if strings.Contains(got, "attach") || strings.Contains(got, "tailnet") {
		t.Errorf("a box without tmux and Tailscale:\n%s", got)
	}
}
