package hetzner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

// The box's Cloud Firewall (docs/adr/0022-hetzner-cloud-firewall.md): one
// per box, named keelbox-<box>, labelled like the server. Inbound it admits
// Tailscale's UDP port, and SSH only until the box has closed public SSH.
// With no outbound rules, Hetzner allows all outbound traffic.
const (
	// TailscalePort is tailscaled's default UDP port. Admitting it lets
	// peers behind difficult NATs connect directly instead of through a
	// relay; tailscaled already accepts it in its own netfilter chain.
	TailscalePort = "41641"
)

var anywhere = []string{"0.0.0.0/0", "::/0"}

// FirewallName is the box's firewall name.
func FirewallName(box string) string { return "keelbox-" + box }

// FirewallRules are the inbound rules for a box.
func FirewallRules(publicSSH bool) []FirewallRule {
	rules := []FirewallRule{{Direction: "in", Protocol: "udp", Port: TailscalePort, SourceIPs: anywhere, Description: "Tailscale direct connections"}}
	if publicSSH {
		rules = append(rules, FirewallRule{Direction: "in", Protocol: "tcp", Port: "22", SourceIPs: anywhere,
			Description: "SSH until keelbox reaches the box over the tailnet"})
	}
	return rules
}

// HasPublicSSH reports whether a firewall admits SSH from anywhere.
func HasPublicSSH(f *Firewall) bool {
	for _, r := range f.Rules {
		if r.Direction == "in" && r.Protocol == "tcp" && r.Port == "22" {
			return true
		}
	}
	return false
}

func ruleKey(r FirewallRule) string {
	src := append([]string(nil), r.SourceIPs...)
	dst := append([]string(nil), r.DestinationIPs...)
	sort.Strings(src)
	sort.Strings(dst)
	return strings.Join([]string{r.Direction, r.Protocol, r.Port, strings.Join(src, ","), strings.Join(dst, ","), r.Description}, "|")
}

// SameRules reports whether two rule sets are equal, in any order.
func SameRules(a, b []FirewallRule) bool {
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

func managed(f *Firewall, box string) bool {
	return f.Labels[LabelManaged] == "true" && f.Labels[LabelBox] == box
}

// FindFirewall returns the box's keelbox firewall, or nil.
func FindFirewall(ctx context.Context, cl *Client, box string) (*Firewall, error) {
	fws, err := cl.Firewalls(ctx, LabelManaged+"=true,"+LabelBox+"="+box)
	if err != nil {
		return nil, err
	}
	switch len(fws) {
	case 0:
		return nil, nil
	case 1:
		return &fws[0], nil
	}
	return nil, fmt.Errorf("%d firewalls are labelled for box %s; keep one and delete the rest in the Hetzner Console (Firewalls), then run the command again", len(fws), box)
}

// EnsureFirewall makes the box's firewall exist with its rules and, when
// serverID is not 0, attached to the server. publicSSH nil keeps the
// current SSH rule (SSH is admitted on a new firewall). It returns the
// firewall and a description of each change made.
func EnsureFirewall(ctx context.Context, cl *Client, c *config.Config, serverID int64, publicSSH *bool) (*Firewall, []string, error) {
	var changes []string
	f, err := FindFirewall(ctx, cl, c.Box)
	if err != nil {
		return nil, nil, err
	}
	if f == nil {
		name := FirewallName(c.Box)
		other, err := cl.FirewallByName(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		if other != nil {
			return nil, nil, fmt.Errorf("a firewall named %s exists (id %d) without keelbox's labels; keelbox does not touch it. Rename or delete it in the Hetzner console", name, other.ID)
		}
		ssh := publicSSH == nil || *publicSSH
		var to []FirewallResource
		if serverID != 0 {
			to = []FirewallResource{ServerResource(serverID)}
		}
		f, acts, err := cl.CreateFirewall(ctx, name, Labels(c), FirewallRules(ssh), to)
		if err != nil {
			return nil, nil, err
		}
		if err := cl.WaitActions(ctx, acts, time.Second); err != nil {
			return f, nil, err
		}
		changes = append(changes, "created firewall "+name+" ("+describeRules(f.Rules, FirewallRules(ssh))+")")
		if serverID != 0 {
			changes = append(changes, fmt.Sprintf("attached it to server %d", serverID))
			f.AppliedTo = to
		}
		return f, changes, nil
	}
	ssh := HasPublicSSH(f)
	if publicSSH != nil {
		ssh = *publicSSH
	}
	if want := FirewallRules(ssh); !SameRules(f.Rules, want) {
		acts, err := cl.SetFirewallRules(ctx, f.ID, want)
		if err != nil {
			return f, changes, err
		}
		if err := cl.WaitActions(ctx, acts, time.Second); err != nil {
			return f, changes, err
		}
		f.Rules = want
		changes = append(changes, "set firewall "+f.Name+" rules: "+describeRules(want, want))
	}
	if serverID != 0 && !f.AppliedToServer(serverID) {
		acts, err := cl.ApplyFirewall(ctx, f.ID, []FirewallResource{ServerResource(serverID)})
		if err != nil {
			return f, changes, err
		}
		if err := cl.WaitActions(ctx, acts, time.Second); err != nil {
			return f, changes, err
		}
		f.AppliedTo = append(f.AppliedTo, ServerResource(serverID))
		changes = append(changes, fmt.Sprintf("attached firewall %s to server %d", f.Name, serverID))
	}
	return f, changes, nil
}

// DescribeRules is a short list of a firewall's inbound rules.
func DescribeRules(rules []FirewallRule) string { return describeRules(rules, rules) }

func describeRules(rules, fallback []FirewallRule) string {
	if len(rules) == 0 {
		rules = fallback
	}
	var parts []string
	for _, r := range rules {
		if r.Direction == "in" {
			parts = append(parts, "in "+r.Protocol+" "+r.Port)
		}
	}
	if len(parts) == 0 {
		return "no inbound"
	}
	return strings.Join(parts, ", ")
}

// DestroyFirewall deletes the box's keelbox firewall, if there is one,
// detaching it first. Hetzner refuses to delete a firewall still in use for
// a short while after its server is deleted, so it retries.
func DestroyFirewall(ctx context.Context, cl *Client, box string) (*Firewall, error) {
	f, err := FindFirewall(ctx, cl, box)
	if err != nil || f == nil {
		return nil, err
	}
	if !managed(f, box) {
		return nil, fmt.Errorf("refusing to delete firewall %d: not labelled %s=true for box %s", f.ID, LabelManaged, box)
	}
	if len(f.AppliedTo) > 0 {
		acts, err := cl.RemoveFirewall(ctx, f.ID, f.AppliedTo)
		if err != nil && !apiErr(err, "firewall_resource_not_found", 404) {
			return f, err
		}
		if err := cl.WaitActions(ctx, acts, time.Second); err != nil {
			return f, err
		}
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := cl.DeleteFirewall(ctx, f.ID)
		if err == nil || apiErr(err, "", 404) {
			return f, nil
		}
		if !apiErr(err, "resource_in_use", 0) || time.Now().After(deadline) {
			return f, err
		}
		select {
		case <-ctx.Done():
			return f, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// apiErr reports an API error with the code, or with the HTTP status.
func apiErr(err error, code string, status int) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return (code != "" && ae.Code == code) || (status != 0 && ae.Status == status)
}
