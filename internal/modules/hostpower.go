package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// host.power.sleep: never (docs/adr/0040-unclean-shutdown-and-sleep.md).
// For a laptop or desktop used as a box: masking the sleep targets is what
// stops a suspend, whoever asks for it (logind, a desktop's idle timer,
// systemctl suspend); the logind drop-in stops logind trying on a closed
// lid or when idle. Reboots and shutdowns are not affected. allow, the
// default, leaves the machine as it is, and undoes only what never did.

// SleepTargets are the units host.power.sleep: never masks.
var SleepTargets = []string{"sleep.target", "suspend.target", "hibernate.target", "hybrid-sleep.target"}

// LogindDropIn is keelbox's logind configuration for sleep: never.
const LogindDropIn = "/etc/systemd/logind.conf.d/60-keelbox.conf"

const logindSleepConfig = `# Managed by keelbox (host.power.sleep: never): a closed lid or an idle
# session never suspends this box. The sleep targets are masked too.
[Login]
HandleLidSwitch=ignore
HandleLidSwitchExternalPower=ignore
HandleLidSwitchDocked=ignore
IdleAction=ignore
`

func powerSleepSteps(c *config.Config) []engine.Step {
	never := c.PowerSleep() == config.SleepNever
	steps := []engine.Step{sleepTargetsStep(never)}
	if never {
		return append(steps, engine.FileStep("logind-sleep", engine.File{
			Path: LogindDropIn, Content: []byte(logindSleepConfig), Mode: 0o644, OnChange: restartLogind,
		}))
	}
	return append(steps, keelboxFileAbsentStep("logind-sleep", LogindDropIn, restartLogind))
}

// masked lists the sleep targets that are, or are not, masked.
func masked(ctx context.Context, e *engine.Env) (yes, no []string) {
	for _, t := range SleepTargets {
		// is-enabled prints "masked" and exits non-zero for a masked unit.
		out, _ := system.Output(ctx, e.X, system.C("systemctl", "is-enabled", t))
		if out == "masked" || out == "masked-runtime" {
			yes = append(yes, t)
		} else {
			no = append(no, t)
		}
	}
	return yes, no
}

// sleepTargetsStep masks the sleep targets for never. For allow it unmasks
// them only when keelbox's logind drop-in is still there: then keelbox
// masked them, and the user's own masks are never touched.
func sleepTargetsStep(never bool) engine.Step {
	return engine.Step{
		Name: "sleep-targets",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			yes, no := masked(ctx, e)
			if never && len(no) > 0 {
				return engine.Pending("mask %s (the box never sleeps)", strings.Join(no, ", ")), nil
			}
			if !never && len(yes) > 0 && keelboxOwns(e, LogindDropIn) {
				return engine.Pending("unmask %s (host.power.sleep: allow)", strings.Join(yes, ", ")), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			verb := "unmask"
			if never {
				verb = "mask"
			}
			_, err := e.X.Run(ctx, system.C("systemctl", append([]string{verb}, SleepTargets...)...))
			return err
		},
	}
}

func keelboxOwns(e *engine.Env, path string) bool {
	data, err := e.X.ReadFile(path)
	return err == nil && strings.HasPrefix(string(data), "# Managed by keelbox")
}

// keelboxFileAbsentStep removes a file keelbox wrote, and nothing else.
func keelboxFileAbsentStep(step, path string, onChange func(context.Context, *engine.Env) error) engine.Step {
	return engine.Step{
		Name: step,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if keelboxOwns(e, path) {
				return engine.Pending("remove %s", path), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if err := e.X.Remove(path); err != nil {
				return err
			}
			return onChange(ctx, e)
		},
	}
}

// restartLogind makes logind read its configuration. A restart can end
// graphical sessions, so on a machine with a display manager running it is
// left to the next boot; the masked targets already stop any suspend.
func restartLogind(ctx context.Context, e *engine.Env) error {
	if system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", "display-manager.service")) {
		if e.Log != nil {
			fmt.Fprintln(e.Log, "    logind reads "+LogindDropIn+" at the next boot: a display manager runs, and restarting logind would end its sessions")
		}
		return nil
	}
	_, err := e.X.Run(ctx, system.C("systemctl", "try-restart", "systemd-logind.service"))
	return err
}

// verifyPowerSleep is doctor's view of host.power.sleep: never.
func verifyPowerSleep(ctx context.Context, e *engine.Env) []engine.Finding {
	if e.Cfg.PowerSleep() != config.SleepNever {
		return nil
	}
	_, no := masked(ctx, e)
	if len(no) > 0 {
		return []engine.Finding{fail("host", "sleep", strings.Join(no, ", ")+" not masked: the box can suspend. keelbox apply "+e.Cfg.Box+" masks them")}
	}
	if !keelboxOwns(e, LogindDropIn) {
		return []engine.Finding{warn("host", "sleep", LogindDropIn+" missing: logind may try to suspend on a closed lid (the masked targets refuse it). keelbox apply "+e.Cfg.Box)}
	}
	return []engine.Finding{ok("host", "sleep", "never: sleep targets masked, lid and idle ignored")}
}

// sleepImpact is what host.power.sleep changes on a machine in use, for
// plan --impact and the adoption report (ADR 0038). Nothing in it is cut
// off, so it never gates the first apply.
func sleepImpact(ctx context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	var out []engine.ImpactItem
	never := e.Cfg.PowerSleep() == config.SleepNever
	if d, ok := in.Pending("host", "sleep-targets"); ok {
		it := engine.ImpactItem{Group: engine.ImpactChanges, Subject: "sleep", Detail: d}
		if never {
			it.Detail = "suspend, hibernate and hybrid sleep are refused from now on, whoever asks (" + strings.Join(SleepTargets, ", ") + " masked); reboots and shutdowns work as before"
			it.Hint = "host.power.sleep: allow keeps them"
		}
		out = append(out, it)
	}
	if _, ok := in.Pending("host", "logind-sleep"); ok {
		desktop := system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", "display-manager.service"))
		switch {
		case !never:
			out = append(out, engine.ImpactItem{Group: engine.ImpactChanges, Subject: "logind", Detail: "keelbox removes its lid and idle settings (" + LogindDropIn + ")"})
		case desktop:
			out = append(out, engine.ImpactItem{Group: engine.ImpactChanges, Subject: "logind", Hint: "host.power.sleep: allow keeps them",
				Detail: "a closed lid or an idle desktop no longer suspends the machine, from the next boot (" + LogindDropIn + "; logind is not restarted while a desktop runs)"})
		default:
			out = append(out, engine.ImpactItem{Group: engine.ImpactChanges, Subject: "logind", Hint: "host.power.sleep: allow keeps them",
				Detail: "a closed lid or idle no longer suspends the machine (" + LogindDropIn + ")"},
				engine.ImpactItem{Group: engine.ImpactRestarts, Subject: "systemd-logind", Detail: "restarts to read the lid and idle settings; logins and sessions stay"})
		}
	}
	return out
}
