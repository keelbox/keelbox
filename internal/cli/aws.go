package cli

import (
	"context"
	"fmt"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/aws"
)

// AWS boxes (docs/adr/0065-aws-through-the-aws-cli.md): keelbox runs the
// user's own AWS CLI and holds no AWS credential. The config names a
// profile and a region; the sign-in is whatever that profile already has.

// awsRunner runs the aws command, and awsInstalled reports whether it is
// on PATH; tests replace both with a fake.
var (
	awsRunner    aws.Runner = aws.Exec{}
	awsInstalled            = aws.Installed
)

func awsClient(profile, region string) *aws.Client {
	return &aws.Client{Profile: profile, Region: region, Run: awsRunner}
}

// openAWS is the box's AWS client, once the AWS CLI is there and signed in.
func openAWS(ctx context.Context, cfg *config.Config) (*awsCloud, error) {
	if !awsInstalled() {
		return nil, aws.ErrNoCLI
	}
	cl := awsClient(cfg.Target.AWS.Profile, cfg.Target.AWS.Region)
	if _, err := cl.Identity(ctx); err != nil {
		return nil, fmt.Errorf("%w; then run the command again", err)
	}
	return &awsCloud{cl: cl, cfg: cfg}, nil
}

// awsCloud is AWS as a cloud: a Lightsail instance per box, with its
// firewall and a static IP.
type awsCloud struct {
	cl  *aws.Client
	cfg *config.Config
}

func (a *awsCloud) title() string { return "aws" }

// Lightsail's Ubuntu images log in as ubuntu, with passwordless sudo.
func (a *awsCloud) bootstrapUser() string { return aws.BootstrapUser }

func (a *awsCloud) server(in *aws.Instance) *cloudServer {
	if in == nil {
		return nil
	}
	return &cloudServer{
		ref:   "instance " + in.Name,
		about: fmt.Sprintf("Lightsail instance %s (%s, %s)", in.Name, a.cl.Region, in.PublicIPAddress),
		ip:    in.PublicIPAddress, stopped: in.State.Name == "stopped", raw: in,
	}
}

func (a *awsCloud) create(ctx context.Context, hk provider.HostKeyPair, logf func(string, ...any)) (*cloudServer, error) {
	in, err := aws.Create(ctx, a.cl, a.cfg, hk, logf)
	return a.server(in), err
}

func (a *awsCloud) find(ctx context.Context) (*cloudServer, error) {
	in, err := aws.Find(ctx, a.cl, a.cfg.Box)
	if err != nil {
		return nil, err
	}
	return a.server(in), nil
}

func (a *awsCloud) destroy(ctx context.Context, srv *cloudServer, logf func(string, ...any)) error {
	done, err := aws.Destroy(ctx, a.cl, srv.raw.(*aws.Instance), a.cfg.Box)
	logChanges(logf, done)
	if err != nil {
		return err
	}
	// A snapshot an interrupted park or resize left goes with the box.
	return a.sweepSnapshots(ctx, logf)
}

// sweepSnapshots deletes every snapshot keelbox made of the box: each is
// a copy of its disk, and billed.
func (a *awsCloud) sweepSnapshots(ctx context.Context, logf func(string, ...any)) error {
	snaps, err := aws.SweepSnapshots(ctx, a.cl, a.cfg.Box)
	for _, name := range snaps {
		logf("deleted snapshot %s", name)
	}
	if err != nil {
		return fmt.Errorf("a snapshot of %s could not be deleted: %w. It is billed until it is: keelbox destroy %s", a.cfg.Box, err, a.cfg.Box)
	}
	return nil
}

func (a *awsCloud) sweep(ctx context.Context, logf func(string, ...any)) {
	if err := a.sweepSnapshots(ctx, logf); err != nil {
		logf("warning: %v", err)
	}
	if ip, err := aws.ReleaseStaticIP(ctx, a.cl, a.cfg.Box); err == nil && ip != nil {
		logf("released static IP %s (%s)", ip.Name, ip.IPAddress)
	}
}

func (a *awsCloud) firewall(ctx context.Context, _ *cloudServer, publicSSH *bool) ([]string, string, error) {
	changes, ports, err := aws.EnsurePorts(ctx, a.cl, a.cfg.Box, publicSSH)
	if err != nil {
		return changes, "", err
	}
	return changes, "of instance " + a.cfg.Box + ": " + aws.DescribePorts(ports), nil
}

func (a *awsCloud) doctor(ctx context.Context, _ *cloudServer, publicSSH func() (bool, error), row func(level, check, detail string)) int {
	box, code := a.cfg.Box, 0
	ports, err := a.cl.Ports(ctx, box)
	if err != nil {
		row("FAIL", "cloud-firewall", err.Error())
		return 1
	}
	open, err := publicSSH()
	switch {
	case err != nil:
		row("warn", "cloud-firewall", "instance firewall ("+aws.DescribePorts(ports)+"); could not read the box's SSH state: "+err.Error())
	case !aws.SamePorts(ports, aws.PortRules(open)):
		row("FAIL", "cloud-firewall", "instance firewall ("+aws.DescribePorts(ports)+") differs from what the box needs ("+aws.DescribePorts(aws.PortRules(open))+"); keelbox apply sets it")
		code = 1
	case open:
		row("ok", "cloud-firewall", "instance firewall: "+aws.DescribePorts(ports)+"; SSH admitted until the tailnet is proven")
	default:
		row("ok", "cloud-firewall", "instance firewall: "+aws.DescribePorts(ports)+"; no public SSH")
	}
	name := aws.StaticIPName(box)
	ip, err := a.cl.StaticIP(ctx, name)
	switch {
	case err != nil:
		row("warn", "static-ip", err.Error())
	case ip == nil || ip.AttachedTo != box:
		row("warn", "static-ip", fmt.Sprintf("no static IP %s attached: the public address changes when the instance stops and starts. Attach one: aws lightsail allocate-static-ip --static-ip-name %s --region %s, then aws lightsail attach-static-ip --static-ip-name %s --instance-name %s --region %s",
			name, name, a.cl.Region, name, box, a.cl.Region))
	default:
		row("ok", "static-ip", name+" ("+ip.IPAddress+") attached")
	}
	return code
}
