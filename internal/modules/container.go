package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// keelbox sets up an unprivileged LXC container (LXD, Incus, Proxmox; ADR
// 0091), but two of the container's own settings decide whether parts of
// the box can work, and neither can be changed from inside it (ADR 0092):
//
//   - nesting: Docker (the system daemon and the agent's rootless one)
//     and the agent CLIs' sandboxes (bwrap) mount /proc in a new user
//     namespace, which an LXC container without nesting refuses. LXD and Incus leave it off by default.
//   - /dev/net/tun: tailscaled needs the TUN device, which Proxmox does not
//     pass to an unprivileged container by default.
//
// Apply checks both before it changes anything, and doctor checks them on
// every run. Outside an LXC container neither check runs.

// ContainerLimit is one container setting that stops part of the box.
type ContainerLimit struct {
	Check   string // doctor's name for it
	Problem string // what it stops, in a phrase
}

// The container managers keelbox names fixes for. A guest API socket says
// which one runs the container; without one it may be Proxmox, or LXD or
// Incus with that API turned off, or plain LXC.
const (
	hostUnknown = iota
	hostLXD
	hostIncus
	hostLXDOrIncus // both sockets: not Proxmox, but not which of the two
)

// lxcHost is the container's manager and its name there.
type lxcHost struct {
	kind int
	name string // the container's name, or NAME when it cannot be known
}

// lxcContainer reports whether the box is an LXC container, and on which
// kind of host. systemd-detect-virt prints "lxc" in LXC, LXD, Incus and
// Proxmox containers, "none" outside a container.
func lxcContainer(ctx context.Context, e *engine.Env) (lxcHost, bool) {
	out, _ := system.Output(ctx, e.X, system.Cmd{Name: "systemd-detect-virt", Args: []string{"--container"}, Timeout: 10 * time.Second})
	if out != "lxc" {
		return lxcHost{}, false
	}
	// The directory of the guest API socket belongs to the host's root,
	// nobody in the container, so the directory itself is looked at, not
	// the socket in it (root's file calls refuse a path through a
	// directory another account owns).
	has := func(dir string) bool { st, err := e.X.Stat(dir); return err == nil && st.Exists }
	lxd, incus := has("/dev/lxd"), has("/dev/incus")
	h := lxcHost{}
	sock := ""
	switch {
	case lxd && incus:
		h.kind = hostLXDOrIncus
	case lxd:
		h.kind, sock = hostLXD, "/dev/lxd/sock"
	case incus:
		h.kind, sock = hostIncus, "/dev/incus/sock"
	}
	h.name = containerName(ctx, e, sock)
	return h, true
}

// containerName is the container's name as LXD or Incus knows it, which
// their commands need. keelbox renames the host to the box, so the
// hostname is the container's name only until the first apply. LXD's and
// Incus's cloud-init datasource (platform lxd) and their guest API's
// meta-data keep it; on another platform local_hostname is only the
// hostname cloud-init was given, so it gets the same check as the
// hostname. When none answers and the hostname is already the box's, it
// is NAME.
func containerName(ctx context.Context, e *engine.Env, sock string) string {
	if b, err := e.X.ReadFile("/run/cloud-init/instance-data.json"); err == nil {
		var d struct {
			V1 struct {
				LocalHostname string `json:"local_hostname"`
				Platform      string `json:"platform"`
			} `json:"v1"`
		}
		if json.Unmarshal(b, &d) == nil && labelName(d.V1.LocalHostname) && (d.V1.Platform == "lxd" || d.V1.LocalHostname != e.Cfg.Host.Hostname) {
			return d.V1.LocalHostname
		}
	}
	if sock != "" {
		// The guest API answers any host name; "guest" is a placeholder.
		out, err := system.Output(ctx, e.X, system.Cmd{Name: "curl", Args: []string{"-fsS", "--max-time", "5", "--unix-socket", sock, "http://guest/1.0/meta-data"}, Timeout: 10 * time.Second})
		if err == nil {
			for _, l := range strings.Split(out, "\n") {
				if v, found := strings.CutPrefix(strings.TrimSpace(l), "local-hostname:"); found && labelName(strings.TrimSpace(v)) {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	if b, err := e.X.ReadFile("/etc/hostname"); err == nil {
		if n := strings.TrimSpace(string(b)); labelName(n) && n != e.Cfg.Host.Hostname {
			return n
		}
	}
	return "NAME"
}

// labelName reports whether s can be printed as a container name in a
// command: letters, digits and hyphens, as LXD and Incus allow.
func labelName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	return strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") == ""
}

// nestingProbe does what Docker and bwrap do first: a new user,
// mount and PID namespace with /proc mounted in it. It leaves nothing
// behind; the namespaces end with the command.
var nestingProbe = system.Cmd{Name: "unshare", Args: []string{"--user", "--map-root-user", "--mount", "--pid", "--fork", "--mount-proc", "true"}, Timeout: 10 * time.Second}

// tunProbe checks /dev/net/tun is a character device and opens it for
// reading and writing, as tailscaled does, then closes it. Opening it
// creates no interface. A plain file in its place, which root could open,
// does not pass.
var tunProbe = system.Cmd{Name: "sh", Args: []string{"-c", "[ -c /dev/net/tun ] && : <>/dev/net/tun"}, Timeout: 10 * time.Second}

// nestingNeed is what needs nesting on this box, or "" when nothing does:
// Docker, system and rootless, and the agent CLIs' sandboxes.
func nestingNeed(e *engine.Env) string {
	switch docker, agents := e.Cfg.Docker(), len(e.Cfg.Agents) > 0; {
	case docker && agents:
		return "Docker and the agents' sandboxes"
	case docker:
		return "Docker"
	case agents:
		return "the agents' sandboxes"
	}
	return ""
}

// containerLimits lists the container settings that stop part of this box.
// It changes nothing.
func containerLimits(ctx context.Context, e *engine.Env) []ContainerLimit {
	var out []ContainerLimit
	if need := nestingNeed(e); need != "" && !system.Succeeds(ctx, e.X, nestingProbe) {
		out = append(out, ContainerLimit{Check: "container-nesting", Problem: "nesting is off (a new user namespace cannot mount /proc), so " + need + " cannot start"})
	}
	if e.Cfg.Tailscale() && !system.Succeeds(ctx, e.X, tunProbe) {
		out = append(out, ContainerLimit{Check: "container-tun", Problem: "/dev/net/tun cannot be opened, so Tailscale cannot start"})
	}
	return out
}

func hasLimit(ls []ContainerLimit, check string) bool {
	for _, l := range ls {
		if l.Check == check {
			return true
		}
	}
	return false
}

// tool is the command for an LXD or Incus host.
func (h lxcHost) tool() string {
	if h.kind == hostIncus {
		return "incus"
	}
	return "lxc"
}

// managerFix is the commands for LXD or Incus.
func (h lxcHost) managerFix(ls []ContainerLimit) []string {
	t := h.tool()
	var out []string
	if hasLimit(ls, "container-nesting") {
		out = append(out, t+" config set "+h.name+" security.nesting=true")
	}
	if hasLimit(ls, "container-tun") {
		out = append(out, t+" config device add "+h.name+" tun unix-char path=/dev/net/tun")
	}
	return append(out, t+" restart "+h.name)
}

// proxmoxFix is the commands for Proxmox. pct set replaces the whole
// features list and a whole devN entry, so the user reads them first and
// adds to them.
func proxmoxFix(ls []ContainerLimit) []string {
	out := []string{"pct config CTID | grep -E '^(features|dev[0-9])'"}
	if hasLimit(ls, "container-nesting") {
		out = append(out, "pct set CTID --features nesting=1,keyctl=1    # keep the features listed above, comma-separated")
	}
	if hasLimit(ls, "container-tun") {
		out = append(out, "pct set CTID --dev1 /dev/net/tun    # a devN not listed above; Proxmox 8.1 or later")
	}
	return append(out, "pct reboot CTID")
}

// CheckContainer refuses to apply to an LXC container whose settings stop
// part of the box, and names the commands that change them on its host.
func CheckContainer(ctx context.Context, e *engine.Env) error {
	h, in := lxcContainer(ctx, e)
	if !in {
		return nil
	}
	ls := containerLimits(ctx, e)
	if len(ls) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s is an LXC container, and nothing has changed:\n", e.Cfg.Box)
	for _, l := range ls {
		fmt.Fprintf(&b, "  - %s\n", l.Problem)
	}
	block := func(lines []string) {
		for _, l := range lines {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	nameNote := ""
	if h.name == "NAME" {
		nameNote = ", where NAME is the container's name"
	}
	switch h.kind {
	case hostLXD, hostIncus:
		product := map[int]string{hostLXD: "LXD", hostIncus: "Incus"}[h.kind]
		fmt.Fprintf(&b, "Change them on the %s host%s:\n", product, nameNote)
		block(h.managerFix(ls))
	case hostLXDOrIncus:
		fmt.Fprintf(&b, "Change them on the LXD host, or with incus for Incus%s:\n", nameNote)
		block(h.managerFix(ls))
	default:
		b.WriteString("Change them on the container's host. On Proxmox, where CTID is the container's number:\n")
		block(proxmoxFix(ls))
		fmt.Fprintf(&b, "On LXD, or with incus for Incus%s:\n", nameNote)
		block(h.managerFix(ls))
	}
	fmt.Fprintf(&b, "Then: keelbox apply %s", e.Cfg.Box)
	return fmt.Errorf("%s", b.String())
}

// verifyContainer is doctor's view of the same checks: a line for each in
// an LXC container, nothing elsewhere.
func verifyContainer(ctx context.Context, e *engine.Env) []engine.Finding {
	h, in := lxcContainer(ctx, e)
	if !in {
		return nil
	}
	ls := containerLimits(ctx, e)
	type check struct{ name, good string }
	var checks []check
	if nestingNeed(e) != "" {
		checks = append(checks, check{"container-nesting", "LXC container with nesting on"})
	}
	if e.Cfg.Tailscale() {
		checks = append(checks, check{"container-tun", "/dev/net/tun opens"})
	}
	var out []engine.Finding
	for _, c := range checks {
		var l *ContainerLimit
		for i := range ls {
			if ls[i].Check == c.name {
				l = &ls[i]
			}
		}
		if l == nil {
			out = append(out, ok("host", c.name, c.good))
			continue
		}
		// On LXD or Incus with a known name the commands fit on the line;
		// otherwise apply prints them, for Proxmox and LXD both.
		detail := l.Problem + "; keelbox apply " + e.Cfg.Box + " prints the commands for the container's host"
		if (h.kind == hostLXD || h.kind == hostIncus) && h.name != "NAME" {
			detail = l.Problem + "; on the container's host: " + strings.Join(h.managerFix([]ContainerLimit{*l}), "; then ")
		}
		out = append(out, fail("host", c.name, detail))
	}
	return out
}
