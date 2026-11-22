package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// applyLog is what engine.Apply and cli.Apply write, as a box streams it.
const applyLog = `keelbox apply: devbox (release 2026.11.2, Ubuntu 24.04 amd64)
host
  ok      packages
  change  timezone: set Europe/London
  change  firewall: ufw: enable
access
  ok      tailscale-repo
agents
  change  claude-code: install
  FAILED  codex: installer exited 1

Applied 3 change(s) in 2m05s. Recorded in /var/lib/keelbox/logbook.jsonl.
`

// fakeClock is a time the test moves.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func clock() func() time.Time {
	return (&fakeClock{time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}).now
}

func render(t *testing.T, st Style, log string, chunk int) string {
	t.Helper()
	var out bytes.Buffer
	p := NewProgress(&out, st, []string{"host", "access", "runtime", "agents"})
	p.now = clock()
	// Written in odd-sized pieces, as a pipe delivers them.
	for i := 0; i < len(log); i += chunk {
		j := min(i+chunk, len(log))
		if _, err := p.Write([]byte(log[i:j])); err != nil {
			t.Fatal(err)
		}
	}
	p.Close()
	return out.String()
}

// settled is what is left on screen: a small terminal that knows the
// moves the view makes (carriage return, erase line, erase down, up).
func settled(s string) string { return Screen(s) }

// The settled view: one line per phase that did something, a failure
// once under its phase with its cause, and the result.
func TestProgressOneLinePerPhase(t *testing.T) {
	got := settled(render(t, Style{Live: true, Width: 100}, applyLog, 7))
	want := `  Applying devbox release 2026.11.2, Ubuntu 24.04 amd64
  ok Hardening the server  2 changes              0s
  !! Agents                1 failed               0s
    - codex
      installer exited 1
  !! 1 step failed - the rest of the box is applied
`
	if got != want {
		t.Errorf("settled view:\n%s\nwant:\n%s", got, want)
	}
}

// An apply that changes nothing is one line.
func TestProgressUpToDateIsOneLine(t *testing.T) {
	log := "keelbox apply: devbox (release 2026.11.2, Ubuntu 24.04 amd64)\nhost\n  ok      packages\nagents\n  ok      codex\n\nNo changes. The box matches the config (140 checks, 3s).\n"
	got := settled(render(t, Style{Live: true, UTF8: true}, log, 5))
	if got != "  ✓ devbox is up to date · 140 checks in 3s\n" {
		t.Errorf("up to date:\n%q", got)
	}
}

// A failed step said by the box on its error stream is not said again.
func TestProgressFailureSaidOnce(t *testing.T) {
	var out bytes.Buffer
	p := NewProgress(&out, Style{Live: true, Width: 100}, []string{"agents"})
	p.now = clock()
	_, _ = p.Write([]byte("agents\n  FAILED  rc: agents/rc: still not converged after apply: start rc.service\n\nNo changes applied (140 checks, 3s).\n"))
	_, _ = p.Err(&out).Write([]byte("keelbox: agents/rc failed: still not converged after apply: start rc.service\n"))
	_, _ = p.Err(&out).Write([]byte("keelbox: 1 step failed; the rest of the box is applied. Fix and re-run keelbox apply.\n"))
	p.Close()
	s := settled(out.String())
	if n := strings.Count(s, "still not converged"); n != 1 || strings.Contains(s, "agents/rc: ") {
		t.Errorf("the failure is said %d times, or with its prefix twice:\n%s", n, s)
	}
}

func TestProgressLiveLines(t *testing.T) {
	var out bytes.Buffer
	p := NewProgress(&out, Style{Live: true, UTF8: true}, []string{"host", "runtime"})
	p.now = clock()
	_, _ = p.Write([]byte("runtime\n  change  docker-packages: install docker-ce\n"))
	live := out.String()
	for _, w := range []string{"Developer tools", "⎿ runtime/docker-packages: install docker-ce", "2/2", "⠋", "1 change"} {
		if !strings.Contains(live, w) {
			t.Errorf("live lines lack %q: %q", w, live)
		}
	}
	if strings.Contains(live, "\x1b[3") || strings.Contains(live, "\x1b[1m") {
		t.Errorf("colour without Colour: %q", live)
	}
	p.Close()
	if s := settled(out.String()); !strings.Contains(s, "✗ Developer tools") || !strings.Contains(s, "at docker-packages (step 1)") {
		t.Errorf("an unfinished phase is not shown as stopped: %q", s)
	}
}

func TestProgressPassesOtherLines(t *testing.T) {
	log := "keelbox: waiting for SSH\nhost\n  change  packages\nkeelbox: warning: something\n  ok      timezone\n\nApplied 1 change in 1s.\n"
	got := settled(render(t, Style{Live: true}, log, 3))
	for _, w := range []string{"keelbox: waiting for SSH\n", "keelbox: warning: something\n", "ok Hardening the server  1 change"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestProgressElapsed(t *testing.T) {
	var out bytes.Buffer
	c := &fakeClock{time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	p := NewProgress(&out, Style{Live: true}, []string{"runtime"})
	p.now = c.now
	_, _ = p.Write([]byte("runtime\n  change  docker-packages: install\n"))
	c.t = c.t.Add(187 * time.Second)
	_, _ = p.Write([]byte("\nApplied 1 change in 3m07s.\n"))
	p.Close()
	if s := settled(out.String()); !strings.Contains(s, "1 change") || !strings.Contains(s, " 3m07s\n") || !strings.Contains(s, "Applied in 3m07s - 1 change") {
		t.Errorf("elapsed time not shown:\n%s", s)
	}
}

func TestProgressErrStreamSettlesFirst(t *testing.T) {
	var out bytes.Buffer
	p := NewProgress(&out, Style{Live: true}, []string{"runtime"})
	p.now = clock()
	_, _ = p.Write([]byte("runtime\n  change  docker-packages: install\n\n"))
	_, _ = p.Err(&out).Write([]byte("keelbox: apply failed after 0 change(s): boom\n"))
	p.Close()
	s := settled(out.String())
	stop, fail := strings.Index(s, "at docker-packages"), strings.Index(s, "apply failed")
	if stop < 0 || fail < stop {
		t.Errorf("the phase should settle before the error:\n%s", s)
	}
}

// Columns line up whatever the phase names, and nothing passes the width.
func TestProgressColumnsAndWidth(t *testing.T) {
	for _, cols := range []int{60, 80, 120, 200} {
		got := settled(render(t, Style{Live: true, UTF8: true, Width: cols}, applyLog, 64))
		at := -1
		for _, l := range strings.Split(got, "\n") {
			if w := Width(l); w >= cols {
				t.Errorf("%d columns: a line is %d wide: %q", cols, w, l)
			}
			if strings.HasSuffix(l, "0s") && (strings.Contains(l, "✓") || strings.Contains(l, "✗")) {
				if at >= 0 && len([]rune(l)) != at {
					t.Errorf("%d columns: the time column moves: %q", cols, l)
				}
				at = len([]rune(l))
			}
		}
	}
}

func TestProgressColour(t *testing.T) {
	got := render(t, Style{Live: true, Colour: true, UTF8: true}, applyLog, 64)
	if !strings.Contains(got, "\x1b[32m✓\x1b[0m") || !strings.Contains(got, "\x1b[1mApplying devbox") || !strings.Contains(got, "\x1b[31m✗\x1b[0m") {
		t.Errorf("no styling with Colour: %q", got)
	}
}

func TestDetect(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name     string
		terminal bool
		env      func(string) string
		want     Style
	}{
		{"pipe", false, env("TERM", "xterm-256color"), Style{}},
		{"terminal", true, env("TERM", "xterm-256color", "LANG", "en_GB.UTF-8"), Style{Live: true, Colour: true, UTF8: true, Depth: Depth256}},
		{"truecolor", true, env("TERM", "xterm-256color", "COLORTERM", "truecolor", "LANG", "en_GB.UTF-8"), Style{Live: true, Colour: true, UTF8: true, Depth: DepthTrue}},
		{"light", true, env("TERM", "xterm", "COLORFGBG", "0;15"), Style{Live: true, Colour: true, Light: true}},
		{"links", true, env("TERM", "xterm-256color", "TERM_PROGRAM", "iTerm.app"), Style{Live: true, Colour: true, Depth: Depth256, Links: true}},
		{"links in tmux", true, env("TERM", "xterm-256color", "TERM_PROGRAM", "iTerm.app", "TMUX", "/tmp/t"), Style{Live: true, Colour: true, Depth: Depth256}},
		{"screen reader", true, env("TERM", "xterm", "ACCESSIBLE", "1"), Style{Colour: true, Accessible: true}},
		{"no colour", true, env("TERM", "xterm", "NO_COLOR", "1"), Style{Live: true}},
		{"dumb", true, env("TERM", "dumb", "LANG", "C.UTF-8"), Style{}},
		{"LC_ALL wins", true, env("LC_ALL", "C", "LANG", "en_GB.UTF-8"), Style{Live: true, Colour: true}},
	}
	for _, c := range cases {
		if got := Detect(c.terminal, c.env); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{41 * time.Second: "41s", 187 * time.Second: "3m07s", 59600 * time.Millisecond: "1m00s"} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%s) = %s, want %s", d, got, want)
		}
	}
}

// Ctrl-C stops between steps: the module is not shown as failed, and it
// names the step done last, the one the apply's own message follows.
func TestProgressCtrlCIsNotAFailure(t *testing.T) {
	var out bytes.Buffer
	p := NewProgress(&out, Style{Live: true}, []string{"host", "runtime"})
	p.now = clock()
	_, _ = p.Write([]byte("runtime\n  change  docker-packages: install\n\n"))
	_, _ = p.Err(&out).Write([]byte("keelbox: apply stopped before runtime/docker-daemon-json, after 1 change.\n"))
	p.Close()
	s := settled(out.String())
	if !strings.Contains(s, "  - Developer tools") || !strings.Contains(s, "stopped after docker-packages (step 1)") || strings.Contains(s, "!!") {
		t.Errorf("Ctrl-C shown as a failure:\n%s", s)
	}
}

// A deferred step is reported after the last module, not inside it.
func TestProgressDeferredAfterTheModules(t *testing.T) {
	log := "host\n  change  packages: install\n  defer   cloud-user-locked: later\n\nDeferred host/cloud-user-locked: later.\nApplied 1 change in 1s.\n"
	got := settled(render(t, Style{Live: true}, log, 1))
	if mod, def := strings.Index(got, "ok Hardening the server"), strings.Index(got, "Deferred host"); mod < 0 || def < mod {
		t.Errorf("deferred line inside the module list:\n%s", got)
	}
}
