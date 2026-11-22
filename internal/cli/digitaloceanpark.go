package cli

import (
	"context"
	"fmt"

	"github.com/keelbox/keelbox/internal/provider/digitalocean"
)

// DigitalOcean's side of keelbox resize, park and resume
// (docs/adr/0071-resize-park-resume.md): the Droplet's size changes with
// its disk left as it is, and a parked box is a snapshot of that disk. A
// Droplet that is only powered off is billed like a running one, so park
// deletes it.

func (d *doCloud) sizeKey() string { return "target.digitalocean.size" }

func (d *doCloud) sizes(ctx context.Context, srv *cloudServer) ([]cloudSize, string, error) {
	dr := srv.raw.(*digitalocean.Droplet)
	all, err := d.cl.Sizes(ctx)
	if err != nil {
		return nil, "", err
	}
	var out []cloudSize
	for _, o := range digitalocean.ResizeOffers(all, dr) {
		s := o.Size
		// The disk is the Droplet's at every size: a size's own, larger one
		// is not taken.
		out = append(out, cloudSize{
			name: s.Slug, cpu: fmt.Sprintf("%d vCPU", s.VCPUs), memory: fmt.Sprintf("%g GB", float64(s.Memory)/1024),
			disk: dr.Disk, more: fmt.Sprintf("%g TB transfer", s.Transfer), price: s.PriceMonthly, currency: "USD", small: o.Small(), current: s.Slug == dr.SizeSlug,
		})
	}
	note := fmt.Sprintf("Prices are estimates from the DigitalOcean API for %s, in US dollars before tax. Only the vCPUs and the memory change: the disk stays %d GB at every size, so you can change back from any size listed. "+sizeAdvice, dr.Region.Slug, dr.Disk)
	return out, note, nil
}

func (d *doCloud) stop(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	return digitalocean.Stop(ctx, d.cl, d.cfg.Box, srv.raw.(*digitalocean.Droplet), logf)
}

func (d *doCloud) start(ctx context.Context, srv *cloudServer) (*cloudServer, error) {
	dr, err := digitalocean.Start(ctx, d.cl, d.cfg.Box, srv.raw.(*digitalocean.Droplet))
	if err != nil {
		return nil, err
	}
	return doServer(dr), nil
}

func (d *doCloud) resize(ctx context.Context, srv *cloudServer, size string, _ func(string, ...any)) (*cloudServer, error) {
	dr, err := digitalocean.Resize(ctx, d.cl, d.cfg.Box, srv.raw.(*digitalocean.Droplet), size)
	if err != nil {
		return nil, err
	}
	return doServer(dr), nil
}

// typical is what the commands took on a Droplet with a 60 GB disk: a
// resize about two and a half minutes, a park one and a half to two, a
// resume about two and a half.
func (d *doCloud) typical() cloudTimes {
	return cloudTimes{resize: "2 to 3 minutes", park: "2 minutes", resume: "2 to 3 minutes"}
}

// parkCost has the Droplet's price from the API and, for the snapshot,
// DigitalOcean's published price: its API has none for snapshots. A price
// that cannot be read is left out; parking does not depend on it.
func (d *doCloud) parkCost(ctx context.Context, srv *cloudServer) (parkBill, error) {
	dr := srv.raw.(*digitalocean.Droplet)
	bill := parkBill{saves: "the server's price"}
	if all, err := d.cl.Sizes(ctx); err == nil {
		for _, s := range all {
			if s.Slug == dr.SizeSlug && s.PriceMonthly > 0 {
				bill.saves, bill.server = serverPrice("USD", s.PriceMonthly, s.PriceHourly*24)
			}
		}
	}
	bill.billed = snapshotAtMost("USD", fmt.Sprintf("%.2f", digitalocean.SnapshotUSDPerGB), digitalocean.SnapshotUSDPerGB, dr.Disk) + " (an estimate from DigitalOcean's list prices, before tax)"
	return bill, nil
}

func doParked(img *digitalocean.Image) *cloudParked {
	if img == nil {
		return nil
	}
	detail := fmt.Sprintf("%.1f GB", img.SizeGigabytes)
	if img.SizeGigabytes > 0 {
		detail += fmt.Sprintf(", about USD %.2f a month", max(img.SizeGigabytes*digitalocean.SnapshotUSDPerGB, 0.01))
	}
	return &cloudParked{ref: fmt.Sprintf("snapshot %d", img.ID), since: img.Created(), detail: detail, raw: img}
}

func (d *doCloud) snapshot(ctx context.Context, srv *cloudServer, _ func(string, ...any)) (*cloudParked, error) {
	img, err := digitalocean.Snapshot(ctx, d.cl, d.cfg, srv.raw.(*digitalocean.Droplet))
	if err != nil {
		return nil, err
	}
	return doParked(img), nil
}

// release deletes the Droplet and nothing else: the firewall and the box's
// tag stay for the Droplet resume creates, and the address goes with the
// Droplet.
func (d *doCloud) release(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	dr := srv.raw.(*digitalocean.Droplet)
	if err := digitalocean.Release(ctx, d.cl, dr, d.cfg.Box); err != nil {
		return err
	}
	logf("deleted Droplet %s (id %d)", dr.Name, dr.ID)
	return nil
}

func (d *doCloud) parked(ctx context.Context) (*cloudParked, error) {
	img, err := digitalocean.Parked(ctx, d.cl, d.cfg.Box)
	if err != nil {
		return nil, err
	}
	return doParked(img), nil
}

func (d *doCloud) restore(ctx context.Context, p *cloudParked, logf func(string, ...any)) (*cloudServer, error) {
	dr, err := digitalocean.Restore(ctx, d.cl, d.cfg, p.raw.(*digitalocean.Image), logf)
	return doServer(dr), err
}

func (d *doCloud) discard(ctx context.Context, p *cloudParked, logf func(string, ...any)) error {
	img := p.raw.(*digitalocean.Image)
	if err := digitalocean.DeleteSnapshot(ctx, d.cl, d.cfg.Box, img); err != nil {
		return err
	}
	logf("deleted snapshot %d", img.ID)
	return nil
}
