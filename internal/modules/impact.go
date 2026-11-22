package modules

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/inventory"
	"github.com/keelbox/keelbox/internal/services"
)

// What apply would change on a machine already in use, for keelbox plan
// --impact and the first-apply gate (ADR 0038). Each module says what its
// own steps would do to what the inventory found. Reachability is read
// from the firewall's state, not probed: keelbox cannot test from the LAN.

// allowed is the host.firewall.allow entry that keeps port/proto open, if any.
func allowed(c *config.Config, port int, proto string) (config.Allow, bool) {
	return c.Allowed(port, proto)
}

func keepEntry(name string, port int, proto string) *config.Allow {
	a := &config.Allow{Name: name, Port: config.Port(port)}
	if proto == "udp" {
		a.Proto = "udp"
	}
	return a
}

func fromWhere(a config.Allow) string {
	if a.From == "" || a.From == "lan" {
		return "the LAN"
	}
	return a.From
}

// Impact: the firewall, the accounts, sshd and the hostname.
func (Host) Impact(ctx context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	c, inv := e.Cfg, in.Inv
	var out []engine.ImpactItem
	add := func(it engine.ImpactItem) { out = append(out, it) }

	switch {
	case !c.FirewallEnabled():
		add(engine.ImpactItem{Group: engine.ImpactKeeps, Subject: "ufw", Detail: "host.firewall.enabled is false: keelbox leaves the firewall as it is"})
	case inv.UFW.Blocks():
		own := "SSH"
		if c.Tailscale() {
			own = "SSH and the tailnet"
		}
		add(engine.ImpactItem{Group: engine.ImpactKeeps, Subject: "ufw", Detail: fmt.Sprintf("UFW is on already: its %s %s, and keelbox adds its own for %s", plural(inv.UFW.Rules, "rule", "rules"), verb(inv.UFW.Rules, "stays", "stay"), own)})
	default:
		state := "off"
		if inv.UFW.Active {
			state = "on but lets everything in"
		}
		add(engine.ImpactItem{Group: engine.ImpactChanges, Subject: "ufw", Detail: "UFW is " + state + " now; keelbox turns it on and refuses inbound connections except SSH, the tailnet and host.firewall.allow"})
		known := serviceGroups{}
		for _, l := range inv.Listeners {
			if l.Published() {
				continue // published by Docker, which bypasses UFW: runtime's
			}
			if known.add(c, inv, l, false) {
				continue
			}
			if a, ok := allowed(c, l.Port, l.Proto); ok {
				add(engine.ImpactItem{Group: engine.ImpactKeeps, Subject: l.Key(), Detail: fmt.Sprintf("%s stays open to %s (host.firewall.allow %s)", l.Label, fromWhere(a), a.Label())})
				continue
			}
			if inv.UFW.Admit(l.Port, l.Proto) {
				add(engine.ImpactItem{Group: engine.ImpactKeeps, Subject: l.Key(), Detail: l.Label + " stays reachable: one of UFW's existing rules admits it"})
				continue
			}
			add(engine.ImpactItem{Group: engine.ImpactCuts, Subject: l.Key(), Gate: true, Keep: keepEntry(l.Name, l.Port, l.Proto),
				Detail: l.Label + " listens on the network now; the firewall would block it from the LAN (the tailnet still reaches it)"})
		}
		out = append(out, known.items("")...)
	}

	_, sshdChanges := in.Pending("host", "sshd-hardening")
	admin, agent := c.Users.Admin.Name, c.Users.Agent.Name
	cloud := map[string]bool{}
	if c.LockCloudUser() {
		for _, u := range CloudUsers(e) {
			if locked, err := shadowLocked(e, u); err == nil && !locked {
				cloud[u] = true
			}
		}
	}
	for _, a := range inv.Accounts {
		switch {
		case a.UID == 0:
			if sshdChanges && inv.SSHD.PermitRootLogin != "no" && inv.SSHD.PermitRootLogin != "" {
				add(engine.ImpactItem{Group: engine.ImpactChanges, Subject: "root", Detail: "root can no longer log in over SSH; log in as " + admin + " and use sudo"})
			}
		case a.Name == admin:
			add(engine.ImpactItem{Group: engine.ImpactChanges, Subject: a.Name, Detail: "exists: keelbox keeps it and its files, gives it sudo without a password and keeps your keys in a marked block of its authorized_keys"})
		case a.Name == agent:
			var lose []string
			if a.In("sudo") && !c.AgentSudo() {
				lose = append(lose, "sudo")
			}
			if a.In("docker") && c.Docker() {
				lose = append(lose, "docker")
			}
			it := engine.ImpactItem{Group: engine.ImpactChanges, Subject: a.Name, Detail: "exists: keelbox keeps it and its files and makes it the agent account"}
			if len(lose) > 0 {
				it.Group, it.Gate = engine.ImpactCuts, true
				it.Detail += ", which leaves the " + strings.Join(lose, " and ") + " group"
				if len(lose) > 1 {
					it.Detail += "s"
				}
				it.Hint = "name another account in users.agent.name, or keep sudo with users.agent.permissions: full"
			}
			add(it)
		case cloud[a.Name]:
			add(engine.ImpactItem{Group: engine.ImpactCuts, Subject: a.Name, Hint: "users.lock_cloud_user: false keeps it",
				Detail: "the image's default account is locked (SSH and sudo) once " + admin + " logs in with your key"})
		case a.Password && inv.SSHD.PasswordAuth && sshdChanges && logsInWithKey(e, inv, a):
			// A key works for it already (keelbox may be logged in as it
			// with one): turning passwords off cuts nothing off.
			add(engine.ImpactItem{Group: engine.ImpactChanges, Subject: a.Name,
				Detail: "logs in over SSH with a key; keelbox turns SSH password logins off, and its password still works for sudo and at the console"})
		case a.Password && inv.SSHD.PasswordAuth && sshdChanges:
			add(engine.ImpactItem{Group: engine.ImpactCuts, Subject: a.Name, Gate: true, Hint: "put their public key in ~" + a.Name + "/.ssh/authorized_keys first",
				Detail: "logs in over SSH with a password now; keelbox allows keys only, and the password still works for sudo and at the console"})
		default:
			keeps := "keeps its password, groups and files"
			if a.In("sudo") {
				keeps = "keeps its password, sudo and files"
			}
			add(engine.ImpactItem{Group: engine.ImpactKeeps, Subject: a.Name, Detail: keeps})
		}
	}
	if sshdChanges {
		add(engine.ImpactItem{Group: engine.ImpactRestarts, Subject: "sshd", Detail: "reloads with keys only: no password or root logins (checked with sshd -t first)"})
	}
	if lan := lanSSHClient(e); lan != "" && c.PublicSSHMode() == config.PublicSSHAuto && c.Tailscale() {
		add(engine.ImpactItem{Group: engine.ImpactCuts, Subject: "ssh", KeepSSH: string(config.PublicSSHLAN),
			Detail: "you are connected from " + lan + " on the LAN; SSH from outside the tailnet, the LAN included, closes once keelbox reaches the box over Tailscale"})
	}
	if _, ok := in.Pending("host", "hostname"); ok && inv.Hostname != "" && inv.Hostname != c.Host.Hostname {
		d := inv.Hostname + " becomes " + c.Host.Hostname
		for _, l := range inv.Listeners {
			if l.Process == "smbd" || l.Process == "nmbd" {
				d += "; Samba's network name follows it"
				break
			}
		}
		add(engine.ImpactItem{Group: engine.ImpactChanges, Subject: "hostname", Detail: d, Hint: "host.hostname: " + inv.Hostname + " keeps it"})
	}
	if c.RebootsItself() {
		when := "at night"
		if c.Host.AutoReboot == config.AutoRebootWeekly {
			when = "on Sunday night"
		}
		add(engine.ImpactItem{Group: engine.ImpactChanges, Subject: "reboots",
			Detail: "the machine reboots itself " + when + " (01:00 to 06:00 " + c.Host.Timezone + ") for an update that needs it, once the agent and logged-in users have been idle for 30 minutes; other accounts' jobs, services and containers do not count",
			Hint:   "host.auto_reboot: never leaves reboots to you: keelbox reboot " + c.Box})
	}
	out = append(out, netLimitsImpact(ctx, e, in)...)
	out = append(out, limitsImpact(ctx, e, in)...)
	return append(out, sleepImpact(ctx, e, in)...)
}

// lanSSHClient is the client address of the SSH session this run came over
// when it is on the LAN rather than the tailnet: the path auto closes.
func lanSSHClient(e *engine.Env) string {
	client, _, ok := parseSSHConnection(e.SSHConnection)
	if !ok {
		return ""
	}
	a, err := netip.ParseAddr(client)
	if err != nil || inventory.IsTailnet(a) || (!a.IsPrivate() && !a.IsLinkLocalUnicast()) {
		return ""
	}
	return client
}

// Impact: the system Docker daemon, its containers and what reaches them.
func (Runtime) Impact(ctx context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	c, inv := e.Cfg, in.Inv
	if !c.Docker() {
		if inv.Docker.Running {
			return []engine.ImpactItem{{Group: engine.ImpactKeeps, Subject: "docker", Detail: "runtime.docker is false: keelbox leaves Docker and its containers alone, and the agent has no Docker"}}
		}
		return nil
	}
	var out []engine.ImpactItem
	add := func(it engine.ImpactItem) { out = append(out, it) }
	var back, stay []string
	for _, ct := range inv.Docker.Containers {
		if !ct.Running {
			continue
		}
		if ct.ComesBack() {
			back = append(back, ct.Name)
		} else {
			stay = append(stay, ct.Name)
		}
	}
	running := len(back) + len(stay)
	stops := ""
	if running > 0 {
		all := append(append([]string(nil), back...), stay...)
		sort.Strings(all)
		stops = fmt.Sprintf("; its %s (%s) %s", plural(running, "running container", "running containers"), strings.Join(all, ", "), verb(running, "stops and is left stopped", "stop and are left stopped"))
	}
	if d, ok := in.Pending("runtime", "docker-conflicts"); ok {
		pkgs := strings.TrimSuffix(strings.TrimPrefix(d, "remove "), " (needs --adopt)")
		add(engine.ImpactItem{Group: engine.ImpactReplaces, Subject: "docker", Gate: true, Hint: "runtime.docker: false leaves Docker alone",
			Detail: pkgs + " is replaced by Docker Engine from Docker's repository" + stops + "; images and volumes are kept"})
		return append(out, dockerPorts(c, inv)...)
	}
	var why []string
	if d, ok := in.Pending("runtime", "docker-packages"); ok && strings.HasPrefix(d, "move ") {
		add(engine.ImpactItem{Group: engine.ImpactReplaces, Subject: "docker", Gate: true, Hint: "runtime.docker: false leaves Docker alone",
			Detail: strings.TrimPrefix(d, "move ") + " (the release's tested series), which restarts Docker"})
		why = append(why, "for the new version")
	}
	if _, ok := in.Pending("runtime", "docker-daemon-json"); ok {
		why = append(why, "to bound container logs (daemon.json)")
	}
	restart := strings.Join(why, " and ")
	if restart != "" && inv.Docker.Running {
		d := "the system daemon restarts " + restart
		if len(back) > 0 {
			d += ": " + strings.Join(back, ", ") + " " + verb(len(back), "comes", "come") + " back (restart policy)"
		}
		it := engine.ImpactItem{Group: engine.ImpactRestarts, Subject: "docker", Detail: d}
		if len(stay) > 0 {
			sep := ": "
			if len(back) > 0 {
				sep = "; "
			}
			it.Detail += sep + strings.Join(stay, ", ") + " " + verb(len(stay), "has no restart policy and stays stopped", "have no restart policy and stay stopped")
			it.Gate = true
			it.Hint = "docker update --restart unless-stopped " + strings.Join(stay, " ") + " brings them back"
		}
		add(it)
	}
	if inv.Docker.Running {
		add(engine.ImpactItem{Group: engine.ImpactKeeps, Subject: "docker", Detail: fmt.Sprintf("the system Docker daemon and its %s stay; the agent gets its own rootless Docker and cannot see them",
			plural(len(inv.Docker.Containers), "container", "containers"))})
	}
	return append(out, dockerPorts(c, inv)...)
}

// dockerPorts: a container published on the network is reachable from the
// LAN now (Docker's forwarding bypasses UFW); keelbox's guard drops those
// connections unless host.firewall.allow names the port.
func dockerPorts(c *config.Config, inv *inventory.Inventory) []engine.ImpactItem {
	var out []engine.ImpactItem
	seen := map[string]bool{}
	known := serviceGroups{}
	for _, ct := range inv.Docker.Containers {
		if !ct.Running {
			continue
		}
		for _, p := range ct.Ports {
			key := fmt.Sprintf("%d/%s", p.Port, p.Proto)
			if seen[key] || !inventory.Reachable(p.HostIP) {
				continue
			}
			seen[key] = true
			l := inventory.Listener{Port: p.Port, Proto: p.Proto, Container: ct.Name, Image: ct.Image, Service: ct.Service}
			if s, ok := services.Lookup(ct.Service); ok && ct.Service != "" {
				l.Label = s.Title + " (container " + ct.Name + ")"
			}
			if known.add(c, inv, l, true) {
				continue
			}
			label := "container " + ct.Name + " (Docker)"
			if a, ok := allowed(c, p.Port, p.Proto); ok {
				out = append(out, engine.ImpactItem{Group: engine.ImpactKeeps, Module: "runtime", Subject: key, Detail: fmt.Sprintf("%s stays open to %s (host.firewall.allow %s)", label, fromWhere(a), a.Label())})
				continue
			}
			out = append(out, engine.ImpactItem{Group: engine.ImpactCuts, Module: "runtime", Subject: key, Gate: true, Keep: keepEntry(inventory.AllowName(ct.Name, p.Port), p.Port, p.Proto),
				Detail: label + " is published to the network now; keelbox's Docker guard would drop connections from the LAN (the tailnet still reaches it)"})
		}
	}
	return append(out, known.items("runtime")...)
}

// serviceGroups gathers the ports of each known service (internal/services)
// found listening, so the report says "UniFi" once rather than one line per
// port, and keeps it with {service: unifi}. Ports a service uses that its
// preset does not list are reported one by one, as for anything else.
type serviceGroups struct {
	order []string
	by    map[string]*serviceGroup
}

type serviceGroup struct {
	svc                 services.Service
	label               string
	docker              bool
	cut, kept, admitted []string
	keptBy, keptFrom    string
}

// add takes the listener if it is a known service's port, and says how the
// firewall (or, for docker, the guard) would treat it.
func (g *serviceGroups) add(c *config.Config, inv *inventory.Inventory, l inventory.Listener, docker bool) bool {
	s, ok := services.Lookup(l.Service)
	if !ok || l.Service == "" || !s.Has(l.Port, l.Proto) {
		return false
	}
	if g.by == nil {
		g.by = map[string]*serviceGroup{}
	}
	sg, ok := g.by[s.Name]
	if !ok {
		label := l.Label
		if label == "" {
			label = s.Title
		}
		sg = &serviceGroup{svc: s, label: label, docker: docker}
		g.by[s.Name] = sg
		g.order = append(g.order, s.Name)
	}
	switch a, ok := allowed(c, l.Port, l.Proto); {
	case ok:
		sg.kept, sg.keptBy, sg.keptFrom = append(sg.kept, l.Key()), a.Label(), fromWhere(a)
	case !docker && inv.UFW.Admit(l.Port, l.Proto):
		sg.admitted = append(sg.admitted, l.Key())
	default:
		sg.cut = append(sg.cut, l.Key())
	}
	return true
}

// items is one impact item per service: cut off if any of its ports would
// be, kept otherwise.
func (g *serviceGroups) items(module string) []engine.ImpactItem {
	var out []engine.ImpactItem
	for _, name := range g.order {
		sg := g.by[name]
		s := sg.svc
		for _, xs := range [][]string{sg.cut, sg.kept, sg.admitted} {
			presetOrder(s, xs)
		}
		if len(sg.cut) == 0 {
			d := sg.label + " stays reachable: UFW's existing rules admit " + listAnd(sg.admitted)
			if len(sg.kept) > 0 {
				d = sg.label + " stays open to " + sg.keptFrom + " (host.firewall.allow " + sg.keptBy + ")"
			}
			out = append(out, engine.ImpactItem{Group: engine.ImpactKeeps, Module: module, Subject: s.Name, Ports: append(sg.kept, sg.admitted...), Detail: d})
			continue
		}
		listening := map[string]bool{}
		for _, k := range append(append(append([]string(nil), sg.cut...), sg.kept...), sg.admitted...) {
			listening[k] = true
		}
		var also []string
		for _, p := range s.Ports {
			if !listening[p.Key()] {
				also = append(also, p.Key())
			}
		}
		d := sg.label + " listens on " + listAnd(sg.cut) + " now; the firewall would block " + verb(len(sg.cut), "it", "them") + " from the LAN (the tailnet still reaches it)"
		if sg.docker {
			d = sg.label + " publishes " + listAnd(sg.cut) + " to the network now; keelbox's Docker guard would drop connections from the LAN (the tailnet still reaches it)"
		}
		hint := "{service: " + s.Name + "} keeps it"
		if len(also) > 0 {
			hint += ", and opens " + listAnd(also) + " as well, which " + s.Title + " lists for the LAN"
		}
		if n := s.DiscoveryNote(); n != "" {
			hint += "; " + n
		}
		out = append(out, engine.ImpactItem{Group: engine.ImpactCuts, Module: module, Subject: s.Name, Gate: true, Ports: sg.cut,
			Keep: &config.Allow{Service: s.Name}, Detail: d, Hint: hint})
	}
	return out
}

// presetOrder sorts "8096/tcp" keys in the order the preset lists the
// ports, whatever order the machine reported them in.
func presetOrder(s services.Service, keys []string) {
	rank := func(k string) int {
		port, proto, _ := strings.Cut(k, "/")
		n, _ := strconv.Atoi(port)
		for i, p := range s.Ports {
			if p.Proto == proto && n >= p.First && n <= p.Last {
				return i
			}
		}
		return len(s.Ports)
	}
	sort.SliceStable(keys, func(i, j int) bool { return rank(keys[i]) < rank(keys[j]) })
}

// listAnd is "a", "a and b", "a, b and c".
func listAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// Impact: tmux servers and agent CLIs already running.
func (Sessions) Impact(ctx context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	by := map[string][]string{}
	for _, r := range in.Inv.Running {
		by[r.User] = append(by[r.User], r.Program)
	}
	users := make([]string, 0, len(by))
	for u := range by {
		users = append(users, u)
	}
	sort.Strings(users)
	var out []engine.ImpactItem
	for _, u := range users {
		progs := strings.Join(by[u], " and ")
		if len(by[u]) == 1 {
			progs += " runs"
		} else {
			progs += " run"
		}
		them, they := verb(len(by[u]), "it", "them"), verb(len(by[u]), "It runs", "They run")
		d := progs + " as " + u + " now: keelbox leaves " + them + " running. " + they + " as " + u + ", not the agent account, so keelbox's limits on the agent do not apply"
		if u == e.Cfg.Users.Agent.Name {
			d = progs + " as " + u + " now and " + verb(len(by[u]), "keeps", "keep") + " running; keelbox's tmux settings apply to new sessions"
		}
		out = append(out, engine.ImpactItem{Group: engine.ImpactKeeps, Subject: u, Detail: d})
	}
	return out
}

func verb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Impact: doctor on a schedule, which raises events about the machine.
func (Doctor) Impact(ctx context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	if _, ok := in.Pending("doctor", "doctor-service-unit"); !ok {
		return nil
	}
	return []engine.ImpactItem{{Group: engine.ImpactChanges, Subject: "doctor",
		Detail: "keelbox checks the box " + e.Cfg.DoctorSchedule() + " and raises an event when a check starts or stops failing",
		Hint:   "doctor.schedule: off turns it off"}}
}

// Impact: where the box's events go.
func (Notify) Impact(ctx context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	chans := e.Cfg.Notify.Channels
	if len(chans) == 0 {
		return nil
	}
	if _, ok := in.Pending("notify", "notify-secret-dir"); !ok {
		return nil
	}
	var names []string
	for _, ch := range chans {
		names = append(names, ch.Name+" ("+ch.Type+")")
	}
	return []engine.ImpactItem{{Group: engine.ImpactChanges, Subject: "notify",
		Detail: "events from this box (doctor, agents, reboots) go to " + strings.Join(names, ", ")}}
}

// logsInWithKey reports whether an account can log in with a key now: sshd
// takes keys and its authorized keys files hold one, or keelbox itself is
// running from its SSH session, which logs in with keys only.
func logsInWithKey(e *engine.Env, inv *inventory.Inventory, a inventory.Account) bool {
	if e.SSHConnection != "" && e.Facts.InvokingUser == a.Name {
		return true
	}
	return inv.SSHD.PubkeyAuth && a.Keys > 0
}
