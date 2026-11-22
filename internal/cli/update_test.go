package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/selfupdate"
)

// updateWorld stands in for the release channel and this computer's own
// keelbox, and records what cmdUpdate did.
type updateWorld struct {
	have, latest string
	latestErr    error
	replaceErr   error
	exe          string
	replaced     bool
	ran          []string
	applied      int
}

func (w *updateWorld) install(t *testing.T) {
	t.Helper()
	oc, or, on, ov, oe := currentRelease, replaceSelf, runNew, runningVersion, selfExe
	t.Cleanup(func() { currentRelease, replaceSelf, runNew, runningVersion, selfExe = oc, or, on, ov, oe })
	currentRelease = func(context.Context) (string, error) { return w.latest, w.latestErr }
	replaceSelf = func(context.Context, string, bool) (string, error) {
		if w.replaceErr != nil {
			return "", w.replaceErr
		}
		w.replaced = true
		return w.latest, nil
	}
	runNew = func(exe string, args []string) error { w.ran = append([]string{exe}, args...); return nil }
	runningVersion = func() string { return w.have }
	selfExe = func() (string, error) { return w.exe, nil }
}

func (w *updateWorld) run(t *testing.T, o options, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	cfg := &config.Config{Box: "acme"}
	code := cmdUpdate(context.Background(), cfg, o, filepath.Join(boxesDir(), "acme.yaml"), args, func() int { w.applied++; return 0 }, &out, &errOut)
	return code, out.String(), errOut.String()
}

// A newer release: this computer's keelbox is replaced first, and the new
// one runs the apply, with the box and the flags as they were typed.
func TestUpdateReplacesKeelboxThenLetsTheNewOneApply(t *testing.T) {
	isolate(t)
	w := &updateWorld{have: "v0.1.1", latest: "v0.1.2", exe: "/home/sam/.local/bin/keelbox"}
	w.install(t)
	code, out, errOut := w.run(t, options{}, "acme", "--verbose")
	if code != 0 || errOut != "" || !w.replaced || w.applied != 0 {
		t.Fatalf("code %d, replaced %v, applied here %d\n%s%s", code, w.replaced, w.applied, out, errOut)
	}
	if got, want := strings.Join(w.ran, " "), "/home/sam/.local/bin/keelbox keelbox apply acme --verbose"; got != want {
		t.Errorf("ran %q, want %q", got, want)
	}
	if want := "keelbox v0.1.1 -> v0.1.2 on this computer. Applying it to acme.\n"; out != want {
		t.Errorf("said %q, want %q", out, want)
	}
}

// Nothing newer, or a development build: this keelbox applies.
func TestUpdateAppliesWhenThereIsNothingNewer(t *testing.T) {
	isolate(t)
	w := &updateWorld{have: "v0.1.2", latest: "v0.1.2", exe: "/home/sam/.local/bin/keelbox"}
	w.install(t)
	code, out, _ := w.run(t, options{}, "acme")
	if code != 0 || w.replaced || w.ran != nil || w.applied != 1 || out != "keelbox v0.1.2 here is the current release. Applying it to acme.\n" {
		t.Errorf("current: code %d, replaced %v, ran %v, applied %d: %q", code, w.replaced, w.ran, w.applied, out)
	}
	// An older release on the channel is not installed without the flag.
	w.latest = "v0.1.0"
	if code, _, _ := w.run(t, options{}, "acme"); code != 0 || w.replaced || w.applied != 2 {
		t.Errorf("older on the channel: code %d, replaced %v, applied %d", code, w.replaced, w.applied)
	}
	w.have, w.latestErr = "dev", errors.New("the channel must not be read for a development build")
	code, _, errOut := w.run(t, options{}, "acme")
	if code != 0 || w.applied != 3 || !strings.Contains(errOut, "development build") {
		t.Errorf("dev build: code %d, applied %d: %q", code, w.applied, errOut)
	}
}

// What stops an update leaves the box alone and ends with the command.
func TestUpdateStopsBeforeTheBox(t *testing.T) {
	isolate(t)
	w := &updateWorld{have: "v0.1.1", latest: "v0.1.2", exe: "/opt/homebrew/Cellar/keelbox/0.1.1/bin/keelbox"}
	w.install(t)
	code, _, errOut := w.run(t, options{}, "acme")
	if code != 1 || w.replaced || w.applied != 0 || !strings.HasSuffix(strings.TrimSpace(errOut), "belongs to Homebrew, which updates it; acme was not touched. Update it, then run this again: brew upgrade keelbox") {
		t.Errorf("Homebrew: code %d: %q", code, errOut)
	}
	w.exe, w.latestErr = "/home/sam/.local/bin/keelbox", errors.New("no route to host")
	code, _, errOut = w.run(t, options{}, "acme")
	if code != 1 || w.applied != 0 || !strings.HasSuffix(strings.TrimSpace(errOut), "Apply what this keelbox has: keelbox apply acme") {
		t.Errorf("channel unread: code %d: %q", code, errOut)
	}
	w.latestErr, w.replaceErr = nil, os.ErrPermission
	code, _, errOut = w.run(t, options{}, "acme")
	if code != 1 || w.applied != 0 || !strings.Contains(errOut, "acme was not touched") || !strings.HasSuffix(strings.TrimSpace(errOut), "keelbox apply acme") {
		t.Errorf("no permission: code %d: %q", code, errOut)
	}
	// The channel moved back between the two reads: this keelbox applies.
	w.replaceErr = selfupdate.ErrUpToDate
	if code, _, _ := w.run(t, options{}, "acme"); code != 0 || w.applied != 1 {
		t.Errorf("up to date after all: code %d, applied %d", code, w.applied)
	}
}

func TestArgsAfterCommand(t *testing.T) {
	if got := strings.Join(argsAfterCommand([]string{"--verbose", "update", "acme", "--adopt"}, "update"), " "); got != "--verbose acme --adopt" {
		t.Errorf("got %q", got)
	}
}

// self-update names the boxes this keelbox has not been applied to, as far
// as this computer knows, with the command for each.
func TestBoxesBehind(t *testing.T) {
	isolate(t)
	for _, b := range []string{"acme", "bay", "cove"} {
		register(t, config.Starter{Box: b, Provider: config.ProviderSSH, SSHHost: "192.0.2.10", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}})
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	saveAppliedNote("acme", "v0.1.2", now)
	saveAppliedNote("bay", "v0.1.0", now)
	saveAppliedNote("nowhere", "v0.1.0", now) // not a box of the registry: no note
	if _, err := os.Stat(appliedNotePath("nowhere")); err == nil {
		t.Error("a note for a box that is not registered")
	}
	var out bytes.Buffer
	printBoxesBehind(&out, "v0.1.2")
	want := "\nNot yet applied with v0.1.2, as far as this computer knows:\n" +
		"  bay   v0.1.0             keelbox update bay\n" +
		"  cove  not recorded here  keelbox update cove\n"
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	out.Reset()
	saveAppliedNote("bay", "v0.1.2", now)
	saveAppliedNote("cove", "v0.1.2", now)
	if printBoxesBehind(&out, "v0.1.2"); out.Len() != 0 {
		t.Errorf("every box level: %q", out.String())
	}
}
