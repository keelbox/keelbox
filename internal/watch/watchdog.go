package watch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/system"
)

// The session watchdog (docs/adr/0039-session-watchdog.md): each watch run
// brings back the agent's tmux server and Remote Control when they are
// gone, within a budget, and records every restart. It acts only on a unit
// that is down: it never stops, restarts or signals a tmux server or a
// Remote Control server that is running, so it never ends an agent's work.

// The agent's user units the watchdog looks after; the sessions and agents
// modules install them.
const (
	TmuxUnit          = "keelbox-tmux.service"
	RemoteControlUnit = "keelbox-claude-remote-control.service"
)

// Checks, by the name the state and the logbook use.
const (
	CheckTmux          = "tmux"
	CheckRemoteControl = "remote-control"
)

// Budgets.
const (
	// RestartsPerHour is how many times a check restarts its unit in an
	// hour; after that it gives up, says so once, and tries hourly.
	RestartsPerHour = 3
	// RemoteControlCooldown is how long a Remote Control unit that
	// systemd has given up on (start limit) waits before a reset.
	RemoteControlCooldown = 15 * time.Minute
	// RemoteControlResetsPerDay caps those resets; after that the
	// watchdog stops until an apply or keelbox login starts it again.
	RemoteControlResetsPerDay = 4
	// onlineTimeout bounds the Remote Control route check.
	onlineTimeout = 3 * time.Second
	// commandTimeout bounds each systemctl and tmux call.
	commandTimeout = 20 * time.Second
)

// remoteControlHost is what Remote Control connects to; no route there
// means a stopped server is waiting for the network, not broken.
const remoteControlHost = "api.anthropic.com:443"

// dial checks the route to Remote Control's service (a TCP connect, as
// root). Tests replace it.
var dial = func(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: onlineTimeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}

// Check is one watched unit's history.
type Check struct {
	// Restarts are the watchdog's restarts in the last day.
	Restarts []time.Time `json:"restarts,omitempty"`
	// DownSince is when a run first found the unit down.
	DownSince *time.Time `json:"down_since,omitempty"`
	// GaveUp is when the budget ran out; cleared once the unit has run for
	// an hour without a restart.
	GaveUp *time.Time `json:"gave_up,omitempty"`
	// Last is the last thing the watchdog did, for keelbox status.
	Last *Action `json:"last,omitempty"`
}

// Action is one restart or give-up.
type Action struct {
	Time   time.Time `json:"time"`
	Detail string    `json:"detail"`
	Result string    `json:"result"`
}

func (c *Check) restartsWithin(now time.Time, d time.Duration) int {
	n := 0
	for _, t := range c.Restarts {
		if now.Sub(t) < d {
			n++
		}
	}
	return n
}

func (c *Check) lastRestart() time.Time {
	if len(c.Restarts) == 0 {
		return time.Time{}
	}
	return c.Restarts[len(c.Restarts)-1]
}

// prune keeps a day of restarts.
func (c *Check) prune(now time.Time) {
	keep := c.Restarts[:0]
	for _, t := range c.Restarts {
		if now.Sub(t) < 24*time.Hour {
			keep = append(keep, t)
		}
	}
	c.Restarts = keep
}

// Observed is what a run saw of the agent's sessions.
type Observed struct {
	TmuxUp bool
	// TmuxUnsure is a look at the server that did not answer: timed out,
	// killed or failed without tmux saying there is no server. The agent's
	// commands run in its slice, so under its memory limit's slow-down a
	// look can take longer than commandTimeout; a running server must not
	// be restarted for that.
	TmuxUnsure bool
	Names      []string
	// RC is the Remote Control unit's state; empty when it is off.
	RC unitState
	// RCSummary is one line for the snapshot and the power report.
	RCSummary string
}

type unitState struct{ Active, Sub, Result string }

func (u unitState) running() bool {
	switch u.Active {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true // systemd is running it, or restarting it itself
	}
	return false
}

func userctl(user string, args ...string) system.Cmd {
	return system.C("systemctl", append([]string{"--user", "-M", user + "@"}, args...)...)
}

// noServer reports whether a tmux client's error says there is no server:
// tmux exits 1 with "no server running on SOCKET" or "error connecting to
// SOCKET" then. Anything else (a timeout, a kill) says nothing about it.
func noServer(err error) bool {
	var ee *system.ExitError
	if !errors.As(err, &ee) || ee.Result.ExitCode != 1 {
		return false
	}
	return strings.Contains(ee.Result.Stderr, "no server running") || strings.Contains(ee.Result.Stderr, "error connecting to")
}

func bounded(ctx context.Context, x system.Executor, c system.Cmd) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return system.Output(ctx, x, c)
}

// observe reads the sessions without changing anything.
func observe(ctx context.Context, e *engine.Env) Observed {
	var o Observed
	agent := e.Cfg.Users.Agent.Name
	if e.Cfg.Tmux() {
		out, err := bounded(ctx, e.X, system.As(agent, "tmux", "list-sessions", "-F", "#S"))
		o.TmuxUp = err == nil
		o.TmuxUnsure = err != nil && !noServer(err)
		for _, n := range strings.Split(out, "\n") {
			if n = strings.TrimSpace(n); n != "" && err == nil {
				o.Names = append(o.Names, n)
			}
		}
	}
	if e.Cfg.RemoteControl() {
		o.RC = readUnit(ctx, e, RemoteControlUnit)
		o.RCSummary = "stopped"
		switch {
		case o.RC.Active == "active" && o.RC.Sub == "running":
			o.RCSummary = "running"
		case o.RC.running():
			o.RCSummary = "restarting"
		case o.RC.Active == "failed":
			o.RCSummary = "failed (" + o.RC.Result + ")"
		case o.RC.Active == "":
			o.RCSummary = "unknown"
		}
	}
	return o
}

func readUnit(ctx context.Context, e *engine.Env, unit string) unitState {
	out, err := bounded(ctx, e.X, userctl(e.Cfg.Users.Agent.Name, "show", "-p", "ActiveState,SubState,Result", unit))
	if err != nil {
		return unitState{}
	}
	kv := engine.ParseEnvFile(out)
	return unitState{Active: kv["ActiveState"], Sub: kv["SubState"], Result: kv["Result"]}
}

// watchdog is one run's watchdog: it acts on what observe found and
// updates obs to what it left.
type watchdog struct {
	e   *engine.Env
	st  *State
	obs *Observed
	now time.Time
}

func (st *State) check(name string) *Check {
	if st.Checks == nil {
		st.Checks = map[string]*Check{}
	}
	c := st.Checks[name]
	if c == nil {
		c = &Check{}
		st.Checks[name] = c
	}
	return c
}

func runWatchdog(ctx context.Context, e *engine.Env, st *State, obs *Observed, now time.Time) {
	w := watchdog{e: e, st: st, obs: obs, now: now}
	if e.Cfg.TmuxStartOnBoot() {
		w.tmux(ctx)
	}
	if e.Cfg.RemoteControl() {
		w.remoteControl(ctx)
	} else {
		st.OfflineSince = nil
	}
	for _, c := range st.Checks {
		c.prune(now)
	}
}

func (w *watchdog) tmux(ctx context.Context) {
	c := w.st.check(CheckTmux)
	agent := w.e.AgentUser()
	if w.obs.TmuxUp {
		w.up(c, CheckTmux, TmuxUnit, "the tmux server is running again", false)
		return
	}
	if w.obs.TmuxUnsure {
		return
	}
	if c.DownSince == nil {
		c.DownSince = &w.now
	}
	if !w.mayRestart(c) {
		w.giveUp(c, CheckTmux, TmuxUnit, fmt.Sprintf("the tmux server keeps stopping: %d restarts in the last hour; the watchdog now tries once an hour", RestartsPerHour),
			"look at what ends it: journalctl --user -u "+TmuxUnit+" (as "+agent.Name+"), then keelbox apply "+w.e.Cfg.Box)
		return
	}
	// The unit is a oneshot that stays active after the server it started
	// is gone, so systemd never notices; a restart runs its ExecStart,
	// which starts the server, and Continuum restores the last save. Its
	// ExecStop saves only while a server runs, so the save is kept.
	err := w.startUserManager(ctx)
	if err == nil {
		_, err = bounded(ctx, w.e.X, userctl(agent.Name, "restart", TmuxUnit))
	}
	if err == nil {
		_, err = bounded(ctx, w.e.X, system.As(agent.Name, "tmux", "has-session"))
	}
	w.restarted(c, CheckTmux, TmuxUnit, "restarted "+TmuxUnit+" for "+agent.Name+" (the tmux server was gone)", err)
	if err == nil {
		w.obs.TmuxUp = true
		if out, err := bounded(ctx, w.e.X, system.As(agent.Name, "tmux", "list-sessions", "-F", "#S")); err == nil {
			w.obs.Names = strings.Fields(out)
		}
	}
}

func (w *watchdog) startUserManager(ctx context.Context) error {
	u, err := w.e.X.LookupUser(w.e.Cfg.Users.Agent.Name)
	if err != nil {
		return err
	}
	_, err = bounded(ctx, w.e.X, system.C("systemctl", "start", fmt.Sprintf("user@%d.service", u.UID)))
	return err
}

// mayRestart applies the budget: RestartsPerHour in any hour, then once an
// hour until the unit stays up.
func (w *watchdog) mayRestart(c *Check) bool {
	if c.GaveUp != nil {
		return w.now.Sub(c.lastRestart()) >= time.Hour
	}
	return c.restartsWithin(w.now, time.Hour) < RestartsPerHour
}

func (w *watchdog) remoteControl(ctx context.Context) {
	c := w.st.check(CheckRemoteControl)
	agent := w.e.Cfg.Users.Agent.Name
	rc := w.obs.RC
	if rc.Active == "" {
		return // the agent's user manager did not answer; the tmux check starts it
	}
	if rc.running() {
		w.st.OfflineSince = nil
		// After a give-up the watchdog starts nothing, so a running unit
		// means an apply or keelbox login started it: a fresh budget.
		w.up(c, CheckRemoteControl, RemoteControlUnit, "Remote Control is running again", true)
		return
	}
	// Not running. Before the user's sign-in that is how it should be.
	if _, err := bounded(ctx, w.e.X, system.As(agent, w.e.AgentTool("claude"), "auth", "status")); err != nil {
		w.obs.RCSummary = "waiting for the Claude Code sign-in (keelbox login)"
		c.DownSince, w.st.OfflineSince = nil, nil
		return
	}
	// Offline, the server exits after about ten minutes; it is waiting for
	// the network, and starting it would only fail again.
	if err := dial(ctx, remoteControlHost); err != nil {
		if w.st.OfflineSince == nil {
			w.st.OfflineSince = &w.now
		}
		w.obs.RCSummary = "offline since " + w.st.OfflineSince.Local().Format("2006-01-02 15:04")
		return
	}
	wasOffline := w.st.OfflineSince != nil
	w.st.OfflineSince = nil
	if c.DownSince == nil {
		c.DownSince = &w.now
	}
	fix := "keelbox login " + w.e.Cfg.Box + " (Remote Control's first run asks on a terminal); logs, as " + agent + ": journalctl --user -u " + RemoteControlUnit
	if rc.Active == "failed" {
		// systemd stopped restarting it (start limit, six an hour). Wait,
		// then reset it, a few times a day; then leave it to the user.
		if c.GaveUp != nil {
			if !w.appliedSince(*c.GaveUp) {
				w.obs.RCSummary = "failed; the watchdog stopped trying (keelbox login " + w.e.Cfg.Box + ")"
				return
			}
			c.GaveUp, c.Restarts = nil, nil
		}
		since := *c.DownSince
		if l := c.lastRestart(); l.After(since) {
			since = l
		}
		if w.now.Sub(since) < RemoteControlCooldown && !wasOffline {
			return
		}
		if c.restartsWithin(w.now, 24*time.Hour) >= RemoteControlResetsPerDay {
			w.giveUp(c, CheckRemoteControl, RemoteControlUnit,
				fmt.Sprintf("Remote Control keeps failing: %d resets today did not keep it running; the watchdog stops until the next apply or sign-in", RemoteControlResetsPerDay), fix)
			return
		}
		_, err := bounded(ctx, w.e.X, userctl(agent, "reset-failed", RemoteControlUnit))
		if err == nil {
			_, err = bounded(ctx, w.e.X, userctl(agent, "start", RemoteControlUnit))
		}
		w.restarted(c, CheckRemoteControl, RemoteControlUnit, "reset and started "+RemoteControlUnit+" for "+agent+" (systemd had stopped restarting it: "+rc.Result+")", w.verifyRC(ctx, err))
		return
	}
	// Stopped without failing: an exit systemd does not restart, a stop
	// by hand, or a start skipped before the sign-in.
	if !w.mayRestart(c) {
		w.giveUp(c, CheckRemoteControl, RemoteControlUnit, fmt.Sprintf("Remote Control keeps stopping: %d starts in the last hour", RestartsPerHour), fix)
		return
	}
	_, err := bounded(ctx, w.e.X, userctl(agent, "start", RemoteControlUnit))
	reason := "it was stopped"
	if wasOffline {
		reason = "the network is back"
	}
	w.restarted(c, CheckRemoteControl, RemoteControlUnit, "started "+RemoteControlUnit+" for "+agent+" ("+reason+")", w.verifyRC(ctx, err))
}

func (w *watchdog) verifyRC(ctx context.Context, err error) error {
	if err != nil {
		return err
	}
	st := readUnit(ctx, w.e, RemoteControlUnit)
	w.obs.RC = st
	if !st.running() {
		return fmt.Errorf("%s is %s/%s after the start", RemoteControlUnit, st.Active, st.Sub)
	}
	w.obs.RCSummary = "running"
	return nil
}

// appliedSince reports whether an apply ran after t: it starts Remote
// Control again, and so may the watchdog.
func (w *watchdog) appliedSince(t time.Time) bool {
	recs, _ := logbook.Read(w.e.X)
	last := logbook.Last(recs, logbook.KindApply)
	return last != nil && last.Time.After(t)
}

// up notes a unit found running. After a give-up it says so, and the
// budget starts again: at once when fresh is set, otherwise once the unit
// has run for an hour without a restart.
func (w *watchdog) up(c *Check, name, unit, detail string, fresh bool) {
	c.DownSince = nil
	if c.GaveUp == nil {
		return
	}
	if fresh {
		c.Restarts = nil
	} else if w.now.Sub(c.lastRestart()) < time.Hour {
		return
	} else {
		detail += " and has stayed up for an hour"
	}
	c.GaveUp = nil
	w.emit("watch-recovered", Action{Time: w.now, Detail: detail, Result: logbook.ResultOK}, name, unit, "")
}

// restarted records a restart in the logbook and the state and raises
// watch-recovered when it worked. When it did not and the budget is now
// spent, it gives up.
func (w *watchdog) restarted(c *Check, name, unit, detail string, err error) {
	c.Restarts = append(c.Restarts, w.now)
	a := Action{Time: w.now, Detail: detail, Result: logbook.ResultOK}
	if err != nil {
		a.Result, a.Detail = logbook.ResultFailed, detail+": "+oneLine(err.Error())
	}
	c.Last = &a
	rec := logbook.Record{Time: w.now, Kind: logbook.KindWatch, Result: a.Result, Error: errText(err),
		Changes: []engine.Change{{Module: "watch", Step: name, Detail: detail}}}
	if lerr := logbook.Action(w.e.X, rec); lerr != nil {
		w.logf("keelbox watch-run: logbook: %v", lerr)
	}
	w.logf("keelbox watch-run: %s: %s", name, a.Detail)
	if err == nil {
		c.DownSince = nil
		if c.GaveUp == nil {
			w.emit("watch-recovered", a, name, unit, "")
		}
	}
}

func (w *watchdog) giveUp(c *Check, name, unit, detail, fix string) {
	if c.GaveUp != nil {
		return
	}
	c.GaveUp = &w.now
	a := Action{Time: w.now, Detail: detail, Result: logbook.ResultFailed}
	c.Last = &a
	w.logf("keelbox watch-run: %s: %s", name, detail)
	w.emit("watch-gave-up", a, name, unit, fix)
}

func (w *watchdog) emit(kind string, a Action, name, unit, fix string) {
	k, _ := events.Lookup(kind)
	f := map[string]string{"unit": unit, "detail": a.Detail}
	if fix != "" {
		f["fix"] = fix
	}
	ev := events.Event{Time: w.now, Source: events.SourceWatch, Kind: kind, Level: k.Level, Box: w.e.Cfg.Box, Key: "watch:" + name, Fields: f}
	if _, err := events.Emit(w.e.X, ev); err != nil {
		w.logf("keelbox watch-run: event %s: %v", kind, err)
	}
}

func (w *watchdog) logf(format string, a ...any) {
	if w.e.Log != nil {
		fmt.Fprintf(w.e.Log, format+"\n", a...)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return oneLine(err.Error())
}

func oneLine(s string) string {
	s = plain.Line(s)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// SaveEvery is how often the watch run saves the agent's tmux sessions.
const SaveEvery = 15 * time.Minute

// saveSessions runs tmux-resurrect's save as the agent every SaveEvery
// while its server runs. Continuum saves from the status line, which tmux
// draws only while a client is attached, so a box whose agents work
// detached would otherwise keep a save as old as the last attach, and a
// restart or power loss would bring back that. The save only reads the
// server's layout.
func saveSessions(ctx context.Context, e *engine.Env, st *State, obs Observed, now time.Time) {
	if !e.Cfg.TmuxRecovery() || !obs.TmuxUp || now.Sub(st.TmuxSaved) < SaveEvery {
		return
	}
	agent := e.AgentUser()
	if _, err := bounded(ctx, e.X, system.As(agent.Name, agent.Home+"/.tmux/plugins/tmux-resurrect/scripts/save.sh", "quiet")); err != nil {
		if e.Log != nil {
			fmt.Fprintf(e.Log, "keelbox watch-run: saving the tmux sessions: %s\n", oneLine(err.Error()))
		}
		return
	}
	st.TmuxSaved = now
}

// StatusRows are keelbox status's day-two rows from the watch state: the
// watchdog's last action or give-up, and how the last boot ended.
func StatusRows(x system.Executor, watchdogOn bool) [][2]string {
	var rows [][2]string
	st, found := ReadState(x)
	switch {
	case !watchdogOn:
		rows = append(rows, [2]string{"watchdog", "off (sessions.watchdog: false)"})
	case !found:
		rows = append(rows, [2]string{"watchdog", "no watch run yet"})
	default:
		v := "ok; nothing restarted"
		var last *Action
		for _, name := range []string{CheckTmux, CheckRemoteControl} {
			c := st.Checks[name]
			if c == nil {
				continue
			}
			if c.GaveUp != nil {
				v = "gave up on " + name + " at " + c.GaveUp.Local().Format("2006-01-02 15:04")
			}
			if c.Last != nil && (last == nil || c.Last.Time.After(last.Time)) {
				last = c.Last
			}
		}
		if last != nil {
			if strings.HasPrefix(v, "ok") {
				v = "ok"
			}
			v += "; last action " + last.Time.Local().Format("2006-01-02 15:04") + " " + last.Detail
		}
		rows = append(rows, [2]string{"watchdog", v})
	}
	if p, ok := ReadPower(x); ok && !p.Started.IsZero() {
		v := "this boot " + p.Started.Local().Format("2006-01-02 15:04")
		if u := p.Unclean; u != nil && u.Boot == p.Boot {
			v += "; the boot before ended without a clean shutdown (power loss, a crash or a hard reset)" + u.window(time.Local)
		} else if u != nil {
			v += "; last unclean shutdown found " + u.Found.Local().Format("2006-01-02 15:04")
		}
		rows = append(rows, [2]string{"power", v})
	}
	return rows
}
