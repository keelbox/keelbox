package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/runs"
	"github.com/keelbox/keelbox/internal/system"
)

func detachConfig(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ7m e2e\"]\n"), "acme.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	r.ApplyDefaults()
	return r
}

func TestStartUnitRunsTheApplyAsATransientUnit(t *testing.T) {
	x := system.NewFake()
	x.AddFile(provider.RemoteConfig, "box: acme\n", 0o600)
	x.OK("systemd-run ", "")
	env := map[string]string{"SUDO_USER": "sam", "KEELBOX_SSH_CONNECTION": "192.168.1.5 50000 192.168.1.20 22", "PATH": "/usr/bin:/bin", "HOME": "/root", "SECRET_THING": "x",
		provider.EnvSSHAddrs: "203.0.113.7:22,[2001:db8::7]:22"}
	id, err := startUnit(context.Background(), x, detachConfig(t), "/var/lib/keelbox/stage/keelbox", options{configFile: provider.RemoteConfig, adopt: true}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !runs.ValidID(id) {
		t.Fatalf("id %q", id)
	}
	var cmd string
	for _, c := range x.Commands() {
		if strings.HasPrefix(c, "systemd-run ") {
			cmd = c
		}
	}
	for _, want := range []string{
		"--unit=keelbox-apply-" + id + " ", "--collect", "--property=KillMode=mixed", "--setenv=KEELBOX_RUN=" + id,
		"--setenv=SUDO_USER=sam", "--setenv=PATH=/usr/bin:/bin", "--setenv=KEELBOX_SSH_CONNECTION=192.168.1.5 50000 192.168.1.20 22",
		"--setenv=KEELBOX_SSH_ADDRS=203.0.113.7:22,[2001:db8::7]:22",
		"-- /var/lib/keelbox/stage/keelbox apply --local /run/keelbox/run-" + id + ".yaml --adopt",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("systemd-run lacks %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "SECRET_THING") {
		t.Error("the run took the whole environment")
	}
	if f := x.Files[runConfig(id)]; f == nil || string(f.Data) != "box: acme\n" || f.Mode != 0o600 {
		t.Errorf("the run's config copy: %+v", f)
	}
	recs, err := runs.Read(x, id)
	if err != nil || len(recs) != 1 || recs[0].By != "sam" || recs[0].Box != "acme" || len(recs[0].Modules) == 0 {
		t.Errorf("start record: %+v %v", recs, err)
	}
}

func TestStartUnitThatFailsEndsItsLog(t *testing.T) {
	x := system.NewFake()
	x.AddFile(provider.RemoteConfig, "box: acme\n", 0o600)
	x.Fail("systemd-run ")
	id, err := startUnit(context.Background(), x, detachConfig(t), "/k", options{configFile: provider.RemoteConfig}, func(string) string { return "" })
	if err == nil {
		t.Fatalf("started %s", id)
	}
	cur, _ := runs.Current(x)
	recs, _ := runs.Read(x, cur)
	if s := runs.Summarise(recs); !s.Done || s.Result != runs.ResultFailed {
		t.Errorf("a failed start: %+v", s)
	}
	if f := x.Files[runConfig(cur)]; f != nil {
		t.Error("the run's config copy is left behind")
	}
}

func TestRunEndTakesTheLogbookRecord(t *testing.T) {
	x := system.NewFake()
	start := time.Now()
	if e := runEnd(x, 1, start, "keelbox: another keelbox apply is running (/run/keelbox.lock is locked)"); e.Result != runs.ResultFailed || e.Error != "another keelbox apply is running (/run/keelbox.lock is locked)" {
		t.Errorf("before apply: %+v", e)
	}
	if e := runEnd(x, exitInterrupted, start, "x"); e.Result != runs.ResultInterrupted {
		t.Errorf("interrupted: %+v", e)
	}
	if err := logbook.Append(x, logbook.Record{Time: time.Now().UTC(), Box: "acme", Release: "2026.11.3", Result: "partial", Checked: 31,
		Changes: make([]engine.Change, 24), Failed: make([]engine.Change, 1)}); err != nil {
		t.Fatal(err)
	}
	if e := runEnd(x, 1, start, "keelbox: 1 step failed"); e.Result != runs.ResultPartial || e.Steps != 31 || e.Changed != 24 || e.Failed != 1 {
		t.Errorf("from the logbook: %+v", e)
	}
}
