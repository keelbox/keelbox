package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/provider/aws/awstest"
)

// parkedAccount is a fake account with the box acme as keelbox creates
// it: instance acme on small_3_0, its firewall and its static IP.
func parkedAccount(t *testing.T) (*awstest.CLI, *Client, *Instance) {
	t.Helper()
	cli, cl := fake(t)
	oldStop, oldSnap := stopTimeout, snapshotTimeout
	stopTimeout, snapshotTimeout = 30*time.Millisecond, time.Second
	t.Cleanup(func() { stopTimeout, snapshotTimeout = oldStop, oldSnap })
	in, err := Create(context.Background(), cl, boxConfig(t, ", bundle: small_3_0, tags: {team: web}"), testHostKey, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	return cli, cl, in
}

func called(cli *awstest.CLI, op string) int {
	n := 0
	for _, c := range cli.Calls() {
		if c == op || strings.HasPrefix(c, op+" ") {
			n++
		}
	}
	return n
}

func formatted(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func TestStopAsksTheSystemFirstThenForces(t *testing.T) {
	cli, cl, in := parkedAccount(t)
	ctx := context.Background()
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil || cli.Instances["acme"].State != "stopped" {
		t.Fatalf("stop: %v, %q", err, cli.Instances["acme"].State)
	}
	// Already stopped: nothing is asked.
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil || called(cli, "lightsail stop-instance") != 1 {
		t.Errorf("second stop: %v, %d calls", err, called(cli, "lightsail stop-instance"))
	}
	started, err := Start(ctx, cl, "acme", in)
	if err != nil || started.State.Name != "running" || started.PublicIPAddress != in.PublicIPAddress {
		t.Fatalf("start: %+v, %v", started, err)
	}
	if _, err := Start(ctx, cl, "acme", in); err != nil || called(cli, "lightsail start-instance") != 1 {
		t.Errorf("start of a running instance: %v, %d calls", err, called(cli, "lightsail start-instance"))
	}
	// An instance that does not shut down is forced, and that is said.
	cli.StuckStopping = true
	var said []string
	if err := Stop(ctx, cl, "acme", in, func(f string, a ...any) { said = append(said, formatted(f, a...)) }); err != nil {
		t.Fatal(err)
	}
	if cli.Instances["acme"].State != "stopped" || !cli.Called("lightsail stop-instance --instance-name acme --force") ||
		len(said) != 1 || !strings.Contains(said[0], "did not shut down within") {
		t.Errorf("forced stop: %q, %v, %v", cli.Instances["acme"].State, said, cli.Calls())
	}
}

// Start attaches the box's static IP again when it is attached to nothing.
func TestStartAttachesAStaticIPLeftLoose(t *testing.T) {
	cli, cl, in := parkedAccount(t)
	ctx := context.Background()
	addr := in.PublicIPAddress
	cli.StaticIPs["keelbox-acme"] = ""
	cli.Instances["acme"].IP = "198.51.100.9"
	got, err := Start(ctx, cl, "acme", in)
	if err != nil || got.PublicIPAddress != addr || cli.StaticIPs["keelbox-acme"] != "acme" {
		t.Errorf("start: %+v, %v, %v", got, err, cli.StaticIPs)
	}
}

func TestResizeOffers(t *testing.T) {
	cli, cl, in := parkedAccount(t)
	bundles, err := cl.AllBundles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := func(in *Instance) string {
		var n []string
		for _, o := range ResizeOffers(bundles, in) {
			n = append(n, o.Bundle.BundleID)
		}
		return strings.Join(n, " ")
	}
	// Nothing smaller, no Windows or IPv6-only bundle, none Lightsail no
	// longer offers; the instance's own is there, cheapest first.
	if got := names(in); got != "small_3_0 medium_3_0 large_3_0" {
		t.Errorf("offers for small_3_0: %s", got)
	}
	// An instance on a bundle that is no longer offered still finds it.
	old := *in
	old.BundleID = "medium_2_0"
	if got := names(&old); got != "medium_2_0 medium_3_0 large_3_0" {
		t.Errorf("offers for medium_2_0: %s", got)
	}
	old.BundleID = "gone_1_0"
	if got := names(&old); got != "" {
		t.Errorf("offers for an unknown bundle: %s", got)
	}
	_ = cli
}

// The snapshot is returned only once Lightsail reports it available, with
// the box's parked tags and the firewall's SSH state; when it fails or the
// wait is cancelled, no snapshot is left and the instance is as it was.
func TestParkSnapshotIsReturnedOnlyWhenAvailable(t *testing.T) {
	cli, cl, in := parkedAccount(t)
	ctx := context.Background()
	c := boxConfig(t, ", bundle: small_3_0, tags: {team: web}")
	if _, err := ParkSnapshot(ctx, cl, c, in); err == nil || !strings.Contains(err.Error(), "only of an instance that is stopped") || len(cli.Snapshots) != 0 {
		t.Fatalf("snapshot of a running instance: %v", err)
	}
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	cli.SnapshotPolls = 3
	snap, err := ParkSnapshot(ctx, cl, c, in)
	if err != nil {
		t.Fatal(err)
	}
	if called(cli, "lightsail get-instance-snapshot") < 4 {
		t.Errorf("returned after %d polls", called(cli, "lightsail get-instance-snapshot"))
	}
	l := snap.Labels()
	if !snap.Parked("acme") || snap.State != "available" || l["team"] != "web" || l[TagPublicSSH] != "open" || l[TagResize] != "" ||
		snap.SizeInGb != 60 || snap.FromBundleID != "small_3_0" || snap.CreatedAt.IsZero() || !strings.HasPrefix(snap.Name, "keelbox-acme-parked-") {
		t.Errorf("snapshot %+v", snap)
	}
	if p, err := Parked(ctx, cl, "acme"); err != nil || p == nil || p.Name != snap.Name {
		t.Errorf("parked: %+v, %v", p, err)
	}
	if err := DeleteSnapshot(ctx, cl, "acme", snap.Name); err != nil || len(cli.Snapshots) != 0 {
		t.Fatalf("delete: %v, %v", err, cli.Snapshots)
	}
	if err := DeleteSnapshot(ctx, cl, "acme", snap.Name); err != nil {
		t.Errorf("delete of a snapshot that is gone: %v", err)
	}

	// Lightsail reports the snapshot failed.
	cli.SnapshotFails = true
	if _, err := ParkSnapshot(ctx, cl, c, in); err == nil || !strings.Contains(err.Error(), "is in state error") {
		t.Errorf("failed snapshot: %v", err)
	}
	cli.SnapshotFails = false
	// The create call fails.
	cli.Fail["lightsail create-instance-snapshot"] = "no capacity"
	if _, err := ParkSnapshot(ctx, cl, c, in); err == nil || !strings.Contains(err.Error(), "no capacity") {
		t.Errorf("refused snapshot: %v", err)
	}
	delete(cli.Fail, "lightsail create-instance-snapshot")
	// It never completes.
	cli.SnapshotPolls = 1 << 30
	snapshotTimeout = 20 * time.Millisecond
	if _, err := ParkSnapshot(ctx, cl, c, in); err == nil || !strings.Contains(err.Error(), "is not available after") {
		t.Errorf("slow snapshot: %v", err)
	}
	// The wait is cancelled: the snapshot is deleted all the same.
	snapshotTimeout = time.Minute
	cancelled, cancel := context.WithCancel(ctx)
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := ParkSnapshot(cancelled, cl, c, in); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled snapshot: %v", err)
	}
	if len(cli.Snapshots) != 0 {
		t.Errorf("snapshots left after failures: %v", cli.Snapshots)
	}
	if got := cli.Instances["acme"]; got == nil || got.State != "stopped" || cli.StaticIPs["keelbox-acme"] != "acme" || cli.Called("lightsail delete-instance") {
		t.Errorf("the instance was touched: %+v", got)
	}
}

func TestParkedIsTheNewestAvailableTaggedSnapshot(t *testing.T) {
	cli, cl, _ := parkedAccount(t)
	ctx := context.Background()
	parked := map[string]string{"keelbox": "true", "keelbox-box": "acme", "keelbox-parked": "true"}
	tags := func(extra ...string) map[string]string {
		l := map[string]string{}
		for k, v := range parked {
			l[k] = v
		}
		for i := 0; i+1 < len(extra); i += 2 {
			if extra[i+1] == "" {
				delete(l, extra[i])
			} else {
				l[extra[i]] = extra[i+1]
			}
		}
		return l
	}
	if p, err := Parked(ctx, cl, "acme"); p != nil || err != nil {
		t.Fatalf("parked with no snapshot: %+v, %v", p, err)
	}
	cli.Snapshots["older"] = &awstest.Snapshot{Name: "older", Tags: tags(), Created: 100}
	cli.Snapshots["newer"] = &awstest.Snapshot{Name: "newer", Tags: tags(), Created: 300}
	cli.Snapshots["failed"] = &awstest.Snapshot{Name: "failed", Tags: tags(), Created: 400, State: "error"}
	cli.Snapshots["unmarked"] = &awstest.Snapshot{Name: "unmarked", Tags: tags("keelbox-parked", ""), Created: 500}
	cli.Snapshots["resize"] = &awstest.Snapshot{Name: "resize", Tags: tags("keelbox-parked", "", "keelbox-resize", "true"), Created: 550}
	cli.Snapshots["other-box"] = &awstest.Snapshot{Name: "other-box", Tags: tags("keelbox-box", "other"), Created: 600}
	cli.Snapshots["by-hand"] = &awstest.Snapshot{Name: "by-hand", Tags: map[string]string{"keelbox-parked": "true"}, Created: 700}
	p, err := Parked(ctx, cl, "acme")
	if err != nil || p == nil || p.Name != "newer" || !p.CreatedAt.Equal(time.Unix(300, 123000000)) {
		t.Fatalf("parked: %+v, %v", p, err)
	}
	// Only the box's own parked snapshots are deleted, one by one.
	for _, name := range []string{"unmarked", "resize", "other-box", "by-hand"} {
		if err := DeleteSnapshot(ctx, cl, "acme", name); err == nil || !strings.Contains(err.Error(), "refusing to delete snapshot "+name) {
			t.Errorf("delete %s: %v", name, err)
		}
	}
	// The sweep takes what keelbox made for the box, whatever its state:
	// parked snapshots and the snapshot of a resize.
	done, err := SweepSnapshots(ctx, cl, "acme")
	if err != nil || strings.Join(done, " ") != "failed newer older resize" {
		t.Errorf("sweep: %v, %v", done, err)
	}
	if len(cli.Snapshots) != 3 || cli.Snapshots["unmarked"] == nil || cli.Snapshots["other-box"] == nil || cli.Snapshots["by-hand"] == nil {
		t.Errorf("left after the sweep: %v", cli.Snapshots)
	}
}

// park parks the fake box by hand: stopped, snapshot taken, instance and
// static IP gone.
func park(t *testing.T, cli *awstest.CLI, cl *Client, in *Instance, extra string) *Snapshot {
	t.Helper()
	ctx := context.Background()
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	snap, err := ParkSnapshot(ctx, cl, boxConfig(t, ", bundle: small_3_0"+extra), in)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := cl.Instance(ctx, "acme")
	if _, err := Destroy(ctx, cl, cur, "acme"); err != nil {
		t.Fatal(err)
	}
	if len(cli.Instances) != 0 || len(cli.StaticIPs) != 0 || len(cli.Snapshots) != 1 {
		t.Fatalf("after park: %v %v %v", cli.Instances, cli.StaticIPs, cli.Snapshots)
	}
	return snap
}

func TestRestoreCreatesTheInstanceFromTheSnapshot(t *testing.T) {
	cli, cl, in := parkedAccount(t)
	ctx := context.Background()
	// The box had closed public SSH: the firewall of the new instance
	// must not admit it either.
	closed := false
	if _, _, err := EnsurePorts(ctx, cl, "acme", &closed); err != nil {
		t.Fatal(err)
	}
	snap := park(t, cli, cl, in, "")
	if snap.Labels()[TagPublicSSH] != "closed" {
		t.Fatalf("snapshot tags %v", snap.Labels())
	}
	cli.PendingPolls, cli.PortsNeedRunning = 3, true
	c := boxConfig(t, ", bundle: medium_3_0, availability_zone: eu-central-1b, tags: {team: web}")
	var said []string
	got, err := Restore(ctx, cl, c, snap, func(f string, a ...any) { said = append(said, formatted(f, a...)) })
	if err != nil {
		t.Fatal(err)
	}
	made := cli.Instances["acme"]
	if made == nil || made.Bundle != "medium_3_0" || made.Zone != "eu-central-1b" || made.FromSnapshot != snap.Name || made.Blueprint != "ubuntu_24_04" {
		t.Fatalf("instance %+v", made)
	}
	if made.Tags["keelbox"] != "true" || made.Tags["keelbox-box"] != "acme" || made.Tags["team"] != "web" || len(made.Tags) != 3 {
		t.Errorf("tags %v", made.Tags)
	}
	// The user data is a script that does nothing: no key, no account.
	if made.UserData != LaunchScript("acme") || !strings.HasPrefix(made.UserData, "#!/bin/sh\n# keelbox box acme") || strings.Count(made.UserData, "\n") != 2 {
		t.Errorf("user data:\n%s", made.UserData)
	}
	if strings.Contains(strings.Join(cli.Calls(), "\n"), "--key-pair-name") {
		t.Error("restore named a key pair")
	}
	if ports, err := cl.Ports(ctx, "acme"); err != nil || !SamePorts(ports, PortRules(false)) {
		t.Errorf("ports %+v, %v", ports, err)
	}
	if cli.StaticIPs["keelbox-acme"] != "acme" || got.PublicIPAddress != cli.StaticAddr["keelbox-acme"] || got.State.Name != "running" {
		t.Errorf("static IP %v, instance %+v", cli.StaticIPs, got)
	}
	if out := strings.Join(said, "\n"); !strings.Contains(out, "creating acme from snapshot "+snap.Name+": medium_3_0 in eu-central-1b") ||
		!strings.Contains(out, "set the firewall of instance acme: in udp 41641") || !strings.Contains(out, "attached static IP keelbox-acme") {
		t.Errorf("said:\n%s", out)
	}
	// The snapshot is kept: resume deletes it after its apply.
	if len(cli.Snapshots) != 1 {
		t.Errorf("snapshots %v", cli.Snapshots)
	}
	// A second restore fails and changes nothing.
	before := len(cli.Calls())
	if _, err := Restore(ctx, cl, c, snap, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second restore: %v", err)
	}
	for _, call := range cli.Calls()[before:] {
		if !strings.HasPrefix(call, "lightsail get-") {
			t.Errorf("second restore ran %s", call)
		}
	}
}

func TestRestoreNamesTheFixAndLeavesNothingBehind(t *testing.T) {
	cli, cl, in := parkedAccount(t)
	ctx := context.Background()
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	// Parked on medium_3_0 (80 GB): small_3_0 no longer fits.
	cli.Instances["acme"].Bundle, cli.Instances["acme"].DiskGB = "medium_3_0", 80
	snap := park(t, cli, cl, in, "")
	_, err := Restore(ctx, cl, boxConfig(t, ", bundle: small_3_0"), snap, t.Logf)
	if err == nil || !strings.Contains(err.Error(), "set target.aws.bundle to one of: medium_3_0, large_3_0, then run keelbox resume acme again") ||
		!strings.Contains(err.Error(), "bundle small_3_0") {
		t.Errorf("smaller bundle: %v", err)
	}
	if _, err := Restore(ctx, cl, boxConfig(t, ", bundle: huge_9_0"), snap, t.Logf); err == nil || !strings.Contains(err.Error(), `target.aws.bundle "huge_9_0" is not a Linux bundle`) {
		t.Errorf("unknown bundle: %v", err)
	}
	if cli.Called("lightsail create-instances-from-snapshot") || cli.Called("lightsail allocate-static-ip --static-ip-name keelbox-acme") && len(cli.StaticIPs) != 0 {
		t.Errorf("changed something: %v", cli.Calls())
	}
	c := boxConfig(t, ", bundle: medium_3_0")
	// Lightsail refuses the instance: the static IP allocated for it is
	// released again.
	cli.Fail["lightsail create-instances-from-snapshot"] = "no capacity"
	if _, err := Restore(ctx, cl, c, snap, t.Logf); err == nil || !strings.Contains(err.Error(), "no capacity") || len(cli.StaticIPs) != 0 || len(cli.Instances) != 0 {
		t.Errorf("refused instance: %v, %v, %v", err, cli.StaticIPs, cli.Instances)
	}
	delete(cli.Fail, "lightsail create-instances-from-snapshot")
	// The address cannot be attached: the instance made is deleted, and
	// the static IP released.
	cli.Fail["lightsail attach-static-ip"] = "try later"
	if _, err := Restore(ctx, cl, c, snap, t.Logf); err == nil || !strings.Contains(err.Error(), "try later") || len(cli.StaticIPs) != 0 || len(cli.Instances) != 0 {
		t.Errorf("address not attached: %v, %v, %v", err, cli.StaticIPs, cli.Instances)
	}
	delete(cli.Fail, "lightsail attach-static-ip")
	// A static IP the box kept is used, and stays when the restore fails.
	cli.StaticIPs["keelbox-acme"], cli.StaticAddr["keelbox-acme"] = "", "203.0.113.200"
	cli.Fail["lightsail create-instances-from-snapshot"] = "no capacity"
	if _, err := Restore(ctx, cl, c, snap, t.Logf); err == nil || len(cli.StaticIPs) != 1 {
		t.Errorf("kept address: %v, %v", err, cli.StaticIPs)
	}
	delete(cli.Fail, "lightsail create-instances-from-snapshot")
	got, err := Restore(ctx, cl, c, snap, t.Logf)
	if err != nil || got.PublicIPAddress != "203.0.113.200" || called(cli, "lightsail allocate-static-ip") != 3 {
		t.Errorf("restore at the kept address: %+v, %v", got, err)
	}
	// One attached to something else is not taken.
	delete(cli.Instances, "acme")
	cli.Instances["other"] = &awstest.Instance{Name: "other", Tags: map[string]string{}}
	cli.StaticIPs["keelbox-acme"] = "other"
	if _, err := Restore(ctx, cl, c, snap, t.Logf); err == nil || !strings.Contains(err.Error(), "is attached to other, which is not this box") || cli.StaticIPs["keelbox-acme"] != "other" {
		t.Errorf("address of another instance: %v", err)
	}
	if len(cli.Snapshots) != 1 {
		t.Errorf("the parked snapshot was touched: %v", cli.Snapshots)
	}
}

// A resize replaces the instance with one of the same name on the bigger
// bundle, from a snapshot Lightsail confirmed before the old instance was
// deleted; the address and the firewall rules stay, and the snapshot goes
// once the new instance has the address.
func TestResizeReplacesTheInstance(t *testing.T) {
	cli, cl, in := parkedAccount(t)
	ctx := context.Background()
	c := boxConfig(t, ", bundle: small_3_0, tags: {team: web}")
	closed := false
	if _, _, err := EnsurePorts(ctx, cl, "acme", &closed); err != nil {
		t.Fatal(err)
	}
	addr := in.PublicIPAddress
	if _, err := Resize(ctx, cl, c, in, "medium_3_0", t.Logf); err == nil || !strings.Contains(err.Error(), "only of an instance that is stopped") {
		t.Fatalf("resize of a running instance: %v", err)
	}
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	for _, bundle := range []string{"small_3_0", "nano_3_0", "medium_win_3_0", "nope"} {
		if _, err := Resize(ctx, cl, c, in, bundle, t.Logf); err == nil || !strings.Contains(err.Error(), "cannot change to bundle "+bundle) {
			t.Errorf("resize to %s: %v", bundle, err)
		}
	}
	if len(cli.Snapshots) != 0 || cli.Called("lightsail delete-instance") {
		t.Fatalf("a refused resize changed something: %v", cli.Calls())
	}
	cli.SnapshotPolls, cli.PendingPolls = 2, 2
	before := len(cli.Calls())
	got, err := Resize(ctx, cl, c, in, "medium_3_0", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	made := cli.Instances["acme"]
	if len(cli.Instances) != 1 || made.Bundle != "medium_3_0" || made.DiskGB != 80 || made.Zone != "eu-central-1a" || made.FromSnapshot == "" ||
		made.UserData != LaunchScript("acme") || made.Tags["team"] != "web" || made.Tags["keelbox-box"] != "acme" || len(made.Tags) != 3 {
		t.Fatalf("instance %+v", made)
	}
	if got.Name != "acme" || got.BundleID != "medium_3_0" || got.PublicIPAddress != addr || got.State.Name != "running" ||
		cli.StaticIPs["keelbox-acme"] != "acme" || cli.StaticAddr["keelbox-acme"] != addr {
		t.Errorf("returned %+v, static IP %v", got, cli.StaticIPs)
	}
	if ports, err := cl.Ports(ctx, "acme"); err != nil || !SamePorts(ports, PortRules(false)) {
		t.Errorf("ports %+v, %v", ports, err)
	}
	if len(cli.Snapshots) != 0 {
		t.Errorf("snapshots left: %v", cli.Snapshots)
	}
	// The order: the snapshot is available and tagged as parked before the
	// instance is deleted; the snapshot is deleted last; the static IP is
	// never released.
	var order []string
	for _, call := range cli.Calls()[before:] {
		op := strings.Fields(call)[1]
		if !strings.HasPrefix(op, "get-") && op != "put-instance-public-ports" {
			order = append(order, op)
		}
	}
	if got := strings.Join(order, " "); got != "create-instance-snapshot tag-resource delete-instance create-instances-from-snapshot attach-static-ip delete-instance-snapshot" {
		t.Errorf("order: %s", got)
	}
}

// The snapshot of a resize that cannot be deleted is not an error: the
// box is resized, and the line says what is still billed and how to
// delete it.
func TestResizeSaysWhenItsSnapshotStays(t *testing.T) {
	cli, cl, in, _ := resizeSetup(t)
	cli.Fail["lightsail delete-instance-snapshot"] = "not now"
	var said []string
	got, err := Resize(context.Background(), cl, boxConfig(t, ", bundle: small_3_0"), in, "medium_3_0", func(f string, a ...any) { said = append(said, formatted(f, a...)) })
	if err != nil || got.BundleID != "medium_3_0" || len(cli.Snapshots) != 1 {
		t.Fatalf("resize: %+v, %v, %v", got, err, cli.Snapshots)
	}
	last := said[len(said)-1]
	if !strings.Contains(last, "could not be deleted") || !strings.Contains(last, "it is billed until it is: aws lightsail delete-instance-snapshot --instance-snapshot-name keelbox-acme-resize-") ||
		!strings.Contains(last, "--region eu-central-1") {
		t.Errorf("said: %s", last)
	}
}

func resizeSetup(t *testing.T) (*awstest.CLI, *Client, *Instance, string) {
	t.Helper()
	cli, cl, in := parkedAccount(t)
	if err := Stop(context.Background(), cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	return cli, cl, in, in.PublicIPAddress
}

// Before the old instance is deleted, a failure leaves it as it was and
// no snapshot.
func TestResizeFailureBeforeTheDeletionLeavesTheInstance(t *testing.T) {
	c := boxConfig(t, ", bundle: small_3_0")
	ctx := context.Background()
	for _, op := range []string{"create-instance-snapshot", "get-instance-snapshot", "tag-resource", "delete-instance"} {
		cli, cl, in, addr := resizeSetup(t)
		cli.Fail["lightsail "+op] = "not now"
		_, err := Resize(ctx, cl, c, in, "medium_3_0", t.Logf)
		var left *LeftParked
		if err == nil || !strings.Contains(err.Error(), "not now") || errors.As(err, &left) {
			t.Errorf("%s fails: %v", op, err)
		}
		got := cli.Instances["acme"]
		if got == nil || got.Bundle != "small_3_0" || got.State != "stopped" || got.IP != addr || cli.StaticIPs["keelbox-acme"] != "acme" || len(cli.Snapshots) != 0 {
			t.Errorf("%s fails: instance %+v, static IP %v, snapshots %v", op, got, cli.StaticIPs, cli.Snapshots)
		}
		if cli.Called("lightsail create-instances-from-snapshot") || cli.Called("lightsail release-static-ip") {
			t.Errorf("%s fails: %v", op, cli.Calls())
		}
	}
	// Lightsail reports the snapshot failed.
	cli, cl, in, _ := resizeSetup(t)
	cli.SnapshotFails = true
	if _, err := Resize(ctx, cl, c, in, "medium_3_0", t.Logf); err == nil || cli.Called("lightsail delete-instance") || len(cli.Snapshots) != 0 {
		t.Errorf("failed snapshot: %v, %v", err, cli.Calls())
	}
}

// The new bundle cannot be had: the instance is created again on the old
// one, at the same address, and the error says so.
func TestResizeFallsBackToTheOldBundle(t *testing.T) {
	cli, cl, in, addr := resizeSetup(t)
	ctx := context.Background()
	cli.FailWith["lightsail create-instances-from-snapshot --bundle-id medium_3_0"] = "no capacity"
	var said []string
	got, err := Resize(ctx, cl, boxConfig(t, ", bundle: small_3_0"), in, "medium_3_0", func(f string, a ...any) { said = append(said, formatted(f, a...)) })
	var left *LeftParked
	if got != nil || err == nil || errors.As(err, &left) || !strings.Contains(err.Error(), "no capacity") || !strings.Contains(err.Error(), "instance acme was created again as small_3_0") {
		t.Fatalf("resize: %+v, %v", got, err)
	}
	made := cli.Instances["acme"]
	if made == nil || made.Bundle != "small_3_0" || made.FromSnapshot == "" || made.IP != addr || cli.StaticIPs["keelbox-acme"] != "acme" || len(cli.Snapshots) != 0 {
		t.Errorf("instance %+v, static IP %v, snapshots %v", made, cli.StaticIPs, cli.Snapshots)
	}
	if ports, err := cl.Ports(ctx, "acme"); err != nil || !SamePorts(ports, PortRules(true)) {
		t.Errorf("ports %+v, %v", ports, err)
	}
	if out := strings.Join(said, "\n"); !strings.Contains(out, "could not be created as medium_3_0") || !strings.Contains(out, "creating it as small_3_0 again") {
		t.Errorf("said:\n%s", out)
	}
	// The shared code starts the server it had: the one that is there now.
	if s, err := Start(ctx, cl, "acme", in); err != nil || s.PublicIPAddress != addr {
		t.Errorf("start after the fallback: %+v, %v", s, err)
	}

	// The instance is created on the new bundle but its address cannot be
	// attached: it is deleted, and the old size comes back the same way.
	cli, cl, in, addr = resizeSetup(t)
	fails := 1
	cli.Fail["lightsail attach-static-ip"] = "try later"
	_, err = Resize(ctx, cl, boxConfig(t, ", bundle: small_3_0"), in, "medium_3_0", func(f string, a ...any) {
		// The second create is announced: let its attach work.
		if strings.Contains(formatted(f, a...), "creating it as small_3_0 again") && fails > 0 {
			fails--
			delete(cli.Fail, "lightsail attach-static-ip")
		}
	})
	if err == nil || errors.As(err, &left) || cli.Instances["acme"] == nil || cli.Instances["acme"].Bundle != "small_3_0" || cli.Instances["acme"].IP != addr || len(cli.Snapshots) != 0 {
		t.Errorf("address not attached: %v, %+v", err, cli.Instances["acme"])
	}
}

// Neither bundle can be had: the box is left parked, with its static IP,
// and Restore brings it back at the same address.
func TestResizeLeavesTheBoxParkedWhenNoInstanceCanBeCreated(t *testing.T) {
	cli, cl, in, addr := resizeSetup(t)
	ctx := context.Background()
	c := boxConfig(t, ", bundle: small_3_0")
	cli.Fail["lightsail create-instances-from-snapshot"] = "no capacity"
	_, err := Resize(ctx, cl, c, in, "medium_3_0", t.Logf)
	var left *LeftParked
	if !errors.As(err, &left) || !strings.Contains(err.Error(), "no capacity") || !strings.Contains(err.Error(), "creating it as small_3_0 again") {
		t.Fatalf("resize: %v", err)
	}
	if len(cli.Instances) != 0 || len(cli.Snapshots) != 1 || cli.StaticAddr["keelbox-acme"] != addr || cli.StaticIPs["keelbox-acme"] != "" || cli.Called("lightsail release-static-ip") {
		t.Fatalf("left: %v %v %v", cli.Instances, cli.Snapshots, cli.StaticIPs)
	}
	p, err := Parked(ctx, cl, "acme")
	if err != nil || p == nil || p.Name != left.Snapshot.Name || !left.Snapshot.Parked("acme") || p.Labels()[TagPublicSSH] != "open" {
		t.Fatalf("parked: %+v, %v", p, err)
	}
	delete(cli.Fail, "lightsail create-instances-from-snapshot")
	got, err := Restore(ctx, cl, c, p, t.Logf)
	if err != nil || got.PublicIPAddress != addr || got.BundleID != "small_3_0" || called(cli, "lightsail allocate-static-ip") != 1 {
		t.Errorf("restore: %+v, %v", got, err)
	}

	// A deletion whose outcome is not known keeps the snapshot too.
	cli, cl, in, _ = resizeSetup(t)
	cli.Regions = []string{"eu-central-1"}
	cut := &cutAfter{CLI: cli, op: "delete-instance"}
	cl.Run = cut
	_, err = Resize(ctx, cl, c, in, "medium_3_0", t.Logf)
	if !errors.As(err, &left) || len(cli.Snapshots) != 1 || !strings.Contains(err.Error(), "deleting instance acme") {
		t.Errorf("unknown deletion: %v, %v", err, cli.Snapshots)
	}
}

// cutAfter is a connection that is lost once an operation was sent: the
// operation happens, and its answer and every later call fail.
type cutAfter struct {
	*awstest.CLI
	op   string
	lost bool
}

func (c *cutAfter) Run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	if c.lost {
		return nil, []byte("\naws: [ERROR]: Could not connect to the endpoint URL\n"), 255, nil
	}
	out, errOut, exit, err := c.CLI.Run(ctx, args)
	if len(args) > 1 && args[1] == c.op {
		c.lost = true
		return nil, []byte("\naws: [ERROR]: Read timeout on endpoint URL\n"), 255, nil
	}
	return out, errOut, exit, err
}

// Nothing here changes an instance or a snapshot without keelbox's tags
// for the box.
func TestParkCallsRefuseUntaggedResources(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	c := boxConfig(t, "")
	cli.Instances["acme"] = &awstest.Instance{Name: "acme", IP: "203.0.113.50", Bundle: "small_3_0", DiskGB: 60, State: "stopped", Tags: map[string]string{"keelbox": "true", "keelbox-box": "other"}}
	cli.Snapshots["by-hand"] = &awstest.Snapshot{Name: "by-hand", From: "acme", SizeGB: 60, Tags: map[string]string{"keelbox-parked": "true"}}
	in, _ := cl.Instance(ctx, "acme")
	if err := Stop(ctx, cl, "acme", in, t.Logf); err == nil || !strings.Contains(err.Error(), "refusing to change instance acme") {
		t.Errorf("stop: %v", err)
	}
	if _, err := Start(ctx, cl, "acme", in); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("start: %v", err)
	}
	if _, err := ParkSnapshot(ctx, cl, c, in); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("snapshot: %v", err)
	}
	if _, err := Resize(ctx, cl, c, in, "medium_3_0", t.Logf); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("resize: %v", err)
	}
	snap, _ := cl.Snapshot(ctx, "by-hand")
	if _, err := Restore(ctx, cl, c, snap, t.Logf); err == nil || !strings.Contains(err.Error(), "refusing to create acme from snapshot by-hand") {
		t.Errorf("restore: %v", err)
	}
	if err := DeleteSnapshot(ctx, cl, "acme", "by-hand"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("delete snapshot: %v", err)
	}
	if done, err := SweepSnapshots(ctx, cl, "acme"); err != nil || len(done) != 0 {
		t.Errorf("sweep: %v, %v", done, err)
	}
	for _, call := range cli.Calls() {
		if op := strings.Fields(call)[1]; !strings.HasPrefix(op, "get-") {
			t.Errorf("ran %s", call)
		}
	}
}

// A timestamp is read in each form the AWS CLI prints.
func TestTimeFormats(t *testing.T) {
	want := time.Date(2026, 11, 14, 10, 15, 0, 123000000, time.UTC)
	for _, in := range []string{`"2026-11-14T10:15:00.123000+00:00"`, `"2026-11-14T11:15:00.123+01:00"`, `"2026-11-14T10:15:00.123Z"`, `1794651300.123`, `"1794651300.123"`} {
		var got Time
		if err := json.Unmarshal([]byte(in), &got); err != nil || got.Sub(want).Abs() > time.Millisecond {
			t.Errorf("%s: %v, %v", in, got.Time, err)
		}
	}
	var got Time
	if err := json.Unmarshal([]byte(`"last Tuesday"`), &got); err == nil {
		t.Error("a timestamp in no known form was accepted")
	}
	if err := json.Unmarshal([]byte(`null`), &got); err != nil || !got.IsZero() {
		t.Errorf("null: %v, %v", got.Time, err)
	}
}
