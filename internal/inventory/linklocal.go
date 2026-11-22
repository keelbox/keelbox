package inventory

import (
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LinkLocalPeer is a socket connected to a link-local address when the
// inventory looked: a cloud's metadata service, resolver or time service,
// or a neighbour on the link. keelbox's network limits refuse those
// addresses to every account but root, so the impact report names what
// talks to one now.
//
// It is one look at the socket table. A service that asks the metadata
// service once a minute and closes the connection is seen only if the look
// falls on a request; an empty list does not mean nothing on the machine
// uses a link-local address.
type LinkLocalPeer struct {
	Process string `json:"process,omitempty"` // as ss names it, or ""
	// UID owns the socket, as ss -e reports it; 0 is root.
	UID     int    `json:"uid"`
	Account string `json:"account,omitempty"` // the uid's name in passwd, or ""
	Proto   string `json:"proto"`             // tcp or udp
	Peer    string `json:"peer"`              // "169.254.169.254:80", "[fe80::1]:53"
	Port    int    `json:"port"`
}

var socketUIDRe = regexp.MustCompile(`\buid:(\d+)`)

// ParseLinkLocalPeers reads ss -Htunpe lines (connected TCP and UDP
// sockets, with the process and the socket's owner) and keeps those whose
// peer is link-local, once for each process, owner, protocol and peer.
// ss leaves "uid:" out for a socket of root's. passwd is getent passwd,
// for the owner's name.
func ParseLinkLocalPeers(lines []string, passwd string) []LinkLocalPeer {
	names := map[int]string{}
	for _, l := range strings.Split(passwd, "\n") {
		f := strings.Split(l, ":")
		if len(f) < 3 {
			continue
		}
		if uid, err := strconv.Atoi(f[2]); err == nil {
			if _, dup := names[uid]; !dup {
				names[uid] = f[0]
			}
		}
	}
	seen := map[string]bool{}
	var out []LinkLocalPeer
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 6 || (f[0] != "tcp" && f[0] != "udp") {
			continue
		}
		host, port, ok := splitHostPort(f[5])
		if !ok {
			continue
		}
		a, err := netip.ParseAddr(strings.Trim(host, "[]"))
		if err != nil || !a.Unmap().IsLinkLocalUnicast() {
			continue
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			continue
		}
		peer := LinkLocalPeer{Proto: f[0], Port: p, Peer: netip.AddrPortFrom(a.Unmap(), uint16(p)).String()} //nolint:gosec // G115: a port ss printed
		if m := processRe.FindStringSubmatch(l); m != nil {
			peer.Process = m[1]
		}
		if m := socketUIDRe.FindStringSubmatch(l); m != nil {
			peer.UID = atoi(m[1])
		}
		peer.Account = names[peer.UID]
		key := peer.Process + " " + strconv.Itoa(peer.UID) + " " + peer.Proto + " " + peer.Peer
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, peer)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UID != out[j].UID {
			return out[i].UID < out[j].UID
		}
		return out[i].Peer < out[j].Peer
	})
	return out
}
