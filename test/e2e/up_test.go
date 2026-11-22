//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/provider/hetzner"
)

// keelbox up, run the way an agent or CI would: without a terminal, every
// answer a flag. Each run ends where only the owner can go on, at the
// vendor sign-ins, so the checks are that up stops there cleanly with exit
// 3, that a second run resumes at that step without redoing the others,
// and that the box is converged and signed out. keelbox runs with its own
// HOME, so the registry, known_hosts and the file credential store start
// empty and nothing is left in the real ~/.config/keelbox.
//
//	KEELBOX_E2E_TARGET=lxd     go test -tags e2e -run TestUp -v ./test/e2e/
//	KEELBOX_E2E_TARGET=hetzner go test -tags e2e -run TestUp -v ./test/e2e/

// upPrefix names this test's VMs and servers; the sweep deletes only those.
// KEELBOX_E2E_UP_PREFIX replaces it, to keep runs apart.
var upPrefix = envOr("KEELBOX_E2E_UP_PREFIX", "kb-up-")

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// upHome is keelbox's HOME for the up tests.
type upHome struct {
	dir string
	env []string
}

func newUpHome(t *testing.T) *upHome {
	t.Helper()
	dir := t.TempDir()
	env := []string{"HOME=" + dir, "KEELBOX_CREDENTIAL_STORE=file"}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "HOME" && k != "HCLOUD_TOKEN" && k != "KEELBOX_CREDENTIAL_STORE" {
			env = append(env, kv)
		}
	}
	return &upHome{dir: dir, env: env}
}

// kbUp runs keelbox with stdin from /dev/null and returns its output and
// exit code.
func (h *upHome) kb(t *testing.T, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, keelbox, args...)
	cmd.Dir, cmd.Env = workDir, h.env
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("keelbox %s: %v", args[0], err)
	}
	logf(t, "keelbox %s: exit %d in %s", strings.Join(args, " "), code, time.Since(start).Round(time.Second))
	fmt.Fprintf(&transcript, "%s\n", out)
	return string(out), code
}

func upBoxName() string { return fmt.Sprintf("%s%d", upPrefix, time.Now().Unix()%1000000) }

// stopsAtSignIn checks the end state every up run should reach without a
// terminal, then that a second run resumes at sign-in and changes nothing.
func stopsAtSignIn(t *testing.T, h *upHome, box, out string, code int, host, bootstrap string) {
	t.Helper()
	if code != 3 || !strings.Contains(out, "signing in is yours to do") || !strings.Contains(out, "keelbox up "+box) {
		t.Fatalf("up did not stop at sign-in (exit %d):\n%s", code, out)
	}
	logf(t, "PASS up stops at sign-in with exit 3")
	if ls, _ := h.kb(t, "ls"); !strings.Contains(ls, box) || !strings.Contains(ls, "needs sign-in: keelbox up "+box) {
		t.Errorf("ls does not show the box waiting for sign-in:\n%s", ls)
	} else {
		logf(t, "PASS ls: %s", firstLine(ls[strings.Index(ls, box):]))
	}
	again, code := h.kb(t, "up", box)
	if code != 3 || !strings.Contains(again, "continuing "+box) || strings.Contains(again, "keelbox apply:") {
		t.Errorf("second up did not resume at sign-in without applying (exit %d):\n%s", code, again)
	} else {
		logf(t, "PASS second up resumes at sign-in, nothing re-applied")
	}
	if plan, code := h.kb(t, "plan", box); code != 0 || !strings.Contains(plan, "No changes.") {
		t.Errorf("plan by box name after up (exit %d):\n%s", code, plan)
	} else {
		logf(t, "PASS plan %s: no changes", box)
	}
	// Signed out: none of the vendor CLIs has credentials.
	if out, err := sshRun(t, host, "agent", nil, "claude auth status >/dev/null 2>&1 && echo claude-in; codex login status >/dev/null 2>&1 && echo codex-in; gh auth status >/dev/null 2>&1 && echo gh-in; echo done"); err != nil || strings.Contains(out, "-in") {
		t.Errorf("agent not signed out: %v\n%s", err, out)
	} else {
		logf(t, "PASS agent CLIs installed and signed out")
	}
	if out, err := sshRun(t, host, "admin", nil, "sudo -n true && echo admin-sudo"); err != nil || !strings.Contains(out, "admin-sudo") {
		t.Errorf("admin: %v %s", err, out)
	}
	if bootstrap != "" && bootstrap != "admin" {
		if _, err := sshRun(t, host, bootstrap, nil, "true"); err == nil {
			t.Errorf("%s can still log in", bootstrap)
		} else {
			logf(t, "PASS %s locked out", bootstrap)
		}
	}
}

func TestUp(t *testing.T) {
	switch os.Getenv("KEELBOX_E2E_TARGET") {
	case "lxd":
		ensureKey(t)
		build(t)
		defer sweepUpVMs(t)
		t.Run("existing-server", upExistingServer)
		t.Run("existing-server-new-key", upNewKey)
		t.Run("existing-server-password-sudo", upPasswordSudo)
		t.Run("cloud-init", upCloudInit)
		t.Run("this-machine", upThisMachine)
	case "hetzner":
		ensureKey(t)
		build(t)
		upHetzner(t)
	default:
		t.Skip("set KEELBOX_E2E_TARGET=lxd or hetzner")
	}
	if p := os.Getenv("KEELBOX_E2E_LOG"); p != "" {
		_ = os.WriteFile(p, transcript.Bytes(), 0o600)
	}
}

// --- LXD ---------------------------------------------------------------

func sweepUpVMs(t *testing.T) {
	if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
		return
	}
	out, err := exec.Command("lxc", "list", "--format", "csv", "-c", "n").Output()
	if err != nil {
		t.Errorf("lxc list: %v", err)
		return
	}
	for _, n := range strings.Fields(string(out)) {
		if strings.HasPrefix(n, upPrefix) {
			logf(t, "sweep: deleting VM %s", n)
			_ = exec.Command("lxc", "delete", "--force", n).Run()
		}
	}
}

// launch starts an Ubuntu VM with userData and waits for its address.
func launch(t *testing.T, name, userData string) string {
	t.Helper()
	img := os.Getenv("KEELBOX_E2E_IMAGE")
	if img == "" {
		img = "26.04"
	}
	lxc(t, "launch", "ubuntu:"+img, name, "--vm", "-c", "limits.cpu=2", "-c", "limits.memory=2GiB", "--config=cloud-init.user-data="+userData)
	var ip string
	for i := 0; i < 90 && ip == ""; i++ {
		time.Sleep(2 * time.Second)
		ip = lxdIPv4(t, name)
	}
	if ip == "" {
		t.Fatalf("%s has no IPv4 address", name)
	}
	logf(t, "launched VM %s (ubuntu:%s) at %s", name, img, ip)
	return ip
}

func deleteVM(t *testing.T, name string) {
	if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
		return
	}
	lxc(t, "delete", "--force", name)
	logf(t, "deleted VM %s", name)
}

// upExistingServer: a fresh cloud image with the key on ubuntu, as a VPS
// provider hands it over; up applies as ubuntu, then as admin.
func upExistingServer(t *testing.T) {
	h := newUpHome(t)
	box := upBoxName()
	pub := ensureKey(t)
	ip := launch(t, box, "#cloud-config\nssh_authorized_keys:\n  - "+pub+"\n")
	defer deleteVM(t, box)
	for i := 0; i < 60; i++ {
		if _, err := sshRun(t, ip, "ubuntu", nil, "cloud-init status --wait >/dev/null 2>&1; true"); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	fpr := strings.Fields(lxc(t, "exec", box, "--", "ssh-keygen", "-l", "-E", "sha256", "-f", "/etc/ssh/ssh_host_ed25519_key.pub"))[1]
	out, code := h.kb(t, "up", box, "--provider", "ssh", "--host", ip, "--ssh-user", "ubuntu", "--host-key", fpr, "--ssh-key", keyPath, "--yes")
	stopsAtSignIn(t, h, box, out, code, ip, "ubuntu")
	cfg, err := os.ReadFile(filepath.Join(h.dir, ".config/keelbox/boxes", box+".yaml"))
	if err != nil || !strings.Contains(string(cfg), "host_key: \""+fpr+"\"") || !strings.Contains(string(cfg), "# keelbox box config") {
		t.Errorf("registry config: %v\n%s", err, cfg)
	}
}

var snippetRe = regexp.MustCompile(`(?s)#cloud-config\n.*?\n\n`)

// upCloudInit: up prints the cloud-config, the "provider console" (LXD)
// creates the server from it, and up continues with its address.
func upCloudInit(t *testing.T) {
	h := newUpHome(t)
	box := upBoxName()
	out, code := h.kb(t, "up", box, "--provider", "cloud-init", "--ssh-key", keyPath, "--yes")
	if code != 3 || !strings.Contains(out, "keelbox up "+box+" --host") {
		t.Fatalf("up did not stop for the server (exit %d):\n%s", code, out)
	}
	snippet := snippetRe.FindString(out)
	if snippet == "" || !strings.Contains(snippet, "ed25519_private") || !strings.Contains(snippet, "name: admin") {
		t.Fatalf("no cloud-config with a host key in the output:\n%s", out)
	}
	logf(t, "PASS up printed a cloud-config (%d lines), exit 3", strings.Count(snippet, "\n"))
	ip := launch(t, box, snippet)
	defer deleteVM(t, box)
	out, code = h.kb(t, "up", box, "--host", ip)
	stopsAtSignIn(t, h, box, out, code, ip, "")
	if !strings.Contains(out, "from the cloud-config") || !strings.Contains(out, "host keys rotated and pinned") {
		t.Errorf("host key not pinned from the snippet and rotated:\n%s", out)
	}
	// The snippet's host key is gone from the server and the laptop.
	snippetPub := regexp.MustCompile(`ed25519_public: (ssh-ed25519 \S+)`).FindStringSubmatch(snippet)
	if served, err := sshRun(t, ip, "admin", nil, "cat /etc/ssh/ssh_host_ed25519_key.pub"); err != nil || len(snippetPub) < 2 || strings.Contains(served, strings.Fields(snippetPub[1])[1]) {
		t.Errorf("the single-use host key is still in use: %v %s", err, served)
	} else {
		logf(t, "PASS single-use host key replaced on the server")
	}
	if _, err := os.Stat(filepath.Join(h.dir, ".config/keelbox/boxes", box+".cloud-config")); err == nil {
		t.Error("the saved cloud-config was not deleted after rotation")
	}
	if out, err := sshRun(t, ip, "admin", nil, "id ubuntu 2>&1 || echo no-ubuntu"); err != nil || !strings.Contains(out, "no-ubuntu") {
		t.Errorf("the image's default account exists: %v %s", err, out)
	} else {
		logf(t, "PASS no image default account; admin from the cloud-config")
	}
}

// --- Hetzner -----------------------------------------------------------

func upHetzner(t *testing.T) {
	tf := filepath.Join(home, ".config/keelbox/hetzner.token")
	tok, err := hetzner.TokenFromFile(tf)
	if err != nil {
		t.Skipf("e2e pending: %v", err)
	}
	cl := hetzner.New(tok)
	ctx := context.Background()
	sweep := func() {
		servers, err := cl.Servers(ctx, hetzner.LabelManaged+"=true")
		if err != nil {
			t.Errorf("sweep: %v", err)
			return
		}
		for i := range servers {
			if s := servers[i]; strings.HasPrefix(s.Name, upPrefix) {
				logf(t, "sweep: deleting server %s (id %d)", s.Name, s.ID)
				_ = hetzner.Destroy(ctx, cl, &s)
				_, _ = hetzner.DestroyFirewall(ctx, cl, s.Name)
			}
		}
	}
	sweep()
	types, err := cl.ServerTypes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st, loc, price, err := hetzner.CheapestShared(types, "x86", []string{"fsn1", "nbg1", "hel1"})
	if err != nil {
		t.Fatal(err)
	}
	// up uploads the laptop key unless the project has it already (as the
	// TestBox harness's keelbox-e2e); a key it uploaded goes at the end.
	fp, err := hetzner.MD5Fingerprint(ensureKey(t))
	if err != nil {
		t.Fatal(err)
	}
	hadKey, err := cl.SSHKeyByFingerprint(ctx, fp)
	if err != nil {
		t.Fatal(err)
	}
	h := newUpHome(t)
	// The token goes to keelbox through the file credential store, copied
	// file to file: never on a command line or in the output.
	credDir := filepath.Join(h.dir, ".config/keelbox/credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "hetzner"), []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	box := upBoxName()
	start := time.Now()
	defer func() {
		if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
			return
		}
		if out, code := h.kb(t, "destroy", box, "--yes", "--discard-work"); code != 0 {
			t.Errorf("destroy: %d\n%s", code, out)
		}
		sweep()
		if hadKey == nil {
			if k, err := cl.SSHKeyByFingerprint(ctx, fp); err != nil {
				t.Errorf("SSH key: %v", err)
			} else if k != nil && k.Labels[hetzner.LabelManaged] == "true" {
				if err := cl.DeleteSSHKey(ctx, k.ID); err != nil {
					t.Errorf("delete SSH key %s: %v", k.Name, err)
				} else {
					logf(t, "deleted the SSH key up uploaded: %s", k.Name)
				}
			}
		}
		hours := time.Since(start).Hours()
		if hours < 1 {
			hours = 1
		}
		logf(t, "estimated spend: EUR %.4f (%s in %s, 1 billed hour at EUR %.4f, plus IPv4)", hours*price, st.Name, loc, price)
	}()
	// A temporary directory as long as macOS's per-user one: ssh's control
	// socket must move to /tmp rather than fail with "ControlPath too long".
	tmp := filepath.Join(h.dir, "var-folders-zz-zyxvpxvq6csfxvn_n0000000000000", "T")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	h.env = append(h.env, "TMPDIR="+tmp)
	logf(t, "TMPDIR is %d characters", len(tmp))

	// The first apply is left (SIGTERM, as a closed terminal) while the
	// server applies the runtime module: up exits 4 and names itself to
	// continue, the server finishes, and the next up reports the run once
	// and carries on to the sign-ins.
	first := h.start(t, "up", box, "--provider", "hetzner", "--size", st.Name, "--location", loc, "--ssh-key", keyPath, "--yes")
	first.waitOut(t, "\nruntime\n", 20*time.Minute)
	_ = first.cmd.Process.Signal(syscall.SIGTERM)
	out, code := first.wait(t, 2*time.Minute)
	if strings.Contains(out, tok) {
		t.Fatal("the token appeared in up's output")
	}
	if strings.Contains(out, "ControlPath") {
		t.Fatalf("ssh refused the control socket path:\n%s", out)
	}
	m := regexp.MustCompile(`running at ([0-9.]+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no server address in up's output (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "your SSH key is already in the Hetzner project as "+e2eLabel) && !strings.Contains(out, "uploaded your SSH key") {
		t.Errorf("no SSH key step:\n%s", out)
	}
	if code != 4 || !strings.Contains(out, "Left setup running on "+box) || !strings.Contains(out, "keelbox up "+box) ||
		strings.Contains(out, "created and set up in ") || strings.Contains(out, "signing in is yours") {
		t.Fatalf("up left mid-apply did not exit 4 and point at keelbox up (exit %d):\n%s", code, out)
	}
	logf(t, "PASS up left mid-apply: exit 4, setup carries on at %s", m[1])
	deadline := time.Now().Add(20 * time.Minute)
	for {
		end, _ := sshRun(t, m[1], "admin", nil, `tail -n 1 /var/lib/keelbox/runs/$(cat /var/lib/keelbox/runs/current)`+".jsonl")
		if strings.Contains(end, `"kind":"end"`) {
			if !strings.Contains(end, `"result":"ok"`) {
				t.Fatalf("the server's run did not end ok: %s", end)
			}
			logf(t, "PASS the server finished the first apply without the laptop: %s", strings.TrimSpace(end))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server's run did not end in 20 minutes: %s", end)
		}
		time.Sleep(10 * time.Second)
	}
	out, code = h.kb(t, "up", box)
	if !regexp.MustCompile(`setup on ` + box + ` finished .*: \d+ steps, \d+ changed, ok`).MatchString(out) {
		t.Errorf("up did not report the run it left (exit %d):\n%s", code, out)
	} else {
		logf(t, "PASS up reported the run it left once")
	}
	stopsAtSignIn(t, h, box, out, code, m[1], "root")
	if doc, code := h.kb(t, "doctor", box); !strings.Contains(doc, "cloud-firewall") || code != 0 {
		t.Errorf("doctor (exit %d):\n%s", code, doc)
	}
}

// --- On the machine itself ----------------------------------------------

// upThisMachine is keelbox up on the machine being set up, the way a home
// server is: installed with get.keelbox.dev's installer from a snapshot
// release served on the VM's loopback, two services listening on the LAN
// (one kept open, one blocked), a first up stopped with Ctrl-C on a real
// terminal mid-apply, then continued by the installer's own up.
func upThisMachine(t *testing.T) {
	rel := snapshotRelease(t)
	box := upBoxName()
	pub := ensureKey(t)
	ip := launch(t, box, "#cloud-config\nssh_authorized_keys:\n  - "+pub+"\n")
	defer deleteVM(t, box)
	for i := 0; i < 60; i++ {
		if _, err := sshRun(t, ip, "ubuntu", nil, "cloud-init status --wait >/dev/null 2>&1; true"); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	for _, f := range rel.files {
		lxc(t, "file", "push", "--create-dirs", "--uid", "1000", "--gid", "1000", f[0], box+"/home/ubuntu/rel/"+f[1])
	}
	must(t, ip, "ubuntu", "sudo systemd-run --unit kb-e2e-rel -p User=ubuntu python3 -m http.server --bind 127.0.0.1 8765 --directory /home/ubuntu/rel/srv >/dev/null && "+
		"sudo systemd-run --unit kb-e2e-lan-a -p DynamicUser=yes python3 -m http.server 8095 >/dev/null && "+
		"sudo systemd-run --unit kb-e2e-lan-b -p DynamicUser=yes python3 -m http.server 8096 >/dev/null && sleep 2 && "+
		"curl -fsS -o /dev/null http://127.0.0.1:8765/stable.json && echo served", "served")
	install := "curl -fsSL http://127.0.0.1:8765/install.sh | KEELBOX_INDEX_URL=http://127.0.0.1:8765/stable.json " +
		"KEELBOX_RELEASE_URL=http://127.0.0.1:8765/dl KEELBOX_TEST_RELEASE_KEYS=/home/ubuntu/rel/test-keys sh -s -- --test"

	// The installer, piped, as the docs give it.
	out := must(t, ip, "ubuntu", install, "release key signature verified", "installed keelbox "+rel.version, "Next:", "Remove: rm /home/ubuntu/.local/bin/keelbox")
	fmt.Fprintf(&transcript, "%s\n", out)
	logf(t, "PASS installer: signature and checksums checked, installed to ~/.local/bin")

	// keelbox up on a terminal, Ctrl-C while the runtime module applies.
	sshInput(t, ip, "ubuntu", ptyDriver, "cat > /home/ubuntu/rel/kb_pty_driver.py")
	up := "~/.local/bin/keelbox up " + box + " --provider local --lan-ports 8095 --agents none --gh --tailscale=false --git-name kb-e2e --git-email kb-e2e@example.com --agent-permissions auto --yes"
	out, err := sshLong(t, ip, "ubuntu", "TERM=xterm-256color LANG=C.UTF-8 python3 /home/ubuntu/rel/kb_pty_driver.py runtime "+up, 30*time.Minute)
	fmt.Fprintf(&transcript, "--- keelbox up on a terminal, Ctrl-C during runtime ---\n%s\n", out)
	// What shows, as one line: styles, card borders and wrapping removed.
	shown := strings.Join(strings.Fields(strings.ReplaceAll(ansiRe.ReplaceAllString(out, ""), "│", " ")), " ")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 130 || !strings.Contains(shown, "stopping after the current step") ||
		!strings.Contains(shown, "→ keelbox up "+box) || !strings.Contains(out, "\x1b[") {
		t.Fatalf("Ctrl-C during apply (%v):\n%s", err, out)
	}
	for _, want := range []string{"open to the LAN: python 8095/tcp", "Blocked from the network: 8096/tcp", "Your account ubuntu keeps its password and sudo."} {
		if !strings.Contains(shown, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
	logf(t, "PASS Ctrl-C on a terminal stops between steps, exit 130, says how to continue")

	// The installer's up continues it, without a terminal, to sign-in.
	out, err = sshLong(t, ip, "ubuntu", install+" up "+box, 30*time.Minute)
	fmt.Fprintf(&transcript, "--- installer, then keelbox up ---\n%s\n", out)
	if !errors.As(err, &ee) || ee.ExitCode() != 3 || !strings.Contains(out, "already installed") || !strings.Contains(out, "starting keelbox up") ||
		!strings.Contains(out, "continuing "+box) || !strings.Contains(out, "signing in is yours to do") {
		t.Fatalf("sh -s -- up did not continue to sign-in (%v):\n%s", err, out)
	}
	logf(t, "PASS curl | sh -s -- up continues the box to sign-in, exit 3")

	// What up chose from this machine.
	cfg := must(t, ip, "ubuntu", "cat ~/.config/keelbox/boxes/"+box+".yaml", "lock_cloud_user: false", "{name: python, port: 8095}")
	tz := strings.TrimSpace(must(t, ip, "ubuntu", "timedatectl show -p Timezone --value"))
	if !regexp.MustCompile(`(?m)^ *timezone: "?` + regexp.QuoteMeta(tz) + `"?( .*)?$`).MatchString(cfg) {
		t.Errorf("timezone %s not kept:\n%s", tz, cfg)
	}
	must(t, ip, "ubuntu", "stat -c %U ~/.config/keelbox/boxes/"+box+".yaml ~/.config/keelbox/boxes/"+box+".up.json | sort -u", "ubuntu")
	must(t, ip, "ubuntu", "sudo ufw status | grep -q 'keelbox allow python' && echo ruled", "ruled")
	logf(t, "PASS config keeps the machine's own account unlocked and port 8095 open to the LAN")
	if _, err := sshRun(t, ip, "ubuntu", nil, "true"); err != nil {
		t.Errorf("ubuntu, the account up ran from, was locked: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Get("http://" + ip + ":8095/"); err != nil {
		t.Errorf("8095, kept open to the LAN, is blocked: %v", err)
	} else {
		resp.Body.Close()
		logf(t, "PASS 8095 reachable from the LAN")
	}
	if resp, err := client.Get("http://" + ip + ":8096/"); err == nil {
		resp.Body.Close()
		t.Error("8096, not kept open, is reachable from the LAN")
	} else {
		logf(t, "PASS 8096 blocked from the LAN")
	}
	again, err := sshLong(t, ip, "ubuntu", "~/.local/bin/keelbox up "+box, 10*time.Minute)
	if !errors.As(err, &ee) || ee.ExitCode() != 3 || !strings.Contains(again, "continuing "+box) || strings.Contains(again, "keelbox apply:") {
		t.Errorf("third up did not resume at sign-in (%v):\n%s", err, again)
	}
	must(t, ip, "ubuntu", "sudo ~/.local/bin/keelbox plan", "No changes.")
	logf(t, "PASS up resumes at sign-in; plan on the box: no changes")
	// As the user who ran up, without sudo: keelbox runs it with sudo.
	must(t, ip, "ubuntu", "~/.local/bin/keelbox status 2>&1", "box        "+box+" (Ubuntu")
	if out, _ := sshRun(t, ip, "ubuntu", nil, "~/.local/bin/keelbox doctor 2>&1"); strings.Contains(out, "permission denied") || !strings.Contains(out, "keelbox up has not finished "+box) {
		t.Errorf("doctor as ubuntu:\n%s", out)
	}
	logf(t, "PASS status and doctor as the user, through sudo, point at keelbox up")
}

// ptyDriver runs a command on a pseudo-terminal, as a person at a terminal
// would, prints what it shows, and presses Ctrl-C once the output contains
// the word given first. It exits with the command's status.
const ptyDriver = `import os, pty, select, sys
word, argv = sys.argv[1].encode(), sys.argv[2:]
pid, fd = pty.fork()
if pid == 0:
    # exec: a shell waiting on the command would die of the Ctrl-C itself.
    os.execvp("sh", ["sh", "-c", "exec " + " ".join(argv)])
seen, sent = b"", False
while True:
    r, _, _ = select.select([fd], [], [], 1)
    if r:
        try:
            data = os.read(fd, 4096)
        except OSError:
            break
        if not data:
            break
        seen += data
        sys.stdout.buffer.write(data)
        sys.stdout.flush()
    if not sent and word in seen:
        os.write(fd, b"\x03")
        sent = True
_, status = os.waitpid(pid, 0)
sys.exit(os.waitstatus_to_exitcode(status))
`

// sshLong is sshRun with a longer timeout, for a whole up.
func sshLong(t *testing.T, host, user, remote string, d time.Duration) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	start := time.Now()
	out, err := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10", "-o", "LogLevel=ERROR", "-o", "ServerAliveInterval=15", "-i", keyPath, "-o", "IdentitiesOnly=yes",
		user+"@"+host, "--", remote).CombinedOutput()
	logf(t, "ssh %s@%s %s: %v in %s", user, host, firstLine(remote), errString(err), time.Since(start).Round(time.Second))
	return string(out), err
}

// release is a snapshot release laid out as get.keelbox.dev and the
// release host serve it, signed with a throwaway key.
type release struct {
	version string
	files   [][2]string // local path, path under ~/rel on the VM
}

// snapshotRelease reads dist/release (make snapshot), signing it with a
// throwaway release key as scripts/test-install.sh does, and writes the
// channel index for a server on the VM's 127.0.0.1:8765.
func snapshotRelease(t *testing.T) release {
	t.Helper()
	dist, _ := filepath.Abs("../../dist/release")
	meta, err := os.ReadFile(filepath.Join(dist, "metadata.json"))
	if err != nil {
		t.Skipf("no snapshot release (make snapshot): %v", err)
	}
	m := regexp.MustCompile(`"tag":"([^"]+)"`).FindSubmatch(meta)
	if m == nil {
		t.Fatal("no tag in dist/release/metadata.json")
	}
	v := string(m[1])
	if _, err := os.Stat(filepath.Join(dist, "SHA256SUMS.sig")); err != nil {
		for _, args := range [][]string{{"testkey", filepath.Join(dist, "test-keys")}} {
			if out, err := exec.Command("go", append([]string{"run", "../../internal/tools/releasesign"}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("releasesign %v: %v %s", args, err, out)
			}
		}
		priv, err := os.ReadFile(filepath.Join(dist, "test-keys/private.pem"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "run", "../../internal/tools/releasesign", "sign", v, filepath.Join(dist, "SHA256SUMS"))
		cmd.Env = append(os.Environ(), "KEELBOX_RELEASE_KEYS="+filepath.Join(dist, "test-keys/keys.pem"), "KEELBOX_RELEASE_SIGNING_KEY="+string(priv))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("releasesign sign: %v %s", err, out)
		}
	}
	bins, _ := filepath.Glob(filepath.Join(dist, "keelbox_linux_"+runtime.GOARCH+"_*", "keelbox"))
	if len(bins) != 1 {
		t.Fatalf("no linux/%s binary in %s", runtime.GOARCH, dist)
	}
	index := filepath.Join(t.TempDir(), "stable.json")
	out, err := exec.Command("../../scripts/channel-index.sh", v, filepath.Join(dist, "SHA256SUMS"), "http://127.0.0.1:8765/dl").Output()
	if err != nil {
		t.Fatalf("channel-index: %v", err)
	}
	if err := os.WriteFile(index, out, 0o600); err != nil {
		t.Fatal(err)
	}
	installer, _ := filepath.Abs("../../get/install.sh")
	d := "srv/dl/" + v + "/"
	return release{version: v, files: [][2]string{
		{installer, "srv/install.sh"}, {index, "srv/stable.json"},
		{filepath.Join(dist, "SHA256SUMS"), d + "SHA256SUMS"}, {filepath.Join(dist, "SHA256SUMS.sig"), d + "SHA256SUMS.sig"},
		{filepath.Join(dist, "SHA256SUMS.sshsig"), d + "SHA256SUMS.sshsig"}, {bins[0], d + "keelbox-linux-" + runtime.GOARCH},
		{filepath.Join(dist, "test-keys/keys.pem"), "test-keys/keys.pem"}, {filepath.Join(dist, "test-keys/keys.ssh"), "test-keys/keys.ssh"},
	}}
}

// isSetUp reports whether out carries the last screen of keelbox up for
// box: the headline, and the card with the ways to start working.
func isSetUp(out, box string) bool {
	return strings.Contains(out, box+" is set up") && strings.Contains(out, "Start working")
}
