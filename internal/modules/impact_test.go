package modules

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/inventory"
	"github.com/keelbox/keelbox/internal/system"
)

// homeServer seeds the sim with a machine in use: Samba and a Python app
// listening, Docker Engine with a published container (restart policy) and
// one without a policy, the owner's account with a password and sudo,
// tmux and Claude Code running as the owner, UFW off.
func homeServer(t *testing.T) *sim {
	t.Helper()
	s := newSim(t)
	for _, p := range dockerPackages {
		s.pkgs[p] = "5:29.9.3-1~ubuntu.26.04~resolute"
	}
	s.units["docker.service"] = true
	s.OK("ss -Hltunp", "tcp LISTEN 0 50 0.0.0.0:445 0.0.0.0:* users:((\"smbd\",pid=1,fd=4))\n"+
		"tcp LISTEN 0 5 0.0.0.0:8095 0.0.0.0:* users:((\"python3\",pid=2,fd=3))\n"+
		"tcp LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:* users:((\"docker-proxy\",pid=3,fd=7))\n"+
		"tcp LISTEN 0 4096 127.0.0.1:5432 0.0.0.0:* users:((\"docker-proxy\",pid=4,fd=7))\n"+
		"tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:((\"sshd\",pid=5,fd=3))\n")
	s.AddFile("/proc/1/status", "Name:\tsmbd\nUid:\t0\t0\t0\t0\n", 0o444)
	s.OK("docker -H "+inventory.DockerSocket+" ps", "web\trunning\t0.0.0.0:8080->80/tcp, [::]:8080->80/tcp\nscratch\trunning\t\ndb\trunning\t127.0.0.1:5432->5432/tcp\n")
	s.OK("docker -H "+inventory.DockerSocket+" inspect", "/web unless-stopped\n/scratch no\n/db always\n")
	s.AddUser("sam", 1001)
	s.groups["sudo"] = append(s.groups["sudo"], "sam")
	shadow, _ := s.ReadFile("/etc/shadow")
	s.AddFile("/etc/shadow", string(shadow)+"sam:$y$j9T$salt$hash:20000:0:99999:7:::\n", 0o640)
	s.OK("getent passwd", "root:x:0:0:root:/root:/bin/bash\nubuntu:x:1000:1000::/home/ubuntu:/bin/bash\nsam:x:1001:1001::/home/sam:/bin/bash\n")
	s.OK("getent group", "sudo:x:27:ubuntu,sam\n")
	s.OK("ps -eo", "sam tmux: server\nsam claude\nroot sshd\n")
	return s
}

// An account with a password that also logs in with a key is not cut off
// when passwords go: its key is in the files sshd reads, or keelbox runs
// from its SSH session.
func TestImpactKeyLoginIsNotCutOff(t *testing.T) {
	s := homeServer(t)
	s.Files["/home/sam/.ssh"] = &system.FakeFile{Dir: true, Mode: 0o700, Owner: "sam", Group: "sam"}
	s.Files["/home/sam/.ssh/authorized_keys"] = &system.FakeFile{Data: []byte("ssh-ed25519 AAAAC3Nza sam@laptop\n"), Mode: 0o600, Owner: "sam", Group: "sam"}
	found := false
	for _, it := range impactOf(t, s, "") {
		if it.Module == "host" && it.Subject == "sam" {
			found = true
			if it.Gate || it.Group != engine.ImpactChanges || !strings.Contains(it.Detail, "logs in over SSH with a key") {
				t.Errorf("with a key: %+v", it)
			}
		}
	}
	if !found {
		t.Error("sam not in the report")
	}
	s = homeServer(t)
	e := simEnv(t, s, testConfig(t, ""))
	e.SSHConnection, e.Facts.InvokingUser = "192.168.1.5 50000 192.168.1.20 22", "sam"
	ctx := context.Background()
	_, items := engine.Impact(ctx, e, All(), inventory.Collect(ctx, s))
	for _, it := range items {
		if it.Module == "host" && it.Subject == "sam" && it.Gate {
			t.Errorf("keelbox's own SSH account: %+v", it)
		}
	}
}

func impactOf(t *testing.T, s *sim, extra string) []engine.ImpactItem {
	t.Helper()
	e := simEnv(t, s, testConfig(t, extra))
	ctx := context.Background()
	_, items := engine.Impact(ctx, e, All(), inventory.Collect(ctx, s))
	return items
}

func describe(items []engine.ImpactItem) []string {
	var out []string
	for _, it := range items {
		g := " "
		if it.Gate {
			g = "*"
		}
		out = append(out, g+" "+string(it.Group)+" "+it.Module+" "+it.Subject)
	}
	sort.Strings(out)
	return out
}

func TestImpactOnAHomeServer(t *testing.T) {
	s := homeServer(t)
	items := impactOf(t, s, "")
	got := strings.Join(describe(items), "\n")
	want := strings.Join([]string{
		"  changes doctor doctor",
		"  changes host hostname",
		"  changes host limits",
		"  changes host network",
		"  changes host reboots",
		"  changes host root",
		"  changes host ufw",
		"  cuts host ubuntu",
		"  keeps host sam",
		"  keeps runtime docker",
		"  keeps sessions sam",
		"  restarts host sshd",
		"* cuts host 8095/tcp",
		"* cuts host sam",
		"* cuts host samba",
		"* cuts runtime 8080/tcp",
		"* restarts runtime docker",
	}, "\n")
	// sam has a password and SSH allows it: listed as cut off, not kept.
	want = strings.Replace(want, "  keeps host sam\n", "", 1)
	if got != want {
		t.Errorf("impact:\n%s\nwant:\n%s", got, want)
	}
	for _, it := range items {
		switch it.Subject {
		case "reboots":
			if !strings.Contains(it.Detail, "01:00 to 06:00 UTC") || !strings.Contains(it.Hint, "host.auto_reboot: never") {
				t.Errorf("reboots: %+v", it)
			}
		case "8080/tcp":
			if it.Keep == nil || it.Keep.Name != "web" || it.Keep.Port != config.Port(8080) || !strings.Contains(it.Detail, "Docker guard") {
				t.Errorf("container port: %+v", it)
			}
		case "samba":
			if it.Keep == nil || it.Keep.Service != "samba" || !strings.Contains(it.Detail, "Samba file sharing (smbd) listens on 445/tcp now") ||
				!strings.Contains(it.Hint, "{service: samba} keeps it, and opens 139/tcp, 137/udp and 138/udp as well") {
				t.Errorf("samba: %+v", it)
			}
		case "docker":
			if it.Group == engine.ImpactRestarts && (!strings.Contains(it.Detail, "db, web come back") || !strings.Contains(it.Detail, "scratch has no restart policy and stays stopped") ||
				!strings.Contains(it.Hint, "docker update --restart unless-stopped scratch")) {
				t.Errorf("docker restart: %+v", it)
			}
		case "hostname":
			if !strings.Contains(it.Detail, "ubuntu becomes testbox") || !strings.Contains(it.Detail, "Samba's network name") {
				t.Errorf("hostname: %+v", it)
			}
		case "sam":
			if it.Module == "sessions" && !strings.Contains(it.Detail, "claude and tmux run as sam") {
				t.Errorf("sessions: %+v", it)
			}
		}
	}
	for _, c := range s.Calls {
		cmd := c.String()
		for _, change := range []string{"apt-get", "useradd", "usermod", "gpasswd", "hostnamectl", "ufw allow", "ufw default", "ufw --force", "systemctl restart", "systemctl enable", "systemctl stop", "docker -H " + inventory.DockerSocket + " update"} {
			if strings.HasPrefix(cmd, change) {
				t.Errorf("impact changed something: %s", cmd)
			}
		}
	}
}

func TestImpactKeptByConfig(t *testing.T) {
	s := homeServer(t)
	items := impactOf(t, s, "host:\n  hostname: ubuntu\n  firewall:\n    allow:\n      - {name: samba, port: 445}\n      - {name: py, port: 8095}\n      - {name: web, port: 8080, from: 192.168.1.0/24}\n")
	var gated []string
	for _, it := range engine.Gated(items) {
		gated = append(gated, it.Subject)
	}
	sort.Strings(gated)
	if strings.Join(gated, " ") != "docker sam" {
		t.Errorf("still gated: %v\n%s", gated, strings.Join(describe(items), "\n"))
	}
	kept := map[string]string{}
	for _, it := range items {
		if it.Group == engine.ImpactKeeps {
			kept[it.Subject] = it.Detail
		}
		if it.Subject == "hostname" {
			t.Errorf("hostname kept by config, still listed: %+v", it)
		}
	}
	if !strings.Contains(kept["8080/tcp"], "stays open to 192.168.1.0/24 (host.firewall.allow web)") || !strings.Contains(kept["samba"], "Samba file sharing (smbd) stays open to the LAN (host.firewall.allow samba)") {
		t.Errorf("kept: %v", kept)
	}
}

func TestImpactUFWAlreadyOn(t *testing.T) {
	s := homeServer(t)
	s.OK("ufw status verbose", "Status: active\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n\nTo Action From\n-- ------ ----\n8095/tcp ALLOW IN Anywhere\n")
	for _, it := range impactOf(t, s, "") {
		if it.Subject == "445/tcp" || it.Subject == "8095/tcp" {
			t.Errorf("UFW already blocks or admits it, keelbox changes nothing there: %+v", it)
		}
		if it.Subject == "ufw" && (it.Group != engine.ImpactKeeps || !strings.Contains(it.Detail, "its 1 rule stays")) {
			t.Errorf("ufw: %+v", it)
		}
		if it.Subject == "8080/tcp" && !it.Gate {
			t.Errorf("Docker bypasses UFW, so the guard still cuts the container: %+v", it)
		}
	}
}

func TestImpactUFWOffWithRules(t *testing.T) {
	s := homeServer(t)
	s.OK("ufw show added", "Added user rules (see 'ufw status' for running firewall):\nufw allow 8095/tcp\n")
	for _, it := range impactOf(t, s, "") {
		if it.Subject == "8095/tcp" && (it.Group != engine.ImpactKeeps || !strings.Contains(it.Detail, "existing rules admits it")) {
			t.Errorf("a rule UFW applies once it is on: %+v", it)
		}
	}
}

func TestImpactDockerIO(t *testing.T) {
	s := homeServer(t)
	for _, p := range dockerPackages {
		delete(s.pkgs, p)
	}
	s.pkgs["docker.io"], s.pkgs["containerd"], s.pkgs["runc"] = "1", "1", "1"
	var rep *engine.ImpactItem
	for _, it := range impactOf(t, s, "") {
		if it.Group == engine.ImpactReplaces {
			rep = &it
		}
		if it.Group == engine.ImpactRestarts && it.Subject == "docker" {
			t.Errorf("a replaced daemon is not also a restart: %+v", it)
		}
	}
	if rep == nil || !rep.Gate || !strings.Contains(rep.Detail, "docker.io containerd runc is replaced") || !strings.Contains(rep.Detail, "3 running containers (db, scratch, web) stop") {
		t.Errorf("replaces: %+v", rep)
	}
}

func TestImpactExistingAgentAccount(t *testing.T) {
	s := homeServer(t)
	s.AddUser("agent", 1002)
	s.groups["sudo"] = append(s.groups["sudo"], "agent")
	s.OK("getent passwd", "agent:x:1002:1002::/home/agent:/bin/bash\n")
	s.OK("getent group", "sudo:x:27:agent\ndocker:x:990:agent\n")
	var it *engine.ImpactItem
	for _, x := range impactOf(t, s, "") {
		if x.Subject == "agent" && x.Module == "host" {
			it = &x
		}
	}
	if it == nil || !it.Gate || !strings.Contains(it.Detail, "leaves the sudo and docker groups") {
		t.Errorf("agent: %+v", it)
	}
}

func TestImpactNotifyChannels(t *testing.T) {
	s := homeServer(t)
	s.AddFile("/home/u/.config/keelbox/topic", "t", 0o600)
	var it *engine.ImpactItem
	for _, x := range impactOf(t, s, "notify:\n  channels:\n    - {name: phone, type: ntfy, topic_file: /home/u/.config/keelbox/topic}\n") {
		if x.Subject == "notify" {
			it = &x
		}
	}
	if it == nil || it.Gate || !strings.Contains(it.Detail, "go to phone (ntfy)") {
		t.Errorf("notify: %+v", it)
	}
}

func TestImpactFreshServerHasNoGate(t *testing.T) {
	s := newSim(t)
	s.OK("getent passwd", "root:x:0:0:root:/root:/bin/bash\nubuntu:x:1000:1000::/home/ubuntu:/bin/bash\n")
	s.OK("ss -Hltunp", "tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\nudp UNCONN 0 0 127.0.0.54:53 0.0.0.0:*\n")
	if g := engine.Gated(impactOf(t, s, "")); len(g) > 0 {
		t.Errorf("a fresh cloud server gates: %+v", g)
	}
}

func TestImpactLANSSHSession(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	e.SSHConnection = "192.168.1.5 50000 192.168.1.20 22"
	ctx := context.Background()
	_, items := engine.Impact(ctx, e, All(), inventory.Collect(ctx, s))
	found := false
	for _, it := range items {
		if it.Subject == "ssh" {
			found = true
			if it.Gate || it.KeepSSH != "lan" || !strings.Contains(it.Detail, "192.168.1.5") {
				t.Errorf("ssh: %+v", it)
			}
		}
	}
	if !found {
		t.Error("LAN SSH closing not listed")
	}
	e.SSHConnection = "100.101.1.2 50000 100.100.1.1 22"
	_, items = engine.Impact(ctx, e, All(), inventory.Collect(ctx, s))
	for _, it := range items {
		if it.Subject == "ssh" {
			t.Errorf("over the tailnet nothing closes: %+v", it)
		}
	}
}

// A home server running containers: Jellyfin and Portainer published by system Docker,
// UniFi on the host network, Samba native. Each is one item, kept with one
// entry; a port a service uses beyond its preset is its own item.
func containerHost(t *testing.T) *sim {
	t.Helper()
	s := newSim(t)
	for _, p := range dockerPackages {
		s.pkgs[p] = "5:29.9.3-1~ubuntu.26.04~resolute"
	}
	s.units["docker.service"] = true
	s.OK("ss -Hltunp", strings.Join([]string{
		`tcp LISTEN 0 4096 0.0.0.0:8096 0.0.0.0:* users:(("docker-proxy",pid=10,fd=7))`,
		`udp UNCONN 0 0 0.0.0.0:7359 0.0.0.0:* users:(("docker-proxy",pid=11,fd=7))`,
		`tcp LISTEN 0 4096 0.0.0.0:9443 0.0.0.0:* users:(("docker-proxy",pid=12,fd=7))`,
		`tcp LISTEN 0 100 *:8080 *:* users:(("java",pid=20,fd=40))`,
		`tcp LISTEN 0 100 *:8443 *:* users:(("java",pid=20,fd=41))`,
		`udp UNCONN 0 0 *:3478 *:* users:(("java",pid=20,fd=42))`,
		`udp UNCONN 0 0 *:10001 *:* users:(("java",pid=20,fd=43))`,
		`udp UNCONN 0 0 *:5514 *:* users:(("java",pid=20,fd=44))`,
		`tcp LISTEN 0 50 0.0.0.0:445 0.0.0.0:* users:(("smbd",pid=30,fd=4))`,
	}, "\n"))
	s.AddFile("/proc/30/status", "Name:\tsmbd\nUid:\t0\t0\t0\t0\n", 0o444)
	s.AddFile("/proc/20/cgroup", "0::/system.slice/docker-cafe00000001aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.scope\n", 0o444)
	s.OK("docker -H "+inventory.DockerSocket+" ps", "jellyfin\trunning\t0.0.0.0:8096->8096/tcp, 0.0.0.0:7359->7359/udp\tjellyfin/jellyfin:10.10.3\tbeef00000001\n"+
		"portainer\trunning\t0.0.0.0:9443->9443/tcp\tportainer/portainer-ce:2.21.4\tbeef00000002\n"+
		"unifi\trunning\t\tjacobalberty/unifi:v9.0\tcafe00000001\n")
	s.OK("docker -H "+inventory.DockerSocket+" inspect", "/jellyfin unless-stopped\n/portainer always\n/unifi unless-stopped\n")
	return s
}

func TestImpactNamesKnownServices(t *testing.T) {
	s := containerHost(t)
	items := impactOf(t, s, "")
	by := map[string]engine.ImpactItem{}
	for _, it := range items {
		if it.Group == engine.ImpactCuts || it.Group == engine.ImpactKeeps {
			by[string(it.Group)+" "+it.Subject] = it
		}
	}
	for key, want := range map[string]string{
		"cuts unifi":     "UniFi Network Application (container unifi, host network) listens on 8080/tcp, 8443/tcp, 3478/udp and 10001/udp now; the firewall would block them from the LAN",
		"cuts jellyfin":  "Jellyfin (container jellyfin) publishes 8096/tcp and 7359/udp to the network now; keelbox's Docker guard would drop connections from the LAN",
		"cuts portainer": "Portainer (container portainer) publishes 9443/tcp",
		"cuts samba":     "Samba file sharing (smbd) listens on 445/tcp now",
		"cuts 5514/udp":  "UniFi Network Application (container unifi, host network) listens on the network now",
	} {
		it, ok := by[key]
		if !ok || !strings.Contains(it.Detail, want) || !it.Gate {
			t.Errorf("%s: %+v, want %q", key, it, want)
		}
	}
	u := by["cuts unifi"]
	if u.Keep == nil || u.Keep.Service != "unifi" || u.Module != "host" || strings.Join(u.Ports, " ") != "8080/tcp 8443/tcp 3478/udp 10001/udp" ||
		!strings.Contains(u.Hint, "opens 1900/udp, 6789/tcp, 8880-8882/tcp and 8843/tcp as well") || !strings.Contains(u.Hint, "10001, 1900/udp are how devices and apps find it on the LAN") {
		t.Errorf("unifi: %+v", u)
	}
	if j := by["cuts jellyfin"]; j.Module != "runtime" || j.Keep.Service != "jellyfin" {
		t.Errorf("jellyfin: %+v", j)
	}

	// The keep entries the report suggests keep everything, by name.
	items = impactOf(t, s, "host:\n  firewall:\n    allow:\n      - {service: unifi}\n      - {service: jellyfin}\n      - {service: portainer}\n      - {service: samba}\n      - {name: unifi-syslog, port: 5514, proto: udp}\n")
	for _, it := range items {
		if it.Gate && it.Group == engine.ImpactCuts && it.Subject != "sam" {
			t.Errorf("still cut off: %+v", it)
		}
		if it.Subject == "unifi" && (it.Group != engine.ImpactKeeps || it.Detail != "UniFi Network Application (container unifi, host network) stays open to the LAN (host.firewall.allow unifi)") {
			t.Errorf("unifi kept: %+v", it)
		}
	}
}
