package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/keelbox/keelbox/internal/provider/gcp"
)

// Google Cloud's side of keelbox resize, park and resume
// (docs/adr/0071-resize-park-resume.md): a stopped VM's machine type
// changes with its disk and its reserved address left as they are, and a
// parked box is a snapshot of its boot disk. Google Cloud bills a disk
// and a reserved address without a VM, so park gives up both; resume
// reserves another address.

func (g *gcpCloud) sizeKey() string { return "target.gcp.machine_type" }

func (g *gcpCloud) sizes(ctx context.Context, srv *cloudServer) ([]cloudSize, string, error) {
	in := srv.raw.(*gcp.Instance)
	types, err := g.cl.MachineTypes(ctx, in.ZoneName())
	if err != nil {
		return nil, "", err
	}
	// The disk is the VM's at every size: a machine type has none.
	disk := 0
	if d := in.BootDisk(); d != nil {
		disk, _ = strconv.Atoi(d.DiskSizeGb)
	}
	var out []cloudSize
	for _, o := range gcp.ResizeOffers(types, in.TypeName()) {
		cpu := fmt.Sprintf("%d vCPU", o.Type.GuestCPUs)
		if o.Type.IsSharedCPU {
			cpu += " (shared)"
		}
		out = append(out, cloudSize{
			name: o.Type.Name, cpu: cpu, memory: fmt.Sprintf("%g GB", float64(o.Type.MemoryMB)/1024),
			disk: disk, small: o.Small(), current: o.Type.Name == in.TypeName(),
		})
	}
	note := fmt.Sprintf("Machine types in %s, smallest first. gcloud prints no prices; Google's are at %s, billed by the second. The disk and the address stay as they are at every size, so you can change back from any size listed. "+sizeAdvice,
		in.ZoneName(), gcp.PricingURL)
	return out, note, nil
}

func (g *gcpCloud) stop(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	return gcp.Stop(ctx, g.cl, g.cfg.Box, srv.raw.(*gcp.Instance), logf)
}

func (g *gcpCloud) start(ctx context.Context, srv *cloudServer) (*cloudServer, error) {
	in, err := gcp.Start(ctx, g.cl, g.cfg.Box, srv.raw.(*gcp.Instance))
	if err != nil {
		return nil, err
	}
	return gcpServer(in), nil
}

func (g *gcpCloud) resize(ctx context.Context, srv *cloudServer, size string, _ func(string, ...any)) (*cloudServer, error) {
	in, err := gcp.Resize(ctx, g.cl, g.cfg.Box, srv.raw.(*gcp.Instance), size)
	if err != nil {
		return nil, err
	}
	return gcpServer(in), nil
}

// typical is what the commands took on an e2-small VM with a 20 GB disk: a
// resize two and a half to three minutes, a park about two and a half, a
// resume about two.
func (g *gcpCloud) typical() cloudTimes {
	return cloudTimes{resize: "2 to 3 minutes", park: "2 to 3 minutes", resume: "2 minutes"}
}

// parkCost has no prices, because gcloud has none, and names Google's
// price lists instead. It also stops, before anything changes, at a VM
// park could not keep whole: one with a disk beside its boot disk.
func (g *gcpCloud) parkCost(_ context.Context, srv *cloudServer) (parkBill, error) {
	if err := gcp.Parkable(g.cl, g.cfg.Box, srv.raw.(*gcp.Instance)); err != nil {
		return parkBill{}, err
	}
	return parkBill{
		saves:  "the server's price, and those of its disk and its reserved address, which are given up with it. gcloud prints no prices; Google's are at " + gcp.PricingURL,
		billed: "the snapshot, at Google's price per GB a month for what the disk holds. gcloud prints no price for it either; see " + gcp.SnapshotPricingURL,
	}, nil
}

func gcpParked(s *gcp.Snapshot) *cloudParked {
	if s == nil {
		return nil
	}
	return &cloudParked{ref: "snapshot " + s.Name, since: s.Created(), detail: s.Describe(), raw: s}
}

func (g *gcpCloud) snapshot(ctx context.Context, srv *cloudServer, _ func(string, ...any)) (*cloudParked, error) {
	s, err := gcp.TakeSnapshot(ctx, g.cl, g.cfg, srv.raw.(*gcp.Instance))
	if err != nil {
		return nil, err
	}
	return gcpParked(s), nil
}

// release deletes the VM with its disk and releases the reserved address;
// the two firewall rules stay for the VM resume creates.
func (g *gcpCloud) release(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	done, err := gcp.Release(ctx, g.cl, g.cfg.Box, srv.raw.(*gcp.Instance))
	logChanges(logf, done)
	return err
}

func (g *gcpCloud) parked(ctx context.Context) (*cloudParked, error) {
	s, err := gcp.Parked(ctx, g.cl, g.cfg.Box)
	if err != nil {
		return nil, err
	}
	return gcpParked(s), nil
}

func (g *gcpCloud) restore(ctx context.Context, p *cloudParked, logf func(string, ...any)) (*cloudServer, error) {
	in, err := gcp.Restore(ctx, g.cl, g.cfg, p.raw.(*gcp.Snapshot), logf)
	return gcpServer(in), err
}

func (g *gcpCloud) discard(ctx context.Context, p *cloudParked, logf func(string, ...any)) error {
	s := p.raw.(*gcp.Snapshot)
	if err := gcp.DeleteSnapshot(ctx, g.cl, g.cfg.Box, s); err != nil {
		return err
	}
	logf("deleted snapshot %s", s.Name)
	return nil
}
