package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/digitalocean"
	"github.com/keelbox/keelbox/internal/provider/digitalocean/dotest"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// doWorld is the box acme in the registry and in a fake DigitalOcean
// account (its Droplet, of size s-2vcpu-4gb, behind its firewall), its
// host key pinned, and a record of what the commands did past doctl.
type doWorld struct {
	cli *dotest.CLI
	cfg *config.Config
	cl  cloud
	d   *dotest.Droplet
	// waited and applied are the addresses boxComesBack and applyExisting
	// were called with; applyCode is what the apply returns.
	waited, applied []string
	applyCode       int
	// terminal is whether destroy finds a person to ask.
	terminal bool
}

func newDOWorld(t *testing.T) *doWorld {
	t.Helper()
	isolate(t)
	w := &doWorld{cli: fakeDO(t)}
	w.d = doBox(w.cli)
	s := config.StarterFor("acme", "digitalocean")
	s.Keys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}
	cfg, err := config.Load(register(t, s))
	if err != nil {
		t.Fatal(err)
	}
	w.cfg = cfg
	if w.cl, err = openCloud(context.Background(), cfg, options{}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := sshTarget(cfg, addr{w.d.IP, 22}, options{}, io.Discard, io.Discard).Pin(provider.ParseHostKeys(boxHostKey)); err != nil {
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
		w.applied = append(w.applied, fmt.Sprintf("%s as %q, snapshots %d", public.host, o.sshUser, len(w.cli.Snapshots)))
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
func (w *doWorld) do(t *testing.T, cmd string, o options, input *string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	p := prompt.New(strings.NewReader(""), &out, false)
	if input != nil {
		p = prompt.New(strings.NewReader(*input), &out, true)
	}
	o.configFile = boxConfigPath("acme")
	logf := func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) }
	ctx := context.Background()
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

// writes are the doctl calls since the mark that changed the account, in
// order, by command.
func (w *doWorld) writes(since int) []string {
	var out []string
	for _, c := range w.cli.Calls()[since:] {
		f := strings.Fields(c)
		switch {
		case len(f) < 3 || f[2] == "list" || f[2] == "get" || f[2] == "list-user":
		default:
			out = append(out, strings.Join(f[1:3], " "))
		}
	}
	return out
}

func (w *doWorld) left() string {
	return fmt.Sprintf("%d droplets, %d snapshots, %d firewalls", len(w.cli.Droplets), len(w.cli.Snapshots), len(w.cli.Firewalls))
}

// the one Droplet and the one snapshot in the account.
func (w *doWorld) droplet(t *testing.T) *dotest.Droplet {
	t.Helper()
	for _, d := range w.cli.Droplets {
		return d
	}
	t.Fatal("no Droplet")
	return nil
}

func (w *doWorld) snapshot(t *testing.T) *dotest.Snapshot {
	t.Helper()
	for _, s := range w.cli.Snapshots {
		return s
	}
	t.Fatal("no snapshot")
	return nil
}

// park shuts down, takes the snapshot, tags it, waits for it and only then
// deletes the Droplet; the firewall stays. resume creates the Droplet from
// the snapshot, moves the pinned host keys, applies as the admin and
// deletes the snapshot only after that apply.
func TestDigitalOceanParkThenResume(t *testing.T) {
	w := newDOWorld(t)
	was, id := w.d.IP, w.d.ID
	blob := pinned(t, was)
	code, out := w.do(t, "park", options{yes: true}, nil)
	if code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	// The parked marker is a tag that has to exist before it is put on the
	// snapshot; the box's own tags exist already.
	want := "droplet-action shutdown, tag create, droplet-action snapshot, tag apply, tag apply, tag apply, droplet delete"
	if got := strings.Join(w.writes(0), ", "); got != want {
		t.Errorf("park's calls: %s\nwant: %s", got, want)
	}
	if w.left() != "0 droplets, 1 snapshots, 1 firewalls" {
		t.Fatalf("after park: %s", w.left())
	}
	snap := w.snapshot(t)
	// The snapshot was read back as available before the delete.
	calls := strings.Join(w.cli.Calls(), "\n")
	if i, j := strings.LastIndex(calls, fmt.Sprintf("compute image get %d", snap.ID)), strings.Index(calls, "compute droplet delete"); i < 0 || j < i {
		t.Errorf("the Droplet was deleted before the snapshot was read back:\n%s", calls)
	}
	if strings.Join(snap.Tags, ",") != "keelbox,keelbox-box-acme,keelbox-parked" || snap.Name != "keelbox-parked-acme" || snap.DropletID != id || snap.Status != "available" {
		t.Errorf("snapshot %+v", snap)
	}
	ref := fmt.Sprintf("snapshot %d", snap.ID)
	wantAll(t, "park", out, "keelbox: shutting acme down\n", fmt.Sprintf("keelbox: Droplet %d is off; saving its disk as a snapshot", id),
		"keelbox: "+ref+" is complete (2.5 GB, about USD 0.15 a month)\n", fmt.Sprintf("keelbox: deleted Droplet acme (id %d)\n", id),
		"\nacme is parked in ")
	wantAll(t, "the end of park", flat(out), parkedWords(ref+", which holds its disk (2.5 GB, about USD 0.15 a month)", ", about USD 24.00 a month", was, "2 to 3 minutes"))
	mark, ok := loadParkMark("acme")
	if !ok || mark.Address != was || mark.Snapshot != ref || mark.Since.IsZero() {
		t.Errorf("mark %+v %v", mark, ok)
	}
	// The firewall and the box's tag are kept for the next Droplet.
	if !w.cli.Tags["keelbox-box-acme"] || w.cli.Firewalls["fw-1"] == nil {
		t.Errorf("tags %v, firewalls %v", w.cli.Tags, w.cli.Firewalls)
	}

	// Every command that needs the server says parked, and so does park.
	n := len(w.cli.Calls())
	if _, _, err := cloudPublic(context.Background(), w.cl, w.cfg, "acme"); err == nil || !strings.Contains(err.Error(), "acme has been parked since "+day(mark.Since)) || !strings.Contains(err.Error(), "keelbox resume acme") {
		t.Errorf("a command on the parked box: %v", err)
	}
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 || len(w.writes(n)) != 0 || !strings.Contains(out, "keelbox: acme is already parked, since "+day(mark.Since)+": "+ref+" holds its disk (2.5 GB, about USD 0.15 a month) and is what is billed. Bring it back: keelbox resume acme\n") {
		t.Errorf("park on a parked box: exit %d, %v\n%s", code, w.writes(n), out)
	}

	n = len(w.cli.Calls())
	code, out = w.do(t, "resume", options{sshUser: "root"}, nil)
	if code != 0 {
		t.Fatalf("resume: exit %d\n%s", code, out)
	}
	// The Droplet is created and named in the firewall at once. The
	// snapshot goes last: deleted, its three tags taken off it so that
	// DigitalOcean stops counting it, and the parked marker deleted.
	if got := strings.Join(w.writes(n), ", "); got != "droplet create, firewall add-droplets, image delete, tag remove, tag remove, tag remove, tag delete" {
		t.Errorf("resume's calls: %s", got)
	}
	if w.left() != "1 droplets, 0 snapshots, 1 firewalls" {
		t.Fatalf("after resume: %s", w.left())
	}
	made := w.droplet(t)
	// Waited for, then applied as the admin while the snapshot was still there.
	if len(w.waited) != 1 || w.waited[0] != made.IP || len(w.applied) != 1 || w.applied[0] != made.IP+` as "", snapshots 1` {
		t.Errorf("waited %v, applied %v, the new address is %s", w.waited, w.applied, made.IP)
	}
	// The host keys this computer trusted are pinned under the new address,
	// and no longer under the one given up.
	if made.IP == was || pinned(t, made.IP) != blob || pinned(t, was) != "" {
		t.Errorf("pins: %s has %q, %s has %q", made.IP, pinned(t, made.IP), was, pinned(t, was))
	}
	if _, ok := loadParkMark("acme"); ok {
		t.Error("the parked note is still there after resume")
	}
	if made.FromSnapshot != snap.ID || made.Size != "s-2vcpu-4gb" || made.Region != "fra1" || strings.Join(made.Tags, ",") != "keelbox,keelbox-box-acme" ||
		!strings.HasPrefix(made.UserData, provider.ResumeUserData("acme")) || strings.Contains(made.UserData, "ssh_authorized_keys") || !strings.Contains(made.UserData, "\nusers: []\n") {
		t.Errorf("created %+v", made)
	}
	if f := w.cli.Firewalls["fw-1"]; len(f.DropletIDs) != 1 || f.DropletIDs[0] != made.ID || strings.Join(f.Tags, ",") != "keelbox-box-acme" {
		t.Errorf("firewall after resume: %+v", f)
	}
	// The parked marker goes with the snapshot; the box's tags stay.
	if w.cli.Tags["keelbox-parked"] || !w.cli.Tags["keelbox"] || !w.cli.Tags["keelbox-box-acme"] {
		t.Errorf("tags after resume: %v", w.cli.Tags)
	}
	wantAll(t, "resume", out, "its server is billed again from the moment it is created: about USD ", " a month when it was parked", fmt.Sprintf("creating acme from %s: s-2vcpu-4gb in fra1", ref), "running at "+made.IP+"; waiting for SSH", "keelbox: deleted "+ref+"\n",
		"\nacme is back in ")
	wantAll(t, "the end of resume", flat(out), resumedWords(made.IP, was, capitalFirst(ref)))

	// Running: said, exit 0, nothing sent.
	n = len(w.cli.Calls())
	if code, out := w.do(t, "resume", options{}, nil); code != 0 || len(w.writes(n)) != 0 || !strings.Contains(out, fmt.Sprintf("acme is not parked: Droplet %d runs at %s. Nothing to resume.", made.ID, made.IP)) {
		t.Errorf("resume on a running box: exit %d, %v\n%s", code, w.writes(n), out)
	}
}

// park says what stays billed, with DigitalOcean's price, before it asks.
func TestDigitalOceanParkAsksFirst(t *testing.T) {
	w := newDOWorld(t)
	code, out := w.do(t, "park", options{}, str("\n"))
	if code != 1 || len(w.writes(0)) != 0 {
		t.Errorf("Enter: exit %d, %v", code, w.writes(0))
	}
	wantAll(t, "the question", flat(out), "Park acme: "+parkCardWords(fmt.Sprintf("Droplet acme (id %d, fra1)", w.d.ID),
		"the server's price, about USD 24.00 a month (USD 0.86 a day)",
		"the snapshot: USD 0.06 per GB a month for what the disk holds, so up to about USD 4.80 a month for its 80 GB (an estimate from DigitalOcean's list prices, before tax)",
		"2 minutes", "2 to 3 minutes", w.d.IP)+" Park acme? [y/N] Nothing changed.")
	if code, _ := w.do(t, "park", options{}, nil); code != 2 || len(w.writes(0)) != 0 {
		t.Errorf("no terminal: exit %d, %v", code, w.writes(0))
	}
}

// Whatever stops the snapshot, the Droplet is not deleted: it is started
// again, and no snapshot is left.
func TestDigitalOceanParkNeverDeletesWithoutAConfirmedSnapshot(t *testing.T) {
	for name, set := range map[string]func(w *doWorld){
		"the snapshot fails":           func(w *doWorld) { w.cli.Errored["snapshot"] = true },
		"the snapshot is refused":      func(w *doWorld) { w.cli.Fail["compute droplet-action snapshot"] = "try again" },
		"a tag cannot be put on it":    func(w *doWorld) { w.cli.Fail["compute tag apply"] = "try again" },
		"it cannot be read back":       func(w *doWorld) { w.cli.Fail["compute image get"] = "try again" },
		"the shutdown is refused":      func(w *doWorld) { w.cli.Fail["compute droplet-action shutdown"] = "try again" },
		"the images cannot be listed":  func(w *doWorld) { w.cli.Fail["compute image list-user"] = "try again" },
		"the action cannot be read":    func(w *doWorld) { w.cli.Fail["compute droplet-action get"] = "try again" },
		"the tags cannot be looked up": func(w *doWorld) { w.cli.Fail["compute tag get"] = "try again" },
	} {
		t.Run(name, func(t *testing.T) {
			w := newDOWorld(t)
			if name == "the images cannot be listed" {
				// park asks for a parked snapshot before the question: it
				// stops there, with the box running.
				set(w)
				if code, out := w.do(t, "park", options{yes: true}, nil); code != 1 || len(w.writes(0)) != 0 {
					t.Fatalf("exit %d, %v\n%s", code, w.writes(0), out)
				}
				return
			}
			w.cli.Hook = func(call string) {
				// From the shutdown on, not before: park's own questions pass.
				if strings.HasPrefix(call, "compute droplet-action shutdown") {
					set(w)
				}
			}
			code, out := w.do(t, "park", options{yes: true}, nil)
			if code != 1 || w.cli.Called("compute droplet delete") {
				t.Fatalf("exit %d, calls %v\n%s", code, w.cli.Calls(), out)
			}
			if name == "the action cannot be read" {
				// Neither the snapshot nor the power-on can be followed: the
				// Droplet is there, switched off, and park says how to start it.
				if w.left() != "1 droplets, 0 snapshots, 1 firewalls" || !strings.Contains(out, "acme is not parked and not deleted, but starting it again failed") || !strings.Contains(out, "Start it: keelbox resume acme") {
					t.Errorf("left %s\n%s", w.left(), out)
				}
				return
			}
			if w.left() != "1 droplets, 0 snapshots, 1 firewalls" || w.d.Status != "" {
				t.Errorf("left %s, Droplet status %q\n%s", w.left(), w.d.Status, out)
			}
			wantAll(t, "park", out, fmt.Sprintf("acme is not parked: Droplet %d was not deleted and runs again at %s. Nothing is lost; try again: keelbox park acme", w.d.ID, w.d.IP))
			if _, ok := loadParkMark("acme"); ok {
				t.Error("a parked note was written")
			}
		})
	}
}

// resize changes the vCPUs and the memory and leaves the disk, so the way
// back is open; the address stays.
func TestDigitalOceanResize(t *testing.T) {
	w := newDOWorld(t)
	before := configText(t)
	code, out := w.do(t, "resize", options{size: "s-4vcpu-8gb", yes: true, sshUser: "root"}, nil)
	if code != 0 {
		t.Fatalf("resize: exit %d\n%s", code, out)
	}
	if got := strings.Join(w.writes(0), ", "); got != "droplet-action shutdown, droplet-action resize, droplet-action power-on" {
		t.Errorf("calls: %s", got)
	}
	for _, c := range w.cli.Calls() {
		if strings.Contains(c, "resize-disk") {
			t.Errorf("the disk was resized: %q", c)
		}
	}
	// s-4vcpu-8gb's own disk is 160 GB; the Droplet's stays 80.
	if w.d.Size != "s-4vcpu-8gb" || w.d.Disk != 80 || w.d.Status != "" {
		t.Errorf("Droplet after resize: %+v", w.d)
	}
	if len(w.waited) != 1 || w.waited[0] != w.d.IP || len(w.applied) != 1 || !strings.HasPrefix(w.applied[0], w.d.IP+` as ""`) {
		t.Errorf("waited %v, applied %v", w.waited, w.applied)
	}
	wantAll(t, "resize", out, "keelbox: shutting acme down\n", fmt.Sprintf("keelbox: Droplet %d is off; changing it from s-2vcpu-4gb to s-4vcpu-8gb\n", w.d.ID),
		"keelbox: set target.digitalocean.size: s-4vcpu-8gb in ~/.config/keelbox/boxes/acme.yaml\n",
		"\nacme is s-4vcpu-8gb now in ")
	wantAll(t, "the end of resize", flat(out), resizedWords("s-4vcpu-8gb: 4 vCPU, 8 GB, 5 TB transfer, USD 48.00/month", "s-2vcpu-4gb: 2 vCPU, 4 GB, 4 TB transfer, USD 24.00/month",
		"costs about USD 24.00 a month more than before ", "its 80 GB disk, its address and everything on it", "s-2vcpu-4gb"))
	after := configText(t)
	if want := strings.Replace(before, "size: s-2vcpu-4gb", "size: s-4vcpu-8gb", 1); after != want || after == before {
		t.Errorf("config after resize:\n%s", after)
	}
	if w.cfg.Target.DigitalOcean.Size != "s-4vcpu-8gb" {
		t.Errorf("the config the apply ships says %s", w.cfg.Target.DigitalOcean.Size)
	}
	// And back down: the 80 GB disk still fits s-2vcpu-4gb.
	if code, out := w.do(t, "resize", options{size: "s-2vcpu-4gb", yes: true}, nil); code != 0 || w.d.Size != "s-2vcpu-4gb" || configText(t) != before {
		t.Errorf("resize down: exit %d\n%s", code, out)
	}
	// A size with a smaller disk, one not sold in the region, or none.
	n := len(w.cli.Calls())
	for _, size := range []string{"s-2vcpu-2gb", "c-2", "s-2vcpu-4gb-old", "nope"} {
		code, out := w.do(t, "resize", options{size: size, yes: true}, nil)
		if code != 2 || len(w.writes(n)) != 0 || !strings.Contains(out, `--size "`+size+`" is not a size acme can change to and keep its disk. It is s-2vcpu-4gb now and can change to s-4vcpu-8gb: keelbox resize acme --size s-4vcpu-8gb`+"\n") {
			t.Errorf("--size %s: exit %d, %v\n%s", size, code, w.writes(n), out)
		}
	}
	// On a terminal: the sizes with prices, where they are from, and that
	// the disk stays.
	code, out = w.do(t, "resize", options{}, str("2\n\n"))
	if code != 1 || len(w.writes(n)) != 0 {
		t.Errorf("Enter at the go-ahead: exit %d, %v", code, w.writes(n))
	}
	wantAll(t, "the questions", out,
		"Prices are estimates from the DigitalOcean API for fra1, in US dollars before tax. Only the vCPUs and the memory change: the disk stays 80 GB at every size, so you can change back from any size listed. 2 vCPU and 4 GB suit one or two agents.",
		"Size for acme (it is s-2vcpu-4gb now):", "  1) s-2vcpu-4gb  2 vCPU, 4 GB, 4 TB transfer, USD 24.00/month; the size now\n",
		"  2) s-4vcpu-8gb  4 vCPU, 8 GB, 5 TB transfer, USD 48.00/month\n", "Resize acme? [y/N] ", "Nothing changed.\n")
	// The disk is said once, where it stays, and not beside each size: a
	// size's own disk is not what the Droplet gets.
	wantAll(t, "the go-ahead", flat(out),
		"Resize acme from s-2vcpu-4gb to s-4vcpu-8gb: from s-2vcpu-4gb: 2 vCPU, 4 GB, 4 TB transfer, USD 24.00/month "+
			"to s-4vcpu-8gb: 4 vCPU, 8 GB, 5 TB transfer, USD 48.00/month "+
			"costs about USD 24.00 a month more than now "+
			"what happens acme restarts: running sessions, agents, dev servers and containers stop. It is back in 2 to 3 minutes, typically "+
			"what stays its 80 GB disk, its address and everything on it "+
			"change back keelbox resize acme --size s-2vcpu-4gb Resize acme? [y/N] Nothing changed.")
	if strings.Contains(out, "160 GB") {
		t.Errorf("the disk of the size itself is shown, which the Droplet does not get:\n%s", out)
	}
}

// A resize DigitalOcean does not carry out leaves the Droplet at its old
// size, running again, and the config as it was.
func TestDigitalOceanResizeFailureLeavesTheOldSize(t *testing.T) {
	for name, set := range map[string]func(w *doWorld){
		"the action fails":        func(w *doWorld) { w.cli.Errored["resize"] = true },
		"the resize is refused":   func(w *doWorld) { w.cli.Fail["compute droplet-action resize"] = "no capacity for this size" },
		"the shutdown is refused": func(w *doWorld) { w.cli.Fail["compute droplet-action shutdown"] = "try again" },
	} {
		t.Run(name, func(t *testing.T) {
			w := newDOWorld(t)
			before := configText(t)
			set(w)
			code, out := w.do(t, "resize", options{size: "s-4vcpu-8gb", yes: true}, nil)
			if code != 1 || w.d.Size != "s-2vcpu-4gb" || w.d.Disk != 80 || w.d.Status != "" || configText(t) != before || len(w.applied) != 0 {
				t.Fatalf("exit %d, Droplet %+v, applied %v\n%s", code, w.d, w.applied, out)
			}
			wantAll(t, "resize", out, fmt.Sprintf("keelbox: acme is still s-2vcpu-4gb and runs again at %s. Try again: keelbox resize acme\n", w.d.IP))
		})
	}
}

// resume on a box that is not parked whose Droplet is off starts it: the
// way back after a park or a resize that could not.
func TestDigitalOceanResumeStartsADropletThatIsOff(t *testing.T) {
	w := newDOWorld(t)
	w.d.Status = "off"
	srv, err := w.cl.find(context.Background())
	if err != nil || !srv.stopped || srv.ip != w.d.IP {
		t.Fatalf("find: %+v %v", srv, err)
	}
	code, out := w.do(t, "resume", options{}, nil)
	if code != 0 || w.d.Status != "" || strings.Join(w.writes(0), ", ") != "droplet-action power-on" {
		t.Errorf("exit %d, status %q, %v\n%s", code, w.d.Status, w.writes(0), out)
	}
	wantAll(t, "resume", out, fmt.Sprintf("acme is not parked, but Droplet %d is off; starting it", w.d.ID), fmt.Sprintf("Droplet %d runs again at %s", w.d.ID, w.d.IP))
}

// Only a Droplet and a snapshot with keelbox's tags for the box are ever
// acted on.
func TestDigitalOceanParkTouchesOnlyTaggedResources(t *testing.T) {
	w := newDOWorld(t)
	ctx := context.Background()
	// A Droplet of the box's name that keelbox did not create, and a
	// snapshot named like a parked one without the tags.
	w.d.Tags = []string{"owner:sam"}
	theirs := w.cli.AddSnapshot("keelbox-parked-acme")
	half := w.cli.AddSnapshot("keelbox-parked-acme", "keelbox", "keelbox-box-acme")
	for _, cmd := range []string{"park", "resize", "resume"} {
		code, out := w.do(t, cmd, options{yes: true, size: "s-4vcpu-8gb"}, nil)
		if code != 1 || len(w.writes(0)) != 0 || !strings.Contains(out, "was not created by keelbox") {
			t.Errorf("%s: exit %d, %v\n%s", cmd, code, w.writes(0), out)
		}
	}
	if p, err := w.cl.parked(ctx); p != nil || err != nil {
		t.Errorf("parked: %+v %v", p, err)
	}
	// The calls themselves refuse, whatever they are handed.
	srv := doServer(&digitalocean.Droplet{ID: w.d.ID, Name: "acme", Tags: []string{"owner:sam"}})
	if err := w.cl.stop(ctx, srv, t.Logf); err == nil {
		t.Error("stop of an untagged Droplet")
	}
	if _, err := w.cl.start(ctx, srv); err == nil {
		t.Error("start of an untagged Droplet")
	}
	if _, err := w.cl.resize(ctx, srv, "s-4vcpu-8gb", t.Logf); err == nil {
		t.Error("resize of an untagged Droplet")
	}
	if _, err := w.cl.snapshot(ctx, srv, t.Logf); err == nil {
		t.Error("snapshot of an untagged Droplet")
	}
	if err := w.cl.release(ctx, srv, t.Logf); err == nil {
		t.Error("release of an untagged Droplet")
	}
	for _, s := range []*dotest.Snapshot{theirs, half} {
		p := &cloudParked{ref: "snapshot", raw: &digitalocean.Image{ID: s.ID, Name: s.Name, Type: "snapshot", Tags: s.Tags, Status: "available", Regions: []string{"fra1"}, MinDiskSize: 80}}
		if err := w.cl.discard(ctx, p, t.Logf); err == nil || !strings.Contains(err.Error(), "refusing to delete image") {
			t.Errorf("discard of snapshot %d: %v", s.ID, err)
		}
		if _, err := w.cl.restore(ctx, p, t.Logf); err == nil || !strings.Contains(err.Error(), "not its parked snapshot") {
			t.Errorf("restore from snapshot %d: %v", s.ID, err)
		}
	}
	if len(w.writes(0)) != 0 || w.left() != "1 droplets, 2 snapshots, 1 firewalls" {
		t.Errorf("something changed: %v, left %s", w.writes(0), w.left())
	}
}

// destroy on a parked box deletes the snapshot, any other parked snapshot
// of the box, the firewall and keelbox's tags; nothing of another box is
// touched.
func TestDigitalOceanDestroyParkedBox(t *testing.T) {
	setup := func() (*doWorld, string) {
		w := newDOWorld(t)
		w.do(t, "park", options{yes: true}, nil)
		ref := fmt.Sprintf("snapshot %d", w.snapshot(t).ID)
		// What an interrupted park left, and what is not this box's.
		unfinished := w.cli.AddSnapshot("keelbox-parked-acme", "keelbox", "keelbox-box-acme", "keelbox-parked")
		unfinished.Status = "pending"
		w.cli.AddSnapshot("keelbox-parked-web", "keelbox", "keelbox-box-web", "keelbox-parked")
		w.cli.AddSnapshot("keelbox-parked-acme")
		w.cli.Firewalls["fw-2"] = &dotest.Firewall{ID: "fw-2", Name: "keelbox-web", Tags: []string{"keelbox-box-web"}, Inbound: []string{"udp:41641:0.0.0.0/0,::/0"}}
		w.cli.Tags["keelbox-box-web"] = true
		return w, ref
	}
	const others = "0 droplets, 2 snapshots, 1 firewalls"
	w, ref := setup()
	n := len(w.cli.Calls())
	// No terminal: refused, --yes or not.
	for _, o := range []options{{}, {yes: true}} {
		code, out := w.do(t, "destroy", o, nil)
		if code != 1 || len(w.writes(n)) != 0 {
			t.Errorf("%+v without a terminal: exit %d, %v", o, code, w.writes(n))
		}
		wantAll(t, "refusal", out, "keelbox: not deleted: acme has been parked since ", ref+" (2.5 GB, about USD 0.15 a month) holds everything that was on the box",
			"keelbox destroy acme --yes --discard-work\n")
	}
	w.terminal = true
	code, out := w.do(t, "destroy", options{}, str("y\nacme\n"))
	if code != 0 || w.left() != others {
		t.Fatalf("destroy: exit %d, left %s\n%s", code, w.left(), out)
	}
	wantAll(t, "destroy", out, "keelbox: deleted "+ref+"\n", "keelbox: deleted snapshot ", "keelbox: deleted firewall keelbox-acme (id fw-1)\n")
	if strings.Count(out, "keelbox: deleted snapshot ") != 2 {
		t.Errorf("the unfinished snapshot was not swept:\n%s", out)
	}
	// keelbox's tags for the box are gone; the ones another box uses stay.
	if w.cli.Tags["keelbox-box-acme"] || !w.cli.Tags["keelbox-box-web"] || !w.cli.Tags["keelbox"] || !w.cli.Tags["keelbox-parked"] {
		t.Errorf("tags %v", w.cli.Tags)
	}
	if _, ok := loadParkMark("acme"); ok || pinned(t, w.d.IP) != "" {
		t.Error("the parked note or the pinned host key is still there")
	}

	// A Droplet with a snapshot beside it, as an unfinished resume leaves:
	// both go, with the firewall and the tags.
	w, _ = setup()
	w.applyCode = 1
	if code, _ := w.do(t, "resume", options{}, nil); code != 1 || len(w.cli.Droplets) != 1 {
		t.Fatalf("resume with a failing apply: exit %d, %s", code, w.left())
	}
	// The snapshot an interrupted park left is not this destroy's to sweep
	// while the Droplet is there; it goes once the Droplet is gone.
	if code, out := w.do(t, "destroy", options{yes: true}, nil); code != 0 || len(w.cli.Droplets) != 0 || w.cli.Firewalls["fw-1"] != nil {
		t.Errorf("Droplet and snapshot: exit %d, left %s\n%s", code, w.left(), out)
	}
	for _, s := range w.cli.Snapshots {
		if s.Status == "available" && strings.Join(s.Tags, ",") == "keelbox,keelbox-box-acme,keelbox-parked" {
			t.Errorf("the parked snapshot is left: %+v", s)
		}
	}
}

// A second computer, with the config and no note of its own, finds the
// parked box by the snapshot's tags.
func TestDigitalOceanResumeFromAnotherComputer(t *testing.T) {
	w := newDOWorld(t)
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	forgetParkMark("acme")
	if err := os.Remove(provider.DefaultKnownHosts()); err != nil {
		t.Fatal(err)
	}
	p, err := w.cl.parked(context.Background())
	if err != nil || p == nil || p.since.IsZero() || p.detail != "2.5 GB, about USD 0.15 a month" {
		t.Fatalf("parked: %+v %v", p, err)
	}
	if code, out := w.do(t, "resume", options{}, nil); code != 0 || w.left() != "1 droplets, 0 snapshots, 1 firewalls" {
		t.Errorf("resume: exit %d, left %s\n%s", code, w.left(), out)
	}
}
