package aws

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/aws/awstest"
)

const privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA-DO-NOT-LEAK\n-----END OPENSSH PRIVATE KEY-----\n"

var testHostKey = provider.HostKeyPair{Private: privateKey, Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHost acme\n"}

const adminKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey sam@laptop"

func boxConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	if !strings.Contains(extra, "bundle:") {
		extra += ", bundle: medium_3_0"
	}
	c, err := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: aws, aws: {region: eu-central-1"+extra+"}}\n"+
		"users: {admin: {ssh_authorized_keys: [\""+adminKey+"\"]}}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fake(t *testing.T) (*awstest.CLI, *Client) {
	t.Helper()
	old := poll
	poll = time.Millisecond
	t.Cleanup(func() { poll = old })
	// The region in the developer's own environment stays out of the tests.
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	cli := awstest.New()
	cli.Secrets = []string{"DO-NOT-LEAK"}
	t.Cleanup(func() {
		if len(cli.Leaked) > 0 {
			t.Errorf("the host key reached a command line: %q", cli.Leaked)
		}
	})
	return cli, &Client{Region: "eu-central-1", Run: cli}
}

func TestCreateFindDestroy(t *testing.T) {
	cli, cl := fake(t)
	cli.PendingPolls = 2
	ctx := context.Background()
	// The user's tags go on too; keelbox's own cannot be overridden.
	c := boxConfig(t, ", tags: {team: web, keelbox: nope}")
	in, err := Create(ctx, cl, c, testHostKey, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	got := cli.Instances["acme"]
	if got == nil || got.Bundle != "medium_3_0" || got.Blueprint != "ubuntu_24_04" || got.Zone != "eu-central-1a" {
		t.Fatalf("instance %+v", got)
	}
	if got.Tags["keelbox"] != "true" || got.Tags["keelbox-box"] != "acme" || got.Tags["team"] != "web" || len(got.Tags) != 3 {
		t.Errorf("tags %v", got.Tags)
	}
	// The user data went by a mode 600 file, removed afterwards.
	if got.UserDataMode != 0o600 || !strings.Contains(got.UserData, privateKey) || !strings.Contains(got.UserData, adminKey) {
		t.Errorf("user data mode %o:\n%s", got.UserDataMode, got.UserData)
	}
	for _, call := range cli.Calls() {
		if i := strings.Index(call, "file://"); i >= 0 {
			if _, err := os.Stat(strings.Fields(call[i+len("file://"):])[0]); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the user data file was left behind: %v", err)
			}
		}
	}
	// The static IP is the address, and only SSH and Tailscale are open.
	if cli.StaticIPs["keelbox-acme"] != "acme" || in.PublicIPAddress != cli.StaticAddr["keelbox-acme"] || !in.IsStaticIP {
		t.Errorf("static IP %v, instance %+v", cli.StaticIPs, in)
	}
	ports, err := cl.Ports(ctx, "acme")
	if err != nil || !SamePorts(ports, PortRules(true)) {
		t.Errorf("ports %v %+v", err, ports)
	}

	// A second create fails and changes nothing.
	if _, err := Create(ctx, cl, c, testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "keelbox destroy acme") {
		t.Errorf("second create: %v", err)
	}
	found, err := Find(ctx, cl, "acme")
	if err != nil || found.Name != "acme" {
		t.Fatalf("find: %v %+v", err, found)
	}
	done, err := Destroy(ctx, cl, found, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(cli.Instances) != 0 || len(cli.StaticIPs) != 0 || len(done) != 2 {
		t.Errorf("left after destroy: %v %v (%v)", cli.Instances, cli.StaticIPs, done)
	}
	if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "keelbox up acme") {
		t.Errorf("find after destroy: %v", err)
	}
}

func TestUserDataIsAScriptThatInstallsTheHostKey(t *testing.T) {
	ud, err := UserData("acme", "acme", testHostKey, []string{adminKey})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#!/bin/sh\n",
		"rm -f /etc/ssh/ssh_host_*_key /etc/ssh/ssh_host_*_key.pub\n",
		"cat > /etc/ssh/ssh_host_ed25519_key <<'KEELBOX_USER_DATA_EOF'\n" + strings.TrimRight(privateKey, "\n") + "\nKEELBOX_USER_DATA_EOF\n",
		"cat > /etc/ssh/ssh_host_ed25519_key.pub <<'KEELBOX_USER_DATA_EOF'\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHost acme\nKEELBOX_USER_DATA_EOF\n",
		"cat >> /home/ubuntu/.ssh/authorized_keys <<'KEELBOX_USER_DATA_EOF'\n" + adminKey + "\nKEELBOX_USER_DATA_EOF\n",
		"systemctl restart ssh.service",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("user data lacks %q:\n%s", want, ud)
		}
	}
	if _, err := UserData("acme", "acme", testHostKey, nil); err == nil {
		t.Error("user data without an admin key")
	}
	if _, err := UserData("acme", "acme", testHostKey, []string{adminKey + " KEELBOX_USER_DATA_EOF"}); err == nil {
		t.Error("a key holding the here-document marker was accepted")
	}
}

func TestNeverTouchesUntaggedInstances(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	cli.Instances["acme"] = &awstest.Instance{Name: "acme", IP: "203.0.113.50", Tags: map[string]string{"owner": "sam"}}
	cli.Instances["other"] = &awstest.Instance{Name: "other", IP: "203.0.113.51", Tags: map[string]string{"keelbox": "true", "keelbox-box": "elsewhere"}}
	cli.StaticIPs["keelbox-acme"], cli.StaticAddr["keelbox-acme"] = "other", "203.0.113.60"
	if _, err := Find(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "not created by keelbox") {
		t.Errorf("find: %v", err)
	}
	if _, err := Find(ctx, cl, "other"); err == nil || !strings.Contains(err.Error(), "not created by keelbox") {
		t.Errorf("find another box's instance: %v", err)
	}
	in, _ := cl.Instance(ctx, "acme")
	if _, err := Destroy(ctx, cl, in, "acme"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("destroy: %v", err)
	}
	// A static IP of the box's name attached to something else stays.
	if _, err := ReleaseStaticIP(ctx, cl, "acme"); err == nil || !strings.Contains(err.Error(), "attached to other, which keelbox did not create") {
		t.Errorf("release: %v", err)
	}
	if _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("create over an untagged instance: %v", err)
	}
	if cli.Called("lightsail delete-instance") || cli.Called("lightsail release-static-ip") || cli.Called("lightsail put-instance-public-ports") || cli.Called("lightsail create-instances") {
		t.Errorf("changed something: %v", cli.Calls())
	}
}

func TestPreflightNamesTheFix(t *testing.T) {
	ctx := context.Background()
	t.Run("not signed in", func(t *testing.T) {
		cli, cl := fake(t)
		cli.SignedIn = false
		cl.Profile = "work"
		_, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf)
		if !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "aws login --profile work") ||
			!strings.Contains(err.Error(), "aws configure sso --profile work") || !strings.Contains(err.Error(), "aws configure --profile work") {
			t.Errorf("%v", err)
		}
	})
	t.Run("unknown profile", func(t *testing.T) {
		cli, cl := fake(t)
		cli.Profile, cl.Profile = "work", "wrok"
		if _, err := cl.Identity(ctx); !errors.Is(err, ErrNotSignedIn) || !strings.Contains(err.Error(), "no profile named wrok") {
			t.Errorf("%v", err)
		}
		// The CLI stops at the profile, with exit 255, whatever the command.
		var e *Error
		if _, err := cl.Instances(ctx); !errors.As(err, &e) || e.Exit != 255 || e.Message != "The config profile (wrok) could not be found" {
			t.Errorf("%v", err)
		}
		cli.ConfigRegion = "eu-west-1"
		if got := cl.ConfiguredRegion(ctx); got != "" {
			t.Errorf("configured region of an unknown profile: %q", got)
		}
	})
	t.Run("region without Lightsail", func(t *testing.T) {
		_, cl := fake(t)
		cl.Region = "us-west-1"
		_, err := cl.Preflight(ctx, "medium_3_0", "ubuntu_24_04")
		if err == nil || !strings.Contains(err.Error(), "does not offer Lightsail") || !strings.Contains(err.Error(), "eu-central-1, us-east-1") {
			t.Errorf("%v", err)
		}
	})
	t.Run("bundle", func(t *testing.T) {
		cli, cl := fake(t)
		for _, b := range []string{"mediun_3_0", "medium_ipv6_3_0", "medium_win_3_0", "medium_2_0"} {
			_, err := Create(ctx, cl, boxConfig(t, ", bundle: "+b), testHostKey, t.Logf)
			if err == nil || !strings.Contains(err.Error(), "target.aws.bundle") || !strings.Contains(err.Error(), "nano_3_0, small_3_0, medium_3_0") {
				t.Errorf("%s: %v", b, err)
			}
		}
		if cli.Called("lightsail create-instances") {
			t.Error("created with a bad bundle")
		}
	})
	t.Run("blueprint", func(t *testing.T) {
		cli, cl := fake(t)
		for _, b := range []string{"ubuntu_30_04", "ubuntu_20_04", "wordpress"} {
			_, err := Create(ctx, cl, boxConfig(t, ", blueprint: "+b), testHostKey, t.Logf)
			if err == nil || !strings.Contains(err.Error(), "target.aws.blueprint") || !strings.Contains(err.Error(), "ubuntu_24_04, ubuntu_22_04") {
				t.Errorf("%s: %v", b, err)
			}
		}
		if cli.Called("lightsail create-instances") {
			t.Error("created with a bad blueprint")
		}
	})
}

// A failure after the instance exists returns it, so the caller can say
// that it may exist.
func TestCreateReturnsTheInstanceWhenALaterStepFails(t *testing.T) {
	cli, cl := fake(t)
	cli.Fail["lightsail attach-static-ip"] = "try again later"
	in, err := Create(context.Background(), cl, boxConfig(t, ""), testHostKey, t.Logf)
	if err == nil || in == nil || in.Name != "acme" || !strings.Contains(err.Error(), "static IP") || !strings.Contains(err.Error(), "try again later") {
		t.Fatalf("%+v %v", in, err)
	}
	// What it left is released without an instance to hang it on.
	delete(cli.Fail, "lightsail attach-static-ip")
	delete(cli.Instances, "acme")
	ip, err := ReleaseStaticIP(context.Background(), cl, "acme")
	if err != nil || ip == nil || len(cli.StaticIPs) != 0 {
		t.Errorf("release: %+v %v", ip, err)
	}
}

func TestEnsurePortsFollowsPublicSSH(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	if _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); err != nil {
		t.Fatal(err)
	}
	// Nothing to change: no call that writes.
	before := len(cli.Calls())
	changes, _, err := EnsurePorts(ctx, cl, "acme", nil)
	if err != nil || len(changes) != 0 {
		t.Fatalf("%v %v", changes, err)
	}
	for _, c := range cli.Calls()[before:] {
		if strings.Contains(c, "put-instance-public-ports") {
			t.Error("rewrote rules that were right")
		}
	}
	closed, open := false, true
	changes, ports, err := EnsurePorts(ctx, cl, "acme", &closed)
	if err != nil || len(changes) != 1 || HasPublicSSH(ports) || DescribePorts(ports) != "in udp 41641" {
		t.Fatalf("close: %v %v %v", changes, ports, err)
	}
	// Closed stays closed when the box's state is not known.
	if changes, ports, err = EnsurePorts(ctx, cl, "acme", nil); err != nil || len(changes) != 0 || HasPublicSSH(ports) {
		t.Errorf("keep: %v %v %v", changes, ports, err)
	}
	if _, ports, err = EnsurePorts(ctx, cl, "acme", &open); err != nil || !SamePorts(ports, PortRules(true)) {
		t.Errorf("open: %v %v", ports, err)
	}
	// A port opened by hand is closed again.
	cli.Instances["acme"].Ports = append(cli.Instances["acme"].Ports, map[string]any{"fromPort": 8080, "toPort": 8080, "protocol": "tcp", "cidrs": []string{"0.0.0.0/0"}})
	if changes, _, err = EnsurePorts(ctx, cl, "acme", nil); err != nil || len(changes) != 1 || !strings.Contains(changes[0], "in tcp 8080") {
		t.Errorf("drift: %v %v", changes, err)
	}
	got, _ := cl.Ports(ctx, "acme")
	if !SamePorts(got, PortRules(true)) {
		t.Errorf("after drift: %+v", got)
	}
}

// A rule for one address family names only that family: an empty
// "ipv6Cidrs=" would reach Lightsail as a list holding an empty string.
func TestPutPortsLeavesOutEmptyRanges(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	if _, err := Create(ctx, cl, boxConfig(t, ""), testHostKey, t.Logf); err != nil {
		t.Fatal(err)
	}
	rules := []Port{
		{FromPort: 8000, ToPort: 8100, Protocol: "tcp", Cidrs: []string{"192.0.2.0/24", "198.51.100.7/32"}},
		{FromPort: 53, ToPort: 53, Protocol: "udp", Ipv6Cidrs: []string{"2001:db8::/32"}},
		{FromPort: 443, ToPort: 443, Protocol: "tcp", Cidrs: anyIPv4, Ipv6Cidrs: anyIPv6},
	}
	if err := cl.PutPorts(ctx, "acme", rules); err != nil {
		t.Fatal(err)
	}
	calls := cli.Calls()
	want := "lightsail put-instance-public-ports --instance-name acme --port-infos " +
		"fromPort=8000,toPort=8100,protocol=tcp,cidrs=192.0.2.0/24,198.51.100.7/32 " +
		"fromPort=53,toPort=53,protocol=udp,ipv6Cidrs=2001:db8::/32 " +
		"fromPort=443,toPort=443,protocol=tcp,cidrs=0.0.0.0/0,ipv6Cidrs=::/0"
	if got := calls[len(calls)-1]; got != want {
		t.Errorf("ran:\n%s\nwant:\n%s", got, want)
	}
	if got, err := cl.Ports(ctx, "acme"); err != nil || !SamePorts(got, rules) {
		t.Errorf("ports read back: %+v, %v", got, err)
	}
}

// The region the AWS CLI would use: the environment before the profile.
func TestConfiguredRegion(t *testing.T) {
	cli, cl := fake(t)
	ctx := context.Background()
	if got := cl.ConfiguredRegion(ctx); got != "" {
		t.Errorf("nothing configured: %q", got)
	}
	cli.ConfigRegion = "eu-west-1"
	if got := cl.ConfiguredRegion(ctx); got != "eu-west-1" {
		t.Errorf("the profile's region: %q", got)
	}
	t.Setenv("AWS_DEFAULT_REGION", "ap-south-1")
	if got := cl.ConfiguredRegion(ctx); got != "ap-south-1" {
		t.Errorf("AWS_DEFAULT_REGION: %q", got)
	}
	t.Setenv("AWS_REGION", "sa-east-1")
	if got := cl.ConfiguredRegion(ctx); got != "sa-east-1" {
		t.Errorf("AWS_REGION: %q", got)
	}
}

func TestOffers(t *testing.T) {
	_, cl := fake(t)
	bundles, err := cl.Bundles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	offers := Offers(bundles)
	var ids []string
	for _, o := range offers {
		ids = append(ids, o.Bundle.BundleID)
	}
	// Cheapest first; no IPv6-only, Windows or inactive bundles.
	if strings.Join(ids, " ") != "nano_3_0 small_3_0 medium_3_0 large_3_0" {
		t.Errorf("offers %v", ids)
	}
	if d := offers[DefaultOffer(offers)]; d.Bundle.BundleID != "medium_3_0" || d.Describe() != "2 vCPU, 4 GB, 80 GB disk, 4096 GB transfer, USD 24.00/month" {
		t.Errorf("default %q %q", d.Bundle.BundleID, d.Describe())
	}
	if !strings.HasSuffix(offers[0].Describe(), "; small for agents") {
		t.Errorf("%q", offers[0].Describe())
	}
}

func TestParseError(t *testing.T) {
	for _, tc := range []struct{ stderr, code, msg string }{
		{"\nAn error occurred (NotFoundException) when calling the GetInstance operation: The Instance does not exist: acme\n", "NotFoundException", "The Instance does not exist: acme"},
		{"aws: [ERROR]: An error occurred (AccessDeniedException) when calling the CreateInstances operation: User is not authorized\n", "AccessDeniedException", "User is not authorized"},
		{"An error occurred (Throttling) when calling the GetBundles operation (reached max retries: 2): Rate exceeded", "Throttling", "Rate exceeded"},
		{"\nUnable to locate credentials. You can configure credentials by running \"aws login\".\n", "", "Unable to locate credentials. You can configure credentials by running \"aws login\"."},
		{"", "", "exit status 255"},
		// The CLI's own errors, as version 2.40 prints them.
		{"\naws: [ERROR]: Unable to locate credentials. You can configure credentials by running \"aws login\".\n", "", "Unable to locate credentials. You can configure credentials by running \"aws login\"."},
		{"\naws: [ERROR]: The config profile (wrok) could not be found\n", "", "The config profile (wrok) could not be found"},
		// Usage text follows the error or comes first; older releases
		// mark the line "aws: error:".
		{"\nusage: aws [options] <command> <subcommand> [<subcommand> ...] [parameters]\nTo see help text, you can run:\n\n  aws help\n\naws: [ERROR]: Unknown options: --bogus\n\n",
			"", "Unknown options: --bogus"},
		{"\naws: [ERROR]: the following arguments are required: --instance-name\n\nusage: aws [options] <command> <subcommand> [<subcommand> ...] [parameters]\nTo see help text, you can run:\n\n  aws help\n  aws <command> help\n  aws <command> <subcommand> help\n",
			"", "the following arguments are required: --instance-name"},
		{"\nusage: aws [options] <command> <subcommand> [<subcommand> ...] [parameters]\nTo see help text, you can run:\n\n  aws help\n\naws: error: the following arguments are required: --instance-name\n",
			"", "the following arguments are required: --instance-name"},
		// The default ("enhanced") format, should the legacy one not be in force.
		{"\naws: [ERROR]: An error occurred (AccessDeniedException) when calling the GetInstance operation: User is not authorized\n\nAdditional error details:\ndocs: https://docs.aws.amazon.com/lightsail/\ntip: Check the policy.\n",
			"AccessDeniedException", "User is not authorized"},
	} {
		e := parseError("lightsail x", []byte(tc.stderr), 255)
		if e.Code != tc.code || e.Message != tc.msg {
			t.Errorf("%q: %+v", tc.stderr, e)
		}
	}
}

// The real runner against a fake aws on PATH: arguments, the environment
// it sets, exit codes and output.
func TestExecRunsTheCLIOnPath(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"$AWS_PAGER|$AWS_CLI_ERROR_FORMAT|$*\" >> \"" + dir + "/log\"\n" +
		"case \"$1 $2\" in\n" +
		"  'sts get-caller-identity') echo '{\"UserId\":\"AIDAEXAMPLE\",\"Account\":\"123456789012\",\"Arn\":\"arn:aws:iam::123456789012:user/sam\"}';;\n" +
		"  *) echo 'An error occurred (NotFoundException) when calling the GetInstance operation: The Instance does not exist: acme' >&2; exit 254;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cl := New("work", "eu-central-1")
	id, err := cl.Identity(context.Background())
	if err != nil || id.Account != "123456789012" {
		t.Fatalf("%+v %v", id, err)
	}
	in, err := cl.Instance(context.Background(), "acme")
	if in != nil || err != nil {
		t.Errorf("a missing instance: %+v %v", in, err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	want := "|legacy|sts get-caller-identity --output json --no-cli-pager --cli-connect-timeout 20 --cli-read-timeout 60 --region eu-central-1 --profile work\n" +
		"|legacy|lightsail get-instance --instance-name acme --output json --no-cli-pager --cli-connect-timeout 20 --cli-read-timeout 60 --region eu-central-1 --profile work\n"
	if string(log) != want {
		t.Errorf("ran:\n%s\nwant:\n%s", log, want)
	}
}

func TestExecWithoutTheCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if Installed() {
		t.Fatal("aws found on an empty PATH")
	}
	_, err := New("", "eu-central-1").Identity(context.Background())
	if !errors.Is(err, ErrNoCLI) || !strings.Contains(err.Error(), InstallURL) {
		t.Errorf("%v", err)
	}
}
