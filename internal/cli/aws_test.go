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
	"github.com/keelbox/keelbox/internal/provider/aws"
	"github.com/keelbox/keelbox/internal/provider/aws/awstest"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// fakeAWS puts a fake AWS CLI, signed in, behind keelbox's AWS calls.
func fakeAWS(t *testing.T) *awstest.CLI {
	t.Helper()
	// The region in the developer's own environment stays out of the tests.
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	cli := awstest.New()
	oldRunner, oldInstalled := awsRunner, awsInstalled
	awsRunner, awsInstalled = cli, func() bool { return true }
	t.Cleanup(func() { awsRunner, awsInstalled = oldRunner, oldInstalled })
	return cli
}

func awsCfg(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: aws, aws: {region: eu-central-1, bundle: medium_3_0, profile: work}}\n"+
		"users: {admin: {ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey sam@laptop\"]}}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// awsBox is a keelbox-made instance acme with its static IP attached.
func awsBox(cli *awstest.CLI) {
	cli.Instances["acme"] = &awstest.Instance{Name: "acme", Zone: "eu-central-1a", IP: "203.0.113.7", Blueprint: "ubuntu_24_04", Bundle: "medium_3_0", DiskGB: 80,
		Tags: map[string]string{"keelbox": "true", "keelbox-box": "acme"}}
	cli.StaticIPs["keelbox-acme"], cli.StaticAddr["keelbox-acme"] = "acme", "203.0.113.7"
}

func TestUpAWSWithFlags(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	cli := fakeAWS(t)
	r := &fakeRunner{signed: true}
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "aws", "--location", "eu-central-1", "--profile", "work", "--ssh-key", key, "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	a := cfg.Target.AWS
	if a.Region != "eu-central-1" || a.Profile != "work" || a.Bundle != "medium_3_0" || a.Blueprint != "ubuntu_24_04" {
		t.Errorf("aws target %+v", a)
	}
	if st.Via != viaAWS || st.Stage != stageReady || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
		t.Errorf("state %+v calls %v", st, r.calls)
	}
	// Asking changed nothing in the account.
	for _, c := range cli.Calls() {
		f := strings.Fields(c)
		if !strings.HasPrefix(f[1], "get-") {
			t.Errorf("up's questions ran %q", c)
		}
	}
}

func TestUpAWSInteractive(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	cli := fakeAWS(t)
	cli.ConfigRegion = "eu-central-1"
	in := script(
		"3",    // where: a new cloud server
		"2",    // which cloud: AWS
		"acme", // name
		"",     // profile: the default
		"",     // region: the CLI's own
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
		"signed in to AWS account 123456789012 as arn:aws:iam::123456789012:user/sam",
		"medium_3_0", "USD 24.00/month", "a new AWS Lightsail instance, medium_3_0 in eu-central-1, ubuntu_24_04",
		"USD 24.00 a month", "only Lightsail resources tagged keelbox=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "medium_ipv6_3_0") || strings.Contains(out, "medium_win_3_0") {
		t.Errorf("offered a bundle a box cannot run on\n%s", out)
	}
	cfg, _ := loadBox(t, "acme")
	if a := cfg.Target.AWS; a.Region != "eu-central-1" || a.Profile != "" || a.Bundle != "medium_3_0" {
		t.Errorf("aws target %+v", a)
	}
}

// Without a terminal, up stops before any change: exit 3 at what only the
// user can do (install the AWS CLI, sign in), exit 2 at a missing answer.
func TestUpAWSStopsBeforeAnyChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(cli *awstest.CLI)
		args   []string
		code   int
		wants  []string
		noCall bool
	}{
		{"no AWS CLI", func(*awstest.CLI) { awsInstalled = func() bool { return false } }, nil, exitNeedsYou,
			[]string{"not installed or not on PATH", aws.InstallURL, "keelbox up acme"}, true},
		{"not signed in", func(c *awstest.CLI) { c.SignedIn = false }, []string{"--profile", "work"}, exitNeedsYou,
			[]string{"not signed in to AWS with profile work", "aws login --profile work", "aws sso login --profile work", "aws configure --profile work", "keelbox up acme"}, false},
		{"no region", func(*awstest.CLI) {}, nil, 2, []string{"the AWS region", "--location", "keelbox up acme --provider aws --location eu-central-1 --yes"}, false},
		{"region without Lightsail", func(*awstest.CLI) {}, []string{"--location", "us-west-1"}, 2, []string{"us-west-1", "eu-central-1, us-east-1"}, false},
		{"unknown bundle", func(*awstest.CLI) {}, []string{"--location", "eu-central-1", "--size", "medium_ipv6_3_0"}, 2, []string{"medium_ipv6_3_0", "nano_3_0, small_3_0, medium_3_0, large_3_0"}, false},
		{"name taken", func(c *awstest.CLI) { c.Instances["acme"] = &awstest.Instance{Name: "acme"} }, []string{"--location", "eu-central-1"}, 2, []string{"already exists in eu-central-1", "keelbox up acme-2"}, false},
		{"bad profile", func(*awstest.CLI) {}, []string{"--profile", "-x"}, 2, []string{"--profile", "profile name"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolate(t)
			key := laptopKey(t, home)
			cli := fakeAWS(t)
			tc.set(cli)
			r := &fakeRunner{}
			args := append([]string{"up", "acme", "--provider", "aws", "--ssh-key", key, "--yes"}, tc.args...)
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
				t.Errorf("ran aws: %v", cli.Calls())
			}
		})
	}
}

// A create that failed after the server existed is continued as ubuntu,
// the account Lightsail's image logs in as.
func TestUpAWSResumesAsTheImageAccount(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	fakeAWS(t)
	r := &fakeRunner{createCode: 1, onCreate: func(o options) { o.hooks.server("203.0.113.44") }}
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "aws", "--location", "us-east-1", "--size", "small_3_0", "--ssh-key", key, "--yes")
	if code != 1 || !strings.Contains(out, "keelbox up acme") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.AWS.Region != "us-east-1" || cfg.Target.AWS.Bundle != "small_3_0" || st.Stage != stageServer || st.Address != "203.0.113.44" || st.Bootstrap != "ubuntu" {
		t.Fatalf("config %+v state %+v", cfg.Target.AWS, st)
	}
	r.calls, r.rootReachable = nil, true
	code, out = upWith(t, r, nil, "up", "acme")
	if code != exitNeedsYou || strings.Join(r.calls, "; ") != "reachable 203.0.113.44 ubuntu; apply acme as ubuntu; signed-in?" {
		t.Errorf("resume: %d %v\n%s", code, r.calls, out)
	}
}

func awsDestroy(t *testing.T, o options, input string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	logf := func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) }
	cfg := awsCfg(t)
	cl, err := openCloud(context.Background(), cfg, o, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	return cloudDestroy(context.Background(), cl, cfg, o, strings.NewReader(input), logf, &out, &out), out.String()
}

func stubProjects(t *testing.T, p *snapshot.Projects, err error, terminal bool) *string {
	t.Helper()
	oldProbe, oldTerm := boxProjects, stdinIsTerminal
	t.Cleanup(func() { boxProjects, stdinIsTerminal = oldProbe, oldTerm })
	asked := ""
	boxProjects = func(_ context.Context, _ *config.Config, ip string, _ options) (*snapshot.Projects, error) {
		asked = ip
		return p, err
	}
	stdinIsTerminal = func() bool { return terminal }
	return &asked
}

// destroy goes through the same work check and confirmation as on
// Hetzner, then deletes the instance and releases its static IP.
func TestAWSDestroy(t *testing.T) {
	isolate(t)
	cli := fakeAWS(t)
	awsBox(cli)
	asked := stubProjects(t, cleanBox, nil, true)
	code, out := awsDestroy(t, options{}, "acme\n")
	if code != 0 || len(cli.Instances) != 0 || len(cli.StaticIPs) != 0 {
		t.Fatalf("exit %d, left %v %v\n%s", code, cli.Instances, cli.StaticIPs, out)
	}
	for _, want := range []string{
		"Delete Lightsail instance acme (eu-central-1, 203.0.113.7) and everything on it? Type the box name to confirm: ",
		"keelbox: deleted instance acme\n", "keelbox: released static IP keelbox-acme (203.0.113.7)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if *asked != "203.0.113.7" {
		t.Errorf("work check looked at %q", *asked)
	}
}

func TestAWSDestroyKeepsWorkAndOtherPeoplesInstances(t *testing.T) {
	isolate(t)
	cli := fakeAWS(t)
	awsBox(cli)
	// Work only on the box, nobody to ask: refused, --yes or not.
	stubProjects(t, workOnBox, nil, false)
	code, out := awsDestroy(t, options{yes: true}, "")
	if code != 1 || cli.Called("lightsail delete-instance") || !strings.Contains(out, "keelbox destroy acme --yes --discard-work") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// The wrong name typed: nothing deleted.
	stubProjects(t, cleanBox, nil, true)
	if code, out := awsDestroy(t, options{}, "acme2\n"); code != 1 || cli.Called("lightsail delete-instance") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// An instance of the box's name without keelbox's tags is not touched,
	// and neither is a static IP attached to it.
	cli.Instances["acme"].Tags = map[string]string{"owner": "sam"}
	code, out = awsDestroy(t, options{yes: true, discardWork: true}, "")
	if code != 1 || !strings.Contains(out, "not created by keelbox") || cli.Called("lightsail delete-instance") || cli.Called("lightsail release-static-ip") {
		t.Errorf("exit %d %v\n%s", code, cli.Calls(), out)
	}
}

// With no instance, destroy still releases a static IP an interrupted
// create left unattached: Lightsail bills it.
func TestAWSDestroySweepsALeftoverStaticIP(t *testing.T) {
	isolate(t)
	cli := fakeAWS(t)
	cli.StaticIPs["keelbox-acme"], cli.StaticAddr["keelbox-acme"] = "", "203.0.113.9"
	code, out := awsDestroy(t, options{yes: true}, "")
	if code != 1 || len(cli.StaticIPs) != 0 || !strings.Contains(out, "released static IP keelbox-acme (203.0.113.9)") || !strings.Contains(out, "no Lightsail instance named acme") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestOpenAWSNamesTheFix(t *testing.T) {
	cli := fakeAWS(t)
	cli.SignedIn = false
	_, err := openCloud(context.Background(), awsCfg(t), options{}, nil, nil)
	if !errors.Is(err, aws.ErrNotSignedIn) || !strings.Contains(err.Error(), "aws login --profile work") {
		t.Errorf("%v", err)
	}
	awsInstalled = func() bool { return false }
	if _, err := openCloud(context.Background(), awsCfg(t), options{}, nil, nil); !errors.Is(err, aws.ErrNoCLI) {
		t.Errorf("%v", err)
	}
}

func TestAWSDoctorAndFirewall(t *testing.T) {
	cli := fakeAWS(t)
	awsBox(cli)
	cfg := awsCfg(t)
	cl, err := openCloud(context.Background(), cfg, options{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	srv, err := cl.find(ctx)
	if err != nil || srv.ip != "203.0.113.7" || srv.ref != "instance acme" {
		t.Fatalf("%+v %v", srv, err)
	}
	doctor := func(open bool) (int, string) {
		var b strings.Builder
		code := cl.doctor(ctx, srv, func() (bool, error) { return open, nil }, func(l, c, d string) { fmt.Fprintf(&b, "%s %s %s\n", l, c, d) })
		return code, b.String()
	}
	// A hand-made instance has no rules yet: doctor fails until apply.
	if code, out := doctor(true); code != 1 || !strings.Contains(out, "FAIL cloud-firewall") || !strings.Contains(out, "keelbox apply sets it") {
		t.Errorf("%d %s", code, out)
	}
	closed := false
	changes, summary, err := cl.firewall(ctx, srv, &closed)
	if err != nil || len(changes) != 1 || summary != "of instance acme: in udp 41641" {
		t.Fatalf("%v %q %v", changes, summary, err)
	}
	if code, out := doctor(false); code != 0 || !strings.Contains(out, "ok cloud-firewall instance firewall: in udp 41641; no public SSH") ||
		!strings.Contains(out, "ok static-ip keelbox-acme (203.0.113.7) attached") {
		t.Errorf("%d %s", code, out)
	}
	// The box opened public SSH again (the tailnet proof lapsed): the
	// firewall no longer matches.
	if code, out := doctor(true); code != 1 || !strings.Contains(out, "differs from what the box needs (in tcp 22, in udp 41641)") {
		t.Errorf("%d %s", code, out)
	}
	delete(cli.StaticIPs, "keelbox-acme")
	if _, out := doctor(false); !strings.Contains(out, "warn static-ip") || !strings.Contains(out, "aws lightsail allocate-static-ip --static-ip-name keelbox-acme") {
		t.Errorf("%s", out)
	}
}

func TestStatusAllPublicFindsAnAWSBox(t *testing.T) {
	isolate(t)
	cli := fakeAWS(t)
	awsBox(cli)
	a, err := statusAllPublic(context.Background(), awsCfg(t))
	if err != nil || a.host != "203.0.113.7" {
		t.Errorf("%+v %v", a, err)
	}
	delete(cli.Instances, "acme")
	if _, err := statusAllPublic(context.Background(), awsCfg(t)); err == nil || !strings.Contains(err.Error(), "finding it in AWS") {
		t.Errorf("%v", err)
	}
}
