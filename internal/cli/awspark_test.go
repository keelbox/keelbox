package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/aws/awstest"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// awsWorld is the box acme in the registry and in a fake Lightsail account
// (instance acme on small_3_0 at 203.0.113.7, its static IP), its host key
// pinned, and a record of what the commands did past the AWS CLI.
type awsWorld struct {
	cli *awstest.CLI
	cfg *config.Config
	cl  cloud
	// waited and applied are the addresses boxComesBack and applyExisting
	// were called with; applyCode is what the apply returns.
	waited, applied []string
	applyCode       int
	// hasFile and fileErr are what the box answers when asked for the
	// cloud-init file; asked counts the questions.
	hasFile bool
	fileErr error
	asked   []string
}

func newAWSWorld(t *testing.T) *awsWorld {
	t.Helper()
	isolate(t)
	cli := fakeAWS(t)
	cli.Instances["acme"] = &awstest.Instance{Name: "acme", Zone: "eu-central-1a", IP: "203.0.113.7", Blueprint: "ubuntu_24_04", Bundle: "small_3_0", DiskGB: 60,
		Tags: map[string]string{"keelbox": "true", "keelbox-box": "acme"}}
	cli.Instances["acme"].Ports = []map[string]any{
		{"fromPort": 41641, "toPort": 41641, "protocol": "udp", "state": "open", "cidrs": []string{"0.0.0.0/0"}, "ipv6Cidrs": []string{"::/0"}},
		{"fromPort": 22, "toPort": 22, "protocol": "tcp", "state": "open", "cidrs": []string{"0.0.0.0/0"}, "ipv6Cidrs": []string{"::/0"}}}
	cli.StaticIPs["keelbox-acme"], cli.StaticAddr["keelbox-acme"] = "acme", "203.0.113.7"
	s := config.StarterFor("acme", "aws")
	s.Bundle = "small_3_0"
	s.Keys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}
	cfg, err := config.Load(register(t, s))
	if err != nil {
		t.Fatal(err)
	}
	w := &awsWorld{cli: cli, cfg: cfg, hasFile: true}
	if w.cl, err = openCloud(context.Background(), cfg, options{}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := sshTarget(cfg, addr{"203.0.113.7", 22}, options{}, io.Discard, io.Discard).Pin(provider.ParseHostKeys(boxHostKey)); err != nil {
		t.Fatal(err)
	}
	oldWait, oldApply, oldTerm, oldProbe, oldClosed, oldFile := boxComesBack, applyExisting, stdinIsTerminal, boxProjects, publicSSHClosed, boxHasFile
	t.Cleanup(func() {
		boxComesBack, applyExisting, stdinIsTerminal, boxProjects, publicSSHClosed, boxHasFile = oldWait, oldApply, oldTerm, oldProbe, oldClosed, oldFile
	})
	publicSSHClosed = func(context.Context, *config.Config, *cloudServer) bool { return false }
	boxHasFile = func(_ context.Context, _ *config.Config, a addr, path string) (bool, error) {
		w.asked = append(w.asked, a.host+" "+path)
		return w.hasFile, w.fileErr
	}
	boxComesBack = func(_ context.Context, _ *config.Config, public addr, _, _ io.Writer) error {
		w.waited = append(w.waited, public.host)
		return nil
	}
	applyExisting = func(_ context.Context, _ cloud, _ *config.Config, srv *cloudServer, public addr, o options, _ func(string, ...any), _, _ io.Writer) int {
		w.applied = append(w.applied, fmt.Sprintf("%s as %q, snapshots %d", public.host, o.sshUser, len(cli.Snapshots)))
		return w.applyCode
	}
	stdinIsTerminal = func() bool { return false }
	boxProjects = func(context.Context, *config.Config, string, options) (*snapshot.Projects, error) {
		return cleanBox, nil
	}
	return w
}

// do runs a command as Main would, with input as the terminal's answers
// (nil: no terminal).
func (w *awsWorld) do(t *testing.T, cmd string, o options, input *string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	p := prompt.New(strings.NewReader(""), &out, false)
	if input != nil {
		p = prompt.New(strings.NewReader(*input), &out, true)
	}
	o.configFile = boxConfigPath("acme")
	logf := func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) }
	ctx := context.Background()
	var code int
	switch cmd {
	case "park":
		code = cloudPark(ctx, w.cl, w.cfg, o, p, logf, &out, &out)
	case "resume":
		code = cloudResume(ctx, w.cl, w.cfg, o, logf, &out, &out)
	case "resize":
		code = cloudResize(ctx, w.cl, w.cfg, o, p, logf, &out, &out)
	case "destroy":
		in := ""
		if input != nil {
			in = *input
		}
		code = cloudDestroy(ctx, w.cl, w.cfg, o, strings.NewReader(in), logf, &out, &out)
	default:
		t.Fatalf("no command %s", cmd)
	}
	return code, out.String()
}

// writes are the calls that changed the account, in order, without the
// reads between them: the operation and what it was on.
func (w *awsWorld) writes(from int) []string {
	var out []string
	for _, c := range w.cli.Calls()[from:] {
		f := strings.Fields(c)
		if f[0] != "lightsail" || strings.HasPrefix(f[1], "get-") {
			continue
		}
		out = append(out, f[1])
	}
	return out
}

func (w *awsWorld) snapshot(t *testing.T) *awstest.Snapshot {
	t.Helper()
	if len(w.cli.Snapshots) != 1 {
		t.Fatalf("%d snapshots", len(w.cli.Snapshots))
	}
	for _, s := range w.cli.Snapshots {
		return s
	}
	return nil
}

// park stops the instance, takes the snapshot, waits for it, and only then
// deletes the instance and releases the static IP; resume creates the
// instance from the snapshot with a new static IP, moves the pinned host
// keys, applies as the admin and deletes the snapshot after that apply.
func TestAWSParkThenResume(t *testing.T) {
	w := newAWSWorld(t)
	blob := pinned(t, "203.0.113.7")
	code, out := w.do(t, "park", options{}, str("y\n"))
	if code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	if got := strings.Join(w.writes(0), " "); got != "stop-instance create-instance-snapshot delete-instance release-static-ip" {
		t.Errorf("park's calls: %s", got)
	}
	// The snapshot was read as available before the delete.
	calls := strings.Join(w.cli.Calls(), "\n")
	if i, j := strings.LastIndex(calls, "lightsail get-instance-snapshot "), strings.Index(calls, "lightsail delete-instance "); i < 0 || j < i {
		t.Errorf("the instance was deleted before the snapshot was read back:\n%s", calls)
	}
	if len(w.cli.Instances) != 0 || len(w.cli.StaticIPs) != 0 {
		t.Fatalf("after park: %v %v", w.cli.Instances, w.cli.StaticIPs)
	}
	snap := w.snapshot(t)
	if snap.Tags["keelbox"] != "true" || snap.Tags["keelbox-box"] != "acme" || snap.Tags["keelbox-parked"] != "true" || snap.Tags["keelbox-public-ssh"] != "open" {
		t.Errorf("snapshot tags %v", snap.Tags)
	}
	wantAll(t, "the go-ahead", flat(out), "Park acme: "+parkCardWords("Lightsail instance acme (eu-central-1)",
		"the server's price, about USD 12.00 a month (USD 0.40 a day), before tax. Its static IP is released, so that is not billed either",
		"the snapshot, at Lightsail's price per GB a month for the whole disk, 60 GB, full or not. The Lightsail API gives no price for it",
		"2 minutes", "1 to 2 minutes", "203.0.113.7")+" Park acme? [y/N]")
	wantAll(t, "park", out,
		"keelbox: shutting acme down\n", "keelbox: snapshot "+snap.Name+" is complete (60 GB disk)\n", "keelbox: deleted instance acme\n",
		"keelbox: released static IP keelbox-acme (203.0.113.7)\n", "\nacme is parked in ")
	// No price for the snapshot, which the Lightsail API does not give; the
	// instance's is there.
	wantAll(t, "the end of park", flat(out), parkedWords("snapshot "+snap.Name+", which holds its disk (60 GB disk)", ", about USD 12.00 a month", "203.0.113.7", "1 to 2 minutes"))
	if len(w.asked) != 1 || w.asked[0] != "203.0.113.7 "+modules.CloudInitKeepFile {
		t.Errorf("asked the box: %v", w.asked)
	}
	// Every command that needs the server says parked.
	if _, _, err := cloudPublic(context.Background(), w.cl, w.cfg, "acme"); err == nil || !strings.Contains(err.Error(), "acme has been parked since") || !strings.Contains(err.Error(), "keelbox resume acme") {
		t.Errorf("a command on the parked box: %v", err)
	}
	from := len(w.cli.Calls())
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 || len(w.writes(from)) != 0 || !strings.Contains(out, "keelbox: acme is already parked, since ") || !strings.Contains(out, ": snapshot "+snap.Name+" holds its disk (60 GB disk) and is what is billed. Bring it back: keelbox resume acme\n") {
		t.Errorf("park on a parked box: exit %d, %v\n%s", code, w.writes(from), out)
	}

	from = len(w.cli.Calls())
	code, out = w.do(t, "resume", options{sshUser: "ubuntu"}, nil)
	if code != 0 {
		t.Fatalf("resume: exit %d\n%s", code, out)
	}
	if got := strings.Join(w.writes(from), " "); got != "allocate-static-ip create-instances-from-snapshot put-instance-public-ports attach-static-ip delete-instance-snapshot" {
		t.Errorf("resume's calls: %s", got)
	}
	in := w.cli.Instances["acme"]
	if in == nil || in.Bundle != "small_3_0" || in.FromSnapshot != snap.Name || len(w.cli.Snapshots) != 0 || w.cli.StaticIPs["keelbox-acme"] != "acme" {
		t.Fatalf("after resume: %+v, %v, %v", in, w.cli.Snapshots, w.cli.StaticIPs)
	}
	// The launch script is a comment, and no key pair is named.
	if !strings.HasPrefix(in.UserData, "#!/bin/sh\n# keelbox box acme") || strings.Count(in.UserData, "\n") != 2 || strings.Contains(strings.Join(w.cli.Calls(), "\n"), "key-pair") {
		t.Errorf("user data:\n%s", in.UserData)
	}
	ip := in.IP
	if ip == "203.0.113.7" || len(w.waited) != 1 || w.waited[0] != ip || len(w.applied) != 1 || w.applied[0] != ip+` as "", snapshots 1` {
		t.Errorf("waited %v, applied %v, the new address is %s", w.waited, w.applied, ip)
	}
	if pinned(t, ip) != blob || pinned(t, "203.0.113.7") != "" {
		t.Errorf("pins: %s has %q, 203.0.113.7 has %q", ip, pinned(t, ip), pinned(t, "203.0.113.7"))
	}
	wantAll(t, "resume", out, "creating acme from snapshot "+snap.Name+": small_3_0 in eu-central-1a", "set the firewall of instance acme: in tcp 22, in udp 41641",
		"instance acme running at "+ip+"; waiting for SSH", "keelbox: deleted snapshot "+snap.Name+"\n", "\nacme is back in ")
	wantAll(t, "the end of resume", flat(out), resumedWords(ip, "203.0.113.7", "Snapshot "+snap.Name))
	from = len(w.cli.Calls())
	if code, out := w.do(t, "resume", options{}, nil); code != 0 || len(w.writes(from)) != 0 || !strings.Contains(out, "acme is not parked: instance acme runs at "+ip+". Nothing to resume.") {
		t.Errorf("resume on a running box: exit %d, %v\n%s", code, w.writes(from), out)
	}
}

// A delete that fails after the snapshot leaves the instance off beside it.
// park again finishes: it does not ask the instance, which is off, for the
// cloud-init file the first park found there, and the older snapshot is
// replaced.
func TestAWSParkAgainFinishesAfterAFailedDelete(t *testing.T) {
	w := newAWSWorld(t)
	w.cli.Fail["lightsail delete-instance"] = "no"
	code, out := w.do(t, "park", options{yes: true}, nil)
	in := w.cli.Instances["acme"]
	if code != 1 || in == nil || in.State != "stopped" || len(w.cli.Snapshots) != 1 || len(w.asked) != 1 {
		t.Fatalf("delete fails: exit %d, instance %+v, snapshots %v, asked %v\n%s", code, in, w.cli.Snapshots, w.asked, out)
	}
	wantAll(t, "delete fails", out, "is complete, but deleting the server failed", "instance acme is off, not deleted, and still billed. Delete it and finish: keelbox park acme; or start it again: keelbox resume acme")
	// Later: the first snapshot has an older name, and the instance, being
	// off, answers nothing.
	first := w.snapshot(t)
	delete(w.cli.Snapshots, first.Name)
	first.Name = "keelbox-acme-parked-20261101T000000Z"
	w.cli.Snapshots[first.Name] = first
	delete(w.cli.Fail, "lightsail delete-instance")
	w.hasFile, w.fileErr = false, errors.New("ssh admin@203.0.113.7: exit status 255: connection timed out")
	from := len(w.cli.Calls())
	code, out = w.do(t, "park", options{yes: true}, nil)
	if code != 0 || len(w.cli.Instances) != 0 || len(w.cli.StaticIPs) != 0 || len(w.asked) != 1 {
		t.Fatalf("park again: exit %d, instances %v, asked %v\n%s", code, w.cli.Instances, w.asked, out)
	}
	if got := strings.Join(w.writes(from), " "); got != "create-instance-snapshot delete-instance release-static-ip delete-instance-snapshot" {
		t.Errorf("park again, calls: %s", got)
	}
	if s := w.snapshot(t); s.Name == first.Name {
		t.Errorf("park again kept the older snapshot %s", s.Name)
	}
	if _, ok := loadParkMark("acme"); !ok {
		t.Error("not noted as parked")
	}
	wantAll(t, "park again", out, "keelbox: deleted snapshot "+first.Name+"\n", "\nacme is parked in ", "  bring it back  keelbox resume acme\n")

	// An instance that is off with no snapshot beside it was never checked:
	// park still asks, and stops when it gets no answer.
	w = newAWSWorld(t)
	w.cli.Instances["acme"].State = "stopped"
	w.hasFile, w.fileErr = false, errors.New("ssh admin@203.0.113.7: exit status 255: connection timed out")
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 1 || len(w.writes(0)) != 0 || !strings.Contains(out, "keelbox: nothing changed: acme could not be asked") {
		t.Errorf("an instance that is off, never parked: exit %d, %v\n%s", code, w.writes(0), out)
	}
}

// When the snapshot fails, nothing is left of it, and the instance is not
// deleted: it runs again at its address.
func TestAWSParkNeverDeletesWithoutAConfirmedSnapshot(t *testing.T) {
	for name, breakIt := range map[string]func(*awstest.CLI){
		"Lightsail reports the snapshot failed": func(c *awstest.CLI) { c.SnapshotFails = true },
		"the snapshot is refused":               func(c *awstest.CLI) { c.Fail["lightsail create-instance-snapshot"] = "no" },
		"the snapshot cannot be read":           func(c *awstest.CLI) { c.Fail["lightsail get-instance-snapshot"] = "no" },
		"the instance does not stop":            func(c *awstest.CLI) { c.Fail["lightsail stop-instance"] = "no" },
	} {
		w := newAWSWorld(t)
		breakIt(w.cli)
		code, out := w.do(t, "park", options{yes: true}, nil)
		in := w.cli.Instances["acme"]
		if code != 1 || in == nil || in.State != "" || len(w.cli.Snapshots) != 0 || w.cli.StaticIPs["keelbox-acme"] != "acme" ||
			w.cli.Called("lightsail delete-instance") || w.cli.Called("lightsail release-static-ip") {
			t.Errorf("%s: exit %d, instance %+v, snapshots %v\n%s", name, code, in, w.cli.Snapshots, out)
		}
		if !strings.Contains(out, "acme is not parked: instance acme was not deleted and runs again at 203.0.113.7. Nothing is lost; try again: keelbox park acme") {
			t.Errorf("%s:\n%s", name, out)
		}
		if _, ok := loadParkMark("acme"); ok {
			t.Errorf("%s: the box is noted as parked", name)
		}
	}
}

// resize lists only what Lightsail can change the instance to, says that
// there is no way back, and replaces the instance under its name and at
// its address.
func TestAWSResize(t *testing.T) {
	w := newAWSWorld(t)
	blob := pinned(t, "203.0.113.7")
	code, out := w.do(t, "resize", options{}, str("\n"))
	if code != 0 || len(w.writes(0)) != 0 {
		t.Fatalf("resize, the size it has: exit %d, %v\n%s", code, w.writes(0), out)
	}
	wantAll(t, "the question", out,
		"Prices are estimates from the Lightsail API for eu-central-1, in US dollars a month before tax, and AWS bills by the hour up to that.",
		"only on a plan at least as large", "acme cannot be made smaller again afterwards. 2 vCPU and 4 GB suit one or two agents.",
		"small_3_0", "2 vCPU, 2 GB, 60 GB disk, 3072 GB transfer, USD 12.00/month; small for agents; the size now",
		"medium_3_0", "large_3_0", "acme is small_3_0 already. Nothing changed.")
	for _, not := range []string{"nano_3_0", "medium_win_3_0", "medium_ipv6_3_0", "medium_2_0"} {
		if strings.Contains(out, not) {
			t.Errorf("the list offers %s:\n%s", not, out)
		}
	}
	// A smaller size is refused before anything changes, with what it can
	// change to.
	code, out = w.do(t, "resize", options{size: "nano_3_0", yes: true}, nil)
	if code != 2 || len(w.writes(0)) != 0 || !strings.Contains(out, `--size "nano_3_0" is not a size acme can change to and keep its disk. It is small_3_0 now and can change to medium_3_0, large_3_0: keelbox resize acme --size medium_3_0`+"\n") {
		t.Errorf("resize to a smaller size: exit %d\n%s", code, out)
	}

	// The go-ahead says what a resize does on Lightsail, and offers no way
	// back, because there is none.
	const warning = "Lightsail cannot change an instance's plan: keelbox replaces the instance with a new one made from a snapshot of its disk. " +
		"The disk grows to the new plan's size, and acme cannot be made smaller again afterwards"
	code, out = w.do(t, "resize", options{size: "medium_3_0"}, str("\n"))
	if code != 1 || len(w.writes(0)) != 0 {
		t.Errorf("Enter at the go-ahead: exit %d, %v", code, w.writes(0))
	}
	wantAll(t, "the go-ahead", flat(out),
		"Resize acme from small_3_0 to medium_3_0: from small_3_0: 2 vCPU, 2 GB, 60 GB disk, 3072 GB transfer, USD 12.00/month; small for agents "+
			"to medium_3_0: 2 vCPU, 4 GB, 80 GB disk, 4096 GB transfer, USD 24.00/month "+
			"costs about USD 12.00 a month more than now "+
			"what happens acme restarts: running sessions, agents, dev servers and containers stop. It is back in about 9 minutes, typically "+
			"what stays its address and everything on its disk "+
			"warning "+warning+" Resize acme? [y/N] Nothing changed.")
	if strings.Contains(out, "change back") || strings.Contains(out, "--size small_3_0") {
		t.Errorf("the go-ahead offers a way back to a smaller plan:\n%s", out)
	}

	code, out = w.do(t, "resize", options{size: "medium_3_0", yes: true}, nil)
	if code != 0 {
		t.Fatalf("resize: exit %d\n%s", code, out)
	}
	// What a resize does on Lightsail is said with --yes too.
	wantAll(t, "resize --yes", out, "keelbox: note: "+warning+"\n")
	if got := strings.Join(w.writes(0), " "); got != "stop-instance create-instance-snapshot tag-resource delete-instance create-instances-from-snapshot put-instance-public-ports attach-static-ip delete-instance-snapshot" {
		t.Errorf("resize's calls: %s", got)
	}
	in := w.cli.Instances["acme"]
	if len(w.cli.Instances) != 1 || in.Bundle != "medium_3_0" || in.IP != "203.0.113.7" || in.State != "" || len(w.cli.Snapshots) != 0 || w.cli.StaticIPs["keelbox-acme"] != "acme" {
		t.Fatalf("after resize: %+v, snapshots %v, static IPs %v", in, w.cli.Snapshots, w.cli.StaticIPs)
	}
	if len(w.waited) != 1 || w.waited[0] != "203.0.113.7" || len(w.applied) != 1 || pinned(t, "203.0.113.7") != blob {
		t.Errorf("waited %v, applied %v", w.waited, w.applied)
	}
	if data, _ := os.ReadFile(boxConfigPath("acme")); !strings.Contains(string(data), "bundle: medium_3_0") || w.cfg.Target.AWS.Bundle != "medium_3_0" {
		t.Errorf("the config does not say the new size:\n%s", data)
	}
	wantAll(t, "resize", out, "keelbox: shutting acme down\n", "instance acme is off; changing it from small_3_0 to medium_3_0\n",
		"saving the disk of instance acme as a snapshot", "is complete; deleting instance acme (small_3_0) to create it again as medium_3_0\n",
		"attached static IP keelbox-acme (203.0.113.7)\n", "keelbox: deleted snapshot keelbox-acme-resize-", "set target.aws.bundle: medium_3_0",
		"instance acme is medium_3_0 and running at 203.0.113.7; waiting for SSH\n", "\nacme is medium_3_0 now in ")
	// The disk is beside each size, because here it grew, and there is no
	// way back to offer.
	wantAll(t, "the end of resize", flat(out), resizedWords("medium_3_0: 2 vCPU, 4 GB, 80 GB disk, 4096 GB transfer, USD 24.00/month",
		"small_3_0: 2 vCPU, 2 GB, 60 GB disk, 3072 GB transfer, USD 12.00/month; small for agents",
		"costs about USD 12.00 a month more than before ", "its address and everything on its disk", ""))
	if strings.Contains(out, "change back") {
		t.Errorf("the end of a resize on Lightsail offers a way back:\n%s", out)
	}
	if _, ok := loadParkMark("acme"); ok {
		t.Error("the box is noted as parked after a resize")
	}
}

// The new size cannot be had: the instance is back on the old one, at its
// address, and the config still says the old size.
func TestAWSResizeFailureLeavesTheOldSize(t *testing.T) {
	w := newAWSWorld(t)
	w.cli.FailWith["lightsail create-instances-from-snapshot --bundle-id medium_3_0"] = "no capacity"
	code, out := w.do(t, "resize", options{size: "medium_3_0", yes: true}, nil)
	in := w.cli.Instances["acme"]
	if code != 1 || in == nil || in.Bundle != "small_3_0" || in.IP != "203.0.113.7" || in.State != "" || len(w.cli.Snapshots) != 0 {
		t.Fatalf("exit %d, instance %+v, snapshots %v\n%s", code, in, w.cli.Snapshots, out)
	}
	wantAll(t, "failed resize", out, "keelbox: the size was not changed:", "no capacity", "instance acme was created again as small_3_0\n",
		"keelbox: acme is still small_3_0 and runs again at 203.0.113.7. Try again: keelbox resize acme\n")
	if data, _ := os.ReadFile(boxConfigPath("acme")); !strings.Contains(string(data), "bundle: small_3_0") || len(w.applied) != 0 {
		t.Errorf("config or apply after a failed resize: %v\n%s", w.applied, data)
	}

	// Before the instance is deleted, a failure leaves it untouched.
	w = newAWSWorld(t)
	w.cli.SnapshotFails = true
	code, out = w.do(t, "resize", options{size: "medium_3_0", yes: true}, nil)
	in = w.cli.Instances["acme"]
	if code != 1 || in == nil || in.FromSnapshot != "" || in.State != "" || len(w.cli.Snapshots) != 0 || w.cli.Called("lightsail delete-instance") ||
		!strings.Contains(out, "acme is still small_3_0 and runs again at 203.0.113.7") {
		t.Errorf("failed snapshot: exit %d, instance %+v\n%s", code, in, out)
	}
}

// No instance can be created again: the box is left parked with its
// address, resize says so, and resume brings it back there.
func TestAWSResizeLeavesTheBoxParked(t *testing.T) {
	w := newAWSWorld(t)
	blob := pinned(t, "203.0.113.7")
	w.cli.Fail["lightsail create-instances-from-snapshot"] = "no capacity"
	code, out := w.do(t, "resize", options{size: "medium_3_0", yes: true}, nil)
	if code != 1 || len(w.cli.Instances) != 0 || w.cli.Called("lightsail release-static-ip") || w.cli.Called("lightsail start-instance") {
		t.Fatalf("exit %d, instances %v\n%s", code, w.cli.Instances, out)
	}
	snap := w.snapshot(t)
	if snap.Tags["keelbox-parked"] != "true" || snap.Tags["keelbox-box"] != "acme" {
		t.Errorf("snapshot tags %v", snap.Tags)
	}
	wantAll(t, "resize", out, "keelbox: the size was not changed:", "no capacity",
		"keelbox: acme is parked: snapshot "+snap.Name+" holds its disk. Bring it back: keelbox resume acme\n")
	if strings.Contains(out, "is still small_3_0") {
		t.Errorf("resize says the box still has a server:\n%s", out)
	}
	if mark, ok := loadParkMark("acme"); !ok || mark.Address != "203.0.113.7" || mark.Snapshot != "snapshot "+snap.Name {
		t.Errorf("mark %+v %v", mark, ok)
	}
	if data, _ := os.ReadFile(boxConfigPath("acme")); !strings.Contains(string(data), "bundle: small_3_0") {
		t.Errorf("the config says a size the box never had:\n%s", data)
	}
	if _, _, err := cloudPublic(context.Background(), w.cl, w.cfg, "acme"); err == nil || !strings.Contains(err.Error(), "keelbox resume acme") {
		t.Errorf("a command on the box: %v", err)
	}
	delete(w.cli.Fail, "lightsail create-instances-from-snapshot")
	code, out = w.do(t, "resume", options{}, nil)
	in := w.cli.Instances["acme"]
	if code != 0 || in == nil || in.Bundle != "small_3_0" || in.IP != "203.0.113.7" || len(w.cli.Snapshots) != 0 || w.cli.Called("lightsail allocate-static-ip") {
		t.Fatalf("resume: exit %d, %+v, %v\n%s", code, in, w.cli.Snapshots, out)
	}
	if pinned(t, "203.0.113.7") != blob {
		t.Error("the host keys pinned for the address the box kept were dropped")
	}
	wantAll(t, "resume", out, "using the static IP keelbox-acme (203.0.113.7) the box kept", "acme is back in ")
}

// A resize that stopped after the new instance was up leaves the snapshot
// beside it. Every other command works on the instance, and resume
// finishes: it applies and deletes the snapshot.
func TestAWSInstanceBesideASnapshotOfAResize(t *testing.T) {
	w := newAWSWorld(t)
	w.cli.Snapshots["keelbox-acme-resize-1"] = &awstest.Snapshot{Name: "keelbox-acme-resize-1", From: "acme", Bundle: "nano_3_0", SizeGB: 20, Created: 1794000000,
		Tags: map[string]string{"keelbox": "true", "keelbox-box": "acme", "keelbox-resize": "true", "keelbox-parked": "true", "keelbox-public-ssh": "open"}}
	a, srv, err := cloudPublic(context.Background(), w.cl, w.cfg, "acme")
	if err != nil || a.host != "203.0.113.7" || srv.ref != "instance acme" {
		t.Errorf("a command on the box: %v %v %v", a, srv, err)
	}
	code, out := w.do(t, "resume", options{}, nil)
	if code != 0 || len(w.cli.Snapshots) != 0 || len(w.applied) != 1 || w.cli.Instances["acme"].FromSnapshot != "" || called(w.cli, "create-instances-from-snapshot") != 0 {
		t.Errorf("resume: exit %d, snapshots %v, applied %v\n%s", code, w.cli.Snapshots, w.applied, out)
	}
	wantAll(t, "resume", out, "instance acme exists and snapshot keelbox-acme-resize-1 is still kept: finishing what an earlier park or resume left", "keelbox: deleted snapshot keelbox-acme-resize-1\n")
	// The same with the instance switched off, as a resize leaves it that
	// stopped between the tagging and the deletion.
	w = newAWSWorld(t)
	w.cli.Instances["acme"].State = "stopped"
	w.cli.Snapshots["keelbox-acme-resize-1"] = &awstest.Snapshot{Name: "keelbox-acme-resize-1", From: "acme", Bundle: "small_3_0", SizeGB: 60, Created: 1794000000,
		Tags: map[string]string{"keelbox": "true", "keelbox-box": "acme", "keelbox-resize": "true", "keelbox-parked": "true"}}
	code, out = w.do(t, "resume", options{}, nil)
	if code != 0 || len(w.cli.Snapshots) != 0 || w.cli.Instances["acme"].State != "" || w.cli.Instances["acme"].FromSnapshot != "" {
		t.Errorf("resume with the instance off: exit %d, %+v\n%s", code, w.cli.Instances["acme"], out)
	}
}

func called(cli *awstest.CLI, op string) int {
	n := 0
	for _, c := range cli.Calls() {
		if strings.HasPrefix(c, "lightsail "+op+" ") {
			n++
		}
	}
	return n
}

// Without the cloud-init file on the box a new instance would make new
// host keys: resize and park stop before any change and name the apply.
func TestAWSParkAndResizeNeedTheCloudInitFileOnTheBox(t *testing.T) {
	for _, cmd := range []string{"park", "resize"} {
		w := newAWSWorld(t)
		w.hasFile = false
		code, out := w.do(t, cmd, options{size: "medium_3_0", yes: true}, nil)
		want := "keelbox: nothing changed: acme has no /etc/cloud/cloud.cfg.d/99-keelbox.cfg. Without it a new Lightsail instance makes new SSH host keys, " +
			"which this computer would refuse. Put it there: keelbox apply acme; then: keelbox " + cmd + " acme\n"
		if code != 1 || out != want || len(w.writes(0)) != 0 {
			t.Errorf("%s without the file: exit %d, %v\n%s", cmd, code, w.writes(0), out)
		}
		w.fileErr = errors.New("ssh admin@203.0.113.7: exit status 255: connection timed out")
		code, out = w.do(t, cmd, options{size: "medium_3_0", yes: true}, nil)
		if code != 1 || len(w.writes(0)) != 0 || !strings.Contains(out, "acme could not be asked whether it has /etc/cloud/cloud.cfg.d/99-keelbox.cfg (ssh admin@203.0.113.7: exit status 255: connection timed out). Without it") ||
			!strings.HasSuffix(out, "Put it there: keelbox apply acme; then: keelbox "+cmd+" acme\n") {
			t.Errorf("%s with a box that does not answer: exit %d\n%s", cmd, code, out)
		}
	}
}

// destroy on a parked box deletes every snapshot keelbox made for it and
// a static IP it kept; nothing without the box's tags is touched.
func TestAWSDestroyParkedBox(t *testing.T) {
	w := newAWSWorld(t)
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	parked := w.snapshot(t).Name
	tags := func(extra map[string]string) map[string]string {
		l := map[string]string{"keelbox": "true", "keelbox-box": "acme"}
		for k, v := range extra {
			l[k] = v
		}
		return l
	}
	w.cli.Snapshots["half-done"] = &awstest.Snapshot{Name: "half-done", Tags: tags(map[string]string{"keelbox-parked": "true"}), State: "error"}
	w.cli.Snapshots["of-a-resize"] = &awstest.Snapshot{Name: "of-a-resize", Tags: tags(map[string]string{"keelbox-resize": "true"})}
	w.cli.Snapshots["yours"] = &awstest.Snapshot{Name: "yours", Tags: tags(nil)}
	w.cli.Snapshots["another-box"] = &awstest.Snapshot{Name: "another-box", Tags: map[string]string{"keelbox": "true", "keelbox-box": "other", "keelbox-parked": "true"}}
	w.cli.StaticIPs["keelbox-acme"], w.cli.StaticAddr["keelbox-acme"] = "", "203.0.113.7"
	// Nobody to ask about work nobody can look at: refused, --yes or not.
	code, out := w.do(t, "destroy", options{yes: true}, nil)
	if code != 1 || len(w.cli.Snapshots) != 5 || !strings.Contains(out, "keelbox destroy acme --yes --discard-work") {
		t.Fatalf("destroy without --discard-work: exit %d\n%s", code, out)
	}
	code, out = w.do(t, "destroy", options{yes: true, discardWork: true}, nil)
	if code != 0 {
		t.Fatalf("destroy: exit %d\n%s", code, out)
	}
	if len(w.cli.Snapshots) != 2 || w.cli.Snapshots["yours"] == nil || w.cli.Snapshots["another-box"] == nil || len(w.cli.StaticIPs) != 0 {
		t.Errorf("left: %v, %v", w.cli.Snapshots, w.cli.StaticIPs)
	}
	wantAll(t, "destroy", out, "keelbox: deleted snapshot "+parked+"\n", "keelbox: deleted snapshot half-done\n", "keelbox: deleted snapshot of-a-resize\n",
		"keelbox: released static IP keelbox-acme (203.0.113.7)\n")
	if _, ok := loadParkMark("acme"); ok {
		t.Error("the parked note is still there")
	}
}

// Instances and snapshots without keelbox's tags for the box are never
// changed by the three commands.
func TestAWSParkTouchesOnlyTaggedResources(t *testing.T) {
	w := newAWSWorld(t)
	w.cli.Instances["acme"].Tags = map[string]string{"owner": "sam"}
	w.cli.Snapshots["by-hand"] = &awstest.Snapshot{Name: "by-hand", From: "acme", Tags: map[string]string{"keelbox-parked": "true", "keelbox-box": "acme"}}
	for _, cmd := range []string{"park", "resize", "resume"} {
		code, out := w.do(t, cmd, options{size: "medium_3_0", yes: true}, nil)
		if code != 1 || !strings.Contains(out, "was not created by keelbox") {
			t.Errorf("%s: exit %d\n%s", cmd, code, out)
		}
	}
	if len(w.writes(0)) != 0 {
		t.Errorf("changed something: %v", w.writes(0))
	}
}

// destroy on a box with an instance also deletes a snapshot an
// interrupted park or resize left beside it.
func TestAWSDestroyTakesLeftoverSnapshots(t *testing.T) {
	w := newAWSWorld(t)
	w.cli.Snapshots["of-a-resize"] = &awstest.Snapshot{Name: "of-a-resize", Tags: map[string]string{"keelbox": "true", "keelbox-box": "acme", "keelbox-resize": "true"}}
	w.cli.Snapshots["parked"] = &awstest.Snapshot{Name: "parked", Tags: map[string]string{"keelbox": "true", "keelbox-box": "acme", "keelbox-parked": "true"}}
	w.cli.Snapshots["yours"] = &awstest.Snapshot{Name: "yours", Tags: map[string]string{"keelbox-box": "acme"}}
	code, out := w.do(t, "destroy", options{yes: true, discardWork: true}, nil)
	if code != 0 || len(w.cli.Instances) != 0 || len(w.cli.StaticIPs) != 0 || len(w.cli.Snapshots) != 1 || w.cli.Snapshots["yours"] == nil {
		t.Errorf("exit %d, left %v %v %v\n%s", code, w.cli.Instances, w.cli.StaticIPs, w.cli.Snapshots, out)
	}
	wantAll(t, "destroy", out, "keelbox: deleted instance acme\n", "keelbox: deleted snapshot of-a-resize\n", "keelbox: deleted snapshot parked\n")
}
