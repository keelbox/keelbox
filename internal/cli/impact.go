package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/inventory"
	"github.com/keelbox/keelbox/internal/logbook"
)

// keelbox plan --impact: what apply would change on a machine already in
// use, grouped by how it matters to the owner, and the first-apply gate
// that stops before changing anything such a machine relies on (ADR 0038).

// impactReport is plan --impact's result; --json prints it as it is.
type impactReport struct {
	Impact     int                  `json:"impact"` // schema version
	Box        string               `json:"box"`
	Release    string               `json:"release"`
	FirstApply bool                 `json:"first_apply"` // no apply recorded yet: the gate applies
	Machine    *inventory.Inventory `json:"machine"`
	Items      []engine.ImpactItem  `json:"items"`
	Keep       impactKeep           `json:"keep"`
	Steps      int                  `json:"steps"`   // steps apply would change
	Errors     int                  `json:"errors"`  // checks that failed
	Adopted    bool                 `json:"adopted"` // run with --adopt
}

// impactKeep is the config that keeps what the gated items would cut off.
type impactKeep struct {
	Allow     []config.Allow `json:"allow,omitempty"`
	PublicSSH string         `json:"public_ssh,omitempty"`
}

// gated is what the first apply stops for.
func (r impactReport) gated() []engine.ImpactItem {
	if !r.FirstApply {
		return nil
	}
	return engine.Gated(r.Items)
}

func collectImpact(ctx context.Context, e *engine.Env, mods []engine.Module) impactReport {
	inv := inventory.Collect(ctx, e.X)
	plan, items := engine.Impact(ctx, e, mods, inv)
	r := impactReport{Impact: 1, Box: e.Cfg.Box, Release: e.M.Release, FirstApply: neverApplied(e),
		Machine: inv, Items: items, Adopted: e.Adopt}
	if r.Items == nil {
		r.Items = []engine.ImpactItem{}
	}
	for _, it := range plan {
		switch {
		case it.Err != nil:
			r.Errors++
		case !it.OK:
			r.Steps++
		}
	}
	seen := map[string]bool{}
	for _, it := range items {
		if it.Keep != nil && !seen[it.Subject] {
			seen[it.Subject] = true
			r.Keep.Allow = append(r.Keep.Allow, *it.Keep)
		}
		if it.KeepSSH != "" {
			r.Keep.PublicSSH = it.KeepSSH
		}
	}
	return r
}

// PlanImpact prints the impact report. Exit 0, or 1 if a check errored.
func PlanImpact(ctx context.Context, e *engine.Env, mods []engine.Module, asJSON bool, out io.Writer) int {
	r := collectImpact(ctx, e, mods)
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	} else {
		fmt.Fprint(out, header(e, "plan --impact"))
		printImpact(out, r, false)
	}
	if r.Errors > 0 {
		return 1
	}
	return 0
}

var groupTitle = map[engine.ImpactGroup]string{
	engine.ImpactCuts:     "Cuts off",
	engine.ImpactReplaces: "Replaces",
	engine.ImpactRestarts: "Restarts",
	engine.ImpactChanges:  "Changes",
	engine.ImpactKeeps:    "Keeps",
}

// machineLine sums up what keelbox found, in a line.
func machineLine(r impactReport) string {
	m := r.Machine
	var parts []string
	if m.Virt != "" {
		parts = append(parts, m.Virt+" guest")
	}
	if m.Docker.Running {
		parts = append(parts, plural(len(m.Docker.Containers), "container", "containers")+" (system Docker)")
	}
	parts = append(parts, plural(len(m.Listeners), "service", "services")+" listening on the network")
	var people []string
	for _, a := range m.Accounts {
		if a.UID != 0 {
			people = append(people, a.Name)
		}
	}
	if len(people) > 0 {
		parts = append(parts, "accounts "+strings.Join(people, ", "))
	}
	name := firstNonEmpty(m.Hostname, r.Box)
	return name + ": " + strings.Join(parts, ", ")
}

// printImpact writes the report; onlyGated limits it to the gate's items.
func printImpact(out io.Writer, r impactReport, onlyGated bool) {
	fmt.Fprintln(out, machineLine(r))
	marked := false
	for _, g := range engine.ImpactGroups {
		var items []engine.ImpactItem
		for _, it := range r.Items {
			if it.Group == g && (!onlyGated || it.Gate) {
				items = append(items, it)
			}
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s\n", groupTitle[g])
		for _, it := range items {
			mark := "  "
			if it.Gate && r.FirstApply {
				mark, marked = "* ", true
			}
			d := it.Detail
			if it.Hint != "" {
				d += " (" + it.Hint + ")"
			}
			fmt.Fprintf(out, "%s%-12s %s\n", mark, it.Subject, d)
		}
	}
	if len(r.Items) == 0 {
		fmt.Fprintln(out, "\nNothing in use here that apply would change.")
	}
	if y := keepYAML(r.Keep); y != "" {
		fmt.Fprintf(out, "\nTo keep them, add to the config:\n%s", y)
	}
	switch {
	case marked && r.Adopted:
		fmt.Fprintln(out, "\n* --adopt accepts these on the first apply.")
	case marked:
		fmt.Fprintf(out, "\n* The first apply stops before changing anything until each is kept in the config or accepted: keelbox apply %s --adopt\n", r.Box)
	case !r.FirstApply && len(engine.Gated(r.Items)) > 0:
		fmt.Fprintln(out, "\nThe box has been applied before, so apply does not stop for these.")
	}
	if !onlyGated {
		fmt.Fprintf(out, "\nIn all, %s; keelbox plan %s lists every step.\n", plural(r.Steps, "step changes", "steps change"), r.Box)
	}
}

// keepYAML is the config that keeps what would be cut off.
func keepYAML(k impactKeep) string {
	if len(k.Allow) == 0 && k.PublicSSH == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("  host:\n    firewall:\n")
	if k.PublicSSH != "" {
		fmt.Fprintf(&b, "      public_ssh: %s\n", k.PublicSSH)
	}
	if len(k.Allow) > 0 {
		b.WriteString("      allow:\n")
		for _, a := range k.Allow {
			b.WriteString("        - " + config.AllowYAML(a) + "\n")
		}
	}
	return b.String()
}

// neverApplied reports whether the logbook has no apply at all, failed
// ones included. A logbook that exists but does not read is a box keelbox
// has applied.
func neverApplied(e *engine.Env) bool {
	recs, err := logbook.Read(e.X)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	return err == nil && logbook.Last(recs, logbook.KindApply) == nil
}

// firstApplyGate stops the first apply on a machine in use before any
// change when it would cut off or replace something the owner relies on
// and neither the config nor --adopt says to go ahead. It reports whether
// the apply may go on.
func firstApplyGate(ctx context.Context, e *engine.Env, mods []engine.Module, stderr io.Writer) bool {
	if !neverApplied(e) {
		return true
	}
	r := collectImpact(ctx, e, mods)
	gated := r.gated()
	if len(gated) == 0 || e.Adopt {
		return true
	}
	failf(stderr, "%s is in use, and this first apply would cut off or replace what it runs. Nothing has changed.\n", e.Cfg.Box)
	fmt.Fprintf(stderr, "Everything apply would change there: keelbox plan %s --impact\n\n", e.Cfg.Box)
	printImpact(stderr, r, true)
	return false
}
