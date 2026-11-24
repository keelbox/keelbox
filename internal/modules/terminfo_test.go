package modules

import (
	"context"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

// terminfoPending is the terminfo step's plan detail, or "" when converged.
func terminfoPending(t *testing.T, e *engine.Env) string {
	t.Helper()
	for _, it := range engine.Plan(context.Background(), e, All()) {
		if it.Step == "terminfo" && !it.OK {
			return it.Detail
		}
	}
	return ""
}

// Ghostty's and kitty's TERM names get entries that copy the release's
// own ghostty and kitty, and a second apply changes nothing.
func TestTerminfoAliasesTheTerminalsEntries(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"xterm-ghostty": "xterm-ghostty|keelbox alias of ghostty,",
		"xterm-kitty":   "xterm-kitty|keelbox alias of kitty,",
	} {
		if got := s.terminfo[name]; got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if d := terminfoPending(t, e); d != "" {
		t.Errorf("after apply: %s", d)
	}
}

// Ubuntu 24.04 has no ghostty entry: xterm-ghostty stands for
// xterm-256color, and becomes ghostty's copy once the release has it.
func TestTerminfoFallsBackWithoutTheReleasesEntry(t *testing.T) {
	s := newSim(t)
	s.noGhostty = true
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := s.terminfo["xterm-ghostty"]; got != "xterm-ghostty|keelbox alias of xterm-256color," {
		t.Errorf("24.04: %q", got)
	}
	s.noGhostty = false // upgraded to a release whose ncurses-term has ghostty
	if d := terminfoPending(t, e); d != "add terminfo for xterm-ghostty in /etc/terminfo" {
		t.Errorf("after the upgrade, plan: %q", d)
	}
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := s.terminfo["xterm-ghostty"]; got != "xterm-ghostty|keelbox alias of ghostty," {
		t.Errorf("after the upgrade: %q", got)
	}
}

// An entry keelbox did not compile, such as one the user copied from
// their terminal with tic, is left as it is.
func TestTerminfoLeavesAnotherEntryAlone(t *testing.T) {
	s := newSim(t)
	own := "xterm-ghostty|ghostty,"
	s.terminfo = map[string]string{"xterm-ghostty": own}
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if got := s.terminfo["xterm-ghostty"]; got != own {
		t.Errorf("the user's entry became %q", got)
	}
	if s.terminfo["xterm-kitty"] == "" {
		t.Error("xterm-kitty was not added")
	}
}
