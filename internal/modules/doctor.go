package modules

import (
	"context"
	"fmt"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/doctorrun"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Doctor schedules doctor on the box (docs/adr/0042-scheduled-doctor.md):
// a timer from doctor.schedule (hourly by default, and five minutes after
// boot) that runs doctor-run, which raises an event when a check starts or
// stops failing. With doctor.schedule: off the units are removed.
type Doctor struct{}

func (Doctor) Name() string                { return "doctor" }
func (Doctor) Enabled(*config.Config) bool { return true }

var doctorService = rootService{
	Unit: doctorrun.Service, Header: ". Scheduled doctor: alerts when a check starts or stops failing.",
	Description: "keelbox scheduled doctor", Command: doctorrun.Command, DayTwo: true, Timeout: "5min",
}

func doctorTimer(schedule string) rootTimer {
	return rootTimer{
		Unit: doctorrun.Timer, Header: " (doctor.schedule).", Description: "keelbox doctor schedule", Service: doctorrun.Service,
		Calendar: schedule, Key: "doctor.schedule", Timer: []string{"OnBootSec=5min", "RandomizedDelaySec=5min", "AccuracySec=1min"},
	}
}

func (Doctor) Steps(e *engine.Env) []engine.Step {
	schedule := e.Cfg.DoctorSchedule()
	if schedule == "" {
		return []engine.Step{
			removeRootUnitStep("doctor-timer-off", doctorrun.Timer),
			removeRootUnitStep("doctor-service-off", doctorrun.Service),
		}
	}
	steps := []engine.Step{
		engine.DirStep("doctor-dir", doctorrun.Dir, 0o755, "root", "root"),
		serviceStep("doctor-service-unit", doctorService),
	}
	return append(steps, timerSteps("doctor-timer", doctorTimer(schedule))...)
}

// removeRootUnitStep stops, disables and removes one of keelbox's root
// units when the config no longer wants it.
func removeRootUnitStep(name, unit string) engine.Step {
	path := unitDir + unit
	return engine.Step{
		Name: name,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			st, err := e.X.Stat(path)
			if err != nil || !st.Exists {
				return engine.Converged(), err
			}
			return engine.Pending("stop and remove %s", unit), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, _ = e.X.Run(ctx, system.C("systemctl", "disable", "--now", unit))
			if err := e.X.Remove(path); err != nil {
				return err
			}
			_, err := e.X.Run(ctx, system.C("systemctl", "daemon-reload"))
			return err
		},
	}
}

// doctorStaleAfter is how old the last scheduled run may be before doctor
// warns: a day and an hour covers a daily schedule.
const doctorStaleAfter = 25 * time.Hour

func (Doctor) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	const mod = "doctor"
	if e.Cfg.DoctorSchedule() == "" {
		return nil
	}
	var out []engine.Finding
	if system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", doctorrun.Timer)) {
		out = append(out, ok(mod, "timer", "scheduled: "+e.Cfg.DoctorSchedule()))
	} else {
		out = append(out, fail(mod, "timer", doctorrun.Timer+" is not active (systemctl status "+doctorrun.Timer+"); keelbox apply "+e.Cfg.Box+" starts it again"))
	}
	l, found := doctorrun.ReadLast(e.X)
	switch {
	case !found:
		out = append(out, ok(mod, "last-run", "no scheduled run yet; the first is five minutes after boot or at the next "+e.Cfg.DoctorSchedule()))
	case time.Since(l.Time) > doctorStaleAfter:
		out = append(out, warn(mod, "last-run", fmt.Sprintf("last scheduled run %s ago: journalctl -u %s", time.Since(l.Time).Round(time.Minute), doctorrun.Service)))
	case time.Duration(l.Seconds*float64(time.Second)) > doctorrun.SlowAfter:
		out = append(out, warn(mod, "last-run", fmt.Sprintf("the last scheduled run took %.0f s, over %s: journalctl -u %s shows which checks were slow", l.Seconds, doctorrun.SlowAfter, doctorrun.Service)))
	default:
		out = append(out, ok(mod, "last-run", fmt.Sprintf("%s ago, %d failures, %d warnings", time.Since(l.Time).Round(time.Minute), l.Failures, l.Warnings)))
	}
	return out
}
