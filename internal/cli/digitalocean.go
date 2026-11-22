package cli

import (
	"context"
	"fmt"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/digitalocean"
)

// DigitalOcean boxes (docs/adr/0067-digitalocean-through-doctl.md):
// keelbox runs the user's own doctl and holds no DigitalOcean credential.
// The config names a doctl context; the sign-in is the token that context
// already has.

// doRunner runs the doctl command, and doInstalled reports whether it is
// on PATH; tests replace both with a fake.
var (
	doRunner    digitalocean.Runner = digitalocean.Exec{}
	doInstalled                     = digitalocean.Installed
)

func doClient(context string) *digitalocean.Client {
	return &digitalocean.Client{Context: context, Run: doRunner}
}

// openDigitalOcean is the box's DigitalOcean client, once doctl is there
// and signed in.
func openDigitalOcean(ctx context.Context, cfg *config.Config) (*doCloud, error) {
	if !doInstalled() {
		return nil, digitalocean.ErrNoCLI
	}
	cl := doClient(cfg.Target.DigitalOcean.Context)
	if _, err := cl.Account(ctx); err != nil {
		return nil, fmt.Errorf("%w; then run the command again", err)
	}
	return &doCloud{cl: cl, cfg: cfg}, nil
}

// doCloud is DigitalOcean as a cloud: a Droplet per box, behind a Cloud
// Firewall that applies to the box's tag.
type doCloud struct {
	cl  *digitalocean.Client
	cfg *config.Config
}

func (d *doCloud) title() string { return "digitalocean" }

// The Droplet boots with the admin account itself (its user data creates
// it), so there is no image account to log in as and turn off.
func (d *doCloud) bootstrapUser() string { return "" }

func doServer(dr *digitalocean.Droplet) *cloudServer {
	if dr == nil {
		return nil
	}
	ref := "Droplet " + dr.Name
	if dr.ID != 0 {
		ref = fmt.Sprintf("Droplet %d", dr.ID)
	}
	ip := dr.PublicIPv4()
	return &cloudServer{
		ref:   ref,
		about: fmt.Sprintf("Droplet %s (id %d, %s, %s)", dr.Name, dr.ID, dr.Region.Slug, ip),
		ip:    ip, stopped: dr.Status == "off", raw: dr,
	}
}

func (d *doCloud) create(ctx context.Context, hk provider.HostKeyPair, logf func(string, ...any)) (*cloudServer, error) {
	dr, err := digitalocean.Create(ctx, d.cl, d.cfg, hk, logf)
	return doServer(dr), err
}

func (d *doCloud) find(ctx context.Context) (*cloudServer, error) {
	dr, err := digitalocean.Find(ctx, d.cl, d.cfg.Box)
	if err != nil {
		return nil, err
	}
	return doServer(dr), nil
}

func (d *doCloud) destroy(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	done, err := digitalocean.Destroy(ctx, d.cl, srv.raw.(*digitalocean.Droplet), d.cfg.Box)
	logChanges(logf, done)
	return err
}

func (d *doCloud) sweep(ctx context.Context, logf func(string, ...any)) {
	// The snapshots first: the box's tag stays while one carries it.
	imgs, err := digitalocean.SweepSnapshots(ctx, d.cl, d.cfg.Box)
	for _, img := range imgs {
		logf("deleted snapshot %d", img.ID)
	}
	if err != nil {
		logf("could not delete a snapshot of %s: %v", d.cfg.Box, err)
	}
	if f, err := digitalocean.Sweep(ctx, d.cl, d.cfg.Box); err == nil && f != nil {
		logf("deleted firewall %s (id %s)", f.Name, f.ID)
	}
}

func (d *doCloud) firewall(ctx context.Context, _ *cloudServer, publicSSH *bool) ([]string, string, error) {
	f, changes, err := digitalocean.EnsureFirewall(ctx, d.cl, d.cfg.Box, publicSSH)
	if err != nil {
		return changes, "", err
	}
	return changes, f.Name + ": " + digitalocean.DescribeRules(f.Inbound), nil
}

func (d *doCloud) doctor(ctx context.Context, srv *cloudServer, publicSSH func() (bool, error), row func(level, check, detail string)) int {
	box := d.cfg.Box
	tag := digitalocean.BoxTag(box)
	f, err := digitalocean.FindFirewall(ctx, d.cl, box)
	switch {
	case err != nil:
		row("FAIL", "cloud-firewall", err.Error())
		return 1
	case f == nil:
		row("FAIL", "cloud-firewall", "no firewall named "+digitalocean.FirewallName(box)+"; keelbox apply creates it")
		return 1
	case !srv.raw.(*digitalocean.Droplet).HasTag(tag):
		// Find returns only a Droplet with the tag; kept as a check of its own.
		row("FAIL", "cloud-firewall", f.Name+" applies to the tag "+tag+", which the Droplet does not carry")
		return 1
	case f.Status == "failed":
		row("FAIL", "cloud-firewall", f.Name+": DigitalOcean reports that applying it failed; keelbox apply sets it again")
		return 1
	}
	rules := digitalocean.DescribeRules(f.Inbound)
	open, err := publicSSH()
	if err != nil {
		row("warn", "cloud-firewall", f.Name+" applies to tag "+tag+" ("+rules+"); could not read the box's SSH state: "+err.Error())
		return 0
	}
	if !digitalocean.SameRules(f.Inbound, digitalocean.InboundRules(open)) || !digitalocean.SameRules(f.Outbound, digitalocean.OutboundRules()) {
		row("FAIL", "cloud-firewall", f.Name+" rules ("+rules+") differ from what the box needs ("+digitalocean.DescribeRules(digitalocean.InboundRules(open))+", all outbound); keelbox apply sets them")
		return 1
	}
	ssh := "SSH admitted until the tailnet is proven"
	if !open {
		ssh = "no public SSH"
	}
	row("ok", "cloud-firewall", f.Name+" applies to tag "+tag+": "+rules+"; "+ssh)
	return 0
}
