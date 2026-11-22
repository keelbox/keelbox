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

	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/aws"
)

// --- AWS (Lightsail) -------------------------------------------------------

// awsTarget creates the box as a Lightsail instance through the AWS CLI on
// this machine, signed in already (KEELBOX_E2E_AWS_PROFILE names the
// profile, KEELBOX_E2E_AWS_REGION the region, default eu-central-1).
type awsTarget struct {
	cl      *aws.Client
	bundle  string
	monthly float64
	start   time.Time
}

// awsHoursPerMonth turns Lightsail's monthly price into its hourly rate.
const awsHoursPerMonth = 730

func newAWS(t *testing.T) *awsTarget {
	t.Helper()
	if !aws.Installed() {
		t.Skipf("e2e pending: %v", aws.ErrNoCLI)
	}
	region := os.Getenv("KEELBOX_E2E_AWS_REGION")
	if region == "" {
		region = "eu-central-1"
	}
	a := &awsTarget{cl: aws.New(os.Getenv("KEELBOX_E2E_AWS_PROFILE"), region)}
	ctx := context.Background()
	id, err := a.cl.Preflight(ctx, "", "")
	if err != nil {
		t.Skipf("e2e pending: %v", err)
	}
	a.sweep(t)
	left, err := a.e2eInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) >= 2 {
		t.Fatalf("%d %s instances already exist in %s; refusing to exceed 2", len(left), e2eLabel, region)
	}
	bundles, err := a.cl.Bundles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The cheapest bundle the full run fits on: Docker, the agent CLIs and
	// a headless browser need 2 GB.
	for _, o := range aws.Offers(bundles) {
		if o.Bundle.CPUCount >= 2 && o.Bundle.RAMSizeInGb >= 2 {
			a.bundle, a.monthly = o.Bundle.BundleID, o.Bundle.Price
			break
		}
	}
	if a.bundle == "" {
		t.Fatalf("no Linux bundle with 2 vCPU and 2 GB in %s", region)
	}
	ensureKey(t)
	t.Logf("aws: account %s, %s in %s, USD %.2f/month", id.Account, a.bundle, region, a.monthly)
	return a
}

func (a *awsTarget) CreateApplies() bool { return true }

func (a *awsTarget) Create(t *testing.T, box string) (string, string, string) {
	a.start = time.Now()
	logf(t, "target aws: bundle %s, region %s, USD %.2f/month before tax", a.bundle, a.cl.Region, a.monthly)
	profile := ""
	if a.cl.Profile != "" {
		profile = "\n    profile: " + a.cl.Profile
	}
	return fmt.Sprintf(`target:
  provider: aws
  ssh:
    identity_file: %s
  aws:
    region: %s%s
    bundle: %s
    tags: {%s: "true"}`, keyPath, a.cl.Region, profile, a.bundle, e2eLabel), aws.BootstrapUser, ""
}

func (a *awsTarget) Destroy(t *testing.T, box string) {
	if out, err := kb(t, "destroy", filepath.Join(workDir, box+".yaml"), "--yes", "--discard-work"); err != nil {
		t.Errorf("destroy: %v\n%s", err, out)
	}
	a.sweep(t)
}

func (a *awsTarget) SizeKey() string { return "bundle:" }
func (a *awsTarget) Size() string    { return a.bundle }

// CannotShrink: Lightsail creates an instance from a snapshot only on a
// plan at least as large, so a box that was made bigger stays bigger.
func (a *awsTarget) CannotShrink() bool { return true }

// BiggerSize is the cheapest bundle the box can change to that has more
// vCPUs or memory.
func (a *awsTarget) BiggerSize(t *testing.T) string {
	bundles, err := a.cl.AllBundles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	offers := aws.ResizeOffers(bundles, &aws.Instance{BundleID: a.bundle})
	var own aws.Bundle
	for _, o := range offers {
		if o.Bundle.BundleID == a.bundle {
			own = o.Bundle
		}
	}
	for _, o := range offers {
		if b := o.Bundle; b.BundleID != a.bundle && (b.CPUCount > own.CPUCount || b.RAMSizeInGb > own.RAMSizeInGb) {
			return b.BundleID
		}
	}
	t.Fatalf("no bundle bigger than %s in %s", a.bundle, a.cl.Region)
	return ""
}

// Holds counts what Lightsail holds for the box by keelbox's tags. An
// instance's firewall is part of the instance, so the third number is the
// instances plus the parked snapshots that record whether the firewall
// admitted SSH, which is what the instance resume creates is given. It
// also checks what has no count of its own: the static IP is there
// exactly while there is an instance, and attached to it.
func (a *awsTarget) Holds(t *testing.T, box string) (servers, snapshots, firewalls int) {
	ctx := context.Background()
	all, err := a.cl.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range all {
		if in.Managed(box) {
			servers++
			firewalls++
		}
	}
	snaps, err := a.cl.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snaps {
		if !s.Parked(box) {
			continue
		}
		snapshots++
		if v := s.Labels()[aws.TagPublicSSH]; v == "open" || v == "closed" {
			firewalls++
		}
	}
	ip, err := a.cl.StaticIP(ctx, aws.StaticIPName(box))
	switch {
	case err != nil:
		t.Fatal(err)
	case servers == 0 && ip != nil:
		t.Errorf("%s has no instance, but its static IP %s (%s) is still allocated", box, ip.Name, ip.IPAddress)
	case servers > 0 && (ip == nil || ip.AttachedTo != box):
		t.Errorf("%s has an instance, but its static IP is not attached to it: %+v", box, ip)
	}
	return servers, snapshots, firewalls
}

// e2eInstances lists the instances tagged by both keelbox and this harness.
func (a *awsTarget) e2eInstances(ctx context.Context) ([]aws.Instance, error) {
	all, err := a.cl.Instances(ctx)
	if err != nil {
		return nil, err
	}
	var out []aws.Instance
	for _, in := range all {
		if l := in.Labels(); l[e2eLabel] == "true" && l[provider.LabelManaged] == "true" && l[provider.LabelBox] == in.Name {
			out = append(out, in)
		}
	}
	return out, nil
}

// sweep deletes every instance this harness tagged, whatever run made it,
// and the snapshots keelbox park and keelbox resize made of them, and
// releases the static IPs of this harness's boxes that are attached to
// nothing (static IPs carry no tags; theirs are named keelbox-<VMPrefix>N).
func (a *awsTarget) sweep(t *testing.T) {
	ctx := context.Background()
	left, err := a.e2eInstances(ctx)
	if err != nil {
		t.Errorf("sweep list: %v", err)
		return
	}
	for i := range left {
		in := left[i]
		logf(t, "sweep: deleting instance %s", in.Name)
		if _, err := aws.Destroy(ctx, a.cl, &in, in.Name); err != nil {
			t.Errorf("sweep delete %s: %v", in.Name, err)
		}
	}
	// Snapshots: a run that failed between keelbox park and keelbox resume,
	// or in the middle of a resize, leaves one, and it is billed until
	// deleted.
	snaps, err := a.cl.Snapshots(ctx)
	if err != nil {
		t.Errorf("sweep snapshots: %v", err)
		return
	}
	swept := map[string]bool{}
	for _, s := range snaps {
		l := s.Labels()
		box := l[provider.LabelBox]
		if l[e2eLabel] != "true" || l[provider.LabelManaged] != "true" || !strings.HasPrefix(box, VMPrefix) || swept[box] {
			continue
		}
		swept[box] = true
		done, err := aws.SweepSnapshots(ctx, a.cl, box)
		for _, name := range done {
			logf(t, "sweep: deleted snapshot %s", name)
		}
		if err != nil {
			t.Errorf("sweep snapshots of %s: %v", box, err)
		}
	}
	ips, err := a.cl.StaticIPs(ctx)
	if err != nil {
		t.Errorf("sweep static IPs: %v", err)
		return
	}
	for _, ip := range ips {
		box := strings.TrimPrefix(ip.Name, "keelbox-")
		if ip.IsAttached || box == ip.Name || !strings.HasPrefix(box, VMPrefix) {
			continue
		}
		logf(t, "sweep: releasing static IP %s (%s)", ip.Name, ip.IPAddress)
		if _, err := aws.ReleaseStaticIP(ctx, a.cl, box); err != nil {
			t.Errorf("sweep release %s: %v", ip.Name, err)
		}
	}
}

func (a *awsTarget) Spend() string {
	if a.start.IsZero() {
		return ""
	}
	hours := math.Max(1, math.Ceil(time.Since(a.start).Hours()))
	hourly := a.monthly / awsHoursPerMonth
	return fmt.Sprintf("USD %.4f before tax (%s, %.0f billed hour(s) at about USD %.4f)", hours*hourly, a.bundle, hours, hourly)
}
