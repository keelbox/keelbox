package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/hetzner/hetznertest"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/ui"
)

const boxHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBoxHostKeyBoxHostKeyBoxHostKeyBoxHostKeyBox0"

// parkWorld is the box acme in the registry and in a fake Hetzner project
// (server 42 at 203.0.113.7 with its firewall), its host key pinned, and a
// record of what the commands did past the cloud's API.
type parkWorld struct {
	api *hetznertest.API
	cfg *config.Config
	cl  *hetznerCloud
	// waited and applied are the addresses boxComesBack and applyExisting
	// were called with; applyCode and waitErr are what they return.
	waited, applied []string
	applyCode       int
	waitErr         error
	// tailnetOnly is whether the box has closed public SSH.
	tailnetOnly bool
	// terminal is whether destroy finds a person to ask.
	terminal bool
}

func newParkWorld(t *testing.T) *parkWorld {
	t.Helper()
	isolate(t)
	api, store := fakeHetzner(t)
	_ = store.Set(context.Background(), "hetzner", goodToken)
	api.Servers = []map[string]any{hetznertest.Server(42, "acme", "203.0.113.7")}
	api.Firewalls = []map[string]any{hetznertest.Firewall(7, "acme", 42)}
	api.Keys = []hetznertest.Key{{ID: 7, Name: "k"}}
	s := config.StarterFor("acme", "hetzner")
	s.ServerType, s.Location, s.Image, s.HetznerKeys = "cx23", "fsn1", "ubuntu-24.04", []string{"k"}
	s.Keys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}
	cfg, err := config.Load(register(t, s))
	if err != nil {
		t.Fatal(err)
	}
	w := &parkWorld{api: api, cfg: cfg}
	w.cl = &hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}
	if err := sshTarget(cfg, addr{"203.0.113.7", 22}, options{}, io.Discard, io.Discard).Pin(provider.ParseHostKeys(boxHostKey)); err != nil {
		t.Fatal(err)
	}
	oldWait, oldApply, oldTerm, oldProbe, oldClosed := boxComesBack, applyExisting, stdinIsTerminal, boxProjects, publicSSHClosed
	t.Cleanup(func() {
		boxComesBack, applyExisting, stdinIsTerminal, boxProjects, publicSSHClosed = oldWait, oldApply, oldTerm, oldProbe, oldClosed
	})
	publicSSHClosed = func(context.Context, *config.Config, *cloudServer) bool { return w.tailnetOnly }
	boxComesBack = func(_ context.Context, _ *config.Config, public addr, _, _ io.Writer) error {
		w.waited = append(w.waited, public.host)
		return w.waitErr
	}
	applyExisting = func(_ context.Context, _ cloud, _ *config.Config, srv *cloudServer, public addr, o options, _ func(string, ...any), _, _ io.Writer) int {
		w.applied = append(w.applied, fmt.Sprintf("%s as %q, snapshots %d", public.host, o.sshUser, len(api.Images)))
		return w.applyCode
	}
	stdinIsTerminal = func() bool { return w.terminal }
	boxProjects = func(context.Context, *config.Config, string, options) (*snapshot.Projects, error) {
		return cleanBox, nil
	}
	return w
}

// do runs one of the three commands as Main would, with input as the
// terminal's answers (nil: no terminal).
func (w *parkWorld) do(t *testing.T, cmd string, o options, input *string) (int, string) {
	t.Helper()
	return w.doWith(t, w.cl, cmd, o, input)
}

func (w *parkWorld) doWith(t *testing.T, cl cloud, cmd string, o options, input *string) (int, string) {
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
		code = cloudPark(ctx, cl, w.cfg, o, p, logf, &out, &out)
	case "resume":
		code = cloudResume(ctx, cl, w.cfg, o, logf, &out, &out)
	case "resize":
		code = cloudResize(ctx, cl, w.cfg, o, p, logf, &out, &out)
	case "destroy":
		in := ""
		if input != nil {
			in = *input
		}
		code = cloudDestroy(ctx, cl, w.cfg, o, strings.NewReader(in), logf, &out, &out)
	default:
		t.Fatalf("no command %s", cmd)
	}
	if strings.Contains(out.String(), goodToken) {
		t.Errorf("%s printed the API token:\n%s", cmd, out.String())
	}
	return code, out.String()
}

// writes are the calls that changed the project, in order, without the
// polls between them.
func (w *parkWorld) writes() []string {
	var out []string
	for _, c := range w.api.Calls() {
		if strings.HasPrefix(c, "POST ") || strings.HasPrefix(c, "DELETE ") {
			out = append(out, c)
		}
	}
	return out
}

func (w *parkWorld) forgetCalls() { w.api.Requests = nil }

// pinned is the key blob known_hosts pins for a host, or "".
func pinned(t *testing.T, host string) string {
	t.Helper()
	out, err := exec.Command("ssh-keygen", "-F", host, "-f", provider.DefaultKnownHosts()).Output()
	if err != nil {
		return ""
	}
	keys := provider.ParseHostKeys(string(out))
	if len(keys) == 0 {
		return ""
	}
	return keys[0].Blob
}

func str(s string) *string { return &s }

func wantAll(t *testing.T, what, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, out)
		}
	}
}

// park shuts down, takes the snapshot, waits for it and only then deletes
// the server; the firewall stays; resume creates the server from the
// snapshot, moves the pinned host keys, applies as the admin and deletes
// the snapshot only after that apply.
func TestParkThenResume(t *testing.T) {
	w := newParkWorld(t)
	blob := pinned(t, "203.0.113.7")
	code, out := w.do(t, "park", options{yes: true}, nil)
	if code != 0 {
		t.Fatalf("park: exit %d\n%s", code, out)
	}
	got := strings.Join(w.writes(), "\n")
	want := "POST /servers/42/actions/shutdown\nPOST /servers/42/actions/create_image\nDELETE /servers/42"
	if got != want {
		t.Errorf("park's calls:\n%s\nwant:\n%s", got, want)
	}
	// The snapshot was read back as available before the delete.
	calls := strings.Join(w.api.Calls(), "\n")
	if i, j := strings.Index(calls, "GET /images/1001"), strings.Index(calls, "DELETE /servers/42"); i < 0 || j < i {
		t.Errorf("the server was deleted before the snapshot was read back:\n%s", calls)
	}
	if len(w.api.Servers) != 0 || len(w.api.Images) != 1 || len(w.api.Firewalls) != 1 {
		t.Fatalf("after park: %d servers, %d snapshots, %d firewalls", len(w.api.Servers), len(w.api.Images), len(w.api.Firewalls))
	}
	labels := w.api.Images[0]["labels"].(map[string]any)
	if labels["keelbox"] != "true" || labels["keelbox-box"] != "acme" || labels["keelbox-parked"] != "true" {
		t.Errorf("snapshot labels %v", labels)
	}
	wantAll(t, "park", out, "keelbox: shutting acme down\n", "keelbox: snapshot 1001 is complete (2.5 GB, about EUR 0.04 a month)\n",
		"keelbox: deleted acme (id 42)\n", "\nacme is parked in ")
	wantAll(t, "the end of park", flat(out), parkedWords("snapshot 1001, which holds its disk (2.5 GB, about EUR 0.04 a month)", ", about EUR 3.79 a month", "203.0.113.7", "1 to 3 minutes"))
	if !strings.HasSuffix(flat(out), "as they were") || strings.Contains(out, "is parked in 0s:") {
		t.Errorf("park does not end on what holds while the box is parked:\n%s", out)
	}
	mark, ok := loadParkMark("acme")
	if !ok || mark.Address != "203.0.113.7" || mark.Snapshot != "snapshot 1001" || time.Since(mark.Since) > time.Minute {
		t.Errorf("mark %+v %v", mark, ok)
	}

	// Parked again: said, exit 0, nothing sent.
	w.forgetCalls()
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 || len(w.writes()) != 0 || !strings.Contains(out, "keelbox: acme is already parked, since "+day(mark.Since)+": snapshot 1001 holds its disk (2.5 GB, about EUR 0.04 a month) and is what is billed. Bring it back: keelbox resume acme\n") {
		t.Errorf("park on a parked box: exit %d, %v\n%s", code, w.writes(), out)
	}

	w.forgetCalls()
	code, out = w.do(t, "resume", options{sshUser: "root"}, nil)
	if code != 0 {
		t.Fatalf("resume: exit %d\n%s", code, out)
	}
	if got, want := strings.Join(w.writes(), "\n"), "POST /servers\nDELETE /images/1001"; got != want {
		t.Errorf("resume's calls:\n%s\nwant:\n%s", got, want)
	}
	if len(w.api.Servers) != 1 || len(w.api.Images) != 0 || len(w.api.Firewalls) != 1 {
		t.Fatalf("after resume: %d servers, %d snapshots, %d firewalls", len(w.api.Servers), len(w.api.Images), len(w.api.Firewalls))
	}
	ip := w.api.Servers[0]["public_net"].(map[string]any)["ipv4"].(map[string]any)["ip"].(string)
	// Waited for, then applied as the admin while the snapshot was still there.
	if len(w.waited) != 1 || w.waited[0] != ip || len(w.applied) != 1 || w.applied[0] != ip+` as "", snapshots 1` {
		t.Errorf("waited %v, applied %v, the new address is %s", w.waited, w.applied, ip)
	}
	// The host keys this computer trusted are pinned under the new address,
	// and no longer under the one given up.
	if pinned(t, ip) != blob || pinned(t, "203.0.113.7") != "" {
		t.Errorf("pins: %s has %q, 203.0.113.7 has %q", ip, pinned(t, ip), pinned(t, "203.0.113.7"))
	}
	if _, ok := loadParkMark("acme"); ok {
		t.Error("the parked note is still there after resume")
	}
	req := w.api.Created[0]
	if req["image"] != float64(1001) || req["user_data"] != provider.ResumeUserData("acme") || fmt.Sprint(req["firewalls"]) != "[map[firewall:7]]" {
		t.Errorf("create request %v", req)
	}
	wantAll(t, "resume", out, "creating acme from snapshot 1001: cx23 in fsn1", "running at "+ip+"; waiting for SSH", "keelbox: deleted snapshot 1001\n",
		"\nacme is back in ")
	wantAll(t, "the end of resume", flat(out), resumedWords(ip, "203.0.113.7", "Snapshot 1001"))
	if !strings.HasSuffix(flat(out), "check it keelbox doctor acme") {
		t.Errorf("resume does not end on how to start working again:\n%s", out)
	}

	// Running: said, exit 0, nothing sent.
	w.forgetCalls()
	if code, out := w.do(t, "resume", options{}, nil); code != 0 || len(w.writes()) != 0 || !strings.Contains(out, "acme is not parked: server 1002 runs at "+ip+". Nothing to resume.") {
		t.Errorf("resume on a running box: exit %d, %v\n%s", code, w.writes(), out)
	}
}

// parkCardWords is the card before park's question as one line of words
// (flat), from its first row to its last: what is deleted, what that
// saves, what stays billed, how long a park and a resume typically take on
// the cloud, and the address given up.
func parkCardWords(deleted, saves, billed, park, resume, ip string) string {
	return "what happens acme shuts down: running sessions, agents, dev servers and containers stop. " +
		"Its disk is saved as a snapshot and, once the cloud reports the snapshot complete, " + deleted + " is deleted " +
		"saves " + saves + " still billed " + billed + " " +
		"takes typically " + park + " to park and " + resume + " to resume, for a box with little on its disk; a fuller disk takes longer " +
		"comes back at a new address (" + ip + " is given up), with its files and sign-ins as they were. Sessions and dev servers have to be started again, as after a reboot " +
		snapshotHolds + " bring it back keelbox resume acme"
}

// parkedWords is the end of a park as one line of words (flat), after its
// headline: what stays billed, what does not, and the way back with how
// long it typically takes on the cloud.
func parkedWords(billed, server, ip, resume string) string {
	return "While acme is parked: still billed " + billed + " not billed the server" + server + ". Its address " + ip + " is given up " +
		"bring it back keelbox resume acme takes " + resume + ", typically. It comes back at a new address, with its files and sign-ins as they were"
}

// resumedWords is the end of a resume as one line of words, after its
// headline: how to pick the work up, the address with the one it replaces,
// the tailnet, and what is billed again.
func resumedWords(ip, was, snapshot string) string {
	return "Start working again: start again keelbox attach acme opens the agent's terminal. Sessions and dev servers were stopped when it was parked, as after a reboot; " +
		"containers with a restart policy came back by themselves " +
		"address " + ip + " (it was " + was + ") anything that names the old one, such as a DNS record or another tool's SSH config, needs the new one " +
		"tailnet it rejoins under its name by itself, unless its Tailscale key expired while it was parked; then: keelbox login acme tailscale " +
		"billed again the server. " + snapshot + " is deleted, and no longer billed check it keelbox doctor acme"
}

// resizedWords is the end of a resize as one line of words, after its
// headline: the size and the one before, what the change costs where the
// cloud gives prices (costs, with its row's name, or ""), what was kept,
// how to pick the work up and, where there is one, the way back (back, the
// size before, or "").
func resizedWords(size, was, costs, kept, back string) string {
	s := "After the resize: size " + size + " it was " + was + " " + costs + "kept " + kept + " " +
		"start again keelbox attach acme opens the agent's terminal. Sessions and dev servers were stopped by the restart, as after a reboot; " +
		"containers with a restart policy came back by themselves"
	if back != "" {
		s += " change back keelbox resize acme --size " + back
	}
	return s
}

// snapshotHolds is what park says about the snapshot before the go-ahead,
// in the card's row "the snapshot" and with --yes as a note: it is the
// whole disk, readable from the cloud account, and parking revokes nothing.
const snapshotHolds = "the snapshot holds everything on the disk, credentials and the agent's sign-ins included: whoever can read snapshots in this cloud account can read them, " +
	"and parking revokes nothing"

// park says what happens, what the snapshot holds and what stays billed,
// and asks, No by default; without a terminal it stops before any change.
func TestParkAsksFirst(t *testing.T) {
	w := newParkWorld(t)
	code, out := w.do(t, "park", options{}, str("\n"))
	if code != 1 || len(w.writes()) != 0 {
		t.Errorf("Enter at the question: exit %d, %v", code, w.writes())
	}
	wantAll(t, "the question", flat(out), "Park acme: "+parkCardWords("server acme (id 42)",
		"the server's price, about EUR 3.79 a month (EUR 0.14 a day)",
		"the snapshot: EUR 0.0143 per GB a month for what the disk holds, so up to about EUR 0.57 a month for its 40 GB (an estimate from Hetzner's prices, with VAT)",
		"1 to 2 minutes", "1 to 3 minutes", "203.0.113.7")+" Park acme? [y/N] Nothing changed.")
	// How long to park for is the reader's to decide: the card gives the
	// figures and names no length of time.
	for _, word := range []string{"weeks", "overnight", "night", "long pause", "use it for"} {
		if strings.Contains(out, word) {
			t.Errorf("the card says how long to park for (%q):\n%s", word, out)
		}
	}
	if strings.Contains(out, "Tailscale key") || strings.Contains(out, "check first") {
		t.Errorf("a box with public SSH open is warned about its Tailscale key:\n%s", out)
	}
	// Where the width is not known the card is plain lines, none past 80
	// columns.
	for _, l := range strings.Split(out, "\n") {
		if ui.Width(l) > 80 {
			t.Errorf("a line of the plain card is %d columns: %q", ui.Width(l), l)
		}
	}
	if code, out := w.do(t, "park", options{}, nil); code != 2 || len(w.writes()) != 0 || !strings.Contains(out, "keelbox: park asks first; without a terminal: keelbox park acme --yes") {
		t.Errorf("no terminal: exit %d, %v\n%s", code, w.writes(), out)
	}
	// A box reached over the tailnet only: the question says what an
	// expired Tailscale key would mean, and --yes says it as a note.
	w.tailnetOnly = true
	const warn = "public SSH is closed on acme, so after a resume it answers over the tailnet only. If its Tailscale key expires while it is parked it cannot rejoin by itself, " +
		"and the way in is then the cloud's rescue console (https://keelbox.dev/docs/guides/public-ssh). Check the key's expiry in the Tailscale admin console first"
	if _, out := w.do(t, "park", options{}, str("\n")); !strings.Contains(flat(out), "parking revokes nothing check first "+warn+" bring it back keelbox resume acme") {
		t.Errorf("tailnet only, asked:\n%s", out)
	}
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 || len(w.api.Servers) != 0 || len(w.api.Images) != 1 || !strings.Contains(out, "keelbox: note: "+warn+"\n") {
		t.Errorf("tailnet only, --yes: exit %d\n%s", code, out)
	} else if i, j := strings.Index(out, "keelbox: note: "+snapshotHolds+" (https://keelbox.dev/docs/guides/resize-park-resume)\n"), strings.Index(out, "keelbox: shutting acme down"); i < 0 || j < i {
		t.Errorf("--yes does not say what the snapshot holds before anything changes:\n%s", out)
	}
	w = newParkWorld(t)
	if code, out := w.do(t, "park", options{}, str("y\n")); code != 0 || len(w.api.Servers) != 0 || len(w.api.Images) != 1 {
		t.Errorf("yes: exit %d\n%s", code, out)
	}
}

// Whatever fails before the delete, the server is not deleted: it is
// started again, and park says so.
func TestParkNeverDeletesWithoutAConfirmedSnapshot(t *testing.T) {
	for name, breakIt := range map[string]func(*parkWorld){
		"the shutdown fails":          func(w *parkWorld) { w.api.Fail = map[string]bool{"shutdown": true} },
		"the snapshot call fails":     func(w *parkWorld) { w.api.Fail = map[string]bool{"create_image": true} },
		"the snapshot's action fails": func(w *parkWorld) { w.api.SnapshotFails = true },
	} {
		w := newParkWorld(t)
		breakIt(w)
		code, out := w.do(t, "park", options{yes: true}, nil)
		if code != 1 || len(w.api.Servers) != 1 || w.api.Servers[0]["status"] != "running" || len(w.api.Images) != 0 {
			t.Errorf("%s: exit %d, %d servers (%v), %d snapshots\n%s", name, code, len(w.api.Servers), w.api.Servers[0]["status"], len(w.api.Images), out)
		}
		for _, c := range w.writes() {
			if c == "DELETE /servers/42" {
				t.Errorf("%s: the server was deleted", name)
			}
		}
		if _, ok := loadParkMark("acme"); ok {
			t.Errorf("%s: noted as parked", name)
		}
		wantAll(t, name, out, "keelbox: acme is not parked: server 42 was not deleted and runs again at 203.0.113.7. Nothing is lost; try again: keelbox park acme\n")
	}

	// A cloud that returns no snapshot without an error is not believed.
	w := newParkWorld(t)
	code, out := w.doWith(t, &noSnapshotCloud{w.cl}, "park", options{yes: true}, nil)
	if code != 1 || len(w.api.Servers) != 1 || w.api.Servers[0]["status"] != "running" || !strings.Contains(out, "the snapshot was not confirmed") {
		t.Errorf("no snapshot returned: exit %d\n%s", code, out)
	}

	// The power cannot be put back either: said, with the command that starts it.
	w = newParkWorld(t)
	w.api.Fail = map[string]bool{"create_image": true, "poweron": true}
	code, out = w.do(t, "park", options{yes: true}, nil)
	if code != 1 || len(w.api.Servers) != 1 || !strings.Contains(out, "acme is not parked and not deleted, but starting it again failed") || !strings.Contains(out, "Start it: keelbox resume acme") {
		t.Errorf("start fails too: exit %d\n%s", code, out)
	}
	// And resume does start a box that is off and not parked.
	w.api.Fail = nil
	w.forgetCalls()
	code, out = w.do(t, "resume", options{}, nil)
	if code != 0 || w.api.Servers[0]["status"] != "running" || strings.Join(w.writes(), " ") != "POST /servers/42/actions/poweron" || len(w.applied) != 0 ||
		!strings.Contains(out, "keelbox: acme is not parked, but server 42 is off; starting it\n") || !strings.Contains(out, "keelbox: server 42 runs again at 203.0.113.7. Check it: keelbox doctor acme\n") {
		t.Errorf("resume of a box that is off: exit %d, %v\n%s", code, w.writes(), out)
	}
}

type noSnapshotCloud struct{ *hetznerCloud }

func (c *noSnapshotCloud) snapshot(context.Context, *cloudServer, func(string, ...any)) (*cloudParked, error) {
	return nil, nil
}

// A delete that fails after the snapshot leaves both; park again finishes
// with one snapshot, and resume instead starts the server and removes it.
func TestParkAndResumeFinishWhatWasLeftHalfDone(t *testing.T) {
	w := newParkWorld(t)
	w.api.Fail = map[string]bool{"delete_server": true}
	code, out := w.do(t, "park", options{yes: true}, nil)
	if code != 1 || len(w.api.Servers) != 1 || len(w.api.Images) != 1 {
		t.Fatalf("delete fails: exit %d, %d servers, %d snapshots\n%s", code, len(w.api.Servers), len(w.api.Images), out)
	}
	wantAll(t, "delete fails", out, "snapshot 1001 is complete, but deleting the server failed", "server 42 is off, not deleted, and still billed. Delete it and finish: keelbox park acme; or start it again: keelbox resume acme")
	if _, ok := loadParkMark("acme"); ok {
		t.Error("noted as parked with its server still there")
	}

	// resume: the server is the box; no second server, the snapshot goes
	// after the apply.
	w.api.Fail = nil
	w.forgetCalls()
	code, out = w.do(t, "resume", options{}, nil)
	if code != 0 || len(w.api.Servers) != 1 || w.api.Servers[0]["status"] != "running" || len(w.api.Images) != 0 || len(w.api.Created) != 0 {
		t.Fatalf("resume of a half-parked box: exit %d\n%s", code, out)
	}
	if got := strings.Join(w.writes(), "\n"); got != "POST /servers/42/actions/poweron\nDELETE /images/1001" {
		t.Errorf("calls:\n%s", got)
	}

	// park again after a failed delete: the older snapshot is replaced.
	w = newParkWorld(t)
	w.api.Fail = map[string]bool{"delete_server": true}
	w.do(t, "park", options{yes: true}, nil)
	w.api.Fail = nil
	code, out = w.do(t, "park", options{yes: true}, nil)
	if code != 0 || len(w.api.Servers) != 0 || len(w.api.Images) != 1 || fmt.Sprint(w.api.Images[0]["id"]) != "1002" {
		t.Errorf("park again: exit %d, %d servers, snapshots %v\n%s", code, len(w.api.Servers), w.api.Images, out)
	}
}

// An apply that fails, or a box that does not answer, keeps the snapshot;
// resume again carries on with the server it made.
func TestResumeKeepsTheSnapshotUntilAnApplyPassed(t *testing.T) {
	w := newParkWorld(t)
	w.do(t, "park", options{yes: true}, nil)
	w.applyCode = 1
	code, out := w.do(t, "resume", options{}, nil)
	if code != 1 || len(w.api.Images) != 1 || len(w.api.Servers) != 1 {
		t.Fatalf("apply fails: exit %d, %d snapshots, %d servers\n%s", code, len(w.api.Images), len(w.api.Servers), out)
	}
	wantAll(t, "apply fails", out, "keelbox: the apply did not pass, so snapshot 1001 is kept until an apply has passed. Fix the cause above, then: keelbox resume acme\n")
	w.applyCode, w.waitErr = 0, errors.New("the server runs, but nothing answers SSH")
	code, out = w.do(t, "resume", options{}, nil)
	if code != 1 || len(w.api.Images) != 1 || len(w.applied) != 1 || !strings.Contains(out, "snapshot 1001 is kept until an apply has passed") {
		t.Fatalf("no answer: exit %d\n%s", code, out)
	}
	// A refused host key after the wait ends with the fix in the config.
	w.waitErr = &provider.HostKeyError{Host: "203.0.113.9", Want: "SHA256:" + strings.Repeat("A", 43)}
	code, out = w.do(t, "resume", options{}, nil)
	if code != 1 || !strings.Contains(out, "under target.ssh, set host_key:") || !strings.Contains(out, "keelbox status acme") {
		t.Fatalf("host key refused: exit %d\n%s", code, out)
	}
	w.waitErr = nil
	code, out = w.do(t, "resume", options{}, nil)
	if code != 0 || len(w.api.Images) != 0 || len(w.api.Servers) != 1 || len(w.api.Created) != 1 {
		t.Fatalf("resume again: exit %d, %d snapshots, %d servers, %d created\n%s", code, len(w.api.Images), len(w.api.Servers), len(w.api.Created), out)
	}
	// The create call failing leaves the box parked.
	w = newParkWorld(t)
	w.do(t, "park", options{yes: true}, nil)
	w.api.Fail = map[string]bool{"create_server": true}
	code, out = w.do(t, "resume", options{}, nil)
	if code != 1 || len(w.api.Images) != 1 || len(w.applied) != 0 || !strings.Contains(out, "acme is still parked: snapshot 1001 is kept. Try again: keelbox resume acme") {
		t.Errorf("create fails: exit %d\n%s", code, out)
	}
}

// Every command that connects stops on a parked box with the one line and
// the fix; up and create never make a second server. (login reaches the
// box the way ssh and attach do, once it has a terminal.)
func TestCommandsOnAParkedBoxNameResume(t *testing.T) {
	w := newParkWorld(t)
	w.do(t, "park", options{yes: true}, nil)
	since := day(time.Now())
	line := "keelbox: acme has been parked since " + since + ": its server is deleted and its disk is kept as a snapshot. Bring it back: keelbox resume acme\n"
	for _, cmd := range [][]string{
		{"apply", "acme"}, {"plan", "acme"}, {"doctor", "acme"}, {"status", "acme"}, {"sessions", "acme"}, {"ssh", "acme"}, {"attach", "acme"},
		{"backup", "acme"}, {"rollback", "acme"}, {"reports", "acme"}, {"notify", "test", "acme"},
		{"create", "acme"}, {"resize", "acme", "--size", "cpx22", "--yes"},
	} {
		// From the cloud alone, as another computer would see it, and with
		// this computer's note.
		for _, noted := range []bool{true, false} {
			if !noted {
				forgetParkMark("acme")
			}
			code, out, errOut := run(cmd...)
			if code != 1 || errOut != line || out != "" || strings.Contains(out+errOut, goodToken) {
				t.Errorf("keelbox %s (noted here: %v): exit %d\n%s%s", strings.Join(cmd, " "), noted, code, out, errOut)
			}
		}
	}
	// keelbox up: the note is enough, and nothing is asked of the cloud.
	_ = saveUpState("acme", upState{Via: viaHetzner, Stage: stageReady, Address: "203.0.113.7"})
	w.forgetCalls()
	code, out := upWith(t, &fakeRunner{}, nil, "up", "acme")
	if code != 1 || !strings.Contains(out, "acme has been parked since "+since) || !strings.Contains(out, "keelbox resume acme") || len(w.api.Calls()) != 0 {
		t.Errorf("up on a parked box: exit %d, %v\n%s", code, w.api.Calls(), out)
	}
	// keelbox up from a computer that knows only the config: it asks the
	// cloud before it would create a server, and says the one line.
	forgetParkMark("acme")
	forgetUpState("acme")
	o, pos, err := parse("up", []string{"acme"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	code = cmdUp(context.Background(), o, pos, prompt.New(strings.NewReader(""), &buf, false), realRunner{}, &buf, &buf)
	if out := buf.String(); code != 1 || strings.Count(out, "acme has been parked since "+since) != 1 || !strings.Contains(out, "keelbox resume acme") || strings.Contains(out, "first apply") {
		t.Errorf("up from another computer: exit %d\n%s", code, out)
	}
	for _, c := range w.api.Calls() {
		if strings.HasPrefix(c, "POST /servers") {
			t.Errorf("a server was created: %s", c)
		}
	}
	if len(w.api.Servers) != 0 || len(w.api.Images) != 1 {
		t.Errorf("after all that: %d servers, %d snapshots", len(w.api.Servers), len(w.api.Images))
	}
}

// keelbox ls and keelbox status --all say parked, with the date, and a
// parked box does not make status --all fail.
func TestListAndStatusAllShowParked(t *testing.T) {
	w := newParkWorld(t)
	w.do(t, "park", options{yes: true}, nil)
	since := day(time.Now())
	_, out, _ := run("ls")
	if !strings.Contains(out, "acme  hetzner   -        parked since "+since+": keelbox resume acme") {
		t.Errorf("ls:\n%s", out)
	}
	code, out, _ := run("status", "--all")
	if code != 0 || !strings.Contains(out, "parked since "+since+": keelbox resume acme") || !strings.Contains(out, "1 box: 1 parked.") || strings.Contains(out, "unreachable") {
		t.Errorf("status --all: exit %d\n%s", code, out)
	}
	_, out, _ = run("status", "--all", "--json")
	if !strings.Contains(out, `"reach": "parked"`) || !strings.Contains(out, `"parked": "`) {
		t.Errorf("status --all --json:\n%s", out)
	}
	// Parked from another computer: the cloud says so, and it is noted.
	forgetParkMark("acme")
	code, out, _ = run("status", "--all")
	if _, ok := loadParkMark("acme"); code != 0 || !ok || !strings.Contains(out, "parked since "+since) {
		t.Errorf("status --all without the note: exit %d\n%s", code, out)
	}
	// forget says the snapshot stays and is billed.
	code, out, _ = run("forget", "acme", "--yes")
	if code != 0 || !strings.Contains(out, "It has been parked since "+since+": the snapshot of its disk stays in the cloud and is billed until it is deleted in the cloud's console: keelbox no longer manages it.") {
		t.Errorf("forget: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(parkMarkPath("acme")); err == nil {
		t.Error("forget left the parked note")
	}
}

// destroy on a parked box deletes the snapshot and the firewall, after
// saying that it cannot look at the work and asking as for a server that
// does not answer; nothing of another box is touched.
func TestDestroyParkedBox(t *testing.T) {
	setup := func() *parkWorld {
		w := newParkWorld(t)
		w.do(t, "park", options{yes: true}, nil)
		theirs := hetznertest.Snapshot(77, "other", time.Now())
		mine := hetznertest.Snapshot(78, "acme", time.Now())
		mine["labels"] = map[string]string{"keelbox": "true", "keelbox-box": "acme"} // not a parked snapshot
		w.api.Images = append(w.api.Images, theirs, mine)
		w.api.Firewalls = append(w.api.Firewalls, hetznertest.Firewall(8, "other"))
		w.forgetCalls()
		return w
	}
	left := func(w *parkWorld) string {
		var ids []string
		for _, i := range w.api.Images {
			ids = append(ids, "snapshot "+fmt.Sprint(i["id"]))
		}
		for _, f := range w.api.Firewalls {
			ids = append(ids, "firewall "+fmt.Sprint(f["id"]))
		}
		return strings.Join(ids, ", ")
	}
	const others = "snapshot 77, snapshot 78, firewall 8"

	// No terminal: refused, --yes or not, with the two ways on.
	w := setup()
	for _, o := range []options{{}, {yes: true}} {
		code, out := w.do(t, "destroy", o, nil)
		if code != 1 || len(w.writes()) != 0 {
			t.Errorf("%+v without a terminal: exit %d, %v", o, code, w.writes())
		}
		wantAll(t, "refusal", out, "keelbox: not deleted: acme has been parked since ", "so keelbox cannot check it for work that exists only there. snapshot 1001 (2.5 GB, about EUR 0.04 a month) holds everything that was on the box, the agent's projects and sign-ins included.",
			"Bring it back and look: keelbox resume acme; or delete it without the check: keelbox destroy acme --yes --discard-work\n")
	}
	// On a terminal: No by default, then the box name.
	w.terminal = true
	if code, out := w.do(t, "destroy", options{}, str("\n")); code != 1 || len(w.writes()) != 0 || !strings.Contains(out, "Delete it without knowing? [y/N] keelbox: not confirmed; nothing deleted") {
		t.Errorf("Enter: exit %d\n%s", code, out)
	}
	if code, out := w.do(t, "destroy", options{}, str("y\nwrong\n")); code != 1 || len(w.writes()) != 0 || !strings.Contains(out, "Delete snapshot 1001 and everything in it? Type the box name to confirm: ") {
		t.Errorf("the wrong name: exit %d\n%s", code, out)
	}
	code, out := w.do(t, "destroy", options{}, str("y\nacme\n"))
	if code != 0 || left(w) != others {
		t.Fatalf("destroy: exit %d, left %s\n%s", code, left(w), out)
	}
	wantAll(t, "destroy", out, "keelbox: deleted snapshot 1001\n", "keelbox: deleted firewall keelbox-acme (id 7)\n")
	if got := strings.Join(w.writes(), "\n"); got != "DELETE /images/1001\nDELETE /firewalls/7" {
		t.Errorf("calls:\n%s", got)
	}
	if _, ok := loadParkMark("acme"); ok || pinned(t, "203.0.113.7") != "" {
		t.Error("the parked note or the pinned host key is still there")
	}

	// Without a terminal, with both flags.
	w = setup()
	if code, out := w.do(t, "destroy", options{yes: true, discardWork: true}, nil); code != 0 || left(w) != others {
		t.Errorf("--yes --discard-work: exit %d, left %s\n%s", code, left(w), out)
	}

	// A server with a snapshot beside it, as an unfinished resume leaves:
	// both go.
	w = setup()
	w.applyCode = 1
	w.do(t, "resume", options{}, nil)
	if code, out := w.do(t, "destroy", options{yes: true}, nil); code != 0 || len(w.api.Servers) != 0 || left(w) != others {
		t.Errorf("server and snapshot: exit %d, %d servers, left %s\n%s", code, len(w.api.Servers), left(w), out)
	}
}

// Only a server and a snapshot with keelbox's labels for the box are
// ever acted on.
func TestParkTouchesOnlyLabelledResources(t *testing.T) {
	w := newParkWorld(t)
	w.api.Servers[0]["labels"] = map[string]string{"owner": "sam"}
	for _, cmd := range []string{"park", "resize"} {
		if code, out := w.do(t, cmd, options{yes: true, size: "cpx22"}, nil); code != 1 || len(w.writes()) != 0 || !strings.Contains(out, "was not created by keelbox") {
			t.Errorf("%s on a server without the labels: exit %d, %v\n%s", cmd, code, w.writes(), out)
		}
	}
	// Another box's parked snapshot does not make this box parked.
	w = newParkWorld(t)
	w.api.Servers = nil
	w.api.Images = []map[string]any{hetznertest.Snapshot(77, "other", time.Now())}
	if code, out := w.do(t, "resume", options{}, nil); code != 1 || len(w.writes()) != 0 || !strings.Contains(out, "no server named acme") {
		t.Errorf("resume beside another box's snapshot: exit %d, %v\n%s", code, w.writes(), out)
	}
}

// From a computer that never saw the box: no note, no pinned key. The
// cloud's labels are enough to resume; the host key is then asked about as
// for any new host, never trusted silently.
func TestResumeFromAnotherComputer(t *testing.T) {
	w := newParkWorld(t)
	w.do(t, "park", options{yes: true}, nil)
	forgetParkMark("acme")
	provider.ForgetHost(provider.DefaultKnownHosts(), "203.0.113.7")
	// A stale pin for the address Hetzner hands out next, from someone
	// else's server.
	stale := addr{"198.51.100.3", 22}
	_ = sshTarget(w.cfg, stale, options{}, io.Discard, io.Discard).Pin(provider.ParseHostKeys("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIStaleStaleStaleStaleStaleStaleStaleStaleSta0"))
	code, out := w.do(t, "resume", options{}, nil)
	if code != 0 || len(w.api.Images) != 0 || len(w.waited) != 1 || w.waited[0] != stale.host {
		t.Fatalf("resume: exit %d, waited for %v\n%s", code, w.waited, out)
	}
	if pinned(t, stale.host) != "" {
		t.Errorf("a key pinned for another server at %s was kept", stale.host)
	}
}

// A machine that is not a cloud server keelbox created has nothing to
// resize, park or resume.
func TestParkNeedsACloudBox(t *testing.T) {
	isolate(t)
	register(t, config.StarterFor("nas", "ssh"))
	register(t, config.StarterFor("here", "local"))
	for _, box := range []string{"nas", "here"} {
		for _, cmd := range []string{"resize", "park", "resume"} {
			code, out, errOut := run(cmd, box)
			if code != 2 || out != "" || !strings.Contains(errOut, "keelbox: "+cmd+" ") || !strings.Contains(errOut, "a server keelbox created on a cloud (provider hetzner, aws, digitalocean or gcp); "+box+" is a machine you have") {
				t.Errorf("keelbox %s %s: exit %d\n%s%s", cmd, box, code, out, errOut)
			}
		}
	}
}

func TestParkHelp(t *testing.T) {
	for _, cmd := range []string{"resize", "park", "resume"} {
		code, out, _ := run(cmd, "--help")
		if code != 0 || !strings.HasPrefix(out, "keelbox "+cmd+" [box]: ") || !strings.Contains(out, "keelbox "+cmd+" acme") {
			t.Errorf("keelbox %s --help: exit %d\n%s", cmd, code, out)
		}
		if !strings.Contains(overview(), "  "+cmd+" [box] ") {
			t.Errorf("keelbox help lacks %s", cmd)
		}
	}
	_, out, _ := run("resize", "--help")
	wantAll(t, "resize --help", out, "--size SIZE", "--yes", "running sessions stop")
	_, out, _ = run("park", "--help")
	wantAll(t, "park --help", out, "--yes", "only once the cloud reports the", "Snapshot storage stays billed")
}

// An apply that changed the cloud's firewall says what that means for
// reaching the box, not only the rules.
func TestFirewallChangedSaysWhatItMeans(t *testing.T) {
	closed, open := false, true
	for want, in := range map[string]*bool{
		"the cloud firewall no longer admits SSH from the internet, as the box itself does not (keelbox-acme: in udp 41641)": &closed,
		"the cloud firewall admits SSH from the internet (keelbox-acme: in udp 41641)":                                       &open,
		"the cloud firewall updated (keelbox-acme: in udp 41641)":                                                            nil,
	} {
		if got := firewallChanged(in, "keelbox-acme: in udp 41641"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// Resume creates a server, which is billed from then: it says so before
// it does, with the price the server had where this computer parked it
// into the snapshot being resumed.
func TestResumeSaysTheServerIsBilledAgain(t *testing.T) {
	pk := &cloudParked{ref: "snapshot 42"}
	for name, tc := range map[string]struct {
		mark parkMark
		want string
	}{
		"parked here":             {parkMark{Snapshot: "snapshot 42", Server: "about EUR 3.79 a month"}, "its server is billed again from the moment it is created: about EUR 3.79 a month when it was parked"},
		"the cloud gave no price": {parkMark{Snapshot: "snapshot 42"}, "its server is billed again from the moment it is created"},
		"parked elsewhere":        {parkMark{}, "its server is billed again from the moment it is created"},
		"another snapshot":        {parkMark{Snapshot: "snapshot 7", Server: "EUR 3.79 a month"}, "its server is billed again from the moment it is created"},
	} {
		if got := billedAgain(tc.mark, pk); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
