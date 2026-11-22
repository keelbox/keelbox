//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/provider/hetzner"
)

// Closing public SSH (docs/adr/0021-close-public-ssh.md) against a
// stand-in tailnet: no Tailscale auth key is used. tailscale reports a
// running tailnet whose only address is 100.100.1.1 on tailscale0, which the
// test host cannot reach, so keelbox must leave SSH open. Then a path from
// the test host to 100.100.1.1:22 is built (LXD: a macvlan named tailscale0
// on the VM's network with a DNAT to 100.100.1.1; Hetzner: an SSH tunnel)
// and named in target.ssh.tailnet_host: apply over it proves the tailnet,
// and public SSH closes.

// fakeTailnet reports a running tailnet at 100.100.1.1 and passes anything
// else to the real tailscale.
const fakeTailnet = `#!/bin/sh
# keelbox e2e stand-in for tailscale (public SSH checks).
case "$1" in
status) printf '{"BackendState":"Running","Self":{"DNSName":"NAME.e2e-fake.ts.net.","TailscaleIPs":["100.100.1.1"]}}\n' ;;
*) exec /usr/bin/tailscale "$@" ;;
esac
`

func installFakeTailnet(t *testing.T, host, box string) {
	t.Helper()
	must(t, host, "admin", "sudo tee /usr/local/bin/tailscale >/dev/null <<'EOF'\n"+strings.ReplaceAll(fakeTailnet, "NAME", box)+"EOF\nsudo chmod 755 /usr/local/bin/tailscale")
	must(t, host, "admin", "ip link show tailscale0 >/dev/null 2>&1 || { sudo ip link add tailscale0 type dummy && sudo ip link set tailscale0 up; }; "+
		"ip -o addr show dev tailscale0 | grep -q 100.100.1.1 || sudo ip addr add 100.100.1.1/32 dev tailscale0")
}

func editConfig(t *testing.T, cfg string, edit func(string) string) {
	t.Helper()
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(edit(string(data))), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setTailnetHost(t *testing.T, cfg, hostPort string) {
	editConfig(t, cfg, func(s string) string {
		return strings.Replace(s, "\n  ssh:\n", "\n  ssh:\n    tailnet_host: \""+hostPort+"\"\n", 1)
	})
}

func publicPortOpen(host string) error {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, "22"), 6*time.Second)
	if err == nil {
		_ = c.Close()
	}
	return err
}

func recordFile(box string) string {
	return filepath.Join(home, ".config/keelbox/boxes", box+".json")
}

// noProofNoClose: the box is on the (stand-in) tailnet, but this machine
// cannot reach it there, so apply leaves public SSH open and says why.
func noProofNoClose(t *testing.T, host, cfg string) {
	t.Helper()
	out, err := kb(t, "apply", cfg)
	if err != nil || !strings.Contains(out, "public SSH stays open") || !strings.Contains(out, "cannot reach its sshd there") {
		t.Fatalf("apply on the tailnet without a path from here: %v\n%s", err, out)
	}
	must(t, host, "admin", "sudo ufw status", "22/tcp")
	must(t, host, "admin", "test ! -e /etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf && echo none", "none")
	if err := publicPortOpen(host); err != nil {
		t.Fatalf("public SSH closed without proof: %v", err)
	}
	logf(t, "PASS no proof, no close: %s", firstLine(out[strings.Index(out, "public SSH stays open"):]))
}

func publicSSHChecks(t *testing.T, host, box, cfg string) {
	t.Helper()
	defer os.Remove(recordFile(box))
	installFakeTailnet(t, host, box)
	noProofNoClose(t, host, cfg)

	// The stand-in path. tailscaled moves to userspace networking so it
	// drops its own tailscale0; a macvlan takes the name and an address on
	// the VM's network; packets to that address on port 22 go to
	// 100.100.1.1, so sshd sees the connection arrive at the tailnet address
	// on tailscale0, as over a real tailnet.
	must(t, host, "admin", `set -e
sudo sed -i 's/^FLAGS=.*/FLAGS="--tun=userspace-networking"/' /etc/default/tailscaled
grep -q '^FLAGS="--tun=userspace-networking"' /etc/default/tailscaled
sudo systemctl restart tailscaled
for i in $(seq 20); do ip link show tailscale0 >/dev/null 2>&1 || break; sleep 1; done
dev=$(ip -o route show default | awk '{print $5; exit}')
sudo ip link del tailscale0 2>/dev/null || true
sudo ip link add tailscale0 link "$dev" type macvlan mode bridge
sudo mkdir -p /run/systemd/network
printf '[Match]\nName=tailscale0\n[Network]\nDHCP=ipv4\nLinkLocalAddressing=no\nIPv6AcceptRA=no\n[DHCPv4]\nUseRoutes=no\nUseDNS=no\nSendHostname=no\nClientIdentifier=mac\n' | sudo tee /run/systemd/network/05-kb-e2e-tailnet.network >/dev/null
sudo networkctl reload
sudo ip link set tailscale0 up
sudo networkctl reconfigure tailscale0
sudo ip addr add 100.100.1.1/32 dev tailscale0`)
	var standIn string
	for i := 0; i < 30 && standIn == ""; i++ {
		time.Sleep(2 * time.Second)
		out, _ := sshRun(t, host, "admin", nil, "ip -4 -o addr show dev tailscale0 | awk '{print $4}' | cut -d/ -f1 | grep -v '^100.100.1.1$' | head -1")
		standIn = strings.TrimSpace(out)
	}
	if standIn == "" {
		t.Fatal("stand-in tailscale0 got no address")
	}
	must(t, host, "admin", "sudo nft -f - <<'EOF'\ntable ip kb_e2e_tailnet {\n  chain prerouting {\n    type nat hook prerouting priority dstnat; policy accept;\n"+
		"    iifname \"tailscale0\" ip daddr "+standIn+" tcp dport 22 dnat to 100.100.1.1\n  }\n}\nEOF")
	if out, err := sshRun(t, standIn, "admin", nil, "echo $SSH_CONNECTION"); err != nil || !strings.Contains(out, " 100.100.1.1 22") {
		t.Fatalf("stand-in tailnet path: %v\n%s", err, out)
	}
	logf(t, "stand-in tailnet: %s on tailscale0, DNAT to 100.100.1.1", standIn)

	// Proof: apply over the tailnet path, as the admin, closes public SSH.
	setTailnetHost(t, cfg, standIn)
	out, err := kb(t, "apply", cfg)
	if err != nil {
		t.Fatalf("apply over the stand-in tailnet: %v\n%s", err, out)
	}
	must(t, standIn, "admin", "sudo cat /var/lib/keelbox/public-ssh.json", `"state": "closed"`, `"address": "100.100.1.1"`)
	if ufw := must(t, standIn, "admin", "sudo ufw status"); strings.Contains(ufw, "22/tcp") {
		t.Errorf("22/tcp still allowed:\n%s", ufw)
	}
	if err := publicPortOpen(host); err == nil {
		t.Error("public SSH still reachable after the proof")
	} else {
		logf(t, "PASS public SSH closed after proof over the tailnet (%v)", err)
	}
	must(t, standIn, "admin", "sudo sshd -T -C user=admin,host=x,addr=192.0.2.1,laddr="+host+",lport=22 | grep -i denyusers", "denyusers *")
	logf(t, "PASS sshd refuses logins arriving at %s", host)

	// keelbox's own commands now go over the tailnet by themselves.
	for _, c := range [][]string{{"apply", cfg}, {"doctor", cfg}, {"status", cfg}, {"sessions", cfg}} {
		out, err := kb(t, c...)
		if err != nil {
			t.Fatalf("%s over the tailnet: %v\n%s", c[0], err, out)
		}
		switch c[0] {
		case "apply":
			if !strings.Contains(out, "No changes.") {
				t.Errorf("apply after closing changed something:\n%s", out)
			}
		case "doctor":
			if !strings.Contains(out, "closed: admin reached sshd over the tailnet") || !strings.Contains(out, "public-ssh-sshd") {
				t.Errorf("doctor does not report public SSH closed:\n%s", out)
			}
		case "status":
			if !strings.Contains(out, "closed: tailnet only") {
				t.Errorf("status:\n%s", out)
			}
		}
	}
	logf(t, "PASS apply, doctor, status and sessions over the tailnet")

	// public_ssh: lan keeps SSH from the private ranges: a UFW rule per
	// range and an sshd Match that lets LAN clients in, on the same proof.
	editConfig(t, cfg, func(s string) string { return s + "host:\n  firewall:\n    public_ssh: lan\n" })
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply with public_ssh: lan: %v\n%s", err, out)
	}
	must(t, standIn, "admin", "sudo ufw status", "keelbox allow ssh-lan", "192.168.0.0/16", "10.0.0.0/8")
	must(t, standIn, "admin", "sudo sshd -T -C user=admin,host=x,addr=192.0.2.1,laddr="+host+",lport=22 | grep -i denyusers", "denyusers *")
	if out, _ := sshRun(t, standIn, "admin", nil, "sudo sshd -T -C user=admin,host=x,addr=192.168.1.10,laddr="+host+",lport=22 | grep -ci denyusers; true"); strings.TrimSpace(out) != "0" {
		t.Errorf("sshd refuses a LAN client with public_ssh: lan: %s", out)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsPrivate() {
		// This machine is on the box's LAN (the LXD bridge).
		if out, err := sshRun(t, host, "admin", nil, "echo lan-ssh"); err != nil || !strings.Contains(out, "lan-ssh") {
			t.Errorf("SSH from the LAN with public_ssh: lan: %v %s", err, out)
		}
	} else if err := publicPortOpen(host); err == nil {
		t.Error("public SSH reachable from the internet with public_ssh: lan")
	}
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "closed to the internet, open to the LAN") {
		t.Errorf("doctor with public_ssh: lan: %v\n%s", err, out)
	}
	logf(t, "PASS public_ssh: lan keeps SSH from the LAN and refuses the rest")

	// Reopen on request.
	editConfig(t, cfg, func(s string) string { return strings.Replace(s, "public_ssh: lan", "public_ssh: open", 1) })
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply with public_ssh: open: %v\n%s", err, out)
	}
	if err := publicPortOpen(host); err != nil {
		t.Errorf("public SSH not reopened: %v", err)
	}
	must(t, host, "admin", "sudo ufw status", "22/tcp")
	logf(t, "PASS public_ssh: open reopens public SSH")
}

// remoteControlChecks: with sessions.remote_control enabled and nobody
// signed in, the service is installed, skipped (not failing or looping),
// and doctor and sessions say it waits for the sign-in.
func remoteControlChecks(t *testing.T, host, cfg string) {
	t.Helper()
	must(t, host, "agent", "systemctl --user is-enabled keelbox-claude-remote-control.service", "enabled")
	st := must(t, host, "agent", "systemctl --user show -p ActiveState,Result,NRestarts keelbox-claude-remote-control.service")
	if !strings.Contains(st, "ActiveState=inactive") || !strings.Contains(st, "NRestarts=0") {
		t.Errorf("remote control unit before sign-in:\n%s", st)
	}
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "waiting for your Claude Code sign-in") {
		t.Errorf("doctor before sign-in: %v\n%s", err, out)
	}
	if out, err := kb(t, "sessions", cfg); err != nil || !strings.Contains(out, "Claude Code Remote Control: waiting for your Claude Code sign-in") {
		t.Errorf("sessions before sign-in: %v\n%s", err, out)
	}
	logf(t, "PASS remote control installed and waiting for sign-in (%s)", strings.Join(strings.Fields(st), " "))
}

// TestHetznerPublicSSH: the Cloud Firewall is created with the server and
// admits SSH, apply restores it after tampering, SSH stays open without
// proof, the proof over the tailnet (a tunnel standing in) removes 22 from
// the Cloud Firewall and UFW, and destroy deletes server and firewall.
func TestHetznerPublicSSH(t *testing.T) {
	if os.Getenv("KEELBOX_E2E_TARGET") != "hetzner" {
		t.Skip("set KEELBOX_E2E_TARGET=hetzner")
	}
	h := newHetzner(t)
	ensureKey(t)
	build(t)
	box := fmt.Sprintf("%s%d", VMPrefix, time.Now().Unix()%1000000)
	ctx := context.Background()
	targetYAML, _, _ := h.Create(t, box)
	destroyed := false
	defer func() {
		if !destroyed {
			h.Destroy(t, box)
		} else {
			h.sweep(t)
		}
		os.Remove(recordFile(box))
		logf(t, "estimated spend: %s", h.Spend())
	}()
	cfg := writeConfig(t, box, targetYAML)
	out, err := kb(t, "create", cfg)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	host := hostFromCreate(t, out, "")
	if !strings.Contains(out, "public SSH stays open") {
		t.Errorf("create does not say public SSH stays open:\n%s", out)
	}
	srv, err := hetzner.Find(ctx, h.cl, box)
	if err != nil {
		t.Fatal(err)
	}
	fw := func() *hetzner.Firewall {
		f, err := hetzner.FindFirewall(ctx, h.cl, box)
		if err != nil || f == nil {
			t.Fatalf("firewall: %v %v", f, err)
		}
		return f
	}
	f := fw()
	if !f.AppliedToServer(srv.ID) || !hetzner.SameRules(f.Rules, hetzner.FirewallRules(true)) || f.Labels[hetzner.LabelManaged] != "true" {
		t.Fatalf("firewall after create: %+v", f)
	}
	logf(t, "PASS create: firewall %s attached: %s", f.Name, hetzner.DescribeRules(f.Rules))

	// Tamper: an extra rule and detached. apply puts it back.
	extra := append(hetzner.FirewallRules(true), hetzner.FirewallRule{Direction: "in", Protocol: "tcp", Port: "80", SourceIPs: []string{"0.0.0.0/0"}})
	if acts, err := h.cl.SetFirewallRules(ctx, f.ID, extra); err != nil || h.cl.WaitActions(ctx, acts, 2*time.Second) != nil {
		t.Fatal("tamper rules:", err)
	}
	if acts, err := h.cl.RemoveFirewall(ctx, f.ID, []hetzner.FirewallResource{hetzner.ServerResource(srv.ID)}); err != nil || h.cl.WaitActions(ctx, acts, 2*time.Second) != nil {
		t.Fatal("detach:", err)
	}
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply after tampering: %v\n%s", err, out)
	}
	if f = fw(); !f.AppliedToServer(srv.ID) || !hetzner.SameRules(f.Rules, hetzner.FirewallRules(true)) {
		t.Fatalf("not reconciled: %+v", f)
	}
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "cloud-firewall") {
		t.Errorf("doctor: %v\n%s", err, out)
	}
	logf(t, "PASS apply reconciles the Cloud Firewall")

	// On the tailnet, but not reachable from here: stays open.
	installFakeTailnet(t, host, box)
	noProofNoClose(t, host, cfg)
	if f = fw(); !hetzner.HasPublicSSH(f) {
		t.Fatal("Cloud Firewall dropped SSH without proof")
	}

	// The proof, over a tunnel to the box's tailnet address. The forward ends
	// at a relay that root runs on the box: only root may open SSH to the
	// box's own addresses, so a forward that ended at sshd would be refused.
	must(t, host, "admin", "sudo systemd-run --quiet --collect --unit kb-e2e-relay "+
		"systemd-socket-activate -l 127.0.0.1:2222 /usr/lib/systemd/systemd-socket-proxyd 100.100.1.1:22")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	tunnel := exec.Command("ssh", "-N", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=10", "-o", "ExitOnForwardFailure=yes", "-i", keyPath, "-o", "IdentitiesOnly=yes",
		"-L", "127.0.0.1:"+strconv.Itoa(port)+":127.0.0.1:2222", "admin@"+host)
	if err := tunnel.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tunnel.Process.Kill(); _ = tunnel.Wait() }()
	for i := 0; i < 20; i++ {
		if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
			c.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	setTailnetHost(t, cfg, "127.0.0.1:"+strconv.Itoa(port))
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply over the tunnel: %v\n%s", err, out)
	}
	if f = fw(); hetzner.HasPublicSSH(f) || !hetzner.SameRules(f.Rules, hetzner.FirewallRules(false)) {
		t.Fatalf("Cloud Firewall after proof: %s", hetzner.DescribeRules(f.Rules))
	}
	time.Sleep(5 * time.Second)
	if err := publicPortOpen(host); err == nil {
		t.Error("public SSH reachable after the proof")
	} else {
		logf(t, "PASS public SSH closed (Cloud Firewall %s; %v)", hetzner.DescribeRules(f.Rules), err)
	}
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "no public SSH") {
		t.Errorf("doctor after closing: %v\n%s", err, out)
	} else {
		logf(t, "PASS doctor over the tailnet path: public SSH and Cloud Firewall closed")
	}

	if out, err := kb(t, "destroy", cfg, "--yes", "--discard-work"); err != nil || !strings.Contains(out, "deleted firewall") {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	destroyed = true
	if f, err := hetzner.FindFirewall(ctx, h.cl, box); err != nil || f != nil {
		t.Errorf("firewall left after destroy: %v %v", f, err)
	}
	if _, err := os.Stat(recordFile(box)); err == nil {
		t.Error("tailnet record left after destroy")
	}
	logf(t, "PASS destroy deletes the server and its Cloud Firewall")
}
