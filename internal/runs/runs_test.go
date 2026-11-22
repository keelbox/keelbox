package runs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

const id = "20261109-140102-ab12"

func clock() func() time.Time {
	t := time.Date(2026, 11, 9, 14, 1, 2, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

func begin(t *testing.T, x *system.Fake) *Log {
	t.Helper()
	l, err := Begin(x, Record{ID: id, Box: "acme", Keelbox: "v0.1.0", By: "sam", Modules: []string{"host", "access"}, Time: time.Date(2026, 11, 9, 14, 1, 2, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	l.Now = clock()
	return l
}

// apply's own log, as engine.Apply and cli.Apply write it.
const applyLog = "keelbox apply: acme (release 2026.11.2, Ubuntu 26.04 amd64)\n" +
	"host\n" +
	"  ok      hostname\n" +
	"  change  admin-user: create user admin\n" +
	"access\n" +
	"  change  ufw: enable\n" +
	"  FAILED  tailscale: install: exit 1 (soft)\n" +
	"\n" +
	"Applied 2 changes in 41s. Recorded in /var/lib/keelbox/logbook.jsonl.\n"

func TestLogRecordsApplyOutputAndRendersItBack(t *testing.T) {
	x := system.NewFake()
	l := begin(t, x)
	if _, err := io.WriteString(l.Stdout(), applyLog); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l.Stderr(), "keelbox: access/tailscale failed: install: exit 1\n")
	if err := l.End(Record{Result: ResultPartial, Code: 1, Steps: 4, Changed: 2, Failed: 1}); err != nil {
		t.Fatal(err)
	}
	if err := l.End(Record{Result: ResultOK}); err != nil {
		t.Fatal(err)
	}
	recs, err := Read(x, id)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	kinds := []string{}
	for _, r := range recs {
		kinds = append(kinds, r.Kind)
		if line, stderr, ok := r.Line(); ok {
			if stderr {
				errOut.WriteString(line + "\n")
			} else {
				out.WriteString(line + "\n")
			}
		}
	}
	if out.String() != applyLog {
		t.Errorf("rendered output differs from apply's:\n%s\nwant:\n%s", out.String(), applyLog)
	}
	if errOut.String() != "keelbox: access/tailscale failed: install: exit 1\n" {
		t.Errorf("stderr: %q", errOut.String())
	}
	want := "start out module step step module-done module step step out out err module-done end"
	if strings.Join(kinds, " ") != want {
		t.Errorf("kinds:\n%s\nwant:\n%s", strings.Join(kinds, " "), want)
	}
	for _, r := range recs {
		if r.Kind == KindModuleDone && r.Module == "access" && (r.Steps != 2 || r.Changed != 1 || r.Failed != 1) {
			t.Errorf("access tally: %+v", r)
		}
	}
	s := Summarise(recs)
	if !s.Done || s.Result != ResultPartial || s.Code != 1 || s.Steps != 4 || s.Changed != 2 || s.Failed != 1 || s.By != "sam" {
		t.Errorf("summary: %+v", s)
	}
	f := x.Files[Path(id)]
	if f.Mode != 0o640 || f.Owner != "root" || f.Group != Group {
		t.Errorf("log mode %o %s:%s", f.Mode, f.Owner, f.Group)
	}
	if c, _ := Current(x); c != id {
		t.Errorf("current = %q", c)
	}
}

func TestLogTextIsPlainAndBounded(t *testing.T) {
	x := system.NewFake()
	l := begin(t, x)
	fmt.Fprintf(l.Stdout(), "name \x1b[2J\u202e%s\n", strings.Repeat("x", MaxText+50))
	big := strings.Repeat("y", 1000) + "\n"
	for i := 0; i < MaxBytes/1000+10; i++ {
		_, _ = io.WriteString(l.Stdout(), big)
	}
	if err := l.End(Record{Result: ResultOK}); err != nil {
		t.Fatal(err)
	}
	data := x.Files[Path(id)].Data
	if len(data) > MaxBytes+4096 {
		t.Errorf("log is %d bytes, over the %d bound", len(data), MaxBytes)
	}
	recs, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	first := recs[1].Text
	if strings.ContainsAny(first, "\x1b\u202e") || !strings.Contains(first, `\x1b[2J\u202e`) || len([]rune(first)) != MaxText {
		t.Errorf("text not escaped and cut: %.60q (%d runes)", first, len([]rune(first)))
	}
	if !strings.Contains(string(data), "reached 4 MiB") || recs[len(recs)-1].Kind != KindEnd {
		t.Error("a full log does not say so, or lost its end record")
	}
}

func TestBeginKeepsTheNewestRuns(t *testing.T) {
	x := system.NewFake()
	for i := 0; i < Keep+3; i++ {
		x.AddFile(Path(fmt.Sprintf("20261101-0000%02d-0000", i)), "{}\n", 0o640)
	}
	begin(t, x)
	names, _ := x.ReadDir(Dir)
	var logs []string
	for _, n := range names {
		if strings.HasSuffix(n, ".jsonl") {
			logs = append(logs, n)
		}
	}
	if len(logs) != Keep || logs[0] != "20261101-000004-0000.jsonl" || logs[len(logs)-1] != id+".jsonl" {
		t.Errorf("kept %d logs: %v", len(logs), logs)
	}
}

func TestParseLeavesOutAPartialLine(t *testing.T) {
	recs, err := Parse([]byte(`{"kind":"start","id":"` + id + `"}` + "\n" + `{"kind":"out","te`))
	if err != nil || len(recs) != 1 {
		t.Fatalf("%v %v", recs, err)
	}
	if _, err := Parse([]byte("nonsense\n")); err == nil {
		t.Error("a broken line parsed")
	}
}

func TestLastTellsRunningFinishedAndDied(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 14, 30, 0, 0, time.UTC)
	x := system.NewFake()
	if _, ok, err := Last(ctx, x); ok || err != nil {
		t.Fatalf("no run: ok %v, %v", ok, err)
	}
	l := begin(t, x)
	_, _ = io.WriteString(l.Stdout(), "host\n  change  admin-user: create user admin\n")
	x.OK("systemctl is-active --quiet "+Unit(id), "")
	s, ok, err := Last(ctx, x)
	if !ok || err != nil || !s.Running || s.Died() {
		t.Fatalf("running: %+v %v %v", s, ok, err)
	}
	if d := s.Describe(now); !strings.HasPrefix(d, "running since 28 min ago, at host/admin-user (1 step, 1 changed so far)") {
		t.Errorf("running: %s", d)
	}
	x.Fail("systemctl is-active --quiet " + Unit(id))
	s, _, _ = Last(ctx, x)
	if !s.Died() || !strings.Contains(s.Describe(now), "interrupted: stopped without finishing after host/admin-user") {
		t.Errorf("died: %+v %s", s, s.Describe(now))
	}
	_ = l.End(Record{Time: now.Add(-6 * time.Minute), Result: ResultOK, Steps: 31, Changed: 24})
	s, _, _ = Last(ctx, x)
	if d := s.Describe(now); d != "finished 6 min ago: 31 steps, 24 changed, ok" {
		t.Errorf("finished: %s", d)
	}
}

func TestRemoteScriptsTakeOnlyRunIDs(t *testing.T) {
	for _, bad := range []string{"", "x; rm -rf /", "20261109-140102-ab12'", "../20261109-140102-ab12"} {
		if _, err := FollowScript(bad, 0); err == nil {
			t.Errorf("FollowScript took %q", bad)
		}
		if _, err := StopCommand(bad); err == nil {
			t.Errorf("StopCommand took %q", bad)
		}
	}
	s, err := FollowScript(id, 120)
	if err != nil || !strings.Contains(s, "tail -c +121 --pid=") || !strings.Contains(s, Unit(id)) {
		t.Errorf("follow script:\n%s", s)
	}
	if c, _ := StopCommand(id); c != "systemctl kill --kill-whom=main --signal=SIGINT keelbox-apply-"+id+".service" {
		t.Errorf("stop: %s", c)
	}
}

func TestStateScriptOnAShell(t *testing.T) {
	// No runs directory here: the id and state are "-".
	out, err := exec.Command("sh", "-c", StateScript()).Output()
	if err != nil {
		t.Fatal(err)
	}
	now, id, st, err := ParseState(string(out))
	if err != nil || id != "-" || st != "-" || time.Since(now) > time.Minute {
		t.Errorf("%q: %v %s %s %v", out, now, id, st, err)
	}
	for _, bad := range []string{"", "x y", "1 x; active", "1 2 3 4"} {
		if _, _, _, err := ParseState(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
	if _, rid, rst, err := ParseState("1762696862 " + id2 + " active\n"); err != nil || rid != id2 || !Running(rst) {
		t.Errorf("%s %s %v", rid, rst, err)
	}
}

const id2 = "20261109-140102-ab12"

func TestBeginLetsTheSudoGroupThrough(t *testing.T) {
	x := system.NewFake()
	if err := x.MkdirAll("/var/lib/keelbox/stage", 0o700, "root", "root"); err != nil {
		t.Fatal(err)
	}
	x.Files["/var/lib/keelbox"] = &system.FakeFile{Dir: true, Mode: 0o700, Owner: "root", Group: "root"}
	begin(t, x)
	if f := x.Files["/var/lib/keelbox"]; f.Mode != 0o755 {
		t.Errorf("/var/lib/keelbox is %o: an account in the sudo group cannot reach the run log", f.Mode)
	}
	if f := x.Files[Dir]; f.Mode != 0o750 || f.Group != Group {
		t.Errorf("%s: %o %s", Dir, f.Mode, f.Group)
	}
}

func TestStateScriptCallsARunThatEndedOver(t *testing.T) {
	// A fake systemctl says the unit is active, as it is while a finished
	// run waits to see whether anyone follows it.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho active\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "runs")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "current"), []byte(id2+"\n"), 0o644)
	state := func(log string) string {
		t.Helper()
		_ = os.WriteFile(filepath.Join(dir, id2+".jsonl"), []byte(log), 0o644)
		cmd := exec.Command("sh", "-c", strings.ReplaceAll(StateScript(), Dir, dir))
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		_, _, st, err := ParseState(string(out))
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := state(`{"kind":"start"}` + "\n" + `{"kind":"step"}` + "\n"); !Running(st) {
		t.Errorf("a run mid-way: %s", st)
	}
	if st := state(`{"kind":"start"}` + "\n" + `{"kind":"end","result":"ok"}` + "\n"); Running(st) || st != "ended" {
		t.Errorf("a run that wrote its end: %s", st)
	}
}
