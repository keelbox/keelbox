// Package doctorrun is scheduled doctor: what keelbox-doctor.timer starts
// (docs/adr/0042-scheduled-doctor.md). It runs the same checks as keelbox
// doctor, keeps the result in a root file a laptop's doctor never touches,
// and raises an event only when a check changes state: doctor-failed when
// it starts failing, doctor-cleared when it is ok again. A check that
// depends on the network (Finding.Flaky) must fail twice in a row first.
package doctorrun

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

// Paths and units on the box.
const (
	Dir      = "/var/lib/keelbox/doctor"
	LastPath = Dir + "/last.json"
	Service  = "keelbox-doctor.service"
	Timer    = "keelbox-doctor.timer"
	// Command is the internal command the service runs.
	Command = "doctor-run"
	// SlowAfter is the run time past which doctor warns.
	SlowAfter = time.Minute
)

// Finding is one check that is not ok, as recorded.
type Finding struct {
	Check  string `json:"check"` // module/check
	Level  string `json:"level"` // warn or FAIL
	Detail string `json:"detail"`
	Flaky  bool   `json:"flaky,omitempty"`
}

// Last is the newest scheduled run (0644 root). It holds what doctor
// prints, which carries no secret, so the admin may read it.
type Last struct {
	Time     time.Time `json:"time"`
	Keelbox  string    `json:"keelbox"`
	Release  string    `json:"release"`
	Seconds  float64   `json:"seconds"`
	OK       int       `json:"ok"`
	Warnings int       `json:"warnings"`
	Failures int       `json:"failures"`
	Findings []Finding `json:"findings,omitempty"`
	// Streak counts the runs in a row each check has wanted an alert, and
	// Alerted the checks whose alert is out, so the next run knows what
	// changed.
	Streak  map[string]int  `json:"streak,omitempty"`
	Alerted map[string]bool `json:"alerted,omitempty"`
}

// ReadLast reads the last run; found is false before the first.
func ReadLast(x system.Executor) (Last, bool) {
	var l Last
	data, err := x.ReadFile(LastPath)
	if err != nil || json.Unmarshal(data, &l) != nil || l.Time.IsZero() {
		return Last{}, false
	}
	return l, true
}

// Options are the owner's choices for scheduled doctor.
type Options struct {
	Box, Keelbox, Release string
	// Warnings alerts on warnings as well as failures
	// (doctor.notify_warnings).
	Warnings bool
}

// Transition is one check changing state.
type Transition struct {
	Check  string
	Failed bool // true: started failing (or warning); false: ok again
	Level  engine.Level
	Detail string
}

var unsafeKey = regexp.MustCompile(`[^A-Za-z0-9._:/@-]`)

// Record compares fs, the findings of a run that took took, with the
// previous run, saves the new state and returns what changed. It emits
// nothing: Notify does, under the day-two lock.
func Record(x system.Executor, fs []engine.Finding, took time.Duration, now time.Time, o Options) ([]Transition, error) {
	prev, _ := ReadLast(x)
	cur := Last{Time: now.UTC(), Keelbox: o.Keelbox, Release: o.Release, Seconds: took.Seconds(),
		Streak: map[string]int{}, Alerted: map[string]bool{}}
	// The worst finding per check, since a module may report one check
	// more than once.
	worst := map[string]engine.Finding{}
	for _, f := range fs {
		switch f.Level {
		case engine.LevelOK:
			cur.OK++
		case engine.LevelWarn:
			cur.Warnings++
		default:
			cur.Failures++
		}
		id := f.Module + "/" + f.Check
		if w, seen := worst[id]; !seen || f.Level > w.Level {
			worst[id] = f
		}
		if f.Level != engine.LevelOK {
			cur.Findings = append(cur.Findings, Finding{Check: id, Level: f.Level.String(), Detail: oneLine(f.Detail), Flaky: f.Flaky})
		}
	}
	alertable := func(f engine.Finding) bool {
		return f.Level == engine.LevelFail || (o.Warnings && f.Level == engine.LevelWarn)
	}
	var out []Transition
	ids := make([]string, 0, len(worst)+len(prev.Alerted))
	for id := range worst {
		ids = append(ids, id)
	}
	for id := range prev.Alerted {
		if _, ok := worst[id]; !ok {
			ids = append(ids, id) // a check that is gone is ok
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		f, present := worst[id]
		if present && alertable(f) {
			cur.Streak[id] = prev.Streak[id] + 1
			due := !f.Flaky || cur.Streak[id] >= 2
			switch {
			case prev.Alerted[id]:
				cur.Alerted[id] = true
			case due:
				cur.Alerted[id] = true
				out = append(out, Transition{Check: id, Failed: true, Level: f.Level, Detail: oneLine(f.Detail)})
			}
			continue
		}
		if prev.Alerted[id] {
			out = append(out, Transition{Check: id, Failed: false, Level: engine.LevelOK, Detail: oneLine(f.Detail)})
		}
	}
	b, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := x.MkdirAll(Dir, 0o755, "root", "root"); err != nil {
		return nil, err
	}
	return out, x.WriteFile(LastPath, append(b, '\n'), 0o644, "root", "root")
}

// Notify raises an event and writes a logbook record for each transition.
func Notify(x system.Executor, ts []Transition, o Options, now time.Time) error {
	if len(ts) == 0 {
		return nil
	}
	var changes []engine.Change
	for _, t := range ts {
		e := events.Event{Time: now.UTC(), Source: events.SourceDoctor, Box: o.Box,
			Key: "doctor:" + unsafeKey.ReplaceAllString(t.Check, "_"), Fields: map[string]string{"check": t.Check}}
		module, step, _ := strings.Cut(t.Check, "/")
		switch {
		case !t.Failed:
			e.Kind, e.Level = "doctor-cleared", events.Info
			changes = append(changes, engine.Change{Module: module, Step: step, Detail: "doctor: ok again"})
		case t.Level == engine.LevelFail:
			e.Kind, e.Level = "doctor-failed", events.Urgent
			changes = append(changes, engine.Change{Module: module, Step: step, Detail: "doctor: FAIL: " + t.Detail})
		default:
			e.Kind, e.Level = "doctor-warning", events.Warn
			changes = append(changes, engine.Change{Module: module, Step: step, Detail: "doctor: warn: " + t.Detail})
		}
		if t.Detail != "" && t.Failed {
			e.Fields["detail"] = t.Detail
		}
		if _, err := events.Emit(x, e); err != nil {
			return fmt.Errorf("doctor event for %s: %w", t.Check, err)
		}
	}
	return logbook.Action(x, logbook.Record{Kind: logbook.KindDoctor, Box: o.Box, Keelbox: o.Keelbox, Time: now.UTC(), Changes: changes})
}

// oneLine makes a finding's detail an event field: one line of printable
// text, at most 500 characters.
func oneLine(s string) string {
	s = plain.Line(s)
	if r := []rune(s); len(r) > 500 {
		s = string(r[:499]) + "…"
	}
	return s
}

// Snapshot is the last run for the status snapshot, or nil before the
// first.
func Snapshot(x system.Executor) *snapshot.Doctor {
	l, found := ReadLast(x)
	if !found {
		return nil
	}
	d := &snapshot.Doctor{Time: l.Time, OK: l.OK, Warnings: l.Warnings, Failures: l.Failures}
	for _, f := range l.Findings {
		if f.Level == engine.LevelFail.String() {
			d.Failing = append(d.Failing, f.Check)
		}
	}
	return d
}

// Summary is keelbox status's doctor row.
func Summary(x system.Executor, schedule string, now time.Time) string {
	l, found := ReadLast(x)
	switch {
	case schedule == "" && !found:
		return "never run on a schedule (doctor.schedule: off)"
	case !found:
		return "no scheduled run yet (" + schedule + ")"
	}
	s := fmt.Sprintf("%s (%s ago): %d failures, %d warnings", l.Time.Local().Format("2006-01-02 15:04"), now.Sub(l.Time).Round(time.Minute), l.Failures, l.Warnings)
	var failing []string
	for _, f := range l.Findings {
		if f.Level == engine.LevelFail.String() {
			failing = append(failing, f.Check)
		}
	}
	if len(failing) > 0 {
		s += " (" + strings.Join(failing, ", ") + ")"
	}
	if schedule == "" {
		s += "; doctor.schedule: off"
	}
	return s
}
