package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// The agent may not schedule jobs with cron or at (ADR 0055). Ubuntu's cron
// and atd start a job in their own service's cgroup (cron's PAM stack has
// no pam_systemd), outside the agent's slice, so users.agent.limits would
// not hold for it, and a crontab is a way to keep code running that no unit
// of keelbox's knows about. The agent's own systemd timers run in its user
// manager, inside its slice, and stay open to it.

// scheduler is cron or at: the program that installs jobs, and its allow
// and deny files.
type scheduler struct {
	name, bin, allow, deny string
}

var (
	cronScheduler = scheduler{name: "cron", bin: "/usr/bin/crontab", allow: "/etc/cron.allow", deny: "/etc/cron.deny"}
	atScheduler   = scheduler{name: "at", bin: "/usr/bin/at", allow: "/etc/at.allow", deny: "/etc/at.deny"}
)

// cronSpool holds each account's crontab. crontab -l -u and -r -u refuse a
// denied account even to root, so its crontab is found and removed here.
// The directory is writable by group crontab (crontab's setgid group):
// find only lists it and rm -f unlinks the entry itself, never a target.
const cronSpool = "/var/spool/cron/crontabs"

func (s scheduler) installed(e *engine.Env) bool {
	st, err := e.X.Stat(s.bin)
	return err == nil && st.Exists
}

// listed reports whether path names user on a line of its own.
func listed(e *engine.Env, path, user string) (exists, has bool) {
	data, err := e.X.ReadFile(path)
	if err != nil {
		return false, false
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == user {
			return true, true
		}
	}
	return true, false
}

// allowed reports whether user may install jobs: with an allow file, only
// the users it names; without one, everyone the deny file does not name.
func (s scheduler) allowed(e *engine.Env, user string) bool {
	if exists, has := listed(e, s.allow, user); exists {
		return has
	}
	_, denied := listed(e, s.deny, user)
	return !denied
}

// jobs are the user's installed jobs: a crontab, or at's job numbers.
func (s scheduler) jobs(ctx context.Context, e *engine.Env, user string) []string {
	if s.name == "cron" {
		out, err := system.Output(ctx, e.X, system.C("find", cronSpool, "-mindepth", "1", "-maxdepth", "1", "-name", user))
		if err == nil && strings.TrimSpace(out) != "" {
			return []string{"crontab"}
		}
		return nil
	}
	out, err := system.Output(ctx, e.X, system.C("atq"))
	if err != nil {
		return nil
	}
	var ids []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[len(f)-1] == user {
			ids = append(ids, f[0])
		}
	}
	return ids
}

func agentSchedulerSteps(agent string) []engine.Step {
	var steps []engine.Step
	for _, s := range []scheduler{cronScheduler, atScheduler} {
		steps = append(steps, s.denyStep(agent), s.noJobsStep(agent))
	}
	return steps
}

// denyStep keeps user out of the scheduler: a line in its deny file, which
// counts while there is no allow file. An allow file is the owner's; one
// that names the agent is left to them, with the fix.
func (s scheduler) denyStep(user string) engine.Step {
	line := engine.LineStep("agent-no-"+s.name, s.deny, user, 0o644, "root")
	return engine.Step{
		Name: line.Name,
		// An allow file naming the agent stops this step, not the box's.
		Soft: true,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if !s.installed(e) || !s.allowed(e, user) {
				return engine.Converged(), nil
			}
			if exists, _ := listed(e, s.allow, user); exists {
				return engine.Pending("%s lets %s schedule %s jobs, which run outside its limits", s.allow, user, s.name), nil
			}
			return engine.Pending("deny %s jobs to %s in %s", s.name, user, s.deny), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if exists, _ := listed(e, s.allow, user); exists {
				return fmt.Errorf("%s names %s, so it can schedule %s jobs that run outside users.agent.limits; remove that line, then run keelbox apply %s",
					s.allow, user, s.name, e.Cfg.Box)
			}
			return line.Apply(ctx, e)
		},
	}
}

// noJobsStep removes jobs the agent installed before it was denied: cron
// and atd run a denied user's existing jobs all the same.
func (s scheduler) noJobsStep(user string) engine.Step {
	return engine.Step{
		Name: "agent-no-" + s.name + "-jobs",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if !s.installed(e) {
				return engine.Converged(), nil
			}
			if j := s.jobs(ctx, e, user); len(j) > 0 {
				return engine.Pending("remove %s's %s jobs (%s), which run outside its limits", user, s.name, strings.Join(j, " ")), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if s.name == "cron" {
				_, err := e.X.Run(ctx, system.C("rm", "-f", "--", cronSpool+"/"+user))
				return err
			}
			if j := s.jobs(ctx, e, user); len(j) > 0 {
				_, err := e.X.Run(ctx, system.C("atrm", j...))
				return err
			}
			return nil
		},
	}
}

// verifyAgentSchedulers fails while the agent may schedule jobs or has
// some.
func verifyAgentSchedulers(ctx context.Context, e *engine.Env) []engine.Finding {
	user := e.Cfg.Users.Agent.Name
	var out []engine.Finding
	for _, s := range []scheduler{cronScheduler, atScheduler} {
		if !s.installed(e) {
			continue
		}
		check := "agent-no-" + s.name
		switch j := s.jobs(ctx, e, user); {
		case s.allowed(e, user):
			out = append(out, fail("host", check, fmt.Sprintf("%s can schedule %s jobs, which run outside its limits: keelbox apply %s", user, s.name, e.Cfg.Box)))
		case len(j) > 0:
			out = append(out, fail("host", check, fmt.Sprintf("%s has %s jobs (%s), which run outside its limits: keelbox apply %s removes them", user, s.name, strings.Join(j, " "), e.Cfg.Box)))
		default:
			out = append(out, ok("host", check, user+" cannot schedule "+s.name+" jobs"))
		}
	}
	return out
}
