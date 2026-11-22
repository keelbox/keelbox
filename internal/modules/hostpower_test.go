package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

func TestSleepNeverConvergesAndAllowUndoesIt(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, "host:\n  power:\n    sleep: never\n"))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for _, u := range SleepTargets {
		if !s.units["masked:"+u] {
			t.Errorf("%s not masked", u)
		}
	}
	conf := s.content(LogindDropIn)
	for _, want := range []string{"HandleLidSwitch=ignore", "HandleLidSwitchExternalPower=ignore", "IdleAction=ignore", "Managed by keelbox"} {
		if !strings.Contains(conf, want) {
			t.Errorf("logind drop-in lacks %q:\n%s", want, conf)
		}
	}
	if !s.Ran("systemctl try-restart systemd-logind.service") {
		t.Error("logind not told about the drop-in on a box without a display manager")
	}
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Changes) != 0 {
		t.Errorf("second apply changed %v (%v)", res.Changes, err)
	}
	for _, f := range (Host{}).Verify(ctx, e) {
		if f.Check == "sleep" && f.Level != engine.LevelOK {
			t.Errorf("doctor: %+v", f)
		}
	}

	// Back to allow: keelbox undoes its own masks and drop-in.
	e = simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if s.has(LogindDropIn) || s.units["masked:suspend.target"] {
		t.Error("allow left keelbox's sleep settings")
	}
	if res, _ := engine.Apply(ctx, e, All()); len(res.Changes) != 0 {
		t.Errorf("allow is not idempotent: %v", res.Changes)
	}
}

func TestSleepAllowLeavesTheOwnersMasks(t *testing.T) {
	s := newSim(t)
	s.units["masked:suspend.target"] = true // masked by hand, no keelbox drop-in
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if !s.units["masked:suspend.target"] || s.Ran("systemctl unmask") {
		t.Error("allow unmasked a target keelbox did not mask")
	}
}

func TestSleepNeverWithADesktopWaitsForBoot(t *testing.T) {
	s := newSim(t)
	s.units["display-manager.service"] = true
	e := simEnv(t, s, testConfig(t, "host:\n  power:\n    sleep: never\n"))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if s.Ran("systemctl try-restart systemd-logind") {
		t.Error("restarted logind under a display manager: that ends the desktop session")
	}
	if !s.units["masked:suspend.target"] {
		t.Error("suspend.target not masked")
	}
}

func TestSleepDoctorFailsWhenUnmasked(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, "host:\n  power:\n    sleep: never\n"))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	s.units["masked:suspend.target"] = false
	var got *engine.Finding
	for _, f := range (Host{}).Verify(ctx, e) {
		if f.Check == "sleep" {
			got = &f
		}
	}
	if got == nil || got.Level != engine.LevelFail || !strings.Contains(got.Detail, "suspend.target") || !strings.Contains(got.Detail, "keelbox apply testbox") {
		t.Errorf("doctor: %+v", got)
	}
}

// The adoption report lists what sleep: never changes, and never gates it.
func TestSleepNeverImpact(t *testing.T) {
	s := homeServer(t)
	got := describe(impactOf(t, s, "host:\n  power:\n    sleep: never\n"))
	for _, want := range []string{"  changes host sleep", "  changes host logind", "  restarts host systemd-logind"} {
		if !contains(got, want) {
			t.Errorf("impact lacks %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
	s = homeServer(t)
	s.units["display-manager.service"] = true
	got = describe(impactOf(t, s, "host:\n  power:\n    sleep: never\n"))
	if contains(got, "  restarts host systemd-logind") || !contains(got, "  changes host logind") {
		t.Errorf("with a desktop running:\n%s", strings.Join(got, "\n"))
	}
	if got := describe(impactOf(t, homeServer(t), "")); contains(got, "  changes host sleep") || contains(got, "  changes host logind") {
		t.Errorf("sleep: allow on a machine keelbox never touched:\n%s", strings.Join(got, "\n"))
	}
}
