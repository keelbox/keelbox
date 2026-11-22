package gcp

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
	"github.com/keelbox/keelbox/internal/provider/gcp/gcptest"
)

const privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA-DO-NOT-LEAK\n-----END OPENSSH PRIVATE KEY-----\n"

var testHostKey = provider.HostKeyPair{Private: privateKey, Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHost acme\n"}

const adminKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey sam@laptop"

func boxConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	if !strings.Contains(extra, "machine_type:") {
		extra += ", machine_type: e2-medium"
	}
	c, err := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: gcp, gcp: {zone: europe-west3-a"+extra+"}}\n"+
		"users: {admin: {ssh_authorized_keys: [\""+adminKey+"\"]}}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fake(t *testing.T) (*gcptest.CLI, *Client) {
	t.Helper()
	old := poll
	poll = time.Millisecond
	t.Cleanup(func() { poll = old })
	cli := gcptest.New()
	cli.Secrets = []string{"DO-NOT-LEAK"}
	t.Cleanup(func() {
		if len(cli.Leaked) > 0 {
			t.Errorf("the host key reached a command line: %q", cli.Leaked)
		}
	})
	return cli, &Client{Run: cli}
}

var ownLabels = map[string]string{"keelbox": "true", "keelbox-box": "acme"}

func TestCreateFindDestroy(t *testing.T) {
	cli, cl := fake(t)
	cli.StagingPolls = 2
	ctx := context.Background()
	c := boxConfig(t, ", labels: {team: web}")
	in, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	made := cli.Instances["europe-west3-a/acme"]
	if made == nil || in.Name != "acme" || in.Status != "RUNNING" || in.PublicIPv4() != made.IP || !in.Managed("acme") || in.ZoneName() != "europe-west3-a" || in.TypeName() != "e2-medium" {
		t.Fatalf("instance %+v", in)
	}
	if made.Labels["keelbox"] != "true" || made.Labels["keelbox-box"] != "acme" || made.Labels["team"] != "web" || strings.Join(made.Tags, ",") != "keelbox-box-acme" ||
		made.Image != "ubuntu-os-cloud/ubuntu-2404-lts-amd64" || made.Disk != "50GB" || made.DiskType != "pd-balanced" || made.Net != "default" {
		t.Errorf("created %+v", made)
	}
	// It runs in the project gcloud is configured for, on the address
	// keelbox reserved for it.
	addr := cli.Addresses["europe-west3/keelbox-acme"]
	if cl.Project != "acme-dev" || addr == nil || addr.IP != made.IP || !strings.HasPrefix(addr.Description, "Managed by keelbox") {
		t.Errorf("project %q, address %+v", cl.Project, addr)
	}
	// No service account, no project keys, no OS Login.
	if made.ServiceAccount != "" || made.Metadata["block-project-ssh-keys"] != "true" || made.Metadata["enable-oslogin"] != "false" || len(in.LoginProblems()) != 0 {
		t.Errorf("service account %q, metadata %v, problems %v", made.ServiceAccount, made.Metadata, in.LoginProblems())
	}
	// The user data came from a mode 600 file, removed afterwards.
	if made.UserDataMode != 0o600 || !strings.Contains(made.UserData, "DO-NOT-LEAK") {
		t.Errorf("user data mode %o", made.UserDataMode)
	}
	for _, call := range cli.Calls() {
		if i := strings.Index(call, "user-data="); i >= 0 {
			if _, err := os.Stat(strings.Fields(call[i+len("user-data="):])[0]); err == nil {
				t.Error("the user data file was left behind")
			}
		}
	}
	// Both firewall rules existed, applied to the box's tag, before the VM.
	calls := strings.Join(cli.Calls(), "\n")
	deny, allow, create := strings.Index(calls, "compute firewall-rules create keelbox-acme-deny"), strings.Index(calls, "compute firewall-rules create keelbox-acme-allow"), strings.Index(calls, "compute instances create")
	if deny < 0 || allow < deny || create < allow {
		t.Errorf("order of creation:\n%s", calls)
	}
	f, err := FindFirewall(ctx, cl, "acme")
	if err != nil || !f.InStep(true) || !f.HasPublicSSH() || f.Describe() != "in tcp 22, in udp 41641" {
		t.Fatalf("firewall %+v %v", f, err)
	}
	if _, err := Create(ctx, cl, c, testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "keelbox destroy acme") {
		t.Errorf("second create: %v", err)
	}
	got, err := Find(ctx, cl, "acme")
	if err != nil || got.ID != in.ID {
		t.Fatalf("find %+v %v", got, err)
	}
	done, err := Destroy(ctx, cl, got, "acme")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"deleted VM acme (europe-west3-a) and its disk", "deleted firewall rule keelbox-acme-allow", "deleted firewall rule keelbox-acme-deny", "released address keelbox-acme (" + made.IP + ")"}
	if strings.Join(done, "|") != strings.Join(want, "|") {
		t.Errorf("destroy said %q", done)
	}
	if len(cli.Instances)+len(cli.Rules)+len(cli.Addresses) != 0 {
		t.Errorf("left: %v %v %v", cli.Instances, cli.Rules, cli.Addresses)
	}
	if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "keelbox up acme") || !strings.Contains(err.Error(), "acme-dev") {
		t.Errorf("find after destroy: %v", err)
	}
}

// target.gcp.project wins over the project gcloud is configured for.
func TestProjectFromTheConfig(t *testing.T) {
	cli, cl := fake(t)
	if _, err := Create(context.Background(), cl, boxConfig(t, ", project: acme-prod"), testHostKey, t.Logf); err != nil {
		t.Fatal(err)
	}
	if cl.Project != "acme-prod" {
		t.Errorf("project %q", cl.Project)
	}
	for _, c := range cli.Calls() {
		if strings.Contains(c, "--project") {
			t.Fatalf("the fake shows --project among a command's own flags: %s", c)
		}
	}
}

func TestUserDataCreatesTheAdminAndInstallsTheHostKey(t *testing.T) {
	cli, cl := fake(t)
	if _, err := Create(context.Background(), cl, boxConfig(t, ""), testHostKey, t.Logf); err != nil {
		t.Fatal(err)
	}
	ud := cli.Instances["europe-west3-a/acme"].UserData
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
		WriteFiles    []struct {
			Path    string `yaml:"path"`
			Append  bool   `yaml:"append"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
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
	// Google's guest agent is told not to replace that host key; what the
	// image already set in its file stays.
	if len(doc.WriteFiles) != 1 || doc.WriteFiles[0].Path != "/etc/default/instance_configs.cfg" || !doc.WriteFiles[0].Append ||
		!strings.Contains(doc.WriteFiles[0].Content, "[InstanceSetup]\nset_host_keys = false\n") {
		t.Errorf("write_files %+v", doc.WriteFiles)
	}
}

func TestNeverTouchesWhatItDidNotMake(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	for _, labels := range []map[string]string{nil, {"keelbox": "true"}, {"keelbox-box": "acme"}, {"keelbox": "true", "keelbox-box": "other"}, {"keelbox": "yes", "keelbox-box": "acme"}} {
		// A network tag is not a label: carrying the box's tag proves nothing.
		in := cli.AddInstance("acme", labels, "keelbox-box-acme")
		if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "not created by keelbox") {
			t.Errorf("labels %v: find: %v", labels, err)
		}
		if _, err := Destroy(ctx, cl, &Instance{ID: in.ID, Name: "acme", Zone: in.Zone, Labels: labels}, "acme"); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("labels %v: destroy: %v", labels, err)
		}
		if _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("labels %v: create: %v", labels, err)
		}
		delete(cli.Instances, "europe-west3-a/acme")
	}
	// Both labels on a VM of another name: not the box's either, and while
	// it carries the box's label the rules and the address stay.
	cli.AddInstance("web", ownLabels)
	if _, err := Find(ctx, cl, "acme"); err == nil {
		t.Error("found a VM of another name")
	}
	if _, err := Sweep(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "still carries") {
		t.Errorf("sweep: %v", err)
	}
	delete(cli.Instances, "europe-west3-a/web")
	if cli.Called("compute instances delete") || cli.Called("compute instances create") || cli.Called("compute firewall-rules create") || cli.Called("compute addresses create") {
		t.Errorf("changed the project: %q", cli.Calls())
	}

	// Firewall rules and an address of the box's names that keelbox did
	// not make: by description, and for a rule by its target.
	ours := managedDescription("the allow rule", "acme")
	for name, r := range map[string]*gcptest.Rule{
		"another description": {Name: "keelbox-acme-allow", Description: "ssh for the team", Network: "default", Action: "ALLOW", Specs: []string{"tcp:22"}, Priority: 1000, SourceRanges: []string{"0.0.0.0/0"}, TargetTags: []string{"keelbox-box-acme"}},
		"another target":      {Name: "keelbox-acme-allow", Description: ours, Network: "default", Action: "ALLOW", Specs: []string{"tcp:22"}, Priority: 1000, SourceRanges: []string{"0.0.0.0/0"}, TargetTags: []string{"web"}},
		"every VM":            {Name: "keelbox-acme-allow", Description: ours, Network: "default", Action: "ALLOW", Specs: []string{"tcp:22"}, Priority: 1000, SourceRanges: []string{"0.0.0.0/0"}},
		"the deny rule":       {Name: "keelbox-acme-deny", Description: "", Network: "default", Action: "DENY", Specs: []string{"all"}, Priority: 901, SourceRanges: []string{"0.0.0.0/0"}, TargetTags: []string{"keelbox-box-acme"}},
	} {
		cli.Rules = map[string]*gcptest.Rule{r.Name: r}
		for what, call := range map[string]func() error{
			"ensure":  func() error { _, _, err := EnsureFirewall(ctx, cl, "acme", nil); return err },
			"destroy": func() error { _, err := DestroyFirewall(ctx, cl, "acme"); return err },
			"sweep":   func() error { _, err := Sweep(ctx, cl, "acme"); return err },
			"create":  func() error { _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); return err },
		} {
			if err := call(); err == nil || !strings.Contains(err.Error(), "does not touch it") {
				t.Errorf("%s, %s: %v", name, what, err)
			}
		}
		if len(cli.Rules) != 1 || cli.Called("compute firewall-rules update") || cli.Called("compute firewall-rules delete") || cli.Called("compute firewall-rules create") {
			t.Errorf("%s: the rules changed: %q", name, cli.Calls())
		}
	}
	cli.Rules = map[string]*gcptest.Rule{}
	cli.Addresses["europe-west3/keelbox-acme"] = &gcptest.Address{Name: "keelbox-acme", Region: "europe-west3", IP: "198.51.100.9", Description: "the website"}
	if _, err := ReleaseAddress(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "does not touch it") {
		t.Errorf("release: %v", err)
	}
	if _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "does not touch it") || len(cli.Instances) != 0 {
		t.Errorf("create: %v", err)
	}
	if cli.Called("compute addresses delete") || len(cli.Addresses) != 1 {
		t.Errorf("the address changed: %q", cli.Calls())
	}

	// keelbox's own address that something else uses now stays.
	cli2, cl2 := fake(t)
	cli2.Addresses["europe-west3/keelbox-acme"] = &gcptest.Address{Name: "keelbox-acme", Region: "europe-west3", IP: "198.51.100.9", Description: managedDescription("the address", "acme")}
	other := cli2.AddInstance("web", nil)
	other.IP = "198.51.100.9"
	if _, err := Sweep(ctx, cl2, "acme"); err == nil || !strings.Contains(err.Error(), "still in use by web") || len(cli2.Addresses) != 1 {
		t.Errorf("sweep: %v", err)
	}
}

func TestPreflightNamesTheFix(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		set         func(*gcptest.CLI, *Client)
		zone, mtype string
		wants       []string
		is          error
	}{
		{"not signed in", func(c *gcptest.CLI, _ *Client) { c.SignedIn = false }, "europe-west3-a", "e2-medium",
			[]string{"not signed in to Google Cloud", "do not currently have an active account selected. Sign in: gcloud auth login"}, ErrNotSignedIn},
		{"sign-in expired", func(c *gcptest.CLI, _ *Client) { c.Expired = true }, "europe-west3-a", "",
			[]string{"problem refreshing your current auth tokens", "Sign in: gcloud auth login"}, ErrNotSignedIn},
		{"no project", func(c *gcptest.CLI, _ *Client) { c.Project = "" }, "europe-west3-a", "",
			[]string{"target.gcp.project is not set", "gcloud projects list", "gcloud config set project PROJECT-ID"}, ErrNoProject},
		{"unknown project", func(_ *gcptest.CLI, cl *Client) { cl.Project = "acme-typo" }, "europe-west3-a", "",
			[]string{"cannot use Compute Engine in project acme-typo", "sam@example.com", "gcloud projects list", "roles/compute.admin"}, nil},
		{"API off", func(c *gcptest.CLI, _ *Client) { c.APIDisabled = true }, "europe-west3-a", "",
			[]string{"Compute Engine API is not enabled in project acme-dev", "gcloud services enable compute.googleapis.com --project acme-dev"}, ErrAPIDisabled},
		{"zone", func(*gcptest.CLI, *Client) {}, "europe-west3-z", "e2-medium", []string{`target.gcp.zone "europe-west3-z"`, "europe-west3-a, europe-west3-b"}, nil},
		{"zone down", func(*gcptest.CLI, *Client) {}, "europe-west3-c", "", []string{"not a zone that is up"}, nil},
		{"zone elsewhere", func(*gcptest.CLI, *Client) {}, "mars-north1-a", "", []string{"gcloud compute zones list --project acme-dev"}, nil},
		{"machine type", func(*gcptest.CLI, *Client) {}, "europe-west3-a", "e9-huge", []string{`target.gcp.machine_type "e9-huge" is not a machine type in europe-west3-a`, "e2-micro, e2-small, e2-medium, e2-standard-2, e2-standard-4", "gcloud compute machine-types list --zones europe-west3-a"}, nil},
		{"arm", func(*gcptest.CLI, *Client) {}, "europe-west3-a", "t2a-standard-2", []string{"an Arm type", "ubuntu-2404-lts-amd64"}, nil},
		{"retired", func(*gcptest.CLI, *Client) {}, "europe-west3-a", "n1-retired-2", []string{"it is retired"}, nil},
		{"no network", func(c *gcptest.CLI, _ *Client) { c.Networks = []string{"prod"} }, "europe-west3-a", "e2-medium",
			[]string{"no VPC network named default", "gcloud compute networks create default --subnet-mode=auto --project acme-dev"}, nil},
		// A list one request failed for is an error, never an empty list.
		{"a refused list", func(c *gcptest.CLI, _ *Client) {
			c.Fail["compute machine-types list"] = "Required 'compute.machineTypes.list' permission for 'projects/acme-dev'"
		}, "europe-west3-a", "e2-medium", []string{"gcloud compute machine-types list: Required 'compute.machineTypes.list' permission"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli, cl := fake(t)
			tc.set(cli, cl)
			_, err := cl.Preflight(ctx, tc.zone, tc.mtype)
			if err == nil {
				t.Fatal("no error")
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("not %v: %v", tc.is, err)
			}
			for _, w := range tc.wants {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("lacks %q: %v", w, err)
				}
			}
			for _, c := range cli.Calls() {
				if f := strings.Fields(c); f[len(f)-1] != "list" && f[1] != "project-info" && !strings.Contains(c, " list --") {
					t.Errorf("preflight ran %q", c)
				}
			}
		})
	}
	_, cl := fake(t)
	if a, err := cl.Preflight(ctx, "europe-west3-a", "c3-standard-4"); err != nil || a.Describe() != "project acme-dev as sam@example.com" {
		t.Errorf("%+v %v", a, err)
	}
}

// A refused list of VMs is an error for Find and Create: gcloud prints it
// as a warning with an empty list and exits 0.
func TestARefusedListIsNotAnEmptyOne(t *testing.T) {
	cli, cl := fake(t)
	cli.AddInstance("acme", ownLabels, "keelbox-box-acme")
	cli.Fail["compute instances list"] = "Required 'compute.instances.list' permission for 'projects/acme-dev'"
	ctx := context.Background()
	if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "compute.instances.list") {
		t.Errorf("find: %v", err)
	}
	if _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "compute.instances.list") {
		t.Errorf("create: %v", err)
	}
	if _, err := Sweep(ctx, cl, "acme"); err == nil || cli.Called("compute firewall-rules delete") {
		t.Errorf("sweep: %v", err)
	}
	cli.Fail = map[string]string{"compute addresses list": "Required 'compute.addresses.list' permission for 'projects/acme-dev'"}
	if a, err := FindAddress(ctx, cl, "acme"); err == nil || a != nil {
		t.Errorf("address: %+v %v", a, err)
	}
}

func TestCreateReturnsTheVMWhenALaterStepFails(t *testing.T) {
	cli, cl := fake(t)
	cli.StagingPolls = 1000
	old := poll
	poll = 0
	defer func() { poll = old }()
	in, err := Create(context.Background(), cl, boxConfig(t, ""), testHostKey, t.Logf)
	if err == nil || in == nil || in.Name != "acme" || len(cli.Instances) != 1 || !strings.Contains(err.Error(), "did not start running") {
		t.Fatalf("%+v %v", in, err)
	}
	// Nothing was created when a firewall rule could not be, and Sweep
	// removes the rule that was.
	cli2, cl2 := fake(t)
	cli2.Fail["compute firewall-rules create"] = "Quota 'FIREWALLS' exceeded"
	in, err = Create(context.Background(), cl2, boxConfig(t, ""), testHostKey, t.Logf)
	if err == nil || in != nil || len(cli2.Instances)+len(cli2.Addresses) != 0 || !strings.Contains(err.Error(), "cloud firewall") {
		t.Fatalf("%+v %v", in, err)
	}
	// The address could not be reserved: the rules exist, no VM does.
	cli3, cl3 := fake(t)
	cli3.Fail["compute addresses create"] = "Quota 'STATIC_ADDRESSES' exceeded. Limit: 8.0 in region europe-west3."
	in, err = Create(context.Background(), cl3, boxConfig(t, ""), testHostKey, t.Logf)
	if err == nil || in != nil || len(cli3.Instances) != 0 || len(cli3.Rules) != 2 || !strings.Contains(err.Error(), "reserve the address") || !strings.Contains(err.Error(), "STATIC_ADDRESSES") {
		t.Fatalf("%+v %v", in, err)
	}
	cli3.Fail = map[string]string{}
	if done, err := Sweep(context.Background(), cl3, "acme"); err != nil || len(done) != 2 || len(cli3.Rules) != 0 {
		t.Errorf("sweep: %q %v", done, err)
	}
	// The VM could not be created: the next create uses the address the
	// first one reserved.
	cli4, cl4 := fake(t)
	cli4.Fail["compute instances create"] = "Quota 'CPUS' exceeded. Limit: 0.0 in region europe-west3."
	if in, err = Create(context.Background(), cl4, boxConfig(t, ""), testHostKey, t.Logf); err == nil || in != nil || len(cli4.Addresses) != 1 {
		t.Fatalf("%+v %v", in, err)
	}
	cli4.Fail = map[string]string{}
	in, err = Create(context.Background(), cl4, boxConfig(t, ""), testHostKey, t.Logf)
	if err != nil || len(cli4.Addresses) != 1 || in.PublicIPv4() != cli4.Addresses["europe-west3/keelbox-acme"].IP {
		t.Fatalf("%+v %v", in, err)
	}
}

func TestEnsureFirewallFollowsPublicSSH(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	yes, no := true, false
	f, changes, err := EnsureFirewall(ctx, cl, "acme", nil)
	if err != nil || len(changes) != 2 || !f.HasPublicSSH() || f.Describe() != "in tcp 22, in udp 41641" {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	allow, deny := cli.Rules["keelbox-acme-allow"], cli.Rules["keelbox-acme-deny"]
	if allow.Priority != 900 || deny.Priority != 901 || allow.Action != "ALLOW" || deny.Action != "DENY" || strings.Join(deny.Specs, ",") != "all" ||
		strings.Join(allow.TargetTags, ",") != "keelbox-box-acme" || strings.Join(deny.TargetTags, ",") != "keelbox-box-acme" ||
		strings.Join(allow.SourceRanges, ",") != "0.0.0.0/0" || allow.Network != "default" || !strings.HasPrefix(deny.Description, "Managed by keelbox: the deny rule of box acme") {
		t.Errorf("rules %+v %+v", allow, deny)
	}
	// In step already: nothing changes, whatever is asked.
	for _, want := range []*bool{nil, &yes} {
		if _, changes, err := EnsureFirewall(ctx, cl, "acme", want); err != nil || len(changes) != 0 {
			t.Errorf("%q %v", changes, err)
		}
	}
	f, changes, err = EnsureFirewall(ctx, cl, "acme", &no)
	if err != nil || len(changes) != 1 || f.HasPublicSSH() || !strings.Contains(changes[0], "keelbox-acme-allow: in udp 41641") || strings.Join(allow.Specs, ",") != "udp:41641" {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	// nil keeps SSH closed.
	if f, changes, err = EnsureFirewall(ctx, cl, "acme", nil); err != nil || len(changes) != 0 || f.HasPublicSSH() {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	// A port added by hand is removed, a rule disabled or moved by hand is
	// set back, and a rule deleted by hand is made again.
	allow.Specs = append(allow.Specs, "tcp:8080")
	deny.Disabled, deny.Priority = true, 2000
	f, changes, err = EnsureFirewall(ctx, cl, "acme", nil)
	if err != nil || len(changes) != 2 || !f.InStep(false) || deny.Disabled || deny.Priority != 901 || strings.Join(allow.Specs, ",") != "udp:41641" {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
	delete(cli.Rules, "keelbox-acme-deny")
	f, changes, err = EnsureFirewall(ctx, cl, "acme", nil)
	if err != nil || len(changes) != 1 || !f.InStep(false) || !strings.Contains(changes[0], "created firewall rule keelbox-acme-deny") {
		t.Fatalf("%+v %q %v", f, changes, err)
	}
}

func TestRules(t *testing.T) {
	for ports, want := range map[string]bool{"22": true, "": true, "20-25": true, "2222": false, "23-80": false} {
		spec := PortSpec{IPProtocol: "tcp"}
		if ports != "" {
			spec.Ports = []string{ports}
		}
		if got := (&Firewall{Allow: &Rule{Allowed: []PortSpec{spec}}}).HasPublicSSH(); got != want {
			t.Errorf("ports %q: %v", ports, got)
		}
	}
	if !(&Firewall{Allow: &Rule{Allowed: []PortSpec{{IPProtocol: "all"}}}}).HasPublicSSH() || (&Firewall{}).HasPublicSSH() {
		t.Error("every protocol admits SSH; no rule admits nothing")
	}
	// What else in the network reaches the box ahead of keelbox's deny rule.
	net := "https://www.googleapis.com/compute/v1/projects/p/global/networks/default"
	allow := []PortSpec{{IPProtocol: "tcp", Ports: []string{"22"}}}
	rules := []Rule{
		{Name: "default-allow-ssh", Network: net, Direction: "INGRESS", Priority: 65534, Allowed: allow},
		{Name: "team-ssh", Network: net, Direction: "INGRESS", Priority: 500, Allowed: allow},
		{Name: "box-web", Network: net, Direction: "INGRESS", Priority: 100, Allowed: allow, TargetTags: []string{"x", "keelbox-box-acme"}},
		{Name: "other-tag", Network: net, Direction: "INGRESS", Priority: 100, Allowed: allow, TargetTags: []string{"web"}},
		{Name: "off", Network: net, Direction: "INGRESS", Priority: 100, Allowed: allow, Disabled: true},
		{Name: "out", Network: net, Direction: "EGRESS", Priority: 100, Allowed: allow},
		{Name: "a-deny", Network: net, Direction: "INGRESS", Priority: 100, Denied: allow},
		{Name: "elsewhere", Network: strings.Replace(net, "default", "prod", 1), Direction: "INGRESS", Priority: 100, Allowed: allow},
		{Name: "keelbox-acme-allow", Network: net, Direction: "INGRESS", Priority: 900, Allowed: allow, TargetTags: []string{"keelbox-box-acme"}},
	}
	if got := strings.Join(Bypasses(rules, "acme"), ","); got != "box-web,team-ssh" {
		t.Errorf("bypasses: %s", got)
	}
	if RegionOf("europe-west3-a") != "europe-west3" || RegionOf("us-central1-f") != "us-central1" {
		t.Error("RegionOf")
	}
}

func TestOffers(t *testing.T) {
	_, cl := fake(t)
	if _, err := cl.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	types, err := cl.MachineTypes(context.Background(), "europe-west3-a")
	if err != nil {
		t.Fatal(err)
	}
	offers := Offers(types)
	var names []string
	for _, o := range offers {
		names = append(names, o.Type.Name)
	}
	// Smallest first; the E2 family up to 8 vCPUs, no Arm or retired type.
	if strings.Join(names, ",") != "e2-micro,e2-small,e2-medium,e2-standard-2,e2-standard-4" {
		t.Errorf("offers %v", names)
	}
	def := offers[DefaultOffer(offers)]
	if def.Type.Name != "e2-medium" || def.Describe() != "2 vCPU (shared), 4 GB" {
		t.Errorf("default %s: %s", def.Type.Name, def.Describe())
	}
	if offers[0].Describe() != "2 vCPU (shared), 1 GB; small for agents" || offers[4].Describe() != "4 vCPU, 16 GB" {
		t.Errorf("describe: %s / %s", offers[0].Describe(), offers[4].Describe())
	}
}

func TestParseError(t *testing.T) {
	for _, tc := range []struct {
		stderr, msg string
		notFound    bool
	}{
		{"ERROR: (gcloud.compute.instances.describe) Could not fetch resource:\n - The resource 'projects/p/zones/z/instances/x' was not found\n\n",
			"The resource 'projects/p/zones/z/instances/x' was not found", true},
		{"Creating firewall...\nfailed.\nERROR: (gcloud.compute.firewall-rules.create) Could not fetch resource:\n - Quota 'FIREWALLS' exceeded.\n   Limit: 100.0 globally.\n - Another\n\n",
			"Quota 'FIREWALLS' exceeded. Limit: 100.0 globally.; Another", false},
		{"WARNING: Some requests did not succeed.\n - Required 'compute.instances.list' permission for 'projects/p'\n\nWARNING: The following filter keys were not present in any resource : name\n",
			"Required 'compute.instances.list' permission for 'projects/p'", false},
		{"ERROR: (gcloud.compute.project-info.describe) You do not currently have an active account selected.\nPlease run:\n\n  $ gcloud auth login\n\nto obtain new credentials.\n",
			"You do not currently have an active account selected. Please run:", false},
		{"ERROR: (gcloud.compute.instances.create) unrecognized arguments: --nope\n\nTo search the help text of gcloud commands, run:\n  gcloud help -- SEARCH_TERMS\n",
			"unrecognized arguments: --nope", false},
		{"Traceback\n  something broke\n", "something broke", false},
		{"", "exit status 1", false},
	} {
		e := parseError("compute instances describe", []byte(tc.stderr), 1)
		if e.Message != tc.msg || IsNotFound(e) != tc.notFound {
			t.Errorf("%q: %+v", tc.stderr, e)
		}
	}
}

// The real runner against a fake gcloud on PATH: arguments, exit codes and
// output.
func TestExecRunsTheCLIOnPath(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + dir + "/log\"\n" +
		"case \"$1 $2\" in\n" +
		"  'config list') echo '{\"core\":{\"account\":\"sam@example.com\"}}';;\n" +
		"  'compute project-info') echo '{\"name\":\"acme-prod\"}';;\n" +
		"  *) echo '[]'; echo 'WARNING: Some requests did not succeed.' >&2; echo \" - Required 'compute.instances.list' permission for 'projects/acme-prod'\" >&2;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "gcloud"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cl := New("acme-prod")
	a, err := cl.Account(context.Background())
	if err != nil || a.Describe() != "project acme-prod as sam@example.com" {
		t.Fatalf("%+v %v", a, err)
	}
	if is, err := cl.InstancesNamed(context.Background(), "acme"); err == nil || is != nil || !strings.Contains(err.Error(), "compute.instances.list") {
		t.Errorf("a refused list: %+v %v", is, err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	want := "config list --format=json --quiet\n" +
		"compute project-info describe --format=json --quiet --project acme-prod\n" +
		"compute instances list --filter name=acme --format=json --quiet --project acme-prod\n"
	if string(log) != want {
		t.Errorf("ran:\n%s\nwant:\n%s", log, want)
	}
}

func TestExecWithoutTheCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if Installed() {
		t.Fatal("gcloud found on an empty PATH")
	}
	_, err := New("").Account(context.Background())
	if !errors.Is(err, ErrNoCLI) || !strings.Contains(err.Error(), InstallURL) {
		t.Errorf("%v", err)
	}
}
