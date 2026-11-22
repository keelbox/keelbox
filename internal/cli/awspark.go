package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/provider/aws"
)

// The AWS (Lightsail) side of keelbox resize, park and resume
// (docs/adr/0071-resize-park-resume.md). A parked box is an instance
// snapshot. Lightsail cannot change an instance's bundle, so a resize
// replaces the instance with one created from a snapshot of its disk, under
// the same name and with the same static IP, and only ever to a larger
// plan.

func (a *awsCloud) sizeKey() string { return "target.aws.bundle" }

// hostKeysFile makes resize and park check the box first: Lightsail takes
// no cloud-config as user data, so only the disk can tell cloud-init on a
// new instance to keep the SSH host keys.
func (a *awsCloud) hostKeysFile() (path, why string) {
	return modules.CloudInitKeepFile, "Without it a new Lightsail instance makes new SSH host keys, which this computer would refuse"
}

// resizeWarning is what a resize on Lightsail does besides changing the
// size, said before the go-ahead.
func (a *awsCloud) resizeWarning() string {
	return fmt.Sprintf("Lightsail cannot change an instance's plan: keelbox replaces the instance with a new one made from a snapshot of its disk. "+
		"The disk grows to the new plan's size, and %s cannot be made smaller again afterwards", a.cfg.Box)
}

func (a *awsCloud) sizes(ctx context.Context, srv *cloudServer) ([]cloudSize, string, error) {
	in := srv.raw.(*aws.Instance)
	bundles, err := a.cl.AllBundles(ctx)
	if err != nil {
		return nil, "", err
	}
	var out []cloudSize
	for _, o := range aws.ResizeOffers(bundles, in) {
		b := o.Bundle
		// The disk is the plan's own: it grows with the plan.
		size := cloudSize{
			name: b.BundleID, cpu: fmt.Sprintf("%d vCPU", b.CPUCount), memory: fmt.Sprintf("%g GB", b.RAMSizeInGb),
			disk: b.DiskSizeInGb, more: fmt.Sprintf("%d GB transfer", b.TransferPerMonthInGb), price: b.Price, currency: "USD", small: o.Small(), current: b.BundleID == in.BundleID,
		}
		if size.current {
			size.disk = max(size.disk, in.DiskGB())
		}
		out = append(out, size)
	}
	note := fmt.Sprintf("Prices are estimates from the Lightsail API for %s, in US dollars a month before tax, and AWS bills by the hour up to that. "+
		"Lightsail changes a size by creating the instance again from a snapshot of its disk, and only on a plan at least as large: "+
		"the disk grows with the plan, and %s cannot be made smaller again afterwards. "+sizeAdvice, a.cl.Region, a.cfg.Box)
	return out, note, nil
}

func (a *awsCloud) stop(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	return aws.Stop(ctx, a.cl, a.cfg.Box, srv.raw.(*aws.Instance), logf)
}

func (a *awsCloud) start(ctx context.Context, srv *cloudServer) (*cloudServer, error) {
	in, err := aws.Start(ctx, a.cl, a.cfg.Box, srv.raw.(*aws.Instance))
	if err != nil {
		return nil, err
	}
	return a.server(in), nil
}

// resize returns the instance that replaced srv, running: Lightsail boots
// an instance it creates. When no instance could be created again, the box
// is left parked and the error says so.
func (a *awsCloud) resize(ctx context.Context, srv *cloudServer, size string, logf func(string, ...any)) (*cloudServer, error) {
	in, err := aws.Resize(ctx, a.cl, a.cfg, srv.raw.(*aws.Instance), size, logf)
	var left *aws.LeftParked
	if errors.As(err, &left) {
		return nil, &errLeftParked{parked: a.parkedSnapshot(left.Snapshot), cause: left.Cause}
	}
	if err != nil {
		return nil, err
	}
	return a.server(in), nil
}

// typical is what the commands took on a small_3_0 instance: a resize 8
// minutes 42 seconds, because the instance is replaced, a park about two
// minutes and a resume about one and a half.
func (a *awsCloud) typical() cloudTimes {
	return cloudTimes{resize: "about 9 minutes", park: "2 minutes", resume: "1 to 2 minutes"}
}

// parkCost has the instance's price from the Lightsail API and none for
// the snapshot, which the API does not report. A price that cannot be read
// is left out; parking does not depend on it.
func (a *awsCloud) parkCost(ctx context.Context, srv *cloudServer) (parkBill, error) {
	in := srv.raw.(*aws.Instance)
	bill := parkBill{saves: "the server's price"}
	if bundles, err := a.cl.AllBundles(ctx); err == nil {
		for _, b := range bundles {
			if b.BundleID == in.BundleID && b.Price > 0 {
				bill.saves, bill.server = serverPrice("USD", b.Price, 0)
				bill.saves += ", before tax"
			}
		}
	}
	bill.saves += ". Its static IP is released, so that is not billed either"
	bill.billed = "the snapshot, at Lightsail's price per GB a month for the whole disk"
	if gb := in.DiskGB(); gb > 0 {
		bill.billed += fmt.Sprintf(", %d GB, full or not", gb)
	}
	bill.billed += ". The Lightsail API gives no price for it"
	return bill, nil
}

func (a *awsCloud) parkedSnapshot(s *aws.Snapshot) *cloudParked {
	if s == nil {
		return nil
	}
	return &cloudParked{ref: "snapshot " + s.Name, since: s.CreatedAt.Time, detail: fmt.Sprintf("%d GB disk", s.SizeInGb), raw: s}
}

func (a *awsCloud) snapshot(ctx context.Context, srv *cloudServer, _ func(string, ...any)) (*cloudParked, error) {
	s, err := aws.ParkSnapshot(ctx, a.cl, a.cfg, srv.raw.(*aws.Instance))
	if err != nil {
		return nil, err
	}
	return a.parkedSnapshot(s), nil
}

// release deletes the instance, and its firewall with it, and releases the
// static IP: Lightsail bills one that is attached to nothing.
func (a *awsCloud) release(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	done, err := aws.Destroy(ctx, a.cl, srv.raw.(*aws.Instance), a.cfg.Box)
	logChanges(logf, done)
	return err
}

func (a *awsCloud) parked(ctx context.Context) (*cloudParked, error) {
	s, err := aws.Parked(ctx, a.cl, a.cfg.Box)
	if err != nil {
		return nil, err
	}
	return a.parkedSnapshot(s), nil
}

func (a *awsCloud) restore(ctx context.Context, p *cloudParked, logf func(string, ...any)) (*cloudServer, error) {
	in, err := aws.Restore(ctx, a.cl, a.cfg, p.raw.(*aws.Snapshot), logf)
	return a.server(in), err
}

func (a *awsCloud) discard(ctx context.Context, p *cloudParked, logf func(string, ...any)) error {
	name := p.raw.(*aws.Snapshot).Name
	if err := aws.DeleteSnapshot(ctx, a.cl, a.cfg.Box, name); err != nil {
		return err
	}
	logf("deleted snapshot %s", name)
	return nil
}
