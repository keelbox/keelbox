package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/hetzner"
)

// hetznerClient is an API client with the box's token (hetznerToken).
func hetznerClient(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, out io.Writer) (*hetzner.Client, error) {
	t, err := hetznerToken(ctx, cfg, boxRef(cfg, o.configFile), p, out)
	if err != nil {
		return nil, err
	}
	return newHetznerClient(t), nil
}

// hetznerCloud is Hetzner Cloud as a cloud: a server per box, with its
// Cloud Firewall (docs/adr/0022-hetzner-cloud-firewall.md).
type hetznerCloud struct {
	cl  *hetzner.Client
	cfg *config.Config
}

func (h *hetznerCloud) title() string { return "hetzner" }

// root is the only account a fresh Hetzner image has.
func (h *hetznerCloud) bootstrapUser() string { return "root" }

func hetznerServer(s *hetzner.Server) *cloudServer {
	if s == nil {
		return nil
	}
	ip := s.PublicNet.IPv4.IP
	return &cloudServer{
		ref:   fmt.Sprintf("server %d", s.ID),
		about: fmt.Sprintf("server %s (id %d, %s)", s.Name, s.ID, ip),
		ip:    ip, stopped: s.Status == "off", raw: s,
	}
}

func (h *hetznerCloud) create(ctx context.Context, hk provider.HostKeyPair, logf func(string, ...any)) (*cloudServer, error) {
	srv, err := hetzner.Create(ctx, h.cl, h.cfg, hk, logf)
	return hetznerServer(srv), err
}

func (h *hetznerCloud) find(ctx context.Context) (*cloudServer, error) {
	srv, err := hetzner.Find(ctx, h.cl, h.cfg.Box)
	if err != nil {
		return nil, err
	}
	return hetznerServer(srv), nil
}

func (h *hetznerCloud) destroy(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	s := srv.raw.(*hetzner.Server)
	if err := hetzner.Destroy(ctx, h.cl, s); err != nil {
		return err
	}
	logf("deleted %s (id %d)", s.Name, s.ID)
	f, err := hetzner.DestroyFirewall(ctx, h.cl, h.cfg.Box)
	if err != nil {
		return fmt.Errorf("cloud firewall: %w", err)
	}
	if f != nil {
		logf("deleted firewall %s (id %d)", f.Name, f.ID)
	}
	return nil
}

// sweep removes what is left of a box without a server: its firewall, and
// any snapshot labelled as its parked one, such as one an interrupted
// park left unfinished.
func (h *hetznerCloud) sweep(ctx context.Context, logf func(string, ...any)) {
	if imgs, err := hetzner.SweepSnapshots(ctx, h.cl, h.cfg.Box); err != nil {
		logf("could not delete a snapshot of %s: %v", h.cfg.Box, err)
	} else {
		for _, img := range imgs {
			logf("deleted snapshot %d", img.ID)
		}
	}
	if f, err := hetzner.DestroyFirewall(ctx, h.cl, h.cfg.Box); err == nil && f != nil {
		logf("deleted firewall %s (id %d)", f.Name, f.ID)
	}
}

func (h *hetznerCloud) firewall(ctx context.Context, srv *cloudServer, publicSSH *bool) ([]string, string, error) {
	f, changes, err := hetzner.EnsureFirewall(ctx, h.cl, h.cfg, srv.raw.(*hetzner.Server).ID, publicSSH)
	if err != nil {
		return changes, "", err
	}
	return changes, f.Name + ": " + hetzner.DescribeRules(f.Rules), nil
}

func (h *hetznerCloud) doctor(ctx context.Context, srv *cloudServer, publicSSH func() (bool, error), row func(level, check, detail string)) int {
	f, err := hetzner.FindFirewall(ctx, h.cl, h.cfg.Box)
	switch {
	case err != nil:
		row("FAIL", "cloud-firewall", err.Error())
		return 1
	case f == nil:
		row("FAIL", "cloud-firewall", "no firewall labelled for "+h.cfg.Box+"; keelbox apply creates it")
		return 1
	case !f.AppliedToServer(srv.raw.(*hetzner.Server).ID):
		row("FAIL", "cloud-firewall", f.Name+" is not attached to the server; keelbox apply attaches it")
		return 1
	}
	open, err := publicSSH()
	if err != nil {
		row("warn", "cloud-firewall", f.Name+" attached ("+hetzner.DescribeRules(f.Rules)+"); could not read the box's SSH state: "+err.Error())
		return 0
	}
	if !hetzner.SameRules(f.Rules, hetzner.FirewallRules(open)) {
		row("FAIL", "cloud-firewall", f.Name+" rules ("+hetzner.DescribeRules(f.Rules)+") differ from what the box needs ("+hetzner.DescribeRules(hetzner.FirewallRules(open))+"); keelbox apply sets them")
		return 1
	}
	ssh := "SSH admitted until the tailnet is proven"
	if !open {
		ssh = "no public SSH"
	}
	row("ok", "cloud-firewall", f.Name+" attached: "+hetzner.DescribeRules(f.Rules)+"; "+ssh)
	return 0
}
