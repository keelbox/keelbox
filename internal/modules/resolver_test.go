package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/inventory"
	"github.com/keelbox/keelbox/internal/system"
)

// resolvedDir is systemd-resolved's directory, its own account's.
func resolvedDir(s *sim, files map[string]string) {
	s.Files["/run/systemd/resolve"] = &system.FakeFile{Dir: true, Mode: 0o755, Owner: "systemd-resolve", Group: "systemd-resolve"}
	for name, data := range files {
		s.Files["/run/systemd/resolve/"+name] = &system.FakeFile{Data: []byte(data), Mode: 0o644, Owner: "systemd-resolve", Group: "systemd-resolve"}
	}
}

func resolverFindings(t *testing.T, s *sim, extra string) []engine.Finding {
	t.Helper()
	return findingsOf(t, context.Background(), simEnv(t, s, testConfig(t, extra)), "resolver")
}

// A box resolves names through a stub on the box, or through a resolver
// that is not link-local, or through one of several: doctor has nothing to
// say. Ubuntu's images are the first case.
func TestDoctorResolverWithNamesForEveryAccount(t *testing.T) {
	for name, setup := range map[string]func(s *sim){
		"no resolv.conf": func(s *sim) {},
		"systemd-resolved's stub, through its link": func(s *sim) {
			resolvedDir(s, map[string]string{"stub-resolv.conf": "nameserver 127.0.0.53\noptions edns0 trust-ad\n", "resolv.conf": "nameserver 169.254.169.254\n"})
			_ = s.Symlink("../run/systemd/resolve/stub-resolv.conf", resolvConfPath)
		},
		"a resolver on the network": func(s *sim) { s.AddFile(resolvConfPath, "# by hand\nnameserver 10.0.0.2\n", 0o644) },
		"a link-local resolver and another": func(s *sim) {
			s.AddFile(resolvConfPath, "nameserver 169.254.169.254\nnameserver 10.0.0.2\n", 0o644)
		},
		"a file with no resolver": func(s *sim) { s.AddFile(resolvConfPath, "search example.internal\n", 0o644) },
		"a FIFO in its place": func(s *sim) {
			s.Files[resolvConfPath] = &system.FakeFile{Special: true, Mode: 0o644, Owner: "root", Group: "root"}
		},
		"a link to a FIFO of another account's": func(s *sim) {
			resolvedDir(s, nil)
			s.Files["/run/systemd/resolve/resolv.conf"] = &system.FakeFile{Special: true, Mode: 0o644, Owner: "systemd-resolve", Group: "systemd-resolve"}
			_ = s.Symlink("/run/systemd/resolve/resolv.conf", resolvConfPath)
		},
		"a link to a link of another account's": func(s *sim) {
			resolvedDir(s, nil)
			s.Files["/run/systemd/resolve/resolv.conf"] = &system.FakeFile{Target: "/etc/shadow", Mode: 0o777, Owner: "systemd-resolve", Group: "systemd-resolve"}
			_ = s.Symlink("/run/systemd/resolve/resolv.conf", resolvConfPath)
		},
	} {
		s := newSim(t)
		setup(s)
		if f := resolverFindings(t, s, ""); len(f) != 0 {
			t.Errorf("%s: %+v", name, f)
		}
	}
}

// A resolv.conf that names only link-local resolvers leaves every account
// but root without names once the limits are loaded. Doctor fails, names
// both ways out and ends with the command for the stub; with accounts
// named in the config it warns instead, since the agent still has none.
func TestDoctorResolverLinkLocalOnly(t *testing.T) {
	s := newSim(t)
	s.AddFile(resolvConfPath, "search c.project.internal\nnameserver 169.254.169.254\n", 0o644)
	f := resolverFindings(t, s, "")
	if len(f) != 1 || f[0].Level != engine.LevelFail {
		t.Fatalf("doctor: %+v", f)
	}
	for _, want := range []string{
		"/etc/resolv.conf names only the link-local resolver 169.254.169.254, which the network limits leave to root: agent and every other account resolve no names",
		"apt's downloads (account _apt)",
		"name the accounts that must resolve in host.firewall.link_local_accounts (never the agent) and keelbox apply testbox",
	} {
		if !strings.Contains(f[0].Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, f[0].Detail)
		}
	}
	if !strings.HasSuffix(f[0].Detail, "or use systemd-resolved's stub, which every account reaches: sudo systemctl enable --now systemd-resolved && sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf") {
		t.Errorf("detail does not end with the command:\n%s", f[0].Detail)
	}

	f = resolverFindings(t, s, "host:\n  firewall:\n    link_local_accounts: [_apt, deploy]\n")
	if len(f) != 1 || f[0].Level != engine.LevelWarn || !strings.Contains(f[0].Detail, "leave to root and _apt, deploy: agent and every other account") {
		t.Errorf("doctor with accounts named: %+v", f)
	}

	// systemd-resolved without its stub: the file is resolved's own, read
	// as that account. Google Cloud's resolver, and a router's.
	s = newSim(t)
	resolvedDir(s, map[string]string{"resolv.conf": "nameserver 169.254.169.254\nnameserver fe80::1%eth0\n"})
	_ = s.Symlink("/run/systemd/resolve/resolv.conf", resolvConfPath)
	if f := resolverFindings(t, s, ""); len(f) != 1 || f[0].Level != engine.LevelFail || !strings.Contains(f[0].Detail, "link-local resolvers 169.254.169.254, fe80::1, which") {
		t.Errorf("doctor on resolved's own file: %+v", f)
	}

	// The C library reads three: a fourth that is not link-local is never asked.
	s = newSim(t)
	s.AddFile(resolvConfPath, "nameserver 169.254.169.253\nnameserver 169.254.169.254\nnameserver fe80::1\nnameserver 10.0.0.2\n", 0o644)
	if f := resolverFindings(t, s, ""); len(f) != 1 || f[0].Level != engine.LevelFail {
		t.Errorf("doctor with a fourth resolver: %+v", f)
	}
}

func impactItem(items []engine.ImpactItem, subject string) (engine.ImpactItem, bool) {
	for _, it := range items {
		if it.Module == "host" && it.Subject == subject {
			return it, true
		}
	}
	return engine.ImpactItem{}, false
}

// On a machine in use the impact report states the rule, once, with the
// key that keeps an account's access. A new server gets no such line.
func TestImpactStatesTheNetworkLimitsOnAMachineInUse(t *testing.T) {
	it, ok := impactItem(impactOf(t, homeServer(t), ""), "network")
	if !ok || it.Group != engine.ImpactChanges || it.Gate ||
		!strings.Contains(it.Detail, "accounts other than root can no longer connect to link-local addresses") ||
		!strings.Contains(it.Detail, "or to this machine's own SSH port") ||
		!strings.Contains(it.Hint, "host.firewall.link_local_accounts: [NAME] keeps link-local addresses for an account") {
		t.Errorf("on a machine in use: %+v %v", it, ok)
	}

	s := newSim(t)
	s.OK("getent passwd", "root:x:0:0:root:/root:/bin/bash\nubuntu:x:1000:1000::/home/ubuntu:/bin/bash\n")
	s.OK("ss -Hltunp", "tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n")
	for _, subject := range []string{"network", "dns"} {
		if it, ok := impactItem(impactOf(t, s, ""), subject); ok {
			t.Errorf("a new server: %+v", it)
		}
	}
}

// What is connected to a link-local address when the report looks is named
// with its account: cut off, with the config that keeps it, or kept when
// the config names the account. Root's sockets, and DNS, time and DHCP of
// Ubuntu's own services, are not listed.
func TestImpactNamesWhatTalksToLinkLocalNow(t *testing.T) {
	seed := func() *sim {
		s := homeServer(t)
		s.OK("getent passwd", "root:x:0:0:root:/root:/bin/bash\nubuntu:x:1000:1000::/home/ubuntu:/bin/bash\nsam:x:1001:1001::/home/sam:/bin/bash\n"+
			"do-agent:x:115:115::/nonexistent:/usr/sbin/nologin\nsystemd-resolve:x:992:992::/:/usr/sbin/nologin\nsystemd-network:x:998:998::/:/usr/sbin/nologin\n")
		s.connected = "tcp ESTAB 0 0 10.0.0.5:44321 169.254.169.254:80 users:((\"do-agent\",pid=700,fd=9)) uid:115 ino:1 sk:1 <->\n" +
			"tcp ESTAB 0 0 10.0.0.5:44322 169.254.169.254:80 users:((\"do-agent\",pid=700,fd=10)) uid:115 ino:2 sk:2 <->\n" +
			"udp ESTAB 0 0 10.0.0.5:5353 169.254.169.254:53 users:((\"systemd-resolve\",pid=300,fd=12)) uid:992 ino:3 sk:3 <->\n" +
			"udp ESTAB 0 0 10.0.0.5%eth0:68 169.254.169.254:67 users:((\"systemd-network\",pid=200,fd=20)) uid:998 ino:4 sk:4 <->\n" +
			"tcp ESTAB 0 0 10.0.0.5:44400 169.254.169.254:80 users:((\"google_guest_ag\",pid=400,fd=7)) ino:5 sk:5 <->\n" +
			"tcp ESTAB 0 0 [fe80::5]:44401 [fe80::1]:8080 users:((\"python3\",pid=2,fd=5)) uid:1001 ino:6 sk:6 <->\n" +
			"tcp ESTAB 0 0 10.0.0.5:22 10.0.0.9:50000 users:((\"sshd\",pid=5,fd=4)) ino:7 sk:7 <->\n"
		return s
	}
	items := impactOf(t, seed(), "")
	it, ok := impactItem(items, "do-agent")
	if !ok || it.Group != engine.ImpactCuts || it.Gate || !strings.Contains(it.Detail, "runs as do-agent and is connected to the link-local address 169.254.169.254:80 now") ||
		it.Hint != "host.firewall.link_local_accounts: [do-agent] keeps them for it" {
		t.Errorf("a service that reads metadata: %+v %v", it, ok)
	}
	if it, ok := impactItem(items, "python3"); !ok || it.Group != engine.ImpactCuts || !strings.Contains(it.Detail, "runs as sam and is connected to the link-local address [fe80::1]:8080 now") {
		t.Errorf("a program of a login account's: %+v %v", it, ok)
	}
	n := 0
	for _, it := range items {
		if strings.Contains(it.Detail, "connected to") {
			n++
		}
		if it.Subject == "systemd-resolve" || it.Subject == "systemd-network" || it.Subject == "google_guest_ag" || it.Subject == "sshd" && strings.Contains(it.Detail, "link-local") {
			t.Errorf("listed though it keeps its access: %+v", it)
		}
	}
	if n != 2 {
		t.Errorf("%d connections listed, want 2: %v", n, describe(items))
	}
	if _, ok := impactItem(items, "network"); !ok {
		t.Error("the rule itself is not stated")
	}

	kept, ok := impactItem(impactOf(t, seed(), "host:\n  firewall:\n    link_local_accounts: [do-agent]\n"), "do-agent")
	if !ok || kept.Group != engine.ImpactKeeps || !strings.Contains(kept.Detail, "do-agent keeps link-local addresses (host.firewall.link_local_accounts)") {
		t.Errorf("with the account named in the config: %+v %v", kept, ok)
	}
}

// A machine that resolves names at a link-local resolver directly: the
// first apply stops for it, since the apply's own downloads would fail
// after the limits load.
func TestImpactGatesALinkLocalResolver(t *testing.T) {
	s := homeServer(t)
	s.AddFile(resolvConfPath, "nameserver 169.254.169.254\n", 0o644)
	it, ok := impactItem(impactOf(t, s, ""), "dns")
	if !ok || it.Group != engine.ImpactCuts || !it.Gate ||
		!strings.Contains(it.Detail, "/etc/resolv.conf names only the link-local resolver 169.254.169.254") ||
		!strings.Contains(it.Detail, "agent, apt's downloads and every other account would resolve no names") ||
		!strings.HasSuffix(it.Hint, "sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf") {
		t.Errorf("a link-local resolver: %+v %v", it, ok)
	}

	// The same file on a new server gates too: nothing else there does.
	n := newSim(t)
	n.OK("getent passwd", "root:x:0:0:root:/root:/bin/bash\n")
	n.AddFile(resolvConfPath, "nameserver 169.254.169.254\n", 0o644)
	if g := engine.Gated(impactOf(t, n, "")); len(g) != 1 || g[0].Subject != "dns" {
		t.Errorf("a new server with a link-local resolver: %+v", g)
	}
}

// Once the table for every account is loaded the report has nothing more
// to say about it; over the table of an older keelbox it does.
func TestImpactOfTheNetworkLimitsAfterTheFirstApply(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	// The accounts keelbox made are login accounts the inventory finds.
	s.OK("getent passwd", "root:x:0:0:root:/root:/bin/bash\nubuntu:x:1000:1000::/home/ubuntu:/bin/bash\nadmin:x:1001:1001::/home/admin:/bin/bash\nagent:x:1002:1002::/home/agent:/bin/bash\n")
	impact := func() []engine.ImpactItem {
		_, items := engine.Impact(ctx, e, All(), inventory.Collect(ctx, s))
		return items
	}
	if it, ok := impactItem(impact(), "network"); ok {
		t.Errorf("with the limits loaded: %+v", it)
	}
	// A rule of the loaded table changes: still nothing to say.
	e.SSHAddrs = "203.0.113.7:22"
	if it, ok := impactItem(impact(), "network"); ok {
		t.Errorf("with one more outside address to record: %+v", it)
	}
	e.SSHAddrs = ""
	old := olderAgentNetRules(simAgentUIDs(t, s), nil)
	s.AddFile(agentNetRules, old, 0o644)
	s.nftLoaded = old
	if _, ok := impactItem(impact(), "network"); !ok {
		t.Error("over an older keelbox's table the report does not state the rule")
	}
}
