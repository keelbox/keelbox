package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/runs"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/ui"
)

const runID = "20261109-140102-ab12"

// fakeRunBox is a box with one run: its log grows as the test adds records,
// it runs until the test ends it, and its connection can drop.
type fakeRunBox struct {
	mu       sync.Mutex
	log      []byte
	running  bool
	down     int // connection attempts that fail before it comes back
	stopped  int
	grew     chan struct{}
	cut      chan struct{} // ends the stream as a dropped connection does
	onStop   func(*fakeRunBox)
	lastOff  int64
	followed int
}

func newFakeRunBox() *fakeRunBox {
	return &fakeRunBox{grew: make(chan struct{}, 100), cut: make(chan struct{}, 1), running: true}
}

func (b *fakeRunBox) add(r runs.Record) {
	if r.Time.IsZero() {
		r.Time = time.Now().UTC()
	}
	data, _ := json.Marshal(r)
	b.mu.Lock()
	b.log = append(append(b.log, data...), '\n')
	b.mu.Unlock()
	b.grew <- struct{}{}
}

func (b *fakeRunBox) end(code int, result string) {
	b.add(runs.Record{Kind: runs.KindEnd, Code: code, Result: result, Steps: 3, Changed: 2})
	b.mu.Lock()
	b.running = false
	b.mu.Unlock()
	b.grew <- struct{}{}
}

func (b *fakeRunBox) die() {
	b.mu.Lock()
	b.running = false
	b.mu.Unlock()
	b.grew <- struct{}{}
}

func (b *fakeRunBox) offline() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down > 0 {
		b.down--
		return true
	}
	return false
}

func (b *fakeRunBox) Output(ctx context.Context, remote string) (string, error) {
	if b.offline() {
		return "", errors.New("ssh: connect to host: Connection timed out")
	}
	if !strings.Contains(remote, "date +%s") {
		return "", fmt.Errorf("unexpected command %q", remote)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := "inactive"
	if b.running {
		st = "active"
	}
	if len(b.log) == 0 {
		return fmt.Sprintf("%d - -\n", time.Now().Unix()), nil
	}
	return fmt.Sprintf("%d %s %s\n", time.Now().Unix(), runID, st), nil
}

var tailFrom = regexp.MustCompile(`tail -c \+(\d+)`)

func (b *fakeRunBox) Follow(ctx context.Context, remote string, w io.Writer) error {
	if b.offline() {
		return errors.New("ssh: Connection timed out")
	}
	m := tailFrom.FindStringSubmatch(remote)
	off, _ := strconv.ParseInt(m[1], 10, 64)
	off--
	b.mu.Lock()
	b.followed++
	b.lastOff = off
	b.mu.Unlock()
	for {
		b.mu.Lock()
		data, running := append([]byte(nil), b.log[off:]...), b.running
		b.mu.Unlock()
		if len(data) > 0 {
			_, _ = w.Write(data)
			off += int64(len(data))
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.cut:
			return errors.New("ssh: Broken pipe")
		case <-b.grew:
		}
	}
}

func (b *fakeRunBox) Stop(ctx context.Context, id string) error {
	b.mu.Lock()
	b.stopped++
	b.mu.Unlock()
	if b.onStop != nil {
		go b.onStop(b)
	}
	return nil
}

func (b *fakeRunBox) start() {
	b.add(runs.Record{Kind: runs.KindStart, ID: runID, Box: "acme", Modules: []string{"host", "access"}})
	for _, l := range []runs.Record{
		{Kind: runs.KindOut, Text: "keelbox apply: acme (release 2026.11.2, Ubuntu 26.04 amd64)"},
		{Kind: runs.KindModule, Module: "host"},
		{Kind: runs.KindStep, Module: "host", Word: "ok", Step: "hostname"},
		{Kind: runs.KindStep, Module: "host", Word: "change", Step: "admin-user", Detail: "create user admin"},
	} {
		b.add(l)
	}
}

func (b *fakeRunBox) finish() {
	b.add(runs.Record{Kind: runs.KindModule, Module: "access"})
	b.add(runs.Record{Kind: runs.KindStep, Module: "access", Word: "change", Step: "ufw", Detail: "enable"})
	b.add(runs.Record{Kind: runs.KindOut, Text: ""})
	b.add(runs.Record{Kind: runs.KindOut, Text: "Applied 2 changes in 41s. Recorded in /var/lib/keelbox/logbook.jsonl."})
	b.end(0, runs.ResultOK)
}

type followed struct {
	f           *follower
	out, errOut *syncWriter
}

func newTestFollower(t *testing.T, b *fakeRunBox, answers string, terminal bool) followed {
	t.Helper()
	isolate(t)
	lostFor, retryEvery = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { lostFor, retryEvery = 3*time.Minute, 5*time.Second })
	out, errOut := &syncWriter{}, &syncWriter{}
	f := &follower{box: b, cfg: &config.Config{Box: "acme"}, o: options{}, user: "admin", stdout: out, stderr: errOut, sig: make(chan os.Signal, 2)}
	if terminal {
		f.term = prompt.New(strings.NewReader(answers), errOut, true)
	}
	return followed{f, out, errOut}
}

type syncWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(b)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func runFollow(f *follower) chan int {
	c := make(chan int, 1)
	go func() { c <- f.follow(context.Background(), runID) }()
	return c
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFollowShowsARunAsAnAttachedApply(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	b.start()
	done := runFollow(tf.f)
	b.finish()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d\n%s", code, tf.errOut)
	}
	want := "keelbox apply: acme (release 2026.11.2, Ubuntu 26.04 amd64)\nhost\n  ok      hostname\n  change  admin-user: create user admin\naccess\n  change  ufw: enable\n\nApplied 2 changes in 41s. Recorded in /var/lib/keelbox/logbook.jsonl.\n"
	if tf.out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", tf.out, want)
	}
	if _, ok := loadRunNote("acme"); ok {
		t.Error("a run seen to its end is still noted")
	}
}

func TestCtrlCWithoutATerminalLeavesTheRunGoing(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	b.start()
	done := runFollow(tf.f)
	waitFor(t, func() bool { return strings.Contains(tf.out.String(), "admin-user") }, "the first steps")
	tf.f.sig <- os.Interrupt
	if code := <-done; code != exitDetached {
		t.Fatalf("exit %d", code)
	}
	if b.stopped != 0 {
		t.Error("the run was stopped")
	}
	e := tf.errOut.String()
	if !strings.Contains(e, "Left setup running on acme (now at host/admin-user)") || !strings.Contains(e, "pick it up again, or see how it ended: keelbox apply acme") {
		t.Errorf("message:\n%s", e)
	}
	if n, ok := loadRunNote("acme"); !ok || n.ID != runID {
		t.Error("the run left going is not noted for the next command")
	}
}

func TestCtrlCAsksAndLeavesByDefault(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "\n", true)
	b.start()
	done := runFollow(tf.f)
	waitFor(t, func() bool { return strings.Contains(tf.out.String(), "admin-user") }, "the first steps")
	tf.f.sig <- os.Interrupt
	if code := <-done; code != exitDetached || b.stopped != 0 {
		t.Fatalf("exit %d, stopped %d", code, b.stopped)
	}
	if e := tf.errOut.String(); !strings.Contains(e, "Setup carries on on acme without this window. What now?") || !strings.Contains(e, "1) Leave it running") {
		t.Errorf("question:\n%s", e)
	}
}

func TestCtrlCCanStopTheRunAfterItsStep(t *testing.T) {
	b := newFakeRunBox()
	b.onStop = func(b *fakeRunBox) {
		b.add(runs.Record{Kind: runs.KindErr, Text: "keelbox: apply stopped before host/swap, after 1 change. Every step before it is in place; the next apply carries on from there."})
		b.end(exitInterrupted, runs.ResultInterrupted)
	}
	tf := newTestFollower(t, b, "2\n", true)
	b.start()
	done := runFollow(tf.f)
	waitFor(t, func() bool { return strings.Contains(tf.out.String(), "admin-user") }, "the first steps")
	tf.f.sig <- os.Interrupt
	if code := <-done; code != exitInterrupted || b.stopped != 1 {
		t.Fatalf("exit %d, stopped %d\n%s", code, b.stopped, tf.errOut)
	}
	if e := tf.errOut.String(); !strings.Contains(e, "stopping acme's setup after the current step") || !strings.Contains(e, "apply stopped before host/swap") {
		t.Errorf("output:\n%s", e)
	}
}

func TestASecondCtrlCAfterStopLeaves(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "stop\n", true)
	b.start()
	done := runFollow(tf.f)
	waitFor(t, func() bool { return strings.Contains(tf.out.String(), "admin-user") }, "the first steps")
	tf.f.sig <- os.Interrupt
	waitFor(t, func() bool { return strings.Contains(tf.errOut.String(), "stopping acme's setup") }, "the stop")
	tf.f.sig <- os.Interrupt
	if code := <-done; code != exitDetached {
		t.Fatalf("exit %d", code)
	}
}

func TestFollowRidesOutALostConnection(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	b.start()
	done := runFollow(tf.f)
	waitFor(t, func() bool { return strings.Contains(tf.out.String(), "admin-user") }, "the first steps")
	// The connection drops: the stream ends and the next tries fail.
	b.mu.Lock()
	b.down = 3
	b.mu.Unlock()
	b.cut <- struct{}{}
	waitFor(t, func() bool { return strings.Contains(tf.errOut.String(), "reconnected to acme") }, "the reconnection")
	b.finish()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d\n%s", code, tf.errOut)
	}
	if e := tf.errOut.String(); !strings.Contains(e, "lost the connection to acme; setup carries on there") {
		t.Errorf("output:\n%s", e)
	}
	if strings.Count(tf.out.String(), "admin-user") != 1 {
		t.Errorf("lines replayed twice after reconnecting:\n%s", tf.out)
	}
}

func TestFollowGivesUpOnALostBoxAndLeavesTheRun(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	b.start()
	done := runFollow(tf.f)
	waitFor(t, func() bool { return strings.Contains(tf.out.String(), "admin-user") }, "the first steps")
	b.mu.Lock()
	b.down = 1 << 30
	b.mu.Unlock()
	b.cut <- struct{}{}
	if code := <-done; code != exitDetached {
		t.Fatalf("exit %d", code)
	}
	if e := tf.errOut.String(); !strings.Contains(e, "Lost the connection to acme for") || !strings.Contains(e, "keelbox apply acme") {
		t.Errorf("output:\n%s", e)
	}
}

func TestFollowReportsARunThatDied(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	b.start()
	done := runFollow(tf.f)
	waitFor(t, func() bool { return strings.Contains(tf.out.String(), "admin-user") }, "the first steps")
	b.die()
	if code := <-done; code != 1 {
		t.Fatalf("exit %d", code)
	}
	if e := tf.errOut.String(); !strings.Contains(e, "setup on acme stopped without finishing after host/admin-user: the box restarted") {
		t.Errorf("output:\n%s", e)
	}
}

func TestRejoinFollowsARunningRun(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	b.start()
	go func() {
		waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.followed > 0 }, "the follow")
		b.finish()
	}()
	code, done := tf.f.rejoin(context.Background(), true)
	if !done || code != 0 || !strings.Contains(tf.out.String(), "setup is running on acme; following it") || !strings.Contains(tf.out.String(), "Applied 2 changes") {
		t.Errorf("done %v, exit %d:\n%s", done, code, tf.out)
	}
}

func TestRejoinReportsARunLeftOnceThenApplies(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	b.start()
	b.finish()
	saveRunNote("acme", runID)
	code, done := tf.f.rejoin(context.Background(), true)
	if !done || code != 0 || !regexp.MustCompile(`keelbox: setup on acme finished (just now|\d+ s ago): 3 steps, 2 changed, ok`).MatchString(tf.out.String()) {
		t.Errorf("done %v, exit %d:\n%s", done, code, tf.out)
	}
	if _, done := tf.f.rejoin(context.Background(), true); done {
		t.Error("a run's result was reported twice")
	}
	// A run another machine started is not this one's to report.
	saveRunNote("acme", "20261109-130000-0000")
	if _, done := tf.f.rejoin(context.Background(), true); done {
		t.Error("another run's result was reported")
	}
}

func TestRejoinReportsADiedRunAndAppliesAgain(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	b.start()
	b.die()
	saveRunNote("acme", runID)
	if _, done := tf.f.rejoin(context.Background(), true); done {
		t.Fatal("a died run ended the apply")
	}
	if e := tf.errOut.String(); !strings.Contains(e, "the last setup run on acme stopped without finishing after host/admin-user") || !strings.Contains(e, "applying again carries on from there") {
		t.Errorf("output:\n%s", e)
	}
}

func TestSIGTERMLeavesWithoutAsking(t *testing.T) {
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "2\n", true)
	b.start()
	done := runFollow(tf.f)
	waitFor(t, func() bool { return strings.Contains(tf.out.String(), "admin-user") }, "the first steps")
	tf.f.sig <- syscall.SIGTERM
	if code := <-done; code != exitDetached || b.stopped != 0 || strings.Contains(tf.errOut.String(), "What now?") {
		t.Fatalf("exit %d, stopped %d:\n%s", code, b.stopped, tf.errOut)
	}
}

// An apply whose Remote Control first run is left for later (ADR 0061)
// looks the same followed from its run log as streamed: the step deferred,
// the deferral and its command shown, not a failure.
func TestFollowShowsARemoteControlDeferralAsStreamed(t *testing.T) {
	const apply = "keelbox apply: acme (release 2026.11.3, Ubuntu 26.04 amd64)\n" +
		"host\n  ok      hostname\n  change  admin-user: create user admin\n" +
		"sessions\n  ok      tmux-boot-unit\n" +
		"  defer   remote-control-service: Claude Code Remote Control needs your answers on a terminal once (enable Remote Control, trust ~/projects): keelbox login acme claude-code\n" +
		"\nDeferred sessions/remote-control-service: Claude Code Remote Control needs your answers on a terminal once (enable Remote Control, trust ~/projects): keelbox login acme claude-code.\n" +
		"Applied 1 change in 3s. Recorded in /var/lib/keelbox/logbook.jsonl.\n"
	modules := []string{"host", "sessions"}
	st := ui.Style{Live: true, UTF8: true, Width: 100}

	var streamed strings.Builder
	p := ui.NewProgress(&streamed, st, modules)
	_, _ = p.Write([]byte(apply))
	p.Close()

	x := system.NewFake()
	l, err := runs.Begin(x, runs.Record{ID: runID, Box: "acme", Modules: modules})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(l.Stdout(), apply)
	if err := l.End(runs.Record{Code: 0, Result: "ok", Steps: 4, Changed: 1}); err != nil {
		t.Fatal(err)
	}
	recs, err := runs.Read(x, runID)
	if err != nil {
		t.Fatal(err)
	}
	b := newFakeRunBox()
	tf := newTestFollower(t, b, "", false)
	tf.f.o.style = st
	for _, r := range recs {
		b.add(r)
	}
	b.die()
	if code := <-runFollow(tf.f); code != 0 {
		t.Fatalf("exit %d\n%s", code, tf.errOut)
	}
	durations := regexp.MustCompile(`\d+(\.\d+)?(ms|s|m)\b`)
	want, got := durations.ReplaceAllString(ui.Screen(streamed.String()), "T"), durations.ReplaceAllString(ui.Screen(tf.out.String()), "T")
	if got != want || !strings.Contains(got, "keelbox login acme claude-code") {
		t.Errorf("followed:\n%s\nstreamed:\n%s", got, want)
	}
}
