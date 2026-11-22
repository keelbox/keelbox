package watch

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

func boot(x *system.Fake, id string) {
	x.AddFile("/proc/sys/kernel/random/boot_id", id+"\n", 0o444)
}

func TestBootMarker(t *testing.T) {
	b := newBox(t, "")
	x := b.Fake
	powerRecs := func() int { return len(records(t, x, logbook.KindPower)) }

	boot(x, "boot-1")
	if msg, err := BootStart(x, t0); err != nil || !strings.Contains(msg, "first start") {
		t.Fatalf("first start: %q %v", msg, err)
	}
	// An apply restarting the unit: stop touches the marker, start in the
	// same boot removes it and finds nothing.
	x.AddFile(CleanMarker, "", 0o644)
	if msg, err := BootStart(x, t0.Add(time.Minute)); err != nil || !strings.Contains(msg, "same boot") || x.Files[CleanMarker] != nil {
		t.Fatalf("restart in the same boot: %q %v", msg, err)
	}
	// A clean reboot.
	x.AddFile(CleanMarker, "", 0o644)
	boot(x, "boot-2")
	if msg, _ := BootStart(x, t0.Add(time.Hour)); !strings.Contains(msg, "clean shutdown") || strings.Contains(msg, "without") || powerRecs() != 0 {
		t.Fatalf("clean reboot: %q, %d power records", msg, powerRecs())
	}
	// The watch ran in boot 2, then the power went: no marker.
	hb := t0.Add(2 * time.Hour)
	if err := writeState(x, State{Heartbeat: hb, BootID: "boot-2"}); err != nil {
		t.Fatal(err)
	}
	boot(x, "boot-3")
	msg, err := BootStart(x, t0.Add(3*time.Hour))
	if err != nil || !strings.Contains(msg, "without a clean shutdown") || !strings.Contains(msg, "between 2026-11-07 11:00 and 11:02 UTC") {
		t.Fatalf("unclean: %q %v", msg, err)
	}
	recs := records(t, x, logbook.KindPower)
	if len(recs) != 1 || recs[0].Release != "2026.12.0" || recs[0].Changes[0].Step != "unclean-shutdown" {
		t.Errorf("logbook: %+v", recs)
	}
	st, _ := ReadPower(x)
	if u := st.Unclean; u == nil || u.Boot != "boot-3" || u.PreviousBoot != "boot-2" || u.LastHeartbeat == nil || !u.LastHeartbeat.Equal(hb) {
		t.Errorf("state: %+v", st.Unclean)
	}
	// The marker directory is root's: only root writes the marker.
	if f := x.Files[PowerDir]; f == nil || f.Owner != "root" || f.Mode.Perm() != 0o755 {
		t.Errorf("%s: %+v", PowerDir, f)
	}
}

func TestPowerBackReportedOnceAfterSettling(t *testing.T) {
	b := newBox(t, "")
	x := b.Fake
	boot(x, "boot-1")
	if _, err := BootStart(x, t0); err != nil {
		t.Fatal(err)
	}
	boot(x, "boot-2")
	if _, err := BootStart(x, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	up := t0.Add(time.Hour)
	x.AddFile("/proc/stat", "btime "+strconv.FormatInt(up.Unix(), 10)+"\n", 0o444)

	run := func(at time.Time) snapshot.Status {
		t.Helper()
		if err := Run(context.Background(), b.e, nil, at); err != nil {
			t.Fatal(err)
		}
		s, _, err := snapshot.Read(x)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	powerBack := func() []events.Event {
		var out []events.Event
		for _, e := range queued(t, x) {
			if e.Kind == "power-back" {
				out = append(out, e)
			}
		}
		return out
	}
	s := run(up.Add(90 * time.Second))
	if len(powerBack()) != 0 {
		t.Error("reported before the box settled")
	}
	if s.Power == nil || !s.Power.Unclean {
		t.Errorf("snapshot power: %+v", s.Power)
	}
	run(up.Add(210 * time.Second))
	run(up.Add(330 * time.Second))
	pb := powerBack()
	if len(pb) != 1 {
		t.Fatalf("%d power-back events, want 1", len(pb))
	}
	if d := pb[0].Fields["detail"]; !strings.Contains(d, "unclean shutdown") || !strings.Contains(d, "tmux: main, work running") || pb[0].Fields["up_since"] == "" {
		t.Errorf("power-back: %+v", pb[0].Fields)
	}

	// A clean reboot after that: no report, and the snapshot says clean.
	x.AddFile(CleanMarker, "", 0o644)
	boot(x, "boot-3")
	if _, err := BootStart(x, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	up = t0.Add(3 * time.Hour)
	x.AddFile("/proc/stat", "btime "+strconv.FormatInt(up.Unix(), 10)+"\n", 0o444)
	if s := run(up.Add(5 * time.Minute)); s.Power == nil || s.Power.Unclean || len(powerBack()) != 1 {
		t.Errorf("after a clean reboot: %+v, %d reports", s.Power, len(powerBack()))
	}
}
