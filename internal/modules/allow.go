package modules

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// host.firewall.allow: ports, ranges and services kept open to the LAN through UFW (and SSH, for
// host.firewall.public_ssh: lan), and the same
// exception in the guard for system Docker containers' published ports
// (dockerGuardScript), since those bypass UFW. keelbox owns the UFW rules
// commented "keelbox allow ..." and removes them when the entry goes; other
// rules are left alone.

// allowRule is one UFW rule: one port or range, protocol and source network.
type allowRule struct {
	src, proto, label string
	port              config.PortRange
}

func (r allowRule) args() []string {
	return []string{"from", r.src, "to", "any", "port", r.port.Colon(), "proto", r.proto}
}

func (r allowRule) key() string { return fmt.Sprintf("%s %s/%s", r.src, r.port, r.proto) }

func (r allowRule) v6() bool {
	p, err := netip.ParsePrefix(r.src)
	return err == nil && p.Addr().Is6()
}

// wantAllow is the UFW rules the config asks for. IPv6 sources are left
// out when UFW has IPv6 off, which would refuse them.
func wantAllow(e *engine.Env) []allowRule {
	ipv6 := true
	if data, err := e.X.ReadFile("/etc/default/ufw"); err == nil {
		ipv6 = engine.ParseEnvFile(string(data))["IPV6"] != "no"
	}
	var out []allowRule
	seen := map[string]bool{}
	rules := e.Cfg.AllowRules()
	if e.Cfg.PublicSSHMode() == config.PublicSSHLAN && e.Cfg.Tailscale() {
		// public_ssh: lan keeps SSH open to the LAN once the public rule
		// goes (publicssh.go); while it is open, the public rule covers it.
		rules = append([]config.Allow{{Name: "ssh-lan", Port: config.Port(22)}}, rules...)
	}
	for _, a := range rules {
		for _, src := range a.Sources() {
			r := allowRule{src: src, proto: a.Protocol(), port: a.Port, label: a.Label()}
			if (r.v6() && !ipv6) || seen[r.key()] {
				continue
			}
			seen[r.key()] = true
			out = append(out, r)
		}
	}
	return out
}

var addedAllowRe = regexp.MustCompile(`^ufw allow from (\S+) to any port (\d+)(?::(\d+))? proto (tcp|udp) comment 'keelbox allow ([^']*)'$`)

// haveAllow is keelbox's allow rules UFW has now, from `ufw show added`,
// which lists rules in the form they were added in, active or not.
func haveAllow(ctx context.Context, e *engine.Env) ([]allowRule, error) {
	out, err := system.Output(ctx, e.X, system.C("ufw", "show", "added"))
	if err != nil {
		return nil, nil // UFW not installed yet: nothing added
	}
	var rules []allowRule
	for _, l := range strings.Split(out, "\n") {
		m := addedAllowRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		first, _ := strconv.Atoi(m[2]) // the pattern has digits only
		last := first
		if m[3] != "" {
			last, _ = strconv.Atoi(m[3])
		}
		rules = append(rules, allowRule{src: m[1], port: config.Ports(first, last), proto: m[4], label: m[5]})
	}
	return rules, nil
}

// allowDiff is what to add and what to remove.
func allowDiff(want, have []allowRule) (add, remove []allowRule) {
	h := map[string]allowRule{}
	for _, r := range have {
		h[r.key()+" "+r.label] = r
	}
	w := map[string]bool{}
	for _, r := range want {
		k := r.key() + " " + r.label
		w[k] = true
		if _, ok := h[k]; !ok {
			add = append(add, r)
		}
	}
	for k, r := range h {
		if !w[k] {
			remove = append(remove, r)
		}
	}
	sort.Slice(remove, func(i, j int) bool { return remove[i].key() < remove[j].key() })
	return add, remove
}

func firewallAllowStep() engine.Step {
	diff := func(ctx context.Context, e *engine.Env) ([]allowRule, []allowRule, error) {
		have, err := haveAllow(ctx, e)
		if err != nil {
			return nil, nil, err
		}
		add, remove := allowDiff(wantAllow(e), have)
		return add, remove, nil
	}
	return engine.Step{
		Name: "firewall-allow",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			add, remove, err := diff(ctx, e)
			if err != nil {
				return engine.Status{}, err
			}
			var p []string
			for _, r := range add {
				p = append(p, "allow "+r.key())
			}
			for _, r := range remove {
				p = append(p, "remove "+r.key())
			}
			if len(p) > 0 {
				return engine.Pending("ufw: %s", strings.Join(p, ", ")), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			add, remove, err := diff(ctx, e)
			if err != nil {
				return err
			}
			for _, r := range remove {
				if _, err := e.X.Run(ctx, system.C("ufw", append([]string{"delete", "allow"}, r.args()...)...)); err != nil {
					return err
				}
			}
			for _, r := range add {
				if _, err := e.X.Run(ctx, system.C("ufw", append(append([]string{"allow"}, r.args()...), "comment", "keelbox allow "+r.label)...)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// dockerGuardAllow is the guard's exception for each allowed port or
// range, per address family: a connection forwarded to a container whose
// published (pre-NAT) port is allowed, from an allowed network, is let
// through. rules are expanded (config.Expand).
func dockerGuardAllow(rules []config.Allow) (v4, v6 []string) {
	seen := map[string]bool{}
	for _, a := range rules {
		for _, src := range a.Sources() {
			line := fmt.Sprintf("$ipt -A KEELBOX-DOCKER -s %s -p %s -m conntrack --ctorigdstport %s -j RETURN", src, a.Protocol(), a.Port.Colon())
			if seen[line] {
				continue
			}
			seen[line] = true
			if strings.Contains(src, ":") {
				v6 = append(v6, line)
			} else {
				v4 = append(v4, line)
			}
		}
	}
	return v4, v6
}
