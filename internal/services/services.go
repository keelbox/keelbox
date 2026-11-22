// Package services is keelbox's table of home-server services it knows by
// name: the ports each listens on, from the vendor's own documentation,
// and how to recognise it on a machine (the process, the systemd unit or
// the container image). host.firewall.allow takes {service: unifi} for the
// whole list, and keelbox up and plan --impact speak of "UniFi" rather
// than of eight ports (docs/adr/0046-allow-ranges-and-services.md).
package services

import (
	"sort"
	"strconv"
	"strings"
)

// Port is one port, or a range of ports, a service listens on.
type Port struct {
	First, Last int
	Proto       string // tcp or udp
	Use         string // what it is for, from the vendor's docs
	// Discovery marks a port devices or apps find the service by, with a
	// broadcast or multicast on the LAN rather than a connection to a
	// known address.
	Discovery bool
}

// Service is one known service.
type Service struct {
	Name   string // the preset: host.firewall.allow {service: unifi}
	Title  string // what to call it: "UniFi Network Application"
	Ports  []Port
	Source string // the vendor page the ports come from
	// Left out: ports the vendor lists that the preset does not open, and
	// why, for the docs.
	Left string
	// Recognised by: the process name as ss shows it (15 characters at
	// most), the systemd unit ("name@" matches every instance of a
	// template), the last part of a container image's repository.
	Processes, Units, Images []string
}

func p(port int, proto, use string) Port {
	return Port{First: port, Last: port, Proto: proto, Use: use}
}

func discovery(port int, use string) Port {
	return Port{First: port, Last: port, Proto: "udp", Use: use, Discovery: true}
}

var all = []Service{
	{
		Name: "unifi", Title: "UniFi Network Application",
		Source: "https://help.ui.com/hc/en-us/articles/218506997",
		Ports: []Port{
			p(8080, "tcp", "device and application communication (devices inform here)"),
			p(8443, "tcp", "application GUI and API"),
			p(3478, "udp", "STUN, for device adoption and communication"),
			discovery(10001, "device discovery during adoption"),
			discovery(1900, `L2 discovery ("Make application discoverable on L2 network")`),
			p(6789, "tcp", "UniFi mobile speed test"),
			{First: 8880, Last: 8882, Proto: "tcp", Use: "hotspot portal redirection (HTTP)"},
			p(8843, "tcp", "hotspot portal redirection (HTTPS)"),
		},
		Left:   "27117/tcp (the local database: loopback only), 5514/udp (remote syslog), 10101/udp (client fingerprinting), 5671/tcp (UXG traffic flow logs), 8444/tcp (secure hotspot portal), 28082/tcp (packet capture), 20100-22100/udp (Site Magic)",
		Units:  []string{"unifi.service"},
		Images: []string{"unifi", "unifi-network-application", "unifi-controller"},
	},
	{
		Name: "jellyfin", Title: "Jellyfin",
		Source: "https://jellyfin.org/docs/general/post-install/networking/",
		Ports: []Port{
			p(8096, "tcp", "web interface and apps (HTTP)"),
			p(8920, "tcp", "HTTPS, when enabled"),
			discovery(7359, "client discovery on the local network"),
		},
		Left:      "1900/udp (DLNA, a plugin since Jellyfin 10.9: add {port: 1900, proto: udp} if you use it)",
		Processes: []string{"jellyfin"},
		Units:     []string{"jellyfin.service"},
		Images:    []string{"jellyfin"},
	},
	{
		Name: "plex", Title: "Plex Media Server",
		Source: "https://support.plex.tv/articles/201543147-what-network-ports-do-i-need-to-allow-through-my-firewall/",
		Ports: []Port{
			p(32400, "tcp", "access to the Plex Media Server (required)"),
			discovery(1900, "the Plex DLNA Server"),
			discovery(5353, "older Bonjour/Avahi network discovery"),
			p(8324, "tcp", "controlling Plex for Roku via Plex Companion"),
			discovery(32410, "GDM network discovery"),
			{First: 32412, Last: 32414, Proto: "udp", Use: "GDM network discovery", Discovery: true},
			p(32469, "tcp", "the Plex DLNA Server"),
		},
		Processes: []string{"Plex Media Serv", "Plex DLNA Serve"},
		Units:     []string{"plexmediaserver.service"},
		Images:    []string{"plex", "pms-docker"},
	},
	{
		Name: "home-assistant", Title: "Home Assistant",
		Source: "https://www.home-assistant.io/integrations/http/",
		Ports:  []Port{p(8123, "tcp", "web interface and apps (the default for Home Assistant Container and Core)")},
		Left:   "21063/tcp and up (HomeKit Bridge, when set up: add its port); mDNS (5353/udp multicast) UFW admits already",
		// Core runs as hass, or as python3 -m homeassistant, which only
		// the unit or image tells apart.
		Processes: []string{"hass"},
		Units:     []string{"home-assistant@", "homeassistant.service", "home-assistant.service"},
		Images:    []string{"home-assistant", "homeassistant"},
	},
	{
		Name: "portainer", Title: "Portainer",
		Source: "https://docs.portainer.io/start/requirements-and-prerequisites",
		Ports: []Port{
			p(9443, "tcp", "the UI and API"),
			p(8000, "tcp", "the TCP tunnel server for Edge Agents (only with Edge Compute)"),
		},
		Left:      "9000/tcp (the UI over plain HTTP in older installs: add {port: 9000} if you use it)",
		Processes: []string{"portainer"},
		Images:    []string{"portainer", "portainer-ce", "portainer-ee"},
	},
	{
		Name: "samba", Title: "Samba file sharing",
		Source: "https://manpages.ubuntu.com/manpages/noble/man5/smb.conf.5.html (smb ports = 445 139), nmbd(8)",
		Ports: []Port{
			p(445, "tcp", "SMB over TCP"),
			p(139, "tcp", "NetBIOS session (SMB for older clients)"),
			discovery(137, "NetBIOS name service (nmbd)"),
			discovery(138, "NetBIOS datagrams and browsing (nmbd)"),
		},
		Left:      "3702/udp and 5357/tcp (WS-Discovery, from the separate wsdd package, so Windows lists the share)",
		Processes: []string{"smbd", "nmbd"},
		Units:     []string{"smbd.service", "nmbd.service", "samba.service"},
		Images:    []string{"samba"},
	},
	{
		Name: "syncthing", Title: "Syncthing",
		Source: "https://docs.syncthing.net/users/firewall.html",
		Ports: []Port{
			p(22000, "tcp", "TCP based sync protocol traffic"),
			p(22000, "udp", "QUIC based sync protocol traffic"),
			discovery(21027, "local discovery broadcasts (IPv4) and multicasts (IPv6)"),
		},
		Left:      "8384/tcp (the web GUI, on 127.0.0.1 by default; the tailnet reaches it if you move it)",
		Processes: []string{"syncthing"},
		Units:     []string{"syncthing@", "syncthing.service"},
		Images:    []string{"syncthing"},
	},
	{
		Name: "pihole", Title: "Pi-hole",
		Source: "https://docs.pi-hole.net/main/prerequisites/",
		Ports: []Port{
			p(53, "tcp", "DNS"),
			p(53, "udp", "DNS"),
			p(80, "tcp", "web interface (HTTP)"),
			p(443, "tcp", "web interface (HTTPS)"),
		},
		Left:      "67/udp and 547/udp (its DHCP server: requests come from 0.0.0.0, which no LAN rule matches), 123/udp (NTP server, optional)",
		Processes: []string{"pihole-FTL"},
		Units:     []string{"pihole-FTL.service"},
		Images:    []string{"pihole"},
	},
	{
		Name: "adguard", Title: "AdGuard Home",
		Source: "https://github.com/AdguardTeam/AdGuardHome/wiki/Docker",
		Ports: []Port{
			p(53, "tcp", "plain DNS"),
			p(53, "udp", "plain DNS"),
			p(80, "tcp", "admin panel"),
			p(443, "tcp", "HTTPS and DNS-over-HTTPS"),
			p(443, "udp", "DNS-over-HTTPS over HTTP/3"),
			p(853, "tcp", "DNS-over-TLS"),
			p(853, "udp", "DNS-over-QUIC"),
			p(3000, "tcp", "admin web interface during setup"),
		},
		Left:      "67-68/udp (its DHCP server: requests come from 0.0.0.0, which no LAN rule matches), 5443 (DNSCrypt), 6060/tcp (debug profiles)",
		Processes: []string{"AdGuardHome"},
		Units:     []string{"AdGuardHome.service"},
		Images:    []string{"adguardhome"},
	},
}

// All lists the services, by name.
func All() []Service {
	out := append([]Service(nil), all...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names lists the preset names, sorted.
func Names() []string {
	var out []string
	for _, s := range All() {
		out = append(out, s.Name)
	}
	return out
}

// Lookup returns the named service.
func Lookup(name string) (Service, bool) {
	for _, s := range all {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// Has reports whether the service listens on port/proto.
func (s Service) Has(port int, proto string) bool {
	for _, p := range s.Ports {
		if p.Proto == proto && port >= p.First && port <= p.Last {
			return true
		}
	}
	return false
}

// Key is "8080/tcp" or "8880-8882/tcp".
func (p Port) Key() string {
	if p.Last != p.First {
		return strconv.Itoa(p.First) + "-" + strconv.Itoa(p.Last) + "/" + p.Proto
	}
	return strconv.Itoa(p.First) + "/" + p.Proto
}

// Summary lists the ports for a sentence: "8080, 8443/tcp; 3478, 10001/udp".
func (s Service) Summary() string {
	var tcp, udp []string
	for _, p := range s.Ports {
		k := strings.TrimSuffix(p.Key(), "/"+p.Proto)
		if p.Proto == "udp" {
			udp = append(udp, k)
		} else {
			tcp = append(tcp, k)
		}
	}
	var parts []string
	if len(tcp) > 0 {
		parts = append(parts, strings.Join(tcp, ", ")+"/tcp")
	}
	if len(udp) > 0 {
		parts = append(parts, strings.Join(udp, ", ")+"/udp")
	}
	return strings.Join(parts, "; ")
}

// DiscoveryNote says which ports are for discovery, for a question or a
// report: "10001, 1900/udp are how devices and apps find it on the LAN".
func (s Service) DiscoveryNote() string {
	d := Service{}
	for _, p := range s.Ports {
		if p.Discovery {
			d.Ports = append(d.Ports, p)
		}
	}
	switch len(d.Ports) {
	case 0:
		return ""
	case 1:
		return d.Summary() + " is how devices and apps find it on the LAN"
	}
	return d.Summary() + " are how devices and apps find it on the LAN"
}

// Match finds the service a listener belongs to, from what the machine
// says about it: its process name, its systemd unit, or the image of the
// container it runs in. Any one is enough; "" is unknown.
func Match(process, unit, image string) (Service, bool) {
	repo := repository(image)
	for _, s := range all {
		for _, p := range s.Processes {
			if process != "" && process == p {
				return s, true
			}
		}
		for _, u := range s.Units {
			if unit == "" {
				continue
			}
			if u == unit || (strings.HasSuffix(u, "@") && strings.HasPrefix(unit, u)) {
				return s, true
			}
		}
		for _, i := range s.Images {
			if repo != "" && repo == i {
				return s, true
			}
		}
	}
	return Service{}, false
}

// repository is the last part of an image's repository, without registry,
// tag or digest: lscr.io/linuxserver/jellyfin:10.10 is jellyfin.
func repository(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	if i := strings.Index(image, ":"); i >= 0 {
		image = image[:i]
	}
	return strings.ToLower(image)
}
