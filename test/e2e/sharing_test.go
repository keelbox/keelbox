//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/manifest"
)

// A machine shared with home services (ADRs 0046 to 0048). A VM runs, in
// the system Docker, a Jellyfin stand-in published on 8096/tcp and
// 7359/udp and a UniFi stand-in on the host network (8080/tcp, 8443/tcp,
// 3478/udp, 10001/udp, and 5514/udp, which the preset leaves out), with
// images named as the vendors' so keelbox recognises them, and an unknown
// web server on 8082. Then:
//
//   - plan --impact names UniFi and Jellyfin, each one item kept with
//     {service: NAME};
//   - keelbox up --lan-ports unifi,jellyfin keeps both and, since other
//     services run there, limits the agent to 75% of CPU and memory;
//   - every preset port and protocol answers from the LAN (this host, on
//     the VM's bridge); 8082/tcp and 5514/udp stay blocked;
//   - CPU stress as the agent stays under its quota, and a root workload
//     beside it gets the larger share;
//   - memory stress as the agent, in its tmux, is stopped at the ceiling
//     while a root process keeps its memory, tmux and its other session
//     keep running, and doctor reports it.
//
//	KEELBOX_E2E_TARGET=lxd go test -tags e2e -run TestSharedMachine -v ./test/e2e/
//
// The VM is named KEELBOX_E2E_SHARE_PREFIX (default kb-share-) plus a
// number, and deleted directly.

var sharePrefix = envOr("KEELBOX_E2E_SHARE_PREFIX", "kb-share-")

// standIn serves "ok" over HTTP on each tcp:PORT and echoes datagrams on
// each udp:PORT.
const standIn = `import http.server, socket, sys, threading
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
def tcp(p):
    http.server.ThreadingHTTPServer(("", p), H).serve_forever()
def udp(p):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind(("", p))
    while True:
        d, a = s.recvfrom(512); s.sendto(d, a)
for spec in sys.argv[1:]:
    proto, p = spec.split(":")
    threading.Thread(target=tcp if proto == "tcp" else udp, args=(int(p),)).start()
`

// udpEcho reports whether a datagram to ip:port comes back.
func udpEcho(ip string, port int) bool {
	c, err := net.Dial("udp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	defer c.Close()
	buf := make([]byte, 16)
	for try := 0; try < 3; try++ {
		_, _ = c.Write([]byte("keelbox"))
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, err := c.Read(buf); err == nil && string(buf[:n]) == "keelbox" {
			return true
		}
	}
	return false
}

type probe struct {
	proto string
	port  int
	open  bool
}

func probeAll(t *testing.T, ip, when string, ps []probe) {
	t.Helper()
	var got []string
	for _, p := range ps {
		ok := false
		if p.proto == "tcp" {
			ok = reachable(ip, p.port)
		} else {
			ok = udpEcho(ip, p.port)
		}
		if ok != p.open {
			t.Errorf("%s: %d/%s reachable=%v, want %v", when, p.port, p.proto, ok, p.open)
		}
		got = append(got, fmt.Sprintf("%d/%s=%v", p.port, p.proto, ok))
	}
	logf(t, "%s: %s", when, strings.Join(got, " "))
}

func sharedMachineSetup(t *testing.T, ip string) {
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	must(t, ip, "ubuntu", `set -e
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
printf 'Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: %s\nComponents: stable\nSigned-By: /etc/apt/keyrings/docker.asc\n' "$(. /etc/os-release && echo "$VERSION_CODENAME")" | sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null
sudo apt-get update -qq
v=$(apt-cache madison docker-ce | awk '{print $3}' | grep '^5:`+m.Docker.Series+`\.' | head -1)
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker-ce=$v docker-ce-cli=$v containerd.io >/dev/null
echo docker-ready`, "docker-ready")
	must(t, ip, "ubuntu", "sudo tee /opt/standin.py >/dev/null <<'EOF'\n"+standIn+"EOF\n"+`set -e
sudo docker pull -q python:3-alpine >/dev/null
sudo docker tag python:3-alpine jellyfin/jellyfin:standin
sudo docker tag python:3-alpine jacobalberty/unifi:standin
sudo docker run -d -q --name jellyfin --restart unless-stopped -p 8096:8096 -p 7359:7359/udp -v /opt/standin.py:/standin.py:ro jellyfin/jellyfin:standin python3 /standin.py tcp:8096 udp:7359 >/dev/null
sudo docker run -d -q --name unifi --restart unless-stopped --network host -v /opt/standin.py:/standin.py:ro jacobalberty/unifi:standin python3 /standin.py tcp:8080 tcp:8443 udp:3478 udp:10001 udp:5514 >/dev/null
sudo systemd-run -q --unit other-web -p DynamicUser=yes python3 -m http.server 8082
sleep 3; echo in-use`, "in-use")
}

// cpuUsec reads a cgroup's CPU time so far.
func cpuUsec(t *testing.T, ip, cgroup string) int64 {
	t.Helper()
	out := must(t, ip, "admin", "awk '$1==\"usage_usec\"{print $2}' "+cgroup+"/cpu.stat")
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		t.Fatalf("%s/cpu.stat: %q", cgroup, out)
	}
	return n
}

// cpuShares measures, over d, each cgroup's share of all the VM's CPUs.
func cpuShares(t *testing.T, ip string, cpus int, d time.Duration, cgroups ...string) []float64 {
	t.Helper()
	before := make([]int64, len(cgroups))
	for i, c := range cgroups {
		before[i] = cpuUsec(t, ip, c)
	}
	start := time.Now()
	time.Sleep(d)
	el := time.Since(start)
	out := make([]float64, len(cgroups))
	for i, c := range cgroups {
		out[i] = float64(cpuUsec(t, ip, c)-before[i]) / float64(el.Microseconds()*int64(cpus)) * 100
	}
	return out
}

func TestSharedMachine(t *testing.T) {
	if os.Getenv("KEELBOX_E2E_TARGET") != "lxd" {
		t.Skip("set KEELBOX_E2E_TARGET=lxd")
	}
	pub := ensureKey(t)
	build(t)
	defer func() {
		if p := os.Getenv("KEELBOX_E2E_LOG"); p != "" {
			_ = os.WriteFile(p, transcript.Bytes(), 0o600)
		}
	}()
	box := fmt.Sprintf("%s%d", sharePrefix, time.Now().Unix()%1000000)
	ip := launch(t, box, "#cloud-config\nssh_authorized_keys:\n  - "+pub+"\n")
	defer deleteVM(t, box)
	for i := 0; i < 60; i++ {
		if _, err := sshRun(t, ip, "ubuntu", nil, "cloud-init status --wait >/dev/null 2>&1; true"); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	sharedMachineSetup(t, ip)
	ports := []probe{
		{"tcp", 8080, true}, {"tcp", 8443, true}, {"udp", 3478, true}, {"udp", 10001, true}, // unifi, host network: UFW
		{"tcp", 8096, true}, {"udp", 7359, true}, // jellyfin, published: the Docker guard
		{"tcp", 8082, false}, {"udp", 5514, false}, // not kept
	}
	before := append([]probe(nil), ports...)
	for i := range before {
		before[i].open = true
	}
	probeAll(t, ip, "before keelbox", before)
	fpr := strings.Fields(lxc(t, "exec", box, "--", "ssh-keygen", "-l", "-E", "sha256", "-f", "/etc/ssh/ssh_host_ed25519_key.pub"))[1]

	// The report names the services.
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
	out, err := kb(t, "plan", cfg, "--ssh-user", "ubuntu", "--impact", "--json")
	if err != nil {
		t.Fatalf("plan --impact: %v\n%s", err, out)
	}
	r := parseImpact(t, out)
	for subject, want := range map[string]string{
		"unifi":    "UniFi Network Application (container unifi, host network) listens on 8080/tcp, 8443/tcp, 3478/udp and 10001/udp now",
		"jellyfin": "Jellyfin (container jellyfin) publishes 8096/tcp and 7359/udp to the network now",
		"5514/udp": "UniFi Network Application (container unifi, host network) listens on the network now",
		"8082/tcp": "a Python app (python3) listens on the network now",
	} {
		if d, gate, ok := r.find("cuts", subject); !ok || !gate || !strings.Contains(d, want) {
			t.Errorf("impact cuts %s: %q (gate %v), want %q", subject, d, gate, want)
		}
	}
	if d, _, _ := r.find("cuts", "unifi"); !strings.Contains(d, "{service: unifi} keeps it") || !strings.Contains(d, "10001, 1900/udp are how devices and apps find it on the LAN") {
		t.Errorf("unifi hint: %q", d)
	}
	text, _ := kb(t, "plan", cfg, "--ssh-user", "ubuntu", "--impact")
	fmt.Fprintf(&transcript, "--- plan --impact ---\n%s\n", text)
	logf(t, "PASS plan --impact names unifi and jellyfin, kept with {service: ...}; gated %s", r.gated())

	// up keeps the two services and proposes limits.
	h := newUpHome(t)
	out, code := h.kb(t, "up", box, "--provider", "ssh", "--host", ip, "--ssh-user", "ubuntu", "--host-key", fpr, "--ssh-key", keyPath,
		"--agents", "claude-code", "--gh=false", "--tailscale=false", "--lan-ports", "unifi,jellyfin", "--skip-sign-in", "--adopt", "--yes")
	if code != 0 && code != 3 {
		t.Fatalf("up (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "the agent gets at most 75% of CPU and memory") || !strings.Contains(out, "open to the LAN: unifi (") {
		t.Errorf("up's summary:\n%s", out)
	}
	data, _ := os.ReadFile(filepath.Join(h.dir, ".config/keelbox/boxes", box+".yaml"))
	for _, want := range []string{"{service: unifi}", "{service: jellyfin}", "cpu: 75%", "memory: 75%"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("up's config lacks %q:\n%s", want, data)
		}
	}
	logf(t, "PASS up kept unifi and jellyfin as services and limited the agent to 75%%")

	probeAll(t, ip, "after keelbox", ports)

	// The limits in force.
	uid := strings.TrimSpace(must(t, ip, "admin", "id -u agent"))
	slice := "/sys/fs/cgroup/user.slice/user-" + uid + ".slice"
	cpus, _ := strconv.Atoi(strings.TrimSpace(must(t, ip, "admin", "nproc")))
	// The slow-down mark is on the user manager's app.slice, where the
	// agent's services run, not on the account's slice, where it would slow
	// the user manager itself (ADR 0052).
	app := slice + "/user@" + uid + ".service/app.slice"
	lim := must(t, ip, "admin", "cd "+slice+" && cat cpu.max cpu.weight memory.high memory.max memory.swap.max /sys/fs/cgroup/user.slice/cpu.weight "+app+"/memory.high; awk '/MemTotal/{print $2*1024}' /proc/meminfo")
	f := strings.Fields(lim)
	if len(f) != 9 || f[0] != strconv.Itoa(75*cpus*1000) || f[1] != "100000" || f[2] != "50" || f[3] != "max" || f[4] == "max" || f[6] != "50" || f[7] == "max" {
		t.Fatalf("agent slice limits:\n%s", lim)
	}
	ram, _ := strconv.ParseFloat(f[8], 64)
	maxB, _ := strconv.ParseFloat(f[4], 64)
	highB, _ := strconv.ParseFloat(f[7], 64)
	if share := maxB / ram * 100; share < 73 || share > 77 {
		t.Errorf("memory.max is %.1f%% of RAM", share)
	}
	if share := highB / ram * 100; share < 66 || share > 69 {
		t.Errorf("app.slice memory.high is %.1f%% of RAM, want 67.5%%", share)
	}
	logf(t, "PASS slice limits live after apply: cpu.max %s %s, weight %s, memory.max %s, swap.max %s, no memory.high; app.slice memory.high %s; user.slice weight %s", f[0], f[1], f[2], f[4], f[5], f[7], f[6])
	must(t, ip, "admin", "sudo systemctl --user -M agent@ show -p OOMPolicy keelbox-tmux.service; sudo systemctl --user -M agent@ is-active keelbox-tmux.service", "OOMPolicy=continue", "active")

	tmux := "sudo -u agent tmux "
	// The agent CLI runs as the agent: its process is in the agent's slice
	// like everything else. Typed into the session's login shell, as the
	// agent would, so the managed .bashrc PATH finds the vendor's install.
	must(t, ip, "admin", tmux+"new-session -d -s cli && sleep 2 && "+tmux+"send-keys -t cli claude Enter && sleep 8 && echo started", "started")
	cli := must(t, ip, "admin", `for p in $(pgrep -u agent); do e=$(sudo readlink /proc/$p/exe); case $e in /home/agent/.local/share/claude/*) echo "$e"; sudo cat /proc/$p/cgroup;; esac; done; true`)
	if !strings.Contains(cli, "/home/agent/.local/share/claude/") || !strings.Contains(cli, "/user.slice/user-"+uid+".slice/") {
		t.Errorf("the agent's claude does not run in the agent's slice:\n%s", cli)
	} else {
		logf(t, "PASS claude from ~/.local/share/claude runs in user-%s.slice", uid)
	}
	must(t, ip, "admin", tmux+"kill-session -t cli; true")
	// CPU: the agent alone stays under 75%; beside a root workload it
	// yields.
	// New sessions in the agent's running tmux server, so their processes
	// run in keelbox-tmux.service, as an agent's would.
	must(t, ip, "admin", tmux+"new-session -d -s burn1 'while :; do :; done' && "+tmux+"new-session -d -s burn2 'while :; do :; done' && echo burning", "burning")
	time.Sleep(3 * time.Second)
	alone := cpuShares(t, ip, cpus, 20*time.Second, slice)[0]
	if alone < 60 || alone > 80 {
		t.Errorf("agent alone used %.0f%% of the CPUs, want about 75%%", alone)
	}
	must(t, ip, "admin", "sudo systemd-run -q --unit kb-root-load sh -c 'for i in $(seq "+strconv.Itoa(cpus)+"); do (while :; do :; done) & done; wait' && echo loaded", "loaded")
	time.Sleep(3 * time.Second)
	sh := cpuShares(t, ip, cpus, 20*time.Second, slice, "/sys/fs/cgroup/system.slice/kb-root-load.service")
	if sh[1] < 55 || sh[0] > 45 {
		t.Errorf("under contention: agent %.0f%%, root %.0f%%; want root at least 55%%", sh[0], sh[1])
	}
	must(t, ip, "admin", "sudo systemctl stop kb-root-load; "+tmux+"kill-session -t burn1; "+tmux+"kill-session -t burn2; echo stopped", "stopped")
	logf(t, "PASS CPU: agent alone %.0f%% (quota 75%%); with a root workload agent %.0f%%, root %.0f%%", alone, sh[0], sh[1])

	// Memory: a root process holds 256 MB; the agent's hog in tmux grows
	// until the kernel stops it at the ceiling.
	must(t, ip, "admin", `sudo systemd-run -q --unit kb-root-mem python3 -c 'import time; b = b"r" * (256 << 20); time.sleep(3600)' && echo held`, "held")
	must(t, ip, "admin", "printf 'import time\\nchunks = []\\nwhile True:\\n    chunks.append(b\"x\" * (64 << 20)); time.sleep(0.05)\\n' | sudo tee /opt/hog.py >/dev/null && "+
		tmux+"new-session -d -s keep 'sleep 3600' && "+tmux+"new-session -d -s hog 'python3 /opt/hog.py' && echo hogging", "hogging")
	// Past MemoryHigh the kernel throttles the hog and pushes its pages to
	// swap, so it reaches the ceiling (RAM and its share of swap) slowly:
	// about five minutes on a 2 GB VM with 2 GB of swap.
	hogStart := time.Now()
	killed := false
	// While the hog is held back, the agent's user manager still answers:
	// it is not under the slow-down mark.
	var slowest time.Duration
	asks := 0
	for i := 0; i < 180 && !killed; i++ {
		time.Sleep(5 * time.Second)
		ev := must(t, ip, "admin", "awk '$1==\"oom_kill\"{print $2}' "+slice+"/memory.events")
		killed = strings.TrimSpace(ev) != "0"
		if killed {
			break
		}
		if out, err := sshRun(t, ip, "admin", nil, "date +%s%N; timeout 60 sudo systemctl --user -M agent@ is-active keelbox-tmux.service; date +%s%N"); err != nil || !strings.Contains(out, "active") {
			t.Errorf("systemctl --user -M agent@ during the memory stress: %v\n%s", err, out)
		} else if l := strings.Fields(out); len(l) == 3 {
			a, _ := strconv.ParseInt(l[0], 10, 64)
			b, _ := strconv.ParseInt(l[2], 10, 64)
			if d := time.Duration(b - a); d > slowest {
				slowest = d
			}
			asks++
		}
	}
	if !killed {
		t.Fatal("the agent's memory hog was not stopped in 15 minutes")
	}
	hogTime := time.Since(hogStart).Round(time.Second)
	if asks == 0 || slowest > 5*time.Second {
		t.Errorf("the agent's user manager answered %d times during the memory stress, slowest in %s; want each within 5s", asks, slowest.Round(10*time.Millisecond))
	} else {
		logf(t, "PASS the agent's user manager answered systemctl --user is-active %d times during the memory stress, slowest in %s", asks, slowest.Round(10*time.Millisecond))
	}
	highEvents := strings.TrimSpace(must(t, ip, "admin", "awk '$1==\"high\"{print $2}' "+slice+"/memory.events"))
	st := must(t, ip, "admin", "systemctl is-active kb-root-mem; sudo systemctl --user -M agent@ is-active keelbox-tmux.service; "+tmux+"list-sessions -F '#S'; awk '$1==\"oom_kill\"{print \"root-oom\", $2}' /sys/fs/cgroup/system.slice/memory.events; true")
	for _, want := range []string{"active\nactive", "keep", "root-oom 0"} {
		if !strings.Contains(st, want) {
			t.Errorf("after the agent's OOM kill, lacks %q:\n%s", want, st)
		}
	}
	logf(t, "PASS memory: the agent's hog was stopped at its ceiling after %s (%s memory.high events); the root process, tmux and the agent's other session kept running", hogTime, highEvents)

	out, _ = h.kb(t, "doctor", box)
	fmt.Fprintf(&transcript, "--- doctor ---\n%s\n", out)
	if !strings.Contains(out, fmt.Sprintf("CPU %d%% of %d (75%% of all)", 75*cpus, cpus)) || !strings.Contains(out, "of the agent's processes because memory ran out") {
		t.Errorf("doctor:\n%s", out)
	}
	out, _ = h.kb(t, "status", box)
	fmt.Fprintf(&transcript, "--- status ---\n%s\n", out)
	if !strings.Contains(out, "(slows at ") {
		t.Errorf("status:\n%s", out)
	}
	logf(t, "PASS doctor shows the limits and the OOM kill; status shows the agent's use")
	must(t, ip, "admin", "sudo systemctl stop kb-root-mem; true")
}
