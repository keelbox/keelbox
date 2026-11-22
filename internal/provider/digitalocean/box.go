package digitalocean

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

// How keelbox knows its own Droplets. A DigitalOcean tag is one string,
// not a key and a value, so the two labels every cloud resource of
// keelbox's carries (provider.LabelManaged, provider.LabelBox) become two
// tags: "keelbox" on every Droplet keelbox creates, and
// "keelbox-box-<box>" on the Droplet of that box. Find and Destroy touch
// a Droplet only when it carries both and is named after the box.

// TagManaged is the tag on every Droplet keelbox creates.
const TagManaged = provider.LabelManaged

// BoxTag is the tag on the Droplet of a box. The box's firewall applies
// to it.
func BoxTag(box string) string { return provider.LabelBox + "-" + box }

// Tags are the tags for a box's Droplet: keelbox's own, then the user's.
func Tags(c *config.Config) []string {
	tags := []string{TagManaged, BoxTag(c.Box)}
	for _, t := range c.Target.DigitalOcean.Tags {
		if t != TagManaged && t != BoxTag(c.Box) {
			tags = append(tags, t)
		}
	}
	return tags
}

// Droplet is a DigitalOcean Droplet.
type Droplet struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	SizeSlug string   `json:"size_slug"`
	Tags     []string `json:"tags"`
	// Disk is the size of the Droplet's disk in GB. It is the disk of the
	// size the Droplet was created with: keelbox resize does not grow it.
	Disk   int `json:"disk"`
	Region struct {
		Slug string `json:"slug"`
	} `json:"region"`
	Image struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"image"`
	Networks struct {
		V4 []struct {
			IPAddress string `json:"ip_address"`
			Type      string `json:"type"`
		} `json:"v4"`
	} `json:"networks"`
}

// PublicIPv4 is the Droplet's public IPv4 address, or "".
func (d *Droplet) PublicIPv4() string {
	for _, n := range d.Networks.V4 {
		if n.Type == "public" {
			return n.IPAddress
		}
	}
	return ""
}

// HasTag reports whether the Droplet carries the tag.
func (d *Droplet) HasTag(tag string) bool {
	for _, t := range d.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Managed reports whether keelbox created the Droplet for box: it is
// named after the box and carries the tags keelbox and keelbox-box-<box>.
func (d *Droplet) Managed(box string) bool {
	return d.Name == box && d.HasTag(TagManaged) && d.HasTag(BoxTag(box))
}

// Droplet returns the Droplet with that id, or nil.
func (c *Client) Droplet(ctx context.Context, id int64) (*Droplet, error) {
	var ds []Droplet
	if err := c.compute(ctx, &ds, "droplet", "get", strconv.FormatInt(id, 10)); err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(ds) == 0 {
		return nil, nil
	}
	return &ds[0], nil
}

// DropletsTagged lists the Droplets that carry the tag. doctl follows the
// API's pages itself and prints one list.
func (c *Client) DropletsTagged(ctx context.Context, tag string) ([]Droplet, error) {
	var ds []Droplet
	return ds, c.compute(ctx, &ds, "droplet", "list", "--tag-name", tag)
}

// DropletsNamed lists the Droplets of that name; DigitalOcean allows
// several. doctl takes the name as a pattern, which a box name (letters,
// digits and hyphens) matches only literally.
func (c *Client) DropletsNamed(ctx context.Context, name string) ([]Droplet, error) {
	var ds []Droplet
	if err := c.compute(ctx, &ds, "droplet", "list", name); err != nil {
		return nil, err
	}
	var out []Droplet
	for _, d := range ds {
		if d.Name == name {
			out = append(out, d)
		}
	}
	return out, nil
}

// SSHKey is a public key stored in the DigitalOcean account.
type SSHKey struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// SSHKeys lists the account's SSH keys.
func (c *Client) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	var keys []SSHKey
	return keys, c.compute(ctx, &keys, "ssh-key", "list")
}

// keyBody is a public key line's type and key, without its comment.
func keyBody(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return ""
	}
	return f[0] + " " + f[1]
}

// accountKey is the id of an account SSH key that is one of the admin's
// keys, or 0. keelbox adds no key to the account. Naming one that is
// already there makes DigitalOcean skip the root password it otherwise
// sets and emails for a Droplet created without a key. A token that may
// not list keys is not an error: the Droplet is created without one.
func (c *Client) accountKey(ctx context.Context, adminKeys []string) int64 {
	keys, err := c.SSHKeys(ctx)
	if err != nil {
		return 0
	}
	for _, k := range keys {
		for _, a := range adminKeys {
			if b := keyBody(a); b != "" && b == keyBody(k.PublicKey) {
				return k.ID
			}
		}
	}
	return 0
}

// writeUserData puts the user data in a mode 600 file for doctl's
// --user-data-file, so the host key never appears in a command line. The
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

// poll is how often Create and Destroy ask DigitalOcean for the new
// state; tests shorten it.
var poll = 3 * time.Second

func wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Create makes the Droplet for a box and returns it once it is active
// and has a public address. The box's firewall exists before the Droplet
// and applies to its tag; DigitalOcean puts it in front of the Droplet
// some seconds after the Droplet is active. Until then a new Droplet
// answers only on what its image listens on, SSH, which a new box's
// firewall admits as well.
// Create fails if a Droplet of that name exists. A Droplet returned with
// an error exists.
func Create(ctx context.Context, cl *Client, c *config.Config, hk provider.HostKeyPair, logf func(string, ...any)) (*Droplet, error) {
	d := c.Target.DigitalOcean
	if _, err := cl.Preflight(ctx, d.Region, d.Size, d.Image); err != nil {
		return nil, err
	}
	// Names are not unique on DigitalOcean; keelbox keeps them so.
	named, err := cl.DropletsNamed(ctx, c.Box)
	if err != nil {
		return nil, err
	}
	if len(named) > 0 {
		return nil, fmt.Errorf("a Droplet named %s already exists (id %d); converge it: keelbox apply %s, or delete it first: keelbox destroy %s", c.Box, named[0].ID, c.Box, c.Box)
	}
	keys, err := c.Users.Admin.AllKeys()
	if err != nil {
		return nil, err
	}
	userData, err := provider.AdminUserData(c.Box, c.Host.Hostname, c.Users.Admin.Name, keys, hk)
	if err != nil {
		return nil, err
	}
	// A new firewall admits SSH; apply closes it once the tailnet is proven.
	_, changes, err := EnsureFirewall(ctx, cl, c.Box, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud firewall: %w", err)
	}
	for _, ch := range changes {
		logf("%s", ch)
	}
	path, err := writeUserData(userData)
	if err != nil {
		return nil, fmt.Errorf("write the user data file: %w", err)
	}
	defer func() { _ = os.Remove(path) }()

	logf("creating %s: %s in %s, %s", c.Box, d.Size, d.Region, d.Image)
	args := []string{c.Box, "--region", d.Region, "--size", d.Size, "--image", d.Image,
		"--tag-names", strings.Join(Tags(c), ","), "--user-data-file", path}
	if id := cl.accountKey(ctx, keys); id != 0 {
		args = append(args, "--ssh-keys", strconv.FormatInt(id, 10))
	}
	var made []Droplet
	if err := cl.compute(ctx, &made, "droplet", "create", args...); err != nil {
		return nil, err
	}
	if len(made) != 1 {
		return &Droplet{Name: c.Box}, fmt.Errorf("doctl compute droplet create printed %d Droplets, not one", len(made))
	}
	return cl.waitActive(ctx, &made[0])
}

func (c *Client) waitActive(ctx context.Context, d *Droplet) (*Droplet, error) {
	for i := 0; i < 100; i++ {
		cur, err := c.Droplet(ctx, d.ID)
		if err != nil {
			return d, err
		}
		if cur != nil {
			d = cur
			if cur.Status == "active" && cur.PublicIPv4() != "" {
				return cur, nil
			}
		}
		if err := wait(ctx, poll); err != nil {
			return d, err
		}
	}
	return d, fmt.Errorf("Droplet %d did not become active with a public address", d.ID)
}

// Find returns the keelbox-managed Droplet for a box.
func Find(ctx context.Context, cl *Client, box string) (*Droplet, error) {
	tagged, err := cl.DropletsTagged(ctx, BoxTag(box))
	if err != nil {
		return nil, err
	}
	var own []Droplet
	for _, d := range tagged {
		if d.Managed(box) {
			own = append(own, d)
		}
	}
	switch len(own) {
	case 1:
		return &own[0], nil
	case 0:
		named, err := cl.DropletsNamed(ctx, box)
		if err != nil {
			return nil, err
		}
		if len(named) > 0 {
			return nil, fmt.Errorf("the Droplet %s (id %d) was not created by keelbox (missing the tags %s and %s); not touching it. Use another box name, or manage it as an existing server: keelbox up %s --provider ssh --host ITS-IP",
				box, named[0].ID, TagManaged, BoxTag(box), box)
		}
		return nil, fmt.Errorf("no Droplet named %s in the DigitalOcean account; %s", box, provider.NoServerFix(box))
	}
	ids := make([]string, len(own))
	for i, d := range own {
		ids[i] = strconv.FormatInt(d.ID, 10)
	}
	return nil, fmt.Errorf("%d Droplets are named %s and tagged for the box (ids %s); keep one and delete the rest in the DigitalOcean control panel, then run the command again", len(own), box, strings.Join(ids, ", "))
}

// Destroy deletes a keelbox-managed Droplet, waits until it is gone,
// then deletes the box's firewall and the tags nothing carries any more.
// It returns what it deleted.
func Destroy(ctx context.Context, cl *Client, d *Droplet, box string) ([]string, error) {
	var done []string
	deleted, err := cl.deleteDroplet(ctx, d, box, "run keelbox destroy "+box+" again to delete its firewall")
	if deleted {
		done = append(done, fmt.Sprintf("deleted Droplet %s (id %d)", d.Name, d.ID))
	}
	if err != nil {
		return done, err
	}
	f, err := Sweep(ctx, cl, box)
	if f != nil {
		done = append(done, fmt.Sprintf("deleted firewall %s (id %s)", f.Name, f.ID))
	}
	if err != nil {
		return done, fmt.Errorf("cloud firewall: %w", err)
	}
	return done, nil
}

// deleteDroplet deletes a keelbox-managed Droplet and waits until it is
// gone. deleted reports whether DigitalOcean took the deletion; then is
// what to do when the wait runs out.
func (c *Client) deleteDroplet(ctx context.Context, d *Droplet, box, then string) (deleted bool, err error) {
	if !d.Managed(box) {
		return false, fmt.Errorf("refusing to delete Droplet %d: not named %s and tagged %s and %s", d.ID, box, TagManaged, BoxTag(box))
	}
	if err := c.compute(ctx, nil, "droplet", "delete", strconv.FormatInt(d.ID, 10), "--force"); err != nil {
		return false, err
	}
	for i := 0; ; i++ {
		cur, err := c.Droplet(ctx, d.ID)
		if err != nil {
			return true, err
		}
		if cur == nil || cur.Status == "archive" {
			return true, nil
		}
		if i == 60 {
			return true, fmt.Errorf("Droplet %d is still being deleted; when it is gone, %s", d.ID, then)
		}
		if err := wait(ctx, poll); err != nil {
			return true, err
		}
	}
}

// Sweep deletes the box's firewall and unused tags when no Droplet
// carries the box's tag: what a destroy leaves to do, and what a create
// that stopped before the Droplet existed left behind. It returns the
// firewall it deleted.
func Sweep(ctx context.Context, cl *Client, box string) (*Firewall, error) {
	left, err := cl.DropletsTagged(ctx, BoxTag(box))
	if err != nil {
		return nil, err
	}
	for _, d := range left {
		if d.Status != "archive" {
			return nil, fmt.Errorf("Droplet %s (id %d) still carries the tag %s, so the firewall %s stays", d.Name, d.ID, BoxTag(box), FirewallName(box))
		}
	}
	return DestroyFirewall(ctx, cl, box)
}

// Region is a DigitalOcean datacentre region.
type Region struct {
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Sizes     []string `json:"sizes"`
}

// Regions lists the regions that take new Droplets, by slug.
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	var all []Region
	if err := c.compute(ctx, &all, "region", "list"); err != nil {
		return nil, err
	}
	var out []Region
	for _, r := range all {
		if r.Available {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func regionSlugs(regions []Region) string {
	n := make([]string, len(regions))
	for i, r := range regions {
		n[i] = r.Slug
	}
	return strings.Join(n, ", ")
}

// Size is a Droplet size.
type Size struct {
	Slug         string   `json:"slug"`
	Memory       int      `json:"memory"` // MB
	VCPUs        int      `json:"vcpus"`
	Disk         int      `json:"disk"`          // GB
	Transfer     float64  `json:"transfer"`      // TB a month
	PriceMonthly float64  `json:"price_monthly"` // USD, before tax
	PriceHourly  float64  `json:"price_hourly"`
	Regions      []string `json:"regions"`
	Available    bool     `json:"available"`
	Description  string   `json:"description"`
}

// In reports whether the size can be ordered in the region.
func (s Size) In(region string) bool {
	if !s.Available {
		return false
	}
	for _, r := range s.Regions {
		if r == region {
			return true
		}
	}
	return false
}

// Sizes lists every Droplet size.
func (c *Client) Sizes(ctx context.Context) ([]Size, error) {
	var sizes []Size
	return sizes, c.compute(ctx, &sizes, "size", "list")
}

// Image is a DigitalOcean image.
type Image struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Distribution string   `json:"distribution"`
	Slug         string   `json:"slug"`
	Regions      []string `json:"regions"`
	Status       string   `json:"status"`
	// Type is "snapshot" for a Droplet snapshot, and Tags, MinDiskSize (the
	// smallest disk, in GB, a Droplet created from it may have),
	// SizeGigabytes (what it stores, which is what is billed) and CreatedAt
	// are what keelbox park and resume read of one.
	Type          string   `json:"type"`
	Tags          []string `json:"tags"`
	MinDiskSize   int      `json:"min_disk_size"`
	SizeGigabytes float64  `json:"size_gigabytes"`
	CreatedAt     string   `json:"created_at"`
}

// Image returns the image with that slug or id, or nil.
func (c *Client) Image(ctx context.Context, ref string) (*Image, error) {
	var imgs []Image
	if err := c.compute(ctx, &imgs, "image", "get", ref); err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(imgs) == 0 {
		return nil, nil
	}
	return &imgs[0], nil
}

// ubuntuImages are the slugs of the Ubuntu distribution images offered
// in the region, for an error to name.
func (c *Client) ubuntuImages(ctx context.Context, region string) string {
	var imgs []Image
	if err := c.compute(ctx, &imgs, "image", "list-distribution"); err != nil {
		return config.DefaultDigitalOceanImage
	}
	var slugs []string
	for _, i := range imgs {
		if i.Distribution == "Ubuntu" && i.Slug != "" && contains(i.Regions, region) {
			slugs = append(slugs, i.Slug)
		}
	}
	if len(slugs) == 0 {
		return config.DefaultDigitalOceanImage
	}
	sort.Strings(slugs)
	return strings.Join(slugs, ", ")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Preflight checks, before anything is created, that doctl is signed in,
// that the region takes new Droplets, and that the size and the image are
// offered there. Each failure names the fix. An empty region, size or
// image skips that check.
func (c *Client) Preflight(ctx context.Context, region, size, image string) (Account, error) {
	acct, err := c.Account(ctx)
	if err != nil {
		return acct, err
	}
	if region == "" {
		return acct, nil
	}
	regions, err := c.Regions(ctx)
	if err != nil {
		return acct, err
	}
	found := false
	for _, r := range regions {
		found = found || r.Slug == region
	}
	if !found {
		return acct, fmt.Errorf("target.digitalocean.region %q is not a region that takes new Droplets; set region to one of: %s", region, regionSlugs(regions))
	}
	if size != "" {
		sizes, err := c.Sizes(ctx)
		if err != nil {
			return acct, err
		}
		offers := Offers(sizes, region)
		ok := false
		var some []string
		for i, o := range offers {
			ok = ok || o.Size.Slug == size
			if i < 6 {
				some = append(some, o.Size.Slug)
			}
		}
		if !ok {
			return acct, fmt.Errorf("target.digitalocean.size %q cannot be ordered in %s; set size to one keelbox up lists with prices, for example: %s", size, region, strings.Join(some, ", "))
		}
	}
	if image != "" {
		img, err := c.Image(ctx, image)
		if err != nil {
			return acct, err
		}
		// An image of the account's own (a snapshot) lists the regions it
		// is stored in; so does a distribution image.
		if img == nil || !contains(img.Regions, region) {
			return acct, fmt.Errorf("target.digitalocean.image %q is not an image available in %s; set image to an Ubuntu 24.04 or newer one: %s", image, region, c.ubuntuImages(ctx, region))
		}
	}
	return acct, nil
}

// Offer is a size a box can run on, with its price.
type Offer struct {
	Size Size
}

// Describe is a one-line summary: cores, memory, disk, transfer and price.
func (o Offer) Describe() string {
	s := o.Size
	d := fmt.Sprintf("%d vCPU, %g GB, %d GB disk, %g TB transfer, USD %.2f/month", s.VCPUs, float64(s.Memory)/1024, s.Disk, s.Transfer, s.PriceMonthly)
	if o.Small() {
		d += "; small for agents"
	}
	return d
}

// Small is a size below 2 vCPU or 4 GB, where a dev server, tests and a
// headless browser together run out of memory.
func (o Offer) Small() bool { return o.Size.VCPUs < 2 || o.Size.Memory < 4096 }

// Offers are the sizes that can be ordered in the region, cheapest first.
func Offers(sizes []Size, region string) []Offer {
	var out []Offer
	for _, s := range sizes {
		if s.In(region) {
			out = append(out, Offer{Size: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Size.PriceMonthly != out[j].Size.PriceMonthly {
			return out[i].Size.PriceMonthly < out[j].Size.PriceMonthly
		}
		return out[i].Size.Slug < out[j].Size.Slug
	})
	return out
}

// DefaultOffer is the cheapest offer with at least 2 vCPUs and 4 GB, the
// size docs/guides/where-to-run.md gives as enough for one or two agents, else the cheapest.
func DefaultOffer(offers []Offer) int {
	for i, o := range offers {
		if !o.Small() {
			return i
		}
	}
	return 0
}
