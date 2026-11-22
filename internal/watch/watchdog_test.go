package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/system"
)

// box is a fake box for the watchdog: an agent whose tmux server and
// Remote Control unit a test turns on and off, and which records what the
// watchdog ran.
type box struct {
	*system.Fake
	e        *engine.Env
	tmux     bool   // the tmux server runs
	tmuxSlow bool   // a look at it times out
	rc       string // Remote Control: active, inactive or failed
	rcResult string
	signedIn bool
	online   bool
	// rcStarts is how a start leaves the unit: active unless set.
	rcStartsFailing bool
}

func newBox(t *testing.T, extra string) *box {
	t.Helper()
	e, x := env(t, extra)
	x.AddUser("agent", 1001)
	b := &box{Fake: x, e: e, tmux: true, rc: "active", signedIn: true, online: true}
	if err := logbook.Append(x, logbook.Record{Time: time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC), Box: "acme", Keelbox: "v0.1.0", Release: "2026.12.0", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	x.OnFunc("", b.run)
	prev := dial
	dial = func(context.Context, string) error {
		if !b.online {
			return errors.New("no route to host")
		}
		return nil
	}
	t.Cleanup(func() { dial = prev })
	return b
}

func fail(c system.Cmd) (system.Result, error) {
	r := system.Result{ExitCode: 1}
	return r, &system.ExitError{Cmd: c, Result: r}
}

func noTmuxServer(c system.Cmd) (system.Result, error) {
	r := system.Result{ExitCode: 1, Stderr: "no server running on /tmp/tmux-1002/default\n"}
	return r, &system.ExitError{Cmd: c, Result: r}
}

func (b *box) run(c system.Cmd) (system.Result, error) {
	s := c.String()
	switch {
	case strings.HasPrefix(s, "(as agent) tmux "):
		if b.tmuxSlow {
			r := system.Result{ExitCode: -1}
			return r, &system.ExitError{Cmd: c, Result: r}
		}
		if !b.tmux {
			return noTmuxServer(c)
		}
		return system.Result{Stdout: "main\nwork\n"}, nil
	case s == "systemctl start user@1001.service":
		return system.Result{}, nil
	case s == "systemctl --user -M agent@ restart "+TmuxUnit:
		b.tmux = true
		return system.Result{}, nil
	case strings.HasPrefix(s, "systemctl --user -M agent@ show -p ActiveState,SubState,Result "+RemoteControlUnit):
		sub := map[string]string{"active": "running", "inactive": "dead", "failed": "failed"}[b.rc]
		res := b.rcResult
		if res == "" {
			res = "success"
		}
		return system.Result{Stdout: "ActiveState=" + b.rc + "\nSubState=" + sub + "\nResult=" + res + "\n"}, nil
	case s == "systemctl --user -M agent@ reset-failed "+RemoteControlUnit:
		b.rc, b.rcResult = "inactive", ""
		return system.Result{}, nil
	case s == "systemctl --user -M agent@ start "+RemoteControlUnit:
		b.rc = "active"
		if b.rcStartsFailing {
			b.rc, b.rcResult = "failed", "exit-code"
		}
		return system.Result{}, nil
	case s == "(as agent) claude auth status":
		if !b.signedIn {
			return fail(c)
		}
		return system.Result{}, nil
	}
	return fail(c)
}

// watch runs the watchdog once and returns the state it left.
func (b *box) watch(t *testing.T, st *State, now time.Time) Observed {
	t.Helper()
	obs := observe(context.Background(), b.e)
	runWatchdog(context.Background(), b.e, st, &obs, now)
	return obs
}

func (b *box) acted() []string {
	var out []string
	for _, c := range b.Commands() {
		for _, verb := range []string{" restart ", " start ", " stop ", " reset-failed ", " kill", "kill-server"} {
			if strings.Contains(c+" ", verb) && !strings.Contains(c, "user@1001") {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func queued(t *testing.T, x system.Executor) []events.Event {
	t.Helper()
	q, err := events.Pending(x)
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Event
	for _, e := range q {
		out = append(out, e.Event)
	}
	return out
}

func records(t *testing.T, x system.Executor, kind string) []logbook.Record {
	t.Helper()
	recs, _ := logbook.Read(x)
	var out []logbook.Record
	for _, r := range recs {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

func TestWatchdogRestartsAGoneTmuxServer(t *testing.T) {
	b := newBox(t, "")
	b.tmux = false
	var st State
	obs := b.watch(t, &st, t0)
	if !b.Ran("systemctl --user -M agent@ restart " + TmuxUnit) {
		t.Fatalf("no restart: %v", b.Commands())
	}
	if !obs.TmuxUp || strings.Join(obs.Names, ",") != "main,work" {
		t.Errorf("after the restart: %+v", obs)
	}
	recs := records(t, b, logbook.KindWatch)
	if len(recs) != 1 || recs[0].Result != "ok" || recs[0].Release != "2026.12.0" || recs[0].Changes[0].Step != CheckTmux ||
		!strings.Contains(recs[0].Changes[0].Detail, "the tmux server was gone") {
		t.Errorf("logbook: %+v", recs)
	}
	q := queued(t, b)
	if len(q) != 1 || q[0].Kind != "watch-recovered" || q[0].Key != "watch:tmux" || q[0].Fields["unit"] != TmuxUnit {
		t.Errorf("events: %+v", q)
	}
	c := st.Checks[CheckTmux]
	if len(c.Restarts) != 1 || c.DownSince != nil || c.Last == nil || c.Last.Result != "ok" {
		t.Errorf("state: %+v", c)
	}

	// Up again: the next run does nothing.
	b.Calls = nil
	b.watch(t, &st, t0.Add(2*time.Minute))
	if a := b.acted(); len(a) != 0 {
		t.Errorf("acted on a running server: %v", a)
	}
}

func TestWatchdogNeverTouchesRunningSessions(t *testing.T) {
	b := newBox(t, "sessions:\n  remote_control:\n    enabled: true\n")
	var st State
	for i := 0; i < 5; i++ {
		b.watch(t, &st, t0.Add(time.Duration(i)*2*time.Minute))
	}
	if a := b.acted(); len(a) != 0 {
		t.Errorf("the watchdog acted on running units: %v", a)
	}
	if b.Ran("(as agent) claude auth status") {
		t.Error("checked the sign-in while Remote Control runs")
	}
	if len(records(t, b, logbook.KindWatch)) != 0 || len(queued(t, b)) != 0 {
		t.Error("recorded or raised something for running units")
	}
}

func TestWatchdogTmuxBudget(t *testing.T) {
	b := newBox(t, "")
	var st State
	restarts := func() int { return len(records(t, b, logbook.KindWatch)) }
	// The agent ends its tmux server after every restart.
	for i := 0; i < 3; i++ {
		b.tmux = false
		b.watch(t, &st, t0.Add(time.Duration(i)*2*time.Minute))
	}
	if restarts() != 3 {
		t.Fatalf("%d restarts, want 3", restarts())
	}
	b.tmux = false
	b.watch(t, &st, t0.Add(6*time.Minute))
	if restarts() != 3 {
		t.Errorf("a fourth restart within the hour")
	}
	q := queued(t, b)
	if last := q[len(q)-1]; last.Kind != "watch-gave-up" || last.Level != events.Urgent || last.Fields["fix"] == "" {
		t.Errorf("no give-up event: %+v", last)
	}
	// Given up: no restart until an hour after the last one, and no
	// second give-up event.
	b.watch(t, &st, t0.Add(30*time.Minute))
	if restarts() != 3 || len(queued(t, b)) != len(q) {
		t.Error("acted or raised again while given up")
	}
	b.watch(t, &st, t0.Add(4*time.Minute+time.Hour))
	if restarts() != 4 {
		t.Errorf("no hourly try after the give-up: %d", restarts())
	}
	if n := len(queued(t, b)); n != len(q) {
		t.Errorf("an hourly try raised an event: %d", n)
	}
	// Up for an hour after the last restart: recovered, budget back.
	b.watch(t, &st, t0.Add(5*time.Minute+2*time.Hour))
	q = queued(t, b)
	if last := q[len(q)-1]; last.Kind != "watch-recovered" || !strings.Contains(last.Fields["detail"], "an hour") {
		t.Errorf("no recovery after an hour up: %+v", last)
	}
	if st.Checks[CheckTmux].GaveUp != nil {
		t.Error("still given up")
	}
}

func TestWatchdogOff(t *testing.T) {
	b := newBox(t, "sessions:\n  watchdog: false\n")
	b.tmux = false
	if err := Run(context.Background(), b.e, nil, t0); err != nil {
		t.Fatal(err)
	}
	if a := b.acted(); len(a) != 0 {
		t.Errorf("sessions.watchdog: false and it acted: %v", a)
	}
}

func TestWatchdogWithoutStartOnBoot(t *testing.T) {
	b := newBox(t, "sessions:\n  start_on_boot: false\n")
	b.tmux = false
	var st State
	b.watch(t, &st, t0)
	if a := b.acted(); len(a) != 0 {
		t.Errorf("no keelbox-tmux.service to restart, and it acted: %v", a)
	}
}

func TestRemoteControlWaitsForSignInAndNetwork(t *testing.T) {
	b := newBox(t, "sessions:\n  remote_control:\n    enabled: true\n")
	b.rc = "inactive"
	b.signedIn = false
	var st State
	obs := b.watch(t, &st, t0)
	if a := b.acted(); len(a) != 0 || !strings.Contains(obs.RCSummary, "sign-in") {
		t.Errorf("not signed in: acted %v, summary %q", a, obs.RCSummary)
	}

	b.signedIn, b.online = true, false
	obs = b.watch(t, &st, t0.Add(2*time.Minute))
	if a := b.acted(); len(a) != 0 || st.OfflineSince == nil || !strings.HasPrefix(obs.RCSummary, "offline since") {
		t.Errorf("offline: acted %v, state %+v, summary %q", a, st, obs.RCSummary)
	}

	b.online = true
	obs = b.watch(t, &st, t0.Add(4*time.Minute))
	if !b.Ran("systemctl --user -M agent@ start "+RemoteControlUnit) || obs.RCSummary != "running" || st.OfflineSince != nil {
		t.Errorf("online again: summary %q, state %+v", obs.RCSummary, st)
	}
	recs := records(t, b, logbook.KindWatch)
	if len(recs) != 1 || !strings.Contains(recs[0].Changes[0].Detail, "the network is back") {
		t.Errorf("logbook: %+v", recs)
	}
}

func TestRemoteControlStartLimit(t *testing.T) {
	b := newBox(t, "sessions:\n  remote_control:\n    enabled: true\n")
	b.rc, b.rcResult, b.rcStartsFailing = "failed", "start-limit-hit", true
	var st State
	resets := func() int {
		n := 0
		for _, c := range b.Commands() {
			if c == "systemctl --user -M agent@ reset-failed "+RemoteControlUnit {
				n++
			}
		}
		return n
	}
	b.watch(t, &st, t0)
	b.watch(t, &st, t0.Add(10*time.Minute))
	if resets() != 0 {
		t.Fatal("reset within the cool-down")
	}
	now := t0.Add(15 * time.Minute)
	for i := 0; i < 4; i++ {
		b.watch(t, &st, now)
		now = now.Add(16 * time.Minute)
	}
	if resets() != 4 {
		t.Fatalf("%d resets, want 4", resets())
	}
	if recs := records(t, b, logbook.KindWatch); len(recs) != 4 || recs[0].Result != "failed" {
		t.Errorf("logbook: %+v", recs)
	}
	b.watch(t, &st, now)
	if resets() != 4 {
		t.Error("a fifth reset in a day")
	}
	q := queued(t, b)
	if last := q[len(q)-1]; last.Kind != "watch-gave-up" || !strings.Contains(last.Fields["fix"], "keelbox login acme") {
		t.Errorf("give-up: %+v", last)
	}
	// Given up: nothing until an apply.
	b.watch(t, &st, now.Add(time.Hour))
	if resets() != 4 {
		t.Error("reset after the give-up without an apply")
	}
	if err := logbook.Append(b, logbook.Record{Time: now.Add(time.Hour + time.Minute), Box: "acme", Keelbox: "v0.1.0", Release: "2026.12.0", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	b.rcStartsFailing = false
	b.watch(t, &st, now.Add(2*time.Hour))
	if resets() != 5 || b.rc != "active" {
		t.Errorf("no fresh try after an apply: %d resets, unit %s", resets(), b.rc)
	}
}

func TestRemoteControlRunningAgainAfterGiveUp(t *testing.T) {
	b := newBox(t, "sessions:\n  remote_control:\n    enabled: true\n")
	var st State
	gave := t0
	st.check(CheckRemoteControl).GaveUp = &gave
	st.check(CheckRemoteControl).Restarts = []time.Time{t0, t0, t0, t0}
	b.watch(t, &st, t0.Add(10*time.Minute)) // keelbox login started it
	c := st.Checks[CheckRemoteControl]
	if c.GaveUp != nil || len(c.Restarts) != 0 {
		t.Errorf("a running unit after a give-up keeps the old budget: %+v", c)
	}
	if q := queued(t, b); len(q) != 1 || q[0].Kind != "watch-recovered" {
		t.Errorf("events: %+v", q)
	}
}

func TestStatusRows(t *testing.T) {
	b := newBox(t, "")
	if rows := StatusRows(b, false); len(rows) != 1 || rows[0][1] != "off (sessions.watchdog: false)" {
		t.Errorf("off: %v", rows)
	}
	b.tmux = false
	var st State
	b.watch(t, &st, t0)
	if err := writeState(b, st); err != nil {
		t.Fatal(err)
	}
	rows := StatusRows(b, true)
	if len(rows) != 1 || !strings.HasPrefix(rows[0][1], "ok; last action ") || !strings.Contains(rows[0][1], "the tmux server was gone") {
		t.Errorf("after a restart: %v", rows)
	}
	boot(b.Fake, "boot-1")
	if _, err := BootStart(b, t0); err != nil {
		t.Fatal(err)
	}
	boot(b.Fake, "boot-2")
	if _, err := BootStart(b, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows = StatusRows(b, true)
	if len(rows) != 2 || rows[1][0] != "power" || !strings.Contains(rows[1][1], "without a clean shutdown") {
		t.Errorf("after an unclean end: %v", rows)
	}
}

func TestWatchRunSavesSessionsEvery15Minutes(t *testing.T) {
	b := newBox(t, "")
	b.OK("(as agent) /home/agent/.tmux/plugins/tmux-resurrect/scripts/save.sh quiet", "")
	saves := func() int {
		n := 0
		for _, c := range b.Commands() {
			if strings.HasSuffix(c, "save.sh quiet") {
				n++
			}
		}
		return n
	}
	for i := 0; i <= 8; i++ {
		if err := Run(context.Background(), b.e, nil, t0.Add(time.Duration(i)*2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if saves() != 2 { // at 0 and 16 minutes
		t.Errorf("%d saves in 16 minutes, want 2", saves())
	}
	b.tmux = false
	b.watch(t, &State{}, t0) // restarted by the watchdog; no save without a server first
	e, _ := env(t, "sessions:\n  recovery: false\n")
	e.X = b.Fake
	before := saves()
	if err := Run(context.Background(), e, nil, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if saves() != before {
		t.Error("saved with sessions.recovery: false")
	}
}

// Under the agent's memory slow-down a look at its tmux server can time out
// (its commands run in its slice): that is no sign the server is gone, and
// a running server is never restarted for it.
func TestWatchdogLeavesTmuxAloneWhenTheLookTimesOut(t *testing.T) {
	b := newBox(t, "")
	b.tmuxSlow = true
	var st State
	obs := b.watch(t, &st, t0)
	if b.Ran("systemctl --user -M agent@ restart "+TmuxUnit) || obs.TmuxUp || !obs.TmuxUnsure {
		t.Errorf("a timed-out look restarted tmux or read as up: %+v %v", obs, b.Commands())
	}
	if c := st.check(CheckTmux); c.DownSince != nil || len(c.Restarts) != 0 {
		t.Errorf("a timed-out look counts as down: %+v", c)
	}
}
