package modules

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// packet is one outgoing packet as the table's rules see it.
type packet struct {
	uid uint32
	// owned is false for a packet the kernel sends by itself (neighbour
	// discovery, a TCP reset): it has no socket that a process opened, so
	// nftables cannot evaluate "meta skuid" and the rule does not apply.
	owned bool
	proto string // tcp, udp or ipv6-icmp
	dst   netip.AddrPort
	// local: the destination is one of the box's own addresses.
	local bool
}

// verdict runs a packet through rendered rules, as far as AgentNetRules
// writes them: it reads the text nft is given, and stops the test at a
// word it does not know, so a new kind of rule needs a line here.
func verdict(t *testing.T, rules string, p packet) string {
	t.Helper()
	chains := map[string][][]string{}
	name := ""
	for _, l := range strings.Split(rules, "\n") {
		f := ruleFields(l)
		switch {
		case len(f) >= 2 && f[0] == "chain":
			name = f[1]
		case name == "" || len(f) == 0 || f[0] == "}" || f[0] == "type":
		default:
			chains[name] = append(chains[name], f)
		}
	}
	var run func(chain string) string
	run = func(chain string) string {
	rule:
		for _, f := range chains[chain] {
			for i := 0; i < len(f); i++ {
				// operand takes the next word, or the next set.
				operand := func() []string {
					i++
					if f[i] != "{" {
						return []string{f[i]}
					}
					var set []string
					for i++; f[i] != "}"; i++ {
						set = append(set, strings.TrimSuffix(f[i], ","))
					}
					return set
				}
				switch f[i] {
				case "meta", "counter":
				case "skuid":
					negated := f[i+1] == "!="
					if negated {
						i++
					}
					in := false
					for _, el := range operand() {
						first, last, isRange := strings.Cut(el, "-")
						if !isRange {
							last = first
						}
						a, err1 := strconv.ParseUint(first, 10, 32)
						b, err2 := strconv.ParseUint(last, 10, 32)
						if err1 != nil || err2 != nil {
							t.Fatalf("uid %q in %v", el, f)
						}
						in = in || (uint64(p.uid) >= a && uint64(p.uid) <= b)
					}
					if !p.owned || in == negated {
						continue rule
					}
				case "tcp", "udp":
					if f[i+1] != "dport" {
						t.Fatalf("%v", f)
					}
					proto := f[i]
					i++
					if port := operand()[0]; p.proto != proto || port != strconv.Itoa(int(p.dst.Port())) {
						continue rule
					}
				case "l4proto":
					if operand()[0] != p.proto {
						continue rule
					}
				case "fib":
					if strings.Join(f[i:i+4], " ") != "fib daddr type local" {
						t.Fatalf("%v", f)
					}
					i += 3
					if !p.local {
						continue rule
					}
				case "ip", "ip6":
					if f[i+1] != "daddr" {
						t.Fatalf("%v", f)
					}
					v6 := f[i] == "ip6"
					i++
					in := false
					for _, el := range operand() {
						prefix, err := netip.ParsePrefix(el)
						if err != nil {
							a, err := netip.ParseAddr(el)
							if err != nil {
								t.Fatalf("address %q in %v", el, f)
							}
							prefix = netip.PrefixFrom(a, a.BitLen())
						}
						in = in || prefix.Contains(p.dst.Addr())
					}
					if p.dst.Addr().Is6() != v6 || !in {
						continue rule
					}
				case "jump":
					if v := run(operand()[0]); v != "" {
						return v
					}
					continue rule
				case "accept":
					return "accept"
				case "reject":
					return "reject"
				default:
					t.Fatalf("rule word %q in %v", f[i], f)
				}
			}
		}
		return ""
	}
	if v := run("output"); v != "" {
		return v
	}
	return "accept" // the chain's policy
}

// typicalBox is the accounts of a box as the table's tests name them: the
// agent with one subordinate range, and every account of linkLocalAccounts.
func typicalBox() (*system.Fake, []uidRange) {
	x := system.NewFake()
	x.AddUser("agent", 1001)
	for name, uid := range map[string]int{"systemd-network": 998, "systemd-timesync": 997, "systemd-resolve": 992, "_chrony": 114} {
		x.AddUser(name, uid)
	}
	return x, []uidRange{{1001, 1001}, {165536, 231071}}
}

func typicalRules(t *testing.T) string {
	t.Helper()
	x, uids := typicalBox()
	allowed, err := linkLocalUIDs(x, uids, nil)
	if err != nil {
		t.Fatal(err)
	}
	return AgentNetRules(uids, allowed, addrPorts("203.0.113.7:22"))
}

// The whole file for a typical box. A change here is a change to what every
// box loads: the security model and ADR 0076 describe these rules.
func TestAgentNetRulesForATypicalBox(t *testing.T) {
	const want = `#!/usr/sbin/nft -f
# Managed by keelbox. Limits the outbound connections of every account but
# root, the agent user's first; see https://keelbox.dev/docs/security-model.
# Loaded by keelbox-agent-net.service.
table inet keelbox_agent
delete table inet keelbox_agent
table inet keelbox_agent {
	chain linklocal {
		meta skuid { 1001, 165536-231071 } counter reject comment "agent: link-local and cloud metadata"
		meta skuid 114 udp dport 123 accept comment "_chrony: NTP"
		meta skuid 992 tcp dport 53 accept comment "systemd-resolve: DNS"
		meta skuid 992 udp dport 53 accept comment "systemd-resolve: DNS"
		meta skuid 997 udp dport 123 accept comment "systemd-timesync: NTP"
		meta skuid 998 meta l4proto ipv6-icmp accept comment "systemd-network: IPv6 router and neighbour messages"
		meta skuid 998 udp dport 67 accept comment "systemd-network: DHCP"
		meta skuid 998 udp dport 547 accept comment "systemd-network: DHCPv6"
		meta skuid != 0 counter reject comment "only root: link-local and cloud metadata"
	}
	chain output {
		type filter hook output priority filter; policy accept;
		tcp dport 22 fib daddr type local meta skuid != 0 counter reject with tcp reset comment "only root may ssh into this box"
		ip daddr 203.0.113.7 tcp dport 22 meta skuid != 0 counter reject with tcp reset comment "only root may ssh into this box: it is reached here from outside"
		ip daddr 169.254.0.0/16 jump linklocal
		ip6 daddr { fe80::/10, fd00:ec2::254 } jump linklocal
	}
}
`
	if got := typicalRules(t); got != want {
		t.Errorf("rules:\n%s", got)
	}
}

// Who may connect where. Every account but root is refused the box's own
// SSH port and link-local addresses, IPv4 and IPv6; the accounts of
// linkLocalAccounts pass there on their own port and nowhere else; root and
// the kernel's own packets pass.
func TestAgentNetRulesByAccount(t *testing.T) {
	rules := typicalRules(t)
	type dst struct {
		proto, addr string
		local       bool
	}
	var (
		meta4    = dst{"tcp", "169.254.169.254:80", false}
		meta6    = dst{"tcp", "[fd00:ec2::254]:80", false}
		other6   = dst{"tcp", "[fe80::1]:443", false}
		dnsGoog  = dst{"udp", "169.254.169.254:53", false}
		dnsAWS   = dst{"tcp", "169.254.169.253:53", false}
		dns6     = dst{"udp", "[fe80::1]:53", false}
		ntpAWS   = dst{"udp", "169.254.169.123:123", false}
		ntpGoog  = dst{"udp", "169.254.169.254:123", false}
		ntpTCP   = dst{"tcp", "169.254.169.123:123", false}
		dhcp     = dst{"udp", "169.254.169.254:67", false}
		dhcp6    = dst{"udp", "[fe80::1]:547", false}
		icmp6    = dst{"ipv6-icmp", "[fe80::1]:0", false}
		sshLoop  = dst{"tcp", "127.0.0.1:22", true}
		sshLoop6 = dst{"tcp", "[::1]:22", true}
		sshOwn   = dst{"tcp", "10.0.0.5:22", true}
		sshOut   = dst{"tcp", "203.0.113.7:22", false}
		sshElse  = dst{"tcp", "140.82.121.4:22", false}
		web      = dst{"tcp", "93.184.216.34:443", false}
		web6     = dst{"tcp", "[2606:2800:220:1::1]:443", false}
		dnsElse  = dst{"udp", "10.0.0.2:53", false}
		localWeb = dst{"tcp", "127.0.0.1:8080", true}
	)
	linkLocal := []dst{meta4, meta6, other6, dnsGoog, dnsAWS, dns6, ntpAWS, ntpGoog, ntpTCP, dhcp, dhcp6, icmp6}
	ownSSH := []dst{sshLoop, sshLoop6, sshOwn, sshOut}
	open := []dst{sshElse, web, web6, dnsElse, localWeb}
	for _, who := range []struct {
		name  string
		uid   uint32
		owned bool
		// passes is what of linkLocal and ownSSH this account reaches.
		passes []dst
	}{
		{"the agent", 1001, true, nil},
		{"a subordinate uid of the agent's, first", 165536, true, nil},
		{"a subordinate uid of the agent's, last", 231071, true, nil},
		{"another login account", 1000, true, nil},
		{"a service account", 113, true, nil},
		{"nobody", 65534, true, nil},
		{"a uid past the agent's range", 231072, true, nil},
		{"systemd-resolve", 992, true, []dst{dnsGoog, dnsAWS, dns6}},
		{"systemd-timesync", 997, true, []dst{ntpAWS, ntpGoog}},
		{"_chrony", 114, true, []dst{ntpAWS, ntpGoog}},
		{"systemd-network", 998, true, []dst{dhcp, dhcp6, icmp6}},
		{"root", 0, true, append(append([]dst(nil), linkLocal...), ownSSH...)},
		{"the kernel", 0, false, append(append([]dst(nil), linkLocal...), ownSSH...)},
	} {
		check := func(d dst, want string) {
			t.Helper()
			p := packet{uid: who.uid, owned: who.owned, proto: d.proto, dst: netip.MustParseAddrPort(d.addr), local: d.local}
			if got := verdict(t, rules, p); got != want {
				t.Errorf("%s (uid %d) to %s %s: %s, want %s", who.name, who.uid, d.proto, d.addr, got, want)
			}
		}
		for _, d := range append(append([]dst(nil), linkLocal...), ownSSH...) {
			want := "reject"
			for _, ok := range who.passes {
				if ok == d {
					want = "accept"
				}
			}
			check(d, want)
		}
		for _, d := range open {
			check(d, "accept")
		}
	}
}

// An account the box does not have gets no rule, nor does one whose uid is
// root's or one of the agent's; the agent's uids are refused before any
// exception.
func TestLinkLocalAccountsOnTheBox(t *testing.T) {
	uids := []uidRange{{1001, 1001}, {165536, 231071}}
	accounts := func(x system.Executor) string {
		t.Helper()
		allowed, err := linkLocalUIDs(x, uids, nil)
		if err != nil {
			t.Fatal(err)
		}
		var l []string
		for _, a := range allowed {
			l = append(l, a.account+" "+a.key())
		}
		return strings.Join(l, "; ")
	}

	x := system.NewFake()
	x.AddUser("agent", 1001)
	if got := accounts(x); got != "" {
		t.Errorf("a box with none of the accounts: %s", got)
	}
	none := AgentNetRules(uids, nil, nil)
	if strings.Contains(none, " accept comment") || strings.Count(none, "reject") != 3 {
		t.Errorf("rules without exceptions:\n%s", none)
	}
	for _, uid := range []uint32{992, 997, 998, 114} {
		if got := verdict(t, none, packet{uid: uid, owned: true, proto: "udp", dst: netip.MustParseAddrPort("169.254.169.254:53")}); got != "reject" {
			t.Errorf("uid %d with no exception: %s", uid, got)
		}
	}

	x.AddUser("systemd-resolve", 992)
	x.AddUser("_chrony", 114)
	if got, want := accounts(x), "_chrony 114 udp 123; systemd-resolve 992 tcp 53; systemd-resolve 992 udp 53"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}

	// A name is not what lets a uid through: root's uid has no rule to be
	// excepted from, and a uid of the agent's stays refused.
	x.AddUser("systemd-timesync", 0)
	x.AddUser("systemd-network", 165540)
	if got, want := accounts(x), "_chrony 114 udp 123; systemd-resolve 992 tcp 53; systemd-resolve 992 udp 53"; got != want {
		t.Errorf("with an account at root's uid and one in the agent's range: %s, want %s", got, want)
	}
	rules := AgentNetRules(uids, []linkLocalUID{{165540, linkLocalAccount{"systemd-network", "udp", 67, "DHCP"}}}, nil)
	if got := verdict(t, rules, packet{uid: 165540, owned: true, proto: "udp", dst: netip.MustParseAddrPort("169.254.169.254:67")}); got != "reject" {
		t.Errorf("a uid of the agent's with an exception written for it: %s", got)
	}
}

// The same accounts render the same file whatever order they come in, so a
// second apply finds nothing to change.
func TestAgentNetRulesAreSorted(t *testing.T) {
	x, uids := typicalBox()
	allowed, err := linkLocalUIDs(x, uids, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := AgentNetRules(uids, allowed, nil)
	reversed := make([]linkLocalUID, 0, len(allowed))
	for i := len(allowed) - 1; i >= 0; i-- {
		reversed = append(reversed, allowed[i])
	}
	if got := AgentNetRules(uids, reversed, nil); got != want {
		t.Errorf("exceptions in another order render another file:\n%s", got)
	}
	last := -1
	for _, l := range strings.Split(want, "\n") {
		f := ruleFields(l)
		if !contains(f, "accept") {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil || uid < last {
			t.Errorf("exceptions not in uid order at %q", l)
		}
		last = uid
	}
	if last < 0 {
		t.Error("no exception read back")
	}
}

// nft lists a rule its own way. What is read back from a listing is the
// same whatever the order of a set, the form of a uid or the counters.
func TestReadLoadedNet(t *testing.T) {
	x, uids := typicalBox()
	x.Users["root"] = system.User{Name: "root", Home: "/root"}
	allowed, err := linkLocalUIDs(x, uids, nil)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, a := range allowed {
		want = append(want, a.key())
	}
	sort.Strings(want)
	_, written, _ := strings.Cut(AgentNetRules(uids, allowed, nil), "delete table inet keelbox_agent\n")
	listed := `table inet keelbox_agent {
	chain linklocal {
		meta skuid { 165536-231071, "agent" } counter packets 12 bytes 720 reject comment "agent: link-local and cloud metadata"
		meta skuid "_chrony" udp dport 123 accept comment "_chrony: NTP"
		meta skuid "systemd-resolve" tcp dport 53 accept comment "systemd-resolve: DNS"
		meta skuid 992 udp dport 53 accept
		meta skuid 997 udp dport 123 accept comment "systemd-timesync: NTP"
		meta skuid 998 meta l4proto icmpv6 accept comment "systemd-network: IPv6 router and neighbour messages"
		meta skuid 998 udp dport 67 accept comment "systemd-network: DHCP"
		meta skuid "systemd-network" udp dport 547 accept comment "systemd-network: DHCPv6"
		meta skuid != "root" counter packets 3 bytes 180 reject comment "only root: link-local and cloud metadata"
	}

	chain output {
		type filter hook output priority filter; policy accept;
		tcp dport 22 fib daddr type local meta skuid != "root" counter packets 0 bytes 0 reject with tcp reset comment "only root may ssh into this box"
		ip daddr 169.254.0.0/16 jump linklocal
		ip6 daddr { fd00:ec2::254, fe80::/10 } jump linklocal
	}
}
`
	for name, listing := range map[string]string{"as written": written, "as nft may list it": listed} {
		n := readLoadedNet(x, listing)
		if !n.everyAccount() || nftUIDs(n.agent) != nftUIDs(uids) || strings.Join(n.allowed, "; ") != strings.Join(want, "; ") {
			t.Errorf("%s: read back %+v", name, n)
		}
	}

	// What must not read as the rules of this keelbox.
	for name, edit := range map[string]func(string) string{
		"SSH refused to the agent alone": func(s string) string {
			return strings.Replace(s, `fib daddr type local meta skuid != "root"`, "fib daddr type local meta skuid 1001", 1)
		},
		"SSH refused on another port": func(s string) string { return strings.Replace(s, "tcp dport 22 fib", "tcp dport 2222 fib", 1) },
		"no reject for the other accounts": func(s string) string {
			return strings.Replace(s, `meta skuid != "root" counter packets 3 bytes 180 reject`, "counter packets 3 bytes 180 accept", 1)
		},
		"the last reject for one port only": func(s string) string {
			return strings.Replace(s, `meta skuid != "root" counter packets 3`, `meta skuid != "root" tcp dport 80 counter packets 3`, 1)
		},
		"IPv4 link-local not sent to the chain": func(s string) string {
			return strings.Replace(s, "\t\tip daddr 169.254.0.0/16 jump linklocal\n", "", 1)
		},
		"IPv6 link-local not sent to the chain": func(s string) string { return strings.Replace(s, "fd00:ec2::254, fe80::/10", "fd00:ec2::254", 1) },
		"link-local sent there for one uid only": func(s string) string {
			return strings.Replace(s, "ip daddr 169.254.0.0/16 jump", "ip daddr 169.254.0.0/16 meta skuid 1001 jump", 1)
		},
		"a jump from a chain that is not output's": func(s string) string { return strings.Replace(s, "chain output {", "chain other {", 1) },
	} {
		if n := readLoadedNet(x, edit(listed)); n.everyAccount() {
			t.Errorf("%s: read as limits for every account: %+v", name, n)
		}
	}
	// An accept that is not an exception keelbox writes.
	for name, line := range map[string]string{
		"every account on one port":    "udp dport 53 accept",
		"every account but one":        "meta skuid != 992 udp dport 53 accept",
		"a range of accounts":          "meta skuid 100-999 udp dport 53 accept",
		"a protocol keelbox never has": "meta skuid 992 meta l4proto sctp accept",
		"a port that is not a number":  "meta skuid 992 udp dport domain accept",
	} {
		n := readLoadedNet(x, strings.Replace(listed, "meta skuid 992 udp dport 53 accept", line, 1))
		if !contains(n.allowed, "?") {
			t.Errorf("%s (%s): read as %v", name, line, n.allowed)
		}
	}
	// One account on every port is what a name in the config gets; doctor
	// compares it with the config like any other exception.
	if n := readLoadedNet(x, strings.Replace(listed, "meta skuid 992 udp dport 53 accept", `meta skuid "_chrony" accept comment "x"`, 1)); !contains(n.allowed, "114 any 0") || contains(n.allowed, "?") {
		t.Errorf("an account on every port: read as %v", n.allowed)
	}
	older := readLoadedNet(x, "\tchain output {\n\t\tmeta skuid != { 1001, 165536-231071 } accept\n\t\ttcp dport 22 fib daddr type local counter packets 0 bytes 0 reject with tcp reset\n\t}\n")
	if older.everyAccount() || !contains(older.allowed, "?") {
		t.Errorf("an older keelbox's table: %+v", older)
	}
}

// olderAgentNetRules is the file keelbox wrote before ADR 0076: the agent's
// uids alone were limited, and every other account passed.
func olderAgentNetRules(uids []uidRange, outside []netip.AddrPort) string {
	var rules strings.Builder
	for _, a := range outside {
		family := "ip"
		if a.Addr().Is6() {
			family = "ip6"
		}
		fmt.Fprintf(&rules, "\t\t%s daddr %s tcp dport %d counter reject with tcp reset comment \"agent may not ssh into this box: it is reached here from outside\"\n",
			family, a.Addr(), a.Port())
	}
	return fmt.Sprintf(`#!/usr/sbin/nft -f
# Managed by keelbox. Limits the agent user's outbound connections; see
# https://keelbox.dev/docs/security-model. Loaded by keelbox-agent-net.service.
table inet keelbox_agent
delete table inet keelbox_agent
table inet keelbox_agent {
	chain output {
		type filter hook output priority filter; policy accept;
		meta skuid != %[1]s accept
		tcp dport 22 fib daddr type local counter reject with tcp reset comment "agent may not ssh into this box"
%[2]s		ip daddr 169.254.0.0/16 counter reject comment "link-local and cloud metadata"
		ip6 daddr { fe80::/10, fd00:ec2::254 } counter reject comment "link-local and cloud metadata"
	}
}
`, nftUIDs(uids), rules.String())
}

// The rules ADR 0070 replaced: they refused the agent and let every other
// account through. As verdict models the owner match, they
// also refused the kernel's own packets to link-local addresses, which the
// current rules let pass.
func TestOlderAgentNetRulesLetOtherAccountsThrough(t *testing.T) {
	old := olderAgentNetRules([]uidRange{{1001, 1001}, {165536, 231071}}, nil)
	meta := netip.MustParseAddrPort("169.254.169.254:80")
	for uid, want := range map[uint32]string{1001: "reject", 165536: "reject", 113: "accept", 1000: "accept"} {
		if got := verdict(t, old, packet{uid: uid, owned: true, proto: "tcp", dst: meta}); got != want {
			t.Errorf("older rules, uid %d to the metadata address: %s, want %s", uid, got, want)
		}
	}
	router := packet{proto: "ipv6-icmp", dst: netip.MustParseAddrPort("[fe80::1]:0")}
	if got := verdict(t, old, router); got != "reject" {
		t.Errorf("older rules, the kernel's neighbour discovery to a link-local router: %s", got)
	}
	if got := verdict(t, typicalRules(t), router); got != "accept" {
		t.Errorf("the kernel's neighbour discovery to a link-local router: %s", got)
	}
}

// A box an older keelbox set up has the table that limits the agent alone.
// Doctor fails on it with the fix, plan shows the rules changing, and apply
// loads the new table and nothing else.
func TestApplyReplacesOlderAgentNetRules(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	e.SSHAddrs = "203.0.113.7:22"
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	e.SSHAddrs = ""
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK {
		t.Fatalf("doctor on the new rules: %+v", f)
	}
	current := s.content(agentNetRules)

	old := olderAgentNetRules(simAgentUIDs(t, s), addrPorts("203.0.113.7:22"))
	s.AddFile(agentNetRules, old, 0o644)
	s.nftLoaded = old
	f := agentNetFinding(t, ctx, e)
	if f.Level != engine.LevelFail || !strings.Contains(f.Detail, "to every account but root") || !strings.HasSuffix(f.Detail, ": keelbox apply testbox") {
		t.Errorf("doctor on an older keelbox's rules: %+v", f)
	}
	if d, pending := stepPending(t, ctx, e, "agent-net-rules"); !pending {
		t.Errorf("plan does not show the rules changing: %q", d)
	}
	res, err := engine.Apply(ctx, e, All())
	if err != nil || len(res.Changes) != 1 || res.Changes[0].Step != "agent-net-rules" {
		t.Fatalf("apply over the older rules: %v %v", res.Changes, err)
	}
	if got := s.content(agentNetRules); got != current || got != simRules(t, s, addrPorts("203.0.113.7:22")) {
		t.Errorf("rules after apply:\n%s", got)
	}
	if s.nftLoaded != current {
		t.Errorf("the table was not loaded again:\n%s", s.nftLoaded)
	}
	out, _ := s.Run(ctx, cmdNftList())
	if !strings.Contains(out.Stdout, "chain linklocal {") || strings.Contains(out.Stdout, "accept\n") {
		t.Errorf("loaded table:\n%s", out.Stdout)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK {
		t.Errorf("doctor after apply: %+v", f)
	}
	if again, err := engine.Apply(ctx, e, All()); err != nil || len(again.Changes) != 0 {
		t.Errorf("a second apply: %v %v", again.Changes, err)
	}
}

// The sim has Ubuntu's three service accounts and no chrony. The loaded
// table excepts those three, each on its own traffic; an account that comes
// later (a package brings chrony) makes doctor fail until the next apply
// loads its rule, and one that goes makes it fail the same way.
func TestAgentNetExceptionsFollowTheAccounts(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	loaded := func() string {
		t.Helper()
		out, _ := s.Run(ctx, cmdNftList())
		return strings.Join(readLoadedNet(s, out.Stdout).allowed, "; ")
	}
	const three = "992 tcp 53; 992 udp 53; 997 udp 123; 998 ipv6-icmp 0; 998 udp 547; 998 udp 67"
	if got := loaded(); got != three {
		t.Errorf("loaded exceptions: %s", got)
	}
	rules := s.content(agentNetRules)
	agent, _ := s.LookupUser("agent")
	for _, c := range []struct {
		uid         int
		proto, addr string
		want        string
	}{
		{agent.UID, "udp", "169.254.169.254:53", "reject"},
		{100000, "tcp", "169.254.169.254:80", "reject"},
		{1000, "tcp", "169.254.169.254:80", "reject"},
		{992, "udp", "169.254.169.254:53", "accept"},
		{992, "tcp", "169.254.169.254:80", "reject"},
		{997, "udp", "169.254.169.123:123", "accept"},
		{998, "udp", "169.254.169.254:67", "accept"},
		{0, "tcp", "169.254.169.254:80", "accept"},
	} {
		p := packet{uid: uint32(c.uid), owned: true, proto: c.proto, dst: netip.MustParseAddrPort(c.addr)} //nolint:gosec // G115: test uids
		if got := verdict(t, rules, p); got != c.want {
			t.Errorf("uid %d to %s %s: %s, want %s", c.uid, c.proto, c.addr, got, c.want)
		}
	}

	s.Users["_chrony"] = system.User{Name: "_chrony", UID: 114, GID: 114, Home: "/var/lib/chrony"}
	f := agentNetFinding(t, ctx, e)
	if f.Level != engine.LevelFail || !strings.Contains(f.Detail, "should let 114 udp 123, ") || !strings.HasSuffix(f.Detail, ": keelbox apply testbox") {
		t.Errorf("doctor with an account the table has no rule for: %+v", f)
	}
	res, err := engine.Apply(ctx, e, All())
	if err != nil || len(res.Changes) != 1 || res.Changes[0].Step != "agent-net-rules" {
		t.Fatalf("apply after the account came: %v %v", res.Changes, err)
	}
	if got := loaded(); got != "114 udp 123; "+three {
		t.Errorf("loaded exceptions after apply: %s", got)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK || !strings.HasSuffix(f.Detail, "stay open to systemd-resolve, systemd-timesync, _chrony, systemd-network)") {
		t.Errorf("doctor after apply: %+v", f)
	}

	delete(s.Users, "systemd-timesync")
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelFail || !strings.Contains(f.Detail, "let 114 udp 123, 992 tcp 53, 992 udp 53, 997 udp 123, ") {
		t.Errorf("doctor with a rule for an account that is gone: %+v", f)
	}
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := loaded(); strings.Contains(got, "997") {
		t.Errorf("loaded exceptions after the account went: %s", got)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK {
		t.Errorf("doctor after apply: %+v", f)
	}
}

// An account the config names (host.firewall.link_local_accounts) keeps
// link-local addresses on every port, and nothing else: the box's SSH port
// stays refused to it, and the agent's uids stay refused whatever the
// config says.
func TestAgentNetRulesWithANamedAccount(t *testing.T) {
	x, uids := typicalBox()
	x.AddUser("do-agent", 115)
	x.AddUser("sneaky", 165540)
	allowed, err := linkLocalUIDs(x, uids, []string{"do-agent", "ghost", "sneaky"})
	if err != nil {
		t.Fatal(err)
	}
	rules := AgentNetRules(uids, allowed, nil)
	const line = "\t\tmeta skuid 115 accept comment \"do-agent: host.firewall.link_local_accounts\"\n"
	if strings.Count(rules, line) != 1 || strings.Contains(rules, "ghost") || strings.Contains(rules, "sneaky") {
		t.Fatalf("rules with a named account, one that is missing and one at a uid of the agent's:\n%s", rules)
	}
	if strings.Index(rules, line) < strings.Index(rules, "agent: link-local") || strings.Index(rules, line) > strings.Index(rules, "only root: link-local") {
		t.Errorf("the named account's rule is not between the agent's reject and the last one:\n%s", rules)
	}
	var builtIn []linkLocalUID
	for _, a := range allowed {
		if a.proto != protoAny {
			builtIn = append(builtIn, a)
		}
	}
	if without := AgentNetRules(uids, builtIn, nil); without != typicalRulesWithout(t, "203.0.113.7") {
		t.Errorf("the named account changed more than its own line:\n%s", without)
	}
	for _, c := range []struct {
		uid         uint32
		proto, addr string
		local       bool
		want        string
	}{
		{115, "tcp", "169.254.169.254:80", false, "accept"},
		{115, "udp", "169.254.169.254:53", false, "accept"},
		{115, "tcp", "[fd00:ec2::254]:80", false, "accept"},
		{115, "tcp", "[fe80::1]:8080", false, "accept"},
		{115, "tcp", "127.0.0.1:22", true, "reject"},
		{1001, "tcp", "169.254.169.254:80", false, "reject"},
		{165540, "tcp", "169.254.169.254:80", false, "reject"},
		{113, "tcp", "169.254.169.254:80", false, "reject"},
		{992, "tcp", "169.254.169.254:80", false, "reject"},
	} {
		p := packet{uid: c.uid, owned: true, proto: c.proto, dst: netip.MustParseAddrPort(c.addr), local: c.local}
		if got := verdict(t, rules, p); got != c.want {
			t.Errorf("uid %d to %s %s: %s, want %s", c.uid, c.proto, c.addr, got, c.want)
		}
	}
	_, table, _ := strings.Cut(rules, "delete table inet keelbox_agent\n")
	if n := readLoadedNet(x, table); !n.everyAccount() || !contains(n.allowed, "115 any 0") || contains(n.allowed, "?") {
		t.Errorf("read back: %+v", n)
	}
}

// typicalRulesWithout is typicalRules without its outside address.
func typicalRulesWithout(t *testing.T, addr string) string {
	t.Helper()
	var out strings.Builder
	for _, l := range strings.SplitAfter(typicalRules(t), "\n") {
		if !strings.Contains(l, "ip daddr "+addr+" ") {
			out.WriteString(l)
		}
	}
	return out.String()
}

func findingsOf(t *testing.T, ctx context.Context, e *engine.Env, check string) []engine.Finding {
	t.Helper()
	var out []engine.Finding
	for _, f := range (Host{}).Verify(ctx, e) {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

// On the box: a named account that exists gets its rule and doctor says
// so. One that does not exist is no reason for apply to fail: doctor warns,
// with the fix, and the account gets its rule at the apply after it
// appears. A name taken out of the config loses its rule.
func TestLinkLocalAccountsFromTheConfig(t *testing.T) {
	const yaml = "host:\n  firewall:\n    link_local_accounts: [do-agent, ghost]\n"
	s := newSim(t)
	s.Users["do-agent"] = system.User{Name: "do-agent", UID: 115, GID: 115, Home: "/nonexistent"}
	e := simEnv(t, s, testConfig(t, yaml))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatalf("apply with a name that is no account: %v", err)
	}
	rules := s.content(agentNetRules)
	if !strings.Contains(rules, "meta skuid 115 accept comment \"do-agent: host.firewall.link_local_accounts\"") || strings.Contains(rules, "ghost") {
		t.Errorf("rules:\n%s", rules)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK || !strings.HasSuffix(f.Detail, "; every port there stays open to do-agent, named in host.firewall.link_local_accounts)") {
		t.Errorf("doctor: %+v", f)
	}
	warns := findingsOf(t, ctx, e, "link-local-accounts")
	if len(warns) != 1 || warns[0].Level != engine.LevelWarn || !strings.Contains(warns[0].Detail, "names ghost, which is not an account on this box") || !strings.HasSuffix(warns[0].Detail, "then keelbox apply testbox") {
		t.Errorf("doctor on a name that is no account: %+v", warns)
	}
	if again, err := engine.Apply(ctx, e, All()); err != nil || len(again.Changes) != 0 {
		t.Errorf("a second apply: %v %v", again.Changes, err)
	}

	// The account appears: its rule is missing until the next apply.
	s.Users["ghost"] = system.User{Name: "ghost", UID: 116, GID: 116, Home: "/nonexistent"}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelFail || !strings.Contains(f.Detail, "116 any 0") || !strings.HasSuffix(f.Detail, ": keelbox apply testbox") {
		t.Errorf("doctor before the apply that adds the rule: %+v", f)
	}
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Changes) != 1 || res.Changes[0].Step != "agent-net-rules" {
		t.Fatalf("apply after the account appeared: %v %v", res.Changes, err)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK || !strings.Contains(f.Detail, "stays open to do-agent, ghost, named in") {
		t.Errorf("doctor after apply: %+v", f)
	}
	if warns := findingsOf(t, ctx, e, "link-local-accounts"); len(warns) != 0 {
		t.Errorf("doctor still warns: %+v", warns)
	}

	// Out of the config: the rule goes, and doctor fails until it has.
	e = simEnv(t, s, testConfig(t, ""))
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelFail || !strings.Contains(f.Detail, "let 115 any 0, 116 any 0, ") {
		t.Errorf("doctor with rules the config no longer asks for: %+v", f)
	}
	// The copy of the config on the box changes with it.
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Changes) != 2 || res.Changes[0].Step != "agent-net-rules" {
		t.Fatalf("apply after the names left the config: %v %v", res.Changes, err)
	}
	if got := s.content(agentNetRules); strings.Contains(got, "link_local_accounts") || got != simRules(t, s, nil) {
		t.Errorf("rules after the names left the config:\n%s", got)
	}
}

// A name whose uid is root's, or one of the agent's, gets no rule either,
// and doctor says which.
func TestLinkLocalAccountsThatGetNoRule(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "host:\n  firewall:\n    link_local_accounts: [toor, inside]\n"))
	ctx := context.Background()
	s.Users["toor"] = system.User{Name: "toor", Home: "/root"}
	s.Users["inside"] = system.User{Name: "inside", UID: 100005, GID: 100005, Home: "/nonexistent"}
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := s.content(agentNetRules); strings.Contains(got, "link_local_accounts") {
		t.Errorf("rules:\n%s", got)
	}
	warns := findingsOf(t, ctx, e, "link-local-accounts")
	if len(warns) != 2 || !strings.Contains(warns[0].Detail, "names toor, whose uid is root's") || !strings.Contains(warns[1].Detail, "names inside, whose uid is one of the agent's") {
		t.Errorf("doctor: %+v", warns)
	}
}
