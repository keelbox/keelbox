package modules

import (
	"context"
	"net/netip"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Outside addresses are where the box's sshd is reached from outside at an
// address no interface of the box carries: a cloud's public address that is
// translated before it arrives (AWS, Google Cloud), a router's forwarded
// port. The agent's rule against SSH to the box matches local addresses, so
// a connection to such an address would leave the box and come back to
// sshd. The box cannot find these addresses itself; the laptop tells it
// where it reaches the box (engine.Env.SSHAddrs), and the box keeps what is
// not local (docs/adr/0069-agent-ssh-to-outside-addresses.md).
const (
	outsideAddrsPath = "/etc/keelbox/outside-addresses"
	// outsideAddrsCap bounds the record, and so the rules: past it, the
	// address recorded longest ago goes. An address the laptop still
	// reaches the box at is recorded again by its next apply.
	outsideAddrsCap = 16
	// outsideAddrsPerRun bounds what one run adds.
	outsideAddrsPerRun = 8
)

const outsideAddrsHeader = `# Managed by keelbox. Where this box's sshd is reached from outside, at
# addresses that are not on its interfaces: a cloud's public address, a
# router's forwarded port. No account but root may connect to them; each has
# a rule in /etc/keelbox/agent-net.nft.
#
# An apply over SSH adds where the laptop reaches the box. keelbox keeps the
# 16 added last. One per line, as ADDRESS:PORT or [IPV6-ADDRESS]:PORT. Delete
# a line the box is no longer reached at, or add one keelbox has not been
# told about, then run keelbox apply. Other lines are not kept.
`

// outsideAddr reads one "address:port", and refuses what cannot be a way to
// the box from outside. The value ends up in an nftables rule: it is
// written from the parsed address, never from the text.
func outsideAddr(s string) (netip.AddrPort, bool) {
	ap, err := netip.ParseAddrPort(strings.TrimSpace(s))
	if err != nil {
		return netip.AddrPort{}, false
	}
	a := ap.Addr().Unmap()
	if ap.Port() == 0 || a.Zone() != "" || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast() {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(a, ap.Port()), true
}

// parseOutsideAddrs reads sep-separated addresses, in order and without
// repeats, skipping comments and anything that is not one.
func parseOutsideAddrs(s, sep string) []netip.AddrPort {
	var out []netip.AddrPort
	seen := map[netip.AddrPort]bool{}
	for _, f := range strings.Split(s, sep) {
		if a, ok := outsideAddr(f); ok && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// parseSSHAddrs reads engine.Env.SSHAddrs.
func parseSSHAddrs(s string) []netip.AddrPort {
	out := parseOutsideAddrs(s, ",")
	if len(out) > outsideAddrsPerRun {
		out = out[:outsideAddrsPerRun]
	}
	return out
}

// mergeOutsideAddrs adds the new addresses after the recorded ones and
// keeps the last outsideAddrsCap. It returns what was added and dropped.
func mergeOutsideAddrs(recorded, told []netip.AddrPort) (want, added, dropped []netip.AddrPort) {
	seen := map[netip.AddrPort]bool{}
	for _, a := range recorded {
		if !seen[a] {
			seen[a] = true
			want = append(want, a)
		}
	}
	for _, a := range told {
		if !seen[a] {
			seen[a] = true
			want, added = append(want, a), append(added, a)
		}
	}
	if n := len(want) - outsideAddrsCap; n > 0 {
		dropped, want = want[:n:n], want[n:]
	}
	return want, added, dropped
}

func renderOutsideAddrs(addrs []netip.AddrPort) string {
	var b strings.Builder
	b.WriteString(outsideAddrsHeader)
	for _, a := range addrs {
		b.WriteString(a.String() + "\n")
	}
	return b.String()
}

// readOutsideAddrs reads the record. A box without one has none.
func readOutsideAddrs(x system.FS) ([]netip.AddrPort, error) {
	st, err := x.Stat(outsideAddrsPath)
	if err != nil || !st.Exists || st.IsDir || st.IsSymlink {
		return nil, err
	}
	data, err := x.ReadFile(outsideAddrsPath)
	if err != nil {
		return nil, err
	}
	return parseOutsideAddrs(string(data), "\n"), nil
}

// isLocalAddr asks the kernel's routing table what nftables' fib lookup
// asks: is this one of the box's own addresses. Those the table's first
// rule already covers.
func isLocalAddr(ctx context.Context, e *engine.Env, a netip.Addr) bool {
	out, err := system.Output(ctx, e.X, system.C("ip", "-o", "route", "get", a.String()))
	f := strings.Fields(out)
	return err == nil && len(f) > 0 && f[0] == "local"
}

// outsideAddrsPlan is what this run records: the addresses on record plus
// those the laptop reaches the box at that are not the box's own.
type outsideAddrsPlan struct {
	want, added, dropped []netip.AddrPort
}

func wantOutsideAddrs(ctx context.Context, e *engine.Env) (outsideAddrsPlan, error) {
	recorded, err := readOutsideAddrs(e.X)
	if err != nil {
		return outsideAddrsPlan{}, err
	}
	var told []netip.AddrPort
	for _, a := range parseSSHAddrs(e.SSHAddrs) {
		if !isLocalAddr(ctx, e, a.Addr()) {
			told = append(told, a)
		}
	}
	var p outsideAddrsPlan
	p.want, p.added, p.dropped = mergeOutsideAddrs(recorded, told)
	return p, nil
}

func joinAddrs(addrs []netip.AddrPort) string {
	var l []string
	for _, a := range addrs {
		l = append(l, a.String())
	}
	return strings.Join(l, ", ")
}

// outsideAddrsStep keeps the record. It only grows by what the laptop
// tells it, so an apply on the box itself keeps what an earlier one learnt.
func outsideAddrsStep() engine.Step {
	const name = "agent-net-addresses"
	file := func(p outsideAddrsPlan) engine.Step {
		return engine.FileStep(name, engine.File{Path: outsideAddrsPath, Content: []byte(renderOutsideAddrs(p.want)), Mode: 0o644})
	}
	return engine.Step{
		Name: name,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			p, err := wantOutsideAddrs(ctx, e)
			if err != nil {
				return engine.Status{}, err
			}
			if len(p.added) == 0 {
				return file(p).Check(ctx, e)
			}
			detail := "record " + joinAddrs(p.added) + ": keelbox reached this box there, and it is not an address of the box"
			if len(p.dropped) > 0 {
				detail += "; forget " + joinAddrs(p.dropped) + ", recorded longest ago"
			}
			return engine.Pending("%s", detail), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			p, err := wantOutsideAddrs(ctx, e)
			if err != nil {
				return err
			}
			return file(p).Apply(ctx, e)
		},
	}
}
