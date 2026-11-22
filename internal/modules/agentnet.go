package modules

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

const (
	agentNetRules = "/etc/keelbox/agent-net.nft"
	agentNetUnit  = "/etc/systemd/system/keelbox-agent-net.service"
	agentNetName  = "keelbox-agent-net.service"
)

// AgentNetRules renders the nftables table that limits what the accounts of
// the box can connect to. It matches on the owner of the socket. Two things
// are refused, to every account but root:
//
//   - SSH to the box itself: port 22 on any address of the box, and each
//     address and port in outside, where keelbox has reached the box's sshd
//     from outside and which no interface carries (a cloud's public address,
//     a router's forwarded port; outsideaddrs.go). The agent shares a box
//     with the admin, whose key may also be the agent's; without this an
//     agent could try ssh admin@localhost and, with a forwarded or cached
//     agent, gain sudo.
//   - Link-local addresses, which include cloud metadata services
//     (169.254.169.254, fd00:ec2::254). They serve instance data, including
//     cloud-init user data, to any process that asks.
//
// The limits began as the agent's alone: uids is its own uid and its
// subordinate ranges (agentuids.go), which also covers its rootless
// containers, whose traffic leaves through the agent's own slirp4netns or
// pasta process. They apply to every other account too because the agent
// can have packages installed (users.agent.permissions: auto), and a
// service that runs under an account of its own and connects where the
// agent tells it would otherwise pass
// (docs/adr/0076-network-limits-for-every-account.md). On link-local
// addresses the agent's uids are refused first; then come the accounts in
// allowed, each for one kind of traffic, or for all of it when the box's
// config names the account (host.firewall.link_local_accounts); then every
// other account but root.
//
// Every reject carries a "meta skuid" match. A packet the kernel sends by
// itself (neighbour discovery to a link-local router, a TCP reset) has no
// socket that a process opened: the match cannot be evaluated, so the rule
// does not apply and the packet passes.
//
// The table is replaced as a whole, so editing it never leaves stale rules.
// The same input always renders the same text.
func AgentNetRules(uids []uidRange, allowed []linkLocalUID, outside []netip.AddrPort) string {
	var ssh strings.Builder
	for _, a := range outside {
		family := "ip"
		if a.Addr().Is6() {
			family = "ip6"
		}
		fmt.Fprintf(&ssh, "\t\t%s daddr %s tcp dport %d meta skuid != 0 counter reject with tcp reset comment \"only root may ssh into this box: it is reached here from outside\"\n",
			family, a.Addr(), a.Port())
	}
	var except strings.Builder
	for _, a := range sortedLinkLocal(allowed) {
		match := fmt.Sprintf("%s dport %d ", a.proto, a.port)
		switch a.proto {
		case protoICMPv6:
			match = "meta l4proto " + protoICMPv6 + " "
		case protoAny:
			match = ""
		}
		fmt.Fprintf(&except, "\t\tmeta skuid %d %saccept comment \"%s: %s\"\n", a.uid, match, a.account, a.what)
	}
	return fmt.Sprintf(`#!/usr/sbin/nft -f
# Managed by keelbox. Limits the outbound connections of every account but
# root, the agent user's first; see https://keelbox.dev/docs/security-model.
# Loaded by keelbox-agent-net.service.
table inet keelbox_agent
delete table inet keelbox_agent
table inet keelbox_agent {
	chain linklocal {
		meta skuid %[1]s counter reject comment "agent: link-local and cloud metadata"
%[2]s		meta skuid != 0 counter reject comment "only root: link-local and cloud metadata"
	}
	chain output {
		type filter hook output priority filter; policy accept;
		tcp dport 22 fib daddr type local meta skuid != 0 counter reject with tcp reset comment "only root may ssh into this box"
%[3]s		ip daddr 169.254.0.0/16 jump linklocal
		ip6 daddr { fe80::/10, fd00:ec2::254 } jump linklocal
	}
}
`, nftUIDs(uids), except.String(), ssh.String())
}

// protoICMPv6 is ICMPv6 as nftables names it after "meta l4proto".
const protoICMPv6 = "ipv6-icmp"

// protoAny stands for every protocol and port: the exception of an account
// the box's config names.
const protoAny = "any"

// linkLocalAccountsKey is the config key that names more accounts.
const linkLocalAccountsKey = "host.firewall.link_local_accounts"

// linkLocalAccount is a system account that needs a link-local service, and
// the one kind of traffic it needs there.
type linkLocalAccount struct {
	account string
	proto   string // udp, tcp, protoICMPv6 or protoAny
	port    uint16 // the destination port; 0 with protoICMPv6 and protoAny
	what    string
}

// linkLocalAccounts are the exceptions to the link-local reject that every
// box gets. They are fixed here; the only other source is the box's config
// (linkLocalAccountsKey), which root keeps and the agent cannot write. Each
// is an account Ubuntu's own packages create for one service, which
// connects where its root-owned configuration or the network tells it, not
// where a local caller asks.
//
//   - systemd-resolve: the resolver is link-local on Google Cloud
//     (169.254.169.254) and can be on AWS (169.254.169.253), and a router
//     may announce its link-local IPv6 address as the resolver.
//   - systemd-timesync, _chrony: AWS's time service is 169.254.169.123,
//     Google's the metadata address.
//   - systemd-network: a DHCP lease is renewed by UDP to the server that
//     gave it, which is 169.254.169.254 on Google Cloud; a DHCPv6 server and
//     an IPv6 router are reached at link-local addresses.
var linkLocalAccounts = []linkLocalAccount{
	{"systemd-resolve", "udp", 53, "DNS"},
	{"systemd-resolve", "tcp", 53, "DNS"},
	{"systemd-timesync", "udp", 123, "NTP"},
	{"_chrony", "udp", 123, "NTP"},
	{"systemd-network", "udp", 67, "DHCP"},
	{"systemd-network", "udp", 547, "DHCPv6"},
	{"systemd-network", protoICMPv6, 0, "IPv6 router and neighbour messages"},
}

// linkLocalUID is a linkLocalAccount on this box: the account's uid.
type linkLocalUID struct {
	uid uint32
	linkLocalAccount
}

// key is what makes two exceptions the same rule.
func (a linkLocalUID) key() string { return fmt.Sprintf("%d %s %d", a.uid, a.proto, a.port) }

// sortedLinkLocal orders exceptions by uid, protocol and port, so the rules
// are written one way.
func sortedLinkLocal(in []linkLocalUID) []linkLocalUID {
	out := append([]linkLocalUID(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.uid != b.uid {
			return a.uid < b.uid
		}
		if a.proto != b.proto {
			return a.proto < b.proto
		}
		return a.port < b.port
	})
	return out
}

// linkLocalUIDs resolves linkLocalAccounts, and the accounts the config
// names for every port, on this box. An account the box does not have is
// left out. So is one whose uid is root's, which no rule refuses, or one of
// the agent's, which are refused whatever their name.
func linkLocalUIDs(x system.Executor, agent []uidRange, named []string) ([]linkLocalUID, error) {
	accounts := append([]linkLocalAccount(nil), linkLocalAccounts...)
	for _, n := range named {
		accounts = append(accounts, linkLocalAccount{n, protoAny, 0, linkLocalAccountsKey})
	}
	var out []linkLocalUID
	for _, a := range accounts {
		u, err := x.LookupUser(a.account)
		if errors.Is(err, system.ErrNoUser) {
			continue
		}
		if err != nil {
			return nil, err
		}
		uid, err := accountUID(u)
		if err != nil {
			return nil, err
		}
		if uid == 0 || inUIDs(agent, uid) {
			continue
		}
		out = append(out, linkLocalUID{uid, a})
	}
	return sortedLinkLocal(out), nil
}

func inUIDs(rs []uidRange, uid uint32) bool {
	for _, r := range rs {
		if r.first <= uid && uid <= r.last {
			return true
		}
	}
	return false
}

const agentNetUnitText = `# Managed by keelbox. Loads the network limits of every account but root at boot.
[Unit]
Description=keelbox agent network limits
DefaultDependencies=no
Before=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f ` + agentNetRules + `
ExecStop=/usr/sbin/nft delete table inet keelbox_agent

[Install]
WantedBy=multi-user.target
`

// agentNetSteps record the outside addresses, install the rules, the unit
// that loads them at boot, and reload the table whenever the rules change.
func agentNetSteps(agent string) []engine.Step {
	return []engine.Step{
		outsideAddrsStep(),
		agentNetRulesStep(agent),
		engine.FileStep("agent-net-unit", engine.File{Path: agentNetUnit, Content: []byte(agentNetUnitText), Mode: 0o644,
			OnChange: func(ctx context.Context, e *engine.Env) error {
				_, err := e.X.Run(ctx, system.C("systemctl", "daemon-reload"))
				return err
			}}),
		engine.ServiceStep("agent-net-service", agentNetName),
	}
}

// agentNetRulesStep writes the rules for the agent's uids, for the accounts
// of linkLocalAccounts the box has, and for the outside addresses this run
// records: those on record and those the laptop told this run, so plan
// shows the rules changing before the addresses file has.
func agentNetRulesStep(agent string) engine.Step {
	reload := func(ctx context.Context, e *engine.Env) error {
		if !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", agentNetName)) {
			return nil // the service step starts it
		}
		_, err := e.X.Run(ctx, system.C("systemctl", "restart", agentNetName))
		return err
	}
	file := func(ctx context.Context, e *engine.Env) (engine.Step, error) {
		uids, err := agentUIDs(e.X, agent)
		if err != nil {
			return engine.Step{}, err
		}
		allowed, err := linkLocalUIDs(e.X, uids, e.Cfg.Host.Firewall.LinkLocalAccounts)
		if err != nil {
			return engine.Step{}, err
		}
		p, err := wantOutsideAddrs(ctx, e)
		return engine.FileStep("agent-net-rules", engine.File{
			Path: agentNetRules, Content: []byte(AgentNetRules(uids, allowed, p.want)), Mode: 0o644,
			Validate: func(ctx context.Context, e *engine.Env) error {
				_, err := e.X.Run(ctx, system.C("nft", "--check", "-f", agentNetRules))
				return err
			},
			OnChange: reload,
		}), err
	}
	return engine.Step{
		Name: "agent-net-rules",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			f, err := file(ctx, e)
			if errors.Is(err, system.ErrNoUser) {
				// Before the first apply: the account's own step comes first.
				return engine.Pending("create %s", agentNetRules), nil
			}
			if err != nil {
				return engine.Status{}, err
			}
			return f.Check(ctx, e)
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			f, err := file(ctx, e)
			if err != nil {
				return err
			}
			return f.Apply(ctx, e)
		},
	}
}

// refreshAgentNet rewrites and reloads the table when what it is rendered
// from changed after its own step ran: the agent's subordinate uid range,
// which the runtime module gives an account that has none. An account of
// linkLocalAccounts that appears later (a package that brings chrony) is in
// the rules from the next apply; doctor fails until then.
func refreshAgentNet(ctx context.Context, e *engine.Env) error {
	s := agentNetRulesStep(e.Cfg.Users.Agent.Name)
	st, err := s.Check(ctx, e)
	if err != nil || st.OK {
		return err
	}
	return s.Apply(ctx, e)
}

// verifyAgentNet checks the table is loaded, not just that its file exists,
// and that what is loaded is what apply would load now: the two rejects
// apply to every account but root, the agent's uids are all there, the
// exceptions are those of the accounts this box has, and every outside
// address on record has its rule. A table an older keelbox wrote, which
// limited the agent alone, fails here until the next apply.
func verifyAgentNet(ctx context.Context, e *engine.Env) engine.Finding {
	fix := "keelbox apply " + e.Cfg.Box
	out, err := system.Output(ctx, e.X, system.C("nft", "list", "table", "inet", "keelbox_agent"))
	if err != nil || !strings.Contains(out, "fib daddr type local") {
		return fail("host", "agent-net", "network limits not loaded (systemctl status "+agentNetName+"): "+fix)
	}
	loaded := readLoadedNet(e.X, out)
	if !loaded.everyAccount() {
		return fail("host", "agent-net", "the loaded network limits do not refuse the box's SSH port and link-local addresses to every account but root "+
			"(the rules of an older keelbox limited the agent alone): "+fix)
	}
	agent := e.Cfg.Users.Agent.Name
	uids, err := agentUIDs(e.X, agent)
	if err != nil {
		return fail("host", "agent-net", "cannot tell which uids are "+agent+"'s: "+err.Error()+": "+fix)
	}
	if nftUIDs(loaded.agent) != nftUIDs(uids) {
		return fail("host", "agent-net", "the loaded network limits take uid "+nftUIDs(loaded.agent)+" for the agent's, but "+agent+
			" and its subordinate uids in "+subUIDPath+" are "+nftUIDs(uids)+": "+fix)
	}
	allowed, err := linkLocalUIDs(e.X, uids, e.Cfg.Host.Firewall.LinkLocalAccounts)
	if err != nil {
		return fail("host", "agent-net", "cannot tell which accounts need a link-local service: "+err.Error()+": "+fix)
	}
	var want []string
	for _, a := range allowed {
		want = append(want, a.key())
	}
	sort.Strings(want)
	if got := loaded.allowed; strings.Join(got, ", ") != strings.Join(want, ", ") {
		return fail("host", "agent-net", "the loaded network limits let "+orNothing(got)+" through to link-local addresses (uid, protocol, port), "+
			"and for the accounts this box has and "+linkLocalAccountsKey+" they should let "+orNothing(want)+" through: "+fix)
	}
	recorded, err := readOutsideAddrs(e.X)
	if err != nil {
		return fail("host", "agent-net", "cannot read "+outsideAddrsPath+": "+err.Error()+": "+fix)
	}
	var missing []string
	for _, a := range recorded {
		if !nftRejects(e.X, out, a) {
			missing = append(missing, a.String())
		}
	}
	if len(missing) > 0 {
		return fail("host", "agent-net", "the loaded network limits have no rule for "+strings.Join(missing, ", ")+
			", which "+outsideAddrsPath+" records as reaching this box: "+fix)
	}
	where := "an address of this box"
	switch n := len(recorded); {
	case n > 3:
		where += fmt.Sprintf(" or to the %d outside addresses in %s", n, outsideAddrsPath)
	case n > 0:
		where += " or to " + joinAddrs(recorded) + ", where it is reached from outside"
	}
	who := "the agent"
	if len(uids) > 1 || uids[0].first != uids[0].last {
		who = "the agent and its subordinate uids"
	}
	detail := "only root may open SSH to " + where + ", or reach link-local and metadata addresses (" + who + " included"
	if names := linkLocalNames(allowed); len(names) > 0 {
		detail += "; DNS, time and address configuration there stay open to " + strings.Join(names, ", ")
	}
	var named []string
	for _, a := range allowed {
		if a.proto == protoAny {
			named = append(named, a.account)
		}
	}
	if len(named) > 0 {
		detail += "; every port there stays open to " + strings.Join(named, ", ") + ", named in " + linkLocalAccountsKey
	}
	return ok("host", "agent-net", detail+")")
}

// verifyLinkLocalAccounts warns about a name in the config that gets no
// rule on this box: apply leaves it out instead of failing, since a service
// may be installed after the box is set up.
func verifyLinkLocalAccounts(e *engine.Env) []engine.Finding {
	var out []engine.Finding
	uids, _ := agentUIDs(e.X, e.Cfg.Users.Agent.Name)
	for _, n := range e.Cfg.Host.Firewall.LinkLocalAccounts {
		why := ""
		u, err := e.X.LookupUser(n)
		switch {
		case errors.Is(err, system.ErrNoUser):
			why = "which is not an account on this box, so it has no rule: install what creates the account, or remove the name from the config, then"
		case err != nil:
			why = "which cannot be looked up (" + err.Error() + "):"
		case u.UID == 0:
			why = "whose uid is root's, which no rule limits: remove the name from the config, then"
		case u.UID > 0 && int64(u.UID) <= maxUID && inUIDs(uids, uint32(u.UID)): //nolint:gosec // G115: in range, checked here
			why = "whose uid is one of the agent's, which stay refused: remove the name from the config, then"
		default:
			continue
		}
		out = append(out, warn("host", "link-local-accounts", linkLocalAccountsKey+" names "+n+", "+why+" keelbox apply "+e.Cfg.Box))
	}
	return out
}

func orNothing(l []string) string {
	if len(l) == 0 {
		return "nothing"
	}
	return strings.Join(l, ", ")
}

// linkLocalNames are the accounts of the exceptions every box gets, each
// once, in the order of linkLocalAccounts.
func linkLocalNames(allowed []linkLocalUID) []string {
	var out []string
	for _, a := range linkLocalAccounts {
		for _, b := range allowed {
			if a.account == b.account && b.proto != protoAny && !contains(out, a.account) {
				out = append(out, a.account)
			}
		}
	}
	return out
}

// loadedNet is what a listed table enforces, read back from nft's own
// rendering of it. nft prints a rule its own way (counters with their
// values, the elements of a set in its own order, an account by uid or by
// quoted name), so the rules are read by their parts and not compared as
// text.
type loadedNet struct {
	// ssh: port 22 on the box's own addresses is refused to every uid but root.
	ssh bool
	// jump4 and jump6: 169.254.0.0/16, and fe80::/10 with fd00:ec2::254, go
	// to the link-local chain.
	jump4, jump6 bool
	// agent is the uids refused there before any exception.
	agent []uidRange
	// allowed is the exceptions there, as linkLocalUID.key, sorted. An accept
	// anywhere in the table that is not one reads as "?".
	allowed []string
	// rest: every other uid but root is refused there.
	rest bool
}

// everyAccount reports whether both rejects reach every account but root.
// The table of a keelbox from before ADR 0076 has neither part.
func (n loadedNet) everyAccount() bool { return n.ssh && n.jump4 && n.jump6 && n.rest }

func readLoadedNet(x system.Executor, listing string) loadedNet {
	var n loadedNet
	chain := ""
	for _, l := range strings.Split(listing, "\n") {
		f := ruleFields(l)
		if len(f) >= 2 && f[0] == "chain" {
			chain = f[1]
			continue
		}
		uids, negated, hasUID := listedSkuid(x, f)
		// allButRoot: the rule applies to every uid except 0.
		allButRoot := hasUID && negated && len(uids) == 1 && uids[0] == uidRange{}
		// narrowed: the rule matches on more than the socket's owner.
		narrowed := contains(f, "daddr") || contains(f, "dport") || contains(f, "l4proto")
		switch {
		case follows(f, "jump", "linklocal"):
			// For every packet to these addresses, whoever owns it.
			all := chain == "output" && !contains(f, "skuid") && !contains(f, "dport") && !contains(f, "l4proto")
			n.jump4 = n.jump4 || (all && hasElement(f, "169.254.0.0/16"))
			n.jump6 = n.jump6 || (all && hasElement(f, "fe80::/10") && hasElement(f, "fd00:ec2::254"))
		case contains(f, "accept"):
			n.allowed = append(n.allowed, listedException(f, chain, uids, hasUID && !negated))
		case !contains(f, "reject"):
		case chain == "output":
			n.ssh = n.ssh || (allButRoot && follows(f, "dport", "22") && follows(f, "type", "local") && !contains(f, "saddr"))
		case chain == "linklocal" && allButRoot:
			n.rest = n.rest || !narrowed
		case chain == "linklocal" && hasUID && !negated && !narrowed:
			n.agent = mergeUIDs(append(n.agent, uids...))
		}
	}
	sort.Strings(n.allowed)
	return n
}

// ruleFields splits one listed line into its words, without its comment.
func ruleFields(line string) []string {
	line, _, _ = strings.Cut(line, ` comment "`)
	return strings.Fields(line)
}

// hasElement reports whether value is a word of the rule, alone or as an
// element of a set.
func hasElement(fields []string, value string) bool {
	for _, f := range fields {
		if strings.Trim(f, "{},") == value {
			return true
		}
	}
	return false
}

// listedSkuid reads a rule's "meta skuid" match: the uids it names, and
// whether the rule is for every uid but those.
func listedSkuid(x system.Executor, fields []string) (uids []uidRange, negated, found bool) {
	i := 0
	for ; i < len(fields) && fields[i] != "skuid"; i++ {
	}
	i++
	if i < len(fields) && fields[i] == "!=" {
		negated = true
		i++
	}
	if i >= len(fields) {
		return nil, false, false
	}
	set := fields[i]
	if strings.HasPrefix(set, "{") {
		for i++; i < len(fields) && !strings.HasSuffix(set, "}"); i++ {
			set += " " + fields[i]
		}
	}
	uids = parseListedUIDs(x, set)
	return uids, negated, uids != nil
}

// listedException reads an accept rule as an exception to the link-local
// reject: one uid, and UDP or TCP to one port, ICMPv6, or no further match
// (an account the config names). Any other accept reads as "?", which no
// box should have loaded.
func listedException(fields []string, chain string, uids []uidRange, named bool) string {
	if chain != "linklocal" || !named || len(uids) != 1 || uids[0].first != uids[0].last || contains(fields, "daddr") {
		return "?"
	}
	uid := uids[0].first
	for i := 1; i+1 < len(fields); i++ {
		if fields[i] != "dport" || (fields[i-1] != "udp" && fields[i-1] != "tcp") {
			continue
		}
		port, err := strconv.ParseUint(fields[i+1], 10, 16)
		if err != nil || contains(fields, "l4proto") {
			return "?"
		}
		return linkLocalUID{uid, linkLocalAccount{proto: fields[i-1], port: uint16(port)}}.key()
	}
	for _, p := range []string{protoICMPv6, "icmpv6", "58"} {
		if follows(fields, "l4proto", p) && !contains(fields, "dport") {
			return linkLocalUID{uid, linkLocalAccount{proto: protoICMPv6}}.key()
		}
	}
	if !contains(fields, "dport") && !contains(fields, "l4proto") && !contains(fields, "saddr") {
		return linkLocalUID{uid, linkLocalAccount{proto: protoAny}}.key()
	}
	return "?"
}

// nftRejects reports whether a listed table refuses one address and port to
// every uid but root. nft prints the address and port as keelbox wrote
// them.
func nftRejects(x system.Executor, listing string, a netip.AddrPort) bool {
	addr, port := a.Addr().String(), strconv.Itoa(int(a.Port()))
	for _, l := range strings.Split(listing, "\n") {
		f := ruleFields(l)
		uids, negated, found := listedSkuid(x, f)
		if follows(f, "daddr", addr) && follows(f, "dport", port) && contains(f, "reject") &&
			found && negated && len(uids) == 1 && uids[0] == (uidRange{}) {
			return true
		}
	}
	return false
}

// follows reports whether value comes straight after key in fields.
func follows(fields []string, key, value string) bool {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == key && fields[i+1] == value {
			return true
		}
	}
	return false
}
