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
//   - nesting: rootless Docker and the agent CLIs' sandboxes (bwrap) mount
//     /proc in a new user namespace, which LXC's AppArmor profile refuses
//     unless nesting is on. LXD and Incus leave it off by default.
//   - /dev/net/tun: tailscaled needs the TUN device, which Proxmox does not
//     pass to an unprivileged container by default.
//
// Apply checks both before it changes anything, and doctor checks them on
// every run. Outside an LXC container neither check runs.

// ContainerLimit is one container setting that stops part of the box.
type ContainerLimit struct {
	Check   string // doctor's name for it
	Problem string // what it stops, in a phrase
	Fix     []string
}

// lxcHost is the container manager, for naming the fix.
type lxcHost struct {
	tool    string // lxc, incus, or "" for Proxmox and other LXC hosts
	product string // LXD, Incus
	name    string // the container's name, as the host knows it
}

// lxcContainer reports whether the box is an LXC container, and on which
// kind of host. systemd-detect-virt prints "lxc" in LXC, LXD, Incus and
// Proxmox containers, "none" outside a container.
func lxcContainer(ctx context.Context, e *engine.Env) (lxcHost, bool) {
	out, _ := system.Output(ctx, e.X, system.Cmd{Name: "systemd-detect-virt", Args: []string{"--container"}, Timeout: 10 * time.Second})
	if out != "lxc" {
		return lxcHost{}, false
	}
	h := lxcHost{name: containerName(e)}
	// The directory of their guest API socket says which one runs it. It
	// belongs to the host's root, nobody in the container, so the directory
	// itself is looked at, not the socket in it (root's file calls refuse a
	// path through a directory another account owns).
	for _, g := range []struct{ tool, product, dir string }{
		{"lxc", "LXD", "/dev/lxd"},
		{"incus", "Incus", "/dev/incus"},
	} {
		if st, err := e.X.Stat(g.dir); err == nil && st.Exists {
			h.tool, h.product = g.tool, g.product
		}
	}
	return h, true
}

// containerName is the container's name as LXD or Incus knows it. Their
// cloud-init datasource records it as local_hostname, which stays when
// keelbox renames the host to the box; without it, the hostname, which they
// set from the name at launch.
func containerName(e *engine.Env) string {
	if b, err := e.X.ReadFile("/run/cloud-init/instance-data.json"); err == nil {
		var d struct {
			V1 struct {
				LocalHostname string `json:"local_hostname"`
			} `json:"v1"`
		}
		if json.Unmarshal(b, &d) == nil && labelName(d.V1.LocalHostname) {
			return d.V1.LocalHostname
		}
	}
	if b, err := e.X.ReadFile("/etc/hostname"); err == nil && labelName(strings.TrimSpace(string(b))) {
		return strings.TrimSpace(string(b))
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

// nestingProbe does what rootless Docker and bwrap do first: a new user,
// mount and PID namespace with /proc mounted in it. It leaves nothing
// behind; the namespaces end with the command.
var nestingProbe = system.Cmd{Name: "unshare", Args: []string{"--user", "--map-root-user", "--mount", "--pid", "--fork", "--mount-proc", "true"}, Timeout: 10 * time.Second}

// tunProbe checks /dev/net/tun is a character device and opens it for
// reading and writing, as tailscaled does, then closes it. Opening it
// creates no interface. A plain file in its place, which root could open,
// does not pass.
var tunProbe = system.Cmd{Name: "sh", Args: []string{"-c", "[ -c /dev/net/tun ] && : <>/dev/net/tun"}, Timeout: 10 * time.Second}

// ContainerLimits lists the container settings that stop part of this box:
// none outside an LXC container. It changes nothing.
func ContainerLimits(ctx context.Context, e *engine.Env) []ContainerLimit {
	h, ok := lxcContainer(ctx, e)
	if !ok {
		return nil
	}
	var out []ContainerLimit
	if !system.Succeeds(ctx, e.X, nestingProbe) {
		what := "the agents' sandboxes cannot start"
		if e.Cfg.Docker() {
			what = "rootless Docker and the agents' sandboxes cannot start"
		}
		out = append(out, ContainerLimit{Check: "container-nesting", Problem: "nesting is off, so " + what, Fix: h.nestingFix()})
	}
	if e.Cfg.Tailscale() && !system.Succeeds(ctx, e.X, tunProbe) {
		out = append(out, ContainerLimit{Check: "container-tun", Problem: "/dev/net/tun cannot be opened, so Tailscale cannot start", Fix: h.tunFix()})
	}
	return out
}

func (h lxcHost) nestingFix() []string {
	if h.tool != "" {
		return []string{h.tool + " config set " + h.name + " security.nesting=true"}
	}
	return []string{"pct set CTID --features nesting=1,keyctl=1"}
}

func (h lxcHost) tunFix() []string {
	if h.tool != "" {
		return []string{h.tool + " config device add " + h.name + " tun unix-char path=/dev/net/tun"}
	}
	return []string{"pct set CTID --dev0 /dev/net/tun"}
}

func (h lxcHost) restart() string {
	if h.tool != "" {
		return h.tool + " restart " + h.name
	}
	return "pct reboot CTID"
}

// CheckContainer refuses to apply to an LXC container whose settings stop
// part of the box, and names the commands that change them on its host.
func CheckContainer(ctx context.Context, e *engine.Env) error {
	limits := ContainerLimits(ctx, e)
	if len(limits) == 0 {
		return nil
	}
	h, _ := lxcContainer(ctx, e)
	var b strings.Builder
	fmt.Fprintf(&b, "%s is an LXC container, and nothing has changed:\n", e.Cfg.Box)
	for _, l := range limits {
		fmt.Fprintf(&b, "  - %s\n", l.Problem)
	}
	if h.tool != "" {
		fmt.Fprintf(&b, "Run these on the %s host, then keelbox apply %s again:\n", h.product, e.Cfg.Box)
	} else {
		fmt.Fprintf(&b, "On Proxmox, CTID is the container's number. Run these on the Proxmox host, then keelbox apply %s again:\n", e.Cfg.Box)
	}
	for _, l := range limits {
		for _, f := range l.Fix {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	fmt.Fprintf(&b, "  %s", h.restart())
	return fmt.Errorf("%s", b.String())
}

// verifyContainer is doctor's view of the same checks: a line for each in
// an LXC container, nothing elsewhere.
func verifyContainer(ctx context.Context, e *engine.Env) []engine.Finding {
	h, in := lxcContainer(ctx, e)
	if !in {
		return nil
	}
	failed := map[string]ContainerLimit{}
	for _, l := range ContainerLimits(ctx, e) {
		failed[l.Check] = l
	}
	checks := []struct{ name, good string }{{"container-nesting", "LXC container with nesting on"}}
	if e.Cfg.Tailscale() {
		checks = append(checks, struct{ name, good string }{"container-tun", "/dev/net/tun opens"})
	}
	var out []engine.Finding
	for _, c := range checks {
		l, bad := failed[c.name]
		if !bad {
			out = append(out, ok("host", c.name, c.good))
			continue
		}
		out = append(out, fail("host", c.name, l.Problem+"; on the container's host: "+strings.Join(append(l.Fix, h.restart()), ", then ")))
	}
	return out
}
