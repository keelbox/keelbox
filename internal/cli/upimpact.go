package cli

import (
	"context"
	"fmt"
	"github.com/keelbox/keelbox/internal/ui"
	"sort"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/services"
)

// keelbox up on a machine already in use runs keelbox plan --impact there
// before its go-ahead question: the owner sees what the first apply would
// cut off, replace or restart, and what it keeps, from the same report the
// first-apply gate stops on (ADR 0038). Ports up has already asked about
// are answered; anything else the gate would stop for needs an explicit
// yes, or --adopt without a terminal.

// inspectImpact runs the impact report on this machine or the server.
// A report that cannot be had is not an error: the first apply's own gate
// still stops before any change.
func (w *wizard) inspectImpact(ctx context.Context, a *answers) error {
	if a.via != viaLocal && a.via != viaSSH {
		return nil
	}
	text, err := a.starter.Render()
	if err != nil {
		return err
	}
	cfg, err := config.Parse([]byte(text), a.starter.Box+".yaml")
	if err != nil {
		return err
	}
	if w.p.Interactive() {
		if w.st.Live {
			say(w.out, w.st, w.st.Pointer(), "looking at what runs there now (nothing changes yet)...")
		} else {
			fmt.Fprintln(w.out, "\nLooking at what runs there now (nothing changes yet)...")
		}
	}
	askSudo := a.via == viaSSH && a.facts != nil && !a.facts.Sudo
	r, err := w.run.impact(ctx, cfg, options{sshUser: firstNonEmpty(w.o.sshUser, "root"), binary: w.o.binary, askSudo: askSudo})
	if err != nil {
		if w.st.Live {
			say(w.out, w.st, w.st.Bang(), fmt.Sprintf("could not list what is in use there (%v); the first apply checks again before it changes anything.", err))
		} else {
			fmt.Fprintf(w.out, "keelbox up: could not list what is in use there (%v); the first apply checks again before it changes anything.\n", err)
		}
		return nil
	}
	a.impact = &r
	return nil
}

// lanChoice is one thing up asks about keeping open to the LAN: a known
// service (internal/services), all its ports with one answer, or a port.
type lanChoice struct {
	Key     string   // the impact item's subject: "unifi" or "8096/tcp"
	Label   string   // "UniFi Network Application (container unifi)"
	Ports   []string // the ports listening now: "8080/tcp"
	Service string   // the preset, for a service
	l       listener // for a port
}

// keep is the host.firewall.allow entry for the choice.
func (c lanChoice) keep() config.Allow {
	if c.Service != "" {
		return config.Allow{Service: c.Service}
	}
	return allowRules([]listener{c.l})[0]
}

// lanCandidates are what up asks about keeping open to the LAN: those the
// impact report says the firewall or the Docker guard would cut off, or,
// without a report, everything the probe found listening. A service UFW
// already blocks, or already admits by a rule of its own, is not asked
// about: keelbox changes nothing for it. Known services come first, one
// choice each.
func lanCandidates(a *answers) []lanChoice {
	byKey := map[string]listener{}
	if a.facts != nil {
		for _, l := range a.facts.Listeners {
			byKey[l.Key()] = l
		}
	}
	if a.impact != nil && a.impact.Machine != nil {
		for _, l := range a.impact.Machine.Listeners { // seen as root: process names for every account
			byKey[l.Key()] = l
		}
	}
	var out []lanChoice
	if a.impact == nil {
		if a.facts == nil {
			return nil
		}
		idx := map[string]int{}
		for _, l := range a.facts.Listeners {
			if s, ok := services.Lookup(l.Service); ok && l.Service != "" && s.Has(l.Port, l.Proto) {
				if i, ok := idx[s.Name]; ok {
					out[i].Ports = append(out[i].Ports, l.Key())
					continue
				}
				idx[s.Name] = len(out)
				out = append(out, lanChoice{Key: s.Name, Label: l.Label, Ports: []string{l.Key()}, Service: s.Name})
				continue
			}
			out = append(out, lanChoice{Key: l.Key(), Label: l.Label, Ports: []string{l.Key()}, l: l})
		}
		sortChoices(out)
		return out
	}
	for _, it := range a.impact.Items {
		if it.Group != engine.ImpactCuts || it.Keep == nil {
			continue
		}
		if svc := it.Keep.Service; svc != "" {
			s, _ := services.Lookup(svc)
			label := s.Title
			if len(it.Ports) > 0 {
				if l, ok := byKey[it.Ports[0]]; ok && l.Service == svc {
					label = l.Label
				}
			}
			out = append(out, lanChoice{Key: it.Subject, Label: label, Ports: it.Ports, Service: svc})
			continue
		}
		l, ok := byKey[it.Subject]
		if !ok {
			l = listener{Port: it.Keep.Port.First, Proto: firstNonEmpty(it.Keep.Proto, "tcp"), Label: "container " + it.Keep.Name + " (Docker)", Name: it.Keep.Name}
		}
		if l.Container != "" || it.Module == "runtime" {
			l.Name = it.Keep.Name
		}
		out = append(out, lanChoice{Key: it.Subject, Label: l.Label, Ports: []string{l.Key()}, l: l})
	}
	sortChoices(out)
	return out
}

// sortChoices puts services first, by name, then ports by protocol and number.
func sortChoices(cs []lanChoice) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if (a.Service != "") != (b.Service != "") {
			return a.Service != ""
		}
		if a.Service != "" {
			return a.Service < b.Service
		}
		if a.l.Proto != b.l.Proto {
			return a.l.Proto < b.l.Proto
		}
		return a.l.Port < b.l.Port
	})
}

// answeredPorts are the subjects up asked about: their cut is the owner's
// answer to "keep open on the LAN".
func answeredPorts(a *answers) map[string]bool {
	out := map[string]bool{}
	for _, c := range lanCandidates(a) {
		out[c.Key] = true
	}
	return out
}

// showImpact lists what the report found in use, leaving out what the
// go-ahead summary already says (the firewall, sshd, the hostname, the
// ports up asked about).
func (w *wizard) showImpact(a *answers) {
	if a.impact == nil {
		return
	}
	asked := answeredPorts(a)
	self := "" // the account up connects as: the summary says what happens to it
	if a.facts != nil {
		self = a.facts.User
	}
	st := w.st
	var card []ui.Line
	for _, g := range engine.ImpactGroups {
		var lines []ui.Line
		for _, it := range a.impact.Items {
			if it.Group != g || asked[it.Subject] || (it.Subject == self && !it.Gate) || it.Subject == "sshd" || it.Subject == "hostname" || it.Subject == "ssh" ||
				(it.Subject == "ufw" && g == engine.ImpactChanges) {
				continue
			}
			mark := " "
			if it.Gate && a.impact.FirstApply {
				mark = st.Warn("*")
			}
			d := it.Detail
			if it.Hint != "" {
				d += " " + st.Dim("("+it.Hint+")")
			}
			lines = append(lines, ui.Line{Mark: mark, Key: it.Subject, Text: d})
		}
		if len(lines) == 0 {
			continue
		}
		if len(card) > 0 {
			card = append(card, ui.Blank())
		}
		card = append(card, ui.Text(st.Bold(groupTitle[g]+":")))
		card = append(card, lines...)
	}
	if len(card) > 0 {
		title := "In use there now"
		if a.via == viaLocal {
			title = "In use here now"
		}
		fmt.Fprint(w.out, "\n"+st.Card(ui.Card{Title: title, Lines: card, KeyWidth: 10}))
	}
}

// openGated are the subjects the first apply would stop for that up has
// not asked about: they need the owner's yes at the go-ahead, or --adopt.
func openGated(a *answers) []string {
	if a.impact == nil {
		return nil
	}
	asked := answeredPorts(a)
	var open []string
	for _, it := range a.impact.gated() {
		if !asked[it.Subject] {
			open = append(open, it.Subject)
		}
	}
	return open
}
