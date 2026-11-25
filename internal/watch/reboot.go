package watch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/backup"
	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

// Rebooting for an update (docs/adr/0090-reboots-for-updates.md). With
// host.auto_reboot: nightly, the watch run reboots the box when an
// installed update needs it, at night in the box's timezone, once the box
// has been idle for half an hour: no one typing in a login, no desktop
// session, no output in the agent's tmux sessions, no agent conversation
// written or at work, the agent's processes near idle and no backup
// running. With weekly it waits for Sunday night, or the first idle night
// after it. The conversations in tmux are resumed after (resume.go).

const (
	// RebootFrom and RebootTo are the hours, in the box's timezone, in
	// which the watch run may reboot.
	RebootFrom = 1
	RebootTo   = 6
	// QuietFor is how long the box must have been idle.
	QuietFor = 30 * time.Minute
	// recently is how far back output and conversations count as use.
	recently = 5 * time.Minute
	// busyCPU is the agent's share of all CPUs, in percent, above which
	// it is at work.
	busyCPU = 10.0
	// rebootsApart keeps an update that still wants a reboot after one
	// from rebooting the box every night.
	rebootsApart = 20 * time.Hour
	// runGap is a gap between two runs (the timer's is two minutes) after
	// which the box's quiet is counted again.
	runGap = 6 * time.Minute
	// RebootDelay is how long after Restart the box reboots, so that the
	// command or run that asked can finish what it says.
	RebootDelay = 5 * time.Second
)

// RebootState is what the watch run keeps for rebooting.
type RebootState struct {
	// QuietSince is when the box was last seen idle with nothing in use
	// since; nil while something is.
	QuietSince *time.Time `json:"quiet_since,omitempty"`
	// Busy is what was in use at the last look.
	Busy string `json:"busy,omitempty"`
	// Last is when the watch run last rebooted the box.
	Last *time.Time `json:"last,omitempty"`
}

// RebootFix says what happens to a needed reboot under the box's
// host.auto_reboot, and how to reboot now.
func RebootFix(cfg *config.Config) string {
	now := "keelbox reboot " + cfg.Box
	zone := cfg.Host.Timezone
	if zone == "" {
		zone = "UTC"
	}
	switch when := cfg.Host.AutoReboot; when {
	case config.AutoRebootNightly:
		return fmt.Sprintf("the box reboots itself between %02d:00 and %02d:00 %s once idle for %d minutes; to reboot now: %s", RebootFrom, RebootTo, zone, int(QuietFor.Minutes()), now)
	case config.AutoRebootWeekly:
		return fmt.Sprintf("the box reboots itself on Sunday between %02d:00 and %02d:00 %s once idle for %d minutes; to reboot now: %s", RebootFrom, RebootTo, zone, int(QuietFor.Minutes()), now)
	case config.AutoRebootNever, "":
		return now + "; or have the box reboot itself at night once idle: host.auto_reboot: nightly, then keelbox apply " + cfg.Box
	default:
		return "unattended-upgrades reboots the box at " + when + "; to reboot now: " + now
	}
}

// Busy says what on the box is in use, or "" when nothing is. cpu is the
// agent's share of all CPUs since the last run.
func Busy(ctx context.Context, e *engine.Env, cpu float64, now time.Time) string {
	if why := loginInUse(ctx, e); why != "" {
		return why
	}
	agent := e.AgentUser()
	if e.Cfg.Tmux() {
		out, _ := bounded(ctx, e.X, system.As(agent.Name, "tmux", "list-windows", "-a", "-F", "#{window_activity}"))
		for _, l := range strings.Split(out, "\n") {
			n, err := strconv.ParseInt(strings.TrimSpace(l), 10, 64)
			if err == nil && now.Sub(time.Unix(n, 0)) < recently {
				return "the agent's tmux sessions had output in the last " + minutes(recently)
			}
		}
	}
	if agent.Home != "" {
		// Claude Code and Codex write each conversation as they go.
		out, _ := bounded(ctx, e.X, system.As(agent.Name, "find", agent.Home+"/.claude/projects", agent.Home+"/.codex/sessions",
			"-type", "f", "-mmin", "-"+strconv.Itoa(int(recently.Minutes())), "-print", "-quit"))
		if strings.TrimSpace(out) != "" {
			return "an agent conversation was written in the last " + minutes(recently)
		}
	}
	if why := claudeAtWork(ctx, e); why != "" {
		return why
	}
	if cpu > busyCPU {
		return fmt.Sprintf("the agent's processes used %.0f%% of the CPUs", cpu)
	}
	if e.Cfg.BackupEnabled() {
		if out, _ := bounded(ctx, e.X, system.C("systemctl", "is-active", backup.Unit)); strings.HasPrefix(strings.TrimSpace(out), "activ") {
			return "a backup is running"
		}
	}
	return ""
}

// loginInUse says whose login is in use: a desktop session at all, or a
// terminal someone typed in within logind's idle time (a few minutes).
// Sessions of the agent's tmux server and services are not logins.
func loginInUse(ctx context.Context, e *engine.Env) string {
	out, err := bounded(ctx, e.X, system.C("loginctl", "list-sessions", "--no-legend"))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		show, err := bounded(ctx, e.X, system.C("loginctl", "show-session", f[0], "-p", "Name", "-p", "Class", "-p", "Type", "-p", "State", "-p", "IdleHint"))
		if err != nil {
			continue
		}
		kv := engine.ParseEnvFile(show)
		if kv["Class"] != "user" || kv["State"] != "active" && kv["State"] != "online" {
			continue
		}
		switch kv["Type"] {
		case "x11", "wayland", "mir":
			return kv["Name"] + "'s desktop session is logged in"
		}
		if kv["IdleHint"] != "yes" {
			return kv["Name"] + " is logged in and was active in the last few minutes"
		}
	}
	return ""
}

func minutes(d time.Duration) string { return strconv.Itoa(int(d.Minutes())) + " minutes" }

// rebootDue reports why the watch run reboots the box now, or "". It
// looks at what is in use only while a reboot is needed. prev is the
// last run's heartbeat: after a gap the box's quiet is counted afresh.
func rebootDue(ctx context.Context, e *engine.Env, st *State, u *snapshot.Updates, cpu float64, prev, now time.Time, loc *time.Location) string {
	if !e.Cfg.RebootsItself() || u == nil || u.RebootRequired == nil {
		if st.Reboot != nil {
			st.Reboot.QuietSince, st.Reboot.Busy = nil, ""
		}
		return ""
	}
	if st.Reboot == nil {
		st.Reboot = &RebootState{}
	}
	r := st.Reboot
	switch busy := Busy(ctx, e, cpu, now); {
	case busy != "":
		r.QuietSince, r.Busy = nil, busy
	case r.QuietSince == nil || now.Sub(prev) > runGap:
		t := now
		r.QuietSince, r.Busy = &t, ""
	}
	if h := now.In(loc).Hour(); h < RebootFrom || h >= RebootTo {
		return ""
	}
	if r.QuietSince == nil || now.Sub(*r.QuietSince) < QuietFor || r.Last != nil && now.Sub(*r.Last) < rebootsApart {
		return ""
	}
	if e.Cfg.Host.AutoReboot == config.AutoRebootWeekly {
		// Only for an update that was waiting at the start of Sunday's
		// night, and once a week: that night, or the first idle one after.
		week := sundayNight(now.In(loc))
		if !u.RebootRequired.Before(week) || r.Last != nil && !r.Last.Before(week) {
			return ""
		}
	}
	t := now
	r.Last, r.QuietSince = &t, nil
	return "the update installed " + u.RebootRequired.In(loc).Format("2006-01-02 15:04") + " needs it, and the box has been idle for " + minutes(QuietFor)
}

// sundayNight is the start of the reboot window on the last Sunday at or
// before t, in t's location.
func sundayNight(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), RebootFrom, 0, 0, 0, t.Location())
	d = d.AddDate(0, 0, -int(d.Weekday()))
	if d.After(t) {
		d = d.AddDate(0, 0, -7)
	}
	return d
}

// raiseRebooting queues the rebooting event before the run sends its
// events, so it goes out before the box goes down.
func raiseRebooting(e *engine.Env, why string, now time.Time) {
	k, _ := events.Lookup("rebooting")
	detail := why
	if back := AfterReboot(e.Cfg); back != "" {
		detail += ". " + back
	}
	ev := events.Event{Time: now, Source: events.SourceUpdates, Kind: k.Name, Level: k.Level, Box: e.Cfg.Box, Key: "reboot", Fields: map[string]string{"detail": detail}}
	if _, err := events.Emit(e.X, ev); err != nil && e.Log != nil {
		fmt.Fprintf(e.Log, "keelbox watch-run: event rebooting: %v\n", err)
	}
}

// AfterReboot says what comes back after a reboot, or "" when the box
// runs no tmux.
func AfterReboot(c *config.Config) string {
	switch {
	case c.TmuxRecovery():
		return "The tmux sessions and Remote Control come back by themselves, and Claude Code and Codex open their conversations again"
	case c.Tmux():
		return "The agent's tmux sessions are not saved (sessions.recovery is off), so what ran in them does not come back"
	}
	return ""
}

// Restart notes the agent's conversations, saves its tmux sessions, has
// systemd reboot the box RebootDelay from now, so the run or command that
// asked can say so first, and records why in the logbook.
func Restart(ctx context.Context, e *engine.Env, why string, now time.Time) error {
	if e.Cfg.TmuxRecovery() {
		if _, err := noteConversations(ctx, e, now, false); err != nil && e.Log != nil {
			fmt.Fprintf(e.Log, "keelbox: noting the conversations to resume: %v\n", err)
		}
		agent := e.AgentUser()
		// With no server, resurrect's save writes an empty save and points
		// its last at it, so the boot would restore nothing.
		if _, err := bounded(ctx, e.X, system.As(agent.Name, "tmux", "has-session")); err == nil {
			if _, err := bounded(ctx, e.X, system.As(agent.Name, agent.Home+"/.tmux/plugins/tmux-resurrect/scripts/save.sh", "quiet")); err != nil && e.Log != nil {
				fmt.Fprintf(e.Log, "keelbox: saving the tmux sessions before the reboot: %s\n", oneLine(err.Error()))
			}
		}
	}
	if _, err := e.X.Run(ctx, system.C("systemd-run", "--quiet", "--on-active="+strconv.Itoa(int(RebootDelay.Seconds())), "--timer-property=AccuracySec=1s", "systemctl", "reboot")); err != nil {
		return fmt.Errorf("scheduling the reboot: %w", err)
	}
	rec := logbook.Record{Time: now, Kind: logbook.KindPower, Result: logbook.ResultOK,
		Changes: []engine.Change{{Module: "power", Step: "reboot", Detail: "rebooted: " + why}}}
	if err := logbook.Action(e.X, rec); err != nil && e.Log != nil {
		fmt.Fprintf(e.Log, "keelbox: the reboot is not in the logbook: %v\n", err)
	}
	return nil
}
