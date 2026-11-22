package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/ui"
)

// On a terminal that takes plain text only (TERM=dumb, a screen reader)
// what keelbox says wraps to the width; off a terminal each message stays
// one line, as a log has it.
func TestPlainOutputWrapsOnATerminal(t *testing.T) {
	const hint = "The server's SSH host key fingerprint lets keelbox check it is talking to your server. Find it in the provider's console, or run ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub there."
	write := func(st ui.Style) string {
		var b bytes.Buffer
		w := &wizard{st: st, out: &b}
		w.hint(hint)
		w.logf("your SSH key is already in the Hetzner project as %s, which the project keeps for the next server keelbox creates there", "keelbox-laptop")
		w.ok("203.0.113.7 answers as root, with sudo; Ubuntu 24.04, 4 GB of memory, 2 CPUs and 38 GB free on its disk, which is enough")
		say(&b, st, "", "public SSH stays open: the box is not on the tailnet yet. keelbox up closes it after the Tailscale sign-in.")
		notef(&b, "no answer over the tailnet at %s (%v); trying the public address %s", "devbox.example.ts.net", "dial tcp: i/o timeout", "203.0.113.7")
		return b.String()
	}
	for _, w := range []int{60, 80, 100, 120} {
		st := ui.Style{Width: w}
		stderrAs(t, st)
		got := write(st)
		for _, l := range strings.Split(got, "\n") {
			if ui.Width(l) >= w {
				t.Errorf("%d: a line is %d columns: %q", w, ui.Width(l), l)
			}
		}
		if !strings.Contains(strings.Join(strings.Fields(got), " "), hint) {
			t.Errorf("%d: the hint's words changed:\n%s", w, got)
		}
	}
	stderrAs(t, ui.Style{})
	if got := write(ui.Style{}); strings.Count(got, "\n") != 5 {
		t.Errorf("off a terminal, want five lines:\n%s", got)
	}
}

// A command in a note and an amount in a hint are not broken across two
// lines where the text wraps, and are written with plain spaces.
func TestNotesKeepCommandsAndAmountsWhole(t *testing.T) {
	for _, st := range []ui.Style{{Live: true, UTF8: true, Width: 100}, {Width: 100}, {}} {
		var b bytes.Buffer
		w := &wizard{st: st, out: &b}
		w.hint("Prices from the Hetzner API for fsn1, with VAT; billed by the hour until the server is deleted. " + sizeHint)
		say(&b, st, st.Tick(), "token works (0 servers in the project); stored as \"hetzner\" in the macOS Keychain. Remove it: "+st.Cmd(ui.Unbroken("keelbox forget-token")))
		got := b.String()
		if strings.ContainsRune(got, '\u00a0') {
			t.Errorf("%+v: a no-break space is written out:\n%q", st, got)
		}
		for _, whole := range []string{"2 vCPU", "4 GB", "keelbox forget-token"} {
			if !strings.Contains(got, whole) {
				t.Errorf("%+v: %q is broken across lines:\n%s", st, whole, got)
			}
		}
	}
}
