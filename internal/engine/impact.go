package engine

import (
	"context"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/inventory"
)

// ImpactGroup is how an impact item matters to the machine's owner.
type ImpactGroup string

// The groups, in the order the report shows them.
const (
	// ImpactCuts: something reachable or usable now stops being so.
	ImpactCuts ImpactGroup = "cuts"
	// ImpactReplaces: software on the machine is removed or swapped.
	ImpactReplaces ImpactGroup = "replaces"
	// ImpactRestarts: a service in use restarts or reloads.
	ImpactRestarts ImpactGroup = "restarts"
	// ImpactChanges: settings and accounts that change.
	ImpactChanges ImpactGroup = "changes"
	// ImpactKeeps: what keelbox found and leaves as it is.
	ImpactKeeps ImpactGroup = "keeps"
)

// ImpactGroups lists the groups in report order.
var ImpactGroups = []ImpactGroup{ImpactCuts, ImpactReplaces, ImpactRestarts, ImpactChanges, ImpactKeeps}

// ImpactItem is one thing apply would change that the owner may care
// about, in plain words.
type ImpactItem struct {
	Group   ImpactGroup `json:"group"`
	Module  string      `json:"module"`
	Subject string      `json:"subject"` // what it is about: "8080/tcp", "sam", "docker"
	Detail  string      `json:"detail"`
	// Gate is set when the first apply stops for it until it is kept by
	// config or accepted with --adopt (ADR 0038).
	Gate bool `json:"gate,omitempty"`
	// Keep is the host.firewall.allow entry that keeps a port reachable:
	// a port, or a known service ({service: unifi}).
	Keep *config.Allow `json:"keep,omitempty"`
	// Ports are the ports a service item is about ("8080/tcp").
	Ports []string `json:"ports,omitempty"`
	// KeepSSH is the host.firewall.public_ssh value that keeps SSH.
	KeepSSH string `json:"keep_public_ssh,omitempty"`
	// Hint is another way to keep it, or what to do first, in a phrase.
	Hint string `json:"hint,omitempty"`
}

// Inspection is what impact functions read: the machine and the plan.
type Inspection struct {
	Inv  *inventory.Inventory
	Plan []PlanItem
}

// Pending reports whether apply would change module's step, and how.
func (in Inspection) Pending(module, step string) (string, bool) {
	for _, it := range in.Plan {
		if it.Module == module && it.Step == step && !it.OK && it.Err == nil {
			return it.Detail, true
		}
	}
	return "", false
}

// Impacter is a module that can say what its steps would change on a
// machine already in use. Optional: the Module interface is unchanged.
type Impacter interface {
	Impact(ctx context.Context, e *Env, in Inspection) []ImpactItem
}

// Impact plans every module and collects their impact on the machine
// inventoried.
func Impact(ctx context.Context, e *Env, mods []Module, inv *inventory.Inventory) ([]PlanItem, []ImpactItem) {
	in := Inspection{Inv: inv, Plan: Plan(ctx, e, mods)}
	var out []ImpactItem
	for _, m := range mods {
		im, ok := m.(Impacter)
		if !ok || !m.Enabled(e.Cfg) {
			continue
		}
		for _, it := range im.Impact(ctx, e, in) {
			if it.Module == "" {
				it.Module = m.Name()
			}
			out = append(out, it)
		}
	}
	return in.Plan, out
}

// Gated is the items the first apply stops for.
func Gated(items []ImpactItem) []ImpactItem {
	var out []ImpactItem
	for _, it := range items {
		if it.Gate {
			out = append(out, it)
		}
	}
	return out
}
