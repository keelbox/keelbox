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
	"github.com/keelbox/keelbox/internal/provider/gcp"
	"github.com/keelbox/keelbox/internal/provider/gcp/gcptest"
)

// fakeGCP puts a fake gcloud, signed in, behind keelbox's Google Cloud calls.
func fakeGCP(t *testing.T) *gcptest.CLI {
	t.Helper()
	cli := gcptest.New()
	oldRunner, oldInstalled := gcpRunner, gcpInstalled
	gcpRunner, gcpInstalled = cli, func() bool { return true }
	t.Cleanup(func() { gcpRunner, gcpInstalled = oldRunner, oldInstalled })
	return cli
}

func gcpCfg(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: gcp, gcp: {project: acme-prod, zone: europe-west3-a, machine_type: e2-medium}}\n"+
		"users: {admin: {ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey sam@laptop\"]}}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const (
	gcpAllowDesc = "Managed by keelbox: the allow rule of box acme. Deleted by keelbox destroy."
	gcpDenyDesc  = "Managed by keelbox: the deny rule of box acme. Deleted by keelbox destroy."
	gcpAddrDesc  = "Managed by keelbox: the address of box acme. Deleted by keelbox destroy."
)

// gcpBox is a keelbox-made VM acme on its reserved address, behind its
// two firewall rules.
func gcpBox(cli *gcptest.CLI) *gcptest.Instance {
	in := cli.AddInstance("acme", map[string]string{"keelbox": "true", "keelbox-box": "acme"}, "keelbox-box-acme")
	any, tag := []string{"0.0.0.0/0"}, []string{"keelbox-box-acme"}
	cli.Rules["keelbox-acme-allow"] = &gcptest.Rule{Name: "keelbox-acme-allow", Description: gcpAllowDesc, Network: "default", Action: "ALLOW",
		Specs: []string{"udp:41641", "tcp:22"}, Priority: 900, SourceRanges: any, TargetTags: tag}
	cli.Rules["keelbox-acme-deny"] = &gcptest.Rule{Name: "keelbox-acme-deny", Description: gcpDenyDesc, Network: "default", Action: "DENY",
		Specs: []string{"all"}, Priority: 901, SourceRanges: any, TargetTags: tag}
	cli.Addresses["europe-west3/keelbox-acme"] = &gcptest.Address{Name: "keelbox-acme", Region: "europe-west3", IP: in.IP, Description: gcpAddrDesc}
	return in
}

func TestUpGCPWithFlags(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	cli := fakeGCP(t)
	r := &fakeRunner{signed: true}
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "gcp", "--location", "europe-west3-b", "--gcp-project", "acme-prod", "--ssh-key", key, "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	g := cfg.Target.GCP
	if g.Project != "acme-prod" || g.Zone != "europe-west3-b" || g.MachineType != "e2-medium" || g.DiskGB != 50 {
		t.Errorf("gcp target %+v", g)
	}
	if st.Via != viaGCP || st.Stage != stageReady || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
		t.Errorf("state %+v calls %v", st, r.calls)
	}
	// Asking changed nothing in the project.
	for _, c := range cli.Calls() {
		if f := strings.Fields(c); f[1] != "list" && f[2] != "list" && f[2] != "describe" {
			t.Errorf("up's questions ran %q", c)
		}
	}
}

// Without --gcp-project and --location, up takes gcloud's own project and
// zone, and writes the project into the config.
func TestUpGCPTakesGcloudsProjectAndZone(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	cli := fakeGCP(t)
	cli.Zone = "europe-west3-b"
	code, out := upWith(t, &fakeRunner{signed: true}, nil, "up", "acme", "--provider", "gcp", "--size", "c3-standard-4", "--ssh-key", key, "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, _ := loadBox(t, "acme")
	if g := cfg.Target.GCP; g.Project != "acme-dev" || g.Zone != "europe-west3-b" || g.MachineType != "c3-standard-4" {
		t.Errorf("gcp target %+v", g)
	}
}

func TestUpGCPInteractive(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeGCP(t)
	in := script(
		"3",              // where: a new cloud server
		"4",              // which cloud: Google Cloud
		"acme",           // name
		"",               // project: gcloud's own
		"europe-west3-a", // zone
		"",               // machine type: default
		"",               // agents: both
		"",               // GitHub CLI: yes
		"",               // Tailscale: yes
		"",               // go ahead
		"n",              // Remote Control
	)
	code, out := upWith(t, &fakeRunner{signed: true}, in, "up")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"3) A new cloud server", "Which cloud?", "4) Google Cloud", "uses your gcloud sign-in",
		"Google Cloud project [acme-dev]", "signed in to Google Cloud: project acme-dev as sam@example.com",
		"e2-medium", "2 vCPU (shared), 4 GB", "small for agents",
		// No price is shown, and keelbox says so.
		"keelbox cannot show Google Cloud's prices: gcloud has no price list", gcp.PricingURL,
		"a new Google Cloud VM, e2-medium in europe-west3-a, project acme-dev, Ubuntu 24.04",
		"not shown: gcloud has no price list", "a stopped VM still pays for its disk and its address",
		"Create the server (billed by Google until you destroy it; price not shown)?",
		"only the VM labelled keelbox=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "t2a-standard-2") || strings.Contains(out, "n1-retired-2") || strings.Contains(out, "USD") || strings.Contains(out, "EUR") {
		t.Errorf("offered what cannot run a box, or a price\n%s", out)
	}
	cfg, _ := loadBox(t, "acme")
	if g := cfg.Target.GCP; g.Project != "acme-dev" || g.Zone != "europe-west3-a" || g.MachineType != "e2-medium" {
		t.Errorf("gcp target %+v", g)
	}
}

// Without a terminal, up stops before any change: exit 3 at what only the
// user can do (install gcloud, sign in, enable the API), exit 2 at a
// missing answer.
func TestUpGCPStopsBeforeAnyChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(cli *gcptest.CLI)
		args   []string
		code   int
		wants  []string
		noCall bool
	}{
		{"no gcloud", func(*gcptest.CLI) { gcpInstalled = func() bool { return false } }, nil, exitNeedsYou,
			[]string{"not installed or not on PATH", gcp.InstallURL, "gcloud auth login", "keelbox up acme"}, true},
		{"not signed in", func(c *gcptest.CLI) { c.SignedIn = false }, []string{"--location", "europe-west3-a"}, exitNeedsYou,
			[]string{"not signed in to Google Cloud", "Sign in: gcloud auth login", "keelbox up acme"}, false},
		{"sign-in expired", func(c *gcptest.CLI) { c.Expired = true }, nil, exitNeedsYou, []string{"refreshing your current auth tokens", "gcloud auth login", "keelbox up acme"}, false},
		{"API off", func(c *gcptest.CLI) { c.APIDisabled = true }, []string{"--gcp-project", "acme-prod"}, exitNeedsYou,
			[]string{"Compute Engine API is not enabled in project acme-prod", "gcloud services enable compute.googleapis.com --project acme-prod", "keelbox up acme"}, false},
		{"no project", func(c *gcptest.CLI) { c.Project = "" }, nil, 2, []string{"the Google Cloud project", "--gcp-project", "keelbox up acme --provider gcp --gcp-project my-project --location europe-west3-a --yes"}, false},
		{"bad project", func(*gcptest.CLI) {}, []string{"--gcp-project", "-x"}, 2, []string{"--gcp-project", "gcloud projects list"}, false},
		{"unknown project", func(*gcptest.CLI) {}, []string{"--gcp-project", "acme-typo"}, 2, []string{"cannot use Compute Engine in project acme-typo", "roles/compute.admin"}, false},
		{"no zone", func(*gcptest.CLI) {}, nil, 2, []string{"the Google Cloud zone", "--location", "keelbox up acme --provider gcp --location europe-west3-a --yes"}, false},
		{"unknown zone", func(*gcptest.CLI) {}, []string{"--location", "europe-west3-z"}, 2, []string{"europe-west3-z", "europe-west3-a, europe-west3-b"}, false},
		{"unknown type", func(*gcptest.CLI) {}, []string{"--location", "europe-west3-a", "--size", "e9-huge"}, 2,
			[]string{"e9-huge", "e2-micro, e2-small, e2-medium, e2-standard-2, e2-standard-4", "gcloud compute machine-types list --zones europe-west3-a"}, false},
		{"name taken", func(c *gcptest.CLI) { c.AddInstance("acme", nil) }, []string{"--location", "europe-west3-b"}, 2, []string{"a VM named acme already exists in europe-west3-a", "keelbox up acme-2"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolate(t)
			key := laptopKey(t, home)
			cli := fakeGCP(t)
			tc.set(cli)
			r := &fakeRunner{}
			args := append([]string{"up", "acme", "--provider", "gcp", "--ssh-key", key, "--yes"}, tc.args...)
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
				t.Errorf("ran gcloud: %v", cli.Calls())
			}
		})
	}
}

// A create that failed after the VM existed is continued as the admin:
// the VM has no other account to log in as.
func TestUpGCPResumesAsTheAdmin(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	fakeGCP(t)
	r := &fakeRunner{createCode: 1, onCreate: func(o options) { o.hooks.server("203.0.113.44") }}
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "gcp", "--location", "us-central1-a", "--size", "e2-standard-2", "--ssh-key", key, "--yes")
	if code != 1 || !strings.Contains(out, "keelbox up acme") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.GCP.Zone != "us-central1-a" || cfg.Target.GCP.MachineType != "e2-standard-2" || st.Stage != stageServer || st.Address != "203.0.113.44" || st.Bootstrap != "" {
		t.Fatalf("config %+v state %+v", cfg.Target.GCP, st)
	}
	r.calls, r.rootReachable = nil, true
	code, out = upWith(t, r, nil, "up", "acme")
	if code != exitNeedsYou || strings.Join(r.calls, "; ") != "apply acme as admin; signed-in?" {
		t.Errorf("resume: %d %v\n%s", code, r.calls, out)
	}
}

func gcpDestroy(t *testing.T, o options, input string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	logf := func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) }
	cfg := gcpCfg(t)
	cl, err := openCloud(context.Background(), cfg, o, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	return cloudDestroy(context.Background(), cl, cfg, o, strings.NewReader(input), logf, &out, &out), out.String()
}

// destroy goes through the same work check and confirmation as on the
// other clouds, then deletes the VM, its firewall rules and its address.
func TestGCPDestroy(t *testing.T) {
	isolate(t)
	cli := fakeGCP(t)
	in := gcpBox(cli)
	asked := stubProjects(t, cleanBox, nil, true)
	code, out := gcpDestroy(t, options{}, "acme\n")
	if code != 0 || len(cli.Instances)+len(cli.Rules)+len(cli.Addresses) != 0 {
		t.Fatalf("exit %d, left %v %v %v\n%s", code, cli.Instances, cli.Rules, cli.Addresses, out)
	}
	for _, want := range []string{
		fmt.Sprintf("Delete VM acme (e2-medium, europe-west3-a, %s) and everything on it? Type the box name to confirm: ", in.IP),
		"keelbox: deleted VM acme (europe-west3-a) and its disk\n", "keelbox: deleted firewall rule keelbox-acme-allow\n",
		"keelbox: deleted firewall rule keelbox-acme-deny\n", "keelbox: released address keelbox-acme (" + in.IP + ")\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if *asked != in.IP {
		t.Errorf("work check looked at %q", *asked)
	}
}

func TestGCPDestroyKeepsWorkAndOtherPeoplesVMs(t *testing.T) {
	isolate(t)
	cli := fakeGCP(t)
	in := gcpBox(cli)
	// Work only on the box, nobody to ask: refused, --yes or not.
	stubProjects(t, workOnBox, nil, false)
	code, out := gcpDestroy(t, options{yes: true}, "")
	if code != 1 || cli.Called("compute instances delete") || !strings.Contains(out, "keelbox destroy acme --yes --discard-work") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// The wrong name typed: nothing deleted.
	stubProjects(t, cleanBox, nil, true)
	if code, out := gcpDestroy(t, options{}, "acme2\n"); code != 1 || cli.Called("compute instances delete") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// A VM of the box's name without keelbox's labels is not touched, and
	// neither are the rules and the address while it carries the box's tag.
	in.Labels = map[string]string{"owner": "sam"}
	code, out = gcpDestroy(t, options{yes: true, discardWork: true}, "")
	if code != 1 || !strings.Contains(out, "not created by keelbox") || cli.Called("compute instances delete") || cli.Called("compute firewall-rules delete") || cli.Called("compute addresses delete") {
		t.Errorf("exit %d %v\n%s", code, cli.Calls(), out)
	}
}

// With no VM, destroy still removes the firewall rules and the address an
// interrupted create left.
func TestGCPDestroySweepsLeftovers(t *testing.T) {
	isolate(t)
	cli := fakeGCP(t)
	gcpBox(cli)
	delete(cli.Instances, "europe-west3-a/acme")
	code, out := gcpDestroy(t, options{yes: true}, "")
	if code != 1 || len(cli.Rules)+len(cli.Addresses) != 0 || !strings.Contains(out, "deleted firewall rule keelbox-acme-deny") ||
		!strings.Contains(out, "released address keelbox-acme") || !strings.Contains(out, "no VM named acme in the Google Cloud project acme-prod") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestOpenGCPNamesTheFix(t *testing.T) {
	cli := fakeGCP(t)
	cli.SignedIn = false
	_, err := openCloud(context.Background(), gcpCfg(t), options{}, nil, nil)
	if !errors.Is(err, gcp.ErrNotSignedIn) || !strings.Contains(err.Error(), "gcloud auth login; then run the command again") {
		t.Errorf("%v", err)
	}
	cli.SignedIn, cli.APIDisabled = true, true
	if _, err := openCloud(context.Background(), gcpCfg(t), options{}, nil, nil); !errors.Is(err, gcp.ErrAPIDisabled) || !strings.Contains(err.Error(), "--project acme-prod") {
		t.Errorf("%v", err)
	}
	gcpInstalled = func() bool { return false }
	if _, err := openCloud(context.Background(), gcpCfg(t), options{}, nil, nil); !errors.Is(err, gcp.ErrNoCLI) {
		t.Errorf("%v", err)
	}
}

func TestGCPDoctorAndFirewall(t *testing.T) {
	cli := fakeGCP(t)
	in := cli.AddInstance("acme", map[string]string{"keelbox": "true", "keelbox-box": "acme"}, "keelbox-box-acme")
	cfg := gcpCfg(t)
	cl, err := openCloud(context.Background(), cfg, options{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cl.title() != "gcp" || cl.bootstrapUser() != "" || bootstrapUser(viaGCP) != "" {
		t.Errorf("title %q, bootstrap %q", cl.title(), cl.bootstrapUser())
	}
	ctx := context.Background()
	srv, err := cl.find(ctx)
	if err != nil || srv.ip != in.IP || srv.ref != "VM acme" {
		t.Fatalf("%+v %v", srv, err)
	}
	doctor := func(open bool) (int, string) {
		srv, err := cl.find(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		code := cl.doctor(ctx, srv, func() (bool, error) { return open, nil }, func(l, c, d string) { fmt.Fprintf(&b, "%s %s %s\n", l, c, d) })
		return code, b.String()
	}
	// No rules yet: doctor fails until apply.
	if code, out := doctor(true); code != 1 || !strings.Contains(out, "FAIL cloud-firewall the firewall rules keelbox-acme-allow and keelbox-acme-deny are not both there; keelbox apply creates them") ||
		!strings.Contains(out, "ok cloud-login project SSH keys blocked, OS Login off") {
		t.Errorf("%d %s", code, out)
	}
	// apply first only makes sure they exist; new ones admit SSH.
	changes, summary, err := cl.firewall(ctx, srv, nil)
	if err != nil || len(changes) != 2 || summary != "keelbox-acme-allow: in tcp 22, in udp 41641" {
		t.Fatalf("%v %q %v", changes, summary, err)
	}
	if code, out := doctor(true); code != 0 || !strings.Contains(out, "ok cloud-firewall keelbox-acme-allow and keelbox-acme-deny apply to tag keelbox-box-acme: in tcp 22, in udp 41641, everything else refused; SSH admitted until the tailnet is proven") {
		t.Errorf("%d %s", code, out)
	}
	closed := false
	changes, summary, err = cl.firewall(ctx, srv, &closed)
	if err != nil || len(changes) != 1 || summary != "keelbox-acme-allow: in udp 41641" {
		t.Fatalf("%v %q %v", changes, summary, err)
	}
	if code, out := doctor(false); code != 0 || !strings.Contains(out, "in udp 41641, everything else refused; no public SSH") || strings.Contains(out, "cloud-firewall-others") {
		t.Errorf("%d %s", code, out)
	}
	// The box opened public SSH again (the tailnet proof lapsed): the
	// rules no longer match.
	if code, out := doctor(true); code != 1 || !strings.Contains(out, "differ from what the box needs (in tcp 22, in udp 41641, every other inbound packet refused)") {
		t.Errorf("%d %s", code, out)
	}
	// The network's own SSH rule sits behind keelbox's deny rule and is
	// not reported; one ahead of it is, as a warning.
	any := []string{"0.0.0.0/0"}
	cli.Rules["default-allow-ssh"] = &gcptest.Rule{Name: "default-allow-ssh", Network: "default", Action: "ALLOW", Specs: []string{"tcp:22"}, Priority: 65534, SourceRanges: any}
	if code, out := doctor(false); code != 0 || strings.Contains(out, "cloud-firewall-others") {
		t.Errorf("%d %s", code, out)
	}
	cli.Rules["team-ssh"] = &gcptest.Rule{Name: "team-ssh", Network: "default", Action: "ALLOW", Specs: []string{"tcp:22"}, Priority: 500, SourceRanges: any}
	if code, out := doctor(false); code != 0 || !strings.Contains(out, "warn cloud-firewall-others other rules of the network default admit traffic to the box ahead of keelbox-acme-deny (priority below 901): team-ssh") {
		t.Errorf("%d %s", code, out)
	}
	// The deny rule disabled by hand: everything the network admits reaches the box.
	cli.Rules["keelbox-acme-deny"].Disabled = true
	if code, out := doctor(false); code != 1 || !strings.Contains(out, "keelbox apply sets them") {
		t.Errorf("%d %s", code, out)
	}
	cli.Rules["keelbox-acme-deny"].Disabled = false
	// Changes to the VM's record that let others in.
	in.Metadata["block-project-ssh-keys"] = "false"
	in.Metadata["ssh-keys"] = "eve:ssh-ed25519 AAAA eve"
	in.ServiceAccount = "1234567890-compute@developer.gserviceaccount.com"
	if code, out := doctor(false); code != 1 || !strings.Contains(out, "FAIL cloud-login block-project-ssh-keys is not true") ||
		!strings.Contains(out, "metadata holds SSH keys") || !strings.Contains(out, "a service account is attached") {
		t.Errorf("%d %s", code, out)
	}
}

func TestStatusAllPublicFindsAGCPBox(t *testing.T) {
	isolate(t)
	cli := fakeGCP(t)
	in := gcpBox(cli)
	a, err := statusAllPublic(context.Background(), gcpCfg(t))
	if err != nil || a.host != in.IP {
		t.Errorf("%+v %v", a, err)
	}
	delete(cli.Instances, "europe-west3-a/acme")
	if _, err := statusAllPublic(context.Background(), gcpCfg(t)); err == nil || !strings.Contains(err.Error(), "finding it in Google Cloud") {
		t.Errorf("%v", err)
	}
}
