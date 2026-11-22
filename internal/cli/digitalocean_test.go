package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider/digitalocean"
	"github.com/keelbox/keelbox/internal/provider/digitalocean/dotest"
)

// fakeDO puts a fake doctl, signed in, behind keelbox's DigitalOcean calls.
func fakeDO(t *testing.T) *dotest.CLI {
	t.Helper()
	cli := dotest.New()
	oldRunner, oldInstalled := doRunner, doInstalled
	doRunner, doInstalled = cli, func() bool { return true }
	t.Cleanup(func() { doRunner, doInstalled = oldRunner, oldInstalled })
	return cli
}

func doCfg(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: digitalocean, digitalocean: {region: fra1, size: s-2vcpu-4gb, context: work}}\n"+
		"users: {admin: {ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey sam@laptop\"]}}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// doBox is a keelbox-made Droplet acme behind its firewall.
func doBox(cli *dotest.CLI) *dotest.Droplet {
	d := cli.AddDroplet("acme", "keelbox", "keelbox-box-acme")
	cli.Firewalls["fw-1"] = &dotest.Firewall{ID: "fw-1", Name: "keelbox-acme", Tags: []string{"keelbox-box-acme"},
		Inbound:  []string{"udp:41641:0.0.0.0/0,::/0", "tcp:22:0.0.0.0/0,::/0"},
		Outbound: []string{"icmp::0.0.0.0/0,::/0", "tcp:0:0.0.0.0/0,::/0", "udp:0:0.0.0.0/0,::/0"}}
	return d
}

func TestUpDigitalOceanWithFlags(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	cli := fakeDO(t)
	r := &fakeRunner{signed: true}
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--location", "fra1", "--doctl-context", "work", "--ssh-key", key, "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	d := cfg.Target.DigitalOcean
	if d.Region != "fra1" || d.Context != "work" || d.Size != "s-2vcpu-4gb" || d.Image != "ubuntu-24-04-x64" {
		t.Errorf("digitalocean target %+v", d)
	}
	if st.Via != viaDO || st.Stage != stageReady || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
		t.Errorf("state %+v calls %v", st, r.calls)
	}
	// Asking changed nothing in the account.
	for _, c := range cli.Calls() {
		if f := strings.Fields(c); f[len(f)-1] != "get" && f[2] != "list" {
			t.Errorf("up's questions ran %q", c)
		}
	}
}

func TestUpDigitalOceanInteractive(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeDO(t)
	in := script(
		"3",    // where: a new cloud server
		"3",    // which cloud: DigitalOcean
		"acme", // name
		"",     // context: the current one
		"",     // region: the first
		"",     // size: default
		"",     // agents: both
		"",     // GitHub CLI: yes
		"",     // Tailscale: yes
		"",     // go ahead
		"n",    // Remote Control
	)
	code, out := upWith(t, &fakeRunner{signed: true}, in, "up")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"3) A new cloud server", "Which cloud?", "3) DigitalOcean", "uses your doctl sign-in",
		"signed in to DigitalOcean: team Sam's team as sam@example.com",
		"fra1", "Frankfurt 1", "s-2vcpu-4gb", "USD 24.00/month", "a new DigitalOcean Droplet, s-2vcpu-4gb in fra1, ubuntu-24-04-x64",
		"USD 24.00 a month", "only Droplets tagged keelbox",
		"https://keelbox.dev/docs/guides/credentials#digitalocean-nothing-kept",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "s-2vcpu-4gb-old") || strings.Contains(out, "nyc2") || strings.Contains(out, "c-2 ") {
		t.Errorf("offered what cannot be ordered\n%s", out)
	}
	cfg, _ := loadBox(t, "acme")
	if d := cfg.Target.DigitalOcean; d.Region != "fra1" || d.Context != "" || d.Size != "s-2vcpu-4gb" {
		t.Errorf("digitalocean target %+v", d)
	}
}

// Without a terminal, up stops before any change: exit 3 at what only the
// user can do (install doctl, sign in), exit 2 at a missing answer.
func TestUpDigitalOceanStopsBeforeAnyChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(cli *dotest.CLI)
		args   []string
		code   int
		wants  []string
		noCall bool
	}{
		{"no doctl", func(*dotest.CLI) { doInstalled = func() bool { return false } }, nil, exitNeedsYou,
			[]string{"not installed or not on PATH", digitalocean.InstallURL, `doctl auth login --scopes "account:read`, "keelbox up acme"}, true},
		{"not signed in", func(c *dotest.CLI) { c.SignedIn = false }, []string{"--doctl-context", "work"}, exitNeedsYou,
			[]string{"not signed in to DigitalOcean", "no token for the doctl context work", "doctl auth init --context work", "keelbox up acme"}, false},
		{"token refused", func(c *dotest.CLI) { c.Refused = true }, nil, exitNeedsYou, []string{"refused the token", "doctl auth init", "keelbox up acme"}, false},
		{"no region", func(*dotest.CLI) {}, nil, 2, []string{"the DigitalOcean region", "--location", "keelbox up acme --provider digitalocean --location fra1 --yes"}, false},
		{"unknown region", func(*dotest.CLI) {}, []string{"--location", "nyc2"}, 2, []string{"nyc2", "fra1, nyc3"}, false},
		{"unknown size", func(*dotest.CLI) {}, []string{"--location", "fra1", "--size", "c-2"}, 2, []string{"c-2", "s-1vcpu-1gb, s-2vcpu-2gb, s-2vcpu-4gb, s-4vcpu-8gb", "doctl compute size list"}, false},
		{"name taken", func(c *dotest.CLI) { c.AddDroplet("acme") }, []string{"--location", "fra1"}, 2, []string{"a Droplet named acme already exists", "keelbox up acme-2"}, false},
		{"bad context", func(*dotest.CLI) {}, []string{"--doctl-context", "-x"}, 2, []string{"--doctl-context", "doctl auth list"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolate(t)
			key := laptopKey(t, home)
			cli := fakeDO(t)
			tc.set(cli)
			r := &fakeRunner{}
			args := append([]string{"up", "acme", "--provider", "digitalocean", "--ssh-key", key, "--yes"}, tc.args...)
			code, out := upWith(t, r, nil, args...)
			if code != tc.code {
				t.Errorf("exit %d, want %d\n%s", code, tc.code, out)
			}
			for _, w := range tc.wants {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q\n%s", w, out)
				}
			}
			if len(r.calls) != 0 || isRegistered("acme") {
				t.Errorf("something changed: %v", r.calls)
			}
			if tc.noCall && len(cli.Calls()) != 0 {
				t.Errorf("ran doctl: %v", cli.Calls())
			}
		})
	}
}

// A create that failed after the Droplet existed is continued as the
// admin: the Droplet has no other account to log in as.
func TestUpDigitalOceanResumesAsTheAdmin(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	fakeDO(t)
	r := &fakeRunner{createCode: 1, onCreate: func(o options) { o.hooks.server("203.0.113.44") }}
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--location", "nyc3", "--size", "c-2", "--ssh-key", key, "--yes")
	if code != 1 || !strings.Contains(out, "keelbox up acme") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.DigitalOcean.Region != "nyc3" || cfg.Target.DigitalOcean.Size != "c-2" || st.Stage != stageServer || st.Address != "203.0.113.44" || st.Bootstrap != "" {
		t.Fatalf("config %+v state %+v", cfg.Target.DigitalOcean, st)
	}
	r.calls, r.rootReachable = nil, true
	code, out = upWith(t, r, nil, "up", "acme")
	if code != exitNeedsYou || strings.Join(r.calls, "; ") != "apply acme as admin; signed-in?" {
		t.Errorf("resume: %d %v\n%s", code, r.calls, out)
	}
}

// The size menu stays short however many sizes DigitalOcean sells, and
// keeps the default in view.
func TestDigitalOceanSizeMenu(t *testing.T) {
	var offers []digitalocean.Offer
	for i := 0; i < 40; i++ {
		offers = append(offers, digitalocean.Offer{Size: digitalocean.Size{Slug: fmt.Sprintf("s-%d", i), PriceMonthly: float64(i)}})
	}
	for _, def := range []int{0, 3, 20, 39} {
		short, d := doMenu(offers, def)
		if len(short) == 0 || len(short) > doMenuMax || short[d].Size.Slug != offers[def].Size.Slug {
			t.Errorf("default %d: %d shown, default at %d", def, len(short), d)
		}
	}
	if short, _ := doMenu(offers[:doMenuMax], 2); short != nil {
		t.Error("a list that fits was shortened")
	}
}

func doDestroy(t *testing.T, o options, input string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	logf := func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) }
	cfg := doCfg(t)
	cl, err := openCloud(context.Background(), cfg, o, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	return cloudDestroy(context.Background(), cl, cfg, o, strings.NewReader(input), logf, &out, &out), out.String()
}

// destroy goes through the same work check and confirmation as on
// Hetzner, then deletes the Droplet, its firewall and keelbox's tags.
func TestDigitalOceanDestroy(t *testing.T) {
	isolate(t)
	cli := fakeDO(t)
	d := doBox(cli)
	asked := stubProjects(t, cleanBox, nil, true)
	code, out := doDestroy(t, options{}, "acme\n")
	if code != 0 || len(cli.Droplets) != 0 || len(cli.Firewalls) != 0 || len(cli.Tags) != 0 {
		t.Fatalf("exit %d, left %v %v %v\n%s", code, cli.Droplets, cli.Firewalls, cli.Tags, out)
	}
	for _, want := range []string{
		fmt.Sprintf("Delete Droplet acme (id %d, fra1, %s) and everything on it? Type the box name to confirm: ", d.ID, d.IP),
		fmt.Sprintf("keelbox: deleted Droplet acme (id %d)\n", d.ID), "keelbox: deleted firewall keelbox-acme (id fw-1)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if *asked != d.IP {
		t.Errorf("work check looked at %q", *asked)
	}
}

func TestDigitalOceanDestroyKeepsWorkAndOtherPeoplesDroplets(t *testing.T) {
	isolate(t)
	cli := fakeDO(t)
	d := doBox(cli)
	// Work only on the box, nobody to ask: refused, --yes or not.
	stubProjects(t, workOnBox, nil, false)
	code, out := doDestroy(t, options{yes: true}, "")
	if code != 1 || cli.Called("compute droplet delete") || !strings.Contains(out, "keelbox destroy acme --yes --discard-work") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// The wrong name typed: nothing deleted.
	stubProjects(t, cleanBox, nil, true)
	if code, out := doDestroy(t, options{}, "acme2\n"); code != 1 || cli.Called("compute droplet delete") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// A Droplet of the box's name without keelbox's tags is not touched,
	// and neither is a firewall while a Droplet carries the box's tag.
	d.Tags = []string{"keelbox-box-acme", "owner:sam"}
	code, out = doDestroy(t, options{yes: true, discardWork: true}, "")
	if code != 1 || !strings.Contains(out, "not created by keelbox") || cli.Called("compute droplet delete") || cli.Called("compute firewall delete") || cli.Called("compute tag delete") {
		t.Errorf("exit %d %v\n%s", code, cli.Calls(), out)
	}
}

// With no Droplet, destroy still removes the firewall and the tag an
// interrupted create left.
func TestDigitalOceanDestroySweepsALeftoverFirewall(t *testing.T) {
	isolate(t)
	cli := fakeDO(t)
	d := doBox(cli)
	delete(cli.Droplets, d.ID)
	code, out := doDestroy(t, options{yes: true}, "")
	if code != 1 || len(cli.Firewalls) != 0 || len(cli.Tags) != 0 || !strings.Contains(out, "deleted firewall keelbox-acme (id fw-1)") || !strings.Contains(out, "no Droplet named acme") {
		t.Errorf("exit %d, tags %v\n%s", code, cli.Tags, out)
	}
}

func TestOpenDigitalOceanNamesTheFix(t *testing.T) {
	cli := fakeDO(t)
	cli.SignedIn = false
	_, err := openCloud(context.Background(), doCfg(t), options{}, nil, nil)
	if !errors.Is(err, digitalocean.ErrNotSignedIn) || !strings.Contains(err.Error(), "doctl auth init --context work") {
		t.Errorf("%v", err)
	}
	doInstalled = func() bool { return false }
	if _, err := openCloud(context.Background(), doCfg(t), options{}, nil, nil); !errors.Is(err, digitalocean.ErrNoCLI) {
		t.Errorf("%v", err)
	}
}

func TestDigitalOceanDoctorAndFirewall(t *testing.T) {
	cli := fakeDO(t)
	d := cli.AddDroplet("acme", "keelbox", "keelbox-box-acme")
	cfg := doCfg(t)
	cl, err := openCloud(context.Background(), cfg, options{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cl.title() != "digitalocean" || cl.bootstrapUser() != "" {
		t.Errorf("title %q, bootstrap %q", cl.title(), cl.bootstrapUser())
	}
	ctx := context.Background()
	srv, err := cl.find(ctx)
	if err != nil || srv.ip != d.IP || srv.ref != fmt.Sprintf("Droplet %d", d.ID) {
		t.Fatalf("%+v %v", srv, err)
	}
	doctor := func(open bool) (int, string) {
		var b strings.Builder
		code := cl.doctor(ctx, srv, func() (bool, error) { return open, nil }, func(l, c, d string) { fmt.Fprintf(&b, "%s %s %s\n", l, c, d) })
		return code, b.String()
	}
	// No firewall yet: doctor fails until apply.
	if code, out := doctor(true); code != 1 || !strings.Contains(out, "FAIL cloud-firewall no firewall named keelbox-acme; keelbox apply creates it") {
		t.Errorf("%d %s", code, out)
	}
	// apply first only makes sure it exists; a new one admits SSH.
	changes, summary, err := cl.firewall(ctx, srv, nil)
	if err != nil || len(changes) != 1 || summary != "keelbox-acme: in tcp 22, in udp 41641" {
		t.Fatalf("%v %q %v", changes, summary, err)
	}
	if code, out := doctor(true); code != 0 || !strings.Contains(out, "ok cloud-firewall keelbox-acme applies to tag keelbox-box-acme: in tcp 22, in udp 41641; SSH admitted until the tailnet is proven") {
		t.Errorf("%d %s", code, out)
	}
	closed := false
	changes, summary, err = cl.firewall(ctx, srv, &closed)
	if err != nil || len(changes) != 1 || summary != "keelbox-acme: in udp 41641" {
		t.Fatalf("%v %q %v", changes, summary, err)
	}
	if code, out := doctor(false); code != 0 || !strings.Contains(out, "in udp 41641; no public SSH") {
		t.Errorf("%d %s", code, out)
	}
	// The box opened public SSH again (the tailnet proof lapsed): the
	// firewall no longer matches.
	if code, out := doctor(true); code != 1 || !strings.Contains(out, "differ from what the box needs (in tcp 22, in udp 41641, all outbound)") {
		t.Errorf("%d %s", code, out)
	}
	// Outbound rules removed by hand: the box could reach nothing.
	for _, f := range cli.Firewalls {
		f.Outbound = nil
	}
	if code, out := doctor(false); code != 1 || !strings.Contains(out, "keelbox apply sets them") {
		t.Errorf("%d %s", code, out)
	}
}

func TestStatusAllPublicFindsADigitalOceanBox(t *testing.T) {
	isolate(t)
	cli := fakeDO(t)
	d := doBox(cli)
	a, err := statusAllPublic(context.Background(), doCfg(t))
	if err != nil || a.host != d.IP {
		t.Errorf("%+v %v", a, err)
	}
	delete(cli.Droplets, d.ID)
	if _, err := statusAllPublic(context.Background(), doCfg(t)); err == nil || !strings.Contains(err.Error(), "finding it in DigitalOcean") {
		t.Errorf("%v", err)
	}
}
