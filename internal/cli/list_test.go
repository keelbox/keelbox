package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/ui"
)

// registerEveryState registers a box in every state keelbox ls names.
func registerEveryState(t *testing.T) {
	t.Helper()
	isolate(t)
	for box, up := range map[string]upState{
		"alpha": {Via: viaSSH, Stage: stageReady, Address: "203.0.113.7"},
		"beta":  {Via: viaSSH, Stage: stageApplied, Address: "203.0.113.8"},
		"zeta":  {Via: viaSSH, Stage: stageReady, Address: "203.0.113.10", Left: []string{"codex", "gh"}},
	} {
		s := config.StarterFor(box, "ssh")
		s.SSHHost = up.Address
		register(t, s)
		if err := saveUpState(box, up); err != nil {
			t.Fatal(err)
		}
	}
	register(t, config.StarterFor("delta", "hetzner"))
	_ = saveParkMark("delta", parkMark{Since: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)})
	register(t, config.StarterFor("echo", "hetzner"))
	_ = saveDestroyedMark("echo", destroyedMark{At: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)})
	pending := config.StarterFor("web", "ssh")
	_ = saveUpState("web", upState{Via: viaCloudInit, Stage: stageWaiting, Pending: &pending})
}

// keelbox ls marks every row that needs something done, the box that is
// set up with sign-ins left included, paints the commands in the accent
// colour, and wraps the status under itself, a command never broken.
func TestListMarksAndWraps(t *testing.T) {
	registerEveryState(t)
	colour := ui.Style{Colour: true, Live: true, UTF8: true, Depth: ui.DepthTrue, Width: 100}
	var b bytes.Buffer
	cmdList(colour, &b, &b)
	got := b.String()
	for _, cmd := range []string{"keelbox up beta", "keelbox login zeta", "keelbox resume delta", "keelbox up echo", "keelbox forget echo", "keelbox up web"} {
		if !strings.Contains(got, colour.Cmd(cmd)) {
			t.Errorf("%q is not in the accent colour:\n%q", cmd, got)
		}
	}
	for _, w := range []int{60, 80, 100, 120} {
		st := ui.Style{Live: true, UTF8: true, Width: w}
		b.Reset()
		cmdList(st, &b, &b)
		got := b.String()
		words := strings.Join(strings.Fields(got), " ") // as read, wherever it wraps
		for _, want := range []string{
			"✓ ready",
			"! needs sign-in: keelbox up beta",
			"! ready, sign-ins left (codex, gh): keelbox login zeta",
			"! waiting for the server: keelbox up web",
			"• parked since 2026-10-01: keelbox resume delta",
			"• server destroyed 2026-11-01: keelbox up echo or keelbox forget echo",
		} {
			if !strings.Contains(words, want) {
				t.Errorf("%d columns: ls lacks %q:\n%s", w, want, got)
			}
		}
		// A command is never broken across two lines.
		for _, cmd := range []string{"keelbox up beta", "keelbox login zeta", "keelbox resume delta", "keelbox up echo", "keelbox forget echo", "keelbox up web"} {
			if !strings.Contains(got, cmd) {
				t.Errorf("%d columns: %q is broken across lines:\n%s", w, cmd, got)
			}
		}
		for _, l := range strings.Split(got, "\n") {
			if ui.Width(l) >= w {
				t.Errorf("%d columns: a line is %d wide: %q", w, ui.Width(l), l)
			}
		}
	}
	// Off a terminal: one line a box, no marks, as a script reads it.
	b.Reset()
	cmdList(ui.Style{}, &b, &b)
	if got := b.String(); strings.Count(got, "\n") != 7 || !strings.Contains(got, "zeta   ssh               203.0.113.10  ready, sign-ins left (codex, gh): keelbox login zeta\n") {
		t.Errorf("off a terminal:\n%s", got)
	}
}

// With no boxes, ls names the registry with ~, as every other message.
func TestListWithNoBoxesNamesTheRegistryWithATilde(t *testing.T) {
	isolate(t)
	_, out, _ := run("ls")
	if out != "No boxes in ~/.config/keelbox/boxes. Set one up: keelbox up\n" {
		t.Errorf("got %q", out)
	}
}

// The start screen wraps to the terminal: a box's status and what each
// command is for hang under themselves.
func TestStartScreenWraps(t *testing.T) {
	registerEveryState(t)
	for _, w := range []int{60, 80, 100, 120} {
		var b bytes.Buffer
		startScreen(&b, ui.Style{Live: true, UTF8: true, Width: w})
		got := b.String()
		for _, l := range strings.Split(got, "\n") {
			if ui.Width(l) >= w {
				t.Errorf("%d columns: a line is %d wide: %q", w, ui.Width(l), l)
			}
		}
		for _, want := range []string{"! needs sign-in: keelbox up beta\n", "keelbox up ", "set up a box: a server you reach over"} {
			if !strings.Contains(got, want) {
				t.Errorf("%d columns: the start screen lacks %q:\n%s", w, want, got)
			}
		}
	}
	var b bytes.Buffer
	startScreen(&b, ui.Style{Live: true, UTF8: true, Width: 60})
	if want := "  keelbox up      set up a box: a server you reach over\n                  SSH, this machine, or a new one\n"; !strings.Contains(b.String(), want) {
		t.Errorf("at 60 columns the row does not hang under itself:\n%s", b.String())
	}
}

// keelbox status --all keeps its table whatever a box's reason is: a box
// that gave no status has its state in the REACH column and its reason on
// a line under the table; a box's health is in doctor's words.
func TestStatusAllPutsReasonsUnderTheTable(t *testing.T) {
	now := time.Now()
	rows := []statusAllRow{
		{Box: "devbox", Reach: reachOK, Status: &snapshot.Status{Written: now, Release: "2026.11.3", Doctor: &snapshot.Doctor{Time: now, Warnings: 2}}},
		{Box: "nas", Reach: reachOK, Status: &snapshot.Status{Written: now, Release: "2026.11.3", Doctor: &snapshot.Doctor{Time: now, Failures: 1}}},
		{Box: "lab", Reach: reachUnreachable, Error: "unreachable: no route to the box (is it off, or this machine off its network?); keelbox ssh lab"},
		parkedNamed("delta", time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)),
	}
	for _, w := range []int{100, 120} {
		var b bytes.Buffer
		printStatusAllStyled(&b, ui.Style{Live: true, UTF8: true, Width: w}, rows, now)
		got := b.String()
		for _, want := range []string{"BOX ", "2 warnings (", "1 failure (", "lab     unreachable  -", "delta   parked       -",
			"4 boxes: 2 ok, 1 parked, 1 unreachable.\n", "\nlab: unreachable: no route to the box", "\ndelta: parked since 2026-10-01: keelbox resume delta\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("%d columns: lacks %q:\n%s", w, want, got)
			}
		}
		for _, l := range strings.Split(got, "\n") {
			if ui.Width(l) >= w {
				t.Errorf("%d columns: a line is %d wide: %q", w, ui.Width(l), l)
			}
		}
		if strings.Contains(got, "FAIL") || strings.Contains(got, "warn (") {
			t.Errorf("%d columns: health is not in doctor's words:\n%s", w, got)
		}
	}
	for _, w := range []int{60, 80} { // too narrow for the columns: a block a box
		var b bytes.Buffer
		printStatusAllStyled(&b, ui.Style{Live: true, UTF8: true, Width: w}, rows, now)
		for _, l := range strings.Split(b.String(), "\n") {
			if ui.Width(l) >= w {
				t.Errorf("%d columns: a line is %d wide: %q", w, ui.Width(l), l)
			}
		}
		if !strings.Contains(b.String(), "lab  unreachable\n") || !strings.Contains(b.String(), "  doctor    2 warnings (") {
			t.Errorf("%d columns:\n%s", w, b.String())
		}
	}
}

func parkedNamed(box string, since time.Time) statusAllRow {
	r := parkedRow(box, since)
	r.Box = box
	return r
}

// The question "Which box?" names a box's state in the words keelbox ls
// uses for it.
func TestWhichBoxUsesTheStateWordsOfLs(t *testing.T) {
	registerEveryState(t)
	for box, want := range map[string]string{
		"alpha": "ssh, 203.0.113.7",
		"beta":  "ssh, 203.0.113.8, needs sign-in",
		"zeta":  "ssh, 203.0.113.10",
		"delta": "hetzner, parked",
		"echo":  "hetzner, server destroyed",
	} {
		row := listRow(box)
		if got := pickDetail(row); got != want {
			t.Errorf("%s: %q, want %q", box, got, want)
		}
		if row.state != "" && !strings.HasPrefix(row.status, row.state) {
			t.Errorf("%s: the menu says %q where ls says %q", box, row.state, row.status)
		}
	}
}

// The sessions table names every column.
func TestTablesNameEveryColumn(t *testing.T) {
	got := ui.Style{}.Table([]string{"SESSION", "WINDOWS", "AGE", "STATE"}, [][]string{{"main", "1", "2h 5m", "attached"}, {"review", "2", "<1m", "detached"}})
	want := "SESSION  WINDOWS  AGE    STATE\nmain     1        2h 5m  attached\nreview   2        <1m    detached\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
