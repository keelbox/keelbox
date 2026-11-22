package digitalocean

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/digitalocean/dotest"
)

const privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA-DO-NOT-LEAK\n-----END OPENSSH PRIVATE KEY-----\n"

var testHostKey = provider.HostKeyPair{Private: privateKey, Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHost acme\n"}

const adminKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey sam@laptop"

func boxConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	if !strings.Contains(extra, "size:") {
		extra += ", size: s-2vcpu-4gb"
	}
	c, err := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: digitalocean, digitalocean: {region: fra1"+extra+"}}\n"+
		"users: {admin: {ssh_authorized_keys: [\""+adminKey+"\"]}}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fake(t *testing.T) (*dotest.CLI, *Client) {
	t.Helper()
	old := poll
	poll = time.Millisecond
	t.Cleanup(func() { poll = old })
	cli := dotest.New()
	cli.Secrets = []string{"DO-NOT-LEAK"}
	t.Cleanup(func() {
		if len(cli.Leaked) > 0 {
			t.Errorf("the host key reached a command line: %q", cli.Leaked)
		}
	})
	return cli, &Client{Run: cli}
}

func TestCreateFindDestroy(t *testing.T) {
	cli, cl := fake(t)
	cli.NewPolls = 2
	ctx := context.Background()
	c := boxConfig(t, ", tags: [\"team:web\"]")
	d, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	made := cli.Droplets[d.ID]
	if d.Name != "acme" || d.Status != "active" || d.PublicIPv4() != made.IP || !d.Managed("acme") {
		t.Errorf("droplet %+v", d)
	}
	if strings.Join(made.Tags, ",") != "keelbox,keelbox-box-acme,team:web" || made.Region != "fra1" || made.Size != "s-2vcpu-4gb" || made.Image != "ubuntu-24-04-x64" {
		t.Errorf("created %+v", made)
	}
	// The user data came from a mode 600 file, removed afterwards, and no
	// account key was named: there is none.
	if made.UserDataMode != 0o600 || !strings.Contains(made.UserData, "DO-NOT-LEAK") || len(made.SSHKeys) != 0 {
		t.Errorf("user data mode %o, keys %v", made.UserDataMode, made.SSHKeys)
	}
	for _, call := range cli.Calls() {
		if i := strings.Index(call, "--user-data-file "); i >= 0 {
			if _, err := os.Stat(strings.Fields(call[i:])[1]); err == nil {
				t.Error("the user data file was left behind")
			}
		}
	}
	// The firewall existed, applied to the box's tag, before the Droplet.
	calls := strings.Join(cli.Calls(), "\n")
	fw, create := strings.Index(calls, "compute firewall create"), strings.Index(calls, "compute droplet create")
	if fw < 0 || create < fw {
		t.Errorf("firewall after the Droplet:\n%s", calls)
	}
	f, err := FindFirewall(ctx, cl, "acme")
	if err != nil || f == nil || !f.Targets("keelbox-box-acme") || !HasPublicSSH(f) || !SameRules(f.Inbound, InboundRules(true)) || !SameRules(f.Outbound, OutboundRules()) {
		t.Fatalf("firewall %+v %v", f, err)
	}
	if _, err := Create(ctx, cl, c, testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "keelbox destroy acme") {
		t.Errorf("second create: %v", err)
	}
	got, err := Find(ctx, cl, "acme")
	if err != nil || got.ID != d.ID {
		t.Fatalf("find %+v %v", got, err)
	}
	done, err := Destroy(ctx, cl, got, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 || !strings.Contains(done[0], "deleted Droplet acme") || !strings.Contains(done[1], "deleted firewall keelbox-acme") {
		t.Errorf("destroy said %q", done)
	}
	// Nothing is left: no Droplet, no firewall, and neither of keelbox's
	// tags. The user's own tag is theirs.
	if len(cli.Droplets) != 0 || len(cli.Firewalls) != 0 || cli.Tags["keelbox"] || cli.Tags["keelbox-box-acme"] || !cli.Tags["team:web"] {
		t.Errorf("left: %d droplets, %d firewalls, tags %v", len(cli.Droplets), len(cli.Firewalls), cli.Tags)
	}
	if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "keelbox up acme") {
		t.Errorf("find after destroy: %v", err)
	}
}

func TestUserDataCreatesTheAdminAndInstallsTheHostKey(t *testing.T) {
	cli, cl := fake(t)
	d, err := Create(context.Background(), cl, boxConfig(t, ""), testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ud := cli.Droplets[d.ID].UserData
	if !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Fatalf("not a cloud-config:\n%s", ud)
	}
	var doc struct {
		Hostname string `yaml:"hostname"`
		Users    []struct {
			Name string   `yaml:"name"`
			Sudo string   `yaml:"sudo"`
			Keys []string `yaml:"ssh_authorized_keys"`
		} `yaml:"users"`
		SSHPwauth     *bool             `yaml:"ssh_pwauth"`
		DisableRoot   *bool             `yaml:"disable_root"`
		SSHDeleteKeys bool              `yaml:"ssh_deletekeys"`
		SSHKeys       map[string]string `yaml:"ssh_keys"`
	}
	if err := yaml.Unmarshal([]byte(ud), &doc); err != nil {
		t.Fatal(err)
	}
	// The admin logs in with the user's key; root and passwords do not.
	if len(doc.Users) != 1 || doc.Users[0].Name != "admin" || doc.Users[0].Keys[0] != adminKey || doc.Users[0].Sudo == "" {
		t.Errorf("users %+v", doc.Users)
	}
	if doc.SSHPwauth == nil || *doc.SSHPwauth || doc.DisableRoot == nil || !*doc.DisableRoot {
		t.Errorf("ssh_pwauth %v disable_root %v", doc.SSHPwauth, doc.DisableRoot)
	}
	if doc.Hostname != "acme" || !doc.SSHDeleteKeys || doc.SSHKeys["ed25519_private"] != privateKey || doc.SSHKeys["ed25519_public"] != strings.TrimSpace(testHostKey.Public) {
		t.Errorf("host key %+v", doc.SSHKeys)
	}
}

// An account key that is the admin's own is named, so DigitalOcean sets
// no root password; keelbox never adds one.
func TestCreateNamesAnAccountKeyThatIsAlreadyThere(t *testing.T) {
	cli, cl := fake(t)
	cli.Keys[7] = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOther someone@else"
	cli.Keys[9] = strings.Fields(adminKey)[0] + " " + strings.Fields(adminKey)[1] + " another-comment"
	d, err := Create(context.Background(), cl, boxConfig(t, ""), testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if got := cli.Droplets[d.ID].SSHKeys; len(got) != 1 || got[0] != "9" {
		t.Errorf("ssh keys %v", got)
	}
	for _, c := range cli.Calls() {
		if strings.HasPrefix(c, "compute ssh-key") && c != "compute ssh-key list" {
			t.Errorf("changed the account's keys: %s", c)
		}
	}
	// A token that may not list keys is no obstacle.
	cli2, cl2 := fake(t)
	cli2.Forbidden["compute ssh-key list"] = true
	if _, err := Create(context.Background(), cl2, boxConfig(t, ""), testHostKey, t.Logf); err != nil {
		t.Errorf("without ssh_key:read: %v", err)
	}
}

func TestNeverTouchesDropletsOrFirewallsItDidNotMake(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	for _, tags := range [][]string{nil, {"keelbox"}, {"keelbox-box-acme"}, {"keelbox", "keelbox-box-other"}} {
		d := cli.AddDroplet("acme", tags...)
		if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "not created by keelbox") {
			t.Errorf("tags %v: find: %v", tags, err)
		}
		if _, err := Destroy(ctx, cl, &Droplet{ID: d.ID, Name: "acme", Tags: tags}, "acme"); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("tags %v: destroy: %v", tags, err)
		}
		if _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("tags %v: create: %v", tags, err)
		}
		delete(cli.Droplets, d.ID)
	}
	// Both tags on a Droplet of another name: not the box's either.
	other := cli.AddDroplet("web", "keelbox", "keelbox-box-acme")
	if _, err := Find(ctx, cl, "acme"); err == nil {
		t.Error("found a Droplet of another name")
	}
	// It carries the box's tag, so the firewall and the tag stay.
	if _, err := Sweep(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "still carries the tag") {
		t.Errorf("sweep: %v", err)
	}
	delete(cli.Droplets, other.ID)
	if cli.Called("compute droplet delete") || cli.Called("compute droplet create") || cli.Called("compute tag delete") {
		t.Errorf("changed the account: %q", cli.Calls())
	}
	// A firewall of the box's name that does not apply to the box's tag
	// is someone else's.
	cli.Tags["web"] = true
	cli.Firewalls["fw-1"] = &dotest.Firewall{ID: "fw-1", Name: "keelbox-acme", Inbound: []string{"tcp:443:0.0.0.0/0"}, Tags: []string{"web"}}
	for name, call := range map[string]func() error{
		"ensure":  func() error { _, _, err := EnsureFirewall(ctx, cl, "acme", nil); return err },
		"destroy": func() error { _, err := DestroyFirewall(ctx, cl, "acme"); return err },
		"create":  func() error { _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "does not touch it") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f := cli.Firewalls["fw-1"]; f == nil || f.Inbound[0] != "tcp:443:0.0.0.0/0" || cli.Called("compute firewall update") || cli.Called("compute firewall delete") {
		t.Errorf("the firewall changed: %+v", f)
	}
	// A tag other Droplets carry is not deleted with the box.
	cli2, cl2 := fake(t)
	d, err := Create(ctx, cl2, boxConfig(t, ""), testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	cli2.AddDroplet("other", "keelbox", "keelbox-box-other")
	if _, err := Destroy(ctx, cl2, d, "acme"); err != nil {
		t.Fatal(err)
	}
	if !cli2.Tags["keelbox"] || cli2.Tags["keelbox-box-acme"] || len(cli2.Droplets) != 1 {
		t.Errorf("tags %v, droplets %d", cli2.Tags, len(cli2.Droplets))
	}
}

func TestPreflightNamesTheFix(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name                string
		set                 func(*dotest.CLI, *Client)
		region, size, image string
		wants               []string
		notSignedIn         bool
	}{
		{"no token", func(c *dotest.CLI, _ *Client) { c.SignedIn = false }, "fra1", "s-2vcpu-4gb", "ubuntu-24-04-x64",
			[]string{"not signed in to DigitalOcean", "doctl's current context", "doctl auth list", "doctl auth init"}, true},
		{"no such context", func(c *dotest.CLI, cl *Client) { c.Context, cl.Context = "work", "wrok" }, "fra1", "", "",
			[]string{"no token for the doctl context wrok", "doctl auth init --context wrok"}, true},
		{"token refused", func(c *dotest.CLI, cl *Client) { c.Refused, cl.Context = true, "work" }, "fra1", "", "",
			[]string{"refused the token of the doctl context work", "Unable to authenticate you", "doctl auth init --context work"}, true},
		{"scoped token", func(c *dotest.CLI, _ *Client) { c.Forbidden["account get"] = true }, "fra1", "", "",
			[]string{"may not read the account", "account:read", "droplet:create", "firewall:update", "tag:delete", TokensURL, "doctl auth init"}, false},
		{"region", func(*dotest.CLI, *Client) {}, "nyc2", "s-2vcpu-4gb", "", []string{`target.digitalocean.region "nyc2"`, "fra1, nyc3"}, false},
		{"size", func(*dotest.CLI, *Client) {}, "fra1", "c-2", "", []string{`target.digitalocean.size "c-2" cannot be ordered in fra1`, "s-1vcpu-1gb, s-2vcpu-2gb, s-2vcpu-4gb, s-4vcpu-8gb"}, false},
		{"retired size", func(*dotest.CLI, *Client) {}, "fra1", "s-2vcpu-4gb-old", "", []string{"cannot be ordered in fra1"}, false},
		{"image", func(*dotest.CLI, *Client) {}, "fra1", "s-2vcpu-4gb", "ubuntu-30-04-x64", []string{`target.digitalocean.image "ubuntu-30-04-x64"`, "ubuntu-24-04-x64, ubuntu-26-04-x64"}, false},
		{"image elsewhere", func(*dotest.CLI, *Client) {}, "fra1", "s-2vcpu-4gb", "ubuntu-22-04-x64", []string{"not an image available in fra1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli, cl := fake(t)
			tc.set(cli, cl)
			_, err := cl.Preflight(ctx, tc.region, tc.size, tc.image)
			if err == nil {
				t.Fatal("no error")
			}
			if errors.Is(err, ErrNotSignedIn) != tc.notSignedIn {
				t.Errorf("ErrNotSignedIn: %v", err)
			}
			for _, w := range tc.wants {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("lacks %q: %v", w, err)
				}
			}
			for _, c := range cli.Calls() {
				if f := strings.Fields(c); len(f) > 2 && f[2] != "list" && f[2] != "get" && f[2] != "list-distribution" {
					t.Errorf("preflight ran %q", c)
				}
			}
		})
	}
	_, cl := fake(t)
	if a, err := cl.Preflight(ctx, "fra1", "s-2vcpu-4gb", "ubuntu-24-04-x64"); err != nil || a.Describe() != "team Sam's team as sam@example.com" {
		t.Errorf("%+v %v", a, err)
	}
}

func TestCreateReturnsTheDropletWhenALaterStepFails(t *testing.T) {
	cli, cl := fake(t)
	cli.Fail["compute droplet get"] = "Server Error"
	d, err := Create(context.Background(), cl, boxConfig(t, ""), testHostKey, t.Logf)
	if err == nil || d == nil || d.ID == 0 || len(cli.Droplets) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	// Nothing was created when the firewall could not be.
	cli2, cl2 := fake(t)
	cli2.Fail["compute firewall create"] = "Server Error"
	d, err = Create(context.Background(), cl2, boxConfig(t, ""), testHostKey, t.Logf)
	if err == nil || d != nil || len(cli2.Droplets) != 0 || !strings.Contains(err.Error(), "cloud firewall") {
		t.Fatalf("%+v %v", d, err)
	}
	// Sweep removes what that left: the tag made for the firewall.
	cli2.Fail = map[string]string{}
	if _, err := Sweep(context.Background(), cl2, "acme"); err != nil || cli2.Tags["keelbox-box-acme"] {
		t.Errorf("sweep: %v, tags %v", err, cli2.Tags)
	}
}

func TestEnsureFirewallFollowsPublicSSH(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	yes, no := true, false
	f, changes, err := EnsureFirewall(ctx, cl, "acme", nil)
	if err != nil || len(changes) != 1 || !HasPublicSSH(f) || !cli.Tags["keelbox-box-acme"] {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	if DescribeRules(f.Inbound) != "in tcp 22, in udp 41641" {
		t.Errorf("describe: %s", DescribeRules(f.Inbound))
	}
	// In step already: nothing changes, whatever is asked.
	for _, want := range []*bool{nil, &yes} {
		if _, changes, err := EnsureFirewall(ctx, cl, "acme", want); err != nil || len(changes) != 0 {
			t.Errorf("%q %v", changes, err)
		}
	}
	f, changes, err = EnsureFirewall(ctx, cl, "acme", &no)
	if err != nil || len(changes) != 1 || HasPublicSSH(f) || !strings.Contains(changes[0], "in udp 41641") {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	// nil keeps SSH closed; an update keeps the target tag.
	f, changes, err = EnsureFirewall(ctx, cl, "acme", nil)
	if err != nil || len(changes) != 0 || HasPublicSSH(f) || !f.Targets("keelbox-box-acme") {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	// A rule added by hand is removed, and Droplets added by hand stay.
	stored := cli.Firewalls[f.ID]
	stored.Inbound = append(stored.Inbound, "tcp:8080:0.0.0.0/0")
	stored.DropletIDs = []int64{77}
	f, changes, err = EnsureFirewall(ctx, cl, "acme", nil)
	if err != nil || len(changes) != 1 || !SameRules(f.Inbound, InboundRules(false)) {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	if s := cli.Firewalls[f.ID]; len(s.Inbound) != 1 || len(s.DropletIDs) != 1 || s.DropletIDs[0] != 77 || len(s.Outbound) != 3 {
		t.Errorf("stored %+v", s)
	}
}

func TestRules(t *testing.T) {
	any := &Peers{Addresses: []string{"::/0", "0.0.0.0/0"}}
	// Every spelling of "all ports" the API uses is the same rule.
	for _, p := range []string{"0", "all", "1-65535", ""} {
		got := []Rule{{Protocol: "udp", Ports: p, Destinations: any}, {Protocol: "tcp", Ports: p, Destinations: any}, {Protocol: "icmp", Ports: "0", Destinations: any}}
		if !SameRules(got, OutboundRules()) {
			t.Errorf("ports %q differ", p)
		}
	}
	if SameRules([]Rule{{Protocol: "udp", Ports: TailscalePort, Sources: &Peers{Addresses: anywhere, Tags: []string{"web"}}}}, InboundRules(false)) {
		t.Error("a rule that also admits a tag is the same")
	}
	for ports, want := range map[string]bool{"22": true, "0": true, "20-25": true, "2222": false, "23-80": false} {
		if got := HasPublicSSH(&Firewall{Inbound: []Rule{{Protocol: "tcp", Ports: ports, Sources: any}}}); got != want {
			t.Errorf("ports %s: %v", ports, got)
		}
	}
	if got := ruleArg(append(InboundRules(true), OutboundRules()[0])); got != "protocol:udp,ports:41641,address:0.0.0.0/0,address:::/0 protocol:tcp,ports:22,address:0.0.0.0/0,address:::/0 protocol:icmp,address:0.0.0.0/0,address:::/0" {
		t.Errorf("rule argument: %s", got)
	}
}

func TestOffers(t *testing.T) {
	cli, cl := fake(t)
	sizes, err := cl.Sizes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	offers := Offers(sizes, "fra1")
	var slugs []string
	for _, o := range offers {
		slugs = append(slugs, o.Size.Slug)
	}
	// Cheapest first; not the retired size, nor one sold elsewhere.
	if strings.Join(slugs, ",") != "s-1vcpu-1gb,s-2vcpu-2gb,s-2vcpu-4gb,s-4vcpu-8gb" {
		t.Errorf("offers %v", slugs)
	}
	def := offers[DefaultOffer(offers)]
	if def.Size.Slug != "s-2vcpu-4gb" || def.Describe() != "2 vCPU, 4 GB, 80 GB disk, 4 TB transfer, USD 24.00/month" {
		t.Errorf("default %s: %s", def.Size.Slug, def.Describe())
	}
	if !strings.HasSuffix(offers[0].Describe(), "; small for agents") {
		t.Errorf("small: %s", offers[0].Describe())
	}
	_ = cli
}

func TestParseError(t *testing.T) {
	for _, tc := range []struct {
		stdout, stderr string
		status         int
		msg            string
	}{
		{`{"errors":[{"detail":"GET https://api.digitalocean.com/v2/droplets/9: 404 The resource you were accessing could not be found.","title":"Not Found","reason":"The resource you were accessing could not be found.","status":404}]}`, "",
			404, "The resource you were accessing could not be found."},
		{`{"errors":[{"detail":"Unable to initialize DigitalOcean API client: access token is required. (hint: run 'doctl auth init')"}]}`, "",
			0, "Unable to initialize DigitalOcean API client: access token is required. (hint: run 'doctl auth init')"},
		{"", "Error: unknown flag: --nope\n\nUsage:\n  doctl compute droplet list [flags]\n", 0, "unknown flag: --nope"},
		{"", "", 0, "exit status 1"},
	} {
		e := parseError("compute droplet get", []byte(tc.stdout), []byte(tc.stderr), 1)
		if e.Status != tc.status || e.Message != tc.msg {
			t.Errorf("%q %q: %+v", tc.stdout, tc.stderr, e)
		}
	}
	if !IsStatus(parseError("x", []byte(`{"errors":[{"status":404}]}`), nil, 1), 404) {
		t.Error("IsStatus")
	}
}

// The real runner against a fake doctl on PATH: arguments, exit codes and
// output.
func TestExecRunsTheCLIOnPath(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + dir + "/log\"\n" +
		"case \"$1 $2\" in\n" +
		"  'account get') echo '{\"email\":\"sam@example.com\",\"uuid\":\"a1b2c3\",\"status\":\"active\"}';;\n" +
		"  *) echo '{\"errors\":[{\"detail\":\"GET /v2/droplets/9: 404 not found\",\"title\":\"Not Found\",\"reason\":\"not found\",\"status\":404}]}'; exit 1;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "doctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cl := New("work")
	a, err := cl.Account(context.Background())
	if err != nil || a.Email != "sam@example.com" {
		t.Fatalf("%+v %v", a, err)
	}
	d, err := cl.Droplet(context.Background(), 9)
	if d != nil || err != nil {
		t.Errorf("a missing Droplet: %+v %v", d, err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	want := "account get --output json --interactive=false --context work\n" +
		"compute droplet get 9 --output json --interactive=false --context work\n"
	if string(log) != want {
		t.Errorf("ran:\n%s\nwant:\n%s", log, want)
	}
}

func TestExecWithoutTheCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if Installed() {
		t.Fatal("doctl found on an empty PATH")
	}
	_, err := New("").Account(context.Background())
	if !errors.Is(err, ErrNoCLI) || !strings.Contains(err.Error(), InstallURL) {
		t.Errorf("%v", err)
	}
}
