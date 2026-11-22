package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// keelbox up prints its banner when it has a question to ask or a step to
// run, and not above a one-line error about how it was called or about a
// box that has no server to set up.
func TestUpBannerIsNotAboveAUsageError(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	st := ui.Style{Live: true, UTF8: true, Width: 100}
	up := func(input string, args ...string) (int, string) {
		t.Helper()
		defer func(f func() bool) { localLikely = f }(localLikely)
		localLikely = func() bool { return false }
		o, pos, err := parse("up", args, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
		o.style = st
		var b bytes.Buffer
		code := cmdUp(t.Context(), o, pos, prompt.New(strings.NewReader(input), &b, true).WithStyle(st), &fakeRunner{}, &b, &b)
		return code, b.String()
	}
	const boat = "▜█████████▛"
	if code, out := up("", "My_Box"); code != 2 || strings.Contains(out, boat) || !strings.Contains(out, "box name") {
		t.Errorf("a bad name: exit %d\n%s", code, out)
	}
	if code, out := up("", "a", "b"); code != 2 || strings.Contains(out, boat) {
		t.Errorf("two names: exit %d\n%s", code, out)
	}
	register(t, config.StarterFor("delta", "hetzner"))
	_ = saveParkMark("delta", parkMark{Since: time.Now()})
	if code, out := up("", "delta"); code != 1 || strings.Contains(out, boat) || !strings.Contains(out, "delta has been parked since") {
		t.Errorf("a parked box: exit %d\n%s", code, out)
	}
	// With a question to ask, the banner comes first, once.
	if _, out := up("\n"); strings.Count(out, boat) != 1 || strings.Index(out, boat) > strings.Index(out, "Where should the box run?") {
		t.Errorf("before the first question:\n%s", out)
	}
}

// A box this computer has noted as parked, or as having its server
// destroyed, says so to a command that needs the server, where the cloud's
// credential would otherwise have to be asked for first.
func TestLocalNoteIsReadBeforeACloudTokenIsAskedFor(t *testing.T) {
	isolate(t)
	fakeHetzner(t) // an empty credential store: the token would be asked for
	register(t, config.StarterFor("old", "hetzner"))
	register(t, config.StarterFor("delta", "hetzner"))
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := saveDestroyedMark("old", destroyedMark{At: at}); err != nil {
		t.Fatal(err)
	}
	if err := saveParkMark("delta", parkMark{Since: at}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"attach", "ssh", "sessions", "status", "doctor", "plan", "apply"} {
		code, out, errs := run(cmd, "old")
		if want := "keelbox: old has no server (keelbox destroy deleted it on 2026-11-01); create it: keelbox up old, or remove the box from keelbox: keelbox forget old\n"; code != 1 || errs != want {
			t.Errorf("keelbox %s old: exit %d\n%s%s", cmd, code, out, errs)
		}
		code, out, errs = run(cmd, "delta")
		if code != 1 || !strings.HasPrefix(errs, "keelbox: delta has been parked since 2026-11-01:") || !strings.HasSuffix(errs, "Bring it back: keelbox resume delta\n") {
			t.Errorf("keelbox %s delta: exit %d\n%s%s", cmd, code, out, errs)
		}
		if strings.Contains(out+errs, "API token") {
			t.Errorf("keelbox %s: asked for, or about, the token first:\n%s%s", cmd, out, errs)
		}
	}
}

// Ctrl-D at one of keelbox up's questions before anything changed says
// what Ctrl-C says there.
func TestUpCtrlDSaysWhatCtrlCSays(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	eof, one := "", "1\n" // the input ends at the first question, and at the second
	code, out := upWith(t, &fakeRunner{}, &eof, "up")
	if code != 1 || !strings.HasSuffix(out, "keelbox up: stopped; nothing was changed. Start again: keelbox up\n") || strings.Contains(out, "nothing more") {
		t.Errorf("at the first question: exit %d\n%s", code, out)
	}
	code, out = upWith(t, &fakeRunner{}, &one, "up")
	if code != 1 || !strings.HasSuffix(out, "keelbox up: stopped; nothing was changed. Your answers so far are kept. Continue: keelbox up\n") {
		t.Errorf("with answers kept: exit %d\n%s", code, out)
	}
}
