package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/ui"
)

func fakeStatusAll(t *testing.T, rows map[string]statusAllRow) {
	t.Helper()
	prev := statusAllFetch
	statusAllFetch = func(_ context.Context, box string) statusAllRow { return rows[box] }
	t.Cleanup(func() { statusAllFetch = prev })
	for b := range rows {
		register(t, config.StarterFor(b, "ssh"))
	}
}

func TestStatusAllTable(t *testing.T) {
	isolate(t)
	now := time.Now().UTC()
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	fakeStatusAll(t, map[string]statusAllRow{
		"acme": {Reach: reachOK, Status: &snapshot.Status{Written: now, Release: "2026.12.0",
			Doctor:   &snapshot.Doctor{Time: now, OK: 40},
			Sessions: &snapshot.Sessions{Tmux: true, Names: []string{"work", "main", "api"}, RemoteControl: "running"},
			Updates:  &snapshot.Updates{}, Disk: []snapshot.Disk{{Mount: "/", Percent: 41}}, Backup: &snapshot.Backup{LastSuccess: ago(6 * time.Hour)}}},
		"nas-home": {Reach: reachOK, Status: &snapshot.Status{Written: now, Release: "2026.12.0",
			Doctor:   &snapshot.Doctor{Time: now, Failures: 1},
			Sessions: &snapshot.Sessions{Tmux: true, Names: []string{"main"}},
			Updates:  &snapshot.Updates{Security: 3, RebootRequired: ago(time.Hour)},
			Disk:     []snapshot.Disk{{Mount: "/", Percent: 40}, {Mount: "/var/lib/docker", Percent: 88}}}},
		"lab":  {Reach: reachUnreachable, Error: "unreachable: ssh timed out (is it off or asleep?); keelbox ssh lab"},
		"old":  noSnapshot("old"),
		"idle": {Reach: reachOK, Status: &snapshot.Status{Written: now.Add(-time.Hour), Release: "2026.12.0"}},
	})
	var out bytes.Buffer
	code := Main([]string{"status", "--all"}, &out, &out)
	got := out.String()
	if code != 1 {
		t.Errorf("exit %d with an unreachable box", code)
	}
	for _, want := range []string{
		"BOX", "REACH", "RELEASE", "DOCTOR", "SESSIONS", "UPDATES", "DISK", "BACKUP",
		"main+2, RC up", "6 h ago", "41%",
		"1 failure (", "3 security, reboot", "88% /var/lib/docker",
		"lab       unreachable  ", "old       no status  ",
		"\nlab: unreachable: ssh timed out (is it off or asleep?); keelbox ssh lab\n",
		"\nold: no status snapshot yet; to write one: keelbox apply old\n",
		"stale (", "5 boxes: 2 ok, 1 stale, 1 unreachable, 1 no status.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
	// One line per box, sorted, after the header.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) < 6 || !strings.HasPrefix(lines[1], "acme") || !strings.HasPrefix(lines[5], "old") {
		t.Errorf("rows:\n%s", got)
	}

	out.Reset()
	if code := Main([]string{"status", "--all", "--json"}, &out, &out); code != 1 {
		t.Errorf("--json exit %d", code)
	}
	var rows []statusAllRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 5 || rows[2].Box != "lab" || rows[2].Reach != reachUnreachable || rows[0].Status.Release != "2026.12.0" {
		t.Errorf("--json: %v\n%s", err, out.String())
	}
}

// A tmux session name with terminal sequences, as the agent can set
// one, reaches the owner's terminal as escaped text.
func TestStatusAllPrintsSessionNamesPlain(t *testing.T) {
	isolate(t)
	now := time.Now().UTC()
	fakeStatusAll(t, map[string]statusAllRow{
		"acme": {Reach: reachOK, Status: &snapshot.Status{Written: now, Release: "2026.12.0",
			Sessions: &snapshot.Sessions{Tmux: true, Names: []string{"\x1b[2J\x1b[H\x1b]0;owned\x07\x1b[32mALL OK"}}}},
	})
	var out bytes.Buffer
	Main([]string{"status", "--all"}, &out, &out)
	if strings.ContainsAny(out.String(), "\x1b\x07") || !strings.Contains(out.String(), `\x1b[2J`) {
		t.Errorf("status --all printed the sequences:\n%q", out.String())
	}
}

func TestStatusAllHealthyExitsZero(t *testing.T) {
	isolate(t)
	now := time.Now().UTC()
	fakeStatusAll(t, map[string]statusAllRow{
		"acme": {Reach: reachOK, Status: &snapshot.Status{Written: now, Doctor: &snapshot.Doctor{Time: now, Warnings: 1}}},
		"beta": {Reach: reachOK, Status: &snapshot.Status{Written: now}},
	})
	var out bytes.Buffer
	if code := Main([]string{"status", "--all"}, &out, &out); code != 0 {
		t.Errorf("exit %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "1 warning (") || strings.Contains(out.String(), "warn (") || !strings.Contains(out.String(), "2 boxes: 2 ok.") {
		t.Errorf("table:\n%s", out.String())
	}
}

func TestStatusAllEmptyAndArguments(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	if code := Main([]string{"status", "--all"}, &out, &out); code != 0 || !strings.Contains(out.String(), "keelbox up") {
		t.Errorf("no boxes: %d\n%s", code, out.String())
	}
	out.Reset()
	if code := Main([]string{"status", "acme", "--all"}, &out, &out); code != 2 || !strings.Contains(out.String(), "keelbox status acme") {
		t.Errorf("a box and --all: %d\n%s", code, out.String())
	}
}

// Boxes are read in parallel, each within its own time limit, so boxes
// that never answer cost one time limit, not one each.
func TestStatusAllBoundedAndParallel(t *testing.T) {
	isolate(t)
	prevT, prevF := statusAllTimeout, statusAllFetch
	t.Cleanup(func() { statusAllTimeout, statusAllFetch = prevT, prevF })
	statusAllTimeout = 300 * time.Millisecond
	statusAllFetch = func(ctx context.Context, box string) statusAllRow {
		<-ctx.Done()
		return statusAllRow{Reach: reachUnreachable, Error: "unreachable: ssh timed out (is it off or asleep?); keelbox ssh " + box}
	}
	for i := range 12 {
		register(t, config.StarterFor("a"+strconv.Itoa(i), "ssh"))
	}
	start := time.Now()
	var out bytes.Buffer
	code := Main([]string{"status", "--all"}, &out, &out)
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("12 silent boxes took %s", took)
	}
	if code != 1 || !strings.Contains(out.String(), "12 boxes: 12 unreachable.") {
		t.Errorf("exit %d:\n%s", code, out.String())
	}
}

// The real fetch against an address where nothing listens: ssh's refusal
// is reported as such, at once.
func TestStatusAllRealSSHRefused(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh client")
	}
	isolate(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	s := config.StarterFor("lab", "ssh")
	s.SSHHost = "127.0.0.1"
	p := register(t, s)
	data, _ := os.ReadFile(p)
	cfg := strings.Replace(string(data), "host: 127.0.0.1", "host: 127.0.0.1\n    port: "+strconv.Itoa(port), 1)
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := fetchStatusAllRow(context.Background(), "lab")
	if r.Reach != reachUnreachable || !strings.Contains(r.Error, "refused") || !strings.Contains(r.Error, "keelbox ssh lab") {
		t.Errorf("row: %+v", r)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("a refused connection took %s", time.Since(start))
	}
}

// A styled terminal too narrow for the columns gets a box a block, not a
// table that wraps.
func TestStatusAllStacksOnANarrowTerminal(t *testing.T) {
	now := time.Now()
	rows := []statusAllRow{
		{Box: "devbox", Reach: reachOK, Status: &snapshot.Status{Written: now, Release: "2026.11.3", Doctor: &snapshot.Doctor{Time: now, Warnings: 2}}},
		{Box: "lab", Reach: reachUnreachable, Error: "unreachable: no route to host; keelbox ssh lab"},
	}
	var narrow, wide bytes.Buffer
	printStatusAllStyled(&narrow, ui.Style{Live: true, UTF8: true, Width: 60}, rows, now)
	printStatusAllStyled(&wide, ui.Style{Live: true, UTF8: true, Width: 160}, rows, now)
	got := narrow.String()
	for _, want := range []string{"devbox  ok\n", "  release   2026.11.3\n", "  doctor    2 warnings (", "lab  unreachable\n", "2 boxes: 1 ok, 1 unreachable.\nlab: unreachable: no route to host; keelbox ssh lab\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("stacked output lacks %q:\n%s", want, got)
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if ui.Width(l) > 59 {
			t.Errorf("line wider than the terminal: %q", l)
		}
	}
	if strings.Contains(got, "BOX") || !strings.Contains(wide.String(), "BOX") {
		t.Errorf("narrow:\n%s\nwide:\n%s", got, wide.String())
	}
}
