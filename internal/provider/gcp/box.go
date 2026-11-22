package gcp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// How keelbox knows its own VMs: the labels every cloud resource of
// keelbox's carries (provider.LabelManaged, provider.LabelBox). Find and
// Destroy touch a VM only when it carries both and is named after the box.
// The VM also carries the network tag BoxTag, which its firewall rules
// apply to; a network tag is not a label and proves nothing.

const (
	// ImageProject and ImageFamily name Canonical's Ubuntu 24.04 LTS image
	// for x86, the release keelbox uses on the other clouds.
	ImageProject = "ubuntu-os-cloud"
	ImageFamily  = "ubuntu-2404-lts-amd64"
	// DiskType is the boot disk's type.
	DiskType = "pd-balanced"
)

// Instance is a Compute Engine VM, as gcloud prints it.
type Instance struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Status      string            `json:"status"`
	Zone        string            `json:"zone"`        // a URL
	MachineType string            `json:"machineType"` // a URL
	Labels      map[string]string `json:"labels"`
	Tags        struct {
		Items []string `json:"items"`
	} `json:"tags"`
	NetworkInterfaces []struct {
		AccessConfigs []struct {
			NatIP string `json:"natIP"`
		} `json:"accessConfigs"`
	} `json:"networkInterfaces"`
	Metadata struct {
		Items []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"items"`
	} `json:"metadata"`
	ServiceAccounts []struct {
		Email string `json:"email"`
	} `json:"serviceAccounts"`
	Disks []AttachedDisk `json:"disks"`
}

// AttachedDisk is a disk of a VM, as the VM's record lists it.
type AttachedDisk struct {
	Source     string `json:"source"` // a URL
	Boot       bool   `json:"boot"`
	AutoDelete bool   `json:"autoDelete"`
	DiskSizeGb string `json:"diskSizeGb"`
}

// Name is the disk's name.
func (d AttachedDisk) Name() string { return base(d.Source) }

// BootDisk is the disk the VM boots from, or nil.
func (i *Instance) BootDisk() *AttachedDisk {
	for j := range i.Disks {
		if i.Disks[j].Boot {
			return &i.Disks[j]
		}
	}
	return nil
}

// Stopped reports whether Google Cloud holds the VM shut down.
func (i *Instance) Stopped() bool { return i.Status == "TERMINATED" }

// PublicIPv4 is the VM's external IPv4 address, or "".
func (i *Instance) PublicIPv4() string {
	for _, n := range i.NetworkInterfaces {
		for _, a := range n.AccessConfigs {
			if a.NatIP != "" {
				return a.NatIP
			}
		}
	}
	return ""
}

// ZoneName is the VM's zone: "europe-west3-a".
func (i *Instance) ZoneName() string { return base(i.Zone) }

// TypeName is the VM's machine type: "e2-medium".
func (i *Instance) TypeName() string { return base(i.MachineType) }

// HasTag reports whether the VM carries the network tag.
func (i *Instance) HasTag(tag string) bool {
	for _, t := range i.Tags.Items {
		if t == tag {
			return true
		}
	}
	return false
}

// Meta is the value of a metadata key of the VM, or "".
func (i *Instance) Meta(key string) string {
	for _, m := range i.Metadata.Items {
		if m.Key == key {
			return m.Value
		}
	}
	return ""
}

// Managed reports whether keelbox created the VM for box: it is named
// after the box and labelled keelbox=true and keelbox-box=<box>.
func (i *Instance) Managed(box string) bool {
	return i.Name == box && provider.Managed(i.Labels, box)
}

// LoginProblems lists what on the VM's record lets someone other than the
// admin's SSH keys create an account on it, or lets the VM act in the
// project: each is something Create sets and a later change undid.
func (i *Instance) LoginProblems() []string {
	var p []string
	if !strings.EqualFold(i.Meta("block-project-ssh-keys"), "true") {
		p = append(p, "block-project-ssh-keys is not true, so the project's SSH keys create accounts on it")
	}
	if strings.EqualFold(i.Meta("enable-oslogin"), "true") {
		p = append(p, "enable-oslogin is true, so the project's IAM decides who logs in")
	}
	if i.Meta("ssh-keys") != "" || i.Meta("sshKeys") != "" {
		p = append(p, "the VM's metadata holds SSH keys, which create accounts with sudo on it")
	}
	if len(i.ServiceAccounts) > 0 {
		p = append(p, "a service account is attached ("+i.ServiceAccounts[0].Email+"), whose token every process on the VM can read")
	}
	return p
}

// InstancesWhere lists the project's VMs, in every zone, that match a
// gcloud filter expression.
func (c *Client) InstancesWhere(ctx context.Context, filter string) ([]Instance, error) {
	var is []Instance
	return is, c.compute(ctx, &is, "instances", "list", "--filter", filter)
}

// InstancesNamed lists the VMs of that name: names are unique in a zone,
// not in a project.
func (c *Client) InstancesNamed(ctx context.Context, name string) ([]Instance, error) {
	is, err := c.InstancesWhere(ctx, "name="+name)
	if err != nil {
		return nil, err
	}
	var out []Instance
	for _, i := range is {
		if i.Name == name {
			out = append(out, i)
		}
	}
	return out, nil
}

// Address is a reserved external IP address.
type Address struct {
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Description string   `json:"description"`
	Status      string   `json:"status"`      // RESERVED or IN_USE
	AddressType string   `json:"addressType"` // EXTERNAL or INTERNAL
	Region      string   `json:"region"`      // a URL
	Users       []string `json:"users"`       // URLs of what uses it
}

// AddressName is the name of the box's reserved address.
func AddressName(box string) string { return "keelbox-" + box }

// RegionOf is the region of a zone: europe-west3 for europe-west3-a.
func RegionOf(zone string) string {
	if i := strings.LastIndex(zone, "-"); i > 0 {
		return zone[:i]
	}
	return zone
}

// FindAddress returns the box's reserved address, or nil. Addresses carry
// no labels, so keelbox knows its own by name and by the description it
// writes. One of that name without the description is an error: keelbox
// did not make it and does not touch it.
func FindAddress(ctx context.Context, cl *Client, box string) (*Address, error) {
	var all []Address
	name := AddressName(box)
	if err := cl.compute(ctx, &all, "addresses", "list", "--filter", "name="+name); err != nil {
		return nil, err
	}
	var found []Address
	for _, a := range all {
		if a.Name == name {
			found = append(found, a)
		}
	}
	switch {
	case len(found) == 0:
		return nil, nil
	case len(found) > 1:
		return nil, fmt.Errorf("%d addresses are named %s, in different regions; keep the one the box uses and release the rest (gcloud compute addresses list%s), then run the command again",
			len(found), name, cl.projectFlag())
	case found[0].Description != managedDescription("the address", box):
		return nil, fmt.Errorf("an address named %s exists in %s that keelbox did not reserve (it lacks keelbox's description), so keelbox does not touch it. "+
			"Rename the box, or release it: gcloud compute addresses delete %s --region %s%s", name, base(found[0].Region), name, base(found[0].Region), cl.projectFlag())
	}
	return &found[0], nil
}

// ensureAddress reserves the box's address in the region, or returns the
// one an interrupted create left there unused.
func ensureAddress(ctx context.Context, cl *Client, box, region string) (*Address, bool, error) {
	a, err := FindAddress(ctx, cl, box)
	if err != nil {
		return nil, false, err
	}
	if a != nil {
		if len(a.Users) > 0 || base(a.Region) != region {
			return nil, false, fmt.Errorf("the address %s (%s, %s) is in use or in another region than %s; release it first: keelbox destroy %s", a.Name, a.Address, base(a.Region), region, box)
		}
		return a, false, nil
	}
	if err := cl.compute(ctx, nil, "addresses", "create", AddressName(box), "--region", region, "--description", managedDescription("the address", box)); err != nil {
		return nil, false, err
	}
	a, err = FindAddress(ctx, cl, box)
	if err == nil && (a == nil || a.Address == "") {
		err = fmt.Errorf("the address %s was reserved in %s but gcloud does not list it yet; run the command again", AddressName(box), region)
	}
	return a, true, err
}

// ReleaseAddress releases the box's reserved address when nothing uses
// it, and returns it. One that something still uses stays, with an error.
func ReleaseAddress(ctx context.Context, cl *Client, box string) (*Address, error) {
	for i := 0; ; i++ {
		a, err := FindAddress(ctx, cl, box)
		if err != nil || a == nil {
			return nil, err
		}
		if len(a.Users) == 0 {
			if err := cl.compute(ctx, nil, "addresses", "delete", a.Name, "--region", base(a.Region)); err != nil && !IsNotFound(err) {
				return nil, err
			}
			return a, nil
		}
		// A VM just deleted is still named as its user for a moment.
		if i == 10 {
			return nil, fmt.Errorf("the address %s (%s) is still in use by %s, so it stays", a.Name, a.Address, base(a.Users[0]))
		}
		if err := wait(ctx, poll); err != nil {
			return nil, err
		}
	}
}

// guestAgentConfig is appended to the user data. Google's guest agent,
// which Ubuntu's image runs, replaces the SSH host keys on a VM's first
// boot, after cloud-init has installed the one keelbox pinned. This turns
// that off, before the agent starts, so the VM presents the pinned key.
const guestAgentConfig = `write_files:
- path: /etc/default/instance_configs.cfg
  append: true
  content: |
    # Added by keelbox: the first host key is the one keelbox generated and
    # pinned, so Google's guest agent must not replace it on first boot.
    [InstanceSetup]
    set_host_keys = false
`

// UserData is the cloud-config a box's VM boots with: the admin account
// with the user's keys, no SSH passwords, no root login, the single-use
// host key, and the guest agent told to leave that key in place.
func UserData(c *config.Config, hk provider.HostKeyPair) (string, error) {
	keys, err := c.Users.Admin.AllKeys()
	if err != nil {
		return "", err
	}
	doc, err := provider.AdminUserData(c.Box, c.Host.Hostname, c.Users.Admin.Name, keys, hk)
	if err != nil {
		return "", err
	}
	return doc + guestAgentConfig, nil
}

// writeUserData puts the user data in a mode 600 file for gcloud's
// --metadata-from-file, so the host key never appears in a command line.
// The caller removes it.
func writeUserData(userData string) (string, error) {
	f, err := os.CreateTemp("", "keelbox-user-data-*") // created mode 600
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(userData); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// poll is how often Create and Destroy ask Google for the new state;
// tests shorten it.
var poll = 3 * time.Second

func wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Create makes the VM for a box and returns it once it runs and has its
// address. The box's firewall rules exist before the VM and apply to its
// tag, so the VM is never reachable without them. Create fails if a VM
// of that name exists. A VM returned with an error exists.
func Create(ctx context.Context, cl *Client, c *config.Config, hk provider.HostKeyPair, logf func(string, ...any)) (*Instance, error) {
	g := c.Target.GCP
	if cl.Project == "" {
		cl.Project = g.Project
	}
	if _, err := cl.Preflight(ctx, g.Zone, g.MachineType); err != nil {
		return nil, err
	}
	// Names are unique in a zone only; keelbox keeps them unique in the project.
	named, err := cl.InstancesNamed(ctx, c.Box)
	if err != nil {
		return nil, err
	}
	if len(named) > 0 {
		return nil, fmt.Errorf("a VM named %s already exists in %s; converge it: keelbox apply %s, or delete it first: keelbox destroy %s", c.Box, named[0].ZoneName(), c.Box, c.Box)
	}
	userData, err := UserData(c, hk)
	if err != nil {
		return nil, err
	}
	// New rules admit SSH; apply closes it once the tailnet is proven.
	_, changes, err := EnsureFirewall(ctx, cl, c.Box, nil)
	for _, ch := range changes {
		logf("%s", ch)
	}
	if err != nil {
		return nil, fmt.Errorf("cloud firewall: %w", err)
	}
	// A reserved address: the one Google lends a VM changes when the VM
	// is stopped and started.
	addr, reserved, err := ensureAddress(ctx, cl, c.Box, RegionOf(g.Zone))
	if err != nil {
		return nil, fmt.Errorf("reserve the address: %w", err)
	}
	if reserved {
		logf("reserved address %s (%s) in %s", addr.Name, addr.Address, RegionOf(g.Zone))
	}
	path, err := writeUserData(userData)
	if err != nil {
		return nil, fmt.Errorf("write the user data file: %w", err)
	}
	defer func() { _ = os.Remove(path) }()

	logf("creating %s: %s in %s, Ubuntu 24.04, %d GB disk", c.Box, g.MachineType, g.Zone, g.DiskGB)
	var made []Instance
	err = cl.compute(ctx, &made, "instances", "create", c.Box, "--zone", g.Zone, "--machine-type", g.MachineType,
		"--image-family", ImageFamily, "--image-project", ImageProject,
		"--boot-disk-size", strconv.Itoa(g.DiskGB)+"GB", "--boot-disk-type", DiskType,
		"--labels", labelPairs(provider.ManagedLabels(c.Box, g.Labels)), "--tags", BoxTag(c.Box),
		"--network", Network, "--address", addr.Address,
		// The admin's keys come from the user data. Keys in the project's
		// metadata create no account here, and IAM does not decide who
		// logs in (OS Login).
		"--metadata", "block-project-ssh-keys=true,enable-oslogin=false",
		"--metadata-from-file", "user-data="+path,
		// No service account: nothing on the VM gets a token for the project.
		"--no-service-account", "--no-scopes")
	if err != nil {
		if said(err, "requireOsLogin", "OS Login") {
			err = fmt.Errorf("%w. The organisation requires OS Login on every VM (constraints/compute.requireOsLogin), and a box logs in with the admin's own SSH key; "+
				"use a project the policy leaves out", err)
		}
		// gcloud may fail after the VM was ordered: say whether it exists.
		if now, ferr := cl.InstancesNamed(ctx, c.Box); ferr == nil && len(now) > 0 {
			return &now[0], err
		}
		return nil, err
	}
	if len(made) != 1 {
		return &Instance{Name: c.Box, Zone: g.Zone}, fmt.Errorf("gcloud compute instances create printed %d VMs, not one", len(made))
	}
	return cl.waitRunning(ctx, &made[0])
}

func (c *Client) waitRunning(ctx context.Context, in *Instance) (*Instance, error) {
	for i := 0; i < 100; i++ {
		if in.Status == "RUNNING" && in.PublicIPv4() != "" {
			return in, nil
		}
		if err := wait(ctx, poll); err != nil {
			return in, err
		}
		now, err := c.InstancesNamed(ctx, in.Name)
		if err != nil {
			return in, err
		}
		for j := range now {
			if now[j].ID == in.ID {
				in = &now[j]
			}
		}
	}
	return in, fmt.Errorf("VM %s did not start running with its address", in.Name)
}

// Find returns the keelbox-managed VM for a box.
func Find(ctx context.Context, cl *Client, box string) (*Instance, error) {
	all, err := cl.InstancesWhere(ctx, "name="+box+" OR labels."+provider.LabelBox+"="+box)
	if err != nil {
		return nil, err
	}
	var own, named []Instance
	for _, i := range all {
		switch {
		case i.Managed(box):
			own = append(own, i)
		case i.Name == box:
			named = append(named, i)
		}
	}
	switch {
	case len(own) == 1:
		return &own[0], nil
	case len(own) > 1:
		zones := make([]string, len(own))
		for i, in := range own {
			zones[i] = in.ZoneName()
		}
		return nil, fmt.Errorf("%d VMs are named %s and labelled for the box (in %s); keep one and delete the rest (gcloud compute instances delete %s --zone ZONE%s), then run the command again",
			len(own), box, strings.Join(zones, ", "), box, cl.projectFlag())
	case len(named) > 0:
		return nil, fmt.Errorf("the VM %s (in %s) was not created by keelbox (missing the labels %s=true and %s=%s); not touching it. Use another box name, or manage it as an existing server: keelbox up %s --provider ssh --host ITS-IP",
			box, named[0].ZoneName(), provider.LabelManaged, provider.LabelBox, box, box)
	}
	in := "gcloud's own project"
	if cl.Project != "" {
		in = "the Google Cloud project " + cl.Project
	}
	return nil, fmt.Errorf("no VM named %s in %s; %s", box, in, provider.NoServerFix(box))
}

// Destroy deletes a keelbox-managed VM with its disk, then the box's
// firewall rules, its reserved address and any parked snapshot left
// beside it. It returns what it deleted.
func Destroy(ctx context.Context, cl *Client, in *Instance, box string) ([]string, error) {
	if !in.Managed(box) {
		return nil, fmt.Errorf("refusing to delete VM %s: not named %s and labelled %s=true and %s=%s", in.Name, box, provider.LabelManaged, provider.LabelBox, box)
	}
	// gcloud waits until the VM is gone. Its boot disk goes with it.
	if err := cl.compute(ctx, nil, "instances", "delete", in.Name, "--zone", in.ZoneName()); err != nil {
		return nil, err
	}
	done := []string{fmt.Sprintf("deleted VM %s (%s) and its disk", in.Name, in.ZoneName())}
	swept, err := Sweep(ctx, cl, box)
	return append(done, swept...), err
}

// Sweep deletes the box's firewall rules, releases its address and
// deletes every snapshot labelled as its parked one, whatever its state,
// when no VM carries the box's tag or label: what a destroy leaves to do,
// what a create that stopped before the VM existed left behind, and what
// park kept for resume. It returns what it deleted, and the first error
// after trying each.
func Sweep(ctx context.Context, cl *Client, box string) ([]string, error) {
	left, err := cl.InstancesWhere(ctx, "tags.items="+BoxTag(box)+" OR labels."+provider.LabelBox+"="+box)
	if err != nil {
		return nil, err
	}
	for _, i := range left {
		if i.HasTag(BoxTag(box)) || i.Labels[provider.LabelBox] == box {
			return nil, fmt.Errorf("VM %s (in %s) still carries the tag %s or the box's label, so the firewall rules and the address of %s stay", i.Name, i.ZoneName(), BoxTag(box), box)
		}
	}
	var done []string
	var first error
	keep := func(err error) {
		if first == nil {
			first = err
		}
	}
	snaps, err := SweepSnapshots(ctx, cl, box)
	for _, s := range snaps {
		done = append(done, "deleted snapshot "+s.Name)
	}
	if err != nil {
		keep(fmt.Errorf("parked snapshot: %w", err))
	}
	rules, err := DestroyFirewall(ctx, cl, box)
	for _, r := range rules {
		done = append(done, "deleted firewall rule "+r)
	}
	if err != nil {
		keep(fmt.Errorf("cloud firewall: %w", err))
	}
	a, err := ReleaseAddress(ctx, cl, box)
	if a != nil {
		done = append(done, fmt.Sprintf("released address %s (%s)", a.Name, a.Address))
	}
	if err != nil {
		keep(fmt.Errorf("release the address: %w", err))
	}
	return done, first
}

// Zone is a Compute Engine zone.
type Zone struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Region string `json:"region"` // a URL
}

// Zones lists the zones that are up, by name.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	var all []Zone
	if err := c.compute(ctx, &all, "zones", "list"); err != nil {
		return nil, err
	}
	var out []Zone
	for _, z := range all {
		if z.Status == "UP" {
			out = append(out, z)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// HasZone reports whether the list holds the zone.
func HasZone(zones []Zone, name string) bool {
	for _, z := range zones {
		if z.Name == name {
			return true
		}
	}
	return false
}

// MachineType is a Compute Engine machine type in a zone.
type MachineType struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	GuestCPUs    int    `json:"guestCpus"`
	MemoryMB     int    `json:"memoryMb"`
	IsSharedCPU  bool   `json:"isSharedCpu"`
	Architecture string `json:"architecture"` // X86_64 or ARM64; older lists leave it out
	Deprecated   *struct {
		State string `json:"state"`
	} `json:"deprecated"`
}

// usable reports whether a box can be created on the type: it is not
// retired, and it runs the x86 image keelbox boots.
func (m MachineType) usable() bool {
	return (m.Deprecated == nil || m.Deprecated.State == "" || m.Deprecated.State == "ACTIVE") && !strings.EqualFold(m.Architecture, "ARM64")
}

// MachineTypes lists the machine types of a zone.
func (c *Client) MachineTypes(ctx context.Context, zone string) ([]MachineType, error) {
	var types []MachineType
	return types, c.compute(ctx, &types, "machine-types", "list", "--zones", zone)
}

// Preflight checks, before anything is created, that gcloud is signed in
// and the project's Compute Engine API is on, that the zone is up, that
// the machine type exists there and runs an x86 image, and that the
// project has the network the box goes in. Each failure names the fix.
// An empty zone skips the checks after the first; an empty machine type
// skips its own.
func (c *Client) Preflight(ctx context.Context, zone, machineType string) (Account, error) {
	acct, err := c.Account(ctx)
	if err != nil || zone == "" {
		return acct, err
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		return acct, err
	}
	if !HasZone(zones, zone) {
		var near []string
		for _, z := range zones {
			if RegionOf(z.Name) == RegionOf(zone) {
				near = append(near, z.Name)
			}
		}
		hint := "gcloud compute zones list" + c.projectFlag() + " shows them"
		if len(near) > 0 {
			hint = "in that region: " + strings.Join(near, ", ")
		}
		return acct, fmt.Errorf("target.gcp.zone %q is not a zone that is up in project %s; set zone to one that is (%s)", zone, c.Project, hint)
	}
	if machineType != "" {
		types, err := c.MachineTypes(ctx, zone)
		if err != nil {
			return acct, err
		}
		var found *MachineType
		for i := range types {
			if types[i].Name == machineType {
				found = &types[i]
			}
		}
		var some []string
		for _, o := range Offers(types) {
			some = append(some, o.Type.Name)
		}
		switch {
		case found == nil:
			return acct, fmt.Errorf("target.gcp.machine_type %q is not a machine type in %s; set machine_type to one that is, for example: %s (gcloud compute machine-types list --zones %s shows all)",
				machineType, zone, strings.Join(some, ", "), zone)
		case !found.usable():
			return acct, fmt.Errorf("target.gcp.machine_type %q cannot run a box: it is retired or an Arm type, and keelbox boots the x86 image %s; set machine_type to, for example: %s",
				machineType, ImageFamily, strings.Join(some, ", "))
		}
	}
	var nets []struct {
		Name string `json:"name"`
	}
	if err := c.compute(ctx, &nets, "networks", "list"); err != nil {
		return acct, err
	}
	for _, n := range nets {
		if n.Name == Network {
			return acct, nil
		}
	}
	return acct, fmt.Errorf("project %s has no VPC network named %s, which keelbox creates the box in. Create it: gcloud compute networks create %s --subnet-mode=auto%s",
		c.Project, Network, Network, c.projectFlag())
}

// Offer is a machine type a box can run on. Google's CLI lists no prices,
// so an offer has none.
type Offer struct {
	Type MachineType
}

// Describe is a one-line summary: cores and memory.
func (o Offer) Describe() string {
	t := o.Type
	cpu := fmt.Sprintf("%d vCPU", t.GuestCPUs)
	if t.IsSharedCPU {
		cpu += " (shared)"
	}
	d := fmt.Sprintf("%s, %g GB", cpu, float64(t.MemoryMB)/1024)
	if o.Small() {
		d += "; small for agents"
	}
	return d
}

// Small is a type below 2 vCPU or 4 GB, where a dev server, tests and a
// headless browser together run out of memory.
func (o Offer) Small() bool { return o.Type.GuestCPUs < 2 || o.Type.MemoryMB < 4096 }

// Offers are the machine types keelbox up lists: the general-purpose E2
// family up to 8 vCPUs, smallest first. A zone has hundreds of types;
// --size and target.gcp.machine_type take any of them.
func Offers(types []MachineType) []Offer {
	var out []Offer
	for _, t := range types {
		if !t.usable() || t.GuestCPUs > 8 {
			continue
		}
		if strings.HasPrefix(t.Name, "e2-standard-") || t.Name == "e2-micro" || t.Name == "e2-small" || t.Name == "e2-medium" {
			out = append(out, Offer{Type: t})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Type, out[j].Type
		if a.MemoryMB != b.MemoryMB {
			return a.MemoryMB < b.MemoryMB
		}
		return a.Name < b.Name
	})
	return out
}

// DefaultOffer is the smallest offer with at least 2 vCPUs and 4 GB, the
// size docs/guides/where-to-run.md gives as enough for one or two agents, else the first.
func DefaultOffer(offers []Offer) int {
	for i, o := range offers {
		if !o.Small() {
			return i
		}
	}
	return 0
}
