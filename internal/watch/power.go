package watch

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/system"
)

// Power-loss and unclean-shutdown detection
// (docs/adr/0040-unclean-shutdown-and-sleep.md). keelbox-boot.service
// starts at boot and stays active; systemd stops it at every orderly
// shutdown or reboot, and its stop writes a marker. A boot that finds no
// marker after an earlier boot, and is not the same boot, followed an
// unclean end: power loss, a crash or a hard reset. The watch run reports
// it once the box has settled. A stop within a boot (an apply restarting
// the unit) writes the marker too; the start that follows, in the same
// boot, removes it.

// Paths and the unit.
const (
	PowerDir       = "/var/lib/keelbox/power"
	PowerStatePath = PowerDir + "/state.json"
	CleanMarker    = PowerDir + "/clean-shutdown"
	BootUnit       = "keelbox-boot.service"
	// BootCommand is the internal command the unit starts with. It needs
	// no config, so a broken config never hides a crash. The unit's stop
	// is a plain touch of the marker, so it works whatever keelbox is
	// installed by then, including after a rollback.
	BootCommand = "boot-mark"
	// PowerSettle is how long after boot the report waits, so it can say
	// what came back.
	PowerSettle = 3 * time.Minute
)

// PowerState is what keelbox-boot.service keeps (0644 root).
type PowerState struct {
	// Boot is the boot the unit last started in.
	Boot    string    `json:"boot"`
	Started time.Time `json:"started"`
	// Unclean is the last unclean end found, kept after it is reported.
	Unclean *Unclean `json:"unclean,omitempty"`
}

// Unclean is one boot that ended without a clean shutdown.
type Unclean struct {
	// Boot is the boot that found it: the one after the unclean end.
	Boot  string    `json:"boot"`
	Found time.Time `json:"found"`
	// PreviousBoot is the boot that ended uncleanly, and PreviousStart
	// when it started.
	PreviousBoot  string    `json:"previous_boot"`
	PreviousStart time.Time `json:"previous_start"`
	// LastHeartbeat is the previous boot's last watch run, when there
	// was one: the box went down within two minutes after it.
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	Reported      *time.Time `json:"reported,omitempty"`
}

// ReadPower reads the state; found is false before the unit first ran.
func ReadPower(x system.Executor) (PowerState, bool) {
	var s PowerState
	data, err := x.ReadFile(PowerStatePath)
	if err != nil || json.Unmarshal(data, &s) != nil {
		return PowerState{}, false
	}
	return s, true
}

func writePower(x system.Executor, s PowerState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return x.WriteFile(PowerStatePath, append(b, '\n'), 0o644, "root", "root")
}

func bootID(x system.Executor) string {
	id, err := x.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(id))
}

// BootStart is keelbox-boot.service's start. It returns what it found, for
// the journal. The first start ever, and a restart within one boot (an
// apply that changed the unit), find nothing.
func BootStart(x system.Executor, now time.Time) (string, error) {
	cur := bootID(x)
	if cur == "" {
		return "", fmt.Errorf("cannot read the boot id")
	}
	if err := x.MkdirAll(PowerDir, 0o755, "root", "root"); err != nil {
		return "", err
	}
	st, found := ReadPower(x)
	_, mErr := x.ReadFile(CleanMarker)
	clean := mErr == nil
	if clean {
		if err := x.Remove(CleanMarker); err != nil {
			return "", err
		}
	}
	msg := "first start: nothing to compare"
	switch {
	case found && st.Boot == cur:
		msg = "started again in the same boot"
	case found && clean:
		msg = "the last boot ended with a clean shutdown"
	case found:
		u := &Unclean{Boot: cur, Found: now, PreviousBoot: st.Boot, PreviousStart: st.Started}
		if w, ok := ReadState(x); ok && w.BootID == st.Boot && !w.Heartbeat.IsZero() {
			hb := w.Heartbeat
			u.LastHeartbeat = &hb
		}
		st.Unclean = u
		msg = "the last boot ended without a clean shutdown (power loss, a crash or a hard reset)" + u.window(time.UTC)
		rec := logbook.Record{Time: now, Kind: logbook.KindPower, Result: logbook.ResultOK,
			Changes: []engine.Change{{Module: "power", Step: "unclean-shutdown", Detail: msg}}}
		if err := logbook.Action(x, rec); err != nil {
			msg += "; not in the logbook: " + err.Error()
		}
	}
	st.Boot, st.Started = cur, now
	return msg, writePower(x, st)
}

// window says when the box went down, from the previous boot's last
// heartbeat: within the two minutes after it.
func (u *Unclean) window(loc *time.Location) string {
	if u.LastHeartbeat == nil {
		return ""
	}
	from := u.LastHeartbeat.In(loc)
	to := from.Add(2 * time.Minute)
	return fmt.Sprintf(" between %s and %s", from.Format("2006-01-02 15:04"), to.Format("15:04 MST"))
}

// reportPower raises power-back once per unclean end, after the box has
// been up PowerSettle, with what came back.
func reportPower(e *engine.Env, obs Observed, boot time.Time, now time.Time, tailnet func() string) {
	st, found := ReadPower(e.X)
	u := st.Unclean
	if !found || u == nil || u.Reported != nil || u.Boot != st.Boot || now.Sub(boot) < PowerSettle {
		return
	}
	var back []string
	if e.Cfg.Tmux() {
		if obs.TmuxUp {
			back = append(back, "tmux: "+strings.Join(obs.Names, ", ")+" running")
		} else {
			back = append(back, "tmux: not running")
		}
	}
	if obs.RCSummary != "" {
		back = append(back, "Remote Control: "+obs.RCSummary)
	}
	if t := tailnet(); t != "" {
		back = append(back, "Tailscale: "+t)
	}
	if e.Cfg.BackupEnabled() {
		back = append(back, "a missed backup runs now (its timer is persistent)")
	}
	loc := time.Local
	f := map[string]string{"up_since": boot.In(loc).Format("2006-01-02 15:04 MST"),
		"detail": "back after an unclean shutdown (power loss, a crash or a hard reset)" + u.window(loc) + "; up since " + boot.In(loc).Format("15:04") + "."}
	if len(back) > 0 {
		f["detail"] += " " + strings.Join(back, "; ") + "."
	}
	if w := strings.TrimPrefix(u.window(loc), " between "); w != "" {
		f["between"] = w
	}
	k, _ := events.Lookup("power-back")
	ev := events.Event{Time: now, Source: events.SourcePower, Kind: k.Name, Level: k.Level, Box: e.Cfg.Box, Key: "power:" + u.Boot, Fields: f}
	if _, err := events.Emit(e.X, ev); err != nil {
		if e.Log != nil {
			fmt.Fprintf(e.Log, "keelbox watch-run: event power-back: %v\n", err)
		}
		return
	}
	t := now
	u.Reported = &t
	_ = writePower(e.X, st)
}
