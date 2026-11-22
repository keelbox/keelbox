package config

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/keelbox/keelbox/internal/services"
)

// Allow keeps a port, a range of ports or a known service reachable from
// the LAN (or a network named by CIDR) through the firewall keelbox turns
// on: a home server's Plex or Samba, say. The tailnet already reaches every
// port. An entry names either a port (with proto) or a service.
type Allow struct {
	// Name labels the rule (in UFW's comment), such as plex.
	Name string `yaml:"name,omitempty"`
	// Service is a preset from internal/services, such as unifi: every
	// port the vendor lists for it, TCP and UDP.
	Service string `yaml:"service,omitempty"`
	// Port is one port (32400) or a range (8000-8010).
	Port PortRange `yaml:"port,omitempty"`
	// Proto is tcp (default) or udp.
	Proto string `yaml:"proto,omitempty"`
	// From is lan (default: the private address ranges) or a CIDR such as
	// 192.168.1.0/24.
	From string `yaml:"from,omitempty"`
}

// PortRange is one port (First equals Last) or a range of ports, written
// port: 32400 or port: 8000-8010 in the config.
type PortRange struct{ First, Last int }

// Port is the range holding one port.
func Port(n int) PortRange { return PortRange{n, n} }

// Ports is the range from first to last.
func Ports(first, last int) PortRange { return PortRange{first, last} }

// IsZero reports an unset port (an entry with a service instead).
func (p PortRange) IsZero() bool { return p.First == 0 && p.Last == 0 }

// IsRange reports more than one port.
func (p PortRange) IsRange() bool { return p.Last != p.First }

// Contains reports whether port is in the range.
func (p PortRange) Contains(port int) bool { return port >= p.First && port <= p.Last }

// String is "32400" or "8000-8010".
func (p PortRange) String() string {
	if p.IsRange() {
		return fmt.Sprintf("%d-%d", p.First, p.Last)
	}
	return strconv.Itoa(p.First)
}

// Colon is the range as UFW and iptables write it: "8000:8010".
func (p PortRange) Colon() string { return strings.Replace(p.String(), "-", ":", 1) }

var portRangeRe = regexp.MustCompile(`^(\d{1,5})\s*-\s*(\d{1,5})$`)

// UnmarshalYAML reads 32400 or "8000-8010".
func (p *PortRange) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: port: use a port such as 32400 or a range such as 8000-8010", n.Line)
	}
	if v, err := strconv.Atoi(n.Value); err == nil && n.Tag != "!!str" {
		*p = Port(v)
		return nil
	}
	m := portRangeRe.FindStringSubmatch(strings.TrimSpace(n.Value))
	if m == nil {
		return fmt.Errorf("line %d: port: %q: use a port such as 32400 or a range such as 8000-8010", n.Line, n.Value)
	}
	first, _ := strconv.Atoi(m[1])
	last, _ := strconv.Atoi(m[2])
	*p = Ports(first, last)
	return nil
}

// MarshalYAML writes a port as a number and a range as "8000-8010".
func (p PortRange) MarshalYAML() (any, error) {
	if p.IsRange() {
		return p.String(), nil
	}
	return p.First, nil
}

// MarshalJSON writes a port as a number and a range as "8000-8010".
func (p PortRange) MarshalJSON() ([]byte, error) {
	if p.IsRange() {
		return []byte(strconv.Quote(p.String())), nil
	}
	return []byte(strconv.Itoa(p.First)), nil
}

// UnmarshalJSON reads what MarshalJSON writes.
func (p *PortRange) UnmarshalJSON(b []byte) error {
	if s, err := strconv.Unquote(string(b)); err == nil {
		return p.UnmarshalYAML(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s})
	}
	n, err := strconv.Atoi(string(b))
	if err != nil {
		return fmt.Errorf("port: %s is not a port", b)
	}
	*p = Port(n)
	return nil
}

// LANRanges are what from: lan means: the private IPv4 ranges (RFC 1918)
// and IPv6 unique local addresses. Not the tailnet's 100.64.0.0/10, which
// is open on tailscale0 anyway, and nothing routable on the internet.
var LANRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}

// Sources are the networks the rule admits, as CIDRs.
func (a Allow) Sources() []string {
	if a.From == "" || a.From == "lan" {
		return LANRanges
	}
	if p, err := netip.ParsePrefix(a.From); err == nil {
		return []string{p.Masked().String()}
	}
	return nil
}

// Label is the rule's name, the service, or the port and protocol.
func (a Allow) Label() string {
	switch {
	case a.Name != "":
		return a.Name
	case a.Service != "":
		return a.Service
	}
	return fmt.Sprintf("%s-%s", a.Port, a.Protocol())
}

// Protocol is tcp unless udp is set.
func (a Allow) Protocol() string {
	if a.Proto == "" {
		return "tcp"
	}
	return a.Proto
}

// Key is "8080/tcp" or "8000-8010/tcp".
func (a Allow) Key() string { return a.Port.String() + "/" + a.Protocol() }

// Expand is the entries as one port or range and protocol each: a service
// becomes every port of its preset, named after the entry (or the
// service), from the entry's network. Entries for an unknown service are
// left out (validation reports them).
func Expand(rules []Allow) []Allow {
	var out []Allow
	for _, a := range rules {
		if a.Service == "" {
			out = append(out, a)
			continue
		}
		s, ok := services.Lookup(a.Service)
		if !ok {
			continue
		}
		for _, p := range s.Ports {
			r := Allow{Name: a.Label(), Port: Ports(p.First, p.Last), From: a.From}
			if p.Proto == "udp" {
				r.Proto = "udp"
			}
			out = append(out, r)
		}
	}
	return out
}

// AllowRules is host.firewall.allow with services expanded.
func (c *Config) AllowRules() []Allow { return Expand(c.Host.Firewall.Allow) }

// Allowed is the host.firewall.allow entry that keeps port/proto open, if
// any, as written in the config (a service entry for a service's port).
func (c *Config) Allowed(port int, proto string) (Allow, bool) {
	for _, a := range c.Host.Firewall.Allow {
		for _, r := range Expand([]Allow{a}) {
			if r.Protocol() == proto && r.Port.Contains(port) {
				return a, true
			}
		}
	}
	return Allow{}, false
}

var allowNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func (c *Config) validateFirewall(add func(string, ...any)) {
	ssh := c.Target.SSH.Port
	for i, a := range c.Host.Firewall.Allow {
		key := fmt.Sprintf("host.firewall.allow[%d]", i)
		switch {
		case a.Service != "":
			if _, ok := services.Lookup(a.Service); !ok {
				add("%s.service: %q is not a service keelbox knows; use one of %s, or a port: {port: 8080}", key, a.Service, strings.Join(services.Names(), ", "))
			}
			if !a.Port.IsZero() || a.Proto != "" {
				add("%s: give a service or a port, not both; another port of the service is an entry of its own: {port: %s}", key, firstNonZero(a.Port))
			}
		case a.Port.IsZero():
			add("%s: give a port or a service, for example {port: 32400} or {service: plex}", key)
		default:
			p := a.Port
			switch {
			case p.First < 1 || p.First > 65535 || p.Last < 1 || p.Last > 65535:
				add("%s.port: %s is not a port; use 1 to 65535, for example port: 32400 or port: 8000-8010", key, p)
			case p.First > p.Last:
				add("%s.port: %s: put the lower port first, port: %d-%d", key, p, p.Last, p.First)
			case p.Contains(22) || (ssh != 0 && p.Contains(ssh)):
				what := "is"
				if p.IsRange() {
					what = "includes"
				}
				add("%s.port: %s %s SSH, which host.firewall.public_ssh manages; remove this entry or leave SSH out of the range", key, p, what)
			}
			if pr := a.Proto; pr != "" && pr != "tcp" && pr != "udp" {
				add("%s.proto: %q: use tcp or udp", key, pr)
			}
		}
		if a.Name != "" && !allowNameRe.MatchString(a.Name) {
			add("%s.name: %q: use lowercase letters, digits and hyphens, for example name: plex", key, a.Name)
		}
		if a.From != "" && a.From != "lan" {
			p, err := netip.ParsePrefix(a.From)
			switch {
			case err != nil:
				add("%s.from: %q: use lan or a network such as 192.168.1.0/24", key, a.From)
			case p.Bits() == 0:
				add("%s.from: %s is the whole internet; keelbox opens ports to the LAN or a network you name (from: lan). For a public port, add a UFW rule yourself: keelbox keeps rules it did not make", key, a.From)
			}
		}
		if !c.FirewallEnabled() {
			add("%s: host.firewall.enabled is false, so keelbox manages no rules; remove host.firewall.allow, or enable the firewall", key)
		}
	}
	c.validateLinkLocalAccounts(add)
}

// validateLinkLocalAccounts checks host.firewall.link_local_accounts. A
// name ends up in an nftables comment and its uid in a rule, so only plain
// account names pass. Whether the account exists is the box's to say:
// doctor warns about one that does not.
func (c *Config) validateLinkLocalAccounts(add func(string, ...any)) {
	const key = "host.firewall.link_local_accounts"
	names := c.Host.Firewall.LinkLocalAccounts
	if len(names) > MaxLinkLocalAccounts {
		add("%s: %d accounts, and at most %d are taken; keep the ones whose service reads a link-local address", key, len(names), MaxLinkLocalAccounts)
	}
	seen := map[string]bool{}
	for i, n := range names {
		switch {
		case !userRe.MatchString(n):
			add("%s[%d]: %q is not an account name; give the name as /etc/passwd has it, for example link_local_accounts: [do-agent]", key, i, n)
		case n == "root":
			add("%s[%d]: root reaches link-local addresses already, and no rule limits it; remove root from the list", key, i)
		case n == c.Users.Agent.Name:
			add("%s[%d]: %s is the agent account, and naming it here would undo the limit that keeps the agent from link-local addresses and cloud metadata; remove %s from the list", key, i, n, n)
		case seen[n]:
			add("%s[%d]: %s is listed twice; remove one", key, i, n)
		}
		seen[n] = true
	}
}

func firstNonZero(p PortRange) string {
	if p.IsZero() {
		return "8080"
	}
	return p.String()
}

// AllowSummary describes the entries for messages: "plex 32400/tcp, unifi
// (8080, 8443/tcp; 3478, 10001/udp)".
func AllowSummary(rules []Allow) string {
	var out []string
	for _, a := range rules {
		if s, ok := services.Lookup(a.Service); ok && a.Service != "" {
			out = append(out, fmt.Sprintf("%s (%s)", a.Label(), s.Summary()))
			continue
		}
		out = append(out, a.Label()+" "+a.Key())
	}
	return strings.Join(out, ", ")
}

// AllowYAML is an entry in the flow style the config uses:
// {name: plex, port: 32400}, {service: unifi}.
func AllowYAML(a Allow) string {
	var parts []string
	if a.Name != "" {
		parts = append(parts, "name: "+yamlScalar(a.Name))
	}
	if a.Service != "" {
		parts = append(parts, "service: "+a.Service)
	}
	if !a.Port.IsZero() {
		parts = append(parts, "port: "+a.Port.String())
	}
	if a.Proto != "" && a.Proto != "tcp" {
		parts = append(parts, "proto: "+a.Proto)
	}
	if a.From != "" && a.From != "lan" {
		parts = append(parts, "from: "+yamlScalar(a.From))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// ValidTimezone and ValidLocale report whether host.timezone and
// host.locale would accept a value, so keelbox up can keep a machine's own.
func ValidTimezone(s string) bool { return tzRe.MatchString(s) }

// ValidHostname reports whether host.hostname would accept a value: one
// DNS label, lowercase.
func ValidHostname(s string) bool { return labelRe.MatchString(s) }

// ValidLocale: see ValidTimezone.
func ValidLocale(s string) bool { return localeRe.MatchString(s) }
