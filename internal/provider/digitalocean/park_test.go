package digitalocean

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider/digitalocean/dotest"
)

// parkedAccount is a fake account with the box acme: its Droplet, of size
// s-2vcpu-4gb with an 80 GB disk, behind its firewall. Waits are short.
func parkedAccount(t *testing.T) (*Client, *dotest.CLI, *Droplet) {
	t.Helper()
	cli, cl := fake(t)
	cli.AddDroplet("acme", "keelbox", "keelbox-box-acme")
	if _, _, err := EnsureFirewall(context.Background(), cl, "acme", nil); err != nil {
		t.Fatal(err)
	}
	oldStop, oldAction, oldSnap, oldSettle, oldFW := stopTimeout, actionTimeout, snapshotTimeout, settleTimeout, firewallTimeout
	stopTimeout, actionTimeout, snapshotTimeout, settleTimeout, firewallTimeout = 20*time.Millisecond, time.Second, time.Second, 50*time.Millisecond, time.Second
	t.Cleanup(func() {
		stopTimeout, actionTimeout, snapshotTimeout, settleTimeout, firewallTimeout = oldStop, oldAction, oldSnap, oldSettle, oldFW
	})
	d, err := Find(context.Background(), cl, "acme")
	if err != nil {
		t.Fatal(err)
	}
	return cl, cli, d
}

func called(cli *dotest.CLI, call string) int {
	n := 0
	for _, c := range cli.Calls() {
		if strings.HasPrefix(c, call) {
			n++
		}
	}
	return n
}

func TestStopAsksTheSystemFirstThenCutsThePower(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	made := cli.Droplets[d.ID]
	if err := Stop(ctx, cl, "acme", d, t.Logf); err != nil || made.Status != "off" {
		t.Fatalf("stop: %v, status %q", err, made.Status)
	}
	if called(cli, "compute droplet-action shutdown") != 1 || called(cli, "compute droplet-action power-off") != 0 {
		t.Errorf("calls %v", cli.Calls())
	}
	// Already off: nothing is sent.
	if err := Stop(ctx, cl, "acme", d, t.Logf); err != nil || called(cli, "compute droplet-action shutdown") != 1 {
		t.Errorf("stop of a Droplet that is off: %v", err)
	}
	// It starts again at the address it had; one that is active is left alone.
	got, err := Start(ctx, cl, "acme", d)
	if err != nil || made.Status != "" || got.Status != "active" || got.PublicIPv4() != made.IP {
		t.Fatalf("start: %+v %v", got, err)
	}
	if _, err := Start(ctx, cl, "acme", d); err != nil || called(cli, "compute droplet-action power-on") != 1 {
		t.Errorf("start of an active Droplet: %v, calls %v", err, cli.Calls())
	}
	// A system that does not shut down has its power cut, and that is said.
	cli.Stubborn = true
	var said []string
	err = Stop(ctx, cl, "acme", d, func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) })
	if err != nil || made.Status != "off" || called(cli, "compute droplet-action power-off") != 1 {
		t.Fatalf("stop of a stubborn Droplet: %v, status %q", err, made.Status)
	}
	if len(said) != 1 || !strings.Contains(said[0], "did not shut down within") || !strings.Contains(said[0], "switching it off") {
		t.Errorf("said %q", said)
	}
	// Cancelled while waiting: the power is not cut.
	if _, err := Start(ctx, cl, "acme", d); err != nil {
		t.Fatal(err)
	}
	stopTimeout = time.Minute
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := Stop(short, cl, "acme", d, t.Logf); err == nil || called(cli, "compute droplet-action power-off") != 1 {
		t.Errorf("stop with a cancelled context: %v", err)
	}
}

func TestResizeKeepsTheDisk(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	made := cli.Droplets[d.ID]
	// A Droplet that runs is not resized.
	if _, err := Resize(ctx, cl, "acme", d, "s-4vcpu-8gb"); err == nil || !strings.Contains(err.Error(), "only while it is off") || called(cli, "compute droplet-action resize") != 0 {
		t.Fatalf("resize of a running Droplet: %v", err)
	}
	if err := Stop(ctx, cl, "acme", d, t.Logf); err != nil {
		t.Fatal(err)
	}
	cli.ActionPolls = 2
	got, err := Resize(ctx, cl, "acme", d, "s-4vcpu-8gb")
	if err != nil {
		t.Fatal(err)
	}
	if got.SizeSlug != "s-4vcpu-8gb" || got.Disk != 80 || got.Status != "off" || made.Size != "s-4vcpu-8gb" || made.Disk != 80 {
		t.Errorf("resized %+v, in the account %+v", got, made)
	}
	// The disk is never grown: the flag that does it is not passed.
	for _, c := range cli.Calls() {
		if strings.Contains(c, "resize-disk") {
			t.Errorf("the disk was resized: %q", c)
		}
	}
	// And so the way back down is open.
	if got, err = Resize(ctx, cl, "acme", got, "s-2vcpu-4gb"); err != nil || got.SizeSlug != "s-2vcpu-4gb" {
		t.Fatalf("back down: %+v %v", got, err)
	}
	// A size DigitalOcean refuses, at once or later, leaves the old one.
	if _, err := Resize(ctx, cl, "acme", d, "s-1vcpu-1gb"); err == nil || !strings.Contains(err.Error(), "smaller disk") || made.Size != "s-2vcpu-4gb" {
		t.Errorf("a size with a smaller disk: %v, size %s", err, made.Size)
	}
	cli.Errored["resize"] = true
	if _, err := Resize(ctx, cl, "acme", d, "s-4vcpu-8gb"); err == nil || !strings.Contains(err.Error(), "the resize of Droplet") || made.Size != "s-2vcpu-4gb" || made.Status != "off" {
		t.Errorf("a resize that failed: %v, size %s, status %q", err, made.Size, made.Status)
	}
}

func TestResizeOffers(t *testing.T) {
	cl, _, d := parkedAccount(t)
	sizes, err := cl.Sizes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := func(offers []Offer) string {
		var n []string
		for _, o := range offers {
			n = append(n, o.Size.Slug)
		}
		return strings.Join(n, " ")
	}
	// An 80 GB disk in fra1: nothing with a smaller disk, nothing that
	// cannot be ordered there (c-2 is in nyc3 only), cheapest first.
	if got := names(ResizeOffers(sizes, d)); got != "s-2vcpu-4gb s-4vcpu-8gb" {
		t.Errorf("offers for an 80 GB disk: %s", got)
	}
	// The disk is the Droplet's own, not its size's: one created small and
	// resized up can still change to every size its disk fits.
	small := *d
	small.SizeSlug, small.Disk = "s-4vcpu-8gb", 25
	if got := names(ResizeOffers(sizes, &small)); got != "s-1vcpu-1gb s-2vcpu-2gb s-2vcpu-4gb s-4vcpu-8gb" {
		t.Errorf("offers for a 25 GB disk: %s", got)
	}
	// The size it has is listed even when it can no longer be ordered.
	old := *d
	old.SizeSlug = "s-2vcpu-4gb-old"
	if got := names(ResizeOffers(sizes, &old)); got != "s-2vcpu-4gb-old s-2vcpu-4gb s-4vcpu-8gb" {
		t.Errorf("offers for a size that is no longer sold: %s", got)
	}
	// A Droplet whose disk is not reported: its size's disk.
	old.SizeSlug, old.Disk = "s-2vcpu-2gb", 0
	if got := names(ResizeOffers(sizes, &old)); got != "s-2vcpu-2gb s-2vcpu-4gb s-4vcpu-8gb" {
		t.Errorf("offers without a disk: %s", got)
	}
}

func TestSnapshotIsReturnedOnlyWhenAvailable(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	c := boxConfig(t, ", tags: [\"team:web\"]")
	// A Droplet that runs is not snapshotted.
	if _, err := Snapshot(ctx, cl, c, d); err == nil || !strings.Contains(err.Error(), "only of a Droplet that is off") || len(cli.Snapshots) != 0 {
		t.Fatalf("snapshot of a running Droplet: %v", err)
	}
	if err := Stop(ctx, cl, "acme", d, t.Logf); err != nil {
		t.Fatal(err)
	}
	// Another snapshot of the same name, which keelbox did not take now.
	other := cli.AddSnapshot("keelbox-parked-acme")
	cli.ActionPolls = 3
	// While it is taken, the snapshot is pending and already tagged.
	cli.Hook = func(call string) {
		if !strings.HasPrefix(call, "compute droplet-action get") {
			return
		}
		for _, s := range cli.Snapshots {
			if s.ID != other.ID && (s.Status != "pending" || strings.Join(s.Tags, ",") != "keelbox,keelbox-box-acme,team:web,keelbox-parked") {
				t.Errorf("while it is taken: %+v", s)
			}
		}
	}
	img, err := Snapshot(ctx, cl, c, d)
	cli.Hook = nil
	if err != nil {
		t.Fatal(err)
	}
	made := cli.Snapshots[img.ID]
	if made == nil || img.ID == other.ID || made.DropletID != d.ID || made.Name != "keelbox-parked-acme" || made.Status != "available" {
		t.Fatalf("snapshot %+v, in the account %+v", img, made)
	}
	if !img.Parked("acme") || img.Status != "available" || img.MinDiskSize != 80 || img.SizeGigabytes != 2.5 || img.Created().IsZero() || !img.HasTag("team:web") {
		t.Errorf("returned %+v", img)
	}
	if called(cli, "compute droplet-action get") != 4 {
		t.Errorf("the action was polled %d times, want 4", called(cli, "compute droplet-action get"))
	}
	// The Droplet is as it was, and the other snapshot was not touched.
	if cli.Droplets[d.ID].Status != "off" || len(other.Tags) != 0 || cli.Snapshots[other.ID] == nil {
		t.Errorf("droplet %+v, other snapshot %+v", cli.Droplets[d.ID], other)
	}
}

// Whatever stops a snapshot, no snapshot of that call is left and the
// Droplet is as it was.
func TestSnapshotFailureLeavesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(cli *dotest.CLI, cancel func())
		want string
	}{
		{"the action fails", func(cli *dotest.CLI, _ func()) { cli.Errored["snapshot"] = true }, "the snapshot of Droplet"},
		{"it takes too long", func(cli *dotest.CLI, _ func()) { cli.ActionPolls = 1 << 30; snapshotTimeout = 10 * time.Millisecond }, "is still in-progress after"},
		{"a tag cannot be put on it", func(cli *dotest.CLI, _ func()) { cli.Fail["compute tag apply"] = "try again" }, "tag snapshot"},
		{"the wait is cancelled", func(cli *dotest.CLI, cancel func()) {
			cli.ActionPolls = 1 << 30
			cli.Hook = func(call string) {
				if strings.HasPrefix(call, "compute droplet-action get") {
					cancel()
				}
			}
		}, "context canceled"},
		{"it is cancelled as it starts", func(cli *dotest.CLI, cancel func()) {
			cli.Hook = func(call string) {
				if strings.HasPrefix(call, "compute droplet-action snapshot") {
					cancel()
				}
			}
		}, "context canceled"},
		{"the start is refused", func(cli *dotest.CLI, _ func()) { cli.Fail["compute droplet-action snapshot"] = "no capacity" }, "no capacity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl, cli, d := parkedAccount(t)
			if err := Stop(context.Background(), cl, "acme", d, t.Logf); err != nil {
				t.Fatal(err)
			}
			kept := cli.AddSnapshot("keelbox-parked-acme", "keelbox", "keelbox-box-acme", "keelbox-parked")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.set(cli, cancel)
			img, err := Snapshot(ctx, cl, boxConfig(t, ""), d)
			if err == nil || img != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("snapshot: %+v %v", img, err)
			}
			// Only the snapshot that was there before is left, and the
			// Droplet is off and not deleted.
			if len(cli.Snapshots) != 1 || cli.Snapshots[kept.ID] == nil {
				t.Errorf("snapshots left: %v", cli.Snapshots)
			}
			if made := cli.Droplets[d.ID]; made == nil || made.Status != "off" || called(cli, "compute droplet delete") != 0 {
				t.Errorf("the Droplet: %+v", made)
			}
			if strings.Contains(err.Error(), "could not be deleted") {
				t.Errorf("the unfinished snapshot was not deleted: %v", err)
			}
		})
	}
}

func TestParkedIsTheNewestAvailableTaggedSnapshot(t *testing.T) {
	cl, cli, _ := parkedAccount(t)
	ctx := context.Background()
	if img, err := Parked(ctx, cl, "acme"); img != nil || err != nil {
		t.Fatalf("nothing parked: %+v %v", img, err)
	}
	parked := []string{"keelbox", "keelbox-box-acme", "keelbox-parked"}
	older := cli.AddSnapshot("keelbox-parked-acme", parked...)
	newer := cli.AddSnapshot("renamed by hand", parked...)
	pending := cli.AddSnapshot("keelbox-parked-acme", parked...)
	pending.Status = "pending"
	// None of these is the box's parked snapshot: another box's, one
	// without the parked marker, one with only the name.
	cli.AddSnapshot("keelbox-parked-acme", "keelbox", "keelbox-box-acme2", "keelbox-parked")
	cli.AddSnapshot("keelbox-parked-acme", "keelbox", "keelbox-box-acme")
	cli.AddSnapshot("keelbox-parked-acme", "keelbox-box-acme", "keelbox-parked")
	named := cli.AddSnapshot("keelbox-parked-acme")
	img, err := Parked(ctx, cl, "acme")
	if err != nil || img == nil || img.ID != newer.ID {
		t.Fatalf("parked: %+v %v, want %d", img, err, newer.ID)
	}
	// Only a snapshot with the box's parked tags is deleted.
	n := &Image{ID: named.ID, Name: named.Name, Type: "snapshot"}
	if err := DeleteSnapshot(ctx, cl, "acme", n); err == nil || !strings.Contains(err.Error(), "refusing to delete image") || cli.Snapshots[named.ID] == nil {
		t.Errorf("delete of an untagged snapshot: %v", err)
	}
	backup := *img
	backup.Type = "backup"
	if err := DeleteSnapshot(ctx, cl, "acme", &backup); err == nil || cli.Snapshots[newer.ID] == nil {
		t.Errorf("delete of an image that is not a snapshot: %v", err)
	}
	if err := DeleteSnapshot(ctx, cl, "acme2", img); err == nil || cli.Snapshots[newer.ID] == nil {
		t.Errorf("delete of another box's snapshot: %v", err)
	}
	if called(cli, "compute image delete") != 0 {
		t.Errorf("calls %v", cli.Calls())
	}
	if err := DeleteSnapshot(ctx, cl, "acme", img); err != nil || cli.Snapshots[newer.ID] != nil {
		t.Fatalf("delete: %v", err)
	}
	// One that is gone already is not an error.
	if err := DeleteSnapshot(ctx, cl, "acme", img); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if img, err := Parked(ctx, cl, "acme"); err != nil || img == nil || img.ID != older.ID {
		t.Errorf("after the delete: %+v %v", img, err)
	}
}

// A sweep removes every parked snapshot of the box, complete or not, and
// only those; the tags go when nothing carries them.
func TestSweepSnapshots(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	parked := []string{"keelbox", "keelbox-box-acme", "keelbox-parked"}
	done := cli.AddSnapshot("keelbox-parked-acme", parked...)
	pending := cli.AddSnapshot("keelbox-parked-acme", parked...)
	pending.Status = "pending"
	theirs := cli.AddSnapshot("keelbox-parked-acme")
	other := cli.AddSnapshot("keelbox-parked-web", "keelbox", "keelbox-box-web", "keelbox-parked")
	// While the box has a Droplet nothing is swept: a park may be at work.
	if gone, err := SweepSnapshots(ctx, cl, "acme"); err == nil || len(gone) != 0 || len(cli.Snapshots) != 4 {
		t.Fatalf("sweep beside a Droplet: %v %v", gone, err)
	}
	if err := Release(ctx, cl, d, "acme"); err != nil || len(cli.Droplets) != 0 {
		t.Fatalf("release: %v", err)
	}
	// Release keeps the firewall and the tags for the next Droplet.
	if f, err := FindFirewall(ctx, cl, "acme"); err != nil || f == nil || !cli.Tags["keelbox-box-acme"] {
		t.Fatalf("after release: firewall %+v %v, tags %v", f, err, cli.Tags)
	}
	gone, err := SweepSnapshots(ctx, cl, "acme")
	if err != nil || len(gone) != 2 || cli.Snapshots[done.ID] != nil || cli.Snapshots[pending.ID] != nil {
		t.Fatalf("sweep: %v %v", gone, err)
	}
	if cli.Snapshots[theirs.ID] == nil || cli.Snapshots[other.ID] == nil {
		t.Errorf("a snapshot that is not the box's was deleted: %v", cli.Snapshots)
	}
	// The box's tag stays while its firewall is there, and the parked
	// marker while another box is parked.
	if !cli.Tags["keelbox-box-acme"] || !cli.Tags["keelbox-parked"] || !cli.Tags["keelbox"] {
		t.Errorf("tags %v", cli.Tags)
	}
	if f, err := Sweep(ctx, cl, "acme"); err != nil || f == nil || cli.Tags["keelbox-box-acme"] {
		t.Errorf("sweep of the firewall: %+v %v, tags %v", f, err, cli.Tags)
	}
	// With the firewall gone, the last snapshot takes every tag with it.
	if gone, err := SweepSnapshots(ctx, cl, "web"); err != nil || len(gone) != 1 {
		t.Fatalf("sweep of the other box: %v %v", gone, err)
	}
	if cli.Tags["keelbox-parked"] || cli.Tags["keelbox-box-web"] || cli.Tags["keelbox"] {
		t.Errorf("tags left: %v", cli.Tags)
	}
}

func TestRestoreCreatesTheDropletFromTheSnapshot(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	c := boxConfig(t, ", tags: [\"team:web\"]")
	// The firewall of a box that had closed public SSH.
	closed := false
	if _, _, err := EnsureFirewall(ctx, cl, "acme", &closed); err != nil {
		t.Fatal(err)
	}
	if err := Stop(ctx, cl, "acme", d, t.Logf); err != nil {
		t.Fatal(err)
	}
	img, err := Snapshot(ctx, cl, c, d)
	if err != nil {
		t.Fatal(err)
	}
	// While the box has a Droplet there is nothing to restore.
	if _, err := Restore(ctx, cl, c, img, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("restore beside a Droplet: %v", err)
	}
	if err := Release(ctx, cl, d, "acme"); err != nil {
		t.Fatal(err)
	}
	cli.NewPolls, cli.FirewallPolls = 2, 3
	var said []string
	got, err := Restore(ctx, cl, c, img, func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) })
	if err != nil {
		t.Fatal(err)
	}
	made := cli.Droplets[got.ID]
	// The Droplet was named in the firewall straight after its creation,
	// before the wait for its boot, and Restore came back only when the
	// firewall no longer listed it as pending.
	calls := cli.Calls()
	for i, call := range calls {
		if strings.HasPrefix(call, "compute droplet create") && !strings.HasPrefix(calls[i+1], "compute firewall add-droplets fw-") {
			t.Errorf("after the create came %q", calls[i+1])
		}
	}
	if n := called(cli, "compute firewall get"); n != 4 || !strings.HasPrefix(calls[len(calls)-1], "compute firewall get") {
		t.Errorf("the firewall was asked %d times, last call %q", n, calls[len(calls)-1])
	}
	for _, f := range cli.Firewalls {
		if len(f.DropletIDs) != 1 || f.DropletIDs[0] != got.ID {
			t.Errorf("the firewall names %v, not %d", f.DropletIDs, got.ID)
		}
	}
	if got.ID == d.ID || got.Name != "acme" || got.Status != "active" || got.PublicIPv4() != made.IP || !got.Managed("acme") {
		t.Errorf("restored %+v", got)
	}
	if made.FromSnapshot != img.ID || made.Region != "fra1" || made.Size != "s-2vcpu-4gb" || strings.Join(made.Tags, ",") != "keelbox,keelbox-box-acme,team:web" {
		t.Errorf("created %+v", made)
	}
	// The user data keeps the disk's host keys and hostname and writes the
	// SSH settings the Droplet was created with. It creates no account and
	// carries no key, and no account key is named: there is none.
	want := "#cloud-config\n# keelbox box acme, resumed from its parked snapshot: the disk is the box as it was parked.\n" +
		"ssh_deletekeys: false\npreserve_hostname: true\nusers: []\nssh_pwauth: false\ndisable_root: true\n"
	if made.UserData != want || made.UserDataMode != 0o600 || len(made.SSHKeys) != 0 {
		t.Errorf("user data %q, mode %o, keys %v", made.UserData, made.UserDataMode, made.SSHKeys)
	}
	// The firewall was there before the Droplet, with the rules it had.
	if called(cli, "compute firewall create") != 1 || called(cli, "compute firewall update") != 1 {
		t.Errorf("the firewall was changed: %v", cli.Calls())
	}
	if f, err := FindFirewall(ctx, cl, "acme"); err != nil || f == nil || HasPublicSSH(f) || !f.Targets("keelbox-box-acme") {
		t.Errorf("firewall %+v %v", f, err)
	}
	if len(said) != 1 || !strings.Contains(said[0], fmt.Sprintf("creating acme from snapshot %d: s-2vcpu-4gb in fra1 (about USD 24.00/month before tax)", img.ID)) {
		t.Errorf("said %q", said)
	}
	// The snapshot stays until the caller deletes it.
	if cli.Snapshots[img.ID] == nil {
		t.Error("restore deleted the snapshot")
	}
}

// A firewall DigitalOcean does not report applied is said, with the
// Droplet that exists.
func TestRestoreSaysWhenTheFirewallIsNotApplied(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	c := boxConfig(t, "")
	cli.AddSnapshot("keelbox-parked-acme", "keelbox", "keelbox-box-acme", "keelbox-parked")
	if err := Release(ctx, cl, d, "acme"); err != nil {
		t.Fatal(err)
	}
	p, err := Parked(ctx, cl, "acme")
	if err != nil || p == nil {
		t.Fatalf("parked %+v %v", p, err)
	}
	cli.FirewallPolls, firewallTimeout = 1<<30, 10*time.Millisecond
	got, err := Restore(ctx, cl, c, p, t.Logf)
	if err == nil || got == nil || cli.Droplets[got.ID] == nil {
		t.Fatalf("restore: %+v %v", got, err)
	}
	for _, want := range []string{fmt.Sprintf("Droplet %d runs, but DigitalOcean does not report the firewall keelbox-acme applied to Droplet %d after", got.ID, got.ID),
		"only the box's own firewall is in front of it", "doctl compute firewall get fw-"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	// A firewall that cannot name the Droplet: the Droplet exists, and is
	// returned with the error.
	if err := Release(ctx, cl, got, "acme"); err != nil {
		t.Fatal(err)
	}
	cli.Fail["compute firewall add-droplets"] = "try again"
	got, err = Restore(ctx, cl, c, p, t.Logf)
	if err == nil || got == nil || cli.Droplets[got.ID] == nil || !strings.Contains(err.Error(), "cloud firewall: doctl compute firewall add-droplets: 500 try again") {
		t.Errorf("restore: %+v %v", got, err)
	}
}

// An account key that is one of the admin's is named, as at create, so
// DigitalOcean sets no root password.
func TestRestoreNamesTheAdminsAccountKey(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	c := boxConfig(t, "")
	cli.Keys[7] = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC other@host"
	cli.Keys[9] = adminKey
	img := cli.AddSnapshot("keelbox-parked-acme", "keelbox", "keelbox-box-acme", "keelbox-parked")
	if err := Release(ctx, cl, d, "acme"); err != nil {
		t.Fatal(err)
	}
	p, err := Parked(ctx, cl, "acme")
	if err != nil || p == nil || p.ID != img.ID {
		t.Fatalf("parked %+v %v", p, err)
	}
	got, err := Restore(ctx, cl, c, p, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if made := cli.Droplets[got.ID]; strings.Join(made.SSHKeys, ",") != "9" {
		t.Errorf("keys %v", made.SSHKeys)
	}
}

// What the config asks for and the snapshot cannot give is said before
// anything is created, with the fix.
func TestRestoreNamesTheFix(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	img := cli.AddSnapshot("keelbox-parked-acme", "keelbox", "keelbox-box-acme", "keelbox-parked")
	if err := Release(ctx, cl, d, "acme"); err != nil {
		t.Fatal(err)
	}
	p, err := Parked(ctx, cl, "acme")
	if err != nil || p == nil {
		t.Fatalf("parked %+v %v", p, err)
	}
	for _, tc := range []struct {
		name  string
		set   func(c *config.Config)
		wants []string
	}{
		{"a smaller disk", func(c *config.Config) { c.Target.DigitalOcean.Size = "s-2vcpu-2gb" }, []string{"size s-2vcpu-2gb has a 60 GB disk and the parked snapshot needs 80 GB",
			"set target.digitalocean.size to one with a disk of 80 GB or more, for example: s-2vcpu-4gb, s-4vcpu-8gb, then run keelbox resume acme again"}},
		{"a size not sold there", func(c *config.Config) { c.Target.DigitalOcean.Size = "c-2" }, []string{`target.digitalocean.size "c-2" cannot be ordered in fra1 now; the snapshot is kept`, "then run keelbox resume acme again"}},
		{"another region", func(c *config.Config) { c.Target.DigitalOcean.Region = "nyc3" }, []string{fmt.Sprintf("the parked snapshot %d is stored in fra1 and target.digitalocean.region is nyc3", img.ID), "set region to fra1, then run keelbox resume acme again"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := boxConfig(t, "")
			tc.set(c)
			got, err := Restore(ctx, cl, c, p, t.Logf)
			if err == nil || got != nil {
				t.Fatalf("restore: %+v %v", got, err)
			}
			for _, w := range tc.wants {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error lacks %q: %v", w, err)
				}
			}
			if called(cli, "compute droplet create") != 0 || cli.Snapshots[img.ID] == nil {
				t.Errorf("something changed: %v", cli.Calls())
			}
		})
	}
	// An image that is not the box's parked snapshot is refused.
	p.Tags = []string{"keelbox", "keelbox-box-acme"}
	if _, err := Restore(ctx, cl, boxConfig(t, ""), p, t.Logf); err == nil || !strings.Contains(err.Error(), "not its parked snapshot") || called(cli, "compute droplet create") != 0 {
		t.Errorf("restore from an unmarked image: %v", err)
	}
}

// Nothing here changes a Droplet that keelbox did not create for the box.
func TestParkCallsRefuseUntaggedDroplets(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	c := boxConfig(t, "")
	before := len(cli.Calls())
	for name, theirs := range map[string]*Droplet{
		"without keelbox's tag": {ID: d.ID, Name: "acme", Tags: []string{"keelbox-box-acme"}},
		"another box's":         {ID: d.ID, Name: "acme", Tags: []string{"keelbox", "keelbox-box-web"}},
		"of another name":       {ID: d.ID, Name: "web", Tags: []string{"keelbox", "keelbox-box-acme"}},
	} {
		if err := Stop(ctx, cl, "acme", theirs, t.Logf); err == nil || !strings.Contains(err.Error(), "refusing to change Droplet") {
			t.Errorf("stop %s: %v", name, err)
		}
		if _, err := Start(ctx, cl, "acme", theirs); err == nil {
			t.Errorf("start %s: %v", name, err)
		}
		if _, err := Resize(ctx, cl, "acme", theirs, "s-4vcpu-8gb"); err == nil {
			t.Errorf("resize %s: %v", name, err)
		}
		if _, err := Snapshot(ctx, cl, c, theirs); err == nil {
			t.Errorf("snapshot %s: %v", name, err)
		}
		if err := Release(ctx, cl, theirs, "acme"); err == nil || !strings.Contains(err.Error(), "refusing to delete Droplet") {
			t.Errorf("release %s: %v", name, err)
		}
	}
	if len(cli.Calls()) != before {
		t.Errorf("doctl was run: %v", cli.Calls()[before:])
	}
	// A Droplet whose tags were taken off since keelbox found it.
	cli.Droplets[d.ID].Tags = []string{"keelbox-box-acme"}
	if err := Stop(ctx, cl, "acme", d, t.Logf); err == nil || !strings.Contains(err.Error(), "no longer") || called(cli, "compute droplet-action") != 0 {
		t.Errorf("stop of a Droplet that lost its tags: %v", err)
	}
}

// An error says the doctl command and the API's reason, and nothing of
// the request doctl sent.
func TestParkErrorsSayTheCommandAndTheReason(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	ctx := context.Background()
	c := boxConfig(t, "")
	for _, cmd := range []string{"compute droplet-action shutdown", "compute droplet-action power-on", "compute droplet-action resize", "compute image list-user"} {
		cli.Fail[cmd] = "Server Error"
	}
	errs := map[string]error{"compute droplet-action shutdown": Stop(ctx, cl, "acme", d, t.Logf)}
	cli.Droplets[d.ID].Status = "off"
	_, errs["compute image list-user"] = Snapshot(ctx, cl, c, d)
	_, errs["compute droplet-action power-on"] = Start(ctx, cl, "acme", d)
	_, errs["compute droplet-action resize"] = Resize(ctx, cl, "acme", d, "s-4vcpu-8gb")
	for cmd, err := range errs {
		if err == nil || err.Error() != "doctl "+cmd+": 500 Server Error" {
			t.Errorf("%s: %v", cmd, err)
		}
	}
	if _, err := Parked(ctx, cl, "acme"); err == nil || strings.Contains(err.Error(), "api.digitalocean.com") {
		t.Errorf("parked: %v", err)
	}
}

// A token with custom scopes that lacks the ones these calls need: the
// error names them and how to replace the token.
func TestParkNamesTheScopesATokenLacks(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	cl.Context = "work"
	cli.Forbidden["compute droplet-action shutdown"] = true
	err := Stop(context.Background(), cl, "acme", d, t.Logf)
	if err == nil || cli.Droplets[d.ID].Status != "" {
		t.Fatalf("stop: %v", err)
	}
	for _, want := range []string{"403", "droplet:update, image:create, image:update, image:delete", TokensURL, "doctl auth init --context work"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if !strings.HasSuffix(Scopes, ParkScopes) {
		t.Errorf("the scopes keelbox names do not cover resize, park and resume: %s", Scopes)
	}
}

// Any call a token with custom scopes may not make ends with every scope
// keelbox needs and how to save a token that has them: DigitalOcean does
// not say which scope was missing. The park commands name theirs and do
// not repeat the general list.
func TestARefusedCallNamesTheScopes(t *testing.T) {
	cl, cli, d := parkedAccount(t)
	cl.Context = "work"
	cli.Forbidden["compute tag create"] = true
	err := cl.compute(context.Background(), nil, "tag", "create", "keelbox-box-acme")
	if err == nil || !IsStatus(err, 403) {
		t.Fatalf("tag create: %v", err)
	}
	for _, want := range []string{"doctl compute tag create: 403", "the doctl context work", "tag:create", "firewall:create", TokensURL, "doctl auth init --context work",
		`doctl auth login --scopes "account:read regions:read`, `image:delete" --context work`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if !strings.HasSuffix(err.Error(), "doctl auth init --context work") {
		t.Errorf("the error does not end with the fix: %v", err)
	}
	cli.Forbidden["compute droplet-action shutdown"] = true
	err = Stop(context.Background(), cl, "acme", d, t.Logf)
	if err == nil || strings.Count(err.Error(), TokensURL) != 1 || strings.Contains(err.Error(), "lacks a scope this call needs") ||
		!strings.Contains(err.Error(), "also need the scopes droplet:update, image:create, image:update, image:delete") {
		t.Errorf("a refused park call repeats the general text, or lacks its own: %v", err)
	}
}
