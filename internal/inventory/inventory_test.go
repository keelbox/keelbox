package inventory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/system"
)

func TestParseUFW(t *testing.T) {
	for out, want := range map[string]UFW{
		"Status: inactive\n": {},
		"Status: active\nLogging: on (low)\nDefault: deny (incoming), allow (outgoing), disabled (routed)\nNew profiles: skip\n\nTo                         Action      From\n--                         ------      ----\n22/tcp                     ALLOW IN    Anywhere\n8081/tcp                   ALLOW IN    192.168.1.0/24\n22/tcp (v6)                ALLOW IN    Anywhere (v6)\n": {Active: true, DefaultIn: "deny", Rules: 3},
		"Status: active\nDefault: allow (incoming), allow (outgoing), deny (routed)\n": {Active: true, DefaultIn: "allow"},
		"": {},
	} {
		if got := ParseUFW(out); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q: %+v, want %+v", out, got, want)
		}
	}
	if !(UFW{Active: true, DefaultIn: "reject"}).Blocks() || (UFW{Active: true, DefaultIn: "allow"}).Blocks() || (UFW{DefaultIn: "deny"}).Blocks() {
		t.Error("Blocks")
	}
}

func TestParseUFWAdded(t *testing.T) {
	got := fmt.Sprint(ParseUFWAdded("Added user rules (see 'ufw status' for running firewall):\nufw allow 22/tcp\nufw allow 8081\nufw allow OpenSSH\n" +
		"ufw allow from 10.0.0.0/8 to any port 445 proto tcp comment 'keelbox allow samba'\nufw allow in on tailscale0 comment 'keelbox tailnet'\nufw deny 9000/tcp\n"))
	if want := "[22/tcp 8081/tcp 8081/udp 445/tcp]"; got != want {
		t.Errorf("admits %s, want %s", got, want)
	}
}

func TestParseContainers(t *testing.T) {
	ps := "web\trunning\t0.0.0.0:8080->80/tcp, [::]:8080->80/tcp\tnginx:alpine\t1a2b3c4d5e6f\n" +
		"scratch\trunning\t\n" +
		"db\trunning\t127.0.0.1:5432->5432/tcp\n" +
		"old\texited\t\n" +
		"dns\trunning\t0.0.0.0:53->53/udp, 80/tcp\tpihole/pihole:2025.11\tabcdefabcdef\n"
	pol := "/web unless-stopped\n/scratch no\n/db always\n/old no\n/dns on-failure\n"
	cs := ParseContainers(ps, pol)
	got := fmt.Sprint(cs)
	want := "[{db true always [{127.0.0.1 5432 tcp}]   } {dns true on-failure [{0.0.0.0 53 udp}] pihole/pihole:2025.11 abcdefabcdef pihole} {old false no []   } {scratch true no []   } {web true unless-stopped [{0.0.0.0 8080 tcp} {:: 8080 tcp}] nginx:alpine 1a2b3c4d5e6f }]"
	if got != want {
		t.Errorf("containers:\n%s\nwant\n%s", got, want)
	}
	if !cs[0].ComesBack() || cs[3].ComesBack() {
		t.Error("ComesBack")
	}
}

func TestParseAccounts(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\ndaemon:x:1:1::/usr/sbin:/usr/sbin/nologin\n" +
		"ubuntu:x:1000:1000::/home/ubuntu:/bin/bash\nsam:x:1001:1001::/home/sam:/bin/bash\n" +
		"svc:x:1002:1002::/home/svc:/usr/sbin/nologin\nnobody:x:65534:65534::/nonexistent:/usr/sbin/nologin\n"
	shadow := "root:*:20000::::::\nubuntu:!:20000::::::\nsam:$y$j9T$abc:20000::::::\nsvc:$6$x:20000::::::\n"
	group := "sudo:x:27:ubuntu,sam\ndocker:x:990:sam\nsam:x:1001:\n"
	got := fmt.Sprint(ParseAccounts(passwd, shadow, group))
	want := "[{root 0 false [] /root 0} {ubuntu 1000 false [sudo] /home/ubuntu 0} {sam 1001 true [docker sudo] /home/sam 0}]"
	if got != want {
		t.Errorf("accounts %s, want %s", got, want)
	}
}

// sshd's AuthorizedKeysFile, expanded for an account, and the keys in it.
func TestAuthorizedKeys(t *testing.T) {
	a := Account{Name: "sam", Home: "/home/sam"}
	d := ParseSSHD("pubkeyauthentication yes\nauthorizedkeysfile .ssh/authorized_keys /etc/ssh/keys/%u %h/.ssh/extra_%%\n")
	if got := strings.Join(d.KeyFiles(a), " "); !d.PubkeyAuth || got != "/home/sam/.ssh/authorized_keys /etc/ssh/keys/sam /home/sam/.ssh/extra_%" {
		t.Errorf("%v %s", d.PubkeyAuth, got)
	}
	if got := strings.Join(ParseSSHD("").KeyFiles(a), " "); got != "/home/sam/.ssh/authorized_keys /home/sam/.ssh/authorized_keys2" {
		t.Errorf("default: %s", got)
	}
	if ParseSSHD("pubkeyauthentication no\n").PubkeyAuth {
		t.Error("pubkeyauthentication no read as yes")
	}
	if n := CountKeys("# keys\nssh-ed25519 AAAA you@laptop\n\nfrom=\"10.0.0.0/8\" ecdsa-sha2-nistp256 AAAA\nnot a key\n"); n != 2 {
		t.Errorf("CountKeys %d", n)
	}
}

func TestParseRunning(t *testing.T) {
	got := fmt.Sprint(ParseRunning("sam tmux: server\nsam claude\nsam claude\nagent codex\nroot sshd\nsam bash\n"))
	if want := "[{agent codex 1} {sam claude 2} {sam tmux 1}]"; got != want {
		t.Errorf("running %s, want %s", got, want)
	}
}

func TestCollect(t *testing.T) {
	x := system.NewFake()
	x.AddFile("/etc/hostname", "nas-home\n", 0o644)
	x.AddFile("/etc/shadow", "sam:$y$abc:1::::::\n", 0o640)
	x.OK("systemd-detect-virt", "kvm\n")
	x.OK("ufw status verbose", "Status: inactive\n")
	x.OK("docker -H "+DockerSocket+" ps", "web\trunning\t0.0.0.0:8080->80/tcp\n")
	x.OK("docker -H "+DockerSocket+" inspect", "/web unless-stopped\n")
	x.OK("ss -Hltunp", "tcp LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:* users:((\"docker-proxy\",pid=9,fd=7))\ntcp LISTEN 0 50 0.0.0.0:445 0.0.0.0:* users:((\"smbd\",pid=1,fd=4))\n")
	x.OK("getent passwd", "sam:x:1001:1001::/home/sam:/bin/bash\n")
	x.OK("getent group", "sudo:x:27:sam\n")
	x.OK("sshd -T", "passwordauthentication yes\npermitrootlogin prohibit-password\n")
	x.OK("ps -eo", "sam tmux: server\n")
	inv := Collect(context.Background(), x)
	if inv.Hostname != "nas-home" || inv.Virt != "kvm" || inv.UFW.Active || !inv.Docker.Running || len(inv.Docker.Containers) != 1 ||
		!inv.SSHD.PasswordAuth || len(inv.Running) != 1 {
		t.Errorf("%+v", inv)
	}
	if len(inv.Listeners) != 2 || inv.Listeners[1].Container != "web" || inv.Listeners[1].Label != "container web (Docker)" {
		t.Errorf("listeners %+v", inv.Listeners)
	}
	if a, ok := inv.Account("sam"); !ok || !a.Password || !a.In("sudo") {
		t.Errorf("sam %+v", a)
	}
	for _, c := range x.Calls {
		if c.User != "" {
			t.Errorf("%s ran as a user", c)
		}
	}
}

// Before the first SSH connection after a boot, sshd -T refuses to run
// without /run/sshd. The inventory still reads sshd's config, so the first
// apply still stops for a password login it would cut off.
func TestCollectSSHDBeforeFirstConnection(t *testing.T) {
	x := system.NewFake()
	x.On("sshd -T", system.Result{ExitCode: 255, Stderr: "Missing privilege separation directory: /run/sshd\n"})
	x.OK("unshare --mount", "passwordauthentication yes\npermitrootlogin yes\n")
	inv := Collect(context.Background(), x)
	if !inv.SSHD.PasswordAuth || inv.SSHD.PermitRootLogin != "yes" {
		t.Errorf("sshd %+v", inv.SSHD)
	}
}

func TestCollectNothingThere(t *testing.T) {
	x := system.NewFake()
	inv := Collect(context.Background(), x)
	if inv.Docker.Running || inv.UFW.Active || len(inv.Listeners) != 0 || len(inv.Accounts) != 0 {
		t.Errorf("%+v", inv)
	}
}

func TestUFWAdmitsRanges(t *testing.T) {
	u := UFW{Admits: ParseUFWAdded("ufw allow 8000:8010/tcp\nufw allow from 192.168.1.0/24 to any port 27015:27030 proto udp comment 'x'\nufw allow 53\n")}
	for _, c := range []struct {
		port  int
		proto string
		want  bool
	}{{8000, "tcp", true}, {8010, "tcp", true}, {8011, "tcp", false}, {8005, "udp", false}, {27020, "udp", true}, {53, "udp", true}, {53, "tcp", true}} {
		if u.Admit(c.port, c.proto) != c.want {
			t.Errorf("%d/%s: %v, want %v (admits %v)", c.port, c.proto, !c.want, c.want, u.Admits)
		}
	}
}

// Known services are recognised by process, by systemd unit and by the
// image of a container, published or on the host network.
func TestCollectRecognisesServices(t *testing.T) {
	x := system.NewFake()
	x.OK("docker -H "+DockerSocket+" ps", "jellyfin\trunning\t0.0.0.0:8096->8096/tcp, 0.0.0.0:7359->7359/udp\tjellyfin/jellyfin:10.10.3\t0123456789ab\n"+
		"unifi\trunning\t\tlscr.io/linuxserver/unifi-network-application:9.0\tba9876543210\n")
	x.OK("docker -H "+DockerSocket+" inspect", "/jellyfin unless-stopped\n/unifi unless-stopped\n")
	x.OK("ss -Hltunp", strings.Join([]string{
		`tcp LISTEN 0 4096 0.0.0.0:8096 0.0.0.0:* users:(("docker-proxy",pid=90,fd=7))`,
		`udp UNCONN 0 0 0.0.0.0:7359 0.0.0.0:* users:(("docker-proxy",pid=91,fd=7))`,
		`tcp LISTEN 0 100 *:8443 *:* users:(("java",pid=200,fd=40))`,
		`udp UNCONN 0 0 *:10001 *:* users:(("java",pid=200,fd=41))`,
		`tcp LISTEN 0 100 *:9443 *:* users:(("portainer",pid=300,fd=8))`,
		`tcp LISTEN 0 100 *:8443 *:* users:(("java",pid=400,fd=40))`,
		`tcp LISTEN 0 100 0.0.0.0:8123 0.0.0.0:* users:(("python3",pid=500,fd=9))`,
		// The agent's own: a process it named jellyfin, and a user unit.
		`tcp LISTEN 0 100 0.0.0.0:8920 0.0.0.0:* users:(("jellyfin",pid=600,fd=9))`,
		`tcp LISTEN 0 100 0.0.0.0:1900 0.0.0.0:* users:(("python3",pid=700,fd=9))`,
	}, "\n"))
	x.AddFile("/proc/300/status", "Name:\tportainer\nUid:\t0\t0\t0\t0\n", 0o444)
	x.AddFile("/proc/600/status", "Name:\tjellyfin\nUid:\t1002\t1002\t1002\t1002\n", 0o444)
	x.AddFile("/proc/700/status", "Name:\tpython3\nUid:\t1002\t1002\t1002\t1002\n", 0o444)
	x.AddFile("/proc/700/cgroup", "0::/user.slice/user-1002.slice/user@1002.service/app.slice/jellyfin.service\n", 0o444)
	x.AddFile("/proc/200/cgroup", "0::/system.slice/docker-ba9876543210ffffffffffffffffffffffffffffffffffffffffffffffffffff.scope\n", 0o444)
	x.AddFile("/proc/500/cgroup", "0::/system.slice/system-home\\x2dassistant.slice/home-assistant@homeassistant.service\n", 0o444)
	inv := Collect(context.Background(), x)
	got := map[string]string{}
	for _, l := range inv.Listeners {
		got[l.Key()] = l.Service + "|" + l.Label + "|" + fmt.Sprint(l.Published())
	}
	for k, want := range map[string]string{
		"8096/tcp":  "jellyfin|Jellyfin (container jellyfin)|true",
		"7359/udp":  "jellyfin|Jellyfin (container jellyfin)|true",
		"8443/tcp":  "unifi|UniFi Network Application (container unifi, host network)|false",
		"10001/udp": "unifi|UniFi Network Application (container unifi, host network)|false",
		"9443/tcp":  "portainer|Portainer|false",
		"8123/tcp":  "home-assistant|Home Assistant (home-assistant@homeassistant.service)|false",
		// Not services: any account can choose those names.
		"8920/tcp": "|jellyfin (run by UID 1002, not a system service)|false",
		"1900/tcp": "|python3 (run by UID 1002, not a system service)|false",
	} {
		if got[k] != want {
			t.Errorf("%s: %q, want %q", k, got[k], want)
		}
	}
	if inv.Docker.Containers[0].Service != "jellyfin" || inv.Docker.Containers[1].Service != "unifi" {
		t.Errorf("containers %+v", inv.Docker.Containers)
	}
	if u, c := CgroupUnit("0::/user.slice/user-1001.slice/user@1001.service/app.slice/docker-0123456789ab.scope\n"); u != "" || c != "" {
		t.Errorf("CgroupUnit: %q %q", u, c)
	}
}
