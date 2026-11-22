package runs

import (
	"fmt"
	"time"
)

// Summary is what a run's log says about it.
type Summary struct {
	ID, Box, By    string
	Started, Ended time.Time
	// Steps, Changed and Failed come from the end record when there is
	// one, else from the module and step records so far.
	Steps, Changed, Failed int
	// Module and Step are the last step the run logged.
	Module, Step string
	// Done is set once the end record is written: Result, Code and Error
	// are then the run's.
	Done   bool
	Result string
	Code   int
	Error  string
	// Running is set by Last (and the laptop's follower) when the run's
	// unit is active. A run neither Done nor Running died: the box
	// restarted, or the process was killed, before it could finish.
	Running bool
}

// Summarise reads records, oldest first.
func Summarise(recs []Record) Summary {
	var s Summary
	for _, r := range recs {
		switch r.Kind {
		case KindStart:
			s.ID, s.Box, s.By, s.Started = r.ID, r.Box, r.By, r.Time
		case KindStep:
			s.Steps++
			s.Module, s.Step = r.Module, r.Step
			switch r.Word {
			case "change":
				s.Changed++
			case "FAILED", "skip":
				s.Failed++
			}
		case KindEnd:
			s.Done, s.Ended, s.Result, s.Code, s.Error = true, r.Time, r.Result, r.Code, r.Error
			if r.Steps > 0 || r.Changed > 0 || r.Failed > 0 {
				s.Steps, s.Changed, s.Failed = r.Steps, r.Changed, r.Failed
			}
		}
	}
	return s
}

// Died reports a run that stopped without its end record and is not
// running: the box restarted mid-run, or the process was killed.
func (s Summary) Died() bool { return !s.Done && !s.Running }

// Tally is "31 steps, 24 changed" (and how many failed).
func (s Summary) Tally() string {
	t := plural(s.Steps, "step", "steps")
	if s.Changed > 0 {
		t += fmt.Sprintf(", %d changed", s.Changed)
	}
	if s.Failed > 0 {
		t += fmt.Sprintf(", %d failed", s.Failed)
	}
	return t
}

// At is where the run is or stopped: module/step, or "" before its first step.
func (s Summary) At() string {
	if s.Step == "" {
		return ""
	}
	return s.Module + "/" + s.Step
}

// Describe says where the run is in one line, for status, doctor and the
// laptop's report: "finished 6 min ago: 31 steps, 24 changed, ok".
func (s Summary) Describe(now time.Time) string {
	switch {
	case s.Running:
		d := "running since " + Ago(now.Sub(s.Started), s.Started)
		if at := s.At(); at != "" {
			d += fmt.Sprintf(", at %s (%s so far)", at, s.Tally())
		}
		return d
	case s.Done:
		d := fmt.Sprintf("finished %s: %s, %s", Ago(now.Sub(s.Ended), s.Ended), s.Tally(), s.Result)
		if s.Result == ResultInterrupted {
			d = fmt.Sprintf("stopped as asked %s: %s", Ago(now.Sub(s.Ended), s.Ended), s.Tally())
		}
		return d
	}
	d := "interrupted: stopped without finishing"
	if at := s.At(); at != "" {
		d += " after " + at
	}
	return d + fmt.Sprintf(" (%s; started %s); the box restarted or keelbox was stopped", s.Tally(), Ago(now.Sub(s.Started), s.Started))
}

// Ago is a short time since: "just now", "40 s ago", "6 min ago", "3 h ago",
// or the date for anything older than a day.
func Ago(d time.Duration, t time.Time) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return "on " + t.UTC().Format("2006-01-02 15:04 UTC")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
