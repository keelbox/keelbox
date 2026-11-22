package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/notify"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/runlock"
	"github.com/keelbox/keelbox/internal/runs"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/version"
)

// An apply a laptop starts over SSH runs on the box as a transient root
// systemd unit, not as a child of the SSH session, so it finishes whatever
// happens to the laptop (docs/adr/0063-detached-apply.md). The box's half
// is here: startRun, which the remote script calls under sudo, and
// detachedRun, the unit's process. The laptop's half is in follow.go.

const (
	// envDetach makes keelbox apply --local start the detached run and
	// print its id instead of applying.
	envDetach = "KEELBOX_DETACH"
	// envRun is set in the unit: this process is that run.
	envRun = "KEELBOX_RUN"
	// runMarker starts the line startRun prints: "keelbox-run: started ID"
	// or "keelbox-run: running ID" (a run was already going).
	runMarker = "keelbox-run:"
	// startLockPath serialises starts, so two laptops never start two runs.
	startLockPath = "/run/keelbox-start.lock"
)

// runEnv is the environment a run keeps from the sudo that started it:
// who started it, over which connection and where the laptop reaches the
// box (the bootstrap account's steps, the tailnet proof and the agent's
// network limits read them), and a plain root environment.
var runEnv = []string{"PATH", "HOME", "LANG", "SUDO_USER", "SUDO_UID", "SUDO_GID", "KEELBOX_SSH_CONNECTION", provider.EnvSSHAddrs}

// startRun starts the detached apply, or finds the one running, and prints
// its id. The staged config is copied for the run: the remote script
// removes its own copy when this returns.
func startRun(ctx context.Context, env *engine.Env, o options, stdout, stderr io.Writer) int {
	lk, err := runlock.Apply(startLockPath, 30*time.Second)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	defer lk.Unlock()
	x := env.X
	if s, ok, err := runs.Last(ctx, x); err == nil && ok && s.Running {
		fmt.Fprintf(stdout, "%s running %s\n", runMarker, s.ID)
		return 0
	}
	// An apply not started this way (keelbox apply on the box itself)
	// holds the apply lock; a run would only wait for it and fail. A
	// day-two run holds it shared for a moment: wait as apply does.
	if l, err := runlock.Apply(lockPath, 30*time.Second); err != nil {
		if errors.Is(err, runlock.ErrBusy) {
			err = fmt.Errorf("another keelbox apply is running (%s is locked)", lockPath)
		}
		fail(stderr, err)
		return 1
	} else {
		l.Unlock()
	}
	exe, err := os.Executable()
	if err != nil {
		fail(stderr, err)
		return 1
	}
	id, err := startUnit(ctx, x, env.Cfg, exe, o, os.Getenv)
	if err != nil {
		failf(stderr, "start the apply on the box: %v", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s started %s\n", runMarker, id)
	return 0
}

// runConfig is the run's own copy of the staged config.
func runConfig(id string) string { return provider.RemoteStage + "/run-" + id + ".yaml" }

// startUnit begins the run's log and starts its unit.
func startUnit(ctx context.Context, x system.Executor, cfg *config.Config, exe string, o options, getenv func(string) string) (string, error) {
	id, err := runs.NewID(time.Now())
	if err != nil {
		return "", err
	}
	data, err := x.ReadFile(o.configFile)
	if err != nil {
		return "", err
	}
	if err := x.WriteFile(runConfig(id), data, 0o600, "root", "root"); err != nil {
		return "", err
	}
	l, err := runs.Begin(x, runs.Record{ID: id, Box: cfg.Box, Keelbox: version.Version, By: getenv("SUDO_USER"), Modules: moduleNames(cfg)})
	if err != nil {
		_ = x.Remove(runConfig(id))
		return "", err
	}
	args := []string{"--unit=" + strings.TrimSuffix(runs.Unit(id), ".service"), "--description=keelbox apply " + cfg.Box + " (run " + id + ")",
		"--collect", "--quiet", "--property=KillMode=mixed", "--property=TimeoutStopSec=5min", "--setenv=" + envRun + "=" + id}
	for _, k := range runEnv {
		if v := getenv(k); v != "" {
			args = append(args, "--setenv="+k+"="+v)
		}
	}
	args = append(append(args, "--", exe, "apply", "--local", runConfig(id)), remoteExtra(o)...)
	if _, err := x.Run(ctx, system.C("systemd-run", args...)); err != nil {
		_ = l.End(runs.Record{Result: runs.ResultFailed, Code: 1, Error: err.Error()})
		_ = x.Remove(runConfig(id))
		return "", err
	}
	return id, nil
}

// detachedRun is the unit's process: the apply, with its output written to
// the run's log as well as the journal, and the result at the end. A run
// nobody follows at the end notifies the owner, when channels are set up.
func detachedRun(ctx context.Context, id string, cfg *config.Config, o options, stdout, stderr io.Writer) int {
	_ = os.Unsetenv(envRun)
	x := &system.Real{}
	l, err := runs.Resume(x, id)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	defer func() {
		_ = x.Remove(runConfig(id))
		provider.RemoveStagedSecrets()
	}()
	last := &tailLine{}
	start := time.Now()
	code := runLocal(ctx, "apply", cfg, o, io.MultiWriter(l.Stdout(), stdout), io.MultiWriter(l.Stderr(), stderr, last))
	end := runEnd(x, code, start, last.get())
	if err := l.End(end); err != nil {
		failf(stderr, "run log: %v", err)
	}
	// A follower whose laptop went away keeps the log open until its next
	// write fails: the end record just written ends it.
	time.Sleep(watchGrace)
	if !runs.Watched(id) {
		notifyRun(ctx, x, cfg, id, end)
	}
	return code
}

// watchGrace is how long a finished run waits before it asks whether
// anyone still follows it.
var watchGrace = 2 * time.Second

// runEnd is the run's end record: the logbook's record of this apply when
// there is one, else what the exit code and the last error line say.
func runEnd(x system.Executor, code int, start time.Time, lastErr string) runs.Record {
	end := runs.Record{Code: code, Seconds: time.Since(start).Seconds(), Error: strings.TrimPrefix(lastErr, "keelbox: ")}
	recs, _ := logbook.Read(x)
	if r := logbook.Last(recs, logbook.KindApply); r != nil && !r.Time.Before(start.Add(-time.Second)) {
		end.Result, end.Steps, end.Changed, end.Failed = r.Result, r.Checked, len(r.Changes), len(r.Failed)
		if r.Error != "" {
			end.Error = r.Error
		}
		if r.Result == runs.ResultOK || r.Result == runs.ResultInterrupted {
			end.Error = ""
		}
		return end
	}
	switch code {
	case 0:
		end.Result, end.Error = runs.ResultOK, ""
	case exitInterrupted:
		end.Result = runs.ResultInterrupted
	default:
		end.Result = runs.ResultFailed
	}
	return end
}

// notifyRun raises apply-finished or apply-failed and sends it now.
func notifyRun(ctx context.Context, x system.Executor, cfg *config.Config, id string, end runs.Record) {
	r, err := cfg.Resolve()
	if err != nil || len(r.Notify.Channels) == 0 {
		return
	}
	e := events.Event{Time: time.Now().UTC(), Source: events.SourceApply, Box: r.Box, Key: "apply:" + id,
		Fields: map[string]string{"result": end.Result, "detail": runs.Summary{Steps: end.Steps, Changed: end.Changed, Failed: end.Failed}.Tally()}}
	switch end.Result {
	case runs.ResultOK, runs.ResultInterrupted:
		e.Kind, e.Level = "apply-finished", events.Info
	default:
		e.Kind, e.Level = "apply-failed", events.Urgent
		if end.Error != "" {
			e.Fields["detail"] = oneLineField(end.Error)
		}
		e.Fields["fix"] = "keelbox apply " + r.Box
	}
	if _, err := events.Emit(x, e); err != nil {
		return
	}
	_, _ = events.Dispatch(ctx, x, notify.Senders(r, x, notify.Options{}), time.Now().UTC())
}

// oneLineField fits text to an event field: one line, at most 500 runes.
func oneLineField(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 500 {
		s = string(r[:499]) + "…"
	}
	return s
}

// tailLine keeps the last complete line written to it.
type tailLine struct {
	mu        sync.Mutex
	cur, line string
}

func (w *tailLine) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cur += string(b)
	if i := strings.LastIndexByte(w.cur, '\n'); i >= 0 {
		if l := strings.TrimSpace(w.cur[:i]); l != "" {
			if j := strings.LastIndexByte(l, '\n'); j >= 0 {
				l = l[j+1:]
			}
			w.line = strings.TrimSpace(l)
		}
		w.cur = w.cur[i+1:]
	}
	return len(b), nil
}

func (w *tailLine) get() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := strings.TrimSpace(w.cur); s != "" {
		return s
	}
	return w.line
}
