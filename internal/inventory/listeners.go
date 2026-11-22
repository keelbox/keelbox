package inventory

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/services"
)

// Listener is one service listening beyond loopback: the firewall's
// default deny would block it from the network.
type Listener struct {
	Port      int    `json:"port"`
	Proto     string `json:"proto"`             // tcp or udp
	Process   string `json:"process,omitempty"` // as ss names it, or ""
	Container string `json:"container,omitempty"`
	// HostNetwork marks a container on the host network: its socket is
	// the host's, so UFW filters it, not the Docker guard.
	HostNetwork bool `json:"host_network,omitempty"`
	PID         int  `json:"pid,omitempty"`
	// UID owns the process, as root reads /proc/PID/status; -1 when not
	// known. Only a process of root's or a system account's is recognised
	// by its name (recognise).
	UID   int    `json:"uid,omitempty"`
	Unit  string `json:"unit,omitempty"`  // the systemd service it runs in
	Image string `json:"image,omitempty"` // its container's image
	// Service is the known service it is (internal/services), or "".
	Service string `json:"service,omitempty"`
	Label   string `json:"label"` // what to call it: "Samba (smbd)", "container jellyfin (Docker)"
	Name    string `json:"name"`  // a host.firewall.allow name: "samba", "jellyfin"
}

// Published reports whether Docker publishes it, which the Docker guard
// filters rather than UFW.
func (l Listener) Published() bool {
	return (l.Container != "" && !l.HostNetwork) || l.Process == "docker-proxy"
}

// Key is "8080/tcp".
func (l Listener) Key() string { return portKey(l.Port, l.Proto) }

func portKey(port int, proto string) string { return fmt.Sprintf("%d/%s", port, proto) }

var processRe = regexp.MustCompile(`users:\(\("([^"]+)"(?:,pid=(\d+))?`)

// ParseListeners reads ss -Hltun(p) lines and keeps the services the
// firewall would block: bound beyond loopback, link-local and the tailnet,
// and not SSH or a port UFW already admits (DHCP, mDNS) or Tailscale's own.
// containers maps "8080/tcp" to the system Docker container publishing it.
func ParseListeners(lines []string, containers map[string]string) []Listener {
	seen := map[string]bool{}
	var out []Listener
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 5 || (f[0] != "tcp" && f[0] != "udp") {
			continue
		}
		host, port, ok := splitHostPort(f[4])
		if !ok || !blockable(host) {
			continue
		}
		p, err := strconv.Atoi(port)
		if err != nil || p == 22 || ignoredPort(f[0], p) {
			continue
		}
		ln := Listener{Port: p, Proto: f[0], Container: containers[portKey(p, f[0])]}
		if m := processRe.FindStringSubmatch(l); m != nil {
			ln.Process = m[1]
			ln.PID = atoi(m[2])
		}
		if seen[ln.Key()] {
			continue
		}
		seen[ln.Key()] = true
		if s, ok := services.Match(ln.Process, "", ""); ok {
			ln.Service = s.Name
		}
		ln.Label, ln.Name = describeService(ln)
		out = append(out, ln)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// splitHostPort reads ss's local address: 0.0.0.0:80, [::]:80, *:80,
// 127.0.0.53%lo:53, [fe80::1%eth0]:546.
func splitHostPort(a string) (string, string, bool) {
	i := strings.LastIndex(a, ":")
	if i < 0 {
		return "", "", false
	}
	host, port := a[:i], a[i+1:]
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if j := strings.Index(host, "%"); j >= 0 {
		host = host[:j]
	}
	return host, port, true
}

var (
	tailnet4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnet6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Reachable reports whether a socket bound to host can be reached from
// another machine, firewall aside: not loopback, link-local or the tailnet.
func Reachable(host string) bool { return blockable(host) }

func blockable(host string) bool {
	switch host {
	case "*", "0.0.0.0", "::", "":
		return true
	case "localhost":
		return false
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsMulticast() && !tailnet4.Contains(a) && !tailnet6.Contains(a)
}

// IsTailnet reports whether a is a Tailscale address.
func IsTailnet(a netip.Addr) bool { a = a.Unmap(); return tailnet4.Contains(a) || tailnet6.Contains(a) }

// ignoredPort: UFW's own rules admit DHCP replies and mDNS, and Tailscale
// opens its port itself.
func ignoredPort(proto string, p int) bool {
	if proto != "udp" {
		return false
	}
	switch p {
	case 67, 68, 546, 547, 5353, 41641:
		return true
	}
	return false
}

// knownServices names common home-server processes.
var knownServices = map[string][2]string{
	"Plex Media Serv": {"Plex", "plex"}, "Plex DLNA Serve": {"Plex DLNA", "plex-dlna"},
	"smbd": {"Samba", "samba"}, "nmbd": {"Samba name service", "samba-nmb"},
	"jellyfin": {"Jellyfin", "jellyfin"}, "cupsd": {"printing (CUPS)", "cups"},
	"minidlnad": {"MiniDLNA", "dlna"}, "syncthing": {"Syncthing", "syncthing"},
	"transmission-da": {"Transmission", "transmission"}, "rpcbind": {"NFS (rpcbind)", "nfs-rpcbind"},
	"rpc.mountd": {"NFS mountd", "nfs-mountd"}, "nginx": {"nginx", "nginx"}, "apache2": {"Apache", "apache"},
	"caddy": {"Caddy", "caddy"}, "cockpit-ws": {"Cockpit", "cockpit"}, "named": {"DNS (BIND)", "dns"},
	"dnsmasq": {"dnsmasq", "dnsmasq"}, "pihole-FTL": {"Pi-hole", "pihole"}, "xrdp": {"remote desktop (xrdp)", "xrdp"},
	"gnome-remote-de": {"GNOME remote desktop", "rdp"}, "kdeconnectd": {"KDE Connect", "kdeconnect"},
	"postgres": {"PostgreSQL", "postgres"}, "mysqld": {"MySQL", "mysql"}, "mariadbd": {"MariaDB", "mariadb"},
	"redis-server": {"Redis", "redis"}, "node": {"a Node app", "node"}, "python3": {"a Python app", "python"},
}

// describeService labels a listener for the user and names its rule.
func describeService(l Listener) (label, name string) {
	if s, ok := services.Lookup(l.Service); ok && l.Service != "" {
		switch {
		case l.HostNetwork:
			return s.Title + " (container " + l.Container + ", host network)", s.Name
		case l.Container != "":
			return s.Title + " (container " + l.Container + ")", s.Name
		case l.Unit != "" && !strings.EqualFold(l.Process, s.Name):
			return s.Title + " (" + l.Unit + ")", s.Name
		case l.Process == "" || strings.HasPrefix(strings.ToLower(s.Title), strings.ToLower(l.Process)):
			return s.Title, s.Name
		}
		return s.Title + " (" + l.Process + ")", s.Name
	}
	if l.UID >= 1000 && l.Process != "" {
		// Another account's program: named as what it is, not as a
		// service, with the account that runs it.
		return fmt.Sprintf("%s (run by UID %d, not a system service)", l.Process, l.UID), AllowName(l.Process, l.Port)
	}
	switch {
	case l.HostNetwork:
		return "container " + l.Container + " (Docker, host network)", AllowName(l.Container, l.Port)
	case l.Container != "":
		return "container " + l.Container + " (Docker)", AllowName(l.Container, l.Port)
	case l.Process == "docker-proxy":
		return "a Docker container", AllowName("docker", l.Port)
	case l.Process != "":
		p := l.Process
		switch {
		case strings.HasPrefix(p, "node"):
			p = "node" // node-MainThread, the thread that listens
		case strings.HasPrefix(p, "python3"):
			p = "python3"
		}
		if k, ok := knownServices[p]; ok {
			if strings.EqualFold(k[0], l.Process) {
				return k[0], k[1]
			}
			return k[0] + " (" + l.Process + ")", k[1]
		}
		return l.Process, AllowName(l.Process, l.Port)
	}
	return "unknown (keelbox could not see the process)", AllowName("", l.Port)
}

var notName = regexp.MustCompile(`[^a-z0-9-]+`)

// AllowName makes a host.firewall.allow name from s: lowercase letters,
// digits and hyphens, at most 32; port-N when nothing is left.
func AllowName(s string, port int) string {
	s = strings.Trim(notName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if s == "" {
		return fmt.Sprintf("port-%d", port)
	}
	return s
}
