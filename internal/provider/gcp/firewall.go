package gcp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The box's firewall, kept like the Hetzner and DigitalOcean ones
// (docs/adr/0022-hetzner-cloud-firewall.md): inbound it admits Tailscale's
// UDP port, and SSH only until the box has closed public SSH.
//
// A VPC network has no firewall object to attach to a VM. It has rules,
// each with a priority, and the "default" network comes with rules that
// admit SSH, RDP, ICMP and internal traffic to every VM in it. So the box
// gets two rules, both applying to the box's network tag only:
//
//	keelbox-<box>-allow  priority 900  admits what the box needs
//	keelbox-<box>-deny   priority 901  refuses every other inbound packet
//
// The lower number wins, so the deny rule overrides the network's own
// rules (priority 65534) and rules made at the default priority (1000),
// and the allow rule overrides the deny rule. Outbound traffic needs no
// rule: a VPC network admits it unless a rule refuses it.
//
// The rules name a tag, not a VM, so they exist before the VM does and
// apply from the moment it is created with the tag.

const (
	// TailscalePort is tailscaled's default UDP port. Admitting it lets
	// peers behind difficult NATs connect directly instead of through a relay.
	TailscalePort = "41641"
	// Network is the VPC network the box is created in.
	Network = "default"
	// AllowPriority and DenyPriority are the priorities of the box's two
	// rules. Lower numbers win.
	AllowPriority = 900
	DenyPriority  = 901

	anywhere = "0.0.0.0/0"
)

// BoxTag is the network tag on the VM of a box. The box's firewall rules
// apply to it.
func BoxTag(box string) string { return "keelbox-box-" + box }

// AllowRuleName and DenyRuleName are the names of the box's two rules.
func AllowRuleName(box string) string { return "keelbox-" + box + "-allow" }

// DenyRuleName is the name of the rule that refuses what the allow rule
// does not admit.
func DenyRuleName(box string) string { return "keelbox-" + box + "-deny" }

// Firewall rules and addresses carry no labels, so keelbox knows its own
// by name and by this description, and changes or deletes one only when
// both match.
func managedDescription(what, box string) string {
	return "Managed by keelbox: " + what + " of box " + box + ". Deleted by keelbox destroy."
}

// PortSpec is one protocol of a firewall rule with its ports; no ports
// means every port.
type PortSpec struct {
	IPProtocol string   `json:"IPProtocol"`
	Ports      []string `json:"ports,omitempty"`
}

// Rule is a VPC firewall rule.
type Rule struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Network      string     `json:"network"`
	Direction    string     `json:"direction"`
	Priority     int        `json:"priority"`
	Disabled     bool       `json:"disabled"`
	SourceRanges []string   `json:"sourceRanges"`
	TargetTags   []string   `json:"targetTags"`
	Allowed      []PortSpec `json:"allowed"`
	Denied       []PortSpec `json:"denied"`
	// Other ways a rule can pick its packets and its VMs. keelbox sets
	// none of them; a rule that has one is not as keelbox wrote it.
	SourceTags            []string `json:"sourceTags"`
	SourceServiceAccounts []string `json:"sourceServiceAccounts"`
	TargetServiceAccounts []string `json:"targetServiceAccounts"`
	DestinationRanges     []string `json:"destinationRanges"`
}

// inNetwork reports whether the rule is in the box's network.
func (r *Rule) inNetwork() bool { return base(r.Network) == Network }

// owned reports whether keelbox made the rule for the box: it has the
// description keelbox writes and applies to the box's tag and nothing else.
func (r *Rule) owned(what, box string) bool {
	return r.Description == managedDescription(what, box) && len(r.TargetTags) == 1 && r.TargetTags[0] == BoxTag(box)
}

// specs is a rule's protocols and ports in one spelling, sorted:
// "tcp:22", "udp:41641", "all".
func specs(list []PortSpec) []string {
	var out []string
	for _, s := range list {
		if len(s.Ports) == 0 {
			out = append(out, s.IPProtocol)
		}
		for _, p := range s.Ports {
			out = append(out, s.IPProtocol+":"+p)
		}
	}
	sort.Strings(out)
	return out
}

// AllowSpecs are the protocols and ports a box admits, as gcloud's
// --rules takes them.
func AllowSpecs(publicSSH bool) []string {
	if publicSSH {
		return []string{"tcp:22", "udp:" + TailscalePort}
	}
	return []string{"udp:" + TailscalePort}
}

// Firewall is the box's pair of rules; either is nil when it does not exist.
type Firewall struct {
	Allow, Deny *Rule
}

// HasPublicSSH reports whether the allow rule admits TCP port 22.
func (f *Firewall) HasPublicSSH() bool {
	if f.Allow == nil {
		return false
	}
	for _, s := range f.Allow.Allowed {
		if s.IPProtocol == "all" || (s.IPProtocol == "tcp" && len(s.Ports) == 0) {
			return true
		}
		if s.IPProtocol != "tcp" {
			continue
		}
		for _, p := range s.Ports {
			lo, hi, ranged := strings.Cut(p, "-")
			if !ranged {
				hi = lo
			}
			l, err1 := strconv.Atoi(lo)
			h, err2 := strconv.Atoi(hi)
			if err1 == nil && err2 == nil && l <= 22 && 22 <= h {
				return true
			}
		}
	}
	return false
}

// shaped reports whether a rule still picks its packets as keelbox wrote
// it: inbound, from anywhere, at its priority, enabled, with no selector
// keelbox does not set.
func shaped(r *Rule, priority int) bool {
	return r.inNetwork() && r.Direction == "INGRESS" && r.Priority == priority && !r.Disabled &&
		len(r.SourceRanges) == 1 && r.SourceRanges[0] == anywhere &&
		len(r.SourceTags)+len(r.SourceServiceAccounts)+len(r.TargetServiceAccounts)+len(r.DestinationRanges) == 0
}

// InStep reports whether both rules exist as keelbox writes them, with
// SSH admitted or not.
func (f *Firewall) InStep(publicSSH bool) bool {
	return f.Allow != nil && f.Deny != nil &&
		shaped(f.Allow, AllowPriority) && len(f.Allow.Denied) == 0 && strings.Join(specs(f.Allow.Allowed), ",") == strings.Join(AllowSpecs(publicSSH), ",") &&
		shaped(f.Deny, DenyPriority) && len(f.Deny.Allowed) == 0 && strings.Join(specs(f.Deny.Denied), ",") == "all"
}

// Describe is a short list of what the box's rules admit.
func (f *Firewall) Describe() string {
	if f.Allow == nil {
		return "no inbound"
	}
	var parts []string
	for _, s := range specs(f.Allow.Allowed) {
		proto, port, ok := strings.Cut(s, ":")
		if !ok {
			port = "all"
		}
		parts = append(parts, "in "+proto+" "+port)
	}
	if len(parts) == 0 {
		return "no inbound"
	}
	return strings.Join(parts, ", ")
}

// Rules lists the project's firewall rules.
func (c *Client) Rules(ctx context.Context) ([]Rule, error) {
	var rules []Rule
	return rules, c.compute(ctx, &rules, "firewall-rules", "list")
}

// BoxFirewall picks the box's two rules out of the project's. A rule of
// either name that keelbox did not make is an error: keelbox does not
// touch it, and cannot use the name.
func BoxFirewall(rules []Rule, box string) (*Firewall, error) {
	f := &Firewall{}
	for i := range rules {
		r := &rules[i]
		var what string
		switch r.Name {
		case AllowRuleName(box):
			what, f.Allow = "the allow rule", r
		case DenyRuleName(box):
			what, f.Deny = "the deny rule", r
		default:
			continue
		}
		if !r.owned(what, box) {
			return nil, fmt.Errorf("a firewall rule named %s exists that keelbox did not create (it lacks keelbox's description, or does not apply to the tag %s alone), so keelbox does not touch it. "+
				"Rename the box, or delete the rule: gcloud compute firewall-rules delete %s", r.Name, BoxTag(box), r.Name)
		}
	}
	return f, nil
}

// FindFirewall returns the box's keelbox rules; either may be nil.
func FindFirewall(ctx context.Context, cl *Client, box string) (*Firewall, error) {
	rules, err := cl.Rules(ctx)
	if err != nil {
		return nil, err
	}
	return BoxFirewall(rules, box)
}

// Bypasses names the other rules of the network that admit inbound
// traffic to the box ahead of keelbox's deny rule: enabled allow rules
// with a priority number below DenyPriority that apply to every VM or to
// the box's tag. keelbox does not change them; doctor reports them.
func Bypasses(rules []Rule, box string) []string {
	var names []string
	for i := range rules {
		r := &rules[i]
		if r.Name == AllowRuleName(box) || !r.inNetwork() || r.Direction != "INGRESS" || r.Disabled || len(r.Allowed) == 0 || r.Priority >= DenyPriority {
			continue
		}
		applies := len(r.TargetTags) == 0 && len(r.TargetServiceAccounts) == 0
		for _, t := range r.TargetTags {
			applies = applies || t == BoxTag(box)
		}
		if applies {
			names = append(names, r.Name)
		}
	}
	sort.Strings(names)
	return names
}

// ruleFlags are the flags that make a rule pick its packets as keelbox
// writes it; create and update both take them.
func ruleFlags(priority int, specs []string) []string {
	return []string{"--rules", strings.Join(specs, ","), "--source-ranges", anywhere, "--priority", strconv.Itoa(priority)}
}

func (c *Client) createRule(ctx context.Context, name, what, box, action string, priority int, specs []string) error {
	args := append([]string{name, "--network", Network, "--direction", "INGRESS", "--action", action,
		"--target-tags", BoxTag(box), "--description", managedDescription(what, box)}, ruleFlags(priority, specs)...)
	return c.compute(ctx, nil, "firewall-rules", "create", args...)
}

// EnsureFirewall makes the box's two rules exist as keelbox writes them.
// publicSSH nil keeps what the allow rule admits now (SSH is admitted by
// a new one). It returns the rules and a description of each change made.
func EnsureFirewall(ctx context.Context, cl *Client, box string, publicSSH *bool) (*Firewall, []string, error) {
	f, err := FindFirewall(ctx, cl, box)
	if err != nil {
		return nil, nil, err
	}
	ssh := f.Allow == nil || f.HasPublicSSH()
	if publicSSH != nil {
		ssh = *publicSSH
	}
	if f.InStep(ssh) {
		return f, nil, nil
	}
	var changes []string
	want := AllowSpecs(ssh)
	// The deny rule first: the box is never more open than keelbox means.
	switch {
	case f.Deny == nil:
		if err := cl.createRule(ctx, DenyRuleName(box), "the deny rule", box, "DENY", DenyPriority, []string{"all"}); err != nil {
			return nil, nil, err
		}
		changes = append(changes, fmt.Sprintf("created firewall rule %s (refuses other inbound traffic) for VMs tagged %s", DenyRuleName(box), BoxTag(box)))
	case !shaped(f.Deny, DenyPriority) || strings.Join(specs(f.Deny.Denied), ",") != "all":
		args := append([]string{f.Deny.Name, "--no-disabled"}, ruleFlags(DenyPriority, []string{"all"})...)
		if err := cl.compute(ctx, nil, "firewall-rules", "update", args...); err != nil {
			return nil, nil, err
		}
		changes = append(changes, "set firewall rule "+f.Deny.Name+" back to refusing all other inbound traffic")
	}
	switch {
	case f.Allow == nil:
		if err := cl.createRule(ctx, AllowRuleName(box), "the allow rule", box, "ALLOW", AllowPriority, want); err != nil {
			return nil, changes, err
		}
		changes = append(changes, fmt.Sprintf("created firewall rule %s (%s) for VMs tagged %s", AllowRuleName(box), describeSpecs(want), BoxTag(box)))
	case !shaped(f.Allow, AllowPriority) || strings.Join(specs(f.Allow.Allowed), ",") != strings.Join(want, ","):
		args := append([]string{f.Allow.Name, "--no-disabled"}, ruleFlags(AllowPriority, want)...)
		if err := cl.compute(ctx, nil, "firewall-rules", "update", args...); err != nil {
			return nil, changes, err
		}
		changes = append(changes, "set firewall rule "+f.Allow.Name+": "+describeSpecs(want))
	}
	// Read back: what is reported is what the project now holds.
	f, err = FindFirewall(ctx, cl, box)
	if err != nil {
		return nil, changes, err
	}
	if !f.InStep(ssh) {
		return f, changes, fmt.Errorf("the firewall rules %s and %s are not as keelbox set them (%s); delete both and run the command again: gcloud compute firewall-rules delete %s %s%s",
			AllowRuleName(box), DenyRuleName(box), f.Describe(), AllowRuleName(box), DenyRuleName(box), cl.projectFlag())
	}
	return f, changes, nil
}

func describeSpecs(specs []string) string {
	parts := make([]string, len(specs))
	for i, s := range specs {
		proto, port, _ := strings.Cut(s, ":")
		parts[i] = "in " + proto + " " + port
	}
	return strings.Join(parts, ", ")
}

// DestroyFirewall deletes the box's keelbox rules, if there are any, and
// returns the names of those it deleted.
func DestroyFirewall(ctx context.Context, cl *Client, box string) ([]string, error) {
	f, err := FindFirewall(ctx, cl, box)
	if err != nil {
		return nil, err
	}
	var gone []string
	// The allow rule first: what is left meanwhile refuses, not admits.
	for _, r := range []*Rule{f.Allow, f.Deny} {
		if r == nil {
			continue
		}
		if err := cl.compute(ctx, nil, "firewall-rules", "delete", r.Name); err != nil && !IsNotFound(err) {
			return gone, err
		}
		gone = append(gone, r.Name)
	}
	return gone, nil
}

// base is the last segment of a resource URL: gcloud prints a zone, a
// network or a machine type as the URL of the resource.
func base(url string) string {
	return url[strings.LastIndex(url, "/")+1:]
}
