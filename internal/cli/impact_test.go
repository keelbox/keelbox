package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/system"
)

// inUse is a module whose one step is pending and whose impact is what a
// home server would show: a port cut off (gated), a restart (not gated).
type inUse struct{ applied *bool }

func (inUse) Name() string                { return "host" }
func (inUse) Enabled(*config.Config) bool { return true }
func (m inUse) Steps(*engine.Env) []engine.Step {
	return []engine.Step{{Name: "firewall",
		Check: func(context.Context, *engine.Env) (engine.Status, error) { return engine.Pending("ufw: enable"), nil },
		Apply: func(context.Context, *engine.Env) error { *m.applied = true; return nil }}}
}
func (inUse) Verify(context.Context, *engine.Env) []engine.Finding { return nil }
func (inUse) Impact(_ context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	items := []engine.ImpactItem{{Group: engine.ImpactRestarts, Subject: "sshd", Detail: "reloads"}}
	if _, ok := e.Cfg.Host.Firewall.Allow, true; ok && len(e.Cfg.Host.Firewall.Allow) == 0 {
		items = append(items, engine.ImpactItem{Group: engine.ImpactCuts, Subject: "445/tcp", Gate: true, Detail: "Samba (smbd) listens on the network now",
			Keep: &config.Allow{Name: "samba", Port: config.Port(445)}}, engine.ImpactItem{Group: engine.ImpactCuts, Subject: "ssh", KeepSSH: "lan", Detail: "closes later"})
	}
	return items
}

func gateEnv(t *testing.T) (*engine.Env, *bool) {
	t.Helper()
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	x := system.NewFake()
	x.AddFile("/etc/hostname", "nas-home\n", 0o644)
	applied := false
	return &engine.Env{X: x, Cfg: &config.Config{Box: "acme"}, M: m}, &applied
}

func TestFirstApplyGate(t *testing.T) {
	e, applied := gateEnv(t)
	mods := []engine.Module{inUse{applied}}
	var errOut bytes.Buffer
	if firstApplyGate(context.Background(), e, mods, &errOut) {
		t.Fatal("first apply with a cut went ahead")
	}
	got := errOut.String()
	for _, want := range []string{
		"keelbox: acme is in use, and this first apply would cut off or replace what it runs. Nothing has changed.",
		"keelbox plan acme --impact",
		"* 445/tcp",
		"- {name: samba, port: 445}",
		"public_ssh: lan",
		"keelbox apply acme --adopt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("gate output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sshd") || strings.Contains(got, "closes later") {
		t.Errorf("the gate lists only what it stops for:\n%s", got)
	}
	if *applied {
		t.Error("a step applied")
	}
	if recs, _ := logbook.Read(e.X); len(recs) != 0 {
		t.Errorf("the gate wrote the logbook: %+v", recs)
	}

	e.Adopt = true
	if !firstApplyGate(context.Background(), e, mods, &errOut) {
		t.Error("--adopt did not accept")
	}
	e.Adopt = false
	e.Cfg.Host.Firewall.Allow = []config.Allow{{Name: "samba", Port: config.Port(445)}}
	if !firstApplyGate(context.Background(), e, mods, &errOut) {
		t.Error("kept by config, still stopped")
	}
	e.Cfg.Host.Firewall.Allow = nil
	if err := logbook.Append(e.X, logbook.Record{Time: time.Now(), Release: e.M.Release, Result: "failed", Keelbox: "v9"}); err != nil {
		t.Fatal(err)
	}
	if !firstApplyGate(context.Background(), e, mods, &errOut) {
		t.Error("a box applied before was stopped")
	}
}

func TestPlanImpact(t *testing.T) {
	e, applied := gateEnv(t)
	mods := []engine.Module{inUse{applied}}
	var out bytes.Buffer
	if code := PlanImpact(context.Background(), e, mods, false, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	text := out.String()
	for _, want := range []string{"keelbox plan --impact: acme", "nas-home: 0 services listening on the network", "\nCuts off\n* 445/tcp ", "  ssh          closes later",
		"\nRestarts\n  sshd         reloads", "To keep them, add to the config:\n  host:\n    firewall:\n      public_ssh: lan\n      allow:\n        - {name: samba, port: 445}\n",
		"* The first apply stops before changing anything until each is kept in the config or accepted: keelbox apply acme --adopt", "In all, 1 step changes; keelbox plan acme lists every step."} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	out.Reset()
	PlanImpact(context.Background(), e, mods, true, &out)
	var r impactReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if r.Impact != 1 || !r.FirstApply || r.Steps != 1 || len(r.gated()) != 1 || r.Keep.Allow[0].Port != config.Port(445) || r.Machine.Hostname != "nas-home" {
		t.Errorf("json %+v", r)
	}
	if *applied {
		t.Error("plan applied a step")
	}
}
