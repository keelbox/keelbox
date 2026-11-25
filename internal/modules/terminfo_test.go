package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

const ghosttyEntry = "/etc/terminfo/x/xterm-ghostty"

// terminfoPending is the terminfo step's plan detail, or "" when converged.
func terminfoPending(t *testing.T, e *engine.Env) string {
	t.Helper()
	for _, it := range engine.Plan(context.Background(), e, All()) {
		if it.Step == "terminfo" {
			if it.Err != nil {
				t.Fatalf("terminfo check: %v", it.Err)
			}
			if !it.OK {
				return it.Detail
			}
		}
	}
	return ""
}

func applyAll(t *testing.T, e *engine.Env) {
	t.Helper()
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
}

// ghosttyNames is the names line of keelbox's xterm-ghostty, "" if none.
func ghosttyNames(s *sim) string {
	f, ok := s.Files[ghosttyEntry]
	if !ok {
		return ""
	}
	names, _, _ := strings.Cut(string(f.Data), "\n")
	return names
}

// xterm-ghostty is a copy of the release's ghostty that every account
// reads; xterm-kitty comes from the kitty-terminfo package, not keelbox.
func TestTerminfoAliasesGhosttyAndInstallsKittys(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	applyAll(t, e)
	if got := ghosttyNames(s); got != "xterm-ghostty|keelbox alias of ghostty," {
		t.Errorf("xterm-ghostty: %q", got)
	}
	if !strings.Contains(string(s.Files[ghosttyEntry].Data), "src=ghostty,") {
		t.Errorf("xterm-ghostty is not ghostty's copy:\n%s", s.Files[ghosttyEntry].Data)
	}
	if s.pkgs["kitty-terminfo"] == "" {
		t.Error("kitty-terminfo is not installed")
	}
	if s.has("/etc/terminfo/x/xterm-kitty") {
		t.Error("keelbox compiled xterm-kitty, which the package provides")
	}
	if d := terminfoPending(t, e); d != "" {
		t.Errorf("after apply: %s", d)
	}
}

// Ubuntu 24.04 has no ghostty entry: xterm-ghostty copies xterm-256color,
// and becomes ghostty's copy once the release has one.
func TestTerminfoFallsBackWithoutTheReleasesEntry(t *testing.T) {
	s := newSim(t)
	s.noGhostty = true
	e := simEnv(t, s, testConfig(t, ""))
	applyAll(t, e)
	if got := ghosttyNames(s); got != "xterm-ghostty|keelbox alias of xterm-256color," {
		t.Errorf("24.04: %q", got)
	}
	s.noGhostty = false // upgraded to a release whose ncurses-term has ghostty
	if d := terminfoPending(t, e); d != "update terminfo xterm-ghostty from the release's ghostty" {
		t.Errorf("after the upgrade, plan: %q", d)
	}
	applyAll(t, e)
	if got := ghosttyNames(s); got != "xterm-ghostty|keelbox alias of ghostty," {
		t.Errorf("after the upgrade: %q", got)
	}
}

// tic copies the base's capabilities when it compiles, so an update to
// the release's entry is copied again.
func TestTerminfoFollowsAnUpdateToTheBase(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	applyAll(t, e)
	s.ncursesRev = "-sru1"
	if d := terminfoPending(t, e); d != "update terminfo xterm-ghostty from the release's ghostty" {
		t.Errorf("after an ncurses update, plan: %q", d)
	}
	applyAll(t, e)
	if !strings.Contains(string(s.Files[ghosttyEntry].Data), "src=ghostty-sru1,") {
		t.Errorf("not recompiled:\n%s", s.Files[ghosttyEntry].Data)
	}
}

// Under an umask of 077 tic writes files only root reads; apply makes
// them readable, and plan says so when they are not.
func TestTerminfoIsReadableUnderAStrictUmask(t *testing.T) {
	s := newSim(t)
	s.umask = 0o077
	e := simEnv(t, s, testConfig(t, ""))
	applyAll(t, e)
	if m := s.Files[ghosttyEntry].Mode; m != 0o644 {
		t.Errorf("entry mode %o", m)
	}
	if m := s.Files["/etc/terminfo/x"].Mode; m != 0o755 {
		t.Errorf("directory mode %o", m)
	}
	s.Files[ghosttyEntry].Mode = 0o600
	if d := terminfoPending(t, e); d != "make "+ghosttyEntry+" mode 644, so every account reads it" {
		t.Errorf("plan: %q", d)
	}
	applyAll(t, e)
	if m := s.Files[ghosttyEntry].Mode; m != 0o644 {
		t.Errorf("entry mode after the fix %o", m)
	}
}

// Once a package installs xterm-ghostty, keelbox's copy would hide it:
// apply removes its own copy.
func TestTerminfoGivesWayToAPackagesEntry(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	applyAll(t, e)
	s.pkgs["ghostty"] = "1"
	if d := terminfoPending(t, e); d != "remove keelbox's terminfo xterm-ghostty: a package installs it" {
		t.Errorf("plan: %q", d)
	}
	applyAll(t, e)
	if s.has(ghosttyEntry) {
		t.Error("keelbox's copy is still there")
	}
	if d := terminfoPending(t, e); d != "" {
		t.Errorf("after: %s", d)
	}
}

// An entry keelbox did not compile, such as one the user copied from
// their terminal with tic, is left as it is.
func TestTerminfoLeavesAnotherEntryAlone(t *testing.T) {
	s := newSim(t)
	s.AddFile(ghosttyEntry, "xterm-ghostty|ghostty,\n\tam,\n", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	applyAll(t, e)
	if got := ghosttyNames(s); got != "xterm-ghostty|ghostty," {
		t.Errorf("the user's entry became %q", got)
	}
}

// The user's own xterm-ghostty stays when a package installs the name
// too: keelbox removes only a copy it compiled.
func TestTerminfoLeavesTheUsersEntryWhenAPackageArrives(t *testing.T) {
	s := newSim(t)
	s.AddFile(ghosttyEntry, "xterm-ghostty|ghostty,\n\tam,\n", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	applyAll(t, e)
	s.pkgs["ghostty"] = "1"
	if d := terminfoPending(t, e); d != "" {
		t.Errorf("plan: %q", d)
	}
	applyAll(t, e)
	if got := ghosttyNames(s); got != "xterm-ghostty|ghostty," {
		t.Errorf("the user's entry became %q", got)
	}
}

// softFailure is the terminfo step's failure in an apply, "" if none.
func softFailure(t *testing.T, e *engine.Env) string {
	t.Helper()
	res, err := engine.Apply(context.Background(), e, All())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Failed {
		if f.Step == "terminfo" {
			return f.Detail
		}
	}
	return ""
}

// A file in keelbox's place that infocmp cannot read is not keelbox's:
// apply leaves it, says how to clear it, and the rest of the box applies.
func TestTerminfoLeavesAnUnreadableFileAndSaysSo(t *testing.T) {
	s := newSim(t)
	s.AddFile(ghosttyEntry, "garbage", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	d := softFailure(t, e)
	if !strings.Contains(d, "sudo rm "+ghosttyEntry) || !strings.Contains(d, "keelbox apply "+e.Cfg.Box) {
		t.Errorf("failure: %q", d)
	}
	if string(s.Files[ghosttyEntry].Data) != "garbage" {
		t.Error("the file was overwritten")
	}
}

// With neither the base nor the fallback, apply names the packages to
// reinstall and writes nothing.
func TestTerminfoWithoutABaseSaysWhatToReinstall(t *testing.T) {
	s := newSim(t)
	s.noBase = true
	e := simEnv(t, s, testConfig(t, ""))
	if d := softFailure(t, e); !strings.Contains(d, "sudo apt-get install --reinstall ncurses-base ncurses-term") {
		t.Errorf("failure: %q", d)
	}
	if s.has(ghosttyEntry) {
		t.Error("an entry was written")
	}
}
