//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// upgradeFrom applies the box first with keelbox built from an older commit
// (KEELBOX_E2E_UPGRADE_FROM), then with this tree, as an owner upgrading
// would. The old binary gets the config without keys it does not know.
func upgradeFrom(t *testing.T, commit, cfg, bootstrap, host, box string) {
	t.Helper()
	dir := filepath.Join(workDir, "old-src")
	if out, err := exec.Command("git", "-C", "../..", "worktree", "add", "--detach", dir, commit).CombinedOutput(); err != nil {
		t.Fatalf("worktree %s: %v\n%s", commit, err, out)
	}
	defer func() { _ = exec.Command("git", "-C", "../..", "worktree", "remove", "--force", dir).Run() }()
	old := filepath.Join(workDir, "keelbox-old")
	b := exec.Command("go", "build", "-o", old, "./cmd/keelbox")
	b.Dir = dir
	b.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", commit, err, out)
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldCfg := filepath.Join(workDir, "old.yaml")
	stripped := data
	// A keelbox older than target.ssh.host_key cannot read it; one that
	// knows it needs it to trust the fresh VM without a terminal.
	if exec.Command("git", "-C", dir, "grep", "-q", `yaml:"host_key`, "--", "internal/config").Run() != nil {
		stripped = regexp.MustCompile(`(?m)^\s*host_key:.*\n`).ReplaceAll(data, nil)
	}
	// Keys newer than the old keelbox: it parses strictly.
	if i := bytes.Index(stripped, []byte(newKeysMarker)); i >= 0 {
		stripped = stripped[:i]
	}
	if err := os.WriteFile(oldCfg, stripped, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, old, "apply", oldCfg, "--ssh-user", bootstrap); err != nil {
		t.Fatalf("apply with keelbox at %s: %v\n%s", commit, err, out)
	}
	logf(t, "PASS apply with keelbox at %s", commit)

	// A preview the old keelbox published, on a stand-in tailnet, so the
	// upgrade has a Caddy config of the old keelbox's to rewrite.
	if !haveTailnetKey() {
		must(t, host, "agent", `mkdir -p ~/e2e-www && echo kb-preview-ok > ~/e2e-www/index.html && `+
			`(setsid python3 -m http.server 3000 --bind 127.0.0.1 --directory ~/e2e-www >/dev/null 2>&1 < /dev/null &) && sleep 1`)
		_, _, cleanup := previewTailnet(t, host, box)
		must(t, host, "agent", "keelbox expose 3000 --name web", ":8401/")
		before := must(t, host, "admin", "sudo cat /var/lib/keelbox/previews/caddy.json")
		defer func() {
			must(t, host, "agent", "keelbox expose --remove web")
			_, _ = sshRun(t, host, "agent", nil, "pkill -f 'http.server 3000'; rm -rf ~/e2e-www")
			cleanup()
		}()
		defer func() {
			after := must(t, host, "admin", "sudo cat /var/lib/keelbox/previews/caddy.json")
			switch {
			case !strings.Contains(after, "X-Forwarded-Host") || !strings.Contains(after, `"localhost:3000"`):
				t.Errorf("the upgrade apply left the old Caddy config:\n%s", after)
			case after == before:
				// The old keelbox already wrote this config.
				logf(t, "PASS upgrade kept the Caddy config of a running preview, already current (Host localhost:3000, X-Forwarded-Host)")
			default:
				logf(t, "PASS upgrade rewrote the Caddy config of a running preview (Host localhost:3000, X-Forwarded-Host)")
			}
		}()
	}
	out, err := kb(t, "apply", cfg)
	if err != nil {
		t.Fatalf("upgrade apply: %v\n%s", err, out)
	}
	// What this tree changes on a box that older keelbox set up: the run
	// log of a detached apply, Remote Control's output off the journal, the
	// bwrap profile where Ubuntu lacks one (24.04).
	must(t, host, "admin", `id=$(cat /var/lib/keelbox/runs/current) && tail -n 1 /var/lib/keelbox/runs/$id.jsonl`, `"result":"ok"`)
	must(t, host, "agent", "cat ~/.config/systemd/user/keelbox-claude-remote-control.service", "StandardOutput=null")
	if strings.Contains(must(t, host, "admin", ". /etc/os-release && echo $VERSION_ID"), "24.04") {
		must(t, host, "admin", "test -e /etc/apparmor.d/keelbox-bwrap && sudo aa-status", "bwrap")
		// The copy older keelbox wrote is replaced by Ubuntu's own.
		must(t, host, "admin", "head -n 1 /etc/apparmor.d/keelbox-bwrap", "Ubuntu's own bwrap-userns-restrict profile")
	}
	logf(t, "PASS upgrade: run log ended ok, Remote Control unit updated, bwrap profile where needed")
	want := []string{"keelbox-binary"}
	// From a commit without rootless Docker for the agent, these change too.
	if exec.Command("git", "-C", dir, "grep", "-q", "agent-not-in-docker-group", "--", "internal").Run() != nil {
		want = append(want, "agent-not-in-docker-group", "agent-docker-enabled")
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("upgrade apply did not change %s:\n%s", w, out)
		}
	}
	logf(t, "PASS upgrade from %s to this tree", commit)
}

// run is kb for another binary.
func run(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()
	saved := keelbox
	keelbox = bin
	defer func() { keelbox = saved }()
	return kb(t, args...)
}

// subuidProbe is a shell command for the agent that opens a TCP connection
// from one of its subordinate uids while staying on the box's own network:
// rootlesskit maps /etc/subuid's range into a user namespace without a
// network namespace, and setpriv switches to uid 1 there. It prints
// subuid=1 from inside, then kb-open or kb-closed, or no-rootlesskit when
// the box lacks either tool.
func subuidProbe(hostPort string) string {
	return `if command -v rootlesskit >/dev/null && command -v setpriv >/dev/null; then ` +
		`rootlesskit --net=host sh -c 'setpriv --reuid=1 --regid=1 --clear-groups sh -c "echo subuid=\$(id -u); ` +
		`timeout 5 bash -c \"exec 3<>/dev/tcp/` + hostPort + `\" 2>&1 && echo kb-open || echo kb-closed"' 2>&1; ` +
		`else echo no-rootlesskit; fi`
}

// isolationChecks cover what the box enforces between the agent, the admin
// and the network. Each claim in the primer has a check here.
func isolationChecks(t *testing.T, host, bootstrap string) {
	t.Helper()
	expect := func(name, user, remote string, want ...string) {
		t.Helper()
		out, _ := sshRun(t, host, user, nil, remote)
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: output lacks %q:\n%s", name, w, out)
				return
			}
		}
		logf(t, "PASS %s: %s", name, firstLine(out))
	}
	expect("agent docker is rootless", "agent", "docker info --format '{{.SecurityOptions}}'", "rootless")
	expect("agent cannot use the system daemon", "agent", "docker -H unix:///var/run/docker.sock ps 2>&1", "permission denied")
	if out, _ := sshRun(t, host, "agent", nil, "id -nG"); strings.Contains(" "+out+" ", " docker ") {
		t.Errorf("agent is in the docker group: %s", out)
	}

	ssh := "ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
	expect("agent cannot ssh to the box (loopback)", "agent", ssh+" admin@127.0.0.1 true 2>&1; echo rc=$?", "Connection refused", "rc=255")
	expect("agent cannot ssh to the box (its address)", "agent", ssh+" admin@"+host+" true 2>&1; echo rc=$?", "Connection refused", "rc=255")
	expect("agent container cannot reach box SSH", "agent",
		"docker run --rm busybox sh -c 'nc -w 5 "+host+" 22 </dev/null >/dev/null 2>&1 && echo open || echo closed'", "closed")
	expect("agent may reach SSH elsewhere (github.com:22)", "agent",
		"timeout 10 bash -c '</dev/tcp/github.com/22' && echo reachable", "reachable")
	expect("ssh-agent forwarding off for the agent", "admin", "sudo sshd -T -C user=agent,host=localhost,addr=127.0.0.1 | grep -i allowagentforwarding", "allowagentforwarding no")
	// The agent's subordinate uids are the agent's too. Nothing may listen
	// at the metadata address (an LXD VM), where that half shows little.
	for _, c := range [][2]string{{"SSH on the box", "127.0.0.1/22"}, {"the metadata address", "169.254.169.254/80"}} {
		name := "agent's subordinate uid cannot reach " + c[0]
		out, _ := sshRun(t, host, "agent", nil, subuidProbe(c[1]))
		switch {
		case strings.Contains(out, "no-rootlesskit"):
			logf(t, "SKIP %s: no rootlesskit or setpriv on the box", name)
		case !strings.Contains(out, "subuid=1") || !strings.Contains(out, "kb-closed") || strings.Contains(out, "kb-open"):
			t.Errorf("%s: want subuid=1 and kb-closed:\n%s", name, out)
		default:
			logf(t, "PASS %s (rootlesskit --net=host, setpriv --reuid=1)", name)
		}
	}
	everyAccountChecks(t, host)

	// Published ports: rootless (agent) and rootful (root) containers on all
	// interfaces answer on the box and not from this machine.
	ports := map[string]string{
		"rootless": "docker run -d --name e2e-port -p 18082:8080 busybox httpd -f -p 8080",
		"rootful":  "sudo docker run -d --name e2e-port -p 18083:8080 busybox httpd -f -p 8080",
	}
	users := map[string]string{"rootless": "agent", "rootful": "admin"}
	port := map[string]string{"rootless": "18082", "rootful": "18083"}
	for _, kind := range []string{"rootless", "rootful"} {
		if out, err := sshRun(t, host, users[kind], nil, ports[kind]+" >/dev/null && sleep 2 && timeout 5 bash -c '</dev/tcp/127.0.0.1/"+port[kind]+"' && echo local-ok"); err != nil || !strings.Contains(out, "local-ok") {
			t.Errorf("%s published port not reachable on the box: %v\n%s", kind, err, out)
			continue
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port[kind]), 5*time.Second)
		if err == nil {
			_ = c.Close()
			t.Errorf("%s published port %s reachable from outside the box", kind, port[kind])
		} else {
			logf(t, "PASS %s published port: reachable on the box, not from outside (%v)", kind, err)
		}
		docker := "docker"
		if kind == "rootful" {
			docker = "sudo docker"
		}
		_, _ = sshRun(t, host, users[kind], nil, docker+" rm -f e2e-port")
	}

	if bootstrap != "root" {
		if out, err := sshRun(t, host, bootstrap, nil, "true"); err == nil {
			t.Errorf("cloud default user %s can still log in", bootstrap)
		} else {
			logf(t, "PASS cloud default user %s locked: %s", bootstrap, firstLine(out))
		}
	}
	// root is never expired: cron refuses an expired root's jobs.
	if out, err := sshRun(t, host, "admin", nil, "sudo chage -l root | grep '^Account expires'"); err != nil || !strings.Contains(out, "never") {
		t.Errorf("root's account expires: %v %s", err, out)
	} else {
		logf(t, "PASS root not expired")
	}
}

// everyAccountChecks cover the network limits as they apply to every
// account but root, not the agent alone (ADR 0076): an account that is
// neither (nobody) is refused the box's SSH port and the metadata address,
// root is not, and what the box itself needs from link-local services
// still works with the table loaded: names, time and apt.
func everyAccountChecks(t *testing.T, host string) {
	t.Helper()
	// probe opens a TCP connection and prints bash's own error, then
	// kb-open or kb-closed.
	probe := func(sudo, hostPort string) string {
		return sudo + ` timeout 5 bash -c 'exec 3<>/dev/tcp/` + hostPort + `' 2>&1 && echo kb-open || echo kb-closed`
	}
	admin := func(remote string) string {
		t.Helper()
		out, _ := sshRun(t, host, "admin", nil, remote)
		return out
	}
	listing := admin("sudo nft list table inet keelbox_agent")
	if !strings.Contains(listing, "chain linklocal") || !strings.Contains(listing, "fib daddr type local") {
		t.Errorf("the loaded table is not the one that limits every account but root:\n%s", listing)
	}
	logf(t, "the loaded table as nft lists it:\n%s", listing)

	for _, c := range []struct{ name, hostPort, also string }{
		{"SSH on the box (loopback)", "127.0.0.1/22", "refused"},
		{"SSH on the box (its address)", host + "/22", "refused"},
		// Nothing may listen at the metadata address (an LXD VM); the
		// reject answers before anything there could.
		{"the metadata address", "169.254.169.254/80", ""},
	} {
		name := "an account that is neither root nor the agent (nobody) cannot reach " + c.name
		out := admin(probe("sudo -u nobody", c.hostPort))
		if !strings.Contains(out, "kb-closed") || strings.Contains(out, "kb-open") || !strings.Contains(out, c.also) {
			t.Errorf("%s: want kb-closed:\n%s", name, out)
			continue
		}
		logf(t, "PASS %s: %s", name, firstLine(out))
	}
	if out := admin(probe("sudo", "127.0.0.1/22")); !strings.Contains(out, "kb-open") {
		t.Errorf("root cannot reach SSH on the box:\n%s", out)
	} else {
		logf(t, "PASS root reaches SSH on the box (loopback)")
	}
	// Root keeps the metadata service, which cloud-init and the clouds'
	// guest agents use. Which cloud this is: what cloud-init found, and the
	// machine's vendor as the hypervisor states it.
	what := strings.ToLower(strings.Join(strings.Fields(admin("cloud-id 2>/dev/null; cat /sys/class/dmi/id/sys_vendor 2>/dev/null")), " "))
	cloud := ""
	for _, c := range []string{"aws", "amazon", "gce", "google", "hetzner", "digitalocean"} {
		if strings.Contains(what, c) {
			cloud = c
			break
		}
	}
	if cloud == "" {
		logf(t, "SKIP root reaches the metadata address: this machine (%s) is on none of the clouds whose metadata service this test knows", what)
	} else if out := admin(probe("sudo", "169.254.169.254/80")); !strings.Contains(out, "kb-open") {
		t.Errorf("root cannot reach the metadata address (%s):\n%s", what, out)
	} else {
		logf(t, "PASS root reaches the metadata address (%s)", what)
	}

	// Names, with the resolver's cache emptied first so the answers come
	// from the resolver the network gave the box (link-local on Google
	// Cloud), through systemd-resolved where the box uses it.
	resolved := strings.Contains(admin("systemctl is-active --quiet systemd-resolved && echo kb-resolved"), "kb-resolved")
	if resolved {
		logf(t, "systemd-resolved's servers: %s", strings.Join(strings.Fields(admin("sudo resolvectl flush-caches; resolvectl dns")), " "))
	}
	for _, user := range []string{"agent", "admin"} {
		cmd, who := "getent hosts example.com", user
		if user == "admin" {
			cmd, who = "sudo "+cmd, "root"
		}
		if out, err := sshRun(t, host, user, nil, cmd); err != nil || !strings.Contains(out, "example.com") {
			t.Errorf("%s cannot resolve example.com: %v\n%s", who, err, out)
		} else {
			logf(t, "PASS %s resolves names: %s", who, firstLine(out))
		}
	}
	if resolved {
		if out, err := sshRun(t, host, "admin", nil, "sudo resolvectl flush-caches; resolvectl query example.org"); err != nil || !strings.Contains(out, "example.org:") {
			t.Errorf("resolvectl query example.org: %v\n%s", err, out)
		} else {
			logf(t, "PASS systemd-resolved answers a name it had not cached: %s", firstLine(out))
		}
	} else {
		logf(t, "SKIP resolvectl query: systemd-resolved is not in use on this box")
	}

	// Time. The clock stays synchronised, and the time service gets an
	// answer from its source after a restart, so with the table loaded:
	// that source is link-local on AWS and Google Cloud.
	out := admin(`if systemctl is-active --quiet systemd-timesyncd; then echo kb-time=timesyncd; sudo systemctl restart systemd-timesyncd; ` +
		`for i in $(seq 1 45); do timedatectl timesync-status 2>/dev/null | grep -q 'Packet count: [1-9]' && break; sleep 2; done; ` +
		`timedatectl timesync-status 2>&1 | grep -E 'Server:|Packet count:'; ` +
		`elif systemctl is-active --quiet chrony || systemctl is-active --quiet chronyd; then echo kb-time=chrony; ` +
		`sudo chronyc waitsync 30 0 0 2 >/dev/null 2>&1; sudo chronyc -n sources 2>&1; ` +
		`else echo kb-time=none; fi; timedatectl show -p NTPSynchronized`)
	answered := strings.Contains(out, "Packet count:") // timesyncd: a reply since the restart
	if strings.Contains(out, "kb-time=chrony") {
		answered = strings.Contains(out, "\n^*") // chrony: a source it is synchronised to now
	}
	switch {
	case strings.Contains(out, "kb-time=none"):
		logf(t, "SKIP the clock is synchronised: neither systemd-timesyncd nor chrony runs on this box")
	case !strings.Contains(out, "NTPSynchronized=yes") || !answered:
		t.Errorf("the clock is not synchronised, or the time service got no answer from its source with the table loaded:\n%s", out)
	default:
		logf(t, "PASS the clock is synchronised and its source answers: %s", strings.Join(strings.Fields(out), " "))
	}

	// apt's downloads run as the account _apt, which is neither root nor
	// excepted: its names go through the box's resolver.
	if out, err := sshRun(t, host, "admin", nil, "sudo apt-get update 2>&1"); err != nil ||
		strings.Contains(out, "Failed to fetch") || strings.Contains(out, "Temporary failure") || strings.Contains(out, "Could not resolve") || strings.Contains(out, "Could not connect") {
		t.Errorf("apt-get update as root: %v\n%s", err, out)
	} else {
		logf(t, "PASS apt-get update as root")
	}
}

// paneOOMCheck fills memory in a tmux pane until the kernel stops the
// process, and looks whether its neighbour in the pane is still there
// (ADR 0081). The pane's systemd unit is capped first, so the box itself
// does not run short. tmux puts each pane in a scope of its own; tmux 3.6
// (Ubuntu 26.04) does so a moment after the pane starts, so the check
// waits for it, and takes the tmux service for a tmux that makes none.
func paneOOMCheck(t *testing.T, host string) {
	t.Helper()
	defer func() {
		_, _ = sshRun(t, host, "agent", nil, `export XDG_RUNTIME_DIR=/run/user/$(id -u); tmux kill-session -t kb-oom 2>/dev/null; `+
			`systemctl --user set-property --runtime keelbox-tmux.service MemoryMax=infinity MemorySwapMax=infinity; true`)
	}()
	out := must(t, host, "agent", `export XDG_RUNTIME_DIR=/run/user/$(id -u); tmux kill-session -t kb-oom 2>/dev/null; tmux new-session -d -s kb-oom; `+
		`p=$(tmux display -p -t kb-oom '#{pane_pid}'); `+
		`for i in $(seq 1 20); do cg=$(sed -n 's/^0:://p' /proc/$p/cgroup); case "$cg" in */tmux-spawn-*.scope) break;; esac; sleep 0.25; done; u=$(basename "$cg"); `+
		`case "$u" in tmux-spawn-*.scope|keelbox-tmux.service) ;; *) echo "the pane is in neither a scope of its own nor the tmux service: $cg"; exit 1;; esac; `+
		`systemctl --user set-property --runtime "$u" MemoryMax=150M MemorySwapMax=0; `+
		`tmux send-keys -t kb-oom 'sleep 613 & python3 -c "a=[b\"x\"*(10<<20) for i in range(4096)]"' Enter; `+
		`ev=/sys/fs/cgroup$cg/memory.events; `+
		`for i in $(seq 1 40); do grep -q '^oom_kill [1-9]' "$ev" && break; sleep 0.5; done; sleep 2; `+
		`echo "unit=$u policy=$(systemctl --user show -p OOMPolicy --value "$u") $(grep '^oom_kill ' "$ev")"; `+
		`pgrep -P "$p" -x sleep >/dev/null && echo neighbour=alive || echo neighbour=gone; tmux has-session -t main && echo main=there || echo main=gone`,
		"policy=continue")
	if !regexp.MustCompile(`oom_kill [1-9]`).MatchString(out) {
		t.Errorf("the kernel stopped nothing in the pane, so the check proved nothing:\n%s", out)
	}
	if !strings.Contains(out, "neighbour=alive") || !strings.Contains(out, "main=there") {
		t.Errorf("a process in the pane was stopped for memory and more went with it:\n%s", out)
	} else {
		logf(t, "PASS a tmux pane and the other sessions keep running when the kernel stops one of the pane's processes for memory (%s)", strings.Fields(out)[0])
	}
}

// terminfoChecks log in as the agent with the TERM names Ghostty (and
// cmux) and kitty send over ssh: tmux starts in a terminal script(1)
// provides and tput moves the cursor, while a name with no entry still
// fails as tmux does. Every account reads the entries (ADR 0094).
func terminfoChecks(t *testing.T, host string) {
	t.Helper()
	must(t, host, "agent", "stat -c '%n %a %U' /etc/terminfo/x /etc/terminfo/x/xterm-ghostty /usr/share/terminfo/x/xterm-kitty",
		"/etc/terminfo/x 755 root", "/etc/terminfo/x/xterm-ghostty 644 root", "/usr/share/terminfo/x/xterm-kitty 644 root")
	tmux := func(term string) (string, error) {
		return sshRun(t, host, "agent", nil, "TERM="+term+" script -qec 'tmux -L kb-term new-session true' /dev/null </dev/null")
	}
	ok := true
	for _, term := range []string{"xterm-ghostty", "xterm-kitty"} {
		if out, err := sshRun(t, host, "agent", nil, "TERM="+term+" tput cup 0 0"); err != nil {
			ok = false
			t.Errorf("TERM=%s: tput failed for the agent: %v\n%s", term, err, out)
		}
		if out, err := tmux(term); err != nil {
			ok = false
			t.Errorf("TERM=%s: the agent's tmux did not start: %v\n%s", term, err, out)
		}
	}
	// The control: the same tmux, with a TERM no entry has.
	if out, err := tmux("xterm-kb-none"); err == nil || !strings.Contains(out, "missing or unsuitable terminal") {
		ok = false
		t.Errorf("tmux with a TERM no entry has did not fail as tmux does, so the check proves nothing: %v\n%s", err, out)
	}
	if !ok {
		return
	}
	logf(t, "PASS over ssh with TERM xterm-ghostty and xterm-kitty, the agent's tmux starts and tput works; the entries are readable by every account")
}

// statusLineChecks read the status line of the agent's session from the
// running tmux server (the one the watch run brought back, so it started
// with this keelbox's config): the box's name on the left, how to leave on
// the right, and the prefix and the user's own settings as they were.
func statusLineChecks(t *testing.T, host, box string) {
	t.Helper()
	// The options as set: the right side has the whole hint for 80 columns
	// and more ("#," is a comma inside a tmux conditional).
	must(t, host, "agent", "tmux show-options -gv status-left; tmux show-options -gv status-right",
		" "+box+" ", "keelbox", "Ctrl-b d  leave#, everything keeps running")
	// As tmux draws it for session main. No client is attached, so there
	// is no width and the right side is the short form.
	drawn := must(t, host, "agent", "tmux display-message -p -t main '#{T:status-left}|#{T:status-right}'", box, "keelbox", "Ctrl-b d  leave")
	if strings.Contains(drawn, "#{") {
		t.Errorf("the status line has a format tmux did not expand:\n%s", drawn)
	}
	must(t, host, "agent", "tmux show-options -gv prefix", "C-b")
	logf(t, "PASS the tmux status line names the box and says how to leave (Ctrl-b d), and the prefix is tmux's own")
	// What the user sets after keelbox's source-file line wins: a server on
	// its own socket reads keelbox's config as ~/.tmux.conf does, then one
	// line of the user's. The plugins are left out, so Continuum in this
	// second server neither saves nor restores the agent's real sessions.
	out, err := sshRun(t, host, "agent", nil, `set -e; grep -v run-shell ~/.config/tmux/keelbox.conf > /tmp/kb-base.conf; `+
		`printf 'source-file -q /tmp/kb-base.conf\nset -g status-right "mine"\n' > /tmp/kb-tmux.conf; `+
		`tmux -L kb-own -f /tmp/kb-tmux.conf new-session -d -s own; tmux -L kb-own show-options -gv status-right; tmux -L kb-own show-options -gv status-left; `+
		`tmux -L kb-own kill-server; rm -f /tmp/kb-tmux.conf /tmp/kb-base.conf`)
	if err != nil || !strings.HasPrefix(out, "mine\n") || !strings.Contains(out, " "+box+" ") {
		t.Errorf("a status-right set after keelbox's config does not replace keelbox's, or took status-left with it: %v\n%s", err, out)
	} else {
		logf(t, "PASS a tmux setting after keelbox's source-file line replaces keelbox's")
	}
}

// welcomeChecks start a session as the agent and read its panes: the first
// pane begins with keelbox's welcome, built from this box (its name, the
// agent without sudo, both agent CLIs, the projects directory, the way out
// and back, the way to root), once; a second window and a shell without a
// terminal get nothing.
func welcomeChecks(t *testing.T, host, box string) {
	t.Helper()
	defer func() { _, _ = sshRun(t, host, "agent", nil, "tmux kill-session -t kb-welcome 2>/dev/null; true") }()
	// Nobody is attached: the session is as wide as it is told to be, here
	// wide enough for every line's whole wording whatever the box is called.
	first := must(t, host, "agent", `tmux kill-session -t kb-welcome 2>/dev/null; tmux new-session -d -s kb-welcome -x 160 -y 30; `+
		`for i in $(seq 1 30); do tmux capture-pane -p -t kb-welcome | grep -q 'Come back' && break; sleep 0.5; done; sleep 1; tmux capture-pane -p -t kb-welcome`,
		box, "you are the agent, an account without sudo", "Ubuntu ",
		"Start an agent   claude    codex",
		"Your work        ~/projects",
		"Leave            Ctrl-b then d: everything here keeps running",
		"Come back        keelbox attach "+box+" --session kb-welcome   (from your computer)",
		"Packages         keelbox install NAME: an Ubuntu package, without sudo",
		"Need root?       from your computer: keelbox ssh "+box+" --admin")
	if n := strings.Count(first, "Come back"); n != 1 {
		t.Errorf("a new session's first pane shows the welcome %d times:\n%s", n, first)
	}
	if lines := strings.Split(strings.TrimRight(first, "\n"), "\n"); len(lines) != 8 || !strings.Contains(lines[7], "$") {
		t.Errorf("the first pane is not seven lines of welcome and a prompt:\n%s", first)
	}
	logf(t, "PASS a new tmux session's first pane starts with the welcome, once, built from this box")

	// A second window, once its shell is at a prompt, and a nested shell
	// in the first pane: nothing more. The mark is gone.
	out, err := sshRun(t, host, "agent", nil, `w=$(tmux new-window -d -t kb-welcome: -P -F '#{window_id}'); `+
		`for i in $(seq 1 30); do tmux capture-pane -p -t "$w" | grep -q '\$' && break; sleep 0.5; done; tmux capture-pane -p -t "$w"; `+
		`tmux send-keys -t kb-welcome:1 bash Enter; sleep 3; echo "first: $(tmux capture-pane -p -t kb-welcome:1 | grep -c 'Come back')"; `+
		`echo "mark: [$(tmux show-options -qv -t kb-welcome @keelbox-welcome)]"`)
	if err != nil || !strings.Contains(out, "$") || strings.Count(out, "Come back") != 0 || strings.Contains(out, "Need root?") || !strings.Contains(out, "first: 1\n") || !strings.Contains(out, "mark: []") {
		t.Errorf("a second window, or a second shell in the first pane, shows the welcome again: %v\n%s", err, out)
	} else {
		logf(t, "PASS a second window and a second shell in the first pane show no welcome")
	}

	// As the agent with no terminal: the command the shell runs (it is in
	// the agent's ~/.bashrc) exits 0 and prints nothing, and a command
	// over ssh, which reads ~/.bashrc up to its interactive guard, gets no
	// welcome either.
	must(t, host, "agent", "grep -c '/usr/local/bin/keelbox welcome --box .*"+box+".* --agent .*agent.* --projects .*projects.* --session ' ~/.bashrc", "1")
	out, err = sshRun(t, host, "agent", nil, "/usr/local/bin/keelbox welcome --box "+box+" --agent agent --projects projects --session main </dev/null; echo \"exit=$?\"")
	if err != nil || out != "exit=0\n" {
		t.Errorf("keelbox welcome as the agent without a terminal: %v\n%q", err, out)
	}
	if out, err := sshRun(t, host, "agent", nil, "true"); err != nil || strings.Contains(out, "Come back") || strings.Contains(out, "keelbox attach") {
		t.Errorf("ssh agent@box true prints the welcome: %v\n%q", err, out)
	} else {
		logf(t, "PASS keelbox welcome exits 0 and prints nothing without a terminal, and ssh agent@box command prints nothing")
	}
}

// firstAttachChecks cover the session a box's first apply starts: the tmux
// server came up, with session main, before keelbox and the agent CLIs were
// installed, so main's first shell could not print the welcome and left
// "retry" in the session's mark. The first attach starts that untouched
// shell again, and it prints the welcome with both agent CLIs.
func firstAttachChecks(t *testing.T, host, box, cfg string) {
	t.Helper()
	mark, _ := sshRun(t, host, "agent", nil, "tmux show-options -qv -t main @keelbox-welcome")
	logf(t, "session main's welcome mark after the first apply: %q", strings.TrimSpace(mark))
	if before, _ := sshRun(t, host, "agent", nil, "tmux capture-pane -p -t main"); strings.Contains(before, "Come back") && !strings.Contains(before, "claude    codex") {
		t.Errorf("session main printed its welcome before the agent CLIs were installed:\n%s", before)
	}
	// attach needs a terminal; script(1) provides one. Its input stays
	// open, so no end-of-file reaches the shell in main.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(); _ = r.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "script", "-qec", fmt.Sprintf("%s attach %s", keelbox, cfg), "/dev/null")
	cmd.Dir = workDir
	cmd.Stdin = r
	_ = cmd.Run()
	out, err := sshRun(t, host, "agent", nil, "tmux capture-pane -p -t main; echo \"mark: [$(tmux show-options -qv -t main @keelbox-welcome)]\"")
	if err != nil || strings.Count(out, "Come back") != 1 || !strings.Contains(out, box) || !strings.Contains(out, "Start an agent") || !strings.Contains(out, "claude    codex") || !strings.Contains(out, "mark: []") {
		t.Errorf("the first attach to the session the first apply started does not show the welcome once, with both agent CLIs: %v\n%s", err, out)
	} else {
		logf(t, "PASS the first attach after the first apply shows the welcome in session main, once, with both agent CLIs")
	}
}

// welcomeAfterRebootChecks count the welcome in every pane of the sessions
// tmux-resurrect brought back. A restored session is marked only after its
// shells have started, so as a rule it shows no welcome; whatever the
// timing, no session shows it twice, and a session created now shows it.
func welcomeAfterRebootChecks(t *testing.T, host string) {
	t.Helper()
	out := must(t, host, "agent", `for s in $(tmux list-sessions -F '#{session_id}'); do n=0; `+
		`for p in $(tmux list-panes -s -t "$s" -F '#{pane_id}'); do n=$((n + $(tmux capture-pane -p -S - -t "$p" | grep -c 'Come back'))); done; `+
		`echo "session $(tmux display-message -p -t "$s" '#{session_name}') welcomes=$n"; done`, "session main welcomes=")
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasSuffix(l, "welcomes=0") && !strings.HasSuffix(l, "welcomes=1") {
			t.Errorf("after the reboot a session shows the welcome more than once:\n%s", out)
			return
		}
	}
	logf(t, "PASS after the reboot no session shows the welcome more than once")
	out, err := sshRun(t, host, "agent", nil, `tmux new-session -d -s kb-after -x 160 -y 30; `+
		`for i in $(seq 1 30); do tmux capture-pane -p -t kb-after | grep -q 'Come back' && break; sleep 0.5; done; tmux capture-pane -p -t kb-after | grep -c 'Come back'; tmux kill-session -t kb-after`)
	if err != nil || strings.TrimSpace(out) != "1" {
		t.Errorf("a session created after the reboot does not start with the welcome: %v\n%s", err, out)
	} else {
		logf(t, "PASS a session created after the reboot starts with the welcome")
	}
}

// sessionChecks drive the laptop-side session commands.
func sessionChecks(t *testing.T, host, box, cfg string) {
	t.Helper()
	out, err := kb(t, "sessions", cfg)
	if err != nil || !strings.Contains(out, "main") {
		t.Errorf("sessions: %v\n%s", err, out)
	}
	// attach needs a terminal; script(1) provides one. The session is created
	// on attach and outlives the client, which timeout ends.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "script", "-qec", fmt.Sprintf("%s attach %s --session e2e", keelbox, cfg), "/dev/null")
	cmd.Dir = workDir
	_ = cmd.Run()
	out, err = kb(t, "sessions", cfg)
	if err != nil || !strings.Contains(out, "e2e") {
		t.Errorf("attach did not create session e2e: %v\n%s", err, out)
	} else {
		logf(t, "PASS attach and sessions")
	}
	// A session started with a command runs no login shell: it has only
	// what the tmux server got from the agent's user manager, which
	// lingering started before apply wrote environment.d. Before any
	// reboot, it finds the agent CLIs, which the vendors' installers put in
	// ~/.local/bin, and the mise tools, whose shims come first.
	envOut, err := sshRun(t, host, "agent", nil, `tmux new-session -d -s kb-env 'for c in claude codex node pnpm; do command -v $c >/dev/null && echo "$c found" || echo "$c missing"; done > /tmp/kb-env.out 2>&1'; `+
		`for i in 1 2 3 4 5 6 7 8 9 10; do [ -s /tmp/kb-env.out ] && break; sleep 1; done; cat /tmp/kb-env.out; rm -f /tmp/kb-env.out; systemctl --user show-environment | grep '^PATH='`)
	if err != nil || strings.Contains(envOut, "missing") || strings.Count(envOut, " found") != 4 || !strings.Contains(envOut, "PATH=/home/agent/.local/share/mise/shims:") || !strings.Contains(envOut, "/home/agent/.local/bin:") {
		t.Errorf("a tmux session started with a command lacks the agent's PATH: %v\n%s", err, envOut)
	} else {
		logf(t, "PASS a fresh tmux session started with a command finds claude, codex, node and pnpm before any reboot")
	}
	// The server the watch run brought back (watchdogChecks) has the
	// agent's own PATH from its user manager, not the fixed PATH keelbox
	// gives the commands it runs as the agent (ADR 0053).
	must(t, host, "agent", "tmux show-environment -g PATH", "/.local/share/mise/shims:", "/.local/bin")
	logf(t, "PASS the tmux server keeps the agent's own PATH")
	statusLineChecks(t, host, box)
	terminfoChecks(t, host)
	paneOOMCheck(t, host)
	welcomeChecks(t, host, box)
	if out, err := kb(t, "login", cfg); err == nil || !strings.Contains(out, "needs a terminal") {
		t.Errorf("login without a terminal: %v\n%s", err, out)
	} else {
		logf(t, "PASS login refuses to run without a terminal")
	}
}

// upgradeChecks exercise the release guard and rollback.
func upgradeChecks(t *testing.T, host, cfg string) {
	t.Helper()
	// Pretend the box was last applied by a newer release.
	forge := `sudo sed -i '$ s/"release":"[^"]*"/"release":"2099.1.0"/' /var/lib/keelbox/logbook.jsonl`
	if out, err := sshRun(t, host, "admin", nil, forge); err != nil {
		t.Fatalf("forge logbook: %v\n%s", err, out)
	}
	out, err := kb(t, "apply", cfg)
	if err == nil || !strings.Contains(out, "--allow-downgrade") {
		t.Errorf("apply over a newer release was not refused: %v\n%s", err, out)
	} else {
		logf(t, "PASS downgrade refused without --allow-downgrade")
	}
	if out, err := kb(t, "apply", cfg, "--allow-downgrade"); err != nil {
		t.Errorf("apply --allow-downgrade: %v\n%s", err, out)
	} else {
		logf(t, "PASS apply --allow-downgrade")
	}

	// A second keelbox build replaces the first, which rollback restores.
	next := filepath.Join(workDir, "keelbox-next")
	b := exec.Command("go", "build", "-ldflags", "-X github.com/keelbox/keelbox/internal/version.Version=v0.0.0-e2e", "-o", next, "../../cmd/keelbox")
	b.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build next: %v\n%s", err, out)
	}
	if out, err := run(t, next, "apply", cfg); err != nil || !strings.Contains(out, "keelbox-binary") {
		t.Fatalf("apply with the next build: %v\n%s", err, out)
	}
	if out, _ := sshRun(t, host, "admin", nil, "keelbox version"); !strings.Contains(out, "v0.0.0-e2e") {
		t.Errorf("box did not install the next build: %s", out)
	}
	if out, err := sshRun(t, host, "admin", nil, "sudo keelbox rollback"); err != nil {
		t.Errorf("rollback: %v\n%s", err, out)
	}
	if out, _ := sshRun(t, host, "admin", nil, "keelbox version"); strings.Contains(out, "v0.0.0-e2e") {
		t.Errorf("rollback left the next build installed: %s", out)
	} else {
		logf(t, "PASS rollback restored the previous keelbox: %s", firstLine(out))
	}
	if out, _ := sshRun(t, host, "admin", nil, "sudo cmp -s /var/lib/keelbox/keelbox.previous /usr/local/bin/keelbox || echo swapped"); !strings.Contains(out, "swapped") {
		t.Errorf("rollback did not keep the replaced keelbox as the previous one")
	}
	// From the laptop, a second rollback undoes the first. It replaces the
	// staged keelbox while that binary is running.
	if out, err := kb(t, "rollback", cfg); err != nil {
		t.Errorf("rollback from the laptop: %v\n%s", err, out)
	} else if out, _ := sshRun(t, host, "admin", nil, "keelbox version"); !strings.Contains(out, "v0.0.0-e2e") {
		t.Errorf("a second rollback, from the laptop, did not return to the next build: %s", out)
	} else {
		logf(t, "PASS rollback from the laptop undid the first rollback")
	}
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Errorf("apply after rollback: %v\n%s", err, out)
	}
}

// symlinkChecks replay an agent-to-root attack: the agent points ~/.config
// at /etc, so an apply that chowned through the link would give it
// /etc/docker, and with it the system Docker daemon's config. apply
// touches the agent's home only as the agent, so the run
// stops at the agent's own permission error and /etc is unchanged. A link
// from ~/.bashrc to /etc/shadow must not copy the shadow file anywhere the
// agent can read. Afterwards the agent's home is restored and re-applied.
func symlinkChecks(t *testing.T, host, cfg string) {
	t.Helper()
	etc := func() string {
		out, err := sshRun(t, host, "admin", nil, "sudo stat -c '%U:%G %a' /etc/docker /etc/docker/daemon.json; sudo find /etc -user agent -o -group agent | head")
		if err != nil {
			t.Fatalf("inspect /etc: %v\n%s", err, out)
		}
		return out
	}
	before := etc()
	if strings.Contains(before, "agent") {
		t.Fatalf("/etc already has agent-owned entries:\n%s", before)
	}
	for _, tc := range []struct{ name, plant, restore string }{
		{"~/.config -> /etc", "mv ~/.config ~/.config.e2e && ln -s /etc ~/.config", "rm ~/.config && mv ~/.config.e2e ~/.config"},
		{"~/.bashrc -> /etc/shadow", "mv ~/.bashrc ~/.bashrc.e2e && ln -s /etc/shadow ~/.bashrc", "rm ~/.bashrc && mv ~/.bashrc.e2e ~/.bashrc"},
	} {
		if out, err := sshRun(t, host, "agent", nil, tc.plant); err != nil {
			t.Fatalf("%s: plant: %v\n%s", tc.name, err, out)
		}
		passed := true
		fail := func(format string, args ...any) {
			t.Helper()
			t.Errorf(format, args...)
			passed = false
		}
		out, err := kb(t, "apply", cfg)
		if err == nil {
			fail("%s: apply through the planted link succeeded:\n%s", tc.name, out)
		} else if !strings.Contains(out, "as agent") || !strings.Contains(out, "the rest of the box is applied") {
			fail("%s: apply failed for another reason, or stopped the whole run:\n%s", tc.name, out)
		}
		if out, err := kb(t, "doctor", cfg); err == nil {
			fail("%s: doctor passed with the agent's home broken:\n%s", tc.name, out)
		}
		if after := etc(); after != before {
			fail("%s: /etc changed:\nbefore:\n%s\nafter:\n%s", tc.name, before, after)
		}
		// No regular file in the agent's home may hold a copy of /etc/shadow.
		copies, err := sshRun(t, host, "admin", nil, `sudo find /home/agent -xdev -type f -size -1M -exec cmp -s /etc/shadow {} \; -print`)
		if err != nil || strings.TrimSpace(copies) != "" {
			fail("%s: /etc/shadow copied into the agent's home: %v %s", tc.name, err, copies)
		}
		if out, err := sshRun(t, host, "agent", nil, tc.restore); err != nil {
			t.Fatalf("%s: restore: %v\n%s", tc.name, err, out)
		}
		if passed {
			logf(t, "PASS agent symlink %s: apply refused as the agent, the rest applied, doctor fails, /etc unchanged", tc.name)
		}
	}
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply after restoring the agent's home: %v\n%s", err, out)
	}
}

// allowChecks open two ports to the LAN with host.firewall.allow, one a
// plain listener and one a system Docker container's published port (which
// bypasses UFW and meets keelbox's DOCKER-USER guard instead), and check
// from this machine, on the LXD bridge's private network, that exactly
// those two are reachable, then that both close when the entries go.
func allowChecks(t *testing.T, host, cfg string) {
	t.Helper()
	must(t, host, "admin", "sudo systemd-run --unit kb-e2e-listen-a -p DynamicUser=yes python3 -m http.server 8090 >/dev/null; "+
		"sudo systemd-run --unit kb-e2e-listen-b -p DynamicUser=yes python3 -m http.server 8091 >/dev/null; "+
		"sudo docker run -d --name kb-e2e-web-a -p 8092:80 busybox httpd -f -p 80 >/dev/null; "+
		"sudo docker run -d --name kb-e2e-web-b -p 8093:80 busybox httpd -f -p 80 >/dev/null; sleep 2; "+
		"for p in 8090 8091 8092 8093; do curl -sS -o /dev/null http://127.0.0.1:$p/ || exit 1; done")
	reachable := func(port string) bool {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 4*time.Second)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	}
	expect := func(when string, open map[string]bool) {
		t.Helper()
		for _, p := range []string{"8090", "8091", "8092", "8093"} {
			if got := reachable(p); got != open[p] {
				t.Errorf("%s: port %s reachable %v, want %v", when, p, got, open[p])
			}
		}
	}
	expect("before allow", map[string]bool{})
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	editConfig(t, cfg, func(s string) string {
		return s + "host:\n  firewall:\n    allow:\n      - {name: e2e-plain, port: 8090}\n      - {name: e2e-docker, port: 8092}\n      - {service: unifi}\n      - {name: e2e-clash, port: 8405}\n"
	})
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply with allow: %v\n%s", err, out)
	}
	expect("with allow", map[string]bool{"8090": true, "8092": true})
	must(t, host, "admin", "sudo ufw status", "8090/tcp", "keelbox allow e2e-plain", "8443/tcp")
	// A port kept open inside the preview range (this config's 8401-8410;
	// UniFi's 8443 in the default 8401-8499): previews skip it, and doctor
	// says so rather than failing.
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "previews skip 8405, kept open to the LAN by host.firewall.allow (e2e-clash)") {
		t.Errorf("doctor with 8405 kept open: %v\n%s", err, out)
	} else {
		logf(t, "PASS doctor with a LAN port inside previews.ports: previews skip 8405, no failure")
	}
	if err := os.WriteFile(cfg, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply without allow: %v\n%s", err, out)
	}
	expect("after removing allow", map[string]bool{})
	must(t, host, "admin", "sudo systemctl stop kb-e2e-listen-a kb-e2e-listen-b; sudo docker rm -f kb-e2e-web-a kb-e2e-web-b >/dev/null; echo cleaned", "cleaned")
	logf(t, "PASS host.firewall.allow: only the allowed ports, plain and Docker, reach the LAN")
}

// permissionsChecks cover users.agent.permissions: auto, the default: as
// the agent, keelbox install gets a small package from Ubuntu's archive
// installed through root's install service, and nothing else gets through
// it: a deny-listed name, a version, an option, a local file, a removal, a
// request written by hand past keelbox install's own checks, and a link in
// place of the request file. Afterwards the agent still has no sudo, and
// the logbook has the requests.
func permissionsChecks(t *testing.T, host, cfg string) {
	t.Helper()
	agent := func(remote string) string {
		out, _ := sshRun(t, host, "agent", nil, remote)
		return out
	}
	// hello is in Ubuntu's main, small, and puts one program on PATH.
	if out := agent(`keelbox install hello --why "e2e: a small package from main" 2>&1; echo rc=$?`); !strings.Contains(out, "Installed hello.") || !strings.Contains(out, "rc=0") {
		t.Fatalf("keelbox install hello as the agent:\n%s", out)
	}
	if out := agent("command -v hello && hello"); !strings.Contains(out, "/usr/bin/hello") || !strings.Contains(out, "Hello, world!") {
		t.Errorf("hello is not on the agent's PATH after the install:\n%s", out)
	} else {
		logf(t, "PASS keelbox install as the agent: hello installed and on PATH")
	}
	if out := agent("keelbox install hello 2>&1; echo rc=$?"); !strings.Contains(out, "Already installed: hello.") || !strings.Contains(out, "rc=0") {
		t.Errorf("a second keelbox install hello:\n%s", out)
	}

	for _, c := range []struct{ name, cmd, says, rc string }{
		{"a deny-listed name", "keelbox install sudo", "sudo is on keelbox's deny-list (privilege tools)", "rc=1"},
		{"the SSH server", "keelbox install openssh-server", "on keelbox's deny-list (remote login)", "rc=1"},
		{"a version", "keelbox install hello=2.10-3", "is not a package name", "rc=1"},
		{"an option keelbox does not have", "keelbox install --reinstall hello", "keelbox: install has no flag --reinstall; list its flags: keelbox help install", "rc=2"},
		{"an option after --", "keelbox install -- --reinstall hello", "is an option, not a package name", "rc=1"},
		{"a local file", "keelbox install ./hello.deb", "is not a package name", "rc=1"},
		{"a removal", "keelbox install ufw-", "apt has no package named ufw-", "rc=1"},
		{"a package apt does not have", "keelbox install keelbox-e2e-no-such-package", "apt has no package named keelbox-e2e-no-such-package", "rc=1"},
	} {
		out := agent(c.cmd + " 2>&1; echo rc=$?")
		if !strings.Contains(out, c.says) || !strings.Contains(out, c.rc) {
			t.Errorf("%s (%s): want %q and %s:\n%s", c.name, c.cmd, c.says, c.rc, out)
			continue
		}
		logf(t, "PASS keelbox install refuses %s: %s", c.name, firstLine(out))
	}
	must(t, host, "admin", "dpkg-query -W -f='${db:Status-Abbrev}' ufw", "ii")

	// A request written by hand: what counts is the service's checks, not
	// keelbox install's. Root names the request itself, so the ID in the
	// file is not the one its record shows.
	out := agent(`f=~/.local/state/keelbox/install-request.json; ` +
		`printf '{"v":1,"id":"e2e0bad1","time":"%s","packages":["--allow-downgrades","sudo"]}' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$f"; ` +
		`for i in $(seq 30); do keelbox install --status | grep -q -- --allow-downgrades && break; sleep 1; done; keelbox install --status`)
	if !strings.Contains(out, `"--allow-downgrades" is an option, not a package name`) || regexp.MustCompile(`(?m)^e2e0bad1 `).MatchString(out) {
		t.Errorf("a request written by hand was not refused by the service:\n%s", out)
	} else {
		logf(t, "PASS a hand-written request with an option is refused by the install service")
	}
	// A link to a root-only file in place of the request: not followed.
	out = agent(`f=~/.local/state/keelbox/install-request.json; ln -sf /etc/shadow "$f"; sleep 6; cat /var/lib/keelbox/installs/requests.json; rm -f "$f"`)
	if strings.Contains(out, "root:") {
		t.Errorf("root's record of the requests carries /etc/shadow:\n%s", out)
	}
	must(t, host, "admin", "sudo journalctl -u keelbox-install.service --no-pager -o cat | tail -n 40", "symbolic link")
	logf(t, "PASS a link in place of the request file is not followed")

	// The limits on the agent are as before.
	if out := agent("sudo -n true 2>&1; echo rc=$?"); strings.Contains(out, "rc=0") {
		t.Errorf("the agent has sudo after keelbox install:\n%s", out)
	}
	if out := agent("id -nG"); strings.Contains(" "+strings.TrimSpace(out)+" ", " sudo ") || strings.Contains(" "+strings.TrimSpace(out)+" ", " docker ") {
		t.Errorf("the agent's groups after keelbox install: %s", out)
	}
	if out := agent("keelbox approve e2e0bad1 2>&1; echo rc=$?"); !strings.Contains(out, "runs on the owner's laptop") || !strings.Contains(out, "rc=2") {
		t.Errorf("keelbox approve as the agent:\n%s", out)
	}
	logf(t, "PASS the agent has no sudo, is in neither the sudo nor the docker group, and cannot approve")

	// Every request is in the logbook; the laptop sees what was installed.
	// A refusal is there in keelbox's own words: the names of a request
	// refused before apt was asked about them are the agent's text, so the
	// line ends at the reason (the quote closes the JSON string), and names
	// apt has no package for are counted.
	must(t, host, "admin", `sudo grep '"kind":"install"' /var/lib/keelbox/logbook.jsonl`,
		"installed hello for agent", `refused agent's request: on the deny-list (privilege tools)"`, `refused agent's request: on the deny-list (remote login)"`,
		`refused agent's request: an option, not a package name"`, `refused agent's request: not a package name"`, "refused agent's request: no install candidate (1 named)")
	logf(t, "PASS the logbook has the installs and the refusals")
	if out, err := kb(t, "status", cfg); err != nil || !strings.Contains(out, "agent-installed: hello (1)") || !strings.Contains(out, "- hello") {
		t.Errorf("status does not list the package or the YAML that keeps it: %v\n%s", err, out)
	}
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "auto: keelbox install installs Ubuntu packages for the agent") || !strings.Contains(out, "keelbox-install.path active") {
		t.Errorf("doctor's permissions rows: %v\n%s", err, out)
	}
	if out, err := kb(t, "requests", cfg); err != nil || !strings.Contains(out, "0 waiting for approval") {
		t.Errorf("requests: %v\n%s", err, out)
	}
	logf(t, "PASS status, doctor and requests report the agent's packages")
}

// manualPermissionsChecks cover users.agent.permissions: manual: as the
// agent, keelbox install records a request and installs nothing; the laptop
// lists it with the agent's reason, keelbox approve has root's service
// install it, and keelbox deny turns another down. A session's welcome says
// that keelbox install asks the owner. The agent still has no sudo, and the
// logbook has each step. The config goes back to auto at the end.
func manualPermissionsChecks(t *testing.T, host, box, cfg string) {
	t.Helper()
	agent := func(remote string) string {
		out, _ := sshRun(t, host, "agent", nil, remote)
		return out
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	editConfig(t, cfg, func(s string) string {
		return strings.Replace(s, "\nagents: [", "\n    permissions: manual\nagents: [", 1)
	})
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply with users.agent.permissions: manual: %v\n%s", err, out)
	}
	defer func() {
		if err := os.WriteFile(cfg, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := kb(t, "apply", cfg); err != nil {
			t.Fatalf("apply back to users.agent.permissions: auto: %v\n%s", err, out)
		}
		if out := agent("keelbox install --status | head -n 1"); !strings.Contains(out, "users.agent.permissions: auto") {
			t.Errorf("the box is not back on auto:\n%s", out)
		}
	}()
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "manual: keelbox install records a request; nothing is installed until keelbox approve "+box) {
		t.Errorf("doctor's permissions row with manual: %v\n%s", err, out)
	}

	// tree and pv are small, in Ubuntu's archive and on no box by default.
	requestID := regexp.MustCompile(`Request ([0-9a-f]{8}) is recorded: `)
	ask := func(pkg string) string {
		t.Helper()
		out := agent(`keelbox install ` + pkg + ` --why "e2e: the owner decides" 2>&1; echo rc=$?`)
		m := requestID.FindStringSubmatch(out)
		if m == nil || !strings.Contains(out, "is recorded: "+pkg+". Nothing is installed yet.") ||
			!strings.Contains(out, "keelbox approve "+box+" "+m[1]) || !strings.Contains(out, "rc=3") {
			t.Fatalf("keelbox install %s as the agent with manual: want a recorded request, the approve command and rc=3:\n%s", pkg, out)
		}
		return m[1]
	}
	id := ask("tree")
	if out := agent("command -v tree; dpkg-query -W -f='${db:Status-Abbrev}' tree 2>&1; echo"); strings.Contains(out, "/tree") || strings.Contains(out, "ii") {
		t.Errorf("tree is installed before the owner approved the request:\n%s", out)
	}
	if out := agent("keelbox install --status"); !regexp.MustCompile(`(?m)^`+id+` .* pending +tree$`).MatchString(out) || !strings.Contains(out, "1 waiting for approval") {
		t.Errorf("the agent's keelbox install --status does not show request %s waiting:\n%s", id, out)
	}
	logf(t, "PASS manual: keelbox install as the agent records request %s and installs nothing", id)

	if out, err := kb(t, "requests", cfg); err != nil || !strings.Contains(out, "1 waiting for approval on "+box+".") ||
		!regexp.MustCompile(`(?m)^`+id+` .* pending +tree$`).MatchString(out) || !strings.Contains(out, "the agent's reason: e2e: the owner decides") {
		t.Errorf("requests with one waiting: %v\n%s", err, out)
	}
	// Welcomed in a new session: keelbox install asks the owner.
	welcome := agent(`tmux kill-session -t kb-manual 2>/dev/null; tmux new-session -d -s kb-manual -x 160 -y 30; ` +
		`for i in $(seq 1 30); do tmux capture-pane -p -t kb-manual | grep -q 'Come back' && break; sleep 0.5; done; sleep 1; tmux capture-pane -p -t kb-manual; tmux kill-session -t kb-manual`)
	if !strings.Contains(welcome, "Packages         keelbox install NAME asks you; answer from your computer: keelbox requests "+box) ||
		!strings.Contains(welcome, "Need root?       from your computer: keelbox ssh "+box+" --admin") {
		t.Errorf("a session's welcome with manual lacks the Packages or the Need root? line:\n%s", welcome)
	} else {
		logf(t, "PASS manual: a new session's welcome says keelbox install asks the owner")
	}

	out, err := kb(t, "approve", cfg, id)
	if err != nil || !strings.Contains(out, "Approved request "+id+": installing tree for agent...") ||
		!strings.Contains(out, "Request "+id+": installed tree.") || !strings.Contains(out, "0 waiting for approval on "+box+".") {
		t.Fatalf("approve %s: %v\n%s", id, err, out)
	}
	if out := agent("command -v tree && tree --version; keelbox install --status"); !strings.Contains(out, "/usr/bin/tree") || !strings.Contains(out, "tree v") ||
		!regexp.MustCompile(`(?m)^`+id+` .* installed +tree$`).MatchString(out) {
		t.Errorf("tree is not installed and on the agent's PATH after keelbox approve:\n%s", out)
	} else {
		logf(t, "PASS manual: keelbox approve from the laptop has request %s installed", id)
	}

	denied := ask("pv")
	if out, err := kb(t, "deny", cfg, denied); err != nil || !strings.Contains(out, "Denied request "+denied+" (pv); nothing was installed.") {
		t.Errorf("deny %s: %v\n%s", denied, err, out)
	}
	if out := agent("dpkg-query -W -f='${db:Status-Abbrev}' pv 2>&1; echo"); strings.Contains(out, "ii") {
		t.Errorf("pv is installed although the owner denied it:\n%s", out)
	}
	if out := agent("keelbox install --status"); !regexp.MustCompile(`(?m)^` + denied + ` .* denied +pv$`).MatchString(out) {
		t.Errorf("the agent's keelbox install --status does not show request %s denied:\n%s", denied, out)
	} else {
		logf(t, "PASS manual: keelbox deny turns request %s down and nothing is installed", denied)
	}

	// The limits on the agent are as before, and it cannot decide for the owner.
	if out := agent("sudo -n true 2>&1; echo rc=$?"); strings.Contains(out, "rc=0") {
		t.Errorf("the agent has sudo with users.agent.permissions: manual:\n%s", out)
	}
	if out := agent("keelbox approve " + id + " 2>&1; echo rc=$?"); !strings.Contains(out, "runs on the owner's laptop") || !strings.Contains(out, "rc=2") {
		t.Errorf("keelbox approve as the agent:\n%s", out)
	}
	must(t, host, "admin", `sudo grep '"kind":"install"' /var/lib/keelbox/logbook.jsonl`,
		"agent asked for tree; waiting for approval", "installed tree for agent, approved by the owner", "the owner denied agent's request for pv")
	logf(t, "PASS manual: the agent has no sudo and cannot approve, and the logbook has the request, the approval and the denial")
}
