package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/keelbox/keelbox/internal/doctorrun"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/notify"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/reports"
	"github.com/keelbox/keelbox/internal/runlock"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/version"
	"github.com/keelbox/keelbox/internal/watch"
)

// internalCommands run on the box only, started by keelbox's own units
// against the applied config. They are not in help.
var internalCommands = map[string]bool{
	"previews-sync":       true,
	watch.Command:         true,
	modules.EventsCommand: true,
	doctorrun.Command:     true,
	watch.BootCommand:     true,
	installs.Command:      true,
}

// hasCommand is the ExecCondition of every day-two unit: exit 0 when this
// keelbox knows the command, 1 when it does not. A rollback can install an
// older keelbox, and one that does not know has-command exits 2. systemd
// skips the run for any exit from 1 to 254 instead of failing the unit.
func hasCommand(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "keelbox: "+modules.HasCommand+" takes one command name")
		return 2
	}
	if _, known := findCmd(args[0]); known || internalCommands[args[0]] {
		return 0
	}
	return 1
}

// eventsSettle is how long the events service waits before reading, so a
// burst of hook lines is one run, not one run per line (ADR 0030).
var eventsSettle = time.Second

// senders are the notification channels day-two runs send to, from the
// applied config (ADR 0043). With none, events wait in the queue.
func senders(e *engine.Env) []events.Sender { return notify.Senders(e.Cfg, e.X, notify.Options{}) }

// dayTwo runs one day-two command under the locks of ADR 0030: it skips,
// with exit 0, while an apply runs.
func dayTwo(ctx context.Context, cmd string, e *engine.Env, stdout, stderr io.Writer) int {
	if cmd == modules.EventsCommand {
		// Before the lock: a file the agent changes without pause gets
		// one run per events.RunPace, and a change that added nothing
		// gets no run at all.
		time.Sleep(events.Pace(e.X, time.Now().UTC()) + eventsSettle)
		if events.NothingNew(e.X, e.AgentUser()) {
			fmt.Fprintf(stdout, "keelbox %s: nothing new in the agent's events file\n", cmd)
			return 0
		}
	}
	shared, own, err := runlock.DayTwo(runlock.Path, runlock.DayTwoPath, 30*time.Second)
	if errors.Is(err, runlock.ErrBusy) {
		fmt.Fprintf(stdout, "keelbox %s: skipped, an apply or another day-two run holds the lock\n", cmd)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", cmd, err)
		return 1
	}
	defer shared.Unlock()
	defer own.Unlock()
	switch cmd {
	case watch.Command:
		e.Log = stdout
		err = watch.Run(ctx, e, senders(e), time.Now().UTC())
	case modules.EventsCommand:
		var r events.AgentRead
		var d events.Delivery
		r, d, err = events.SyncAgent(ctx, e.X, e.AgentUser(), e.Cfg.Box, events.AgentOptions{Text: e.Cfg.NotifyAgentText()}, senders(e), func() time.Time { return time.Now().UTC() })
		for _, p := range append(r.Problems, fileReports(ctx, e, r.Reports)...) {
			fmt.Fprintln(stderr, "keelbox events-sync:", plain.Line(p))
		}
		fmt.Fprintf(stdout, "keelbox events-sync: %d event(s) queued, %d line(s) skipped; %d sent, %d waiting, %d dropped\n",
			len(r.Events), r.Skipped, d.Sent, d.Waiting, d.Dropped)
	}
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

// doctorRun is keelbox-doctor.service. It reads the box with only the apply
// lock shared, so a run of several seconds never holds up the watch run or
// the events service, then takes the day-two lock to queue and send what
// changed (ADR 0030, 0042).
func doctorRun(ctx context.Context, e *engine.Env, mods []engine.Module, stdout, stderr io.Writer) int {
	shared, err := runlock.Shared(runlock.Path)
	if errors.Is(err, runlock.ErrBusy) {
		fmt.Fprintf(stdout, "keelbox %s: skipped, an apply holds the lock\n", doctorrun.Command)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", doctorrun.Command, err)
		return 1
	}
	defer shared.Unlock()
	start := time.Now()
	fs := engine.Doctor(ctx, e, mods)
	took := time.Since(start)
	o := doctorrun.Options{Box: e.Cfg.Box, Keelbox: version.Version, Release: e.M.Release, Warnings: e.Cfg.DoctorNotifyWarnings()}
	own, err := runlock.Own(runlock.DayTwoPath, 30*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", doctorrun.Command, err)
		return 1
	}
	defer own.Unlock()
	now := time.Now().UTC()
	ts, err := doctorrun.Record(e.X, fs, took, now, o)
	if err == nil {
		err = doctorrun.Notify(e.X, ts, o, now)
	}
	if err == nil {
		_, err = events.Dispatch(ctx, e.X, senders(e), now)
	}
	l, _ := doctorrun.ReadLast(e.X)
	fmt.Fprintf(stdout, "keelbox %s: %d ok, %d warnings, %d failures in %s; %d change(s)\n", doctorrun.Command, l.OK, l.Warnings, l.Failures, took.Round(time.Second), len(ts))
	for _, t := range ts {
		state := "ok again"
		if t.Failed {
			state = t.Level.String() + ": " + t.Detail
		}
		fmt.Fprintf(stdout, "  %s %s\n", t.Check, state)
	}
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", doctorrun.Command, err)
		return 1
	}
	return 0
}

// bootMark is keelbox-boot.service's start (ADR 0040): it tells whether the
// boot before this one ended with a clean shutdown. It reads no config, so
// a config that does not load cannot hide a crash.
func bootMark(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "keelbox: "+watch.BootCommand+" takes no arguments; keelbox-boot.service runs it")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "keelbox: "+watch.BootCommand+" runs as root, from keelbox-boot.service")
		return 1
	}
	msg, err := watch.BootStart(&system.Real{}, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", watch.BootCommand, err)
		return 1
	}
	fmt.Fprintf(stdout, "keelbox %s: %s\n", watch.BootCommand, msg)
	return 0
}

// fileReports puts the keelbox reports the agent filed in the box's inbox,
// with the box's facts, or drops them with reports.enabled: false. Nothing
// is queued or sent: the owner reads them with keelbox reports BOX.
func fileReports(ctx context.Context, e *engine.Env, lines []events.AgentLine) []string {
	var problems []string
	for _, l := range lines {
		if !e.Cfg.ReportsEnabled() {
			problems = append(problems, "dropped keelbox report "+l.Session+": reports.enabled is false")
			continue
		}
		r := reports.Report{ID: l.Session, Filed: l.Time, Received: time.Now().UTC(), Title: l.Title, Area: l.Area, Detail: l.Detail, Facts: reports.FactsOf(ctx, e)}
		if err := reports.Save(e.X, r); err != nil {
			problems = append(problems, "dropped keelbox report "+l.Session+": "+err.Error())
			continue
		}
		problems = append(problems, "filed keelbox report "+l.Session+" in "+reports.Dir)
	}
	return problems
}
