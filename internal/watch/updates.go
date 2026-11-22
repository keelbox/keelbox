package watch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

// Pending OS updates and a needed reboot: a new server
// from an old image has many, and unattended-upgrades installs only the
// security ones, once a day; a reboot follows host.auto_reboot (reboot.go).

const (
	// AptCheck is Ubuntu's update counter (update-notifier-common), the
	// one the login banner uses: it prints "UPDATES;SECURITY" on stderr.
	AptCheck = "/usr/lib/update-notifier/apt-check"
	// RebootRequired exists while an installed update needs a reboot.
	RebootRequired = "/var/run/reboot-required"
	// updatesEvery is how often the watch run counts updates: apt-check
	// takes seconds and the count changes about daily.
	updatesEvery = time.Hour
	// updatesRepeat is how often an unchanged reason is reported again.
	updatesRepeat = 7 * 24 * time.Hour
)

// UpdatesState is what the watch run last counted and reported.
type UpdatesState struct {
	Checked  time.Time  `json:"checked"`
	Total    int        `json:"total"`
	Security int        `json:"security"`
	Known    bool       `json:"known"`
	Reboot   *time.Time `json:"reboot_since,omitempty"`
	// Sent is when updates-pending was last raised, and for what.
	Sent       time.Time `json:"sent,omitempty"`
	SentReason string    `json:"sent_reason,omitempty"`
}

// PendingUpdates counts the updates apt would install, and the security
// ones among them. ok is false without apt-check.
func PendingUpdates(ctx context.Context, x system.Executor) (total, security int, ok bool) {
	if st, err := x.Stat(AptCheck); err != nil || !st.Exists {
		return 0, 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	r, err := x.Run(ctx, system.C(AptCheck))
	if err != nil {
		return 0, 0, false
	}
	a, b, found := strings.Cut(strings.TrimSpace(r.Stderr+r.Stdout), ";")
	t, err1 := strconv.Atoi(a)
	s, err2 := strconv.Atoi(b)
	if !found || err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return t, s, true
}

// RebootNeeded reports whether an installed update waits for a reboot.
func RebootNeeded(x system.Executor) bool {
	st, err := x.Stat(RebootRequired)
	return err == nil && st.Exists
}

// updates refreshes the count when it is due, fills the snapshot and raises
// updates-pending when a reboot becomes needed or security updates appear,
// and again weekly while either stays.
func updates(ctx context.Context, e *engine.Env, st *State, now time.Time) *snapshot.Updates {
	u := st.Updates
	if u == nil {
		u = &UpdatesState{}
		st.Updates = u
	}
	if now.Sub(u.Checked) >= updatesEvery {
		u.Total, u.Security, u.Known = PendingUpdates(ctx, e.X)
		u.Checked = now
	}
	switch reboot := RebootNeeded(e.X); {
	case reboot && u.Reboot == nil:
		t := now
		u.Reboot = &t
	case !reboot:
		u.Reboot = nil
	}
	var reasons []string
	if u.Reboot != nil {
		reasons = append(reasons, "reboot")
	}
	if u.Known && u.Security > 0 {
		reasons = append(reasons, "security")
	}
	reason := strings.Join(reasons, ",")
	if reason != "" && (reason != u.SentReason || now.Sub(u.Sent) >= updatesRepeat) {
		raiseUpdates(e, u, reason, now)
	}
	if reason == "" {
		u.SentReason = ""
	}
	s := &snapshot.Updates{Security: u.Security, RebootRequired: u.Reboot}
	if !u.Known {
		s.Security = 0
	}
	return s
}

func raiseUpdates(e *engine.Env, u *UpdatesState, reason string, now time.Time) {
	var parts []string
	if u.Known && u.Security > 0 {
		parts = append(parts, fmt.Sprintf("%d security update(s) waiting (unattended-upgrades installs them daily)", u.Security))
	}
	if u.Reboot != nil {
		parts = append(parts, "an installed update needs a reboot")
	}
	fix := RebootFix(e.Cfg)
	if u.Reboot == nil {
		fix = "keelbox ssh " + e.Cfg.Box + " --admin, then sudo unattended-upgrade"
	}
	fields := map[string]string{"security": strconv.Itoa(u.Security), "detail": strings.Join(parts, "; ") + ": " + fix}
	if u.Reboot != nil {
		fields["reboot_since"] = u.Reboot.UTC().Format(time.RFC3339)
	}
	k, _ := events.Lookup("updates-pending")
	ev := events.Event{Time: now, Source: events.SourceUpdates, Kind: "updates-pending", Level: k.Level, Box: e.Cfg.Box, Key: "updates", Fields: fields}
	if _, err := events.Emit(e.X, ev); err != nil {
		if e.Log != nil {
			fmt.Fprintf(e.Log, "keelbox watch-run: event updates-pending: %v\n", err)
		}
		return
	}
	u.Sent, u.SentReason = now, reason
}
