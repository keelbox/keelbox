package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFirewallAllow(t *testing.T) {
	c, err := Parse([]byte(minimal("host:\n  firewall:\n    allow:\n      - {name: plex, port: 32400}\n      - {port: 137, proto: udp, from: 192.168.1.7/24}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	a := c.Host.Firewall.Allow
	if len(a) != 2 || a[0].Protocol() != "tcp" || !reflect.DeepEqual(a[0].Sources(), LANRanges) {
		t.Fatalf("defaults: %+v", a)
	}
	if got := a[1].Sources(); !reflect.DeepEqual(got, []string{"192.168.1.0/24"}) {
		t.Errorf("CIDR not masked: %v", got)
	}
	if got := AllowSummary(a); got != "plex 32400/tcp, 137-udp 137/udp" {
		t.Errorf("summary: %s", got)
	}
}

func TestFirewallAllowRejected(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"{port: 0}", "give a port or a service"},
		{"{port: 70000}", "is not a port"},
		{"{port: 8010-8000}", "put the lower port first, port: 8000-8010"},
		{"{port: 20-30}", "includes SSH"},
		{"{service: unifi, port: 8080}", "a service or a port, not both"},
		{"{service: emby}", "is not a service keelbox knows; use one of adguard, home-assistant"},
		{"{port: 22}", "is SSH"},
		{"{port: 80, proto: icmp}", "use tcp or udp"},
		{"{port: 80, from: everywhere}", "use lan or a network"},
		{"{port: 80, from: 0.0.0.0/0}", "the whole internet"},
		{"{port: 80, from: \"::/0\"}", "the whole internet"},
		{"{port: 80, name: Plex Server}", "lowercase letters"},
	} {
		p := problems(t, minimal("host:\n  firewall:\n    allow: ["+c.yaml+"]\n"))
		if len(p) != 1 || !strings.Contains(p[0], c.want) {
			t.Errorf("%s: %v, want %q", c.yaml, p, c.want)
		}
	}
	if _, err := Parse([]byte(minimal("host:\n  firewall:\n    allow: [{port: 80-}]\n")), "t"); err == nil || !strings.Contains(err.Error(), "a range such as 8000-8010") {
		t.Errorf("port: 80-: %v", err)
	}
	p := problems(t, minimal("host:\n  firewall:\n    enabled: false\n    allow: [{port: 80}]\n"))
	if len(p) != 1 || !strings.Contains(p[0], "enabled is false") {
		t.Errorf("allow without the firewall: %v", p)
	}
}

func TestFirewallAllowRangesAndServices(t *testing.T) {
	c, err := Parse([]byte(minimal("host:\n  firewall:\n    allow:\n      - {name: games, port: 27015-27030, proto: udp}\n      - {service: jellyfin, from: 192.168.1.0/24}\n      - {name: hass, service: home-assistant}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	a := c.Host.Firewall.Allow
	if a[0].Port != Ports(27015, 27030) || a[0].Key() != "27015-27030/udp" || a[0].Port.Colon() != "27015:27030" {
		t.Errorf("range: %+v", a[0])
	}
	got := []string{}
	for _, r := range c.AllowRules() {
		got = append(got, r.Label()+" "+r.Key()+" "+r.From)
	}
	want := "games 27015-27030/udp |jellyfin 8096/tcp 192.168.1.0/24|jellyfin 8920/tcp 192.168.1.0/24|jellyfin 7359/udp 192.168.1.0/24|hass 8123/tcp "
	if strings.Join(got, "|") != want {
		t.Errorf("expanded:\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
	if e, ok := c.Allowed(27020, "udp"); !ok || e.Name != "games" {
		t.Errorf("27020/udp: %+v %v", e, ok)
	}
	if e, ok := c.Allowed(7359, "udp"); !ok || e.Service != "jellyfin" {
		t.Errorf("7359/udp: %+v %v", e, ok)
	}
	if _, ok := c.Allowed(7359, "tcp"); ok {
		t.Error("7359/tcp is not jellyfin's")
	}
	if s := AllowSummary(a); s != "games 27015-27030/udp, jellyfin (8096, 8920/tcp; 7359/udp), hass (8123/tcp)" {
		t.Errorf("summary: %s", s)
	}
	if y := AllowYAML(a[0]) + " " + AllowYAML(a[1]); y != "{name: games, port: 27015-27030, proto: udp} {service: jellyfin, from: 192.168.1.0/24}" {
		t.Errorf("yaml: %s", y)
	}
	j, _ := json.Marshal([]Allow{a[0], {Port: Port(445)}})
	if !strings.Contains(string(j), `"Port":"27015-27030"`) || !strings.Contains(string(j), `"Port":445`) {
		t.Errorf("json: %s", j)
	}
	var back []Allow
	if err := json.Unmarshal(j, &back); err != nil || back[0].Port != a[0].Port || back[1].Port != Port(445) {
		t.Errorf("json back: %+v %v", back, err)
	}
	out, err := c.Marshal()
	if err != nil || !strings.Contains(string(out), "port: 27015-27030") {
		t.Errorf("marshal: %v\n%s", err, out)
	}
}

func TestFirewallLinkLocalAccounts(t *testing.T) {
	c, err := Parse([]byte(minimal("host:\n  firewall:\n    enabled: false\n    link_local_accounts: [do-agent, _telegraf, admin]\n")), "t")
	if err != nil {
		t.Fatalf("names taken without UFW too: %v", err)
	}
	if got := c.Host.Firewall.LinkLocalAccounts; !reflect.DeepEqual(got, []string{"do-agent", "_telegraf", "admin"}) {
		t.Errorf("accounts: %v", got)
	}
	many := strings.TrimSuffix(strings.Repeat("svc, ", 1), ", ")
	for i := 0; i < MaxLinkLocalAccounts; i++ {
		many += ", svc" + string(rune('a'+i))
	}
	for _, c := range []struct{ users, yaml, want string }{
		{"", "[root]", "host.firewall.link_local_accounts[0]: root reaches link-local addresses already, and no rule limits it; remove root from the list"},
		{"", "[do-agent, agent]", "host.firewall.link_local_accounts[1]: agent is the agent account, and naming it here would undo the limit that keeps the agent from link-local addresses and cloud metadata; remove agent from the list"},
		{"  agent:\n    name: coder\n", "[coder]", "coder is the agent account"},
		{"", "[\"Do Agent\"]", "\"Do Agent\" is not an account name; give the name as /etc/passwd has it, for example link_local_accounts: [do-agent]"},
		{"", "[\"1001\"]", "is not an account name"},
		{"", "[\"a\\\" accept\"]", "is not an account name"},
		{"", "[\"\"]", "is not an account name"},
		{"", "[cwagent, cwagent]", "host.firewall.link_local_accounts[1]: cwagent is listed twice; remove one"},
		{"", "[" + many + "]", "17 accounts, and at most 16 are taken"},
	} {
		y := "version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [\"" + key + "\"]\n" + c.users + "host:\n  firewall:\n    link_local_accounts: " + c.yaml + "\n"
		p := problems(t, y)
		if len(p) != 1 || !strings.Contains(p[0], c.want) {
			t.Errorf("%s: %v, want %q", c.yaml, p, c.want)
		}
	}
	// With another agent account, "agent" is a name like any other.
	y := "version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [\"" + key + "\"]\n  agent:\n    name: coder\n" + "host:\n  firewall:\n    link_local_accounts: [agent]\n"
	if p := problems(t, y); len(p) != 0 {
		t.Errorf("agent named where the agent account is coder: %v", p)
	}
}
