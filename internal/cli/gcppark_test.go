package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/gcp"
	"github.com/keelbox/keelbox/internal/provider/gcp/gcptest"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// gcpWorld is the box acme in the registry and in a fake Google Cloud
// project (the VM on its reserved address 203.0.113.7 behind its two
// rules), its host key pinned, and a record of what the commands did past
// gcloud.
type gcpWorld struct {
	cli *gcptest.CLI
	cfg *config.Config
	cl  *gcpCloud
	// waited and applied are the addresses boxComesBack and applyExisting
	// were called with; applyCode is what the apply returns.
	waited, applied []string
	applyCode       int
	// terminal is whether destroy finds a person to ask.
	terminal bool
	// from is where the calls of the command under test start.
	from int
}

func newGCPWorld(t *testing.T) *gcpWorld {
	t.Helper()
	isolate(t)
	cli := fakeGCP(t)
	in := gcpBox(cli)
	s := config.StarterFor("acme", "gcp")
	s.Project = "acme-prod"
	s.Keys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}
	cfg, err := config.Load(register(t, s))
	if err != nil {
		t.Fatal(err)
	}
	w := &gcpWorld{cli: cli, cfg: cfg, cl: &gcpCloud{cl: gcpClient("acme-prod"), cfg: cfg}}
	if err := sshTarget(cfg, addr{in.IP, 22}, options{}, io.Discard, io.Discard).Pin(provider.ParseHostKeys(boxHostKey)); err != nil {
		t.Fatal(err)
	}
	oldWait, oldApply, oldTerm, oldProbe, oldClosed := boxComesBack, applyExisting, stdinIsTerminal, boxProjects, publicSSHClosed
	t.Cleanup(func() {
		boxComesBack, applyExisting, stdinIsTerminal, boxProjects, publicSSHClosed = oldWait, oldApply, oldTerm, oldProbe, oldClosed
	})
	publicSSHClosed = func(context.Context, *config.Config, *cloudServer) bool { return false }
	boxComesBack = func(_ context.Context, _ *config.Config, public addr, _, _ io.Writer) error {
		w.waited = append(w.waited, public.host)
		return nil
	}
	applyExisting = func(_ context.Context, _ cloud, _ *config.Config, _ *cloudServer, public addr, o options, _ func(string, ...any), _, _ io.Writer) int {
		w.applied = append(w.applied, fmt.Sprintf("%s as %q, snapshots %d", public.host, o.sshUser, len(cli.Snapshots)))
		return w.applyCode
	}
	stdinIsTerminal = func() bool { return w.terminal }
	boxProjects = func(context.Context, *config.Config, string, options) (*snapshot.Projects, error) {
		return cleanBox, nil
	}
	return w
}

// do runs one of the commands as Main would, with input as the terminal's
// answers (nil: no terminal).
func (w *gcpWorld) do(t *testing.T, cmd string, o options, input *string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	p := prompt.New(strings.NewReader(""), &out, false)
	if input != nil {
		p = prompt.New(strings.NewReader(*input), &out, true)
	}
	o.configFile = boxConfigPath("acme")
	logf := func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) }
	ctx := context.Background()
	w.from = len(w.cli.Calls())
	switch cmd {
	case "park":
		return cloudPark(ctx, w.cl, w.cfg, o, p, logf, &out, &out), out.String()
	case "resume":
		return cloudResume(ctx, w.cl, w.cfg, o, logf, &out, &out), out.String()
	case "resize":
		return cloudResize(ctx, w.cl, w.cfg, o, p, logf, &out, &out), out.String()
	case "destroy":
		in := ""
		if input != nil {
			in = *input
		}
		return cloudDestroy(ctx, w.cl, w.cfg, o, strings.NewReader(in), logf, &out, &out), out.String()
	}
	t.Fatalf("no command %s", cmd)
	return 0, ""
}

// writes are the calls of the last command that changed the project, in
// order, without the reads between them.
func (w *gcpWorld) writes() string {
	var out []string
	for _, c := range w.cli.Calls()[w.from:] {
		f := strings.Fields(c)
		if len(f) < 4 || f[0] != "compute" || f[2] == "list" || f[2] == "describe" {
			continue
		}
		out = append(out, strings.Join(f[1:4], " "))
	}
	return strings.Join(out, "\n")
}

func (w *gcpWorld) vm() *gcptest.Instance { return w.cli.Instances["europe-west3-a/acme"] }

// holds is what the project holds for the box.
func (w *gcpWorld) holds() string {
	return fmt.Sprintf("%d VMs, %d disks, %d addresses, %d snapshots, %d rules", len(w.cli.Instances), len(w.cli.Disks), len(w.cli.Addresses), len(w.cli.Snapshots), len(w.cli.Rules))
}

// park stops the VM, takes the snapshot, waits until Google reports it
// READY and only then deletes the VM with its disk and releases the
// address; the rules stay. resume reserves an address, creates the VM
// from the snapshot, moves the pinned host keys, applies as the admin and
// deletes the snapshot only after that apply.
func TestGCPParkThenResume(t *testing.T) {
	w := newGCPWorld(t)
	blob := pinned(t, "203.0.113.7")
	code, out := w.do(t, "park", options{yes: true}, nil)
	if code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	if got, want := w.writes(), "instances stop acme\nsnapshots create keelbox-acme-\ninstances delete acme\naddresses delete keelbox-acme"; !sameCalls(got, want) {
		t.Errorf("park's calls:\n%s\nwant:\n%s", got, want)
	}
	if w.holds() != "0 VMs, 0 disks, 0 addresses, 1 snapshots, 2 rules" {
		t.Fatalf("after park: %s", w.holds())
	}
	var snap *gcptest.Snapshot
	for _, s := range w.cli.Snapshots {
		snap = s
	}
	// The snapshot was read as READY before the VM was deleted.
	calls := strings.Join(w.cli.Calls(), "\n")
	if i, j := strings.LastIndex(calls, "compute snapshots describe "+snap.Name), strings.Index(calls, "compute instances delete acme"); i < 0 || j < i {
		t.Errorf("the VM was deleted before the snapshot was read back:\n%s", calls)
	}
	if snap.Labels["keelbox"] != "true" || snap.Labels["keelbox-box"] != "acme" || snap.Labels["keelbox-parked"] != "true" {
		t.Errorf("snapshot labels %v", snap.Labels)
	}
	wantAll(t, "park", out, "keelbox: shutting acme down\n", "keelbox: VM acme is off; saving its disk as a snapshot",
		"keelbox: snapshot "+snap.Name+" is complete (1.3 GB stored of a 50 GB disk)\n",
		"keelbox: deleted VM acme (europe-west3-a) and its disk\n", "keelbox: released address keelbox-acme (203.0.113.7)\n",
		"\nacme is parked in ")
	// No prices, which gcloud does not have: what is stored, and no amount.
	wantAll(t, "the end of park", flat(out), parkedWords("snapshot "+snap.Name+", which holds its disk (1.3 GB stored of a 50 GB disk)", "", "203.0.113.7", "2 minutes"))
	if strings.Contains(out, "USD") || strings.Contains(out, "EUR") {
		t.Errorf("park names a price on Google Cloud:\n%s", out)
	}
	mark, ok := loadParkMark("acme")
	if !ok || mark.Address != "203.0.113.7" || mark.Snapshot != "snapshot "+snap.Name || time.Since(mark.Since) > time.Minute {
		t.Errorf("mark %+v %v", mark, ok)
	}

	// Parked again: said, exit 0, nothing sent.
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 || w.writes() != "" || !strings.Contains(out, "keelbox: acme is already parked, since "+day(mark.Since)+": snapshot "+snap.Name+" holds its disk (1.3 GB stored of a 50 GB disk) and is what is billed. Bring it back: keelbox resume acme\n") {
		t.Errorf("park on a parked box: exit %d, %q\n%s", code, w.writes(), out)
	}
	// Every command that needs the VM says parked, with the fix.
	if _, _, err := cloudPublic(context.Background(), w.cl, w.cfg, "acme"); err == nil || !strings.Contains(err.Error(), "acme has been parked since") || !strings.Contains(err.Error(), "keelbox resume acme") {
		t.Errorf("a command on a parked box: %v", err)
	}

	code, out = w.do(t, "resume", options{sshUser: "root"}, nil)
	if code != 0 {
		t.Fatalf("resume: exit %d\n%s", code, out)
	}
	if got, want := w.writes(), "addresses create keelbox-acme\ninstances create acme\nsnapshots delete "+snap.Name; got != want {
		t.Errorf("resume's calls:\n%s\nwant:\n%s", got, want)
	}
	if w.holds() != "1 VMs, 1 disks, 1 addresses, 0 snapshots, 2 rules" {
		t.Fatalf("after resume: %s", w.holds())
	}
	vm := w.vm()
	ip := vm.IP
	// Waited for, then applied as the admin while the snapshot was still there.
	if len(w.waited) != 1 || w.waited[0] != ip || len(w.applied) != 1 || w.applied[0] != ip+` as "", snapshots 1` {
		t.Errorf("waited %v, applied %v, the new address is %s", w.waited, w.applied, ip)
	}
	// The host keys this computer trusted are pinned under the new
	// address, and no longer under the one given up.
	if ip == "203.0.113.7" || pinned(t, ip) != blob || pinned(t, "203.0.113.7") != "" {
		t.Errorf("pins: %s has %q, 203.0.113.7 has %q", ip, pinned(t, ip), pinned(t, "203.0.113.7"))
	}
	if _, ok := loadParkMark("acme"); ok {
		t.Error("the parked note is still there after resume")
	}
	if vm.SourceSnapshot != snap.Name || vm.UserData != gcp.ResumeUserData("acme") || vm.ServiceAccount != "" || strings.Join(vm.Tags, ",") != "keelbox-box-acme" ||
		vm.Metadata["block-project-ssh-keys"] != "true" || vm.Metadata["enable-oslogin"] != "false" || vm.Metadata["ssh-keys"] != "" {
		t.Errorf("the VM resume created: %+v", vm)
	}
	wantAll(t, "resume", out, "keelbox: reserved address keelbox-acme ("+ip+") in europe-west3\n", "creating acme from snapshot "+snap.Name+": e2-medium in europe-west3-a, 50 GB disk",
		"running at "+ip+"; waiting for SSH", "keelbox: deleted snapshot "+snap.Name+"\n", "\nacme is back in ")
	wantAll(t, "the end of resume", flat(out), resumedWords(ip, "203.0.113.7", "Snapshot "+snap.Name))

	// Running: said, exit 0, nothing sent.
	if code, out := w.do(t, "resume", options{}, nil); code != 0 || w.writes() != "" || !strings.Contains(out, "acme is not parked: VM acme runs at "+ip+". Nothing to resume.") {
		t.Errorf("resume on a running box: exit %d, %q\n%s", code, w.writes(), out)
	}
}

// sameCalls compares calls line by line; a wanted line that ends in "-"
// matches any call it is a prefix of (a snapshot's name ends in its time).
func sameCalls(got, want string) bool {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		prefix := strings.HasSuffix(w[i], "-") && strings.HasPrefix(g[i], w[i])
		if g[i] != w[i] && !prefix {
			return false
		}
	}
	return true
}

// park says what happens and what stays billed, and asks, No by default.
func TestGCPParkAsksFirst(t *testing.T) {
	w := newGCPWorld(t)
	code, out := w.do(t, "park", options{}, str("\n"))
	if code != 1 || w.writes() != "" {
		t.Errorf("Enter at the question: exit %d, %q", code, w.writes())
	}
	wantAll(t, "the question", flat(out), "Park acme: "+parkCardWords("VM acme (e2-medium, europe-west3-a)",
		"the server's price, and those of its disk and its reserved address, which are given up with it. gcloud prints no prices; Google's are at https://cloud.google.com/compute/vm-instance-pricing",
		"the snapshot, at Google's price per GB a month for what the disk holds. gcloud prints no price for it either; see https://cloud.google.com/compute/disks-image-pricing",
		"2 to 3 minutes", "2 minutes", "203.0.113.7")+" Park acme? [y/N] Nothing changed.")
	if code, out := w.do(t, "park", options{}, nil); code != 2 || w.writes() != "" || !strings.Contains(out, "keelbox park acme --yes") {
		t.Errorf("no terminal: exit %d, %q\n%s", code, w.writes(), out)
	}
	// A VM with a disk park would not keep: said before the question.
	w.vm().ExtraDisks = []string{"data-1"}
	code, out = w.do(t, "park", options{yes: true}, nil)
	if code != 1 || w.writes() != "" || !strings.Contains(out, "gcloud compute instances detach-disk acme --disk data-1 --zone europe-west3-a --project acme-prod; then: keelbox park acme") {
		t.Errorf("a VM with a data disk: exit %d, %q\n%s", code, w.writes(), out)
	}
}

// Whatever fails before the delete, the VM is not deleted: it is started
// again, no snapshot is left, and park says so.
func TestGCPParkNeverDeletesWithoutAConfirmedSnapshot(t *testing.T) {
	for name, breakIt := range map[string]func(*gcptest.CLI){
		"the stop fails":              func(c *gcptest.CLI) { c.Fail["compute instances stop"] = "The service is currently unavailable." },
		"the snapshot is refused":     func(c *gcptest.CLI) { c.Fail["compute snapshots create"] = "Quota 'SNAPSHOTS' exceeded." },
		"the snapshot fails":          func(c *gcptest.CLI) { c.SnapshotFails = true },
		"the snapshot cannot be read": func(c *gcptest.CLI) { c.Fail["compute snapshots describe"] = "The service is currently unavailable." },
	} {
		w := newGCPWorld(t)
		breakIt(w.cli)
		code, out := w.do(t, "park", options{yes: true}, nil)
		if code != 1 || w.vm() == nil || w.vm().Status != "" || w.holds() != "1 VMs, 1 disks, 1 addresses, 0 snapshots, 2 rules" {
			t.Errorf("%s: exit %d, %s, VM %+v\n%s", name, code, w.holds(), w.vm(), out)
		}
		if strings.Contains(w.writes(), "instances delete") || strings.Contains(w.writes(), "addresses delete") {
			t.Errorf("%s: sent\n%s", name, w.writes())
		}
		if _, ok := loadParkMark("acme"); ok {
			t.Errorf("%s: noted as parked", name)
		}
		wantAll(t, name, out, "keelbox: acme is not parked: VM acme was not deleted and runs again at 203.0.113.7. Nothing is lost; try again: keelbox park acme\n")
	}

	// The VM cannot be started again either: said, with the command that
	// starts it, and resume does start a box that is off and not parked.
	w := newGCPWorld(t)
	w.cli.Fail["compute snapshots create"] = "Quota 'SNAPSHOTS' exceeded."
	w.cli.Fail["compute instances start"] = "The service is currently unavailable."
	code, out := w.do(t, "park", options{yes: true}, nil)
	if code != 1 || w.vm() == nil || w.vm().Status != "TERMINATED" || !strings.Contains(out, "acme is not parked and not deleted, but starting it again failed") || !strings.Contains(out, "Start it: keelbox resume acme") {
		t.Errorf("start fails too: exit %d\n%s", code, out)
	}
	w.cli.Fail = map[string]string{}
	code, out = w.do(t, "resume", options{}, nil)
	if code != 0 || w.vm().Status != "" || w.writes() != "instances start acme" || len(w.applied) != 0 ||
		!strings.Contains(out, "keelbox: acme is not parked, but VM acme is off; starting it\n") || !strings.Contains(out, "keelbox: VM acme runs again at 203.0.113.7. Check it: keelbox doctor acme\n") {
		t.Errorf("resume of a box that is off: exit %d, %q\n%s", code, w.writes(), out)
	}
}

// A resume whose apply fails keeps the snapshot, and run again it carries
// on with the VM that exists.
func TestGCPResumeKeepsTheSnapshotUntilAnApplyPassed(t *testing.T) {
	w := newGCPWorld(t)
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	w.applyCode = 1
	code, out := w.do(t, "resume", options{}, nil)
	if code != 1 || w.holds() != "1 VMs, 1 disks, 1 addresses, 1 snapshots, 2 rules" || !strings.Contains(out, "is kept until an apply has passed") {
		t.Fatalf("resume with a failing apply: exit %d, %s\n%s", code, w.holds(), out)
	}
	w.applyCode = 0
	code, out = w.do(t, "resume", options{}, nil)
	if code != 0 || w.holds() != "1 VMs, 1 disks, 1 addresses, 0 snapshots, 2 rules" || !strings.HasPrefix(w.writes(), "snapshots delete keelbox-acme-") ||
		!strings.Contains(out, "finishing what an earlier park or resume left") {
		t.Errorf("resume again: exit %d, %s, %q\n%s", code, w.holds(), w.writes(), out)
	}

	// A restore that creates no VM leaves the box parked, and no address.
	w = newGCPWorld(t)
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	w.cli.Fail["compute instances create"] = "The zone 'projects/acme-prod/zones/europe-west3-a' does not have enough resources available to fulfill the request."
	code, out = w.do(t, "resume", options{}, nil)
	if code != 1 || w.holds() != "0 VMs, 0 disks, 0 addresses, 1 snapshots, 2 rules" || !strings.Contains(out, "acme is still parked: snapshot keelbox-acme-") || !strings.Contains(out, "Try again: keelbox resume acme") {
		t.Errorf("resume without capacity: exit %d, %s\n%s", code, w.holds(), out)
	}
}

// resize stops the VM, changes its machine type, starts it at the address
// it had, rewrites the size in the config and applies.
func TestGCPResize(t *testing.T) {
	w := newGCPWorld(t)
	before := configText(t)
	code, out := w.do(t, "resize", options{size: "e2-standard-2", yes: true, sshUser: "root"}, nil)
	if code != 0 {
		t.Fatalf("resize: exit %d\n%s", code, out)
	}
	if got, want := w.writes(), "instances stop acme\ninstances set-machine-type acme\ninstances start acme"; got != want {
		t.Errorf("calls:\n%s\nwant:\n%s", got, want)
	}
	if vm := w.vm(); vm.MachineType != "e2-standard-2" || vm.Status != "" || vm.IP != "203.0.113.7" || w.holds() != "1 VMs, 1 disks, 1 addresses, 0 snapshots, 2 rules" {
		t.Errorf("VM after resize: %+v, %s", vm, w.holds())
	}
	// Waited for and applied as the admin, at the address it kept.
	if len(w.waited) != 1 || w.waited[0] != "203.0.113.7" || len(w.applied) != 1 || !strings.HasPrefix(w.applied[0], `203.0.113.7 as ""`) {
		t.Errorf("waited %v, applied %v", w.waited, w.applied)
	}
	wantAll(t, "resize", out, "keelbox: shutting acme down\n", "keelbox: VM acme is off; changing it from e2-medium to e2-standard-2\n",
		"keelbox: set target.gcp.machine_type: e2-standard-2 in ~/.config/keelbox/boxes/acme.yaml\n",
		"\nacme is e2-standard-2 now in ")
	wantAll(t, "the end of resize", flat(out), resizedWords("e2-standard-2: 2 vCPU, 8 GB", "e2-medium: 2 vCPU (shared), 4 GB", "", "its 50 GB disk, its address and everything on it", "e2-medium"))
	after := configText(t)
	if !strings.Contains(after, "    machine_type: e2-standard-2") || strings.Contains(after, "machine_type: e2-medium") || len(strings.Split(after, "\n")) != len(strings.Split(before, "\n")) {
		t.Errorf("config after resize:\n%s", after)
	}
	if w.cfg.Target.GCP.MachineType != "e2-standard-2" {
		t.Errorf("the config the apply ships says %s", w.cfg.Target.GCP.MachineType)
	}
	// And back down.
	if code, out := w.do(t, "resize", options{size: "e2-medium", yes: true}, nil); code != 0 || w.vm().MachineType != "e2-medium" || configText(t) != before {
		t.Errorf("resize down: exit %d\n%s", code, out)
	}
	// The size it has: said, nothing sent.
	if code, out := w.do(t, "resize", options{size: "e2-medium", yes: true}, nil); code != 0 || w.writes() != "" || !strings.Contains(out, "keelbox: acme is e2-medium already. Nothing changed.") {
		t.Errorf("resize to the size it has: exit %d\n%s", code, out)
	}
	// A size that is not listed: an Arm type, a retired one, none at all.
	for _, size := range []string{"t2a-standard-2", "n1-retired-2", "nope"} {
		code, out := w.do(t, "resize", options{size: size, yes: true}, nil)
		if code != 2 || w.writes() != "" || !strings.Contains(out, `--size "`+size+`" is not a size acme can change to and keep its disk. It is e2-medium now and can change to e2-micro, e2-small, e2-standard-2, e2-standard-4: keelbox resize acme --size e2-standard-2`+"\n") {
			t.Errorf("--size %s: exit %d, %q\n%s", size, code, w.writes(), out)
		}
	}
}

// On a terminal resize lists the sizes without prices, says where Google's
// are, and marks the size the VM has.
func TestGCPResizeAsks(t *testing.T) {
	w := newGCPWorld(t)
	code, out := w.do(t, "resize", options{}, str("4\n\n"))
	if code != 1 || w.writes() != "" {
		t.Errorf("Enter at the go-ahead: exit %d, %q", code, w.writes())
	}
	wantAll(t, "the questions", out,
		"Machine types in europe-west3-a, smallest first. gcloud prints no prices; Google's are at https://cloud.google.com/compute/vm-instance-pricing, billed by the second. "+
			"The disk and the address stay as they are at every size, so you can change back from any size listed. 2 vCPU and 4 GB suit one or two agents.",
		"Size for acme (it is e2-medium now):",
		"e2-micro", "2 vCPU (shared), 1 GB; small for agents",
		"e2-medium", "2 vCPU (shared), 4 GB; the size now",
		"e2-standard-4", "4 vCPU, 16 GB",
		"Resize acme? [y/N] ", "Nothing changed.\n")
	wantAll(t, "the go-ahead", flat(out),
		"Resize acme from e2-medium to e2-standard-2: from e2-medium: 2 vCPU (shared), 4 GB to e2-standard-2: 2 vCPU, 8 GB "+
			"what happens acme restarts: running sessions, agents, dev servers and containers stop. It is back in 2 to 3 minutes, typically "+
			"what stays its 50 GB disk, its address and everything on it "+
			"change back keelbox resize acme --size e2-medium Resize acme? [y/N] Nothing changed.")
	if strings.Contains(out, "costs") || strings.Contains(out, "saves") {
		t.Errorf("the go-ahead says what the change costs without a price:\n%s", out)
	}
	for _, not := range []string{"t2a-standard-2", "n1-retired-2", "e2-standard-16", "c3-standard-4", "EUR", "USD", "$"} {
		if strings.Contains(out, not) {
			t.Errorf("the list holds %q:\n%s", not, out)
		}
	}
}

// A change Google refuses leaves the box as it was, running, and the
// config untouched.
func TestGCPResizeThatFailsStartsTheVMAgain(t *testing.T) {
	for _, call := range []string{"compute instances stop", "compute instances set-machine-type"} {
		w := newGCPWorld(t)
		before := configText(t)
		w.cli.Fail[call] = "Quota 'CPUS' exceeded.  Limit: 8.0 in region europe-west3."
		code, out := w.do(t, "resize", options{size: "e2-standard-4", yes: true}, nil)
		if vm := w.vm(); code != 1 || vm.MachineType != "e2-medium" || vm.Status != "" || configText(t) != before || len(w.applied) != 0 {
			t.Errorf("%s fails: exit %d, VM %+v\n%s", call, code, w.vm(), out)
		}
		wantAll(t, call, out, "Quota 'CPUS' exceeded", "keelbox: acme is still e2-medium and runs again at 203.0.113.7. Try again: keelbox resize acme\n")
	}
}

// destroy on a parked box deletes the snapshot and the rules park kept;
// destroy of a box with a VM also deletes a parked snapshot left beside it.
func TestGCPDestroyParked(t *testing.T) {
	w := newGCPWorld(t)
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	// Nobody could look at the box: --yes does not answer for its work.
	code, out := w.do(t, "destroy", options{yes: true}, nil)
	if code != 1 || w.writes() != "" || !strings.Contains(out, "keelbox destroy acme --yes --discard-work") {
		t.Errorf("destroy of a parked box without --discard-work: exit %d, %q\n%s", code, w.writes(), out)
	}
	// An unfinished snapshot an interrupted park left goes too.
	w.cli.AddSnapshot("keelbox-acme-unfinished", "acme").Status = "UPLOADING"
	code, out = w.do(t, "destroy", options{yes: true, discardWork: true}, nil)
	if code != 0 || w.holds() != "0 VMs, 0 disks, 0 addresses, 0 snapshots, 0 rules" {
		t.Fatalf("destroy of a parked box: exit %d, %s\n%s", code, w.holds(), out)
	}
	wantAll(t, "destroy", out, "keelbox: deleted snapshot keelbox-acme-2", "keelbox: deleted snapshot keelbox-acme-unfinished\n",
		"keelbox: deleted firewall rule keelbox-acme-allow\n", "keelbox: deleted firewall rule keelbox-acme-deny\n")
	if _, ok := loadParkMark("acme"); ok {
		t.Error("the parked note is still there after destroy")
	}

	w = newGCPWorld(t)
	w.cli.AddSnapshot("keelbox-acme-left", "acme")
	code, out = w.do(t, "destroy", options{yes: true, discardWork: true}, nil)
	if code != 0 || w.holds() != "0 VMs, 0 disks, 0 addresses, 0 snapshots, 0 rules" || !strings.Contains(out, "keelbox: deleted snapshot keelbox-acme-left\n") {
		t.Errorf("destroy of a box with a snapshot beside its VM: exit %d, %s\n%s", code, w.holds(), out)
	}
}
