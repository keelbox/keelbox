package aws

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// Resize, park and resume on Lightsail (docs/adr/0071-resize-park-resume.md).
//
// A parked box is an instance snapshot tagged like the instance plus
// provider.LabelParked, and no instance; park also releases the static IP,
// which Lightsail bills while it is attached to nothing.
//
// Lightsail cannot change an instance's bundle, and an instance's name
// cannot change. A resize is therefore a snapshot of the disk, the old
// instance deleted, and an instance of the same name created from the
// snapshot on the new bundle, with the box's static IP attached again. A
// snapshot only fits a bundle at least as large as the one it came from,
// so there is no way back down.
//
// Every call here that changes or deletes an instance or a snapshot first
// checks keelbox's tags for the box.

// Tags keelbox puts on snapshots besides the box's own.
const (
	// TagResize marks the snapshot a resize takes.
	TagResize = "keelbox-resize"
	// TagPublicSSH records on a snapshot whether the instance's firewall
	// admitted SSH ("open" or "closed"): an instance's firewall goes with
	// the instance, so the one created from the snapshot is given the same.
	TagPublicSSH = "keelbox-public-ssh"
)

// How long the waits below last; tests shorten them.
var (
	// stopTimeout is how long an instance gets to shut down by itself
	// before Lightsail is asked to force it. systemd gives each unit 90
	// seconds to stop.
	stopTimeout = 5 * time.Minute
	// snapshotTimeout bounds one snapshot.
	snapshotTimeout = 45 * time.Minute
)

// Time is a timestamp as the AWS CLI prints it: an ISO 8601 string, or
// seconds since 1970 when the user's cli_timestamp_format is "wire".
type Time struct{ time.Time }

// UnmarshalJSON implements json.Unmarshaler.
func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec, frac := math.Modf(f)
		t.Time = time.Unix(int64(sec), int64(frac*1e9)).UTC()
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02T15:04:05.999999999"} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v.UTC()
			return nil
		}
	}
	return fmt.Errorf("timestamp %q is in no form the AWS CLI prints", s)
}

// Snapshot is a Lightsail instance snapshot.
type Snapshot struct {
	Name             string `json:"name"`
	State            string `json:"state"`
	CreatedAt        Time   `json:"createdAt"`
	Tags             []Tag  `json:"tags"`
	SizeInGb         int    `json:"sizeInGb"`
	FromInstanceName string `json:"fromInstanceName"`
	FromBundleID     string `json:"fromBundleId"`
}

// Labels are the snapshot's tags as a map.
func (s *Snapshot) Labels() map[string]string { return labelsOf(s.Tags) }

// Parked reports whether the snapshot is a parked snapshot of box.
func (s *Snapshot) Parked(box string) bool { return provider.Parked(s.Labels(), box) }

// ofBox reports whether keelbox made the snapshot for box, at a park or at
// a resize.
func (s *Snapshot) ofBox(box string) bool {
	l := s.Labels()
	return provider.Managed(l, box) && (l[provider.LabelParked] == "true" || l[TagResize] == "true")
}

// Snapshot returns the instance snapshot of that name in the region, or nil.
func (c *Client) Snapshot(ctx context.Context, name string) (*Snapshot, error) {
	var r struct {
		Snapshot *Snapshot `json:"instanceSnapshot"`
	}
	if err := c.lightsail(ctx, &r, "get-instance-snapshot", "--instance-snapshot-name", name); err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.Snapshot, nil
}

// Snapshots lists the region's instance snapshots.
func (c *Client) Snapshots(ctx context.Context) ([]Snapshot, error) {
	var r struct {
		Snapshots []Snapshot `json:"instanceSnapshots"`
	}
	return r.Snapshots, c.lightsail(ctx, &r, "get-instance-snapshots")
}

// deleteSnapshot deletes the snapshot of that name; one that is gone is
// not an error. Lightsail may refuse while the snapshot is being taken, so
// it tries for a while. The caller has checked that it is keelbox's.
func (c *Client) deleteSnapshot(ctx context.Context, name string) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = c.lightsail(ctx, nil, "delete-instance-snapshot", "--instance-snapshot-name", name); err == nil || notFound(err) {
			return nil
		}
		if werr := wait(ctx, poll); werr != nil {
			return err
		}
	}
	return err
}

func (c *Client) deleteSnapshotFix(name string) string {
	return fmt.Sprintf("aws lightsail delete-instance-snapshot --instance-snapshot-name %s --region %s%s", name, c.Region, c.profileFlag())
}

// own returns the instance as Lightsail has it now, and refuses one
// without keelbox's tags for the box.
func (c *Client) own(ctx context.Context, box, name string) (*Instance, error) {
	in, err := c.Instance(ctx, name)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, fmt.Errorf("no Lightsail instance named %s in %s", name, c.Region)
	}
	if !in.Managed(box) {
		return nil, fmt.Errorf("refusing to change instance %s: not tagged %s=true and %s=%s", in.Name, provider.LabelManaged, provider.LabelBox, box)
	}
	return in, nil
}

// waitState polls the instance until it reports state, or the time is up.
func (c *Client) waitState(ctx context.Context, name, state string, timeout time.Duration) (*Instance, error) {
	deadline := time.Now().Add(timeout)
	for {
		in, err := c.Instance(ctx, name)
		if err != nil {
			return nil, err
		}
		if in == nil {
			return nil, fmt.Errorf("instance %s no longer exists", name)
		}
		if in.State.Name == state {
			return in, nil
		}
		if time.Now().After(deadline) {
			return in, fmt.Errorf("instance %s is %s, not %s, after %s", name, in.State.Name, state, timeout.Round(time.Second))
		}
		if err := wait(ctx, poll); err != nil {
			return in, err
		}
	}
}

// Stop shuts the box's instance down and returns once Lightsail reports it
// stopped. Lightsail asks the operating system, so services stop in order
// and the disk is written out; an instance still running after stopTimeout
// is forced, and logf says so. An instance already stopped is left as it
// is.
func Stop(ctx context.Context, cl *Client, box string, in *Instance, logf func(string, ...any)) error {
	cur, err := cl.own(ctx, box, in.Name)
	if err != nil {
		return err
	}
	switch cur.State.Name {
	case "stopped":
		return nil
	case "stopping":
	default:
		if err := cl.lightsail(ctx, nil, "stop-instance", "--instance-name", cur.Name); err != nil {
			return err
		}
	}
	if _, err := cl.waitState(ctx, cur.Name, "stopped", stopTimeout); err == nil {
		return nil
	} else if ctx.Err() != nil {
		return err
	}
	logf("instance %s did not shut down within %s; forcing it to stop", cur.Name, stopTimeout.Round(time.Second))
	if err := cl.lightsail(ctx, nil, "stop-instance", "--instance-name", cur.Name, "--force"); err != nil {
		return err
	}
	_, err = cl.waitState(ctx, cur.Name, "stopped", 2*time.Minute)
	return err
}

// Start powers the box's instance on and returns it once it runs and has
// its address. The box's static IP is attached again when it is attached
// to nothing, as an interrupted resume or resize leaves it. An instance
// already running is returned as it is.
func Start(ctx context.Context, cl *Client, box string, in *Instance) (*Instance, error) {
	cur, err := cl.own(ctx, box, in.Name)
	if err != nil {
		return nil, err
	}
	if cur.State.Name == "stopping" {
		if cur, err = cl.waitState(ctx, cur.Name, "stopped", stopTimeout); err != nil {
			return nil, err
		}
	}
	if cur.State.Name == "stopped" {
		// Lightsail refuses a start for a moment after a stop or a
		// snapshot, while that operation is still being closed.
		for i := 0; ; i++ {
			if err = cl.lightsail(ctx, nil, "start-instance", "--instance-name", cur.Name); err == nil || i == 5 {
				break
			}
			if werr := wait(ctx, 5*poll); werr != nil {
				return nil, werr
			}
		}
		if err != nil {
			return nil, err
		}
	}
	run, err := cl.waitRunning(ctx, cur.Name)
	if err != nil {
		return nil, err
	}
	ip, err := cl.StaticIP(ctx, StaticIPName(box))
	if err != nil || ip == nil || ip.IsAttached {
		return run, err
	}
	run, _, err = cl.attachStaticIP(ctx, box, cur.Name)
	return run, err
}

// ResizeOffers lists the bundles the instance can change to, cheapest
// first, its own included: usable ones whose disk, memory and vCPUs are
// all at least the instance's. Lightsail creates an instance from a
// snapshot only on a plan at least as large as the one the snapshot came
// from, so nothing smaller is listed. bundles should include the inactive
// ones, so that the instance's own is found.
func ResizeOffers(bundles []Bundle, in *Instance) []Offer {
	var own *Bundle
	for i := range bundles {
		if bundles[i].BundleID == in.BundleID {
			own = &bundles[i]
		}
	}
	if own == nil {
		return nil
	}
	disk := max(own.DiskSizeInGb, in.DiskGB())
	var out []Offer
	for _, b := range bundles {
		if b.BundleID != own.BundleID && (!b.Usable() || b.DiskSizeInGb < disk || b.RAMSizeInGb < own.RAMSizeInGb || b.CPUCount < own.CPUCount) {
			continue
		}
		out = append(out, Offer{Bundle: b})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bundle.Price != out[j].Bundle.Price {
			return out[i].Bundle.Price < out[j].Bundle.Price
		}
		return out[i].Bundle.BundleID < out[j].Bundle.BundleID
	})
	return out
}

// AllBundles lists the region's bundles, those Lightsail no longer offers
// for new instances included.
func (c *Client) AllBundles(ctx context.Context) ([]Bundle, error) {
	var r struct {
		Bundles []Bundle `json:"bundles"`
	}
	return r.Bundles, c.lightsail(ctx, &r, "get-bundles", "--include-inactive")
}

// snapshotName names a snapshot of the box: "keelbox-acme-parked-20261114T101500Z".
func snapshotName(box, kind string) string {
	return "keelbox-" + box + "-" + kind + "-" + time.Now().UTC().Format("20060102T150405Z")
}

// sshTag is the TagPublicSSH value for an instance's firewall rules.
func sshTag(ports []Port) string {
	if HasPublicSSH(ports) {
		return "open"
	}
	return "closed"
}

// takeSnapshot saves the disk of the box's instance, which must be
// stopped, under name with labels, and returns the snapshot only once
// Lightsail reports it available: complete, and usable to create an
// instance from. When that does not happen, also when ctx is cancelled,
// the unfinished snapshot is deleted and the error returned.
func (c *Client) takeSnapshot(ctx context.Context, box string, in *Instance, name string, labels map[string]string) (*Snapshot, error) {
	cur, err := c.own(ctx, box, in.Name)
	if err != nil {
		return nil, err
	}
	if cur.State.Name != "stopped" {
		return nil, fmt.Errorf("instance %s is %s; a snapshot is taken only of an instance that is stopped", cur.Name, cur.State.Name)
	}
	ports, err := c.Ports(ctx, cur.Name)
	if err != nil {
		return nil, err
	}
	labels[TagPublicSSH] = sshTag(ports)
	// From the create call on a snapshot of that name may exist: on any
	// failure it is deleted, with a context that is not cancelled.
	fail := func(err error) (*Snapshot, error) {
		if derr := c.deleteSnapshot(context.WithoutCancel(ctx), name); derr != nil {
			return nil, fmt.Errorf("%w (and the unfinished snapshot %s could not be deleted: %v; delete it: %s)", err, name, derr, c.deleteSnapshotFix(name))
		}
		return nil, err
	}
	args := append([]string{"--instance-snapshot-name", name, "--instance-name", cur.Name, "--tags"}, tagArgs(labels)...)
	if err := c.lightsail(ctx, nil, "create-instance-snapshot", args...); err != nil {
		return fail(err)
	}
	deadline := time.Now().Add(snapshotTimeout)
	for missing := 0; ; {
		s, err := c.Snapshot(ctx, name)
		switch {
		case err != nil:
			return fail(fmt.Errorf("snapshot %s: %w", name, err))
		case s == nil:
			// Lightsail may not list a snapshot in its first seconds.
			if missing++; missing > 10 {
				return fail(fmt.Errorf("snapshot %s does not exist after Lightsail accepted it", name))
			}
		case s.State == "available":
			if !provider.Managed(s.Labels(), box) || s.FromInstanceName != cur.Name {
				return fail(fmt.Errorf("snapshot %s is not the snapshot of %s keelbox asked for", name, cur.Name))
			}
			return s, nil
		case s.State != "pending":
			return fail(fmt.Errorf("snapshot %s is in state %s", name, s.State))
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("snapshot %s is not available after %s", name, snapshotTimeout.Round(time.Second)))
		}
		if err := wait(ctx, poll); err != nil {
			return fail(err)
		}
	}
}

// ParkSnapshot saves the disk of the box's instance, which must be
// stopped, as the box's parked snapshot, and returns it only once
// Lightsail reports it available. After an error no snapshot is left.
func ParkSnapshot(ctx context.Context, cl *Client, c *config.Config, in *Instance) (*Snapshot, error) {
	s, err := cl.takeSnapshot(ctx, c.Box, in, snapshotName(c.Box, "parked"), provider.ParkedLabels(c.Box, c.Target.AWS.Tags))
	if err != nil {
		return nil, err
	}
	if !s.Parked(c.Box) {
		_ = cl.deleteSnapshot(context.WithoutCancel(ctx), s.Name)
		return nil, fmt.Errorf("snapshot %s does not carry the tags of a parked snapshot of %s", s.Name, c.Box)
	}
	return s, nil
}

// Parked returns the box's parked snapshot: the newest available snapshot
// tagged as it, or nil when the box is not parked.
func Parked(ctx context.Context, cl *Client, box string) (*Snapshot, error) {
	all, err := cl.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	var newest *Snapshot
	for i := range all {
		s := &all[i]
		if s.State != "available" || !s.Parked(box) {
			continue
		}
		if newest == nil || s.CreatedAt.After(newest.CreatedAt.Time) {
			newest = s
		}
	}
	return newest, nil
}

// DeleteSnapshot deletes a parked snapshot of the box. It refuses a
// snapshot that does not carry the box's parked tags now; one that is gone
// is not an error.
func DeleteSnapshot(ctx context.Context, cl *Client, box, name string) error {
	s, err := cl.Snapshot(ctx, name)
	if err != nil || s == nil {
		return err
	}
	if !s.Parked(box) {
		return fmt.Errorf("refusing to delete snapshot %s: not tagged %s=true, %s=%s and %s=true", name, provider.LabelManaged, provider.LabelBox, box, provider.LabelParked)
	}
	return cl.deleteSnapshot(ctx, name)
}

// SweepSnapshots deletes every snapshot keelbox made for the box, at a
// park or at a resize, whatever its state, and returns their names: what
// is left of a box that is being destroyed.
func SweepSnapshots(ctx context.Context, cl *Client, box string) ([]string, error) {
	all, err := cl.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	var done []string
	for i := range all {
		if !all[i].ofBox(box) {
			continue
		}
		if err := cl.deleteSnapshot(ctx, all[i].Name); err != nil {
			return done, err
		}
		done = append(done, all[i].Name)
	}
	return done, nil
}

// LaunchScript is the user data of an instance created from a snapshot of
// the box. Lightsail runs user data as a shell script and takes no
// cloud-config, so this cannot say what provider.ResumeUserData says on
// the other clouds; the disk says it itself, in the cloud-init file apply
// keeps there (modules.CloudInitKeepFile). The script does nothing: it
// creates no account, adds no key and holds nothing secret.
func LaunchScript(box string) string {
	return "#!/bin/sh\n# keelbox box " + box + ", created from a snapshot of its disk: the disk is the box as it was.\n"
}

// fromSnapshot creates the box's instance from the snapshot on the bundle,
// gives it the firewall rules as soon as Lightsail takes them, waits until
// it runs and attaches the box's static IP, which must exist. When it
// fails it deletes the instance it made, and says so in the error if it
// could not.
func (c *Client) fromSnapshot(ctx context.Context, cfg *config.Config, snap *Snapshot, bundle, zone string, ports []Port, logf func(string, ...any)) (*Instance, error) {
	box := cfg.Box
	args := []string{"--instance-names", box, "--availability-zone", zone, "--instance-snapshot-name", snap.Name,
		"--bundle-id", bundle, "--user-data", LaunchScript(box), "--tags"}
	args = append(args, tagArgs(provider.ManagedLabels(box, cfg.Target.AWS.Tags))...)
	// A name is not free at once after its instance was deleted.
	var err error
	for i := 0; ; i++ {
		if err = c.lightsail(ctx, nil, "create-instances-from-snapshot", args...); err == nil || i == 5 || !strings.Contains(err.Error(), "already in use") {
			break
		}
		if werr := wait(ctx, 5*poll); werr != nil {
			return nil, werr
		}
	}
	if err != nil {
		return nil, err
	}
	undo := func(err error) (*Instance, error) {
		held := context.WithoutCancel(ctx)
		if in, ierr := c.Instance(held, box); ierr != nil || (in != nil && (!in.Managed(box) || c.deleteInstance(held, box) != nil)) {
			return nil, fmt.Errorf("%w (and the instance %s this left could not be deleted)", err, box)
		}
		return nil, err
	}
	// Lightsail opens 22 and 80 on a new instance, also on one created from
	// a snapshot: the rules go on with the first call it accepts.
	set, unread := false, 0
	var in *Instance
	for i := 0; i < 100 && in == nil; i++ {
		if !set {
			if perr := c.PutPorts(ctx, box, ports); perr == nil {
				set = true
				logf("set the firewall of instance %s: %s", box, DescribePorts(ports))
			} else if ctx.Err() != nil {
				return undo(perr)
			}
		}
		cur, err := c.Instance(ctx, box)
		switch {
		case err == nil:
			unread = 0
		case unread < 5 && ctx.Err() == nil:
			// One answer lost is no reason to delete the instance.
			unread++
		default:
			return undo(err)
		}
		if cur != nil && cur.State.Name == "running" && cur.PublicIPAddress != "" {
			in = cur
			break
		}
		if err := wait(ctx, poll); err != nil {
			return undo(err)
		}
	}
	if in == nil {
		return undo(fmt.Errorf("instance %s did not reach the running state", box))
	}
	// Read back once it runs: the rules in force are the box's.
	if have, err := c.Ports(ctx, box); err != nil {
		return undo(fmt.Errorf("instance firewall: %w", err))
	} else if !set || !SamePorts(have, ports) {
		if err := c.PutPorts(ctx, box, ports); err != nil {
			return undo(fmt.Errorf("instance firewall: %w", err))
		}
		logf("set the firewall of instance %s: %s (was: %s)", box, DescribePorts(ports), DescribePorts(have))
	}
	cur, ip, err := c.attachStaticIP(ctx, box, box)
	if err != nil {
		return undo(err)
	}
	logf("attached static IP %s (%s)", ip.Name, ip.IPAddress)
	return cur, nil
}

// portsOf are the firewall rules for an instance created from the
// snapshot: SSH admitted unless the snapshot says the box had closed it.
func portsOf(snap *Snapshot) []Port {
	return PortRules(snap.Labels()[TagPublicSSH] != "closed")
}

// Restore creates the box's instance again from its parked snapshot: the
// name, bundle, zone and tags of the config, the firewall the instance had
// when the snapshot was taken, and a static IP. It returns the instance
// once it runs and has the address, and fails when the box has an
// instance. After an error no instance is left unless the error says so.
//
// A static IP of the box's name that is attached to nothing is used as it
// is: a resize that could not create the instance again leaves the box
// parked with its address. Otherwise one is allocated, and released again
// if the instance cannot be created.
//
// Lightsail starts an instance with ports 22 and 80 open and takes
// firewall rules only once the instance exists, so the box's rules are in
// force from the first call Lightsail accepts and not from the first
// packet. It installs the region's default key pair for the image's
// account; that account stays as the disk has it.
func Restore(ctx context.Context, cl *Client, c *config.Config, snap *Snapshot, logf func(string, ...any)) (*Instance, error) {
	a := c.Target.AWS
	if !snap.Parked(c.Box) {
		return nil, fmt.Errorf("refusing to create %s from snapshot %s: not its parked snapshot", c.Box, snap.Name)
	}
	if in, err := cl.Instance(ctx, c.Box); err != nil {
		return nil, err
	} else if in != nil {
		return nil, fmt.Errorf("a Lightsail instance named %s already exists in %s", c.Box, cl.Region)
	}
	bundles, err := cl.AllBundles(ctx)
	if err != nil {
		return nil, err
	}
	// What the snapshot fits on: a plan at least as large as the one it
	// came from.
	from := Bundle{DiskSizeInGb: snap.SizeInGb}
	for _, b := range bundles {
		if b.BundleID == snap.FromBundleID {
			from = b
			from.DiskSizeInGb = max(b.DiskSizeInGb, snap.SizeInGb)
		}
	}
	var want *Bundle
	var fits []string
	for _, o := range Offers(bundles) {
		b := o.Bundle
		if b.BundleID == a.Bundle {
			want = &b
		}
		if b.DiskSizeInGb >= from.DiskSizeInGb && b.RAMSizeInGb >= from.RAMSizeInGb && b.CPUCount >= from.CPUCount {
			fits = append(fits, b.BundleID)
		}
	}
	another := fmt.Sprintf("set target.aws.bundle to one of: %s, then run keelbox resume %s again", strings.Join(fits, ", "), c.Box)
	switch {
	case want == nil:
		return nil, fmt.Errorf("target.aws.bundle %q is not a Linux bundle with a public IPv4 address in %s; %s", a.Bundle, cl.Region, another)
	case !contains(fits, want.BundleID):
		return nil, fmt.Errorf("bundle %s (%g GB, %d GB disk) is smaller than the %s the parked snapshot was taken on (%d GB disk), and Lightsail creates an instance from a snapshot only on a plan at least as large; %s",
			want.BundleID, want.RAMSizeInGb, want.DiskSizeInGb, snap.FromBundleID, snap.SizeInGb, another)
	}
	zone := a.AvailabilityZone
	if zone == "" {
		if zone, err = cl.firstZone(ctx); err != nil {
			return nil, err
		}
	}
	ipName, allocated := StaticIPName(c.Box), false
	switch ip, err := cl.StaticIP(ctx, ipName); {
	case err != nil:
		return nil, err
	case ip == nil:
		if err := cl.lightsail(ctx, nil, "allocate-static-ip", "--static-ip-name", ipName); err != nil {
			return nil, fmt.Errorf("static IP: %w", err)
		}
		allocated = true
	case ip.IsAttached:
		return nil, fmt.Errorf("the static IP %s (%s) is attached to %s, which is not this box; keelbox leaves it. Detach it if %s should have it: aws lightsail detach-static-ip --static-ip-name %s --region %s%s",
			ipName, ip.IPAddress, ip.AttachedTo, c.Box, ipName, cl.Region, cl.profileFlag())
	default:
		logf("using the static IP %s (%s) the box kept", ipName, ip.IPAddress)
	}
	logf("creating %s from snapshot %s: %s in %s", c.Box, snap.Name, want.BundleID, zone)
	in, err := cl.fromSnapshot(ctx, c, snap, want.BundleID, zone, portsOf(snap), logf)
	if err != nil && allocated {
		// Released again, unless an instance is left that Start would give
		// it to.
		held := context.WithoutCancel(ctx)
		if left, lerr := cl.Instance(held, c.Box); lerr == nil && left == nil {
			if rerr := cl.lightsail(held, nil, "release-static-ip", "--static-ip-name", ipName); rerr != nil && !notFound(rerr) {
				err = fmt.Errorf("%w (and the static IP %s could not be released: %v)", err, ipName, rerr)
			}
		}
	}
	return in, err
}

// LeftParked is the error of a resize that deleted the instance and could
// not create one again, on the new bundle or on the old: the box is
// parked. Snapshot is its parked snapshot, which holds the disk; the
// static IP is kept for the instance Restore creates.
type LeftParked struct {
	Snapshot *Snapshot
	Cause    error
}

func (e *LeftParked) Error() string {
	return fmt.Sprintf("%v; snapshot %s holds the disk", e.Cause, e.Snapshot.Name)
}

func (e *LeftParked) Unwrap() error { return e.Cause }

// Resize puts the box on another bundle. Lightsail cannot change an
// instance, so the instance, which must be stopped, is replaced: a
// snapshot of its disk, the instance deleted, and an instance of the same
// name created from the snapshot on the bundle, with the same firewall
// rules and the box's static IP, which is never released. It returns the
// new instance, running.
//
// Nothing is deleted before Lightsail reports the snapshot available and
// it carries the box's parked tags, so from the deletion to the new
// instance the box is a parked box that keelbox resume brings back,
// whatever happens to this process. The snapshot is deleted once an
// instance has the address.
//
// When the instance cannot be created on the bundle, it is created again
// on the old one and the error returned: the box is as it was. When that
// fails too the error is a *LeftParked.
func Resize(ctx context.Context, cl *Client, c *config.Config, in *Instance, bundle string, logf func(string, ...any)) (*Instance, error) {
	box := c.Box
	old, err := cl.own(ctx, box, in.Name)
	if err != nil {
		return nil, err
	}
	bundles, err := cl.AllBundles(ctx)
	if err != nil {
		return nil, err
	}
	ok := false
	for _, o := range ResizeOffers(bundles, old) {
		ok = ok || (o.Bundle.BundleID == bundle && bundle != old.BundleID)
	}
	if !ok {
		return nil, fmt.Errorf("instance %s (%s) cannot change to bundle %s: Lightsail creates an instance from a snapshot only on a plan at least as large", old.Name, old.BundleID, bundle)
	}
	ports, err := cl.Ports(ctx, old.Name)
	if err != nil {
		return nil, err
	}
	if ip, err := cl.StaticIP(ctx, StaticIPName(box)); err != nil {
		return nil, err
	} else if ip == nil || ip.AttachedTo != old.Name {
		name := StaticIPName(box)
		return nil, fmt.Errorf("no static IP %s is attached to instance %s, so its address would not stay. Attach one first: aws lightsail allocate-static-ip --static-ip-name %s --region %s%s (unless it exists), then aws lightsail attach-static-ip --static-ip-name %s --instance-name %s --region %s%s",
			name, old.Name, name, cl.Region, cl.profileFlag(), name, old.Name, cl.Region, cl.profileFlag())
	}
	labels := provider.ManagedLabels(box, c.Target.AWS.Tags)
	labels[TagResize] = "true"
	logf("saving the disk of instance %s as a snapshot (minutes)", old.Name)
	snap, err := cl.takeSnapshot(ctx, box, old, snapshotName(box, "resize"), labels)
	if err != nil {
		return nil, err
	}
	// The snapshot is the box from here until an instance exists again:
	// tagged as parked first, so keelbox resume finds it.
	if err := cl.lightsail(ctx, nil, "tag-resource", "--resource-name", snap.Name, "--tags", "key="+provider.LabelParked+",value=true"); err != nil {
		_ = cl.deleteSnapshot(context.WithoutCancel(ctx), snap.Name)
		return nil, err
	}
	snap.Tags = append(snap.Tags, Tag{Key: provider.LabelParked, Value: "true"})
	logf("snapshot %s is complete; deleting instance %s (%s) to create it again as %s", snap.Name, old.Name, old.BundleID, bundle)
	if err := cl.deleteInstance(ctx, old.Name); err != nil {
		var refused *Error
		if errors.As(err, &refused) && refused.Code != "" {
			// Lightsail refused the deletion: the instance is the box, as
			// it was.
			_ = cl.deleteSnapshot(context.WithoutCancel(ctx), snap.Name)
			return nil, err
		}
		// Whether the instance is going is not known: the snapshot stays.
		return nil, &LeftParked{Snapshot: snap, Cause: fmt.Errorf("deleting instance %s: %w", old.Name, err)}
	}
	zone := old.Location.AvailabilityZone
	done := func(what string) {
		if err := cl.deleteSnapshot(context.WithoutCancel(ctx), snap.Name); err != nil {
			logf("warning: the snapshot %s of the resize could not be deleted (%v); it is billed until it is: %s", snap.Name, err, cl.deleteSnapshotFix(snap.Name))
			return
		}
		logf("deleted snapshot %s: %s", snap.Name, what)
	}
	next, err := cl.fromSnapshot(ctx, c, snap, bundle, zone, ports, logf)
	if err == nil {
		done("instance " + next.Name + " has the disk")
		return next, nil
	}
	logf("instance %s could not be created as %s (%v); creating it as %s again", box, bundle, err, old.BundleID)
	back, berr := cl.fromSnapshot(ctx, c, snap, old.BundleID, zone, ports, logf)
	if berr != nil {
		return nil, &LeftParked{Snapshot: snap, Cause: errors.Join(err, fmt.Errorf("creating it as %s again: %w", old.BundleID, berr))}
	}
	done("instance " + back.Name + " has the disk")
	return nil, fmt.Errorf("%w; instance %s was created again as %s", err, box, old.BundleID)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
