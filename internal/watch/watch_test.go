package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

func env(t *testing.T, extra string) (*engine.Env, *system.Fake) {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdmin admin@laptop\"]\n"+extra), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	x := system.NewFake()
	return &engine.Env{X: x, Cfg: c, M: m}, x
}

func TestRun(t *testing.T) {
	e, x := env(t, "backup:\n  repository: s3:https://s3.example.com/bucket\n  credentials_file: /etc/keelbox/backup/s3.credentials\n  password_file: /etc/keelbox/backup/restic.password\n")
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	x.AddFile("/proc/sys/kernel/random/boot_id", "6f1e\n", 0o444)
	x.AddFile("/proc/stat", "cpu  1 2 3\nbtime 1794038400\n", 0o444)
	x.AddFile("/var/lib/keelbox/backup/status.json", `{"last_attempt":"2026-11-07T03:00:00Z","last_success":"2026-11-07T03:00:00Z"}`, 0o600)
	x.Files["/"] = &system.FakeFile{Dir: true, Mode: 0o755, Owner: "root", Group: "root"}
	x.Files["/var/lib/docker"] = &system.FakeFile{Dir: true, Mode: 0o710, Owner: "root", Group: "root"}
	x.OK("df --output=target,pcent -- / /var/lib/docker", "Mounted on Use%\n/ 41%\n/ 41%\n/var/lib/docker 88%\n")
	if err := logbook.Append(x, logbook.Record{Time: now.Add(-time.Hour), Box: "acme", Keelbox: "v0.1.0", Release: "2026.12.0", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := logbook.Action(x, logbook.Record{Kind: logbook.KindWatch, Result: logbook.ResultFailed}); err != nil {
		t.Fatal(err)
	}
	k, _ := events.Lookup("disk-warn")
	if _, err := events.Emit(x, events.Event{Time: now, Source: events.SourceDisk, Kind: k.Name, Level: k.Level, Box: "acme", Key: "disk:/var/lib/docker"}); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), e, []events.Sender{events.Nop{}}, now); err != nil {
		t.Fatal(err)
	}
	st, found := ReadState(x)
	if !found || !st.Heartbeat.Equal(now) || st.BootID != "6f1e" {
		t.Errorf("state %+v %v", st, found)
	}
	s, found, err := snapshot.Read(x)
	if err != nil || !found {
		t.Fatalf("snapshot: %v %v", found, err)
	}
	if s.Box != "acme" || s.Release != "2026.12.0" || s.LastApply == nil || s.LastApply.Result != "ok" {
		t.Errorf("snapshot release and last apply: %+v %+v", s, s.LastApply)
	}
	if len(s.Disk) != 2 || s.Disk[1].Mount != "/var/lib/docker" || s.Disk[1].Percent != 88 {
		t.Errorf("disk: %+v", s.Disk)
	}
	if s.Backup == nil || s.Backup.LastSuccess == nil || s.Backup.LastFailed {
		t.Errorf("backup: %+v", s.Backup)
	}
	if s.Power == nil || s.Power.LastBoot.Unix() != 1794038400 {
		t.Errorf("power: %+v", s.Power)
	}
	if q, _ := events.Pending(x); len(q) != 0 {
		t.Errorf("queued events not sent: %d", len(q))
	}
	// The snapshot is world-readable: nothing from the credential files or
	// the repository URL may appear in it.
	data := string(x.Files[snapshot.Path].Data)
	for _, secretish := range []string{"/etc/keelbox/backup", "s3:", "://", "credentials", "password"} {
		if strings.Contains(data, secretish) {
			t.Errorf("snapshot contains %q:\n%s", secretish, data)
		}
	}
}

func TestRunWithoutAnythingToRead(t *testing.T) {
	e, x := env(t, "")
	now := time.Now().UTC()
	if err := Run(context.Background(), e, nil, now); err != nil {
		t.Fatal(err)
	}
	s, found, err := snapshot.Read(x)
	if err != nil || !found || s.LastApply != nil || s.Disk != nil || s.Backup != nil {
		t.Errorf("snapshot on a box with nothing to report: %+v %v %v", s, found, err)
	}
}
