package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/gcp"
)

// Google Cloud boxes (docs/adr/0068-google-cloud-through-gcloud.md):
// keelbox runs the user's own gcloud and holds no Google credential. The
// config names a project; the sign-in is the one gcloud already has.

// gcpRunner runs the gcloud command, and gcpInstalled reports whether it
// is on PATH; tests replace both with a fake.
var (
	gcpRunner    gcp.Runner = gcp.Exec{}
	gcpInstalled            = gcp.Installed
)

func gcpClient(project string) *gcp.Client {
	return &gcp.Client{Project: project, Run: gcpRunner}
}

// openGCP is the box's Google Cloud client, once gcloud is there, signed
// in, and has a project whose Compute Engine API is on.
func openGCP(ctx context.Context, cfg *config.Config) (*gcpCloud, error) {
	if !gcpInstalled() {
		return nil, gcp.ErrNoCLI
	}
	cl := gcpClient(cfg.Target.GCP.Project)
	if _, err := cl.Account(ctx); err != nil {
		return nil, fmt.Errorf("%w; then run the command again", err)
	}
	return &gcpCloud{cl: cl, cfg: cfg}, nil
}

// gcpCloud is Google Cloud as a cloud: a Compute Engine VM per box, on a
// reserved address, behind two firewall rules that apply to the box's
// network tag.
type gcpCloud struct {
	cl  *gcp.Client
	cfg *config.Config
}

func (g *gcpCloud) title() string { return "gcp" }

// The VM boots with the admin account itself (its user data creates it),
// so there is no image account to log in as and turn off.
func (g *gcpCloud) bootstrapUser() string { return "" }

func gcpServer(in *gcp.Instance) *cloudServer {
	if in == nil {
		return nil
	}
	ip := in.PublicIPv4()
	return &cloudServer{
		ref:   "VM " + in.Name,
		about: fmt.Sprintf("VM %s (%s, %s, %s)", in.Name, in.TypeName(), in.ZoneName(), ip),
		ip:    ip, stopped: in.Stopped(), raw: in,
	}
}

func (g *gcpCloud) create(ctx context.Context, hk provider.HostKeyPair, logf func(string, ...any)) (*cloudServer, error) {
	in, err := gcp.Create(ctx, g.cl, g.cfg, hk, logf)
	return gcpServer(in), err
}

func (g *gcpCloud) find(ctx context.Context) (*cloudServer, error) {
	in, err := gcp.Find(ctx, g.cl, g.cfg.Box)
	if err != nil {
		return nil, err
	}
	return gcpServer(in), nil
}

func (g *gcpCloud) destroy(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	done, err := gcp.Destroy(ctx, g.cl, srv.raw.(*gcp.Instance), g.cfg.Box)
	logChanges(logf, done)
	return err
}

// sweep removes what is left of a box without a VM: its firewall rules,
// its address, and any snapshot labelled as its parked one, such as one
// an interrupted park left unfinished.
func (g *gcpCloud) sweep(ctx context.Context, logf func(string, ...any)) {
	done, err := gcp.Sweep(ctx, g.cl, g.cfg.Box)
	logChanges(logf, done)
	if err != nil {
		logf("not everything of %s could be removed: %v", g.cfg.Box, err)
	}
}

func (g *gcpCloud) firewall(ctx context.Context, _ *cloudServer, publicSSH *bool) ([]string, string, error) {
	f, changes, err := gcp.EnsureFirewall(ctx, g.cl, g.cfg.Box, publicSSH)
	if err != nil {
		return changes, "", err
	}
	return changes, gcp.AllowRuleName(g.cfg.Box) + ": " + f.Describe(), nil
}

func (g *gcpCloud) doctor(ctx context.Context, srv *cloudServer, publicSSH func() (bool, error), row func(level, check, detail string)) int {
	box := g.cfg.Box
	tag := gcp.BoxTag(box)
	in := srv.raw.(*gcp.Instance)
	code := 0
	// What the VM's own record lets in beside the admin's key.
	if p := in.LoginProblems(); len(p) > 0 {
		row("FAIL", "cloud-login", strings.Join(p, "; ")+". Set it back in the Google Cloud console, or recreate the box")
		code = 1
	} else {
		row("ok", "cloud-login", "project SSH keys blocked, OS Login off, no SSH keys in the VM's metadata, no service account")
	}
	rules, err := g.cl.Rules(ctx)
	if err != nil {
		row("FAIL", "cloud-firewall", err.Error())
		return 1
	}
	f, err := gcp.BoxFirewall(rules, box)
	names := gcp.AllowRuleName(box) + " and " + gcp.DenyRuleName(box)
	switch {
	case err != nil:
		row("FAIL", "cloud-firewall", err.Error())
		return 1
	case f.Allow == nil || f.Deny == nil:
		row("FAIL", "cloud-firewall", "the firewall rules "+names+" are not both there; keelbox apply creates them")
		return 1
	case !in.HasTag(tag):
		row("FAIL", "cloud-firewall", names+" apply to the network tag "+tag+", which the VM does not carry; add it: gcloud compute instances add-tags "+box+" --zone "+in.ZoneName()+" --tags "+tag)
		return 1
	}
	open, err := publicSSH()
	if err != nil {
		row("warn", "cloud-firewall", names+" apply to tag "+tag+" ("+f.Describe()+"); could not read the box's SSH state: "+err.Error())
		return code
	}
	if !f.InStep(open) {
		row("FAIL", "cloud-firewall", names+" ("+f.Describe()+") differ from what the box needs ("+describeGCPRules(open)+", every other inbound packet refused); keelbox apply sets them")
		return 1
	}
	ssh := "SSH admitted until the tailnet is proven"
	if !open {
		ssh = "no public SSH"
	}
	row("ok", "cloud-firewall", names+" apply to tag "+tag+": "+f.Describe()+", everything else refused; "+ssh)
	// Rules keelbox did not write that admit traffic ahead of its deny rule.
	if by := gcp.Bypasses(rules, box); len(by) > 0 {
		row("warn", "cloud-firewall-others", fmt.Sprintf("other rules of the network %s admit traffic to the box ahead of %s (priority below %d): %s. keelbox leaves them; check them: gcloud compute firewall-rules list",
			gcp.Network, gcp.DenyRuleName(box), gcp.DenyPriority, strings.Join(by, ", ")))
	}
	return code
}

func describeGCPRules(publicSSH bool) string {
	var parts []string
	for _, s := range gcp.AllowSpecs(publicSSH) {
		proto, port, _ := strings.Cut(s, ":")
		parts = append(parts, "in "+proto+" "+port)
	}
	return strings.Join(parts, ", ")
}
