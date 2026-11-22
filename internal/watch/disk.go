package watch

import (
	"fmt"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// DiskAlert is the alert level a mount was last reported at.
type DiskAlert struct {
	Level string    `json:"level"` // disk-warn or disk-full
	Sent  time.Time `json:"sent"`
}

// diskRepeat is how often a mount that stays over a threshold is reported
// again.
const diskRepeat = 24 * time.Hour

// diskAlerts raises disk-warn at host.disk.warn_percent and disk-full at
// fail_percent, per mount: when a mount first crosses a threshold, when it
// moves up to the next, and daily while it stays over one. A mount back
// under the warning is forgotten, so the next crossing is reported again.
func diskAlerts(e *engine.Env, st *State, disks []snapshot.Disk, now time.Time) {
	warn, fail := e.Cfg.DiskThresholds()
	seen := map[string]bool{}
	for _, d := range disks {
		seen[d.Mount] = true
		kind, limit := "", 0
		switch {
		case d.Percent >= fail:
			kind, limit = "disk-full", fail
		case d.Percent >= warn:
			kind, limit = "disk-warn", warn
		}
		prev, had := st.Disk[d.Mount]
		if kind == "" {
			delete(st.Disk, d.Mount)
			continue
		}
		if had && (prev.Level == kind || prev.Level == "disk-full") && now.Sub(prev.Sent) < diskRepeat {
			continue
		}
		k, _ := events.Lookup(kind)
		ev := events.Event{Time: now, Source: events.SourceDisk, Kind: kind, Level: k.Level, Box: e.Cfg.Box, Key: "disk:" + d.Mount,
			Fields: map[string]string{"mount": d.Mount, "percent": fmt.Sprint(d.Percent),
				"detail": fmt.Sprintf("%s is %d%% full (alerts at %d%%); see what uses it: sudo du -xh --max-depth=2 %s | sort -h | tail", d.Mount, d.Percent, limit, d.Mount)}}
		if _, err := events.Emit(e.X, ev); err != nil {
			if e.Log != nil {
				fmt.Fprintf(e.Log, "keelbox watch-run: event %s: %v\n", kind, err)
			}
			continue
		}
		if st.Disk == nil {
			st.Disk = map[string]DiskAlert{}
		}
		st.Disk[d.Mount] = DiskAlert{Level: kind, Sent: now}
	}
	for m := range st.Disk {
		if !seen[m] && len(disks) > 0 {
			delete(st.Disk, m)
		}
	}
}
