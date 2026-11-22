package aws

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The instance's Lightsail firewall, kept like the Hetzner Cloud Firewall
// (docs/adr/0022-hetzner-cloud-firewall.md): inbound it admits Tailscale's
// UDP port, and SSH only until the box has closed public SSH. Lightsail
// filters inbound traffic only; a new instance admits 22 and 80.

// TailscalePort is tailscaled's default UDP port. Admitting it lets peers
// behind difficult NATs connect directly instead of through a relay.
const TailscalePort = 41641

// Port is one rule of an instance's firewall: a port range and protocol,
// admitted from the given IPv4 and IPv6 ranges.
type Port struct {
	FromPort        int      `json:"fromPort"`
	ToPort          int      `json:"toPort"`
	Protocol        string   `json:"protocol"`
	Cidrs           []string `json:"cidrs"`
	Ipv6Cidrs       []string `json:"ipv6Cidrs"`
	CidrListAliases []string `json:"cidrListAliases"`
}

var (
	anyIPv4 = []string{"0.0.0.0/0"}
	anyIPv6 = []string{"::/0"}
)

// PortRules are the firewall rules for a box.
func PortRules(publicSSH bool) []Port {
	rules := []Port{{FromPort: TailscalePort, ToPort: TailscalePort, Protocol: "udp", Cidrs: anyIPv4, Ipv6Cidrs: anyIPv6}}
	if publicSSH {
		rules = append(rules, Port{FromPort: 22, ToPort: 22, Protocol: "tcp", Cidrs: anyIPv4, Ipv6Cidrs: anyIPv6})
	}
	return rules
}

// HasPublicSSH reports whether the rules admit TCP port 22.
func HasPublicSSH(ports []Port) bool {
	for _, p := range ports {
		if (p.Protocol == "tcp" || p.Protocol == "all") && p.FromPort <= 22 && p.ToPort >= 22 {
			return true
		}
	}
	return false
}

func portKey(p Port) string {
	v4, v6 := append([]string(nil), p.Cidrs...), append([]string(nil), p.Ipv6Cidrs...)
	// A rule listed without ranges is open to every address.
	if len(v4) == 0 && len(v6) == 0 && len(p.CidrListAliases) == 0 {
		v4, v6 = anyIPv4, anyIPv6
	}
	sort.Strings(v4)
	sort.Strings(v6)
	al := append([]string(nil), p.CidrListAliases...)
	sort.Strings(al)
	return fmt.Sprintf("%s|%d|%d|%s|%s|%s", p.Protocol, p.FromPort, p.ToPort, strings.Join(v4, ","), strings.Join(v6, ","), strings.Join(al, ","))
}

// SamePorts reports whether two rule sets are equal, in any order.
func SamePorts(a, b []Port) bool {
	if len(a) != len(b) {
		return false
	}
	ka, kb := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		ka[i], kb[i] = portKey(a[i]), portKey(b[i])
	}
	sort.Strings(ka)
	sort.Strings(kb)
	return strings.Join(ka, "\n") == strings.Join(kb, "\n")
}

// DescribePorts is a short list of the rules.
func DescribePorts(ports []Port) string {
	var parts []string
	for _, p := range ports {
		r := strconv.Itoa(p.FromPort)
		if p.ToPort != p.FromPort {
			r += "-" + strconv.Itoa(p.ToPort)
		}
		parts = append(parts, "in "+p.Protocol+" "+r)
	}
	if len(parts) == 0 {
		return "no inbound"
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// Ports reads the instance's firewall rules.
func (c *Client) Ports(ctx context.Context, instance string) ([]Port, error) {
	var r struct {
		PortStates []Port `json:"portStates"`
	}
	return r.PortStates, c.lightsail(ctx, &r, "get-instance-port-states", "--instance-name", instance)
}

// PutPorts replaces the instance's firewall rules: Lightsail closes every
// port not in the request.
func (c *Client) PutPorts(ctx context.Context, instance string, ports []Port) error {
	args := []string{"--instance-name", instance, "--port-infos"}
	for _, p := range ports {
		spec := fmt.Sprintf("fromPort=%d,toPort=%d,protocol=%s", p.FromPort, p.ToPort, p.Protocol)
		// A list with nothing in it is left out: the CLI's shorthand turns
		// "cidrs=" into a list holding one empty string.
		if len(p.Cidrs) > 0 {
			spec += ",cidrs=" + strings.Join(p.Cidrs, ",")
		}
		if len(p.Ipv6Cidrs) > 0 {
			spec += ",ipv6Cidrs=" + strings.Join(p.Ipv6Cidrs, ",")
		}
		args = append(args, spec)
	}
	return c.lightsail(ctx, nil, "put-instance-public-ports", args...)
}

// EnsurePorts makes the instance's firewall hold exactly the box's rules.
// publicSSH nil keeps whether SSH is admitted now (a new instance admits
// it). It returns a description of each change and the rules in force.
func EnsurePorts(ctx context.Context, cl *Client, instance string, publicSSH *bool) ([]string, []Port, error) {
	have, err := cl.Ports(ctx, instance)
	if err != nil {
		return nil, nil, err
	}
	ssh := HasPublicSSH(have)
	if publicSSH != nil {
		ssh = *publicSSH
	}
	want := PortRules(ssh)
	if SamePorts(have, want) {
		return nil, want, nil
	}
	if err := cl.PutPorts(ctx, instance, want); err != nil {
		return nil, have, err
	}
	return []string{fmt.Sprintf("set the firewall of instance %s: %s (was: %s)", instance, DescribePorts(want), DescribePorts(have))}, want, nil
}

// Offer is a bundle a box can run on, with its price.
type Offer struct {
	Bundle Bundle
}

// Describe is a one-line summary: cores, memory, disk, transfer and price.
func (o Offer) Describe() string {
	b := o.Bundle
	d := fmt.Sprintf("%d vCPU, %g GB, %d GB disk, %d GB transfer, USD %.2f/month", b.CPUCount, b.RAMSizeInGb, b.DiskSizeInGb, b.TransferPerMonthInGb, b.Price)
	if o.Small() {
		d += "; small for agents"
	}
	return d
}

// Small is a bundle below 2 vCPU or 4 GB, where a dev server, tests and a
// headless browser together run out of memory.
func (o Offer) Small() bool { return o.Bundle.CPUCount < 2 || o.Bundle.RAMSizeInGb < 4 }

// Offers are the bundles a box can run on, cheapest first.
func Offers(bundles []Bundle) []Offer {
	var out []Offer
	for _, b := range bundles {
		if b.Usable() {
			out = append(out, Offer{Bundle: b})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bundle.Price != out[j].Bundle.Price {
			return out[i].Bundle.Price < out[j].Bundle.Price
		}
		return out[i].Bundle.BundleID < out[j].Bundle.BundleID
	})
	return out
}

// DefaultOffer is the cheapest offer with at least 2 vCPUs and 4 GB, the
// size docs/guides/where-to-run.md gives as enough for one or two agents, else the cheapest.
func DefaultOffer(offers []Offer) int {
	for i, o := range offers {
		if !o.Small() {
			return i
		}
	}
	return 0
}
