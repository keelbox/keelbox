package digitalocean

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The box's Cloud Firewall, kept like the Hetzner one
// (docs/adr/0022-hetzner-cloud-firewall.md): one per box, named
// keelbox-<box>. Inbound it admits Tailscale's UDP port, and SSH only
// until the box has closed public SSH. A DigitalOcean firewall blocks
// whatever no rule admits, in both directions, so it also carries rules
// that admit all outbound traffic.
//
// The firewall targets the box's tag, not a Droplet id. It can therefore
// exist before the Droplet does, and DigitalOcean applies it to a Droplet
// created with the tag by itself: not from the Droplet's first moment, but
// some seconds after the Droplet is active (see nameInFirewall in park.go).

// TailscalePort is tailscaled's default UDP port. Admitting it lets peers
// behind difficult NATs connect directly instead of through a relay.
const TailscalePort = "41641"

var anywhere = []string{"0.0.0.0/0", "::/0"}

// Peers are the other ends a firewall rule admits: an inbound rule's
// sources or an outbound rule's destinations.
type Peers struct {
	Addresses        []string `json:"addresses,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	DropletIDs       []int64  `json:"droplet_ids,omitempty"`
	LoadBalancerUIDs []string `json:"load_balancer_uids,omitempty"`
	KubernetesIDs    []string `json:"kubernetes_ids,omitempty"`
}

// Rule is one rule of a firewall. Sources is set on an inbound rule,
// Destinations on an outbound one.
type Rule struct {
	Protocol     string `json:"protocol"`
	Ports        string `json:"ports,omitempty"`
	Sources      *Peers `json:"sources,omitempty"`
	Destinations *Peers `json:"destinations,omitempty"`
}

// Firewall is a DigitalOcean Cloud Firewall.
type Firewall struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Inbound    []Rule   `json:"inbound_rules"`
	Outbound   []Rule   `json:"outbound_rules"`
	DropletIDs []int64  `json:"droplet_ids"`
	Tags       []string `json:"tags"`
	// Pending are the Droplets DigitalOcean has not finished applying the
	// firewall to, or taking it off.
	Pending []struct {
		DropletID int64 `json:"droplet_id"`
	} `json:"pending_changes"`
}

// FirewallName is the box's firewall name.
func FirewallName(box string) string { return "keelbox-" + box }

// InboundRules are the inbound rules for a box.
func InboundRules(publicSSH bool) []Rule {
	rules := []Rule{{Protocol: "udp", Ports: TailscalePort, Sources: &Peers{Addresses: anywhere}}}
	if publicSSH {
		rules = append(rules, Rule{Protocol: "tcp", Ports: "22", Sources: &Peers{Addresses: anywhere}})
	}
	return rules
}

// OutboundRules admit all outbound traffic.
func OutboundRules() []Rule {
	to := &Peers{Addresses: anywhere}
	return []Rule{{Protocol: "icmp", Destinations: to}, {Protocol: "tcp", Ports: "0", Destinations: to}, {Protocol: "udp", Ports: "0", Destinations: to}}
}

// ports is a rule's port range in one spelling: the API takes and returns
// "0", "all" and "1-65535" for every port, and "0" or nothing for ICMP.
func ports(r Rule) string {
	switch {
	case r.Protocol == "icmp", r.Ports == "", r.Ports == "0", r.Ports == "all", r.Ports == "1-65535":
		return "all"
	}
	return r.Ports
}

// HasPublicSSH reports whether a firewall admits TCP port 22.
func HasPublicSSH(f *Firewall) bool {
	for _, r := range f.Inbound {
		if r.Protocol != "tcp" {
			continue
		}
		p := ports(r)
		if p == "all" || p == "22" {
			return true
		}
		if lo, hi, ok := strings.Cut(p, "-"); ok {
			l, err1 := strconv.Atoi(lo)
			h, err2 := strconv.Atoi(hi)
			if err1 == nil && err2 == nil && l <= 22 && 22 <= h {
				return true
			}
		}
	}
	return false
}

func ruleKey(r Rule) string {
	p := r.Sources
	if p == nil {
		p = r.Destinations
	}
	if p == nil {
		p = &Peers{}
	}
	addrs := append([]string(nil), p.Addresses...)
	sort.Strings(addrs)
	// Any peer that is not an address range makes the rule one keelbox
	// did not write.
	other := fmt.Sprint(p.Tags, p.DropletIDs, p.LoadBalancerUIDs, p.KubernetesIDs)
	return strings.Join([]string{r.Protocol, ports(r), strings.Join(addrs, ","), other}, "|")
}

// SameRules reports whether two rule sets are equal, in any order.
func SameRules(a, b []Rule) bool {
	if len(a) != len(b) {
		return false
	}
	ka, kb := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		ka[i], kb[i] = ruleKey(a[i]), ruleKey(b[i])
	}
	sort.Strings(ka)
	sort.Strings(kb)
	return strings.Join(ka, "\n") == strings.Join(kb, "\n")
}

// DescribeRules is a short list of a firewall's inbound rules.
func DescribeRules(inbound []Rule) string {
	var parts []string
	for _, r := range inbound {
		parts = append(parts, "in "+r.Protocol+" "+ports(r))
	}
	if len(parts) == 0 {
		return "no inbound"
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// ruleArg is the rules in the form doctl's --inbound-rules and
// --outbound-rules take: rules separated by spaces, each a comma-separated
// list of key:value.
func ruleArg(rules []Rule) string {
	var out []string
	for _, r := range rules {
		parts := []string{"protocol:" + r.Protocol}
		if r.Protocol != "icmp" {
			parts = append(parts, "ports:"+r.Ports)
		}
		p := r.Sources
		if p == nil {
			p = r.Destinations
		}
		for _, a := range p.Addresses {
			parts = append(parts, "address:"+a)
		}
		out = append(out, strings.Join(parts, ","))
	}
	return strings.Join(out, " ")
}

// Targets reports whether the firewall applies to Droplets with the tag.
func (f *Firewall) Targets(tag string) bool {
	for _, t := range f.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Firewalls lists the account's firewalls.
func (c *Client) Firewalls(ctx context.Context) ([]Firewall, error) {
	var fws []Firewall
	return fws, c.compute(ctx, &fws, "firewall", "list")
}

// FindFirewall returns the box's keelbox firewall, or nil. Firewalls carry
// no tags of their own, so keelbox knows its own by name and by target:
// the firewall named keelbox-<box> that applies to the tag
// keelbox-box-<box>. One of that name with another target is an error:
// keelbox did not make it and does not touch it.
func FindFirewall(ctx context.Context, cl *Client, box string) (*Firewall, error) {
	fws, err := cl.Firewalls(ctx)
	if err != nil {
		return nil, err
	}
	name := FirewallName(box)
	var found []Firewall
	for _, f := range fws {
		if f.Name == name {
			found = append(found, f)
		}
	}
	switch {
	case len(found) == 0:
		return nil, nil
	case len(found) > 1:
		return nil, fmt.Errorf("%d firewalls are named %s; keep one and delete the rest in the DigitalOcean control panel (Networking, Firewalls), then run the command again", len(found), name)
	case !found[0].Targets(BoxTag(box)):
		return nil, fmt.Errorf("a firewall named %s exists (id %s) that does not apply to the tag %s, so keelbox did not create it and does not touch it. Rename or delete it in the DigitalOcean control panel (Networking, Firewalls)",
			name, found[0].ID, BoxTag(box))
	}
	return &found[0], nil
}

// firewallArgs is the full representation doctl's create and update take.
// An update resets whatever it leaves out, so the Droplet ids and tags the
// firewall already has are passed back.
func firewallArgs(f *Firewall) []string {
	args := []string{"--name", f.Name, "--inbound-rules", ruleArg(f.Inbound), "--outbound-rules", ruleArg(f.Outbound), "--tag-names", strings.Join(f.Tags, ",")}
	if len(f.DropletIDs) > 0 {
		ids := make([]string, len(f.DropletIDs))
		for i, id := range f.DropletIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		args = append(args, "--droplet-ids", strings.Join(ids, ","))
	}
	return args
}

// EnsureFirewall makes the box's firewall exist with its rules, applied
// to the box's tag. publicSSH nil keeps the current SSH rule (SSH is
// admitted on a new firewall). It returns the firewall and a description
// of each change made.
func EnsureFirewall(ctx context.Context, cl *Client, box string, publicSSH *bool) (*Firewall, []string, error) {
	f, err := FindFirewall(ctx, cl, box)
	if err != nil {
		return nil, nil, err
	}
	if f == nil {
		// A firewall can only name a tag that exists.
		if err := cl.ensureTag(ctx, BoxTag(box)); err != nil {
			return nil, nil, err
		}
		ssh := publicSSH == nil || *publicSSH
		want := &Firewall{Name: FirewallName(box), Inbound: InboundRules(ssh), Outbound: OutboundRules(), Tags: []string{BoxTag(box)}}
		var made []Firewall
		if err := cl.compute(ctx, &made, "firewall", "create", firewallArgs(want)...); err != nil {
			return nil, nil, err
		}
		if len(made) == 1 {
			want.ID, want.Status = made[0].ID, made[0].Status
		}
		return want, []string{fmt.Sprintf("created firewall %s (%s) for Droplets tagged %s", want.Name, DescribeRules(want.Inbound), BoxTag(box))}, nil
	}
	ssh := HasPublicSSH(f)
	if publicSSH != nil {
		ssh = *publicSSH
	}
	in, out := InboundRules(ssh), OutboundRules()
	if SameRules(f.Inbound, in) && SameRules(f.Outbound, out) {
		return f, nil, nil
	}
	f.Inbound, f.Outbound = in, out
	if err := cl.compute(ctx, nil, "firewall", "update", append([]string{f.ID}, firewallArgs(f)...)...); err != nil {
		return f, nil, err
	}
	return f, []string{"set firewall " + f.Name + " rules: " + DescribeRules(in)}, nil
}

// DestroyFirewall deletes the box's keelbox firewall, if there is one,
// and then the box's tag when nothing carries it any more. It returns
// the firewall it deleted.
func DestroyFirewall(ctx context.Context, cl *Client, box string) (*Firewall, error) {
	f, err := FindFirewall(ctx, cl, box)
	if err != nil {
		return nil, err
	}
	if f != nil {
		if err := cl.compute(ctx, nil, "firewall", "delete", f.ID, "--force"); err != nil && !notFound(err) {
			return nil, err
		}
	}
	return f, cl.dropUnusedTags(ctx, BoxTag(box), TagManaged)
}

// Tag is a DigitalOcean tag with how many resources carry it.
type Tag struct {
	Name      string `json:"name"`
	Resources struct {
		Count int `json:"count"`
	} `json:"resources"`
}

// tag returns the tag of that name, or nil.
func (c *Client) tag(ctx context.Context, name string) (*Tag, error) {
	var tags []Tag
	if err := c.compute(ctx, &tags, "tag", "get", name); err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(tags) == 0 {
		return nil, nil
	}
	return &tags[0], nil
}

func (c *Client) ensureTag(ctx context.Context, name string) error {
	if t, err := c.tag(ctx, name); err != nil || t != nil {
		return err
	}
	return c.compute(ctx, nil, "tag", "create", name)
}

// dropUnusedTags deletes each of the tags that no resource carries, so a
// destroyed box leaves no tag behind. A tag still in use stays: deleting
// it would take it off the resources that have it.
func (c *Client) dropUnusedTags(ctx context.Context, names ...string) error {
	for _, name := range names {
		t, err := c.tag(ctx, name)
		if err != nil {
			return err
		}
		if t == nil || t.Resources.Count != 0 {
			continue
		}
		if err := c.compute(ctx, nil, "tag", "delete", name, "--force"); err != nil && !notFound(err) {
			return err
		}
	}
	return nil
}
