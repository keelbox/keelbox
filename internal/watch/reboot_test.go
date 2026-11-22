package watch

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// rebootBox is a box an update wants rebooted, with one SSH login that
// has been idle and nothing else in use.
func rebootBox(t *testing.T, extra string) *box {
	t.Helper()
	b := newBox(t, extra)
	b.OK("loginctl list-sessions", "  7 1000 sam - pts/0 active no -\n")
	b.OK("loginctl show-session 7", "Name=sam\nClass=user\nType=tty\nState=active\nIdleHint=yes\n")
	b.OK("(as agent) tmux list-windows", "")
	b.OK("(as agent) find ", "")
	return b
}

var night = time.Date(2026, 11, 8, 0, 40, 0, 0, time.UTC)

func needed() *snapshot.Updates {
	t := night.Add(-6 * time.Hour)
	return &snapshot.Updates{RebootRequired: &t}
}

// The box reboots itself in the night once it has been idle for half an
// hour, and not again the next night for the same update.
func TestRebootsWhenIdleAtNight(t *testing.T) {
	b := rebootBox(t, "")
	ctx := context.Background()
	var st State
	at := func(min int) time.Time { return night.Add(time.Duration(min) * time.Minute) }
	prev := at(-2)
	for m := 0; m <= 28; m += 2 {
		if why := rebootDue(ctx, b.e, &st, needed(), 1, prev, at(m), time.UTC); why != "" {
			t.Fatalf("rebooted after %d quiet minutes: %s", m, why)
		}
		prev = at(m)
	}
	why := rebootDue(ctx, b.e, &st, needed(), 1, prev, at(30), time.UTC)
	if !strings.Contains(why, "needs it") || !strings.Contains(why, "idle for 30 minutes") || st.Reboot.Last == nil {
		t.Fatalf("no reboot after 30 quiet minutes: %q %+v", why, st.Reboot)
	}
	for m := 32; m <= 120; m += 2 {
		if why := rebootDue(ctx, b.e, &st, needed(), 1, at(m-2), at(m), time.UTC); why != "" {
			t.Fatalf("rebooted again at +%d: %s", m, why)
		}
	}
	// The next night it may, if the update still asks.
	day := 24 * 60
	why = ""
	for m := day; m <= day+30 && why == ""; m += 2 {
		why = rebootDue(ctx, b.e, &st, needed(), 1, at(m-2), at(m), time.UTC)
	}
	if why == "" {
		t.Error("an update that still needs a reboot the next night got none")
	}
}

// Only between 01:00 and 06:00 in the box's timezone.
func TestRebootsOnlyAtNight(t *testing.T) {
	b := rebootBox(t, "")
	ctx := context.Background()
	var st State
	noon := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for m := 0; m <= 120; m += 2 {
		now := noon.Add(time.Duration(m) * time.Minute)
		if why := rebootDue(ctx, b.e, &st, needed(), 0, now.Add(-2*time.Minute), now, time.UTC); why != "" {
			t.Fatalf("rebooted at %s", now)
		}
	}
	// 12:00 UTC is 01:00 in Auckland.
	akl, err := time.LoadLocation("Pacific/Auckland")
	if err != nil {
		t.Skip(err)
	}
	st = State{}
	var why string
	for m := 0; m <= 90; m += 2 {
		now := noon.Add(time.Duration(m) * time.Minute)
		if why = rebootDue(ctx, b.e, &st, needed(), 0, now.Add(-2*time.Minute), now, akl); why != "" {
			break
		}
	}
	if why == "" {
		t.Error("no reboot at night in the box's own timezone")
	}
}

// Anything in use starts the half hour again, and says what it was.
func TestRebootWaitsForQuiet(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		setup func(b *box)
		cpu   float64
		want  string
	}{
		"typing": {setup: func(b *box) {
			b.OK("loginctl show-session 7", "Name=sam\nClass=user\nType=tty\nState=active\nIdleHint=no\n")
		}, want: "sam is logged in and was active"},
		"desktop": {setup: func(b *box) {
			b.OK("loginctl show-session 7", "Name=sam\nClass=user\nType=wayland\nState=active\nIdleHint=yes\n")
		}, want: "sam's desktop session is logged in"},
		"tmux output": {setup: func(b *box) {
			b.OK("(as agent) tmux list-windows", "1700000000\n"+strconv.FormatInt(night.Add(29*time.Minute).Unix(), 10)+"\n")
		}, want: "tmux sessions had output"},
		"conversation": {setup: func(b *box) {
			b.OK("(as agent) find /home/agent/.claude/projects /home/agent/.codex/sessions -type f -mmin -5", "/home/agent/.claude/projects/app/1.jsonl\n")
		}, want: "agent conversation was written"},
		"cpu": {cpu: 40, want: "used 40% of the CPUs"},
		"claude at work": {setup: func(b *box) {
			// Started by Remote Control: the process is not called claude.
			b.OK("ps -u agent", "  4242  4100 /home/agent/.local/share/claude/versions/2.1.329 --session-id x\n")
			b.OK("(as agent) find /home/agent/.claude/sessions -maxdepth 1 -type f -name *.json -exec grep -lE", "/home/agent/.claude/sessions/4242.json\n")
		}, want: "a Claude Code conversation is at work"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := rebootBox(t, "")
			if c.setup != nil {
				c.setup(b)
			}
			st := State{Reboot: &RebootState{QuietSince: ptr(night.Add(-2 * time.Hour))}}
			now := night.Add(30 * time.Minute)
			if why := rebootDue(ctx, b.e, &st, needed(), c.cpu, now.Add(-2*time.Minute), now, time.UTC); why != "" {
				t.Fatalf("rebooted while in use: %s", why)
			}
			if st.Reboot.QuietSince != nil || !strings.Contains(st.Reboot.Busy, c.want) {
				t.Errorf("state %+v, want busy %q", st.Reboot, c.want)
			}
		})
	}
}

// A note from a Claude Code process that has ended does not count.
func TestRebootIgnoresAnEndedClaude(t *testing.T) {
	b := rebootBox(t, "")
	b.OK("ps -u agent", "  4100     1 -bash\n")
	b.OK("(as agent) find /home/agent/.claude/sessions", "/home/agent/.claude/sessions/4242.json\n")
	if why := Busy(context.Background(), b.e, 0, night); why != "" {
		t.Errorf("busy: %s", why)
	}
}

// Sessions that are not someone's login do not count: the agent's
// lingering manager, and a session closing after its SSH connection ended.
func TestRebootIgnoresSessionsThatAreNotLogins(t *testing.T) {
	b := rebootBox(t, "")
	b.OK("loginctl list-sessions", "  3 1001 agent - - active no -\n  9 1000 sam - pts/1 closing no -\n")
	b.OK("loginctl show-session 3", "Name=agent\nClass=manager\nType=unspecified\nState=active\nIdleHint=no\n")
	b.OK("loginctl show-session 9", "Name=sam\nClass=user\nType=tty\nState=closing\nIdleHint=no\n")
	if why := Busy(context.Background(), b.e, 0, night); why != "" {
		t.Errorf("busy: %s", why)
	}
}

// After a gap in the runs (the box was off, or the timer stopped), the
// quiet is counted from the run after it.
func TestRebootCountsQuietAgainAfterAGap(t *testing.T) {
	b := rebootBox(t, "")
	st := State{Reboot: &RebootState{QuietSince: ptr(night.Add(-3 * time.Hour))}}
	now := night.Add(40 * time.Minute)
	if why := rebootDue(context.Background(), b.e, &st, needed(), 0, now.Add(-time.Hour), now, time.UTC); why != "" {
		t.Fatalf("rebooted right after a gap: %s", why)
	}
	if st.Reboot.QuietSince == nil || !st.Reboot.QuietSince.Equal(now) {
		t.Errorf("quiet since %v", st.Reboot.QuietSince)
	}
}

// never and a time leave the reboot to the owner and unattended-upgrades,
// and nothing is looked at without a reboot needed.
func TestRebootOnlyNightlyOrWeekly(t *testing.T) {
	ctx := context.Background()
	for _, when := range []string{"never", `"04:00"`} {
		b := rebootBox(t, "host:\n  auto_reboot: "+when+"\n")
		st := State{Reboot: &RebootState{QuietSince: ptr(night.Add(-2 * time.Hour))}}
		now := night.Add(30 * time.Minute)
		if why := rebootDue(ctx, b.e, &st, needed(), 0, now.Add(-2*time.Minute), now, time.UTC); why != "" {
			t.Errorf("%s: rebooted: %s", when, why)
		}
	}
	b := rebootBox(t, "")
	var st State
	if why := rebootDue(ctx, b.e, &st, &snapshot.Updates{}, 0, night.Add(-2*time.Minute), night, time.UTC); why != "" || b.Ran("loginctl") {
		t.Errorf("looked at the box without a reboot needed: %q", why)
	}
}

// Restart saves the sessions, records the reboot and leaves systemd to
// reboot a few seconds later; the watch run's event goes out first.
func TestRestart(t *testing.T) {
	b := rebootBox(t, "")
	b.OK("systemd-run ", "")
	b.OK("(as agent) tmux has-session", "")
	raiseRebooting(b.e, "the update installed 2026-11-07 18:40 needs it", night)
	if err := Restart(context.Background(), b.e, "the update installed 2026-11-07 18:40 needs it", night); err != nil {
		t.Fatal(err)
	}
	if !b.Ran("(as agent) /home/agent/.tmux/plugins/tmux-resurrect/scripts/save.sh quiet") {
		t.Error("sessions not saved")
	}
	if !b.Ran("systemd-run --quiet --on-active=5 --timer-property=AccuracySec=1s systemctl reboot") {
		t.Errorf("no reboot scheduled: %v", b.Commands())
	}
	if r := records(t, b, logbook.KindPower); len(r) != 1 || !strings.Contains(r[0].Changes[0].Detail, "rebooted: the update") {
		t.Errorf("logbook %+v", r)
	}
	if q := queued(t, b); len(q) != 1 || q[0].Kind != "rebooting" || !strings.Contains(q[0].Fields["detail"], "Claude Code and Codex open their conversations again") {
		t.Errorf("events %+v", q)
	}
}

// With no tmux server, resurrect's save would replace the last save with an
// empty one, so the boot would restore nothing: Restart leaves it.
func TestRestartWithoutATmuxServer(t *testing.T) {
	b := rebootBox(t, "")
	b.OK("systemd-run ", "")
	b.Fail("(as agent) tmux has-session")
	if err := Restart(context.Background(), b.e, "asked", night); err != nil {
		t.Fatal(err)
	}
	if b.Ran("(as agent) /home/agent/.tmux/plugins/tmux-resurrect/scripts/save.sh") {
		t.Error("saved with no tmux server")
	}
	if !b.Ran("systemd-run ") {
		t.Error("no reboot scheduled")
	}
}

// A reboot systemd did not take is not in the logbook.
func TestRestartThatFailsIsNotRecorded(t *testing.T) {
	b := rebootBox(t, "")
	b.OK("(as agent) tmux has-session", "")
	b.Fail("systemd-run ")
	if err := Restart(context.Background(), b.e, "asked", night); err == nil {
		t.Fatal("no error")
	}
	if r := records(t, b, logbook.KindPower); len(r) != 0 {
		t.Errorf("logbook %+v", r)
	}
}

// Without sessions.recovery, nothing promises that the sessions come back.
func TestRebootingWithoutRecovery(t *testing.T) {
	b := rebootBox(t, "sessions:\n  recovery: false\n")
	raiseRebooting(b.e, "asked", night)
	if q := queued(t, b); len(q) != 1 || strings.Contains(q[0].Fields["detail"], "conversations again") || !strings.Contains(q[0].Fields["detail"], "not saved") {
		t.Errorf("events %+v", q)
	}
}

func TestRebootFix(t *testing.T) {
	for when, want := range map[string]string{
		"nightly": "reboots itself between 01:00 and 06:00 UTC once idle for 30 minutes; to reboot now: keelbox reboot acme",
		"weekly":  "reboots itself on Sunday between 01:00 and 06:00 UTC once idle for 30 minutes; to reboot now: keelbox reboot acme",
		"never":   "keelbox reboot acme; or have the box reboot itself at night once idle: host.auto_reboot: nightly, then keelbox apply acme",
		"04:00":   "unattended-upgrades reboots the box at 04:00; to reboot now: keelbox reboot acme",
	} {
		c := &config.Config{Box: "acme", Host: config.Host{AutoReboot: when}}
		if got := RebootFix(c); !strings.Contains(got, want) {
			t.Errorf("%s: %q", when, got)
		}
	}
}

// Weekly: on Sunday night for an update that was waiting at its start, or
// on the first idle night after it; an update that comes later in the week
// waits for the next Sunday.
func TestRebootWeekly(t *testing.T) {
	ctx := context.Background()
	if night.Weekday() != time.Sunday {
		t.Fatal("night is not a Sunday")
	}
	day := 24 * time.Hour
	cases := []struct {
		name   string
		now    time.Time
		update time.Time
		last   *time.Time
		reboot bool
	}{
		{"Sunday, waiting since Friday", night.Add(30 * time.Minute), night.Add(-2 * day), nil, true},
		{"Saturday night", night.Add(30*time.Minute - day), night.Add(-2 * day), nil, false},
		{"Tuesday, missed on Sunday", night.Add(30*time.Minute + 2*day), night.Add(-2 * day), ptr(night.Add(-6 * day)), true},
		{"Tuesday, rebooted on Sunday", night.Add(30*time.Minute + 2*day), night.Add(-2 * day), ptr(night.Add(30 * time.Minute)), false},
		{"Tuesday, update from Monday", night.Add(30*time.Minute + 2*day), night.Add(day), nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := rebootBox(t, "host:\n  auto_reboot: weekly\n")
			st := State{Reboot: &RebootState{QuietSince: ptr(c.now.Add(-time.Hour)), Last: c.last}}
			u := c.update
			why := rebootDue(ctx, b.e, &st, &snapshot.Updates{RebootRequired: &u}, 0, c.now.Add(-2*time.Minute), c.now, time.UTC)
			if (why != "") != c.reboot {
				t.Errorf("reboot %q, want %v", why, c.reboot)
			}
		})
	}
	if got := sundayNight(night); !got.Equal(time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("before Sunday's window: %s", got)
	}
}

func ptr(t time.Time) *time.Time { return &t }
