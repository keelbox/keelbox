package gcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// Resize, park and resume (docs/adr/0071-resize-park-resume.md). A VM's
// machine type changes while it is stopped, with its disk and its reserved
// address left as they are. A parked box is a snapshot of its boot disk,
// labelled like the VM plus provider.LabelParked, and its two firewall
// rules: no VM, no disk and no reserved address, because Google Cloud
// bills a disk and an unused address whether a VM runs or not. Every call
// here that changes or deletes a VM or a snapshot first checks keelbox's
// labels for the box.

// How long the waits below last; tests shorten them.
var (
	// stopTimeout is how long a VM that is already stopping gets to stop.
	stopTimeout = 10 * time.Minute
	// slowStop is the time a stop takes when Google Cloud did not wait for
	// the operating system any longer. A VM whose system shuts down by
	// itself is stopped in about a minute; one that does not is switched
	// off, which took two and a quarter minutes when it was measured.
	slowStop = 2 * time.Minute
	// snapshotTimeout bounds one snapshot. A 20 GB disk with a few GB used
	// takes about a minute; a full large one much longer.
	snapshotTimeout = 60 * time.Minute
)

// own returns the box's VM as Google Cloud holds it now. It refuses a VM
// that is not named after the box and labelled as keelbox's, on the
// record the caller holds and on the one just read, and one that is not
// the VM the caller holds.
func own(ctx context.Context, cl *Client, box string, in *Instance) (*Instance, error) {
	if !in.Managed(box) {
		return nil, fmt.Errorf("refusing to change VM %s: not named %s and labelled %s=true and %s=%s", in.Name, box, provider.LabelManaged, provider.LabelBox, box)
	}
	cur, err := Find(ctx, cl, box)
	if err != nil {
		return nil, err
	}
	if in.ID != "" && cur.ID != in.ID {
		return nil, fmt.Errorf("the VM %s is not the one this command found first (id %s, it was %s); run the command again", box, cur.ID, in.ID)
	}
	return cur, nil
}

// waitStatus polls the VM until it reports status, or the time is up.
func (c *Client) waitStatus(ctx context.Context, box string, in *Instance, status string, timeout time.Duration) (*Instance, error) {
	deadline := time.Now().Add(timeout)
	for {
		cur, err := own(ctx, c, box, in)
		if err != nil {
			return nil, err
		}
		if cur.Status == status {
			return cur, nil
		}
		if time.Now().After(deadline) {
			return cur, fmt.Errorf("VM %s is %s, not %s, after %s", cur.Name, cur.Status, status, timeout.Round(time.Second))
		}
		if err := wait(ctx, poll); err != nil {
			return cur, err
		}
	}
}

// Stop shuts the box's VM down and returns once Google Cloud reports it
// TERMINATED. Google asks the operating system to shut down, so services
// stop in order and the disk is written out, and switches the VM off
// itself when the system has not finished in time. It does not report
// which of the two happened, so logf says when the stop took as long as
// a switch-off does. A VM already stopped is left as it is.
func Stop(ctx context.Context, cl *Client, box string, in *Instance, logf func(string, ...any)) error {
	cur, err := own(ctx, cl, box, in)
	if err != nil {
		return err
	}
	if cur.Stopped() {
		return nil
	}
	start := time.Now()
	if cur.Status != "STOPPING" {
		// gcloud waits until the VM is stopped.
		if err := cl.compute(ctx, nil, "instances", "stop", cur.Name, "--zone", cur.ZoneName()); err != nil {
			return err
		}
	}
	if _, err := cl.waitStatus(ctx, box, cur, "TERMINATED", stopTimeout); err != nil {
		return err
	}
	if took := time.Since(start); took >= slowStop {
		logf("VM %s took %s to stop: Google Cloud switches a VM off when its operating system has not shut down in time, and does not say when it did, so something on %s may not have stopped in order",
			cur.Name, took.Round(time.Second), box)
	}
	return nil
}

// Start powers the box's VM on and returns it once it runs and has its
// address, which is the one it had: a reserved address stays with a
// stopped VM. A VM already running is returned as it is.
func Start(ctx context.Context, cl *Client, box string, in *Instance) (*Instance, error) {
	deadline := time.Now().Add(stopTimeout)
	started := false
	for {
		cur, err := own(ctx, cl, box, in)
		if err != nil {
			return nil, err
		}
		switch {
		case cur.Status == "RUNNING" || cur.Status == "PROVISIONING" || cur.Status == "STAGING":
			return cl.waitRunning(ctx, cur)
		case cur.Stopped() && !started:
			// gcloud waits until the VM runs.
			if err := cl.compute(ctx, nil, "instances", "start", cur.Name, "--zone", cur.ZoneName()); err != nil {
				return nil, err
			}
			started = true
			continue
		case cur.Status == "SUSPENDED" || cur.Status == "SUSPENDING":
			return nil, fmt.Errorf("VM %s is %s, which keelbox did not do; resume it: gcloud compute instances resume %s --zone %s%s", cur.Name, cur.Status, cur.Name, cur.ZoneName(), cl.projectFlag())
		}
		// Stopping, or being repaired: it can be started once that is over.
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("VM %s is still %s after %s, so it does not run", cur.Name, cur.Status, stopTimeout.Round(time.Second))
		}
		if err := wait(ctx, poll); err != nil {
			return nil, err
		}
	}
}

// ResizeOffers lists the machine types a box's VM can change to and back
// from, smallest first, its own included: the ones keelbox up lists
// (Offers), and, for a VM of another family, that family up to twice the
// VM's vCPUs. All run the x86 image the box booted from, and none touches
// the disk, so the change works in both directions. A current type the
// zone does not list (a custom one) is still in the list.
func ResizeOffers(types []MachineType, current string) []Offer {
	out := Offers(types)
	listed := map[string]bool{}
	for _, o := range out {
		listed[o.Type.Name] = true
	}
	if !listed[current] {
		var cur *MachineType
		for i := range types {
			if types[i].Name == current {
				cur = &types[i]
			}
		}
		family, limit := current, 8
		if i := strings.LastIndex(current, "-"); i > 0 {
			family = current[:i+1]
		}
		if cur != nil && cur.GuestCPUs*2 > limit {
			limit = cur.GuestCPUs * 2
		}
		for _, t := range types {
			if t.Name != current && !listed[t.Name] && strings.HasPrefix(t.Name, family) && t.usable() && t.GuestCPUs <= limit {
				out = append(out, Offer{Type: t})
			}
		}
		if cur == nil {
			cur = &MachineType{Name: current}
		}
		out = append(out, Offer{Type: *cur})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Type, out[j].Type
		if a.MemoryMB != b.MemoryMB {
			return a.MemoryMB < b.MemoryMB
		}
		if a.GuestCPUs != b.GuestCPUs {
			return a.GuestCPUs < b.GuestCPUs
		}
		return a.Name < b.Name
	})
	return out
}

// Resize changes the machine type of the box's VM, which must be stopped,
// and returns the VM, still stopped. The disk and the address are not
// touched. When it fails the VM still has its old type.
func Resize(ctx context.Context, cl *Client, box string, in *Instance, machineType string) (*Instance, error) {
	cur, err := own(ctx, cl, box, in)
	if err != nil {
		return nil, err
	}
	if !cur.Stopped() {
		return nil, fmt.Errorf("VM %s is %s; its machine type changes only while it is stopped", cur.Name, cur.Status)
	}
	if cur.TypeName() == machineType {
		return cur, nil
	}
	if err := cl.compute(ctx, nil, "instances", "set-machine-type", cur.Name, "--zone", cur.ZoneName(), "--machine-type", machineType); err != nil {
		return nil, err
	}
	now, err := own(ctx, cl, box, cur)
	if err != nil {
		return nil, err
	}
	if now.TypeName() != machineType {
		return nil, fmt.Errorf("VM %s is still %s after gcloud changed it to %s", now.Name, now.TypeName(), machineType)
	}
	return now, nil
}

// Snapshot is a disk snapshot, as gcloud prints it.
type Snapshot struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Status            string            `json:"status"` // CREATING, UPLOADING, READY, FAILED or DELETING
	Labels            map[string]string `json:"labels"`
	CreationTimestamp string            `json:"creationTimestamp"`
	DiskSizeGb        string            `json:"diskSizeGb"`
	StorageBytes      string            `json:"storageBytes"`
	SourceDisk        string            `json:"sourceDisk"` // a URL
	StorageLocations  []string          `json:"storageLocations"`
}

// Parked reports whether the snapshot carries the labels of the box's
// parked snapshot.
func (s *Snapshot) Parked(box string) bool { return provider.Parked(s.Labels, box) }

// Created is when the snapshot was taken, or the zero time when Google
// Cloud does not say.
func (s *Snapshot) Created() time.Time {
	t, err := time.Parse(time.RFC3339, s.CreationTimestamp)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Describe says what the snapshot holds in a few words: what is stored,
// which is what is billed, and the size of the disk it is of.
func (s *Snapshot) Describe() string {
	disk := s.DiskSizeGb + " GB disk"
	if b, err := strconv.ParseFloat(s.StorageBytes, 64); err == nil && b > 0 {
		return fmt.Sprintf("%.1f GB stored of a %s", b/(1<<30), disk)
	}
	return "of a " + disk
}

// SnapshotName is the name of a parked snapshot of the box taken at t.
// The labels say whose it is; the name only has to be new in the project
// and within the 63 characters Google Cloud allows.
func SnapshotName(box string, t time.Time) string {
	stamp := t.UTC().Format("20060102-150405")
	head := "keelbox-" + box
	if over := len(head) + 1 + len(stamp) - 63; over > 0 {
		head = strings.TrimRight(head[:len(head)-over], "-")
	}
	return head + "-" + stamp
}

// Snapshots lists every snapshot with the box's parked labels, whatever
// its state.
func Snapshots(ctx context.Context, cl *Client, box string) ([]Snapshot, error) {
	all, err := cl.SnapshotsWhere(ctx, "labels."+provider.LabelBox+"="+box)
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, s := range all {
		if s.Parked(box) {
			out = append(out, s)
		}
	}
	return out, nil
}

// SnapshotsWhere lists the project's snapshots that match a gcloud filter
// expression.
func (c *Client) SnapshotsWhere(ctx context.Context, filter string) ([]Snapshot, error) {
	var all []Snapshot
	return all, c.compute(ctx, &all, "snapshots", "list", "--filter", filter)
}

// snapshotNamed returns the snapshot of that name, or nil.
func (c *Client) snapshotNamed(ctx context.Context, name string) (*Snapshot, error) {
	var s Snapshot
	if err := c.compute(ctx, &s, "snapshots", "describe", name); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// Parked returns the box's parked snapshot: the newest one with its
// parked labels that Google Cloud reports READY, or nil when the box is
// not parked.
func Parked(ctx context.Context, cl *Client, box string) (*Snapshot, error) {
	all, err := Snapshots(ctx, cl, box)
	if err != nil {
		return nil, err
	}
	var newest *Snapshot
	for i := range all {
		s := &all[i]
		if s.Status != "READY" || s.Created().IsZero() {
			continue
		}
		if newest == nil || s.Created().After(newest.Created()) {
			newest = s
		}
	}
	return newest, nil
}

// OtherDisks names the disks attached to the VM beside its boot disk.
// keelbox attaches none. park saves the boot disk only, and deleting the
// VM deletes or strands the others, so park refuses a VM that has any.
func (i *Instance) OtherDisks() []string {
	var out []string
	for _, d := range i.Disks {
		if !d.Boot {
			out = append(out, d.Name())
		}
	}
	return out
}

// Parkable checks, before anything changes, that parking the box's VM
// would keep everything on it: it has a boot disk and no other disk.
func Parkable(cl *Client, box string, in *Instance) error {
	if in.BootDisk() == nil {
		return fmt.Errorf("the record of VM %s names no boot disk, so there is nothing keelbox could keep of it", in.Name)
	}
	if others := in.OtherDisks(); len(others) > 0 {
		return fmt.Errorf("VM %s has another disk attached (%s). keelbox park keeps the boot disk only, and deleting the VM would delete the other or leave it billed. "+
			"Detach it: gcloud compute instances detach-disk %s --disk %s --zone %s%s; then: keelbox park %s",
			in.Name, strings.Join(others, ", "), in.Name, others[0], in.ZoneName(), cl.projectFlag(), box)
	}
	return nil
}

func labelPairs(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + labels[k]
	}
	return strings.Join(pairs, ",")
}

// TakeSnapshot saves the boot disk of the box's VM, which must be
// stopped, as the box's parked snapshot, and returns it only once Google
// Cloud reports it READY: complete, and usable to create a disk from.
// When that does not happen, also because ctx was cancelled, the
// unfinished snapshot is deleted and the error returned. The VM is not
// changed either way.
func TakeSnapshot(ctx context.Context, cl *Client, c *config.Config, in *Instance) (*Snapshot, error) {
	cur, err := own(ctx, cl, c.Box, in)
	if err != nil {
		return nil, err
	}
	if !cur.Stopped() {
		return nil, fmt.Errorf("VM %s is %s; a snapshot is taken only of a VM that is stopped", cur.Name, cur.Status)
	}
	if err := Parkable(cl, c.Box, cur); err != nil {
		return nil, err
	}
	disk := cur.BootDisk()
	name := SnapshotName(c.Box, time.Now())
	// The name is new: one that exists already is not this call's to
	// delete when the create is refused.
	if old, err := cl.snapshotNamed(ctx, name); err != nil {
		return nil, err
	} else if old != nil {
		return nil, fmt.Errorf("a snapshot named %s exists already; run the command again", name)
	}
	// --async: gcloud returns once the snapshot is ordered, and the wait
	// below is keelbox's own, so Ctrl-C ends it at once.
	err = cl.compute(ctx, nil, "snapshots", "create", name, "--source-disk", disk.Name(), "--source-disk-zone", cur.ZoneName(),
		"--labels", labelPairs(provider.ParkedLabels(c.Box, c.Target.GCP.Labels)),
		"--description", "Managed by keelbox: the disk of the parked box "+c.Box+". keelbox resume creates its VM from it and then deletes it.",
		"--async")
	// From here an unfinished snapshot may exist: on any failure it is
	// deleted, also when ctx was cancelled.
	fail := func(err error) (*Snapshot, error) {
		held := context.WithoutCancel(ctx)
		by := fmt.Sprintf("delete it: gcloud compute snapshots delete %s%s", name, cl.projectFlag())
		s, derr := cl.snapshotNamed(held, name)
		if derr == nil && s != nil {
			derr = DeleteSnapshot(held, cl, c.Box, s)
		}
		if derr != nil {
			return nil, fmt.Errorf("%w (and the unfinished snapshot %s could not be deleted: %v; %s)", err, name, derr, by)
		}
		return nil, err
	}
	if err != nil {
		return fail(err)
	}
	deadline := time.Now().Add(snapshotTimeout)
	for {
		got, err := cl.snapshotNamed(ctx, name)
		switch {
		case err != nil:
			return fail(fmt.Errorf("snapshot %s: %w", name, err))
		case got == nil:
			return fail(fmt.Errorf("snapshot %s is gone before it was complete", name))
		case got.Status == "READY":
			if !got.Parked(c.Box) || got.Created().IsZero() || base(got.SourceDisk) != disk.Name() {
				return fail(fmt.Errorf("snapshot %s is not the parked snapshot keelbox asked for", name))
			}
			return got, nil
		case got.Status == "FAILED":
			return fail(fmt.Errorf("snapshot %s: Google Cloud reports it as FAILED", name))
		case time.Now().After(deadline):
			return fail(fmt.Errorf("snapshot %s is still %s after %s", name, got.Status, snapshotTimeout.Round(time.Second)))
		}
		if err := wait(ctx, poll); err != nil {
			return fail(err)
		}
	}
}

// DeleteSnapshot deletes a parked snapshot of the box. It refuses one
// that does not carry the box's parked labels. gcloud waits until it is
// gone, also for one that is still being made.
func DeleteSnapshot(ctx context.Context, cl *Client, box string, s *Snapshot) error {
	if !s.Parked(box) {
		return fmt.Errorf("refusing to delete snapshot %s: not labelled %s=true, %s=%s and %s=true", s.Name, provider.LabelManaged, provider.LabelBox, box, provider.LabelParked)
	}
	if err := cl.compute(ctx, nil, "snapshots", "delete", s.Name); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

// SweepSnapshots deletes every snapshot labelled as the box's parked
// snapshot, whatever its state, and returns the ones deleted: what is
// left of a box that is being destroyed.
func SweepSnapshots(ctx context.Context, cl *Client, box string) ([]Snapshot, error) {
	all, err := Snapshots(ctx, cl, box)
	if err != nil {
		return nil, err
	}
	var done []Snapshot
	for i := range all {
		if err := DeleteSnapshot(ctx, cl, box, &all[i]); err != nil {
			return done, err
		}
		done = append(done, all[i])
	}
	return done, nil
}

// Release deletes the box's VM with its boot disk and releases the box's
// reserved address: Google Cloud bills a disk and an unused address, and
// a parked box is to cost its snapshot only. The firewall rules stay for
// the VM resume creates. It returns what it did. The VM being gone is
// what counts: an address that could not be released is said in the list,
// with the command that releases it, and is not an error.
func Release(ctx context.Context, cl *Client, box string, in *Instance) ([]string, error) {
	cur, err := own(ctx, cl, box, in)
	if err != nil {
		return nil, err
	}
	if err := Parkable(cl, box, cur); err != nil {
		return nil, err
	}
	// The boot disk goes with the VM. Create asks for that; make sure of
	// it, or the disk would stay, billed, with nothing that names it.
	if d := cur.BootDisk(); !d.AutoDelete {
		if err := cl.compute(ctx, nil, "instances", "set-disk-auto-delete", cur.Name, "--zone", cur.ZoneName(), "--disk", d.Name(), "--auto-delete"); err != nil {
			return nil, fmt.Errorf("the boot disk %s would outlive VM %s: %w", d.Name(), cur.Name, err)
		}
	}
	// gcloud waits until the VM is gone.
	if err := cl.compute(ctx, nil, "instances", "delete", cur.Name, "--zone", cur.ZoneName()); err != nil {
		return nil, err
	}
	done := []string{fmt.Sprintf("deleted VM %s (%s) and its disk", cur.Name, cur.ZoneName())}
	a, err := ReleaseAddress(ctx, cl, box)
	if a != nil {
		done = append(done, fmt.Sprintf("released address %s (%s)", a.Name, a.Address))
	}
	if err != nil {
		done = append(done, fmt.Sprintf("warning: the address %s was not released (%v); Google Cloud bills it while nothing uses it. keelbox resume %s uses it again; or release it: gcloud compute addresses delete %s --region %s%s",
			AddressName(box), err, box, AddressName(box), RegionOf(cur.ZoneName()), cl.projectFlag()))
	}
	return done, nil
}

// ResumeUserData is the cloud-config the VM keelbox resume creates boots
// with: keep the SSH host keys and the hostname on the disk, and create
// no account (Ubuntu's image would otherwise create "ubuntu" with
// passwordless sudo, which a box created by keelbox never had). Google's
// guest agent leaves the host keys alone because the disk still holds the
// setting Create wrote (guestAgentConfig).
func ResumeUserData(box string) string { return provider.ResumeUserData(box) }

// restoreAddress reserves the box's address in the region for the VM
// resume creates, or returns the one a park that could not release it
// left there. reserved is set when this call made it.
func restoreAddress(ctx context.Context, cl *Client, box, region string, logf func(string, ...any)) (a *Address, reserved bool, err error) {
	old, err := FindAddress(ctx, cl, box)
	if err != nil {
		return nil, false, err
	}
	switch {
	case old != nil && len(old.Users) > 0:
		return nil, false, fmt.Errorf("the address %s (%s) is in use by %s, which is not the box's VM; free it there, or release it: gcloud compute addresses delete %s --region %s%s, then: keelbox resume %s",
			old.Name, old.Address, base(old.Users[0]), old.Name, base(old.Region), cl.projectFlag(), box)
	case old != nil && base(old.Region) != region:
		// Left in the region the box was in before its zone changed.
		if _, err := ReleaseAddress(ctx, cl, box); err != nil {
			return nil, false, err
		}
		logf("released address %s (%s) in %s", old.Name, old.Address, base(old.Region))
	}
	return ensureAddress(ctx, cl, box, region)
}

// Restore creates the box's VM again from its parked snapshot: the name,
// machine type, zone, labels and network tag of the config, a boot disk
// made from the snapshot at the size it had, a newly reserved address,
// the same login settings as Create (no project SSH keys, no OS Login, no
// key in the metadata, no service account), and user data that keeps
// what is on the disk and creates no account (ResumeUserData). The box's
// firewall rules apply to the network tag, so they are in force from the
// moment the VM exists; they are kept from before the park and created
// only if missing. It returns the VM once it runs and has its address,
// and fails when a VM of that name exists. A VM returned with an error
// exists.
func Restore(ctx context.Context, cl *Client, c *config.Config, s *Snapshot, logf func(string, ...any)) (*Instance, error) {
	g := c.Target.GCP
	if cl.Project == "" {
		cl.Project = g.Project
	}
	if !s.Parked(c.Box) || s.Status != "READY" {
		return nil, fmt.Errorf("refusing to create %s from snapshot %s: not its parked snapshot, complete", c.Box, s.Name)
	}
	if _, err := cl.Preflight(ctx, g.Zone, g.MachineType); err != nil {
		return nil, err
	}
	named, err := cl.InstancesNamed(ctx, c.Box)
	if err != nil {
		return nil, err
	}
	if len(named) > 0 {
		return nil, fmt.Errorf("a VM named %s already exists in %s", c.Box, named[0].ZoneName())
	}
	// As at create: the rules exist before the VM. Rules that are there
	// keep what they admit, so a box that had closed public SSH stays
	// closed.
	_, changes, err := EnsureFirewall(ctx, cl, c.Box, nil)
	for _, ch := range changes {
		logf("%s", ch)
	}
	if err != nil {
		return nil, fmt.Errorf("cloud firewall: %w", err)
	}
	addr, reserved, err := restoreAddress(ctx, cl, c.Box, RegionOf(g.Zone), logf)
	if err != nil {
		return nil, fmt.Errorf("reserve the address: %w", err)
	}
	if reserved {
		logf("reserved address %s (%s) in %s", addr.Name, addr.Address, RegionOf(g.Zone))
	}
	// Without a VM the address would be billed for nothing until the
	// next attempt: give back the one this call reserved.
	giveBack := func(err error) error {
		if !reserved {
			return err
		}
		if _, rerr := ReleaseAddress(context.WithoutCancel(ctx), cl, c.Box); rerr != nil {
			return fmt.Errorf("%w (and the address %s reserved for it was not released: %v; keelbox resume %s uses it again)", err, addr.Name, rerr, c.Box)
		}
		return err
	}
	path, err := writeUserData(ResumeUserData(c.Box))
	if err != nil {
		return nil, giveBack(fmt.Errorf("write the user data file: %w", err))
	}
	defer func() { _ = os.Remove(path) }()

	logf("creating %s from snapshot %s: %s in %s, %s GB disk", c.Box, s.Name, g.MachineType, g.Zone, s.DiskSizeGb)
	var made []Instance
	err = cl.compute(ctx, &made, "instances", "create", c.Box, "--zone", g.Zone, "--machine-type", g.MachineType,
		"--source-snapshot", s.Name, "--boot-disk-type", DiskType,
		"--labels", labelPairs(provider.ManagedLabels(c.Box, g.Labels)), "--tags", BoxTag(c.Box),
		"--network", Network, "--address", addr.Address,
		// As at create: no key but the ones on the disk lets anyone in.
		"--metadata", "block-project-ssh-keys=true,enable-oslogin=false",
		"--metadata-from-file", "user-data="+path,
		"--no-service-account", "--no-scopes")
	if err != nil {
		if said(err, "requireOsLogin", "OS Login") {
			err = fmt.Errorf("%w. The organisation requires OS Login on every VM (constraints/compute.requireOsLogin), and a box logs in with the admin's own SSH key", err)
		}
		// gcloud may fail after the VM was ordered: say whether it exists.
		now, ferr := cl.InstancesNamed(context.WithoutCancel(ctx), c.Box)
		switch {
		case ferr == nil && len(now) > 0:
			return &now[0], err
		case ferr == nil:
			return nil, giveBack(err)
		}
		return nil, err
	}
	if len(made) != 1 {
		return &Instance{Name: c.Box, Zone: g.Zone}, errors.New("gcloud compute instances create printed " + strconv.Itoa(len(made)) + " VMs, not one")
	}
	return cl.waitRunning(ctx, &made[0])
}
