//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/provider/hetzner"
)

// e2eLabel marks every server this harness creates. The sweep deletes only
// servers carrying both it and keelbox's own managed label.
const e2eLabel = "keelbox-e2e"

var createdAt = regexp.MustCompile(`running at ([0-9.]+)`)

func hostFromCreate(t *testing.T, out, fallback string) string {
	t.Helper()
	if m := createdAt.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	if fallback == "" {
		t.Fatal("could not find the server address in create output")
	}
	return fallback
}

// --- Hetzner ---------------------------------------------------------------

type hetznerTarget struct {
	cl        *hetzner.Client
	tokenFile string
	typ       string
	location  string
	image     string
	hourly    float64
	start     time.Time
	// st is the server type picked from the catalogue.
	st hetzner.ServerType
	// keyID is the keelbox-e2e project key when this run uploaded it;
	// Destroy deletes it.
	keyID int64
}

func newHetzner(t *testing.T) *hetznerTarget {
	t.Helper()
	tf := filepath.Join(home, ".config/keelbox/hetzner.token")
	tok, err := hetzner.TokenFromFile(tf)
	if err != nil {
		t.Skipf("e2e pending: %v", err)
	}
	h := &hetznerTarget{cl: hetzner.New(tok), tokenFile: tf}
	ctx := context.Background()
	h.sweep(t)
	existing, err := h.cl.Servers(ctx, e2eLabel+"=true")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) >= 2 {
		t.Fatalf("%d %s servers already exist; refusing to exceed 2", len(existing), e2eLabel)
	}
	locs, err := h.cl.Locations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var eu []string
	for _, l := range locs {
		if strings.HasPrefix(l.NetworkZone, "eu-") {
			eu = append(eu, l.Name)
		}
	}
	types, err := h.cl.ServerTypes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st, loc, price, err := hetzner.CheapestShared(types, "x86", eu)
	if err != nil {
		t.Fatal(err)
	}
	h.typ, h.location, h.hourly = st.Name, loc, price
	h.st = st
	want := os.Getenv("KEELBOX_E2E_IMAGE")
	for _, img := range []string{want, "ubuntu-26.04", "ubuntu-24.04"} {
		if img == "" {
			continue
		}
		if i, err := h.cl.SystemImage(ctx, img, "x86"); err == nil && i != nil {
			h.image = img
			break
		}
	}
	if h.image == "" {
		t.Fatal("neither ubuntu-26.04 nor ubuntu-24.04 is offered")
	}
	pub := ensureKey(t)
	k, err := h.cl.SSHKeyByName(ctx, e2eLabel)
	if err != nil {
		t.Fatal(err)
	}
	if k == nil {
		k, err = h.cl.CreateSSHKey(ctx, e2eLabel, pub, map[string]string{e2eLabel: "true"})
		if err != nil {
			t.Fatal(err)
		}
		h.keyID = k.ID
	} else if !strings.HasPrefix(pub, strings.TrimSpace(strings.Join(strings.Fields(k.PublicKey)[:2], " "))) {
		t.Fatalf("project key %s does not match %s.pub", e2eLabel, keyPath)
	}
	t.Logf("hetzner: %s in %s, %s, EUR %.4f/h", h.typ, h.location, h.image, h.hourly)
	return h
}

func (h *hetznerTarget) CreateApplies() bool { return true }

func (h *hetznerTarget) Create(t *testing.T, box string) (string, string, string) {
	h.start = time.Now()
	logf(t, "target hetzner: server type %s, location %s, image %s, EUR %.4f/h gross", h.typ, h.location, h.image, h.hourly)
	return fmt.Sprintf(`target:
  provider: hetzner
  ssh:
    identity_file: %s
  hetzner:
    server_type: %s
    location: %s
    image: %s
    ssh_keys: [%s]
    token_file: %s
    labels: {%s: "true"}`, keyPath, h.typ, h.location, h.image, e2eLabel, h.tokenFile, e2eLabel), "root", ""
}

func (h *hetznerTarget) Destroy(t *testing.T, box string) {
	if out, err := kb(t, "destroy", filepath.Join(workDir, box+".yaml"), "--yes", "--discard-work"); err != nil {
		t.Errorf("destroy: %v\n%s", err, out)
	}
	h.sweep(t)
	if h.keyID != 0 {
		if err := h.cl.DeleteSSHKey(context.Background(), h.keyID); err != nil {
			t.Errorf("delete SSH key %s: %v", e2eLabel, err)
		} else {
			logf(t, "deleted the SSH key this run uploaded: %s", e2eLabel)
		}
	}
}

func (h *hetznerTarget) SizeKey() string { return "server_type:" }
func (h *hetznerTarget) Size() string    { return h.typ }

// BiggerSize is the cheapest shared x86 type in the location with more
// vCPUs or memory than the box's and a disk at least as large. The
// catalogue is read again: what can be ordered in a location changes by
// the minute.
func (h *hetznerTarget) BiggerSize(t *testing.T) string {
	types, err := h.cl.ServerTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	best, price := "", 0.0
	for _, c := range types {
		if c.Deprecated || c.CPUType != "shared" || c.Architecture != "x86" || !c.AvailableIn(h.location) || c.Disk < h.st.Disk {
			continue
		}
		if c.Cores <= h.st.Cores && c.Memory <= h.st.Memory {
			continue
		}
		if p, ok := c.HourlyGross(h.location); ok && (best == "" || p < price) {
			best, price = c.Name, p
		}
	}
	if best == "" {
		t.Fatalf("no server type bigger than %s can be ordered in %s", h.typ, h.location)
	}
	return best
}

func (h *hetznerTarget) Holds(t *testing.T, box string) (servers, snapshots, firewalls int) {
	ctx := context.Background()
	sel := hetzner.LabelManaged + "=true," + hetzner.LabelBox + "=" + box
	srvs, err := h.cl.Servers(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := h.cl.Snapshots(ctx, sel+","+hetzner.LabelParked+"=true")
	if err != nil {
		t.Fatal(err)
	}
	fws, err := h.cl.Firewalls(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	return len(srvs), len(snaps), len(fws)
}

// HoldAddress creates a placeholder server while the box is parked, so that
// the address the box gave up is likely taken when it resumes and Hetzner
// hands it another: left alone, Hetzner tends to give a project back the
// address it just freed. The placeholder carries the harness's labels (the
// sweep deletes it) and another box label, and is never logged in to. The
// returned function deletes it.
func (h *hetznerTarget) HoldAddress(t *testing.T, box, addr string) func() {
	ctx := context.Background()
	key, err := h.cl.SSHKeyByName(ctx, e2eLabel)
	if err != nil || key == nil {
		t.Fatalf("project key %s: %v", e2eLabel, err)
	}
	name := box + "-hold"
	srv, act, err := h.cl.CreateServer(ctx, hetzner.CreateServerRequest{
		Name: name, ServerType: h.typ, Image: h.image, Location: h.location, SSHKeys: []int64{key.ID},
		Labels: map[string]string{e2eLabel: "true", hetzner.LabelManaged: "true", hetzner.LabelBox: name},
	})
	if err != nil {
		t.Fatalf("placeholder server: %v", err)
	}
	if err := h.cl.WaitAction(ctx, act.ID, 2*time.Second); err != nil {
		t.Errorf("placeholder server: %v", err)
	}
	if got, err := h.cl.Server(ctx, srv.ID); err == nil {
		srv = got
	}
	logf(t, "placeholder %s (id %d) holds %s; the box had %s", name, srv.ID, srv.PublicNet.IPv4.IP, addr)
	return func() {
		if err := hetzner.Destroy(ctx, h.cl, srv); err != nil {
			t.Errorf("delete placeholder %d: %v", srv.ID, err)
			return
		}
		logf(t, "deleted placeholder %s (id %d)", name, srv.ID)
	}
}

// sweep deletes every server this harness labelled, whatever run made it,
// and the parked snapshots and firewalls of its boxes.
func (h *hetznerTarget) sweep(t *testing.T) {
	ctx := context.Background()
	servers, err := h.cl.Servers(ctx, e2eLabel+"=true,"+hetzner.LabelManaged+"=true")
	if err != nil {
		t.Errorf("sweep list: %v", err)
		return
	}
	for i := range servers {
		s := servers[i]
		logf(t, "sweep: deleting %s (id %d)", s.Name, s.ID)
		if err := hetzner.Destroy(ctx, h.cl, &s); err != nil {
			t.Errorf("sweep delete %d: %v", s.ID, err)
		}
	}
	// Parked snapshots: a run that failed between keelbox park and
	// keelbox resume leaves one, and it is billed until deleted.
	snaps, err := h.cl.Snapshots(ctx, e2eLabel+"=true,"+hetzner.LabelManaged+"=true,"+hetzner.LabelParked+"=true")
	if err != nil {
		t.Errorf("sweep snapshots: %v", err)
		return
	}
	for i := range snaps {
		img := snaps[i]
		logf(t, "sweep: deleting snapshot %d (%s)", img.ID, img.Description)
		if err := hetzner.DeleteSnapshot(ctx, h.cl, img.Labels[hetzner.LabelBox], &img); err != nil {
			t.Errorf("sweep delete snapshot %d: %v", img.ID, err)
		}
	}
	// Cloud Firewalls this harness's boxes left: labelled by keelbox and by
	// the harness, detached from deleted servers by Hetzner.
	fws, err := h.cl.Firewalls(ctx, e2eLabel+"=true,"+hetzner.LabelManaged+"=true")
	if err != nil {
		t.Errorf("sweep firewalls: %v", err)
		return
	}
	for _, f := range fws {
		if f.Labels[hetzner.LabelBox] == "" {
			continue
		}
		logf(t, "sweep: deleting firewall %s (id %d)", f.Name, f.ID)
		if _, err := hetzner.DestroyFirewall(ctx, h.cl, f.Labels[hetzner.LabelBox]); err != nil {
			t.Errorf("sweep delete firewall %d: %v", f.ID, err)
		}
	}
}

func (h *hetznerTarget) Spend() string {
	if h.start.IsZero() {
		return ""
	}
	hours := math.Max(1, math.Ceil(time.Since(h.start).Hours()))
	return fmt.Sprintf("EUR %.4f (%s, %.0f billed hour(s) at EUR %.4f, plus IPv4)", hours*h.hourly, h.typ, hours, h.hourly)
}

// --- LXD -------------------------------------------------------------------

// lxdTarget launches a local LXD virtual machine. A VM, not a container: the
// box runs Docker, UFW and systemd user services like a real server.
type lxdTarget struct{}

func (lxdTarget) CreateApplies() bool { return false }

// VMPrefix names every VM (and cloud server) this harness creates; the
// sweep deletes only names with it.
// KEELBOX_E2E_VM_PREFIX (kb-<word>-) keeps runs that share an LXD host
// apart.
var VMPrefix = vmPrefix()

func vmPrefix() string {
	p := os.Getenv("KEELBOX_E2E_VM_PREFIX")
	if p == "" {
		return "kb-e2e-"
	}
	if !regexp.MustCompile(`^kb-[a-z0-9]+-$`).MatchString(p) {
		panic("KEELBOX_E2E_VM_PREFIX must look like kb-name-")
	}
	return p
}

func (lxdTarget) Create(t *testing.T, box string) (string, string, string) {
	pub := ensureKey(t)
	if n := len(lxdE2EVMs(t)); n >= 2 {
		t.Fatalf("%d %s* VMs already exist; refusing to exceed 2", n, VMPrefix)
	}
	img := os.Getenv("KEELBOX_E2E_IMAGE")
	if img == "" {
		img = "26.04"
	}
	// Same path as a cloud server: an official cloud image, an SSH key
	// delivered by cloud-init, nothing else pre-installed.
	userData := "#cloud-config\nssh_authorized_keys:\n  - " + pub + "\n"
	lxc(t, "launch", "ubuntu:"+img, box, "--vm", "-c", "limits.cpu=2", "-c", "limits.memory=2GiB",
		"--config=cloud-init.user-data="+userData)
	var ip string
	for i := 0; i < 90 && ip == ""; i++ {
		time.Sleep(2 * time.Second)
		ip = lxdIPv4(t, box)
	}
	if ip == "" {
		t.Fatalf("%s has no IPv4 address", box)
	}
	ready := false
	for i := 0; i < 60 && !ready; i++ {
		_, err := sshRun(t, ip, "ubuntu", nil, "cloud-init status --wait >/dev/null 2>&1; true")
		ready = err == nil
		if !ready {
			time.Sleep(5 * time.Second)
		}
	}
	if !ready {
		t.Fatalf("%s: SSH as ubuntu never came up", box)
	}
	// The host key fingerprint comes out of band, through LXD, as a user
	// would read it from a provider console.
	fpr := strings.Fields(lxc(t, "exec", box, "--", "ssh-keygen", "-l", "-E", "sha256", "-f", "/etc/ssh/ssh_host_ed25519_key.pub"))[1]
	logf(t, "target lxd: VM %s (ubuntu:%s, 2 vCPU, 2 GiB) at %s, host key %s", box, img, ip, fpr)
	return fmt.Sprintf(`target:
  provider: ssh
  ssh:
    host: %s
    identity_file: %s
    known_hosts: %s
    host_key: "%s"`, ip, keyPath, filepath.Join(workDir, "known_hosts"), fpr), "ubuntu", ip
}

func (lxdTarget) Destroy(t *testing.T, box string) {
	lxc(t, "delete", "--force", box)
	logf(t, "deleted VM %s", box)
	for _, name := range lxdE2EVMs(t) {
		logf(t, "sweep: deleting VM %s", name)
		lxc(t, "delete", "--force", name)
	}
}

// lxdE2EVMs lists instances named with the harness prefix.
func lxdE2EVMs(t *testing.T) []string {
	var list []struct{ Name string }
	if err := json.Unmarshal([]byte(lxc(t, "list", "--format", "json")), &list); err != nil {
		t.Fatalf("lxc list: %v", err)
	}
	var out []string
	for _, i := range list {
		if strings.HasPrefix(i.Name, VMPrefix) {
			out = append(out, i.Name)
		}
	}
	return out
}
func (lxdTarget) Spend() string { return "" }

func lxc(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("lxc", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("lxc %s: %v\n%s", args[0], err, out)
	}
	return string(out)
}

func lxdIPv4(t *testing.T, box string) string {
	var list []struct {
		State struct {
			Network map[string]struct {
				Addresses []struct {
					Family, Address, Scope string
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(lxc(t, "list", box, "--format", "json")), &list); err != nil || len(list) == 0 {
		return ""
	}
	for name, n := range list[0].State.Network {
		if name == "lo" || strings.HasPrefix(name, "docker") {
			continue
		}
		for _, a := range n.Addresses {
			if a.Family == "inet" && a.Scope == "global" {
				return a.Address
			}
		}
	}
	return ""
}
