package modules

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

func addrPorts(ss ...string) []netip.AddrPort {
	var out []netip.AddrPort
	for _, s := range ss {
		out = append(out, netip.MustParseAddrPort(s))
	}
	return out
}

func TestOutsideAddrTakesOnlyWaysToTheBox(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7:22":             "203.0.113.7:22",
		" 203.0.113.7:2222\t":        "203.0.113.7:2222",
		"[2001:db8::7]:22":           "[2001:db8::7]:22",
		"[2001:DB8:0:0::7]:22":       "[2001:db8::7]:22",
		"[::ffff:203.0.113.7]:22":    "203.0.113.7:22", // the IPv4 address it is
		"10.0.0.9:22":                "10.0.0.9:22",    // a private address behind a forward
		"203.0.113.7":                "",               // no port
		"203.0.113.7:0":              "",
		"203.0.113.7:70000":          "",
		"box.example:22":             "", // names are resolved on the laptop
		"127.0.0.1:22":               "",
		"[::1]:22":                   "",
		"0.0.0.0:22":                 "",
		"169.254.169.254:22":         "", // link-local is refused as a whole already
		"[fe80::1%eth0]:22":          "",
		"[fe80::1]:22":               "",
		"224.0.0.1:22":               "",
		"203.0.113.7:22 } accept; #": "",
		"# a comment":                "",
		"":                           "",
	} {
		got := ""
		if a, ok := outsideAddr(in); ok {
			got = a.String()
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestParseSSHAddrsBoundsOneRun(t *testing.T) {
	if got := joinAddrs(parseSSHAddrs("203.0.113.7:22,[2001:db8::7]:22,203.0.113.7:22,rubbish,,127.0.0.1:22")); got != "203.0.113.7:22, [2001:db8::7]:22" {
		t.Errorf("got %q", got)
	}
	var many []string
	for i := 1; i <= 12; i++ {
		many = append(many, fmt.Sprintf("192.0.2.%d:22", i))
	}
	got := parseSSHAddrs(strings.Join(many, ","))
	if len(got) != outsideAddrsPerRun || got[len(got)-1].String() != "192.0.2.8:22" {
		t.Errorf("one run added %v", got)
	}
	if got := parseSSHAddrs(""); len(got) != 0 {
		t.Errorf("nothing dialled gave %v", got)
	}
}

func TestMergeOutsideAddrsAccumulatesUpToTheCap(t *testing.T) {
	a, b, c := "203.0.113.7:22", "[2001:db8::7]:22", "198.51.100.4:2222"
	want, added, dropped := mergeOutsideAddrs(addrPorts(a), addrPorts(b, a, c))
	if joinAddrs(want) != a+", "+b+", "+c || joinAddrs(added) != b+", "+c || len(dropped) != 0 {
		t.Errorf("merge: want %v added %v dropped %v", want, added, dropped)
	}
	// Nothing dialled, or only what is on record: the record stays.
	for _, told := range [][]netip.AddrPort{nil, addrPorts(a)} {
		want, added, dropped = mergeOutsideAddrs(addrPorts(a, b), told)
		if joinAddrs(want) != a+", "+b || len(added)+len(dropped) != 0 {
			t.Errorf("merge with %v: want %v added %v dropped %v", told, want, added, dropped)
		}
	}
	// At the cap the address recorded longest ago goes.
	var full []netip.AddrPort
	for i := 1; i <= outsideAddrsCap; i++ {
		full = append(full, netip.MustParseAddrPort(fmt.Sprintf("192.0.2.%d:22", i)))
	}
	want, added, dropped = mergeOutsideAddrs(full, addrPorts(a, b))
	if len(want) != outsideAddrsCap || joinAddrs(dropped) != "192.0.2.1:22, 192.0.2.2:22" || joinAddrs(added) != a+", "+b ||
		want[0].String() != "192.0.2.3:22" || joinAddrs(want[outsideAddrsCap-2:]) != a+", "+b {
		t.Errorf("at the cap: want %v added %v dropped %v", want, added, dropped)
	}
	// An address still dialled after it was dropped is recorded again.
	again, added, _ := mergeOutsideAddrs(want, addrPorts("192.0.2.1:22"))
	if again[len(again)-1].String() != "192.0.2.1:22" || len(added) != 1 || len(again) != outsideAddrsCap {
		t.Errorf("dialled again: %v %v", again, added)
	}
	if !strings.Contains(strings.ReplaceAll(outsideAddrsHeader, "\n# ", " "), "keeps the "+strconv.Itoa(outsideAddrsCap)+" added last") {
		t.Error("the file's header does not state the cap")
	}
}

func TestOutsideAddrsFileRoundTrips(t *testing.T) {
	addrs := addrPorts("203.0.113.7:22", "[2001:db8::7]:22", "198.51.100.4:2222")
	text := renderOutsideAddrs(addrs)
	if !strings.HasPrefix(text, "# Managed by keelbox.") {
		t.Errorf("no managed header:\n%s", text)
	}
	if got := parseOutsideAddrs(text, "\n"); joinAddrs(got) != joinAddrs(addrs) {
		t.Errorf("read back %v", got)
	}
	// Lines that are not addresses, and repeats, are not read.
	edited := text + "not an address\n203.0.113.7:22\n  192.0.2.9:22  \n# 192.0.2.10:22\n"
	if got := joinAddrs(parseOutsideAddrs(edited, "\n")); got != joinAddrs(addrs)+", 192.0.2.9:22" {
		t.Errorf("hand-edited file read as %q", got)
	}
}

func TestAgentNetRulesRefuseOutsideAddresses(t *testing.T) {
	uids := []uidRange{{1001, 1001}}
	rules := AgentNetRules(uids, nil, addrPorts("203.0.113.7:22", "[2001:db8::7]:22", "198.51.100.4:2222"))
	for _, want := range []string{
		"tcp dport 22 fib daddr type local meta skuid != 0 counter reject with tcp reset",
		"\t\tip daddr 203.0.113.7 tcp dport 22 meta skuid != 0 counter reject with tcp reset comment \"",
		"\t\tip6 daddr 2001:db8::7 tcp dport 22 meta skuid != 0 counter reject with tcp reset comment \"",
		"\t\tip daddr 198.51.100.4 tcp dport 2222 meta skuid != 0 counter reject with tcp reset comment \"",
		"ip daddr 169.254.0.0/16 jump linklocal",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules missing %q:\n%s", want, rules)
		}
	}
	// Each is refused to every account but root, the agent's among them, and
	// to no other port or address.
	for _, uid := range []uint32{1001, 1000, 113} {
		for dst, want := range map[string]string{
			"203.0.113.7:22": "reject", "[2001:db8::7]:22": "reject", "198.51.100.4:2222": "reject",
			"203.0.113.7:2222": "accept", "198.51.100.4:22": "accept", "203.0.113.8:22": "accept",
		} {
			if got := verdict(t, rules, packet{uid: uid, owned: true, proto: "tcp", dst: netip.MustParseAddrPort(dst)}); got != want {
				t.Errorf("uid %d to %s: %s, want %s", uid, dst, got, want)
			}
		}
	}
	if got := verdict(t, rules, packet{owned: true, proto: "tcp", dst: netip.MustParseAddrPort("203.0.113.7:22")}); got != "accept" {
		t.Errorf("root to an outside address: %s", got)
	}
	if AgentNetRules(uids, nil, nil) != strings.ReplaceAll(rules, rulesFor(rules), "") {
		t.Error("without outside addresses the table is not the same minus their rules")
	}
}

// rulesFor returns the outside address rules of a rendered table.
func rulesFor(rules string) string {
	var out strings.Builder
	for _, l := range strings.SplitAfter(rules, "\n") {
		if strings.Contains(l, "it is reached here from outside") {
			out.WriteString(l)
		}
	}
	return out.String()
}

func TestNftRejectsMatchesWholeAddresses(t *testing.T) {
	listing := `table inet keelbox_agent {
	chain output {
		tcp dport 22 fib daddr type local meta skuid != 0 counter packets 0 bytes 0 reject with tcp reset
		ip daddr 11.2.3.45 tcp dport 22 meta skuid != 0 counter packets 0 bytes 0 reject with tcp reset comment "x"
		ip daddr 198.51.100.4 tcp dport 2222 meta skuid != "root" counter packets 3 bytes 180 reject with tcp reset comment "x"
		ip6 daddr 2001:db8::7 tcp dport 22 meta skuid != 0 counter packets 0 bytes 0 reject with tcp reset comment "x"
		ip daddr 192.0.2.1 tcp dport 22 meta skuid != 0 counter packets 0 bytes 0 accept
		ip daddr 192.0.2.2 tcp dport 22 counter packets 0 bytes 0 reject with tcp reset comment "meta skuid != 0"
		ip daddr 192.0.2.3 tcp dport 22 meta skuid != 1001 counter packets 0 bytes 0 reject with tcp reset
		ip daddr 192.0.2.4 tcp dport 22 meta skuid 0 counter packets 0 bytes 0 reject with tcp reset
	}
}`
	x := system.NewFake()
	x.Users["root"] = system.User{Name: "root", Home: "/root"}
	for in, want := range map[string]bool{
		"11.2.3.45:22":      true,
		"1.2.3.4:22":        false, // part of another address
		"198.51.100.4:2222": true,
		"198.51.100.4:22":   false, // another port
		"[2001:db8::7]:22":  true,
		"[2001:db8::70]:22": false,
		"192.0.2.1:22":      false, // not a reject
		"192.0.2.2:22":      false, // an older keelbox's rule, behind its accept for other accounts
		"192.0.2.3:22":      false, // every account but one that is not root
		"192.0.2.4:22":      false, // root alone
	} {
		if got := nftRejects(x, listing, netip.MustParseAddrPort(in)); got != want {
			t.Errorf("%s: %v, want %v", in, got, want)
		}
	}
}

func agentNetFinding(t *testing.T, ctx context.Context, e *engine.Env) engine.Finding {
	t.Helper()
	for _, f := range (Host{}).Verify(ctx, e) {
		if f.Check == "agent-net" {
			return f
		}
	}
	t.Fatal("no agent-net finding")
	return engine.Finding{}
}

func stepPending(t *testing.T, ctx context.Context, e *engine.Env, step string) (string, bool) {
	t.Helper()
	for _, it := range engine.Plan(ctx, e, All()) {
		if it.Step == step {
			if it.Err != nil {
				t.Fatalf("%s: %v", step, it.Err)
			}
			return it.Detail, !it.OK
		}
	}
	t.Fatalf("no step %s", step)
	return "", false
}

// A box whose public address is translated on the way in (AWS, Google
// Cloud) learns it from the laptop: the address is recorded, gets its own
// reject rule in the loaded table, and stays when a later apply arrives
// over the tailnet or runs on the box.
func TestOutsideAddressIsRecordedAndRefused(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	e.SSHConnection = "198.51.100.77 50000 10.0.0.5 22"
	e.SSHAddrs = "203.0.113.7:22,[2001:db8::7]:22"

	// plan names the address, and shows the rules changing with it.
	if _, err := engine.Apply(ctx, simEnv(t, s, testConfig(t, "")), All()); err != nil {
		t.Fatal(err)
	}
	if d, pending := stepPending(t, ctx, e, "agent-net-addresses"); !pending || !strings.Contains(d, "record 203.0.113.7:22, [2001:db8::7]:22") {
		t.Errorf("plan for the addresses: %q", d)
	}
	if _, pending := stepPending(t, ctx, e, "agent-net-rules"); !pending {
		t.Error("plan does not show the rules changing")
	}

	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 2 {
		t.Errorf("recording the address changed %v", res.Changes)
	}
	if got := s.content(outsideAddrsPath); got != outsideAddrsHeader+"203.0.113.7:22\n[2001:db8::7]:22\n" {
		t.Errorf("record:\n%s", got)
	}
	if st, _ := s.Stat(outsideAddrsPath); st.Mode != 0o644 || st.Owner != "root" {
		t.Errorf("record's mode and owner: %+v", st)
	}
	for _, want := range []string{"ip daddr 203.0.113.7 tcp dport 22 meta skuid != 0 counter packets 0 bytes 0 reject with tcp reset", "ip6 daddr 2001:db8::7 tcp dport 22 meta skuid != 0 counter packets 0 bytes 0 reject with tcp reset", "fib daddr type local"} {
		if out, _ := s.Run(ctx, cmdNftList()); !strings.Contains(out.Stdout, want) {
			t.Errorf("loaded table lacks %q:\n%s", want, out.Stdout)
		}
	}
	if !s.Ran("nft --check -f " + agentNetRules) {
		t.Error("rules not validated before loading")
	}
	f := agentNetFinding(t, ctx, e)
	if f.Level != engine.LevelOK || !strings.Contains(f.Detail, "203.0.113.7:22, [2001:db8::7]:22, where it is reached from outside") {
		t.Errorf("doctor: %+v", f)
	}

	// The same address again, the tailnet, and a run on the box itself
	// change nothing: what was learnt is kept.
	s.tailscale = "Running"
	for _, told := range []string{e.SSHAddrs, "100.101.102.103:22", "10.0.0.5:22,[::1]:22", ""} {
		e.SSHAddrs = told
		for _, step := range []string{"agent-net-addresses", "agent-net-rules"} {
			if d, pending := stepPending(t, ctx, e, step); pending {
				t.Errorf("told %q: %s pending: %s", told, step, d)
			}
		}
	}
	if got := s.content(outsideAddrsPath); got != outsideAddrsHeader+"203.0.113.7:22\n[2001:db8::7]:22\n" {
		t.Errorf("record after later runs:\n%s", got)
	}
}

// simRules is the rules file apply writes on the sim as it is now, for
// these outside addresses.
func simRules(t *testing.T, s *sim, outside []netip.AddrPort) string {
	t.Helper()
	uids := simAgentUIDs(t, s)
	allowed, err := linkLocalUIDs(s, uids, nil)
	if err != nil {
		t.Fatal(err)
	}
	return AgentNetRules(uids, allowed, outside)
}

// simAgentUIDs is the agent's uid and subordinate ranges on the sim.
func simAgentUIDs(t *testing.T, s *sim) []uidRange {
	t.Helper()
	uids, err := agentUIDs(s, "agent")
	if err != nil {
		t.Fatal(err)
	}
	return uids
}

func cmdNftList() system.Cmd { return system.C("nft", "list", "table", "inet", "keelbox_agent") }

// A box reached at its own address (Hetzner, DigitalOcean, a LAN machine)
// records nothing: the local address rule covers it.
func TestLocalAddressIsNotRecorded(t *testing.T) {
	s := newSim(t)
	s.localAddrs = []string{"203.0.113.7", "2001:db8::7"}
	e := simEnv(t, s, testConfig(t, ""))
	e.SSHAddrs = "203.0.113.7:22,[2001:db8::7]:22"
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := s.content(outsideAddrsPath); got != outsideAddrsHeader {
		t.Errorf("record:\n%s", got)
	}
	if got := s.content(agentNetRules); got != simRules(t, s, nil) {
		t.Errorf("rules:\n%s", got)
	}
	f := agentNetFinding(t, ctx, e)
	if f.Level != engine.LevelOK || f.Detail != "only root may open SSH to an address of this box, or reach link-local and metadata addresses (the agent and its subordinate uids included; "+
		"DNS, time and address configuration there stay open to systemd-resolve, systemd-timesync, systemd-network)" {
		t.Errorf("doctor: %+v", f)
	}
}

// The record can be edited by hand: a deleted line leaves the table at the
// next apply, an added one enters it, and until then doctor fails because
// the loaded table has no rule for it.
func TestOutsideAddressesEditedByHand(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	e.SSHAddrs = "203.0.113.7:22"
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	e.SSHAddrs = ""

	// The owner adds the router's forwarded port.
	s.AddFile(outsideAddrsPath, outsideAddrsHeader+"203.0.113.7:22\n198.51.100.4:2222\n", 0o644)
	f := agentNetFinding(t, ctx, e)
	if f.Level != engine.LevelFail || !strings.Contains(f.Detail, "no rule for 198.51.100.4:2222") || !strings.HasSuffix(f.Detail, ": keelbox apply testbox") {
		t.Errorf("doctor with an address the table lacks: %+v", f)
	}
	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 1 || res.Changes[0].Step != "agent-net-rules" {
		t.Errorf("changes: %v", res.Changes)
	}
	if out, _ := s.Run(ctx, cmdNftList()); !strings.Contains(out.Stdout, "ip daddr 198.51.100.4 tcp dport 2222 meta skuid != 0 counter packets 0 bytes 0 reject") {
		t.Errorf("the table was not reloaded with the new address:\n%s", out.Stdout)
	}
	if f := agentNetFinding(t, ctx, e); f.Level != engine.LevelOK {
		t.Errorf("doctor after apply: %+v", f)
	}

	// The box moved: the owner deletes the old address.
	s.AddFile(outsideAddrsPath, outsideAddrsHeader+"198.51.100.4:2222\n", 0o644)
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := s.content(agentNetRules); got != simRules(t, s, addrPorts("198.51.100.4:2222")) {
		t.Errorf("rules after the deletion:\n%s", got)
	}
	if out, _ := s.Run(ctx, cmdNftList()); strings.Contains(out.Stdout, "203.0.113.7") {
		t.Errorf("the deleted address is still in the loaded table:\n%s", out.Stdout)
	}
	if got := s.content(outsideAddrsPath); got != outsideAddrsHeader+"198.51.100.4:2222\n" {
		t.Errorf("record after the deletion:\n%s", got)
	}

	// Anything else in the file is rewritten away, keeping the addresses.
	s.AddFile(outsideAddrsPath, "my notes\n198.51.100.4:2222\n198.51.100.4:2222\n", 0o644)
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := s.content(outsideAddrsPath); got != outsideAddrsHeader+"198.51.100.4:2222\n" {
		t.Errorf("record after a rewrite:\n%s", got)
	}
}

// Past the cap the address recorded longest ago is forgotten, and plan
// says which.
func TestOutsideAddressesAtTheCap(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	var lines strings.Builder
	for i := 1; i <= outsideAddrsCap; i++ {
		fmt.Fprintf(&lines, "192.0.2.%d:22\n", i)
	}
	s.AddFile(outsideAddrsPath, outsideAddrsHeader+lines.String(), 0o644)
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	e.SSHAddrs = "203.0.113.7:22"
	if d, _ := stepPending(t, ctx, e, "agent-net-addresses"); !strings.Contains(d, "record 203.0.113.7:22") || !strings.Contains(d, "forget 192.0.2.1:22, recorded longest ago") {
		t.Errorf("plan at the cap: %q", d)
	}
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	got, err := readOutsideAddrs(s)
	if err != nil || len(got) != outsideAddrsCap || got[0].String() != "192.0.2.2:22" || got[len(got)-1].String() != "203.0.113.7:22" {
		t.Errorf("record at the cap: %v %v", got, err)
	}
	rules := s.content(agentNetRules)
	if strings.Contains(rules, "ip daddr 192.0.2.1 ") || !strings.Contains(rules, "ip daddr 203.0.113.7 ") || strings.Count(rules, "it is reached here from outside") != outsideAddrsCap {
		t.Errorf("rules at the cap:\n%s", rules)
	}
	f := agentNetFinding(t, ctx, e)
	if f.Level != engine.LevelOK || !strings.Contains(f.Detail, "the 16 outside addresses in "+outsideAddrsPath) {
		t.Errorf("doctor: %+v", f)
	}
}
