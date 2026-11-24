package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/ui"
)

// homeServer is what the probe prints on a home server running Plex,
// Samba and Jellyfin in Docker, from sudo ss -Hltunp.
const homeServer = `os=ubuntu 24.04
hostname=nas
user=sam
timezone=Europe/London
locale=en_GB.UTF-8
sudo=yes
cloud_users=
sessions=0
listen=udp UNCONN 0 0 0.0.0.0:5353 0.0.0.0:* users:(("avahi-daemon",pid=811,fd=12))
listen=udp UNCONN 0 0 127.0.0.53%lo:53 0.0.0.0:* users:(("systemd-resolve",pid=700,fd=13))
listen=udp UNCONN 0 0 0.0.0.0%eth0:68 0.0.0.0:* users:(("systemd-network",pid=650,fd=22))
listen=udp UNCONN 0 0 0.0.0.0:41641 0.0.0.0:* users:(("tailscaled",pid=900,fd=9))
listen=udp UNCONN 0 0 192.168.1.20:137 0.0.0.0:* users:(("nmbd",pid=1200,fd=17))
listen=tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=1,fd=3))
listen=tcp LISTEN 0 4096 [::]:22 [::]:* users:(("sshd",pid=1,fd=4))
listen=tcp LISTEN 0 50 0.0.0.0:445 0.0.0.0:* users:(("smbd",pid=1300,fd=44))
listen=tcp LISTEN 0 50 [::]:445 [::]:* users:(("smbd",pid=1300,fd=45))
listen=tcp LISTEN 0 4096 *:32400 *:* users:(("Plex Media Serv",pid=1400,fd=31))
listen=tcp LISTEN 0 4096 127.0.0.1:32401 0.0.0.0:* users:(("Plex Media Serv",pid=1400,fd=32))
listen=tcp LISTEN 0 4096 0.0.0.0:8096 0.0.0.0:* users:(("docker-proxy",pid=1500,fd=7))
listen=tcp LISTEN 0 4096 100.101.102.103:8080 0.0.0.0:* users:(("tailscaled",pid=900,fd=20))
listen=tcp LISTEN 0 4096 [fe80::1%eth0]:9000 [::]:*
listen=tcp LISTEN 0 4096 0.0.0.0:9100 0.0.0.0:*
listen=tcp LISTEN 0 511 *:3000 *:* users:(("node-MainThread",pid=1600,fd=21))
listen=tcp LISTEN 0 32 10.0.3.1:53 0.0.0.0:* users:(("dnsmasq",pid=1700,fd=5))
container=jellyfin 0.0.0.0:8096->8096/tcp, [::]:8096->8096/tcp
container=db 127.0.0.1:5432->5432/tcp
`

func TestProbeFindsWhatTheFirewallBlocks(t *testing.T) {
	f := parseProbe(homeServer)
	if f.OSID != "ubuntu" || f.OSVersion != "24.04" || f.Hostname != "nas" || f.User != "sam" || !f.Sudo || f.Desktop != "" {
		t.Errorf("facts %+v", f)
	}
	if f.Timezone != "Europe/London" || f.Locale != "en_GB.UTF-8" {
		t.Errorf("clock and language: %q %q", f.Timezone, f.Locale)
	}
	var got []string
	for _, l := range f.Listeners {
		got = append(got, l.Key()+" "+l.Name+" "+l.Label)
	}
	want := []string{
		"53/tcp dnsmasq dnsmasq",
		"445/tcp samba Samba file sharing (smbd)",
		"3000/tcp node a Node app (node-MainThread)",
		"8096/tcp jellyfin container jellyfin (Docker)",
		"9100/tcp port-9100 unknown (keelbox could not see the process)",
		"32400/tcp plex Plex Media Server",
		"137/udp samba Samba file sharing (nmbd)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("listeners:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if err := f.supported(); err != nil {
		t.Error(err)
	}
}

func TestProbeDesktopAndUnsupported(t *testing.T) {
	if f := parseProbe("os=ubuntu 24.04\nsessions=0\n"); f.Desktop != "" {
		t.Errorf("server: %q", f.Desktop)
	}
	if f := parseProbe("os=ubuntu 24.04\nsessions=2\n"); f.Desktop != "a desktop is installed" {
		t.Errorf("desktop: %q", f.Desktop)
	}
	if f := parseProbe("os=ubuntu 24.04\ndisplay_manager=gdm3\n"); !strings.Contains(f.Desktop, "gdm3") {
		t.Errorf("display manager: %q", f.Desktop)
	}
	for probe, want := range map[string]string{
		"os=debian 12\n":    "this is debian 12",
		"os=ubuntu 20.04\n": "do-release-upgrade",
		"":                  "not Linux",
	} {
		if err := parseProbe(probe).supported(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", probe, err, want)
		}
	}
}

func TestPickPorts(t *testing.T) {
	f := parseProbe(homeServer)
	cs := lanCandidates(&answers{facts: &f})
	keys := func(cs []lanChoice) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Key)
		}
		return strings.Join(out, " ")
	}
	// Plex and Samba are known services: one choice each, Samba's TCP and
	// UDP ports together.
	if got := keys(cs); got != "plex samba 53/tcp 3000/tcp 8096/tcp 9100/tcp" {
		t.Fatalf("choices %q", got)
	}
	if strings.Join(cs[1].Ports, " ") != "445/tcp 137/udp" || cs[1].Label != "Samba file sharing (smbd)" {
		t.Errorf("samba: %+v", cs[1])
	}
	for answer, want := range map[string]string{
		"":           "plex samba 53/tcp 3000/tcp 8096/tcp 9100/tcp",
		"all":        "plex samba 53/tcp 3000/tcp 8096/tcp 9100/tcp",
		"none":       "",
		"32400, 445": "plex samba",
		"samba 8096": "samba 8096/tcp",
		"137/udp":    "samba",
	} {
		got, err := pickPorts(answer, cs)
		if err != nil || keys(got) != want {
			t.Errorf("%q: %q %v, want %q", answer, keys(got), err, want)
		}
	}
	if _, err := pickPorts("80", cs); err == nil || !strings.Contains(err.Error(), "80 is not one of those listed (plex, samba, 53/tcp,") {
		t.Errorf("unknown port: %v", err)
	}
	rules := keepRules(cs)
	if len(rules) != 6 || rules[0].Service != "plex" || rules[1].Service != "samba" || rules[2].Name != "dnsmasq" || rules[2].Port != config.Port(53) {
		t.Errorf("rules %+v", rules)
	}
	// Two services with one name get the port added to the second.
	dup := allowRules([]listener{{Port: 80, Proto: "tcp", Name: "nginx"}, {Port: 443, Proto: "tcp", Name: "nginx"}})
	if dup[0].Name != "nginx" || dup[1].Name != "nginx-443" {
		t.Errorf("names %+v", dup)
	}
}

func TestSSHProblemSaysWhatToDo(t *testing.T) {
	for stderr, want := range map[string]string{
		"Permission denied (publickey).":                              "does not accept your SSH key for ubuntu",
		"@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@":    "ssh-keygen -R 10.0.0.5 -f ~/.config/keelbox/known_hosts",
		"ssh: connect to host 10.0.0.5 port 22: Connection refused":   "systemctl status ssh",
		"ssh: connect to host 10.0.0.5 port 22: Connection timed out": "does not answer on port 22",
		"dial tcp 10.0.0.5:22: i/o timeout":                           "does not answer on port 22; check it is on and reachable from here: ssh ubuntu@10.0.0.5",
		"dial tcp 10.0.0.5:22: connect: no route to host":             "does not answer on port 22",
		"dial tcp 10.0.0.5:22: connect: connection refused":           "refuses SSH on port 22",
		"ssh-keyscan 10.0.0.5: exit status 1":                         "does not answer on port 22",
		"ssh: Could not resolve hostname nas.local":                   "does not resolve",
		"dial tcp: lookup nas.local: no such host":                    "does not resolve",
		"kex_exchange_identification: read: Connection reset":         "ssh ubuntu@10.0.0.5 true",
	} {
		isolate(t)
		err := sshProblem(errors.New(stderr), "ubuntu", "10.0.0.5", 22)
		if got := err.Error(); !strings.Contains(got, want) {
			t.Errorf("%s:\n%s\nwant %q", stderr, got, want)
		}
		var refused *keyRefused
		if errors.As(err, &refused) != strings.Contains(stderr, "Permission denied") {
			t.Errorf("%s: a key problem is %v", stderr, err)
		}
	}
}

// A problem with the server's host key is not answered with a hint about
// the user's own key.
func TestSSHProblemLeavesHostKeyErrorsAlone(t *testing.T) {
	hk := &provider.HostKeyError{Host: "10.0.0.5"}
	if err := sshProblem(hk, "ubuntu", "10.0.0.5", 22); err != error(hk) {
		t.Errorf("a host key error was rewritten: %v", err)
	}
	err := sshProblem(provider.ErrHostKeyUnconfirmed, "ubuntu", "10.0.0.5", 22)
	if got := err.Error(); !strings.Contains(got, "the host key of 10.0.0.5 was not confirmed, so keelbox did not connect") || strings.Contains(got, "Check your key logs in") {
		t.Errorf("declined at the prompt: %s", got)
	}
}

func TestMachineChangesSayWhatIsInstalled(t *testing.T) {
	w := &wizard{}
	a := &answers{via: viaLocal, starter: config.Starter{NoTailscale: true, Agents: []string{}}}
	if got := strings.Join(w.machineChanges(a), "\n"); !strings.Contains(got, "New accounts admin and agent; Docker and Node installed.") {
		t.Errorf("no agents, no Tailscale:\n%s", got)
	}
	a.starter = config.Starter{}
	if got := strings.Join(w.machineChanges(a), "\n"); !strings.Contains(got, "Docker, Node, Tailscale and the agent CLIs installed.") {
		t.Errorf("defaults:\n%s", got)
	}
}

func TestAdminConfirmedIsOneLine(t *testing.T) {
	log := "host\n  ok      packages\n  change  cloud-user-locked: lock cloud default account ubuntu (SSH and sudo)\n\nApplied 1 change in 2s.\n"
	if got := adminConfirmed(log, "admin", "ubuntu", ui.Style{}); got != "  ok admin logs in with your key and uses sudo; ubuntu locked (1 change)" {
		t.Errorf("%q", got)
	}
	log = "host\n  change  bootstrap-sudoers: remove /etc/sudoers.d/90-keelbox-bootstrap, which gives sam sudo without a password\n\nApplied 1 change in 2s.\n" +
		"Removed /etc/sudoers.d/90-keelbox-bootstrap, which gives sam sudo without a password; sudo asks for the password again.\n"
	if got := adminConfirmed(log, "admin", "sam", ui.Style{}); got != "  ok admin logs in with your key and uses sudo (1 change)\n  Removed /etc/sudoers.d/90-keelbox-bootstrap, which gives sam sudo without a password; sudo asks for the password again." {
		t.Errorf("%q", got)
	}
}

// attach starts the session's own shell: the box's welcome is printed by
// that shell, once, and attach adds no line of its own beside it.
func TestAttachLeavesTheWelcomeToTheBox(t *testing.T) {
	if got := attachCommand("main"); got != "infocmp >/dev/null 2>&1 || export TERM=xterm-256color; tmux new-session -A -s main" {
		t.Errorf("%s", got)
	}
}

// A terminal the box has no terminfo entry for still gets a session: the
// command falls back to xterm-256color, and keeps a TERM the box knows.
func TestAttachFallsBackForAnUnknownTerminal(t *testing.T) {
	if _, err := exec.LookPath("infocmp"); err != nil {
		t.Skip("no infocmp here")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\necho \"$TERM\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for term, want := range map[string]string{"xterm-keelbox-unknown": "xterm-256color", "xterm": "xterm"} {
		c := exec.Command("sh", "-c", attachCommand("main"))
		c.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "TERM=" + term, "TERMINFO_DIRS=", "HOME=" + t.TempDir()}
		out, err := c.Output()
		if err != nil {
			t.Fatalf("TERM=%s: %v", term, err)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("TERM=%s: session got TERM=%s, want %s", term, got, want)
		}
	}
}

func TestSelfUpdateNamesTheBoxCopy(t *testing.T) {
	dir := t.TempDir()
	box := dir + "/keelbox"
	if boxCopyNote(dir+"/mine", box) != "" {
		t.Error("note without a box copy")
	}
	_ = os.WriteFile(box, nil, 0o755)
	if got := boxCopyNote(dir+"/mine", box); !strings.Contains(got, box+", is replaced by the next apply: keelbox apply") {
		t.Errorf("%q", got)
	}
	if boxCopyNote(box, box) != "" {
		t.Error("note for the box copy itself")
	}
}
