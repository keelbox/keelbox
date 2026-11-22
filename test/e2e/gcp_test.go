//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/gcp"
)

// --- Google Cloud -----------------------------------------------------------

// gcpTarget creates the box as a Compute Engine VM through the gcloud on
// this machine, signed in already (KEELBOX_E2E_GCP_PROJECT names the
// project, default gcloud's own; KEELBOX_E2E_GCP_ZONE the zone, default
// europe-west3-a).
type gcpTarget struct {
	cl      *gcp.Client
	zone    string
	mtype   string
	created bool
}

// gcpE2EType is the smallest machine type the full run fits on: Docker,
// the agent CLIs and a headless browser need 2 GB.
const gcpE2EType = "e2-small"

func newGCP(t *testing.T) *gcpTarget {
	t.Helper()
	if !gcp.Installed() {
		t.Skipf("e2e pending: %v", gcp.ErrNoCLI)
	}
	zone := os.Getenv("KEELBOX_E2E_GCP_ZONE")
	if zone == "" {
		zone = "europe-west3-a"
	}
	g := &gcpTarget{cl: gcp.New(os.Getenv("KEELBOX_E2E_GCP_PROJECT")), zone: zone, mtype: gcpE2EType}
	ctx := context.Background()
	acct, err := g.cl.Preflight(ctx, zone, g.mtype)
	if err != nil {
		t.Skipf("e2e pending: %v", err)
	}
	g.sweep(t)
	left, err := g.e2eInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) >= 2 {
		t.Fatalf("%d VMs labelled %s already exist; refusing to exceed 2", len(left), e2eLabel)
	}
	ensureKey(t)
	t.Logf("gcp: %s, %s in %s (gcloud prints no price; see %s)", acct.Describe(), g.mtype, zone, gcp.PricingURL)
	return g
}

func (g *gcpTarget) CreateApplies() bool { return true }

func (g *gcpTarget) Create(t *testing.T, box string) (string, string, string) {
	g.created = true
	logf(t, "target gcp: machine type %s, zone %s, project %s", g.mtype, g.zone, g.cl.Project)
	// The VM boots with the admin account; root never takes a login, which
	// the isolation checks assert.
	return fmt.Sprintf(`target:
  provider: gcp
  ssh:
    identity_file: %s
  gcp:
    project: %s
    zone: %s
    machine_type: %s
    disk_gb: 20
    labels: {%s: "true"}`, keyPath, g.cl.Project, g.zone, g.mtype, e2eLabel), "root", ""
}

func (g *gcpTarget) Destroy(t *testing.T, box string) {
	if out, err := kb(t, "destroy", filepath.Join(workDir, box+".yaml"), "--yes", "--discard-work"); err != nil {
		t.Errorf("destroy: %v\n%s", err, out)
	}
	g.sweep(t)
}

func (g *gcpTarget) SizeKey() string { return "machine_type:" }
func (g *gcpTarget) Size() string    { return g.mtype }

// BiggerSize is the next size up that keelbox resize offers the box: the
// first in its list, which is smallest first, with more vCPUs or memory.
func (g *gcpTarget) BiggerSize(t *testing.T) string {
	types, err := g.cl.MachineTypes(context.Background(), g.zone)
	if err != nil {
		t.Fatal(err)
	}
	offers := gcp.ResizeOffers(types, g.mtype)
	var own *gcp.MachineType
	for i := range offers {
		if offers[i].Type.Name == g.mtype {
			own = &offers[i].Type
		}
	}
	for _, o := range offers {
		if own != nil && o.Type.Name != g.mtype && o.Type.GuestCPUs >= own.GuestCPUs && o.Type.MemoryMB >= own.MemoryMB {
			return o.Type.Name
		}
	}
	t.Fatalf("no machine type bigger than %s is offered in %s", g.mtype, g.zone)
	return ""
}

// Holds counts the box's VMs, its parked snapshots (in any state) and its
// firewall, which is the pair of rules. Google Cloud bills a disk and an
// unused reserved address, so with no VM it also checks that neither is
// left: a parked box is to cost its snapshot only.
func (g *gcpTarget) Holds(t *testing.T, box string) (servers, snapshots, firewalls int) {
	ctx := context.Background()
	vms, err := g.cl.InstancesWhere(ctx, "labels."+provider.LabelBox+"="+box)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range vms {
		if in.Managed(box) {
			servers++
		}
	}
	snaps, err := gcp.Snapshots(ctx, g.cl, box)
	if err != nil {
		t.Fatal(err)
	}
	f, err := gcp.FindFirewall(ctx, g.cl, box)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case f.Allow != nil && f.Deny != nil:
		firewalls = 1
	case f.Allow != nil || f.Deny != nil:
		t.Errorf("only one of the two firewall rules of %s is there", box)
	}
	addr, err := gcp.FindAddress(ctx, g.cl, box)
	if err != nil {
		t.Fatal(err)
	}
	// The boot disk has the VM's name and no labels of its own.
	out, err := exec.Command("gcloud", "compute", "disks", "list", "--project", g.cl.Project, "--filter", "name="+box, "--format", "value(name)").Output()
	if err != nil {
		t.Fatalf("list disks: %v", err)
	}
	disks := len(strings.Fields(string(out)))
	if servers == 0 && (addr != nil || disks != 0) {
		t.Errorf("no VM for %s, but Google Cloud still holds %d disk(s) named after it and the address %v: both are billed", box, disks, addr)
	}
	if servers == 1 && (addr == nil || len(addr.Users) != 1 || disks != 1) {
		t.Errorf("the VM %s should have one disk and its reserved address in use: %d disk(s), address %+v", box, disks, addr)
	}
	return servers, len(snaps), firewalls
}

// e2eInstances lists the VMs labelled by both keelbox and this harness.
func (g *gcpTarget) e2eInstances(ctx context.Context) ([]gcp.Instance, error) {
	all, err := g.cl.InstancesWhere(ctx, "labels."+e2eLabel+"=true")
	if err != nil {
		return nil, err
	}
	var out []gcp.Instance
	for _, in := range all {
		if in.Labels[e2eLabel] == "true" && in.Managed(in.Name) {
			out = append(out, in)
		}
	}
	return out, nil
}

// sweep deletes every VM this harness labelled, whatever run made it,
// with its firewall rules and address, then the parked snapshots it
// labelled, then the rules and addresses of this harness's boxes that no
// VM is behind any more (they carry no labels; theirs are named
// keelbox-<VMPrefix>N... and described as keelbox's own, which gcp.Sweep
// checks).
func (g *gcpTarget) sweep(t *testing.T) {
	ctx := context.Background()
	left, err := g.e2eInstances(ctx)
	if err != nil {
		t.Errorf("sweep list: %v", err)
		return
	}
	for i := range left {
		in := left[i]
		logf(t, "sweep: deleting VM %s (%s)", in.Name, in.ZoneName())
		if _, err := gcp.Destroy(ctx, g.cl, &in, in.Name); err != nil {
			t.Errorf("sweep delete %s: %v", in.Name, err)
		}
	}
	// Parked snapshots: a run that failed between keelbox park and
	// keelbox resume leaves one, and it is billed until deleted. They
	// carry the labels of the box, the harness's among them.
	snaps, err := g.cl.SnapshotsWhere(ctx, "labels."+e2eLabel+"=true")
	if err != nil {
		t.Errorf("sweep snapshots: %v", err)
		return
	}
	boxes := map[string]bool{}
	for i := range snaps {
		s := snaps[i]
		box := s.Labels[provider.LabelBox]
		if s.Labels[e2eLabel] != "true" || !s.Parked(box) {
			continue
		}
		logf(t, "sweep: deleting snapshot %s", s.Name)
		if err := gcp.DeleteSnapshot(ctx, g.cl, box, &s); err != nil {
			t.Errorf("sweep delete snapshot %s: %v", s.Name, err)
		}
		// Its rules, which park kept, go below.
		boxes[box] = true
	}
	rules, err := g.cl.Rules(ctx)
	if err != nil {
		t.Errorf("sweep firewall rules: %v", err)
		return
	}
	for _, r := range rules {
		box := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(r.Name, "keelbox-"), "-allow"), "-deny")
		if box != r.Name && strings.HasPrefix(box, VMPrefix) {
			boxes[box] = true
		}
	}
	for box := range boxes {
		// Rules with a VM behind them belong to a run in progress; Sweep
		// leaves those, and anything keelbox did not make.
		if done, err := gcp.Sweep(ctx, g.cl, box); err == nil {
			for _, d := range done {
				logf(t, "sweep: %s", d)
			}
		}
	}
}

func (g *gcpTarget) Spend() string {
	if !g.created {
		return ""
	}
	// gcloud prints no price, and this harness does not make one up.
	return fmt.Sprintf("not known: one %s VM with a 20 GB disk and a reserved address in %s (labels %s=true, %s=true), billed by the second; Google's prices: %s",
		g.mtype, g.zone, provider.LabelManaged, e2eLabel, gcp.PricingURL)
}
