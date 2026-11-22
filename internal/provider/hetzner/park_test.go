package hetzner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/hetzner/hetznertest"
)

// parkedProject is a fake project with the box acme: its server 42, of
// type cx23 with a 40 GB disk, and its firewall. Waits are short.
func parkedProject(t *testing.T) (*Client, *hetznertest.API, *Server) {
	t.Helper()
	cl, a := fake(t)
	a.Servers = []map[string]any{hetznertest.Server(42, "acme", "203.0.113.7")}
	a.Firewalls = []map[string]any{hetznertest.Firewall(7, "acme", 42)}
	a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
	oldPoll, oldStop, oldSnap := pollEvery, stopTimeout, snapshotTimeout
	pollEvery, stopTimeout, snapshotTimeout = time.Millisecond, 20*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { pollEvery, stopTimeout, snapshotTimeout = oldPoll, oldStop, oldSnap })
	s, err := Find(context.Background(), cl, "acme")
	if err != nil {
		t.Fatal(err)
	}
	return cl, a, s
}

func called(a *hetznertest.API, call string) int {
	n := 0
	for _, c := range a.Calls() {
		if strings.HasPrefix(c, call) {
			n++
		}
	}
	return n
}

func status(a *hetznertest.API, i int) string { return fmt.Sprint(a.Servers[i]["status"]) }

func TestStopAsksTheSystemFirstThenCutsThePower(t *testing.T) {
	cl, a, s := parkedProject(t)
	ctx := context.Background()
	if err := Stop(ctx, cl, "acme", s, t.Logf); err != nil || status(a, 0) != "off" {
		t.Fatalf("stop: %v, status %s", err, status(a, 0))
	}
	if called(a, "POST /servers/42/actions/shutdown") != 1 || called(a, "POST /servers/42/actions/poweroff") != 0 {
		t.Errorf("calls %v", a.Calls())
	}
	// Already off: nothing is sent.
	if err := Stop(ctx, cl, "acme", s, t.Logf); err != nil || called(a, "POST /servers/42/actions/shutdown") != 1 {
		t.Errorf("stop of a server that is off: %v", err)
	}
	// A system that does not shut down has its power cut, and that is said.
	if _, err := Start(ctx, cl, "acme", s); err != nil || status(a, 0) != "running" {
		t.Fatalf("start: %v", err)
	}
	a.IgnoresShutdown = true
	var said []string
	err := Stop(ctx, cl, "acme", s, func(f string, v ...any) { said = append(said, fmt.Sprintf(f, v...)) })
	if err != nil || status(a, 0) != "off" || called(a, "POST /servers/42/actions/poweroff") != 1 {
		t.Fatalf("stop of a hung server: %v, status %s", err, status(a, 0))
	}
	if len(said) != 1 || !strings.Contains(said[0], "did not shut down within") {
		t.Errorf("said %q", said)
	}
}

func TestResizeKeepsTheDisk(t *testing.T) {
	cl, _, s := parkedProject(t)
	ctx := context.Background()
	// Hetzner refuses a type change on a running server.
	if err := Resize(ctx, cl, "acme", s, "cpx22"); err == nil || !strings.Contains(err.Error(), "server_not_stopped") {
		t.Fatalf("resize of a running server: %v", err)
	}
	if err := Stop(ctx, cl, "acme", s, t.Logf); err != nil {
		t.Fatal(err)
	}
	if err := Resize(ctx, cl, "acme", s, "cpx22"); err != nil {
		t.Fatal(err)
	}
	got, err := Start(ctx, cl, "acme", s)
	if err != nil {
		t.Fatal(err)
	}
	// cpx22 has an 80 GB disk; the server keeps its 40.
	if got.ServerType.Name != "cpx22" || got.DiskGB() != 40 || got.Status != "running" {
		t.Errorf("after resize: type %s, disk %d, %s", got.ServerType.Name, got.DiskGB(), got.Status)
	}
}

func TestResizeOffers(t *testing.T) {
	cl, _, s := parkedProject(t)
	types, err := cl.ServerTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := func(s *Server) string {
		var out []string
		for _, o := range ResizeOffers(types, s, "fsn1") {
			out = append(out, o.Type.Name)
		}
		return strings.Join(out, " ")
	}
	// x86 with a disk of 40 GB or more that can be ordered in fsn1,
	// cheapest first: not cpx02 (20 GB), cpx11 (not sold there), cx33
	// (being retired there) or cax11 (arm).
	if got := names(s); got != "cx23 cpx22 ccx13" {
		t.Errorf("offers for cx23: %s", got)
	}
	// A server whose own type is no longer sold still lists it, as the
	// size it has.
	old := *s
	old.ServerType = ServerType{Name: "cpx11", Disk: 40, Architecture: "x86"}
	if got := names(&old); got != "cpx11 cx23 cpx22 ccx13" && got != "cx23 cpx11 cpx22 ccx13" {
		t.Errorf("offers for cpx11: %s", got)
	}
	// A server made with an 80 GB disk cannot change to a 40 GB type.
	big := *s
	big.ServerType, big.PrimaryDiskSize = ServerType{Name: "cpx22", Disk: 80, Architecture: "x86"}, 80
	if got := names(&big); got != "cpx22 ccx13" {
		t.Errorf("offers for cpx22: %s", got)
	}
}

func TestSnapshotIsReturnedOnlyWhenAvailable(t *testing.T) {
	cl, a, s := parkedProject(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	// Not of a running server.
	if _, err := Snapshot(ctx, cl, cfg, s); err == nil || called(a, "POST /servers/42/actions/create_image") != 0 {
		t.Fatalf("snapshot of a running server: %v", err)
	}
	if err := Stop(ctx, cl, "acme", s, t.Logf); err != nil {
		t.Fatal(err)
	}
	// The action fails: an error, and the unfinished snapshot is deleted.
	a.SnapshotFails = true
	if img, err := Snapshot(ctx, cl, cfg, s); err == nil || img != nil || len(a.Images) != 0 {
		t.Fatalf("failed snapshot: %v %v, %d image(s) left", img, err, len(a.Images))
	}
	// The action succeeds but the image never becomes available.
	a.SnapshotFails, a.SnapshotStatus = false, "creating"
	if img, err := Snapshot(ctx, cl, cfg, s); err == nil || img != nil || len(a.Images) != 0 || !strings.Contains(err.Error(), "still creating") {
		t.Fatalf("stuck snapshot: %v %v, %d image(s) left", img, err, len(a.Images))
	}
	a.SnapshotStatus = ""
	img, err := Snapshot(ctx, cl, cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	if img.Status != "available" || !provider.Parked(img.Labels, "acme") || img.Labels["keelbox-e2e"] != "true" || img.Description != "keelbox: parked box acme" {
		t.Errorf("snapshot %+v", img)
	}
	if len(a.Servers) != 1 {
		t.Error("the snapshot deleted the server")
	}
}

func TestParkedIsTheNewestAvailableLabelledSnapshot(t *testing.T) {
	cl, a, _ := parkedProject(t)
	ctx := context.Background()
	if img, err := Parked(ctx, cl, "acme"); err != nil || img != nil {
		t.Fatalf("no snapshot: %v %v", img, err)
	}
	now := time.Now()
	creating := hetznertest.Snapshot(3, "acme", now)
	creating["status"] = "creating"
	plain := hetznertest.Snapshot(4, "acme", now)
	plain["labels"] = map[string]string{"keelbox": "true", "keelbox-box": "acme"} // a snapshot the user took
	a.Images = []map[string]any{
		hetznertest.Snapshot(1, "acme", now.Add(-time.Hour)),
		hetznertest.Snapshot(2, "acme", now.Add(-time.Minute)),
		creating, plain,
		hetznertest.Snapshot(5, "other", now),
	}
	img, err := Parked(ctx, cl, "acme")
	if err != nil || img == nil || img.ID != 2 {
		t.Fatalf("parked: %+v %v", img, err)
	}
	// Only snapshots with the box's parked labels are ever deleted.
	for _, id := range []int64{4, 5} {
		other, err := cl.Image(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := DeleteSnapshot(ctx, cl, "acme", other); err == nil {
			t.Errorf("deleted image %d, which is not acme's parked snapshot", id)
		}
	}
	if err := DeleteSnapshot(ctx, cl, "acme", &Image{ID: 9, Type: "system", Labels: provider.ParkedLabels("acme", nil)}); err == nil {
		t.Error("deleted a system image")
	}
	done, err := SweepSnapshots(ctx, cl, "acme")
	if err != nil || len(done) != 3 || len(a.Images) != 2 {
		t.Errorf("sweep: %d deleted, %d left, %v", len(done), len(a.Images), err)
	}
	for _, left := range a.Images {
		if id := fmt.Sprint(left["id"]); id != "4" && id != "5" {
			t.Errorf("image %s left, and one of the others deleted", id)
		}
	}
}

func TestRestoreCreatesTheServerFromTheSnapshot(t *testing.T) {
	cl, a, s := parkedProject(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	a.Images = []map[string]any{hetznertest.Snapshot(9, "acme", time.Now())}
	img, _ := Parked(ctx, cl, "acme")
	// Never while the box has a server.
	if _, err := Restore(ctx, cl, cfg, img, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") || len(a.Created) != 0 {
		t.Fatalf("restore beside a server: %v", err)
	}
	if err := Destroy(ctx, cl, s); err != nil {
		t.Fatal(err)
	}
	// Not from an image that is not the box's parked snapshot.
	other := *img
	other.Labels = map[string]string{"keelbox": "true", "keelbox-box": "other", "keelbox-parked": "true"}
	if _, err := Restore(ctx, cl, cfg, &other, t.Logf); err == nil || len(a.Created) != 0 {
		t.Fatalf("restore from another box's snapshot: %v", err)
	}
	// A type whose disk is smaller than the snapshot's, with the ones that fit.
	small := *cfg
	small.Target.Hetzner.ServerType = "cpx02"
	if _, err := Restore(ctx, cl, &small, img, t.Logf); err == nil || !strings.Contains(err.Error(), "set target.hetzner.server_type to one of: ccx13, cpx22, cx23, then run keelbox resume acme again") {
		t.Fatalf("restore on a type too small: %v", err)
	}
	// A project key that is gone: Hetzner would set a root password.
	nokey := *cfg
	nokey.Target.Hetzner.SSHKeys = []string{"gone"}
	if _, err := Restore(ctx, cl, &nokey, img, t.Logf); err == nil || !strings.Contains(err.Error(), `no key named "gone"`) || len(a.Created) != 0 {
		t.Fatalf("restore without the project key: %v", err)
	}

	got, err := Restore(ctx, cl, cfg, img, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || got.PublicNet.IPv4.IP == "" || got.PublicNet.IPv4.IP == "203.0.113.7" || !provider.Managed(got.Labels, "acme") {
		t.Errorf("restored server %+v", got)
	}
	req := a.Created[0]
	labels := req["labels"].(map[string]any)
	if req["name"] != "acme" || req["server_type"] != "cx23" || req["location"] != "fsn1" || req["image"] != float64(9) ||
		labels["keelbox"] != "true" || labels["keelbox-box"] != "acme" || labels["keelbox-e2e"] != "true" || labels["keelbox-parked"] != nil {
		t.Errorf("create request %v", req)
	}
	// The firewall is in the create call, and is the one that was kept.
	if fws := req["firewalls"].([]any); len(fws) != 1 || fws[0].(map[string]any)["firewall"] != float64(7) {
		t.Errorf("firewalls in the create call: %v", req["firewalls"])
	}
	if called(a, "POST /firewalls") != 0 {
		t.Error("a second firewall was created")
	}
	if keys := req["ssh_keys"].([]any); len(keys) != 1 || keys[0] != float64(7) {
		t.Errorf("ssh_keys %v", req["ssh_keys"])
	}
	// User data keeps the host keys and the hostname, creates no account
	// and holds no key.
	ud := fmt.Sprint(req["user_data"])
	if ud != provider.ResumeUserData("acme") || !strings.Contains(ud, "ssh_deletekeys: false\n") || !strings.Contains(ud, "preserve_hostname: true\n") ||
		!strings.Contains(ud, "users: []\n") || strings.Contains(ud, "ssh_keys") || strings.Contains(ud, "PRIVATE") {
		t.Errorf("user data:\n%s", ud)
	}
}

func TestParkCallsRefuseUnlabelledServers(t *testing.T) {
	cl, a, s := parkedProject(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	theirs := *s
	theirs.Labels = map[string]string{"keelbox": "true", "keelbox-box": "other"}
	if err := Stop(ctx, cl, "acme", &theirs, t.Logf); err == nil {
		t.Error("stopped a server of another box")
	}
	if _, err := Start(ctx, cl, "acme", &theirs); err == nil {
		t.Error("started a server of another box")
	}
	if err := Resize(ctx, cl, "acme", &theirs, "cpx22"); err == nil {
		t.Error("resized a server of another box")
	}
	if _, err := Snapshot(ctx, cl, cfg, &theirs); err == nil {
		t.Error("took a snapshot of a server of another box")
	}
	for _, c := range a.Calls() {
		if strings.HasPrefix(c, "POST ") || strings.HasPrefix(c, "DELETE ") {
			t.Errorf("sent %s", c)
		}
	}
}

func TestParkErrorsNeverCarryTheToken(t *testing.T) {
	cl, a, s := parkedProject(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	a.Fail = map[string]bool{"shutdown": true, "poweron": true, "change_type": true, "create_image": true, "delete_image": true, "create_server": true}
	a.Images = []map[string]any{hetznertest.Snapshot(9, "acme", time.Now())}
	img, _ := Parked(ctx, cl, "acme")
	var errs []error
	errs = append(errs, Stop(ctx, cl, "acme", s, t.Logf), Resize(ctx, cl, "acme", s, "cpx22"), DeleteSnapshot(ctx, cl, "acme", img))
	a.Servers[0]["status"] = "off"
	_, err := Start(ctx, cl, "acme", s)
	errs = append(errs, err)
	_, err = Snapshot(ctx, cl, cfg, s)
	errs = append(errs, err)
	a.Servers = nil
	_, err = Restore(ctx, cl, cfg, img, t.Logf)
	errs = append(errs, err)
	for i, err := range errs {
		if err == nil || strings.Contains(err.Error(), token) {
			t.Errorf("call %d: %v", i, err)
		}
	}
}

func TestSnapshotPrice(t *testing.T) {
	cl, _, _ := parkedProject(t)
	if p, err := cl.SnapshotPrice(context.Background()); err != nil || p != 0.0143 {
		t.Errorf("price %v %v", p, err)
	}
}
