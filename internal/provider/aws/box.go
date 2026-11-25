package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// BootstrapUser is the account Lightsail's Ubuntu images log in as. It has
// passwordless sudo; the first apply locks it once the admin is proven.
const BootstrapUser = "ubuntu"

// Tag is a Lightsail resource tag.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Instance is a Lightsail instance.
type Instance struct {
	Name            string `json:"name"`
	Arn             string `json:"arn"`
	BlueprintID     string `json:"blueprintId"`
	BundleID        string `json:"bundleId"`
	IsStaticIP      bool   `json:"isStaticIp"`
	PublicIPAddress string `json:"publicIpAddress"`
	Tags            []Tag  `json:"tags"`
	State           struct {
		Name string `json:"name"`
	} `json:"state"`
	Location struct {
		AvailabilityZone string `json:"availabilityZone"`
		RegionName       string `json:"regionName"`
	} `json:"location"`
	Hardware struct {
		Disks []struct {
			SizeInGb     int  `json:"sizeInGb"`
			IsSystemDisk bool `json:"isSystemDisk"`
		} `json:"disks"`
	} `json:"hardware"`
}

// DiskGB is the size of the instance's system disk, or 0 when Lightsail
// does not say.
func (i *Instance) DiskGB() int {
	for _, d := range i.Hardware.Disks {
		if d.IsSystemDisk {
			return d.SizeInGb
		}
	}
	return 0
}

// tagArgs are labels as the values of a --tags option, in key order.
func tagArgs(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys))
	for _, k := range keys {
		args = append(args, "key="+k+",value="+labels[k])
	}
	return args
}

func labelsOf(tags []Tag) map[string]string {
	l := map[string]string{}
	for _, t := range tags {
		l[t.Key] = t.Value
	}
	return l
}

// Labels are the instance's tags as a map.
func (i *Instance) Labels() map[string]string { return labelsOf(i.Tags) }

// Managed reports whether keelbox created the instance for box: it
// carries the tags keelbox=true and keelbox-box=<box>.
func (i *Instance) Managed(box string) bool { return provider.Managed(i.Labels(), box) }

// notFound reports Lightsail's answer for a resource that does not exist.
func notFound(err error) bool { return IsCode(err, "NotFoundException", "DoesNotExist") }

// Instance returns the instance of that name in the region, or nil.
func (c *Client) Instance(ctx context.Context, name string) (*Instance, error) {
	var r struct {
		Instance *Instance `json:"instance"`
	}
	if err := c.lightsail(ctx, &r, "get-instance", "--instance-name", name); err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.Instance, nil
}

// Instances lists every instance in the region. The AWS CLI follows the
// API's page tokens itself and prints all pages as one list.
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	var r struct {
		Instances []Instance `json:"instances"`
	}
	return r.Instances, c.lightsail(ctx, &r, "get-instances")
}

// StaticIP is a Lightsail static IP. Lightsail has no tags on static IPs,
// so keelbox knows its own by name (StaticIPName) and by what it is
// attached to.
type StaticIP struct {
	Name       string `json:"name"`
	IPAddress  string `json:"ipAddress"`
	AttachedTo string `json:"attachedTo"`
	IsAttached bool   `json:"isAttached"`
}

// StaticIPName is the name of the box's static IP.
func StaticIPName(box string) string { return "keelbox-" + box }

// StaticIP returns the static IP of that name in the region, or nil.
func (c *Client) StaticIP(ctx context.Context, name string) (*StaticIP, error) {
	var r struct {
		StaticIP *StaticIP `json:"staticIp"`
	}
	if err := c.lightsail(ctx, &r, "get-static-ip", "--static-ip-name", name); err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.StaticIP, nil
}

// StaticIPs lists the region's static IPs.
func (c *Client) StaticIPs(ctx context.Context) ([]StaticIP, error) {
	var r struct {
		StaticIPs []StaticIP `json:"staticIps"`
	}
	return r.StaticIPs, c.lightsail(ctx, &r, "get-static-ips")
}

// userDataEOF ends each here-document in the user data. Keys are base64
// and public key lines, which cannot contain it.
const userDataEOF = "KEELBOX_USER_DATA_EOF"

// UserData is the launch script for a new instance. Lightsail runs user
// data as a shell script, as root, at the end of the first boot; it does
// not take a cloud-config document. The script replaces the host keys the
// image generated with the one made on the laptop, so the first connection
// keelbox accepts is checked against a key it already holds, and puts the
// admin's public keys in the image account's authorized_keys for the first
// login. Until the script has run the instance presents a key keelbox has
// not pinned, and keelbox refuses the connection and tries again.
//
// The private host key is the only secret here and is single-use: any
// process on the instance can read user data from the metadata service, so
// keelbox replaces the key over the first connection
// (provider.SSH.RotateHostKeys).
func UserData(box, hostname string, hk provider.HostKeyPair, adminKeys []string) (string, error) {
	if len(adminKeys) == 0 {
		return "", fmt.Errorf("no SSH public key for the first login as %s; set users.admin.ssh_authorized_keys", BootstrapUser)
	}
	for _, s := range append([]string{hk.Private, hk.Public, hostname}, adminKeys...) {
		if strings.Contains(s, userDataEOF) {
			return "", errors.New("render user data: a key contains the here-document marker")
		}
	}
	var b strings.Builder
	doc := func(path, body string) {
		fmt.Fprintf(&b, "cat > %s <<'%s'\n%s\n%s\n", path, userDataEOF, strings.TrimRight(body, "\n"), userDataEOF)
	}
	b.WriteString("#!/bin/sh\n# keelbox box " + box + ". keelbox and its config arrive over SSH after first boot.\nset -eu\numask 077\n")
	b.WriteString("rm -f /etc/ssh/ssh_host_*_key /etc/ssh/ssh_host_*_key.pub\n")
	doc("/etc/ssh/ssh_host_ed25519_key", hk.Private)
	doc("/etc/ssh/ssh_host_ed25519_key.pub", strings.TrimSpace(hk.Public))
	b.WriteString("chmod 600 /etc/ssh/ssh_host_ed25519_key\nchmod 644 /etc/ssh/ssh_host_ed25519_key.pub\n")
	home := "/home/" + BootstrapUser
	fmt.Fprintf(&b, "install -d -m 700 -o %s -g %s %s/.ssh\n", BootstrapUser, BootstrapUser, home)
	fmt.Fprintf(&b, "cat >> %s/.ssh/authorized_keys <<'%s'\n%s\n%s\n", home, userDataEOF, strings.Join(adminKeys, "\n"), userDataEOF)
	fmt.Fprintf(&b, "chown %s:%s %s/.ssh/authorized_keys\nchmod 600 %s/.ssh/authorized_keys\n", BootstrapUser, BootstrapUser, home, home)
	fmt.Fprintf(&b, "hostnamectl set-hostname '%s' || true\n", hostname)
	// sshd reads host keys when it starts; with socket activation the next
	// connection starts a new one.
	b.WriteString("systemctl restart ssh.service 2>/dev/null || systemctl restart sshd.service 2>/dev/null || true\n")
	return b.String(), nil
}

// writeUserData puts the user data in a mode 600 file for the AWS CLI's
// file:// parameter, so the host key never appears in a command line. The
// caller removes it.
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

// poll is how often Create and Destroy ask Lightsail for the new state;
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

// Create makes the instance for a box and returns it once it runs, its
// firewall admits only SSH and Tailscale, and its static IP is attached.
// It fails if an instance of that name exists. An instance returned with
// an error exists.
func Create(ctx context.Context, cl *Client, c *config.Config, hk provider.HostKeyPair, logf func(string, ...any)) (*Instance, error) {
	a := c.Target.AWS
	if _, err := cl.Preflight(ctx, a.Bundle, a.Blueprint); err != nil {
		return nil, err
	}
	if in, err := cl.Instance(ctx, c.Box); err != nil {
		return nil, err
	} else if in != nil {
		return nil, fmt.Errorf("a Lightsail instance named %s already exists in %s; converge it: keelbox apply %s, or delete it first: keelbox destroy %s", c.Box, cl.Region, c.Box, c.Box)
	}
	ipName := StaticIPName(c.Box)
	if ip, err := cl.StaticIP(ctx, ipName); err != nil {
		return nil, err
	} else if ip != nil {
		return nil, fmt.Errorf("a static IP named %s already exists in %s (%s). If an earlier keelbox create left it, release it: aws lightsail release-static-ip --static-ip-name %s --region %s%s; otherwise use another box name",
			ipName, cl.Region, ip.IPAddress, ipName, cl.Region, cl.profileFlag())
	}
	zone := a.AvailabilityZone
	if zone == "" {
		z, err := cl.firstZone(ctx)
		if err != nil {
			return nil, err
		}
		zone = z
	}
	keys, err := c.Users.Admin.AllKeys()
	if err != nil {
		return nil, err
	}
	userData, err := UserData(c.Box, c.Host.Hostname, hk, keys)
	if err != nil {
		return nil, err
	}
	path, err := writeUserData(userData)
	if err != nil {
		return nil, fmt.Errorf("write the user data file: %w", err)
	}
	defer func() { _ = os.Remove(path) }()

	logf("creating %s: %s in %s, %s", c.Box, a.Bundle, zone, a.Blueprint)
	args := []string{"--instance-names", c.Box, "--availability-zone", zone,
		"--blueprint-id", a.Blueprint, "--bundle-id", a.Bundle, "--user-data", "file://" + path, "--tags"}
	args = append(args, tagArgs(provider.ManagedLabels(c.Box, a.Tags))...)
	if err := cl.lightsail(ctx, nil, "create-instances", args...); err != nil {
		return nil, err
	}
	in, err := cl.waitRunning(ctx, c.Box)
	if err != nil {
		return orNamed(in, c.Box), err
	}
	// Lightsail opens 22 and 80 on a new instance. Nothing listens on 80
	// on the Ubuntu image; it is closed here, as soon as Lightsail takes
	// firewall changes for the instance.
	changes, _, err := EnsurePorts(ctx, cl, c.Box, nil)
	if err != nil {
		return in, fmt.Errorf("instance firewall: %w", err)
	}
	for _, ch := range changes {
		logf("%s", ch)
	}
	// The static IP replaces the address the instance booted with, so it
	// is attached before keelbox connects: the address then stays the same
	// across a stop and start.
	if err := cl.lightsail(ctx, nil, "allocate-static-ip", "--static-ip-name", ipName); err != nil {
		return in, fmt.Errorf("static IP: %w", err)
	}
	cur, ip, err := cl.attachStaticIP(ctx, c.Box, c.Box)
	if err != nil {
		return in, err
	}
	logf("attached static IP %s (%s)", ipName, ip.IPAddress)
	return cur, nil
}

// attachStaticIP makes the box's static IP the address of the instance,
// taking it from whatever holds it, and returns the instance once it has
// the address. The caller has checked what the address is taken from.
func (c *Client) attachStaticIP(ctx context.Context, box, instance string) (*Instance, *StaticIP, error) {
	name := StaticIPName(box)
	ip, err := c.StaticIP(ctx, name)
	if err != nil {
		return nil, nil, fmt.Errorf("static IP: %w", err)
	}
	if ip == nil {
		return nil, nil, fmt.Errorf("static IP %s does not exist in %s", name, c.Region)
	}
	if ip.IsAttached && ip.AttachedTo != instance {
		if err := c.lightsail(ctx, nil, "detach-static-ip", "--static-ip-name", name); err != nil {
			return nil, ip, fmt.Errorf("static IP: %w", err)
		}
		ip.IsAttached = false
	}
	if !ip.IsAttached {
		if err := c.lightsail(ctx, nil, "attach-static-ip", "--static-ip-name", name, "--instance-name", instance); err != nil {
			return nil, ip, fmt.Errorf("static IP: %w", err)
		}
	}
	for i := 0; i < 40; i++ {
		ip, err := c.StaticIP(ctx, name)
		if err != nil {
			return nil, ip, err
		}
		cur, err := c.Instance(ctx, instance)
		if err != nil {
			return nil, ip, err
		}
		if ip != nil && cur != nil && ip.IsAttached && ip.AttachedTo == instance && cur.PublicIPAddress == ip.IPAddress {
			return cur, ip, nil
		}
		if err := wait(ctx, poll); err != nil {
			return nil, ip, err
		}
	}
	return nil, ip, fmt.Errorf("static IP %s did not become the address of %s", name, instance)
}

func orNamed(in *Instance, box string) *Instance {
	if in != nil {
		return in
	}
	return &Instance{Name: box}
}

func (c *Client) waitRunning(ctx context.Context, box string) (*Instance, error) {
	var in *Instance
	for i := 0; i < 100; i++ {
		cur, err := c.Instance(ctx, box)
		if err != nil {
			return in, err
		}
		if cur != nil {
			in = cur
			if cur.State.Name == "running" && cur.PublicIPAddress != "" {
				return cur, nil
			}
		}
		if err := wait(ctx, poll); err != nil {
			return in, err
		}
	}
	return in, fmt.Errorf("instance %s did not reach the running state", box)
}

// Find returns the keelbox-managed instance for a box.
func Find(ctx context.Context, cl *Client, box string) (*Instance, error) {
	in, err := cl.Instance(ctx, box)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, &provider.NoServerError{Msg: fmt.Sprintf("no Lightsail instance named %s in %s; %s", box, cl.Region, provider.NoServerFix(box))}
	}
	if !in.Managed(box) {
		return nil, fmt.Errorf("the Lightsail instance %s in %s was not created by keelbox (missing the %s tags); not touching it. Use another box name, or manage it as an existing server: keelbox up %s --provider ssh --host ITS-IP",
			box, cl.Region, provider.LabelManaged, box)
	}
	return in, nil
}

// Destroy deletes a keelbox-managed instance, waits until it is gone, then
// releases the box's static IP. It returns what it deleted.
func Destroy(ctx context.Context, cl *Client, in *Instance, box string) ([]string, error) {
	if !in.Managed(box) {
		return nil, fmt.Errorf("refusing to delete instance %s: not tagged %s=true and %s=%s", in.Name, provider.LabelManaged, provider.LabelBox, box)
	}
	if err := cl.deleteInstance(ctx, in.Name); err != nil {
		if !errors.Is(err, errStillDeleting) {
			return nil, err
		}
		return []string{"deleted instance " + in.Name}, fmt.Errorf("%w; when it is gone, run keelbox destroy %s again to release its static IP", err, box)
	}
	done := []string{"deleted instance " + in.Name}
	released, err := ReleaseStaticIP(ctx, cl, box)
	if released != nil {
		done = append(done, fmt.Sprintf("released static IP %s (%s)", released.Name, released.IPAddress))
	}
	return done, err
}

// errStillDeleting is an instance Lightsail has not finished deleting.
var errStillDeleting = errors.New("the instance is still being deleted")

// deleteInstance deletes the instance and returns once Lightsail no longer
// has it. The caller has checked that it is keelbox's.
func (c *Client) deleteInstance(ctx context.Context, name string) error {
	if err := c.lightsail(ctx, nil, "delete-instance", "--instance-name", name, "--force-delete-add-ons"); err != nil {
		return err
	}
	for i := 0; ; i++ {
		cur, err := c.Instance(ctx, name)
		if err != nil {
			return err
		}
		if cur == nil {
			return nil
		}
		if i == 60 {
			return fmt.Errorf("instance %s: %w", name, errStillDeleting)
		}
		if err := wait(ctx, poll); err != nil {
			return err
		}
	}
}

// ReleaseStaticIP releases the box's static IP when it is attached to
// nothing or to the box's own instance. Lightsail keeps a static IP, and
// bills it, after its instance is deleted.
func ReleaseStaticIP(ctx context.Context, cl *Client, box string) (*StaticIP, error) {
	name := StaticIPName(box)
	ip, err := cl.StaticIP(ctx, name)
	if err != nil || ip == nil {
		return nil, err
	}
	if ip.IsAttached {
		// Attached to the box's own instance it goes; to anything else,
		// an instance of the box's name without keelbox's tags included,
		// it stays.
		in, err := cl.Instance(ctx, ip.AttachedTo)
		if err != nil {
			return nil, err
		}
		if ip.AttachedTo != box || in == nil || !in.Managed(box) {
			return nil, fmt.Errorf("static IP %s is attached to %s, which keelbox did not create for this box; keelbox leaves it. Release it yourself if it is not needed: aws lightsail release-static-ip --static-ip-name %s --region %s%s",
				name, ip.AttachedTo, name, cl.Region, cl.profileFlag())
		}
	}
	if err := cl.lightsail(ctx, nil, "release-static-ip", "--static-ip-name", name); err != nil && !notFound(err) {
		return nil, err
	}
	return ip, nil
}

// Region is an AWS region that offers Lightsail.
type Region struct {
	Name              string `json:"name"`
	DisplayName       string `json:"displayName"`
	AvailabilityZones []struct {
		ZoneName string `json:"zoneName"`
		State    string `json:"state"`
	} `json:"availabilityZones"`
}

// catalogRegion is where keelbox asks for the list of Lightsail regions
// when the box's own region may not offer Lightsail at all.
const catalogRegion = "us-east-1"

// Regions lists the regions that offer Lightsail, with their zones.
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	var r struct {
		Regions []Region `json:"regions"`
	}
	region := c.Region
	if region == "" {
		region = catalogRegion
	}
	err := c.call(ctx, region, &r, "lightsail", "get-regions", "--include-availability-zones")
	if err != nil && region != catalogRegion && !errors.Is(err, ErrNoCLI) {
		// The region may have no Lightsail endpoint: ask one that has.
		if err2 := c.call(ctx, catalogRegion, &r, "lightsail", "get-regions", "--include-availability-zones"); err2 == nil {
			err = nil
		}
	}
	sort.SliceStable(r.Regions, func(i, j int) bool { return r.Regions[i].Name < r.Regions[j].Name })
	return r.Regions, err
}

func regionNames(regions []Region) string {
	n := make([]string, len(regions))
	for i, r := range regions {
		n[i] = r.Name
	}
	return strings.Join(n, ", ")
}

func (c *Client) firstZone(ctx context.Context) (string, error) {
	regions, err := c.Regions(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range regions {
		if r.Name != c.Region {
			continue
		}
		for _, z := range r.AvailabilityZones {
			if z.State == "" || z.State == "available" {
				return z.ZoneName, nil
			}
		}
	}
	return c.Region + "a", nil
}

// Bundle is a Lightsail instance plan.
type Bundle struct {
	BundleID               string   `json:"bundleId"`
	Name                   string   `json:"name"`
	Price                  float64  `json:"price"` // USD a month, before tax
	CPUCount               int      `json:"cpuCount"`
	RAMSizeInGb            float64  `json:"ramSizeInGb"`
	DiskSizeInGb           int      `json:"diskSizeInGb"`
	TransferPerMonthInGb   int      `json:"transferPerMonthInGb"`
	IsActive               bool     `json:"isActive"`
	SupportedPlatforms     []string `json:"supportedPlatforms"`
	PublicIpv4AddressCount *int     `json:"publicIpv4AddressCount"`
}

// Usable reports whether keelbox can put a box on the bundle: active,
// for Linux, with a public IPv4 address (keelbox connects over IPv4 and a
// static IP needs one).
func (b Bundle) Usable() bool {
	if !b.IsActive || (b.PublicIpv4AddressCount != nil && *b.PublicIpv4AddressCount == 0) {
		return false
	}
	for _, p := range b.SupportedPlatforms {
		if p == "LINUX_UNIX" {
			return true
		}
	}
	return len(b.SupportedPlatforms) == 0
}

// Bundles lists the region's bundles.
func (c *Client) Bundles(ctx context.Context) ([]Bundle, error) {
	var r struct {
		Bundles []Bundle `json:"bundles"`
	}
	return r.Bundles, c.lightsail(ctx, &r, "get-bundles")
}

// Blueprint is a Lightsail image.
type Blueprint struct {
	BlueprintID string `json:"blueprintId"`
	Name        string `json:"name"`
	Group       string `json:"group"`
	Type        string `json:"type"`
	IsActive    bool   `json:"isActive"`
	Platform    string `json:"platform"`
}

// Blueprints lists the region's blueprints.
func (c *Client) Blueprints(ctx context.Context) ([]Blueprint, error) {
	var r struct {
		Blueprints []Blueprint `json:"blueprints"`
	}
	return r.Blueprints, c.lightsail(ctx, &r, "get-blueprints")
}

// Preflight checks, before anything is created, that the AWS CLI is signed
// in, that the region offers Lightsail, and that the bundle and blueprint
// exist there. Each failure names the fix. Empty bundle or blueprint skips
// that check.
func (c *Client) Preflight(ctx context.Context, bundle, blueprint string) (Identity, error) {
	id, err := c.Identity(ctx)
	if err != nil {
		return id, err
	}
	regions, err := c.Regions(ctx)
	if err != nil {
		return id, err
	}
	found := false
	for _, r := range regions {
		found = found || r.Name == c.Region
	}
	if !found {
		return id, fmt.Errorf("target.aws.region %q does not offer Lightsail; set region to one of: %s", c.Region, regionNames(regions))
	}
	if bundle != "" {
		bundles, err := c.Bundles(ctx)
		if err != nil {
			return id, err
		}
		offers := Offers(bundles)
		ok := false
		var some []string
		for i, o := range offers {
			ok = ok || o.Bundle.BundleID == bundle
			if i < 6 {
				some = append(some, o.Bundle.BundleID)
			}
		}
		if !ok {
			return id, fmt.Errorf("target.aws.bundle %q is not a Linux bundle with a public IPv4 address in %s; set bundle to one keelbox up lists with prices, for example: %s", bundle, c.Region, strings.Join(some, ", "))
		}
	}
	if blueprint != "" {
		bps, err := c.Blueprints(ctx)
		if err != nil {
			return id, err
		}
		ok := false
		var ubuntu []string
		for _, b := range bps {
			if !b.IsActive || b.Type != "os" {
				continue
			}
			ok = ok || b.BlueprintID == blueprint
			if strings.HasPrefix(b.BlueprintID, "ubuntu") {
				ubuntu = append(ubuntu, b.BlueprintID)
			}
		}
		if !ok {
			return id, fmt.Errorf("target.aws.blueprint %q is not an active OS blueprint in %s; set blueprint to an Ubuntu 24.04 or newer one: %s", blueprint, c.Region, strings.Join(ubuntu, ", "))
		}
	}
	return id, nil
}
