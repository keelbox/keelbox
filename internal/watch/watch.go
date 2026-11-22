// Package watch is the watch run: what keelbox-watch.timer starts every two
// minutes (ADR 0030). It records a heartbeat, brings back the agent's tmux
// server and Remote Control when they are gone (the watchdog), reports an
// unclean shutdown and a filling disk, writes the status snapshot and sends
// queued events. The whole run must stay within a few seconds.
package watch

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/agentlimits"
	"github.com/keelbox/keelbox/internal/backup"
	"github.com/keelbox/keelbox/internal/doctorrun"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/projects"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/version"
)

// Paths and units on the box.
const (
	Dir       = "/var/lib/keelbox/watch"
	StatePath = Dir + "/state.json"
	Service   = "keelbox-watch.service"
	Timer     = "keelbox-watch.timer"
	// Command is the internal command the service runs.
	Command = "watch-run"
	// StaleAfter is how old the heartbeat may be before doctor fails.
	StaleAfter = 10 * time.Minute
)

// State is what the watch run keeps between runs (0644 root).
type State struct {
	// Heartbeat is when the last run started.
	Heartbeat time.Time `json:"heartbeat"`
	// BootID is the boot the last run saw, so a run can tell the box
	// restarted since.
	BootID string `json:"boot_id,omitempty"`
	// Checks are the watchdog's, by check name.
	Checks map[string]*Check `json:"checks,omitempty"`
	// OfflineSince is when Remote Control's service stopped answering.
	OfflineSince *time.Time `json:"offline_since,omitempty"`
	// Disk is the alert each mount was last reported at.
	Disk map[string]DiskAlert `json:"disk,omitempty"`
	// TmuxSaved is when the watch run last saved the agent's sessions.
	TmuxSaved time.Time `json:"tmux_saved,omitempty"`
	// Updates are the pending OS updates last counted.
	Updates *UpdatesState `json:"updates,omitempty"`
	// Reboot is what the watch run saw of the box's use while a reboot
	// was needed, and when it last rebooted.
	Reboot *RebootState `json:"reboot,omitempty"`
}

// ReadState reads the state; found is false before the first run.
func ReadState(x system.Executor) (State, bool) {
	var s State
	data, err := x.ReadFile(StatePath)
	if err != nil || json.Unmarshal(data, &s) != nil {
		return State{}, false
	}
	return s, true
}

func writeState(x system.Executor, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return x.WriteFile(StatePath, append(b, '\n'), 0o644, "root", "root")
}

// Run is one watch run.
func Run(ctx context.Context, e *engine.Env, senders []events.Sender, now time.Time) error {
	st, _ := ReadState(e.X)
	prev := st.Heartbeat
	st.Heartbeat = now
	if id := bootID(e.X); id != "" {
		st.BootID = id
	}
	if err := writeState(e.X, st); err != nil {
		return err
	}
	obs := observe(ctx, e)
	if e.Cfg.Watchdog() {
		runWatchdog(ctx, e, &st, &obs, now)
	}
	saveSessions(ctx, e, &st, obs, now)
	if obs.TmuxUp {
		resumeConversations(ctx, e, now)
	}
	before, _, _ := snapshot.Read(e.X)
	s := Collect(ctx, e, now)
	s.Sessions = sessionsOf(e, obs)
	diskAlerts(e, &st, s.Disk, now)
	memoryAlert(ctx, e, before.AgentUsage, s.AgentUsage, now)
	s.Updates = updates(ctx, e, &st, now)
	var cpu float64
	if s.AgentUsage != nil {
		cpu = s.AgentUsage.CPUPercent
	}
	reboot := rebootDue(ctx, e, &st, s.Updates, cpu, prev, now, Location)
	if reboot != "" {
		raiseRebooting(e, reboot, now)
	}
	if s.Power != nil {
		reportPower(e, obs, s.Power.LastBoot, now, tailnetState(ctx, e))
		if p, ok := ReadPower(e.X); ok && p.Unclean != nil && p.Unclean.Boot == p.Boot && p.Boot == st.BootID {
			s.Power.Unclean = true
		}
	}
	if err := writeState(e.X, st); err != nil {
		return err
	}
	if err := snapshot.Write(e.X, s); err != nil {
		return err
	}
	_, err := events.Dispatch(ctx, e.X, senders, now)
	if reboot != "" {
		return Restart(ctx, e, reboot, now)
	}
	return err
}

// Location is the box's timezone, which apply sets from host.timezone;
// tests replace it.
var Location = time.Local

func sessionsOf(e *engine.Env, o Observed) *snapshot.Sessions {
	if !e.Cfg.Tmux() && !e.Cfg.RemoteControl() {
		return nil
	}
	return &snapshot.Sessions{Tmux: o.TmuxUp, Names: o.Names, RemoteControl: o.RCSummary}
}

// tailnetState is "connected" or "not connected", for the power report;
// empty without Tailscale. It runs only when there is something to report.
func tailnetState(ctx context.Context, e *engine.Env) func() string {
	return func() string {
		if !e.Cfg.Tailscale() {
			return ""
		}
		out, err := bounded(ctx, e.X, system.C("tailscale", "status", "--json"))
		var st struct{ BackendState string }
		if err == nil && json.Unmarshal([]byte(out), &st) == nil && st.BackendState == "Running" {
			return "connected"
		}
		return "not connected"
	}
}

// Collect builds the snapshot from what is cheap to read: the logbook, the
// disks, the backup status, the boot time and the agent's projects (git,
// as the agent, within a few seconds: internal/projects). A part that cannot be read is
// left out rather than failing the run. Run adds the sessions.
func Collect(ctx context.Context, e *engine.Env, now time.Time) snapshot.Status {
	s := snapshot.Status{Written: now, Box: e.Cfg.Box, Keelbox: version.Version, Release: e.M.Release,
		Agent: e.Cfg.Users.Agent.Name, Host: snapshot.ReadHost(ctx, e.X), ReportsOff: !e.Cfg.ReportsEnabled()}
	recs, _ := logbook.Read(e.X)
	if last := logbook.Last(recs, logbook.KindApply); last != nil {
		s.LastApply = &snapshot.Apply{Time: last.Time, Result: last.Result, Keelbox: last.Keelbox}
		s.Release = last.Release
	}
	// df fails for a path that does not exist (/var/lib/docker without
	// Docker), and --output cannot be combined with -P.
	args := []string{"--output=target,pcent", "--"}
	for _, p := range []string{"/", "/home", "/var/lib/docker"} {
		if st, err := e.X.Stat(p); err == nil && st.Exists {
			args = append(args, p)
		}
	}
	if out, err := system.Output(ctx, e.X, system.C("df", args...)); err == nil {
		s.Disk = parseDF(out)
	}
	if e.Cfg.BackupEnabled() {
		if b, found := backup.ReadStatus(e); found {
			s.Backup = &snapshot.Backup{LastFailed: b.Error != ""}
			if !b.LastSuccess.IsZero() {
				t := b.LastSuccess
				s.Backup.LastSuccess = &t
			}
		}
	}
	s.Doctor = doctorrun.Snapshot(e.X)
	s.AgentUsage = agentUsage(ctx, e, now)
	s.Projects = projects.Probe(ctx, e.X, e.Cfg)
	if st, err := installs.ReadState(e.X); err == nil {
		s.RequestsWaiting = len(st.Pending())
	}
	if boot, ok := bootTime(e.X); ok {
		s.Power = &snapshot.Power{LastBoot: boot}
	}
	return s
}

// parseDF reads `df --output=target,pcent`, one row per mount even when
// several paths share one.
func parseDF(out string) []snapshot.Disk {
	var disks []snapshot.Disk
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n")[1:] {
		f := strings.Fields(l)
		if len(f) != 2 || seen[f[0]] {
			continue
		}
		p, err := strconv.Atoi(strings.TrimSuffix(f[1], "%"))
		if err != nil {
			continue
		}
		seen[f[0]] = true
		disks = append(disks, snapshot.Disk{Mount: f[0], Percent: p})
	}
	return disks
}

// bootTime reads btime from /proc/stat.
func bootTime(x system.Executor) (time.Time, bool) {
	data, err := x.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(n, 0).UTC(), true
			}
		}
	}
	return time.Time{}, false
}

// agentUsage samples the agent's slice against the last snapshot's sample:
// its CPU share since then, and when its limits last held it back.
func agentUsage(ctx context.Context, e *engine.Env, now time.Time) *snapshot.AgentUsage {
	u := e.AgentUser()
	if u.UID < 0 {
		return nil
	}
	c, found := agentlimits.ReadAs(e.X, e.FS(u.Name), u.UID)
	if !found {
		return nil
	}
	cpus := 0
	if out, err := system.Output(ctx, e.X, system.C("nproc")); err == nil {
		cpus, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	prev, _, _ := snapshot.Read(e.X)
	return agentlimits.Usage(c, prev.AgentUsage, now, cpus)
}
