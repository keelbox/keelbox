//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/manifest"
)

// Adopting a machine already in use (ADR 0038). A VM is set up the way a
// home server is: Docker Engine from Docker's repository with a container
// published on 8080 (restart unless-stopped) and one without a restart
// policy, an account that logs in with a password and has sudo, UFW on
// with a rule for a service on 8081 and a second service on 8082 that UFW
// blocks, and a tmux server running as that account. Then:
//
//   - plan --impact lists what the first apply would cut off, restart and
//     keep, and nothing for what UFW already decides;
//   - a first apply without consent stops before any change;
//   - keelbox up without --adopt stops before any change, asking for it;
//   - keelbox up --lan-ports all --adopt sets the box up;
//   - everything the report said would keep working does, and everything
//     it said would be cut off or stopped is.
//
//	KEELBOX_E2E_TARGET=lxd go test -tags e2e -run TestAdopt -v ./test/e2e/
//
// VMs are named KEELBOX_E2E_ADOPT_PREFIX (default kb-adopt-) plus a number
// and deleted, with a sweep of any other VM with the prefix.

var adoptPrefix = envOr("KEELBOX_E2E_ADOPT_PREFIX", "kb-adopt-")

// impactJSON is the part of plan --impact --json the test reads.
type impactJSON struct {
	Impact     int  `json:"impact"`
	FirstApply bool `json:"first_apply"`
	Items      []struct {
		Group, Module, Subject, Detail, Hint string
		Gate                                 bool
	} `json:"items"`
	Keep struct {
		Allow []struct {
			Name string
			Port int
		}
	} `json:"keep"`
}

func (r impactJSON) find(group, subject string) (string, bool, bool) {
	for _, it := range r.Items {
		if it.Group == group && it.Subject == subject {
			return it.Detail + " " + it.Hint, it.Gate, true
		}
	}
	return "", false, false
}

func (r impactJSON) gated() string {
	var g []string
	for _, it := range r.Items {
		if it.Gate {
			g = append(g, it.Subject)
		}
	}
	sort.Strings(g)
	return strings.Join(g, " ")
}

func parseImpact(t *testing.T, out string) impactJSON {
	t.Helper()
	var r impactJSON
	if i := strings.Index(out, "{"); i < 0 || json.Unmarshal([]byte(out[i:]), &r) != nil || r.Impact != 1 {
		t.Fatalf("no impact report:\n%s", out)
	}
	return r
}

func sweepAdoptVMs(t *testing.T) {
	if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
		return
	}
	out, _ := exec.Command("lxc", "list", "--format", "csv", "-c", "n").Output()
	for _, n := range strings.Fields(string(out)) {
		if strings.HasPrefix(n, adoptPrefix) {
			logf(t, "sweep: deleting VM %s", n)
			_ = exec.Command("lxc", "delete", "--force", n).Run()
		}
	}
}

// reachable reports whether an HTTP server answers at ip:port from here.
func reachable(ip string, port int) bool {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/", ip, port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// homeServerSetup is the machine in use, set up as its owner would.
func homeServerSetup(t *testing.T, ip string) {
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	// Docker Engine as Docker's install guide gives it, at the release's
	// tested series so keelbox does not move it.
	docker := `set -e
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
printf 'Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: %s\nComponents: stable\nSigned-By: /etc/apt/keyrings/docker.asc\n' "$(. /etc/os-release && echo "$VERSION_CODENAME")" | sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null
sudo apt-get update -qq
v=$(apt-cache madison docker-ce | awk '{print $3}' | grep '^5:` + m.Docker.Series + `\.' | head -1)
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker-ce=$v docker-ce-cli=$v containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
sudo docker run -d -q --name web --restart unless-stopped -p 8080:80 nginx:alpine >/dev/null
sudo docker run -d -q --name scratch nginx:alpine >/dev/null
echo docker-ready`
	must(t, ip, "ubuntu", docker, "docker-ready")
	must(t, ip, "ubuntu", `set -e
sudo useradd -m -s /bin/bash -G sudo sam
echo "sam:$(head -c 12 /dev/urandom | base64)" | sudo chpasswd
echo 'PasswordAuthentication yes' | sudo tee /etc/ssh/sshd_config.d/50-cloud-init.conf >/dev/null
sudo systemctl reload ssh
sudo ufw allow 22/tcp >/dev/null && sudo ufw allow 8081/tcp >/dev/null && sudo ufw --force enable >/dev/null
sudo systemd-run -q --unit kb-adopt-a -p DynamicUser=yes python3 -m http.server 8081
sudo systemd-run -q --unit kb-adopt-b -p DynamicUser=yes python3 -m http.server 8082
sudo -u sam tmux new -d -s work
sleep 2; echo in-use`, "in-use")
	must(t, ip, "ubuntu", "sudo sshd -T | grep -x 'passwordauthentication yes'; sudo passwd -S sam", "passwordauthentication yes", "sam P")
	for port, want := range map[int]bool{8080: true, 8081: true, 8082: false} {
		if reachable(ip, port) != want {
			t.Fatalf("before keelbox: %d reachable=%v, want %v", port, !want, want)
		}
	}
	logf(t, "in use: web on 8080 (Docker, unless-stopped) and scratch (no policy); sam with a password and sudo; UFW on with 8081 open, 8082 blocked; tmux as sam")
}

func TestAdopt(t *testing.T) {
	if os.Getenv("KEELBOX_E2E_TARGET") != "lxd" {
		t.Skip("set KEELBOX_E2E_TARGET=lxd")
	}
	pub := ensureKey(t)
	build(t)
	defer sweepAdoptVMs(t)
	defer func() {
		if p := os.Getenv("KEELBOX_E2E_LOG"); p != "" {
			_ = os.WriteFile(p, transcript.Bytes(), 0o600)
		}
	}()
	box := fmt.Sprintf("%s%d", adoptPrefix, time.Now().Unix()%1000000)
	ip := launch(t, box, "#cloud-config\nssh_authorized_keys:\n  - "+pub+"\n")
	defer deleteVM(t, box)
	for i := 0; i < 60; i++ {
		if _, err := sshRun(t, ip, "ubuntu", nil, "cloud-init status --wait >/dev/null 2>&1; true"); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	homeServerSetup(t, ip)
	started := strings.TrimSpace(must(t, ip, "ubuntu", "sudo docker inspect -f '{{.State.StartedAt}}' web"))
	fpr := strings.Fields(lxc(t, "exec", box, "--", "ssh-keygen", "-l", "-E", "sha256", "-f", "/etc/ssh/ssh_host_ed25519_key.pub"))[1]

	// A hand-written config, as keelbox init gives it.
	cfg := filepath.Join(workDir, box+".yaml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(`version: 1
box: %s
target:
  provider: ssh
  ssh:
    host: %s
    identity_file: %s
    known_hosts: %s
    host_key: "%s"
users:
  admin:
    ssh_authorized_key_files: [%s.pub]
access:
  tailscale:
    enabled: false
runtime:
  node: false
  gh: false
agents: []
`, box, ip, keyPath, filepath.Join(workDir, "known_hosts"), fpr, keyPath)), 0o600); err != nil {
		t.Fatal(err)
	}

	// The report.
	out, err := kb(t, "plan", cfg, "--ssh-user", "ubuntu", "--impact", "--json")
	if err != nil {
		t.Fatalf("plan --impact: %v\n%s", err, out)
	}
	r := parseImpact(t, out)
	if !r.FirstApply || r.gated() != "8080/tcp docker sam" {
		t.Errorf("gated %q, first apply %v", r.gated(), r.FirstApply)
	}
	checks := []struct{ group, subject, want string }{
		{"cuts", "8080/tcp", "container web (Docker) is published to the network now"},
		{"cuts", "sam", "logs in over SSH with a password now"},
		{"restarts", "docker", "web comes back (restart policy); scratch has no restart policy and stays stopped"},
		{"keeps", "ufw", "UFW is on already: its "},
		{"keeps", "docker", "the system Docker daemon and its 2 containers stay"},
		{"keeps", "sam", "tmux runs as sam now: keelbox leaves it running"},
		{"restarts", "sshd", "keys only"},
	}
	for _, c := range checks {
		if d, _, ok := r.find(c.group, c.subject); !ok || !strings.Contains(d, c.want) {
			t.Errorf("impact %s %s: %q, want %q", c.group, c.subject, d, c.want)
		}
	}
	for _, it := range r.Items {
		if it.Subject == "8081/tcp" || it.Subject == "8082/tcp" {
			t.Errorf("UFW decides %s today and keelbox keeps its rules, yet it is listed: %+v", it.Subject, it)
		}
	}
	if len(r.Keep.Allow) != 1 || r.Keep.Allow[0].Name != "web" || r.Keep.Allow[0].Port != 8080 {
		t.Errorf("keep %+v", r.Keep)
	}
	text, _ := kb(t, "plan", cfg, "--ssh-user", "ubuntu", "--impact")
	fmt.Fprintf(&transcript, "--- plan --impact ---\n%s\n", text)
	logf(t, "PASS plan --impact: gated %s; 8081 and 8082 left to UFW", r.gated())

	unchanged := func(when string) {
		t.Helper()
		out := must(t, ip, "ubuntu", "sudo test -e /var/lib/keelbox/logbook.jsonl && echo logbook; sudo test -e /etc/ssh/sshd_config.d/01-keelbox.conf && echo dropin; id admin 2>/dev/null && echo admin; sudo docker ps --format '{{.Names}}' | sort | tr '\\n' ' '; echo end")
		if strings.Contains(out, "logbook") || strings.Contains(out, "dropin") || strings.Contains(out, "admin") || !strings.Contains(out, "scratch web end") {
			t.Fatalf("%s changed the machine:\n%s", when, out)
		}
	}

	// The gate: a first apply without consent changes nothing.
	out, err = kb(t, "apply", cfg, "--ssh-user", "ubuntu")
	if err == nil || !strings.Contains(out, box+" is in use, and this first apply would cut off or replace what it runs. Nothing has changed.") ||
		!strings.Contains(out, "- {name: web, port: 8080}") || !strings.Contains(out, "keelbox apply "+box+" --adopt") {
		t.Fatalf("first apply did not stop at the gate (%v):\n%s", err, out)
	}
	unchanged("the gated apply")
	logf(t, "PASS first apply stops before any change, with the YAML and --adopt")

	// keelbox up without --adopt asks for it, and changes nothing.
	h := newUpHome(t)
	upArgs := []string{"up", box, "--provider", "ssh", "--host", ip, "--ssh-user", "ubuntu", "--host-key", fpr, "--ssh-key", keyPath,
		"--agents", "none", "--gh=false", "--tailscale=false", "--lan-ports", "all", "--yes"}
	out, code := h.kb(t, upArgs...)
	if code != 2 || !strings.Contains(out, "what the first apply would cut off or replace there (sam, docker)") || !strings.Contains(out, "--adopt") ||
		!strings.Contains(out, "In use there now") {
		t.Fatalf("up without --adopt (exit %d):\n%s", code, out)
	}
	unchanged("up without --adopt")
	logf(t, "PASS up lists what is in use and needs --adopt for the rest, nothing changed")

	out, code = h.kb(t, append(upArgs, "--adopt")...)
	if code != 0 && code != 3 {
		t.Fatalf("up --adopt (exit %d):\n%s", code, out)
	}
	logf(t, "up --adopt: exit %d", code)
	data, err := os.ReadFile(filepath.Join(h.dir, ".config/keelbox/boxes", box+".yaml"))
	if err != nil || !strings.Contains(string(data), "{name: web, port: 8080}") || strings.Contains(string(data), "8081") || strings.Contains(string(data), "8082") {
		t.Errorf("up's config keeps the wrong ports (%v):\n%s", err, data)
	}

	// Reality against the report.
	for port, want := range map[int]bool{8080: true, 8081: true, 8082: false} {
		if got := reachable(ip, port); got != want {
			t.Errorf("after adopting: %d reachable=%v, want %v", port, got, want)
		}
	}
	logf(t, "PASS 8080 (kept by host.firewall.allow) and 8081 (UFW's own rule) reachable, 8082 still blocked")
	st := must(t, ip, "admin", "sudo docker inspect -f '{{.Name}} {{.State.Status}} {{.State.StartedAt}}' web scratch")
	if !strings.Contains(st, "/web running") || strings.Contains(st, "/web running "+started) || !strings.Contains(st, "/scratch exited") {
		t.Errorf("containers after the Docker restart:\n%s", st)
	} else {
		logf(t, "PASS Docker restarted: web came back, scratch stayed stopped, as the report said")
	}
	must(t, ip, "admin", "sudo sshd -T | grep -x 'passwordauthentication no' && sudo passwd -S sam && id sam", "passwordauthentication no", "sam P", "(sudo)")
	logf(t, "PASS sam keeps the password and sudo; SSH takes keys only, as the report said")
	must(t, ip, "admin", "sudo ufw status | grep -c '^8081/tcp' && pgrep -u sam -f '^tmux' >/dev/null && echo tmux-up", "tmux-up")
	logf(t, "PASS UFW kept the 8081 rule; sam's tmux server still runs")
	if out := must(t, ip, "admin", "sudo -u agent docker -H unix:///var/run/docker.sock ps 2>&1; echo end"); !strings.Contains(out, "permission denied") || strings.Contains(out, "web") {
		t.Errorf("the agent reaches the system daemon's containers:\n%s", out)
	} else {
		logf(t, "PASS the agent cannot reach the system daemon or its containers")
	}

	// The box is keelbox's now: the report no longer gates.
	out, code = h.kb(t, "plan", box, "--impact", "--json")
	if r := parseImpact(t, out); code != 0 || r.FirstApply {
		t.Errorf("after the first apply (exit %d): first_apply %v", code, r.FirstApply)
	}
	if out, code := h.kb(t, "plan", box); code != 0 || !strings.Contains(out, "No changes.") {
		t.Errorf("plan after adopting (exit %d):\n%s", code, out)
	}
	logf(t, "PASS adopted box converged; the gate no longer applies")
}
