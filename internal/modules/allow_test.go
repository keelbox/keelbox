package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

// host.firewall.allow converges UFW rules from each LAN range, the Docker
// guard's matching exceptions, and removes keelbox's rules when an entry
// goes, leaving rules keelbox did not add.
func TestFirewallAllow(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, "host:\n  firewall:\n    allow:\n      - {name: plex, port: 32400}\n      - {name: netbios, port: 137, proto: udp, from: 192.168.1.0/24}\n"))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	added := strings.Join(s.ufwAdded, "\n")
	for _, want := range []string{
		"ufw allow from 192.168.0.0/16 to any port 32400 proto tcp comment 'keelbox allow plex'",
		"ufw allow from fc00::/7 to any port 32400 proto tcp comment 'keelbox allow plex'",
		"ufw allow from 192.168.1.0/24 to any port 137 proto udp comment 'keelbox allow netbios'",
	} {
		if !strings.Contains(added, want) {
			t.Errorf("missing rule %q in:\n%s", want, added)
		}
	}
	if len(s.ufwAdded) != 5 {
		t.Errorf("%d rules, want 4 LAN ranges for plex and 1 for netbios:\n%s", len(s.ufwAdded), added)
	}
	guard, _ := s.ReadFile(dockerGuardScript)
	for _, want := range []string{
		"$ipt -A KEELBOX-DOCKER -s 10.0.0.0/8 -p tcp -m conntrack --ctorigdstport 32400 -j RETURN",
		"$ipt -A KEELBOX-DOCKER -s fc00::/7 -p tcp -m conntrack --ctorigdstport 32400 -j RETURN",
		"$ipt -A KEELBOX-DOCKER -s 192.168.1.0/24 -p udp -m conntrack --ctorigdstport 137 -j RETURN",
	} {
		if !strings.Contains(string(guard), want) {
			t.Errorf("guard lacks %q:\n%s", want, guard)
		}
	}
	if i, j := strings.Index(string(guard), "--ctorigdstport"), strings.Index(string(guard), "--ctstate NEW -j DROP"); i < 0 || i > j {
		t.Errorf("the exceptions must come before the drop:\n%s", guard)
	}
	if again, err := engine.Apply(ctx, e, All()); err != nil || len(again.Changes) != 0 {
		t.Fatalf("second apply: %v %v", err, again.Changes)
	}

	// A rule the user added by hand stays; plex's go when its entry does.
	s.ufwAdded = append(s.ufwAdded, "ufw allow from 203.0.113.0/24 to any port 443 proto tcp comment 'mine'")
	e.Cfg = testConfig(t, "host:\n  firewall:\n    allow:\n      - {name: netbios, port: 137, proto: udp, from: 192.168.1.0/24}\n")
	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	added = strings.Join(s.ufwAdded, "\n")
	if strings.Contains(added, "plex") || !strings.Contains(added, "'mine'") || !strings.Contains(added, "netbios") {
		t.Errorf("after removing plex:\n%s", added)
	}
	changed := map[string]bool{}
	for _, c := range res.Changes {
		changed[c.Step] = true
	}
	if !changed["firewall-allow"] || !changed["docker-guard-script"] {
		t.Errorf("changes: %v", res.Changes)
	}

	// With IPv6 off in UFW, the IPv6 range is left out rather than refused.
	s.AddFile("/etc/default/ufw", "IPV6=no\n", 0o644)
	if got := wantAllow(&engine.Env{X: s, Cfg: testConfig(t, "host: {firewall: {allow: [{port: 80}]}}\n")}); len(got) != 3 {
		t.Errorf("IPv6 off: %v", got)
	}
}

func TestNoAllowKeepsTheGuardAsItWas(t *testing.T) {
	g := dockerGuardScriptText(testConfig(t, ""))
	if strings.Contains(g, "ctorigdstport") || strings.Contains(g, "host.firewall.allow\n\tif") {
		t.Errorf("guard without allow rules:\n%s", g)
	}
}

// A range is one UFW rule and one guard exception per source; a service
// is its preset's ports, TCP and UDP, each commented with the service.
// Ports two entries share are added once.
func TestFirewallAllowRangesAndServices(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, "host:\n  firewall:\n    allow:\n      - {name: games, port: 27015-27030, proto: udp, from: 192.168.1.0/24}\n      - {service: unifi, from: 192.168.1.0/24}\n      - {service: plex, from: 192.168.1.0/24}\n"))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	added := strings.Join(s.ufwAdded, "\n")
	for _, want := range []string{
		"ufw allow from 192.168.1.0/24 to any port 27015:27030 proto udp comment 'keelbox allow games'",
		"ufw allow from 192.168.1.0/24 to any port 8080 proto tcp comment 'keelbox allow unifi'",
		"ufw allow from 192.168.1.0/24 to any port 10001 proto udp comment 'keelbox allow unifi'",
		"ufw allow from 192.168.1.0/24 to any port 8880:8882 proto tcp comment 'keelbox allow unifi'",
		"ufw allow from 192.168.1.0/24 to any port 32412:32414 proto udp comment 'keelbox allow plex'",
	} {
		if !strings.Contains(added, want) {
			t.Errorf("missing rule %q in:\n%s", want, added)
		}
	}
	// 1900/udp is in both presets: the first entry's rule only.
	if n := strings.Count(added, "port 1900 proto udp"); n != 1 {
		t.Errorf("1900/udp added %d times:\n%s", n, added)
	}
	guard, _ := s.ReadFile(dockerGuardScript)
	for _, want := range []string{
		"-s 192.168.1.0/24 -p udp -m conntrack --ctorigdstport 27015:27030 -j RETURN",
		"-s 192.168.1.0/24 -p udp -m conntrack --ctorigdstport 3478 -j RETURN",
		"-s 192.168.1.0/24 -p tcp -m conntrack --ctorigdstport 8880:8882 -j RETURN",
	} {
		if !strings.Contains(string(guard), want) {
			t.Errorf("guard lacks %q:\n%s", want, guard)
		}
	}
	if again, err := engine.Apply(ctx, e, All()); err != nil || len(again.Changes) != 0 {
		t.Fatalf("second apply: %v %v", err, again.Changes)
	}
	// Dropping the service removes all its rules, and only those.
	e.Cfg = testConfig(t, "host:\n  firewall:\n    allow:\n      - {name: games, port: 27015-27030, proto: udp, from: 192.168.1.0/24}\n      - {service: plex, from: 192.168.1.0/24}\n")
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	added = strings.Join(s.ufwAdded, "\n")
	if strings.Contains(added, "'keelbox allow unifi'") || !strings.Contains(added, "port 1900 proto udp comment 'keelbox allow plex'") || !strings.Contains(added, "27015:27030") {
		t.Errorf("after removing unifi:\n%s", added)
	}
}
