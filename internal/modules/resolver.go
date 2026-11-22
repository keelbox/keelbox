package modules

import (
	"context"
	"net/netip"
	"path"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/inventory"
	"github.com/keelbox/keelbox/internal/system"
)

// The network limits refuse link-local addresses to every account but root
// (agentnet.go). Name resolution survives that where programs ask a stub on
// the box, as Ubuntu's images do (127.0.0.53, systemd-resolved, whose own
// account may reach a link-local resolver). A machine whose
// /etc/resolv.conf names a link-local resolver directly has every program
// send its own queries there, and with the limits loaded only root's get
// through: the agent, the admin and apt's downloads (the account _apt)
// resolve nothing. Doctor fails on that state, and the impact report names
// it before the first apply (docs/adr/0076-network-limits-for-every-account.md).

const resolvConfPath = "/etc/resolv.conf"

// resolvConfMax bounds what is read of the file; the C library reads three
// nameserver lines.
const (
	resolvConfMax     = 64 << 10
	resolvConfServers = 3
)

const stubFix = "sudo systemctl enable --now systemd-resolved && sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf"

// readResolvConf reads /etc/resolv.conf, through one symbolic link if it
// is one. Where the file it points at is in a directory another account
// owns (systemd-resolved's, under /run), it is read as that account and as
// data: a regular file only, without blocking. "" when there is no file or
// it cannot be read that way.
func readResolvConf(x system.Executor) string {
	p := resolvConfPath
	st, err := x.Stat(p)
	if err != nil || !st.Exists || st.IsDir {
		return ""
	}
	if st.IsSymlink {
		p = st.LinkTarget
		if !path.IsAbs(p) {
			p = path.Join(path.Dir(resolvConfPath), p)
		}
		p = path.Clean(p)
	}
	dir, err := x.Stat(path.Dir(p))
	if err != nil || !dir.Exists || !dir.IsDir {
		return ""
	}
	var data []byte
	if dir.Owner == "root" {
		if st, err := x.Stat(p); err != nil || !st.Exists || st.IsDir || st.IsSymlink || st.Size > resolvConfMax {
			return ""
		}
		data, err = x.ReadFile(p)
	} else {
		data, err = system.ReadData(x.UserFS(dir.Owner), p, resolvConfMax)
	}
	if err != nil {
		return ""
	}
	return string(data)
}

// nameservers are the resolvers a resolv.conf names, the first three, as
// the C library takes them.
func nameservers(conf string) []netip.Addr {
	var out []netip.Addr
	for _, l := range strings.Split(conf, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		if a, err := netip.ParseAddr(f[1]); err == nil && len(out) < resolvConfServers {
			out = append(out, a.Unmap())
		}
	}
	return out
}

// linkLocalResolvers returns the box's resolvers when every one of them is
// link-local, so that no account but root can resolve a name. A file that
// also names a resolver elsewhere is left alone: the refusal is immediate,
// and the C library goes on to the next one.
func linkLocalResolvers(x system.Executor) []string {
	servers := nameservers(readResolvConf(x))
	var out []string
	for _, a := range servers {
		if !a.IsLinkLocalUnicast() {
			return nil
		}
		out = append(out, a.WithZone("").String())
	}
	return out
}

// verifyResolver fails when /etc/resolv.conf names only link-local
// resolvers. Naming accounts in host.firewall.link_local_accounts is a way
// out for those accounts, so it is a warning then: the agent cannot be
// named, and still resolves nothing.
func verifyResolver(e *engine.Env) []engine.Finding {
	servers := linkLocalResolvers(e.X)
	if len(servers) == 0 {
		return nil
	}
	named := e.Cfg.Host.Firewall.LinkLocalAccounts
	who := "root"
	if len(named) > 0 {
		who = "root and " + strings.Join(named, ", ")
	}
	detail := resolvConfPath + " names only the link-local " + verb(len(servers), "resolver", "resolvers") + " " + strings.Join(servers, ", ") +
		", which the network limits leave to " + who + ": " + e.Cfg.Users.Agent.Name + " and every other account resolve no names, apt's downloads (account _apt) among them. " +
		"Either name the accounts that must resolve in " + linkLocalAccountsKey + " (never the agent) and keelbox apply " + e.Cfg.Box +
		", or use systemd-resolved's stub, which every account reaches: " + stubFix
	if len(named) > 0 {
		return []engine.Finding{warn("host", "resolver", detail)}
	}
	return []engine.Finding{fail("host", "resolver", detail)}
}

// netLimitsImpact says what the network limits take from a machine in use,
// before the apply that first loads them for every account: the rule
// itself, what is connected to a link-local address now under an account
// that would lose it, and a resolver only root would still reach.
func netLimitsImpact(ctx context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	if _, pending := in.Pending("host", "agent-net-rules"); !pending {
		return nil
	}
	if out, err := system.Output(ctx, e.X, system.C("nft", "list", "table", "inet", "keelbox_agent")); err == nil && readLoadedNet(e.X, out).everyAccount() {
		return nil // loaded already: this apply only changes a rule of it
	}
	c, inv := e.Cfg, in.Inv
	keepHint := linkLocalAccountsKey + ": [NAME] keeps link-local addresses for an account"
	var out []engine.ImpactItem

	if servers := linkLocalResolvers(e.X); len(servers) > 0 {
		out = append(out, engine.ImpactItem{Group: engine.ImpactCuts, Subject: "dns", Gate: true,
			Detail: resolvConfPath + " names only the link-local " + verb(len(servers), "resolver", "resolvers") + " " + strings.Join(servers, ", ") +
				"; keelbox refuses link-local addresses to every account but root, so " + c.Users.Agent.Name + ", apt's downloads and every other account would resolve no names",
			Hint: "first use systemd-resolved's stub: " + stubFix})
	}

	seen := map[string]bool{}
	for _, p := range inv.LinkLocal {
		if p.UID == 0 || p.Account == c.Users.Agent.Name || builtInLinkLocal(p) {
			continue
		}
		who := p.Account
		if who == "" {
			who = "uid " + strconv.Itoa(p.UID)
		}
		subject := firstNonEmptyString(p.Process, who)
		if seen[subject+" "+who] {
			continue
		}
		seen[subject+" "+who] = true
		if p.Account != "" && contains(c.Host.Firewall.LinkLocalAccounts, p.Account) {
			out = append(out, engine.ImpactItem{Group: engine.ImpactKeeps, Subject: subject,
				Detail: "runs as " + who + " and is connected to " + p.Peer + " now; " + who + " keeps link-local addresses (" + linkLocalAccountsKey + ")"})
			continue
		}
		it := engine.ImpactItem{Group: engine.ImpactCuts, Subject: subject,
			Detail: "runs as " + who + " and is connected to the link-local address " + p.Peer + " now; keelbox refuses those addresses to every account but root"}
		if p.Account != "" && p.Account != "root" {
			it.Hint = linkLocalAccountsKey + ": [" + p.Account + "] keeps them for it"
		}
		out = append(out, it)
	}

	if len(out) > 0 || machineInUse(e, inv) {
		out = append(out, engine.ImpactItem{Group: engine.ImpactChanges, Subject: "network", Hint: keepHint,
			Detail: "accounts other than root can no longer connect to link-local addresses (169.254.0.0/16 and fe80::/10, a cloud's metadata service among them) " +
				"or to this machine's own SSH port: a metadata query or ssh localhost needs sudo. DNS, time and DHCP through Ubuntu's own services are not affected"})
	}
	return out
}

// builtInLinkLocal reports whether a connection is one the rules let
// through for every box: an account of linkLocalAccounts on its own port.
func builtInLinkLocal(p inventory.LinkLocalPeer) bool {
	for _, a := range linkLocalAccounts {
		if a.account == p.Account && a.proto == p.Proto && int(a.port) == p.Port {
			return true
		}
	}
	return false
}

// machineInUse reports whether the inventory found anything of an owner's
// on the machine: a service on the network, Docker, a tmux server or an
// agent CLI, or a login account that is not the image's own. A server
// created a minute ago has none, and the report says nothing to it about
// rules that take nothing from it.
func machineInUse(e *engine.Env, inv *inventory.Inventory) bool {
	if len(inv.Listeners) > 0 || inv.Docker.Running || len(inv.Running) > 0 {
		return true
	}
	image := CloudUsers(e)
	for _, a := range inv.Accounts {
		if a.UID != 0 && !contains(image, a.Name) {
			return true
		}
	}
	return false
}

func firstNonEmptyString(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
