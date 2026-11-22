package gcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/gcp/gcptest"
)

// parkBox is a fake project with the box acme created in it as keelbox
// creates it: the VM on its reserved address behind its two rules.
func parkBox(t *testing.T, extra string) (*gcptest.CLI, *Client, *config.Config, *Instance) {
	t.Helper()
	cli, cl := fake(t)
	oldStop, oldSnap, oldSlow := stopTimeout, snapshotTimeout, slowStop
	stopTimeout, snapshotTimeout = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { stopTimeout, snapshotTimeout, slowStop = oldStop, oldSnap, oldSlow })
	c := boxConfig(t, extra)
	in, err := Create(context.Background(), cl, c, testHostKey, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	return cli, cl, c, in
}

// changes are the calls that changed the project, in order.
func changes(cli *gcptest.CLI, from int) []string {
	var out []string
	for _, c := range cli.Calls()[from:] {
		w := strings.Fields(c)
		if len(w) < 3 || w[0] != "compute" {
			continue
		}
		switch w[2] {
		case "list", "describe":
			continue
		}
		out = append(out, strings.Join(w[:4], " "))
	}
	return out
}

func TestStopResizeStart(t *testing.T) {
	cli, cl, _, in := parkBox(t, "")
	ctx := context.Background()
	vm := cli.Instances["europe-west3-a/acme"]
	ip := in.PublicIPv4()
	var said []string
	logf := func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) }

	// A VM that runs cannot change its type.
	if _, err := Resize(ctx, cl, "acme", in, "e2-standard-2"); err == nil || !strings.Contains(err.Error(), "only while it is stopped") || cli.Called("compute instances set-machine-type") {
		t.Errorf("resize of a running VM: %v", err)
	}
	from := len(cli.Calls())
	if err := Stop(ctx, cl, "acme", in, logf); err != nil {
		t.Fatal(err)
	}
	if vm.Status != "TERMINATED" || len(said) != 0 {
		t.Errorf("after stop: %s, said %q", vm.Status, said)
	}
	// Stopped already: nothing is sent.
	if err := Stop(ctx, cl, "acme", in, logf); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(changes(cli, from), "|"); got != "compute instances stop acme" {
		t.Errorf("stop sent %q", got)
	}
	// The stopped VM keeps its reserved address.
	off, err := Find(ctx, cl, "acme")
	if err != nil || !off.Stopped() || off.PublicIPv4() != ip {
		t.Fatalf("stopped VM %+v %v", off, err)
	}

	// A change Google refuses leaves the VM as it was, stopped.
	cli.Fail["compute instances set-machine-type"] = "Quota 'CPUS' exceeded.  Limit: 8.0 in region europe-west3."
	if _, err := Resize(ctx, cl, "acme", in, "e2-standard-4"); err == nil || !strings.Contains(err.Error(), "Quota 'CPUS' exceeded") {
		t.Errorf("a refused resize: %v", err)
	}
	if vm.MachineType != "e2-medium" || vm.Status != "TERMINATED" {
		t.Errorf("after a refused resize the VM is %s, %s", vm.MachineType, vm.Status)
	}
	delete(cli.Fail, "compute instances set-machine-type")
	if _, err := Resize(ctx, cl, "acme", in, "e9-none-1"); err == nil || vm.MachineType != "e2-medium" {
		t.Errorf("a type that does not exist: %v, VM is %s", err, vm.MachineType)
	}

	got, err := Resize(ctx, cl, "acme", in, "e2-standard-2")
	if err != nil || got.TypeName() != "e2-standard-2" || !got.Stopped() || vm.MachineType != "e2-standard-2" || vm.Disk != "50GB" {
		t.Fatalf("resize: %+v %v", got, err)
	}
	// And back down: the disk was never touched.
	if got, err = Resize(ctx, cl, "acme", got, "e2-small"); err != nil || got.TypeName() != "e2-small" {
		t.Fatalf("resize down: %+v %v", got, err)
	}

	run, err := Start(ctx, cl, "acme", in)
	if err != nil || run.Status != "RUNNING" || run.PublicIPv4() != ip || run.TypeName() != "e2-small" {
		t.Fatalf("start: %+v %v", run, err)
	}
	// Running already: nothing is sent.
	from = len(cli.Calls())
	if _, err := Start(ctx, cl, "acme", in); err != nil || len(changes(cli, from)) != 0 {
		t.Errorf("start of a running VM: %v, %q", err, changes(cli, from))
	}

	// A stop that takes as long as a switch-off does is said.
	slowStop = 0
	if err := Stop(ctx, cl, "acme", in, logf); err != nil || len(said) != 1 || !strings.Contains(said[0], "switches a VM off when its operating system has not shut down in time") {
		t.Errorf("a slow stop: %v, said %q", err, said)
	}
}

// A VM that is still stopping is started once it has stopped.
func TestStartWaitsForAStopInProgress(t *testing.T) {
	cli, cl, _, in := parkBox(t, "")
	vm := cli.Instances["europe-west3-a/acme"]
	vm.Status = "STOPPING"
	lists := 0
	cli.Hook = func(call string) {
		if strings.HasPrefix(call, "compute instances list") {
			if lists++; lists == 3 {
				vm.Status = "TERMINATED"
			}
		}
	}
	run, err := Start(context.Background(), cl, "acme", in)
	if err != nil || run.Status != "RUNNING" || !cli.Called("compute instances start acme") {
		t.Fatalf("start: %+v %v", run, err)
	}
	// One that never stops is not started, and the wait ends.
	vm.Status = "STOPPING"
	cli.Hook = nil
	if _, err := Start(context.Background(), cl, "acme", in); err == nil || !strings.Contains(err.Error(), "still STOPPING") {
		t.Errorf("a VM that never stops: %v", err)
	}
}

func TestResizeOffers(t *testing.T) {
	cli := gcptest.New()
	_, cl := cli, &Client{Run: cli}
	types, err := cl.MachineTypes(context.Background(), "europe-west3-a")
	if err != nil {
		t.Fatal(err)
	}
	names := func(os []Offer) string {
		var n []string
		for _, o := range os {
			n = append(n, o.Type.Name)
		}
		return strings.Join(n, " ")
	}
	// The sizes keelbox up lists: no Arm type, no retired one, no other family.
	if got := names(ResizeOffers(types, "e2-medium")); got != "e2-micro e2-small e2-medium e2-standard-2 e2-standard-4" {
		t.Errorf("offers for e2-medium: %s", got)
	}
	// A VM of another family keeps its own type in the list.
	if got := names(ResizeOffers(types, "c3-standard-4")); got != "e2-micro e2-small e2-medium e2-standard-2 c3-standard-4 e2-standard-4" {
		t.Errorf("offers for c3-standard-4: %s", got)
	}
	// One bigger than the list goes to: its family up to twice its size.
	more := append([]MachineType{}, types...)
	for _, n := range []int{8, 32, 64} {
		more = append(more, MachineType{Name: fmt.Sprintf("e2-standard-%d", n), GuestCPUs: n, MemoryMB: n * 4096, Architecture: "X86_64"})
	}
	if got := names(ResizeOffers(more, "e2-standard-16")); got != "e2-micro e2-small e2-medium e2-standard-2 e2-standard-4 e2-standard-8 e2-standard-16 e2-standard-32" {
		t.Errorf("offers for e2-standard-16: %s", got)
	}
	// A type the zone does not list is still the current one.
	if got := ResizeOffers(types, "e2-custom-2-4096"); len(got) != 6 || got[0].Type.Name != "e2-custom-2-4096" {
		t.Errorf("offers for a custom type: %s", names(got))
	}
}

func TestSnapshotReleaseRestore(t *testing.T) {
	cli, cl, c, in := parkBox(t, ", labels: {team: web}, disk_gb: 30")
	ctx := context.Background()
	var said []string
	logf := func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) }
	ip := in.PublicIPv4()
	// The box has closed public SSH; that has to survive the park.
	closed := false
	if _, _, err := EnsureFirewall(ctx, cl, "acme", &closed); err != nil {
		t.Fatal(err)
	}
	if err := Parkable(cl, "acme", in); err != nil {
		t.Fatal(err)
	}
	// A VM that runs is not snapshotted.
	if _, err := TakeSnapshot(ctx, cl, c, in); err == nil || !strings.Contains(err.Error(), "only of a VM that is stopped") || len(cli.Snapshots) != 0 {
		t.Errorf("snapshot of a running VM: %v", err)
	}
	if err := Stop(ctx, cl, "acme", in, logf); err != nil {
		t.Fatal(err)
	}
	if p, err := Parked(ctx, cl, "acme"); p != nil || err != nil {
		t.Fatalf("parked before the park: %+v %v", p, err)
	}

	cli.SnapshotPolls = 4
	from := len(cli.Calls())
	snap, err := TakeSnapshot(ctx, cl, c, in)
	if err != nil {
		t.Fatal(err)
	}
	made := cli.Snapshots[snap.Name]
	if made == nil || snap.Status != "READY" || !snap.Parked("acme") || time.Since(snap.Created()) > time.Minute || snap.Describe() != "1.3 GB stored of a 30 GB disk" {
		t.Fatalf("snapshot %+v (%s)", snap, snap.Describe())
	}
	if made.SourceDisk != "europe-west3-a/acme" || made.Labels["keelbox"] != "true" || made.Labels["keelbox-box"] != "acme" || made.Labels["keelbox-parked"] != "true" || made.Labels["team"] != "web" ||
		!strings.HasPrefix(made.Name, "keelbox-acme-2") || !strings.HasPrefix(made.Description, "Managed by keelbox") {
		t.Errorf("made %+v", made)
	}
	// It was read until Google reported it READY, and the VM is as it was.
	reads := 0
	for _, call := range cli.Calls()[from:] {
		if strings.HasPrefix(call, "compute snapshots describe "+snap.Name) {
			reads++
		}
	}
	if vm := cli.Instances["europe-west3-a/acme"]; reads < 5 || vm == nil || vm.Status != "TERMINATED" || !cli.Disks["europe-west3-a/acme"] {
		t.Errorf("%d reads of the snapshot, VM %+v", reads, vm)
	}
	if p, err := Parked(ctx, cl, "acme"); err != nil || p == nil || p.Name != snap.Name {
		t.Fatalf("parked: %+v %v", p, err)
	}

	// Release: the VM, its disk and the address go; the rules stay.
	done, err := Release(ctx, cl, "acme", in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "deleted VM acme (europe-west3-a) and its disk|released address keelbox-acme (" + ip + ")"; strings.Join(done, "|") != want {
		t.Errorf("release said %q", done)
	}
	if len(cli.Instances)+len(cli.Disks)+len(cli.Addresses) != 0 || len(cli.Rules) != 2 || len(cli.Snapshots) != 1 {
		t.Fatalf("after release: %d VMs, %d disks, %d addresses, %d rules, %d snapshots", len(cli.Instances), len(cli.Disks), len(cli.Addresses), len(cli.Rules), len(cli.Snapshots))
	}

	// Restore: the VM of the config, from the snapshot, on a new address,
	// behind the rules as they were.
	said, from = nil, len(cli.Calls())
	got, err := Restore(ctx, cl, c, snap, logf)
	if err != nil {
		t.Fatal(err)
	}
	vm := cli.Instances["europe-west3-a/acme"]
	addr := cli.Addresses["europe-west3/keelbox-acme"]
	if vm == nil || addr == nil || got.Status != "RUNNING" || got.PublicIPv4() != addr.IP || addr.IP == ip || !got.Managed("acme") {
		t.Fatalf("restored %+v on %+v", got, addr)
	}
	if vm.SourceSnapshot != snap.Name || vm.Image != "" || vm.Disk != "30GB" || vm.DiskType != "pd-balanced" || vm.MachineType != "e2-medium" || vm.Net != "default" ||
		vm.Labels["team"] != "web" || vm.Labels["keelbox-parked"] != "" || strings.Join(vm.Tags, ",") != "keelbox-box-acme" {
		t.Errorf("restored VM %+v", vm)
	}
	// No login the disk does not have: the settings of a created box, and
	// user data that creates no account and carries no key.
	if vm.ServiceAccount != "" || len(vm.Metadata) != 3 || len(got.LoginProblems()) != 0 || vm.UserDataMode != 0o600 || vm.UserData != ResumeUserData("acme") {
		t.Errorf("service account %q, metadata %v, problems %v, user data mode %o:\n%s", vm.ServiceAccount, vm.Metadata, got.LoginProblems(), vm.UserDataMode, vm.UserData)
	}
	for _, want := range []string{"ssh_deletekeys: false\n", "preserve_hostname: true\n", "\nusers: []\n"} {
		if !strings.Contains(vm.UserData, want) {
			t.Errorf("user data lacks %q:\n%s", want, vm.UserData)
		}
	}
	for _, not := range []string{"ssh_authorized_keys", "ssh_keys", "PRIVATE KEY", "passwd", "sudo"} {
		if strings.Contains(vm.UserData, not) {
			t.Errorf("user data holds %q:\n%s", not, vm.UserData)
		}
	}
	for _, call := range cli.Calls()[from:] {
		if i := strings.Index(call, "user-data="); i >= 0 {
			if _, err := os.Stat(strings.Fields(call[i+len("user-data="):])[0]); err == nil {
				t.Error("the user data file was left behind")
			}
		}
	}
	// The rules were there before the VM and were not changed.
	if got := strings.Join(changes(cli, from), "|"); got != "compute addresses create keelbox-acme|compute instances create acme" {
		t.Errorf("restore sent %q", got)
	}
	if f, err := FindFirewall(ctx, cl, "acme"); err != nil || !f.InStep(false) {
		t.Errorf("firewall after restore: %+v %v", f, err)
	}
	if len(said) != 2 || !strings.HasPrefix(said[0], "reserved address keelbox-acme") || said[1] != "creating acme from snapshot "+snap.Name+": e2-medium in europe-west3-a, 30 GB disk" {
		t.Errorf("restore said %q", said)
	}
	// With the VM there, a second restore is refused.
	if _, err := Restore(ctx, cl, c, snap, logf); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second restore: %v", err)
	}
	if err := DeleteSnapshot(ctx, cl, "acme", snap); err != nil || len(cli.Snapshots) != 0 {
		t.Errorf("delete snapshot: %v, %d left", err, len(cli.Snapshots))
	}
	// Gone already is not an error.
	if err := DeleteSnapshot(ctx, cl, "acme", snap); err != nil {
		t.Errorf("delete a snapshot that is gone: %v", err)
	}
}

// Whatever keeps the snapshot from completing, none is left and the VM is
// as it was.
func TestSnapshotFailureLeavesNoSnapshot(t *testing.T) {
	for name, tc := range map[string]struct {
		breakIt func(cli *gcptest.CLI, cancel context.CancelFunc)
		want    string
	}{
		"the order is refused": {func(cli *gcptest.CLI, _ context.CancelFunc) {
			cli.Fail["compute snapshots create"] = "Quota 'SNAPSHOTS' exceeded.  Limit: 1000.0 globally."
		}, "Quota 'SNAPSHOTS' exceeded"},
		"Google reports it failed": {func(cli *gcptest.CLI, _ context.CancelFunc) { cli.SnapshotPolls, cli.SnapshotFails = 2, true }, "as FAILED"},
		"it takes too long":        {func(cli *gcptest.CLI, _ context.CancelFunc) { cli.SnapshotPolls = 1 << 30 }, "is still CREATING after"},
		"the wait is cancelled": {func(cli *gcptest.CLI, cancel context.CancelFunc) {
			cli.SnapshotPolls = 1 << 30
			reads := 0
			cli.Hook = func(call string) {
				if strings.HasPrefix(call, "compute snapshots describe") {
					if reads++; reads == 3 {
						cancel()
					}
				}
			}
		}, "context canceled"},
	} {
		cli, cl, c, in := parkBox(t, "")
		ctx, cancel := context.WithCancel(context.Background())
		if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
			t.Fatal(err)
		}
		tc.breakIt(cli, cancel)
		snap, err := TakeSnapshot(ctx, cl, c, in)
		if err == nil || snap != nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %+v %v", name, snap, err)
		}
		if name == "the wait is cancelled" && !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v is not the context's error", name, err)
		}
		vm := cli.Instances["europe-west3-a/acme"]
		if len(cli.Snapshots) != 0 || vm == nil || vm.Status != "TERMINATED" || !cli.Disks["europe-west3-a/acme"] || len(cli.Addresses) != 1 {
			t.Errorf("%s: %d snapshots left, VM %+v", name, len(cli.Snapshots), vm)
		}
		if cli.Called("compute instances delete") {
			t.Errorf("%s: the VM was deleted", name)
		}
		cancel()
	}

	// An unfinished snapshot that cannot be deleted is named, with the
	// command that deletes it.
	cli, cl, c, in := parkBox(t, "")
	ctx := context.Background()
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	cli.SnapshotPolls, cli.SnapshotFails = 2, true
	cli.Fail["compute snapshots delete"] = "Required 'compute.snapshots.delete' permission for 'projects/acme-dev/global/snapshots/x'"
	if _, err := TakeSnapshot(ctx, cl, c, in); err == nil || !strings.Contains(err.Error(), "could not be deleted") || !strings.Contains(err.Error(), "gcloud compute snapshots delete keelbox-acme-") {
		t.Errorf("a snapshot that cannot be deleted: %v", err)
	}
}

// park keeps the boot disk only: a VM with another disk is refused before
// anything changes, and a boot disk told to outlive its VM is told
// otherwise before the VM is deleted.
func TestParkAndTheDisks(t *testing.T) {
	cli, cl, c, in := parkBox(t, "")
	ctx := context.Background()
	vm := cli.Instances["europe-west3-a/acme"]
	vm.ExtraDisks = []string{"data-1"}
	with, err := Find(ctx, cl, "acme")
	if err != nil {
		t.Fatal(err)
	}
	err = Parkable(cl, "acme", with)
	if err == nil || !strings.Contains(err.Error(), "gcloud compute instances detach-disk acme --disk data-1 --zone europe-west3-a --project acme-dev; then: keelbox park acme") {
		t.Errorf("a VM with a data disk: %v", err)
	}
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	if _, err := TakeSnapshot(ctx, cl, c, in); err == nil || len(cli.Snapshots) != 0 {
		t.Errorf("snapshot of a VM with a data disk: %v", err)
	}
	if _, err := Release(ctx, cl, "acme", in); err == nil || cli.Called("compute instances delete") {
		t.Errorf("release of a VM with a data disk: %v", err)
	}

	vm.ExtraDisks, vm.KeepDisk = nil, true
	from := len(cli.Calls())
	if _, err := Release(ctx, cl, "acme", in); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(changes(cli, from), "|"); got != "compute instances set-disk-auto-delete acme|compute instances delete acme|compute addresses delete keelbox-acme" {
		t.Errorf("release sent %q", got)
	}
	if len(cli.Instances)+len(cli.Disks)+len(cli.Addresses) != 0 {
		t.Errorf("after release: %d VMs, %d disks, %d addresses", len(cli.Instances), len(cli.Disks), len(cli.Addresses))
	}
}

// An address that cannot be released does not undo the park: the VM is
// gone, and the list says what stays billed and how to release it.
func TestReleaseSaysAnAddressItCouldNotRelease(t *testing.T) {
	cli, cl, _, in := parkBox(t, "")
	cli.Fail["compute addresses delete"] = "Required 'compute.addresses.delete' permission"
	done, err := Release(context.Background(), cl, "acme", in)
	if err != nil || len(cli.Instances) != 0 || len(done) != 2 ||
		!strings.Contains(done[1], "warning: the address keelbox-acme was not released") || !strings.Contains(done[1], "gcloud compute addresses delete keelbox-acme --region europe-west3 --project acme-dev") {
		t.Errorf("release: %q %v", done, err)
	}
}

func TestParkNeverTouchesWhatItDidNotMake(t *testing.T) {
	cli, cl, c, in := parkBox(t, "")
	ctx := context.Background()
	from := len(cli.Calls())
	refused := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: not refused", what)
		}
		if got := changes(cli, from); len(got) != 0 {
			t.Errorf("%s: sent %q", what, got)
		}
	}
	each := func(in *Instance, why string) {
		t.Helper()
		refused("stop "+why, Stop(ctx, cl, "acme", in, t.Logf))
		_, err := Start(ctx, cl, "acme", in)
		refused("start "+why, err)
		_, err = Resize(ctx, cl, "acme", in, "e2-small")
		refused("resize "+why, err)
		_, err = TakeSnapshot(ctx, cl, c, in)
		refused("snapshot "+why, err)
		_, err = Release(ctx, cl, "acme", in)
		refused("release "+why, err)
	}
	// A record without keelbox's labels, or of another box.
	bare := *in
	bare.Labels = map[string]string{"owner": "sam"}
	each(&bare, "of a VM without the labels")
	other := *in
	other.Labels = provider.ManagedLabels("other", nil)
	each(&other, "of another box's VM")
	// The record is keelbox's, but the VM in the project no longer is.
	cli.Instances["europe-west3-a/acme"].Labels = map[string]string{"owner": "sam"}
	each(in, "of a VM that lost its labels")
	// Another VM took the box's name and labels meanwhile.
	cli.Instances["europe-west3-a/acme"].Labels = provider.ManagedLabels("acme", nil)
	stale := *in
	stale.ID = "1"
	each(&stale, "of a VM that was replaced")

	// Snapshots: only one with the box's parked labels is the box's.
	mine := cli.AddSnapshot("keelbox-acme-1", "acme")
	mine.Created = time.Now().Add(-time.Hour)
	newest := cli.AddSnapshot("keelbox-acme-2", "acme")
	cli.AddSnapshot("keelbox-acme-3", "acme").Status = "CREATING"
	unmarked := cli.AddSnapshot("acme-by-hand", "acme")
	delete(unmarked.Labels, "keelbox-parked")
	cli.AddSnapshot("keelbox-other-1", "other")
	p, err := Parked(ctx, cl, "acme")
	if err != nil || p == nil || p.Name != newest.Name {
		t.Fatalf("parked: %+v %v", p, err)
	}
	all, err := cl.SnapshotsWhere(ctx, "labels.keelbox-box=acme")
	if err != nil || len(all) != 4 {
		t.Fatalf("snapshots: %d %v", len(all), err)
	}
	from = len(cli.Calls())
	for i := range all {
		if all[i].Name == "acme-by-hand" {
			refused("delete a snapshot without the parked label", DeleteSnapshot(ctx, cl, "acme", &all[i]))
			_, err := Restore(ctx, cl, c, &all[i], t.Logf)
			refused("restore from a snapshot without the parked label", err)
		}
		if all[i].Name == "keelbox-acme-3" {
			_, err := Restore(ctx, cl, c, &all[i], t.Logf)
			refused("restore from a snapshot that is not complete", err)
		}
	}
	refused("delete another box's snapshot", DeleteSnapshot(ctx, cl, "acme", &Snapshot{Name: "keelbox-other-1", Labels: provider.ParkedLabels("other", nil)}))
}

// What is left of a box without a VM goes: its parked snapshots, whatever
// their state, its rules and a reserved address nothing uses.
func TestSweepRemovesParkedSnapshotsAndTheAddress(t *testing.T) {
	cli, cl, _, in := parkBox(t, "")
	ctx := context.Background()
	cli.AddSnapshot("keelbox-acme-1", "acme")
	cli.AddSnapshot("keelbox-acme-2", "acme").Status = "CREATING"
	delete(cli.AddSnapshot("acme-by-hand", "acme").Labels, "keelbox-parked")
	cli.AddSnapshot("keelbox-other-1", "other")
	// While the VM is there nothing is swept.
	if done, err := Sweep(ctx, cl, "acme"); err == nil || len(done) != 0 || len(cli.Snapshots) != 4 || len(cli.Rules) != 2 || len(cli.Addresses) != 1 {
		t.Fatalf("sweep with the VM there: %q %v", done, err)
	}
	// As after a park: no VM, no disk; here the address is still reserved.
	delete(cli.Instances, "europe-west3-a/acme")
	delete(cli.Disks, "europe-west3-a/acme")
	done, err := Sweep(ctx, cl, "acme")
	if err != nil {
		t.Fatal(err)
	}
	want := "deleted snapshot keelbox-acme-1|deleted snapshot keelbox-acme-2|deleted firewall rule keelbox-acme-allow|deleted firewall rule keelbox-acme-deny|released address keelbox-acme (" + in.PublicIPv4() + ")"
	if strings.Join(done, "|") != want {
		t.Errorf("sweep said %q", done)
	}
	if len(cli.Snapshots) != 2 || cli.Snapshots["acme-by-hand"] == nil || cli.Snapshots["keelbox-other-1"] == nil || len(cli.Rules)+len(cli.Addresses) != 0 {
		t.Errorf("after the sweep: snapshots %v, %d rules, %d addresses", cli.Snapshots, len(cli.Rules), len(cli.Addresses))
	}
	// Nothing left: nothing said, no error.
	if done, err := Sweep(ctx, cl, "acme"); err != nil || len(done) != 0 {
		t.Errorf("second sweep: %q %v", done, err)
	}
	// A snapshot that cannot be deleted does not keep the rest.
	cli, cl, _, _ = parkBox(t, "")
	delete(cli.Instances, "europe-west3-a/acme")
	cli.AddSnapshot("keelbox-acme-1", "acme")
	cli.Fail["compute snapshots delete"] = "Required 'compute.snapshots.delete' permission"
	if done, err := Sweep(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "parked snapshot") || len(done) != 3 || len(cli.Rules)+len(cli.Addresses) != 0 {
		t.Errorf("sweep with a snapshot that stays: %q %v", done, err)
	}
}

// A restore that creates no VM leaves no address billed for nothing, and
// the snapshot is kept.
func TestRestoreThatFailsLeavesNoAddress(t *testing.T) {
	cli, cl, c, in := parkBox(t, "")
	ctx := context.Background()
	if err := Stop(ctx, cl, "acme", in, t.Logf); err != nil {
		t.Fatal(err)
	}
	snap, err := TakeSnapshot(ctx, cl, c, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Release(ctx, cl, "acme", in); err != nil {
		t.Fatal(err)
	}
	cli.Fail["compute instances create"] = "The zone 'projects/acme-dev/zones/europe-west3-a' does not have enough resources available to fulfill the request."
	got, err := Restore(ctx, cl, c, snap, t.Logf)
	if err == nil || got != nil || !strings.Contains(err.Error(), "does not have enough resources") {
		t.Fatalf("restore: %+v %v", got, err)
	}
	if len(cli.Instances)+len(cli.Addresses) != 0 || len(cli.Snapshots) != 1 || len(cli.Rules) != 2 {
		t.Errorf("after a failed restore: %d VMs, %d addresses, %d snapshots, %d rules", len(cli.Instances), len(cli.Addresses), len(cli.Snapshots), len(cli.Rules))
	}
	// A machine type the zone does not have is said with the config key
	// to change, before anything is reserved.
	delete(cli.Fail, "compute instances create")
	c.Target.GCP.MachineType = "e9-none-1"
	from := len(cli.Calls())
	if _, err := Restore(ctx, cl, c, snap, t.Logf); err == nil || !strings.Contains(err.Error(), "target.gcp.machine_type") || len(changes(cli, from)) != 0 {
		t.Errorf("restore with a type that does not exist: %v, %q", err, changes(cli, from))
	}
	// An address a park could not release is used again.
	c.Target.GCP.MachineType = "e2-small"
	cli.Addresses["europe-west3/keelbox-acme"] = &gcptest.Address{Name: "keelbox-acme", Region: "europe-west3", IP: "198.51.100.99",
		Description: "Managed by keelbox: the address of box acme. Deleted by keelbox destroy."}
	got, err = Restore(ctx, cl, c, snap, t.Logf)
	if err != nil || got.PublicIPv4() != "198.51.100.99" || got.TypeName() != "e2-small" {
		t.Errorf("restore on the address that was left: %+v %v", got, err)
	}
}

func TestSnapshotName(t *testing.T) {
	at := time.Date(2026, 11, 14, 9, 5, 7, 0, time.UTC)
	if got := SnapshotName("acme", at); got != "keelbox-acme-20261114-090507" {
		t.Errorf("name %q", got)
	}
	// Google Cloud allows 63 characters; a box name may have 49.
	long := strings.Repeat("a", 46) + "-bc"
	if got := SnapshotName(long, at); len(got) != 63 || !strings.HasSuffix(got, "-20261114-090507") || strings.Contains(got, "--") {
		t.Errorf("name %q (%d)", got, len(got))
	}
}
