package doctorrun

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/system"
)

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

func f(check string, l engine.Level, flaky bool) engine.Finding {
	m, c, _ := strings.Cut(check, "/")
	return engine.Finding{Module: m, Check: c, Level: l, Detail: check + " detail\nsecond line", Flaky: flaky}
}

func run(t *testing.T, x *system.Fake, at time.Duration, o Options, fs ...engine.Finding) []string {
	t.Helper()
	ts, err := Record(x, fs, 3*time.Second, t0.Add(at), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := Notify(x, ts, o, t0.Add(at)); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tr := range ts {
		s := tr.Check + " ok"
		if tr.Failed {
			s = tr.Check + " " + tr.Level.String()
		}
		out = append(out, s)
	}
	return out
}

func newBox(t *testing.T) *system.Fake {
	t.Helper()
	x := system.NewFake()
	if err := logbook.Append(x, logbook.Record{Time: t0.Add(-time.Hour), Box: "acme", Keelbox: "0.2.0", Release: "2026.11.1", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	return x
}

func TestTransitions(t *testing.T) {
	x := newBox(t)
	o := Options{Box: "acme", Keelbox: "0.2.0", Release: "2026.11.1"}
	steps := []struct {
		fs   []engine.Finding
		want string
	}{
		{[]engine.Finding{f("watch/timer", engine.LevelOK, false), f("host/sshd", engine.LevelWarn, false)}, ""},
		{[]engine.Finding{f("watch/timer", engine.LevelFail, false), f("host/sshd", engine.LevelWarn, false)}, "watch/timer FAIL"},
		{[]engine.Finding{f("watch/timer", engine.LevelFail, false)}, ""}, // still failing: no repeat
		{[]engine.Finding{f("watch/timer", engine.LevelFail, false), f("access/tailnet", engine.LevelFail, true)}, ""},
		{[]engine.Finding{f("watch/timer", engine.LevelOK, false), f("access/tailnet", engine.LevelFail, true)}, "access/tailnet FAIL,watch/timer ok"},
		{[]engine.Finding{f("access/tailnet", engine.LevelOK, true)}, "access/tailnet ok"},
		{[]engine.Finding{f("access/tailnet", engine.LevelFail, true)}, ""}, // one flaky failure is not enough
		{[]engine.Finding{f("access/tailnet", engine.LevelOK, true)}, ""},
		{[]engine.Finding{f("backup/last-backup", engine.LevelFail, false)}, "backup/last-backup FAIL"},
		{nil, "backup/last-backup ok"}, // a check that is gone counts as ok
	}
	for i, s := range steps {
		if got := strings.Join(run(t, x, time.Duration(i)*time.Hour, o, s.fs...), ","); got != s.want {
			t.Errorf("run %d: %q, want %q", i, got, s.want)
		}
	}
	q, _ := events.Pending(x)
	var kinds []string
	for _, e := range q {
		kinds = append(kinds, e.Event.Kind+" "+e.Event.Fields["check"])
		if e.Event.Kind == "doctor-failed" && (strings.Contains(e.Event.Fields["detail"], "\n") || e.Event.Level != events.Urgent || e.Event.Key != "doctor:"+e.Event.Fields["check"]) {
			t.Errorf("event %+v", e.Event)
		}
	}
	sort.Strings(kinds)
	want := "doctor-cleared access/tailnet,doctor-cleared backup/last-backup,doctor-cleared watch/timer,doctor-failed access/tailnet,doctor-failed backup/last-backup,doctor-failed watch/timer"
	if strings.Join(kinds, ",") != want {
		t.Errorf("queued:\n%s\nwant\n%s", strings.Join(kinds, ","), want)
	}
	recs, _ := logbook.Read(x)
	n := 0
	for _, r := range recs {
		if r.Kind == logbook.KindDoctor {
			n++
			if r.Release != "2026.11.1" || len(r.Changes) == 0 {
				t.Errorf("record %+v", r)
			}
		}
	}
	if n != 5 {
		t.Errorf("%d doctor records, want one per run with a change (5)", n)
	}
	if d := Snapshot(x); d == nil || d.Failures != 0 {
		t.Errorf("snapshot %+v", d)
	}
}

func TestWarnings(t *testing.T) {
	x := newBox(t)
	o := Options{Box: "acme", Keelbox: "0.2.0", Release: "2026.11.1"}
	if got := run(t, x, 0, o, f("host/sshd", engine.LevelWarn, false)); len(got) != 0 {
		t.Errorf("a warning alerted without doctor.notify_warnings: %v", got)
	}
	o.Warnings = true
	if got := run(t, x, time.Hour, o, f("host/sshd", engine.LevelWarn, false)); strings.Join(got, ",") != "host/sshd warn" {
		t.Errorf("with notify_warnings: %v", got)
	}
	q, _ := events.Pending(x)
	if len(q) != 1 || q[0].Event.Kind != "doctor-warning" || q[0].Event.Level != events.Warn {
		t.Errorf("queue %+v", q)
	}
	l, _ := ReadLast(x)
	if l.Warnings != 1 || len(l.Findings) != 1 || l.Seconds != 3 || x.Files[LastPath].Mode != 0o644 {
		t.Errorf("last %+v", l)
	}
	if s := Summary(x, "hourly", t0.Add(90*time.Minute)); !strings.Contains(s, "0 failures, 1 warnings") {
		t.Errorf("summary %q", s)
	}
	if s := Summary(system.NewFake(), "", t0); !strings.Contains(s, "doctor.schedule: off") {
		t.Errorf("summary off %q", s)
	}
}
