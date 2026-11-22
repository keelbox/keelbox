package modules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/doctorrun"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

func TestScheduledDoctorUnits(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if !s.units[doctorrun.Timer] {
		t.Errorf("%s not enabled and running", doctorrun.Timer)
	}
	for unit, wants := range map[string][]string{
		doctorrun.Service: {"ExecCondition=/usr/local/bin/keelbox has-command doctor-run", "ExecStart=/usr/local/bin/keelbox doctor-run --local /etc/keelbox/keelbox.yaml", "TimeoutStartSec=5min"},
		doctorrun.Timer:   {"OnCalendar=hourly", "OnBootSec=5min", "RandomizedDelaySec=5min"},
	} {
		for _, want := range wants {
			if c := s.content(unitDir + unit); !strings.Contains(c, want) {
				t.Errorf("%s lacks %q:\n%s", unit, want, c)
			}
		}
	}

	finding := func() engine.Finding {
		for _, f := range engine.Doctor(ctx, e, All()) {
			if f.Module == "doctor" && f.Check == "last-run" {
				return f
			}
		}
		t.Fatal("no doctor/last-run finding")
		return engine.Finding{}
	}
	// A laptop's doctor records nothing.
	if f := finding(); f.Level != engine.LevelOK {
		t.Errorf("before the first run: %+v", f)
	}
	if _, found := doctorrun.ReadLast(e.X); found {
		t.Error("keelbox doctor wrote the scheduled run's state")
	}
	write := func(l doctorrun.Last) {
		b, _ := json.Marshal(l)
		s.Files[doctorrun.LastPath] = &system.FakeFile{Data: b, Mode: 0o644, Owner: "root", Group: "root"}
	}
	for name, c := range map[string]struct {
		last doctorrun.Last
		want engine.Level
	}{
		"recent": {doctorrun.Last{Time: time.Now().Add(-time.Hour), Seconds: 8}, engine.LevelOK},
		"stale":  {doctorrun.Last{Time: time.Now().Add(-26 * time.Hour), Seconds: 8}, engine.LevelWarn},
		"slow":   {doctorrun.Last{Time: time.Now().Add(-time.Hour), Seconds: 90}, engine.LevelWarn},
	} {
		write(c.last)
		if f := finding(); f.Level != c.want {
			t.Errorf("%s: %+v", name, f)
		}
	}

	e.Cfg = testConfig(t, "doctor: {schedule: \"off\"}\n")
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{doctorrun.Timer, doctorrun.Service} {
		if st, _ := e.X.Stat(unitDir + unit); st.Exists {
			t.Errorf("doctor.schedule: off left %s", unit)
		}
	}
	if fs := (Doctor{}).Verify(ctx, e); len(fs) != 0 {
		t.Errorf("doctor with the schedule off: %+v", fs)
	}
}
