//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/provider/digitalocean"
)

// --- DigitalOcean ----------------------------------------------------------

// doTarget creates the box as a Droplet through the doctl on this machine,
// signed in already (KEELBOX_E2E_DO_CONTEXT names the doctl context,
// KEELBOX_E2E_DO_REGION the region, default fra1).
type doTarget struct {
	cl     *digitalocean.Client
	region string
	size   string
	// spec is the size's vCPUs, memory and disk.
	spec   digitalocean.Size
	hourly float64
	start  time.Time
}

func newDigitalOcean(t *testing.T) *doTarget {
	t.Helper()
	if !digitalocean.Installed() {
		t.Skipf("e2e pending: %v", digitalocean.ErrNoCLI)
	}
	region := os.Getenv("KEELBOX_E2E_DO_REGION")
	if region == "" {
		region = "fra1"
	}
	d := &doTarget{cl: digitalocean.New(os.Getenv("KEELBOX_E2E_DO_CONTEXT")), region: region}
	ctx := context.Background()
	acct, err := d.cl.Preflight(ctx, region, "", "")
	if err != nil {
		t.Skipf("e2e pending: %v", err)
	}
	d.sweep(t)
	left, err := d.e2eDroplets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) >= 2 {
		t.Fatalf("%d Droplets tagged %s already exist; refusing to exceed 2", len(left), e2eLabel)
	}
	sizes, err := d.cl.Sizes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The cheapest size the full run fits on: Docker, the agent CLIs and a
	// headless browser need 2 GB.
	for _, o := range digitalocean.Offers(sizes, region) {
		if o.Size.VCPUs >= 2 && o.Size.Memory >= 2048 {
			d.size, d.spec, d.hourly = o.Size.Slug, o.Size, o.Size.PriceHourly
			break
		}
	}
	if d.size == "" {
		t.Fatalf("no size with 2 vCPU and 2 GB in %s", region)
	}
	ensureKey(t)
	t.Logf("digitalocean: %s, %s in %s, USD %.4f/hour", acct.Describe(), d.size, region, d.hourly)
	return d
}

func (d *doTarget) CreateApplies() bool { return true }

func (d *doTarget) Create(t *testing.T, box string) (string, string, string) {
	d.start = time.Now()
	logf(t, "target digitalocean: size %s, region %s, USD %.4f/hour before tax", d.size, d.region, d.hourly)
	context := ""
	if d.cl.Context != "" {
		context = "\n    context: " + d.cl.Context
	}
	// The Droplet boots with the admin account; root never takes a login,
	// which the isolation checks assert.
	return fmt.Sprintf(`target:
  provider: digitalocean
  ssh:
    identity_file: %s
  digitalocean:
    region: %s%s
    size: %s
    tags: [%s]`, keyPath, d.region, context, d.size, e2eLabel), "root", ""
}

func (d *doTarget) Destroy(t *testing.T, box string) {
	if out, err := kb(t, "destroy", filepath.Join(workDir, box+".yaml"), "--yes", "--discard-work"); err != nil {
		t.Errorf("destroy: %v\n%s", err, out)
	}
	d.sweep(t)
}

func (d *doTarget) SizeKey() string { return "size:" }
func (d *doTarget) Size() string    { return d.size }

// BiggerSize is the cheapest size in the region with more vCPUs or memory
// than the box's and a disk at least as large. The catalogue is read
// again: what can be ordered in a region changes.
func (d *doTarget) BiggerSize(t *testing.T) string {
	sizes, err := d.cl.Sizes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range digitalocean.Offers(sizes, d.region) {
		if o.Size.Disk >= d.spec.Disk && (o.Size.VCPUs > d.spec.VCPUs || o.Size.Memory > d.spec.Memory) && o.Size.VCPUs >= d.spec.VCPUs && o.Size.Memory >= d.spec.Memory {
			return o.Size.Slug
		}
	}
	t.Fatalf("no size bigger than %s can be ordered in %s", d.size, d.region)
	return ""
}

func (d *doTarget) Holds(t *testing.T, box string) (servers, snapshots, firewalls int) {
	ctx := context.Background()
	tagged, err := d.cl.DropletsTagged(ctx, digitalocean.BoxTag(box))
	if err != nil {
		t.Fatal(err)
	}
	for _, dr := range tagged {
		if dr.Managed(box) {
			servers++
		}
	}
	imgs, err := d.cl.UserImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range imgs {
		if img.Parked(box) {
			snapshots++
		}
	}
	f, err := digitalocean.FindFirewall(ctx, d.cl, box)
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		firewalls++
	}
	return servers, snapshots, firewalls
}

// e2eDroplets lists the Droplets tagged by both keelbox and this harness.
func (d *doTarget) e2eDroplets(ctx context.Context) ([]digitalocean.Droplet, error) {
	all, err := d.cl.DropletsTagged(ctx, e2eLabel)
	if err != nil {
		return nil, err
	}
	var out []digitalocean.Droplet
	for _, dr := range all {
		if dr.Managed(dr.Name) {
			out = append(out, dr)
		}
	}
	return out, nil
}

// sweep deletes every Droplet this harness tagged, whatever run made it,
// with its firewall and tags, then the parked snapshots of its boxes, then
// the firewalls of this harness's boxes that no Droplet is behind any
// more (firewalls carry no tags of their own; theirs are named
// keelbox-<VMPrefix>N and apply to that box's tag).
func (d *doTarget) sweep(t *testing.T) {
	ctx := context.Background()
	left, err := d.e2eDroplets(ctx)
	if err != nil {
		t.Errorf("sweep list: %v", err)
		return
	}
	for i := range left {
		dr := left[i]
		logf(t, "sweep: deleting Droplet %s (id %d)", dr.Name, dr.ID)
		if _, err := digitalocean.Destroy(ctx, d.cl, &dr, dr.Name); err != nil {
			t.Errorf("sweep delete %s: %v", dr.Name, err)
		}
	}
	// Parked snapshots: a run that failed between keelbox park and
	// keelbox resume leaves one, and it is billed until deleted.
	imgs, err := d.cl.UserImages(ctx)
	if err != nil {
		t.Errorf("sweep snapshots: %v", err)
		return
	}
	for i := range imgs {
		img := imgs[i]
		if !img.HasTag(e2eLabel) {
			continue
		}
		for _, tag := range img.Tags {
			box := strings.TrimPrefix(tag, digitalocean.BoxTag(""))
			if box == tag || !img.Parked(box) {
				continue
			}
			logf(t, "sweep: deleting snapshot %d (%s)", img.ID, img.Name)
			if err := digitalocean.DeleteSnapshot(ctx, d.cl, box, &img); err != nil {
				t.Errorf("sweep delete snapshot %d: %v", img.ID, err)
			}
		}
	}
	fws, err := d.cl.Firewalls(ctx)
	if err != nil {
		t.Errorf("sweep firewalls: %v", err)
		return
	}
	for _, f := range fws {
		box := strings.TrimPrefix(f.Name, "keelbox-")
		if box == f.Name || !strings.HasPrefix(box, VMPrefix) || !f.Targets(digitalocean.BoxTag(box)) {
			continue
		}
		// A firewall with a Droplet behind it belongs to a run in progress.
		if behind, err := d.cl.DropletsTagged(ctx, digitalocean.BoxTag(box)); err != nil || len(behind) > 0 {
			continue
		}
		if gone, err := digitalocean.Sweep(ctx, d.cl, box); err != nil {
			t.Errorf("sweep firewall %s: %v", f.Name, err)
		} else if gone != nil {
			logf(t, "sweep: deleted firewall %s", gone.Name)
		}
	}
}

func (d *doTarget) Spend() string {
	if d.start.IsZero() {
		return ""
	}
	// Billed per second; an hour is the generous estimate.
	hours := math.Max(1, math.Ceil(time.Since(d.start).Hours()))
	return fmt.Sprintf("at most USD %.4f before tax (%s, %.0f hour(s) at USD %.4f)", hours*d.hourly, d.size, hours, d.hourly)
}
