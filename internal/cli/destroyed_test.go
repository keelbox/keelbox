package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

// onTerminal sets whether keelbox takes its output for a terminal.
func onTerminal(t *testing.T, yes bool) {
	t.Helper()
	old := stdoutIsTerminal
	stdoutIsTerminal = func(io.Writer) bool { return yes }
	t.Cleanup(func() { stdoutIsTerminal = old })
}

const configKept = "keelbox: acme's config is kept: keelbox up acme creates the server again, keelbox forget acme removes the box from keelbox.\n"

// keelbox destroy keeps the box's config, notes here that the server is
// gone, and on a terminal says what the config is kept for.
func TestDestroyNotesThatTheServerIsGone(t *testing.T) {
	w := newParkWorld(t)
	onTerminal(t, true)
	before := time.Now().Add(-time.Second)
	code, out := w.do(t, "destroy", options{yes: true}, nil)
	if code != 0 || len(w.api.Servers) != 0 {
		t.Fatalf("destroy: exit %d, %d servers\n%s", code, len(w.api.Servers), out)
	}
	m, ok := loadDestroyedMark("acme")
	if !ok || m.At.Before(before) || m.At.After(time.Now().Add(time.Second)) {
		t.Errorf("the note: %+v, %v", m, ok)
	}
	if !isRegistered("acme") {
		t.Error("destroy removed the config")
	}
	if !strings.HasSuffix(out, configKept) {
		t.Errorf("on a terminal:\n%s", out)
	}

	// Without a terminal the note is written and the line is left out.
	w = newParkWorld(t)
	onTerminal(t, false)
	code, out = w.do(t, "destroy", options{yes: true}, nil)
	if _, ok := loadDestroyedMark("acme"); code != 0 || !ok || strings.Contains(out, "config is kept") {
		t.Errorf("without a terminal: exit %d, note %v\n%s", code, ok, out)
	}

	// A parked box that is destroyed has no server either.
	w = newParkWorld(t)
	onTerminal(t, true)
	w.do(t, "park", options{yes: true}, nil)
	code, out = w.do(t, "destroy", options{yes: true, discardWork: true}, nil)
	if _, ok := loadDestroyedMark("acme"); code != 0 || !ok || !strings.HasSuffix(out, configKept) {
		t.Errorf("a parked box: exit %d, note %v\n%s", code, ok, out)
	}
	if _, ok := loadParkMark("acme"); ok {
		t.Error("the parked note is still there")
	}

	// A destroy that deleted nothing notes nothing.
	w = newParkWorld(t)
	onTerminal(t, true)
	w.terminal = true
	code, out = w.do(t, "destroy", options{}, str("wrong\n"))
	if _, ok := loadDestroyedMark("acme"); code != 1 || ok || strings.Contains(out, "config is kept") {
		t.Errorf("not confirmed: exit %d, note %v\n%s", code, ok, out)
	}
}

// keelbox ls and keelbox status --all say that the server is destroyed,
// with the date and the two commands, from this computer's note alone.
func TestListAndStatusAllShowDestroyed(t *testing.T) {
	w := newParkWorld(t)
	if code, out := w.do(t, "destroy", options{yes: true}, nil); code != 0 {
		t.Fatalf("destroy: exit %d\n%s", code, out)
	}
	w.forgetCalls()
	status := "server destroyed " + day(time.Now()) + ": keelbox up acme or keelbox forget acme"
	_, out, _ := run("ls")
	if !strings.Contains(out, "acme  hetzner   -        "+status+"\n") {
		t.Errorf("ls:\n%s", out)
	}
	code, out, _ := run("status", "--all")
	if code != 0 || !strings.Contains(out, status) || !strings.Contains(out, "acme  server destroyed  -") || !strings.Contains(out, "1 box: 1 server destroyed.\nacme: "+status+"\n") || strings.Contains(out, "unreachable") {
		t.Errorf("status --all: exit %d\n%s", code, out)
	}
	_, out, _ = run("status", "--all", "--json")
	if !strings.Contains(out, `"reach": "destroyed"`) || !strings.Contains(out, `"destroyed": "`) {
		t.Errorf("status --all --json:\n%s", out)
	}
	if calls := w.api.Calls(); len(calls) != 0 {
		t.Errorf("the cloud was asked about a destroyed box: %v", calls)
	}

	// Without the note the same box is one keelbox cannot reach.
	forgetDestroyedMark("acme")
	_, out, _ = run("ls")
	if strings.Contains(out, "destroyed") {
		t.Errorf("ls without the note:\n%s", out)
	}
	if code, out, _ := run("status", "--all"); code != 1 || strings.Contains(out, "destroyed") {
		t.Errorf("status --all without the note: exit %d\n%s", code, out)
	}
}

// keelbox forget removes the note, and says the server is gone instead of
// that it keeps running.
func TestForgetRemovesTheDestroyedNote(t *testing.T) {
	w := newParkWorld(t)
	w.do(t, "destroy", options{yes: true}, nil)
	code, out, errs := run("forget", "acme", "--yes")
	if code != 0 || out != "Forgot acme. keelbox destroy deleted its server on "+day(time.Now())+".\n" {
		t.Errorf("forget: exit %d\n%s%s", code, out, errs)
	}
	if _, err := os.Stat(destroyedMarkPath("acme")); err == nil {
		t.Error("forget left the destroyed note")
	}
	if isRegistered("acme") {
		t.Error("forget left the config")
	}
}

// A server for the box, created by keelbox up or keelbox create or found
// in the cloud, ends the note.
func TestAServerEndsTheDestroyedNote(t *testing.T) {
	for _, cmd := range []string{"up", "create"} {
		w := newParkWorld(t)
		// ssh answers, and returns no host keys: the command stops right
		// after it has created the server.
		stubSSH(t).write("sessions-end", "0", 0o644)
		w.do(t, "destroy", options{yes: true}, nil)
		if _, ok := loadDestroyedMark("acme"); !ok {
			t.Fatal("destroy left no note")
		}
		code, out, errs := run(cmd, "acme")
		if code != 1 || !strings.Contains(errs, "rotate host keys") || len(w.api.Servers) != 1 {
			t.Fatalf("keelbox %s acme: exit %d, %d servers\n%s%s", cmd, code, len(w.api.Servers), out, errs)
		}
		if _, ok := loadDestroyedMark("acme"); ok {
			t.Errorf("keelbox %s acme created a server and left the note", cmd)
		}
	}

	// Created again from another computer: the first command that finds
	// the server drops the note.
	w := newParkWorld(t)
	if err := saveDestroyedMark("acme", destroyedMark{At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cloudPublic(context.Background(), w.cl, w.cfg, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadDestroyedMark("acme"); ok {
		t.Error("the note outlived a server found in the cloud")
	}
}

// A command that needs the server of a box whose cloud has none says so,
// and ends with both ways on: create the server, or forget the box.
func TestNoServerNamesBothFixes(t *testing.T) {
	const fix = "; create it: keelbox up acme, or remove the box from keelbox: keelbox forget acme"
	ctx := context.Background()
	open := func(t *testing.T, cfg *config.Config) cloud {
		t.Helper()
		cl, err := openCloud(ctx, cfg, options{}, nil, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}
	for name, c := range map[string]struct {
		cloud func(t *testing.T) (cloud, *config.Config)
		want  string
	}{
		"hetzner": {func(t *testing.T) (cloud, *config.Config) {
			fakeHetzner(t)
			cfg := hetznerCfg(t, "")
			return &hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}, cfg
		}, "no server named acme in the Hetzner project"},
		"aws": {func(t *testing.T) (cloud, *config.Config) {
			fakeAWS(t)
			return open(t, awsCfg(t)), awsCfg(t)
		}, "no Lightsail instance named acme in eu-central-1"},
		"digitalocean": {func(t *testing.T) (cloud, *config.Config) {
			fakeDO(t)
			return open(t, doCfg(t)), doCfg(t)
		}, "no Droplet named acme in the DigitalOcean account"},
		"gcp": {func(t *testing.T) (cloud, *config.Config) {
			fakeGCP(t)
			return open(t, gcpCfg(t)), gcpCfg(t)
		}, "no VM named acme in the Google Cloud project acme-prod"},
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			cl, cfg := c.cloud(t)
			_, _, err := cloudPublic(ctx, cl, cfg, "acme")
			if err == nil || err.Error() != c.want+fix {
				t.Errorf("got  %v\nwant %s", err, c.want+fix)
			}
		})
	}
}
