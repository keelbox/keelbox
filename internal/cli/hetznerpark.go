package cli

import (
	"context"
	"fmt"

	"github.com/keelbox/keelbox/internal/provider/hetzner"
)

// Hetzner's side of keelbox resize, park and resume
// (docs/adr/0071-resize-park-resume.md): the server's type changes with
// its disk left as it is, and a parked box is a snapshot of that disk.

func (h *hetznerCloud) sizeKey() string { return "target.hetzner.server_type" }

func (h *hetznerCloud) sizes(ctx context.Context, srv *cloudServer) ([]cloudSize, string, error) {
	s := srv.raw.(*hetzner.Server)
	types, err := h.cl.ServerTypes(ctx)
	if err != nil {
		return nil, "", err
	}
	location := h.cfg.Target.Hetzner.Location
	if l := s.LocationName(); l != "" {
		location = l
	}
	var out []cloudSize
	for _, o := range hetzner.ResizeOffers(types, s, location) {
		cpu := "shared"
		if o.Type.CPUType == "dedicated" {
			cpu = "dedicated"
		}
		// The disk is the server's at every size: a type's own, larger one
		// is not taken.
		out = append(out, cloudSize{
			name: o.Type.Name, cpu: fmt.Sprintf("%d vCPU (%s)", o.Type.Cores, cpu), memory: fmt.Sprintf("%g GB", o.Type.Memory),
			disk: s.DiskGB(), price: o.Monthly, currency: "EUR", small: o.Small(), current: o.Type.Name == s.ServerType.Name,
		})
	}
	note := fmt.Sprintf("Prices are estimates from the Hetzner API for %s, with VAT, and Hetzner bills by the hour. The disk stays %d GB at every size, so you can change back from any size listed. "+sizeAdvice, location, s.DiskGB())
	return out, note, nil
}

func (h *hetznerCloud) stop(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	return hetzner.Stop(ctx, h.cl, h.cfg.Box, srv.raw.(*hetzner.Server), logf)
}

func (h *hetznerCloud) start(ctx context.Context, srv *cloudServer) (*cloudServer, error) {
	s, err := hetzner.Start(ctx, h.cl, h.cfg.Box, srv.raw.(*hetzner.Server))
	if err != nil {
		return nil, err
	}
	return hetznerServer(s), nil
}

func (h *hetznerCloud) resize(ctx context.Context, srv *cloudServer, size string, _ func(string, ...any)) (*cloudServer, error) {
	if err := hetzner.Resize(ctx, h.cl, h.cfg.Box, srv.raw.(*hetzner.Server), size); err != nil {
		return nil, err
	}
	return srv, nil
}

// typical is what the commands took on Hetzner's smallest type: a resize 61
// to 91 seconds, a park 47 to 118, a resume 58 to 131.
func (h *hetznerCloud) typical() cloudTimes {
	return cloudTimes{resize: "1 to 2 minutes", park: "1 to 2 minutes", resume: "1 to 3 minutes"}
}

// parkCost has both prices from the Hetzner API. They are a courtesy:
// parking does not depend on them, so a price that cannot be read is left
// out and nothing fails.
func (h *hetznerCloud) parkCost(ctx context.Context, srv *cloudServer) (parkBill, error) {
	s := srv.raw.(*hetzner.Server)
	bill := parkBill{saves: "the server's price", billed: "the snapshot, at Hetzner's price per GB a month for what the disk holds"}
	if types, err := h.cl.ServerTypes(ctx); err == nil {
		for _, o := range hetzner.ResizeOffers(types, s, h.cfg.Target.Hetzner.Location) {
			if o.Type.Name == s.ServerType.Name && o.Monthly > 0 {
				bill.saves, bill.server = serverPrice("EUR", o.Monthly, o.Hourly*24)
			}
		}
	}
	if price, err := h.cl.SnapshotPrice(ctx); err == nil {
		bill.billed = snapshotAtMost("EUR", fmt.Sprintf("%.4f", price), price, s.DiskGB()) + " (an estimate from Hetzner's prices, with VAT)"
	}
	return bill, nil
}

func (h *hetznerCloud) parkedImage(ctx context.Context, img *hetzner.Image) *cloudParked {
	if img == nil {
		return nil
	}
	detail := fmt.Sprintf("%.1f GB", img.ImageSize)
	if price, err := h.cl.SnapshotPrice(ctx); err == nil && img.ImageSize > 0 {
		detail += fmt.Sprintf(", about EUR %.2f a month", price*img.ImageSize)
	}
	return &cloudParked{ref: fmt.Sprintf("snapshot %d", img.ID), since: img.Created, detail: detail, raw: img}
}

func (h *hetznerCloud) snapshot(ctx context.Context, srv *cloudServer, _ func(string, ...any)) (*cloudParked, error) {
	img, err := hetzner.Snapshot(ctx, h.cl, h.cfg, srv.raw.(*hetzner.Server))
	if err != nil {
		return nil, err
	}
	return h.parkedImage(ctx, img), nil
}

// release deletes the server and nothing else: the Cloud Firewall stays
// for the server resume creates, and the address goes with the server.
func (h *hetznerCloud) release(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	s := srv.raw.(*hetzner.Server)
	if err := hetzner.Destroy(ctx, h.cl, s); err != nil {
		return err
	}
	logf("deleted %s (id %d)", s.Name, s.ID)
	return nil
}

func (h *hetznerCloud) parked(ctx context.Context) (*cloudParked, error) {
	img, err := hetzner.Parked(ctx, h.cl, h.cfg.Box)
	if err != nil {
		return nil, err
	}
	return h.parkedImage(ctx, img), nil
}

func (h *hetznerCloud) restore(ctx context.Context, p *cloudParked, logf func(string, ...any)) (*cloudServer, error) {
	srv, err := hetzner.Restore(ctx, h.cl, h.cfg, p.raw.(*hetzner.Image), logf)
	return hetznerServer(srv), err
}

func (h *hetznerCloud) discard(ctx context.Context, p *cloudParked, logf func(string, ...any)) error {
	img := p.raw.(*hetzner.Image)
	if err := hetzner.DeleteSnapshot(ctx, h.cl, h.cfg.Box, img); err != nil {
		return err
	}
	logf("deleted snapshot %d", img.ID)
	return nil
}
