package provider

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// EnvSSHAddrs carries, to keelbox on the box, the addresses and ports at
// which this machine reaches the box's sshd: "203.0.113.7:22,[2001:db8::7]:22".
// A box behind address translation (a cloud's public address, a router's
// forwarded port) never sees that address on an interface or in
// $SSH_CONNECTION, so it has to be told where it is reached from outside to
// refuse the agent's own connections there
// (docs/adr/0069-agent-ssh-to-outside-addresses.md).
const EnvSSHAddrs = "KEELBOX_SSH_ADDRS"

// maxSSHAddrs bounds what one run reports: a name with many addresses.
const maxSSHAddrs = 8

// Addr is a host, by name or address, and the port of its sshd.
type Addr struct {
	Host string
	Port int
}

// sshAddrs is EnvSSHAddrs' value for this target: the address this run
// connects to, then the box's other known ones (s.Outside). It is "" when
// none can be found; the run goes ahead either way.
func (s *SSH) sshAddrs(ctx context.Context) string {
	return sshAddrs(ctx, append([]Addr{{s.Host, s.Port}}, s.Outside...), s.sshHostname, net.DefaultResolver.LookupNetIP)
}

// sshAddrs finds the addresses ssh connects to for each target. An address
// is used as it is. A name goes through ssh's own configuration first,
// since target.ssh.host may be an alias from ~/.ssh/config, and is then
// resolved here, on the machine that connects: the box's resolver can
// answer differently for its own name (a cloud's public host name resolves
// to the private address from inside its network).
func sshAddrs(ctx context.Context, targets []Addr, sshHostname func(ctx context.Context, host string, port int) string,
	lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)) string {
	var out []string
	seen := map[netip.AddrPort]bool{}
	for _, t := range targets {
		if t.Host == "" || t.Port < 1 || t.Port > 65535 {
			continue
		}
		for _, a := range hostAddrs(ctx, t, sshHostname, lookup) {
			ap := netip.AddrPortFrom(a.Unmap(), uint16(t.Port))
			// A scoped address is link-local: it means nothing on the box.
			if !a.IsValid() || a.Zone() != "" || seen[ap] {
				continue
			}
			if len(out) == maxSSHAddrs {
				return strings.Join(out, ",")
			}
			seen[ap] = true
			out = append(out, ap.String())
		}
	}
	return strings.Join(out, ",")
}

func hostAddrs(ctx context.Context, t Addr, sshHostname func(ctx context.Context, host string, port int) string,
	lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)) []netip.Addr {
	if a, err := netip.ParseAddr(t.Host); err == nil {
		return []netip.Addr{a}
	}
	name := t.Host
	if h := sshHostname(ctx, t.Host, t.Port); h != "" {
		name = h
	}
	if a, err := netip.ParseAddr(name); err == nil {
		return []netip.Addr{a}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, _ := lookup(ctx, "ip", name)
	return addrs
}

// sshHostname asks ssh which host name it connects to for host once its
// configuration is applied (ssh -G), or "" when ssh does not say.
func (s *SSH) sshHostname(ctx context.Context, host string, port int) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append(s.baseOpts(), "-G", "-p", strconv.Itoa(port), s.User+"@"+host)
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return ""
	}
	return sshConfigHostname(out.String())
}

// sshConfigHostname reads the hostname line of ssh -G's output.
func sshConfigHostname(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), " "); ok && k == "hostname" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
