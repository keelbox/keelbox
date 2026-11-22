package config

import (
	"net"
	"path"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// PublicSSHMode says when public SSH is closed (docs/adr/0021-close-public-ssh.md).
type PublicSSHMode string

// Public SSH modes.
const (
	// PublicSSHAuto closes public SSH once keelbox has reached the box's
	// sshd over the tailnet from the machine running it, and opens it again
	// when that proof no longer holds. The default.
	PublicSSHAuto PublicSSHMode = "auto"
	// PublicSSHOpen keeps 22/tcp open to the internet.
	PublicSSHOpen PublicSSHMode = "open"
	// PublicSSHClosed closes public SSH as soon as it is proven over the
	// tailnet, keeps it closed while the tailnet is down, and makes doctor
	// fail until it is closed.
	PublicSSHClosed PublicSSHMode = "closed"
	// PublicSSHLAN is auto for the internet, while the LAN (LANRanges)
	// keeps SSH: a home server stays reachable from the desktop beside it.
	PublicSSHLAN PublicSSHMode = "lan"
)

// UnmarshalYAML reads auto, lan, open or closed, and the bools of earlier
// releases: true is open, false is closed.
func (m *PublicSSHMode) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!bool" {
		var b bool
		if err := n.Decode(&b); err != nil {
			return err
		}
		*m = PublicSSHOpen
		if !b {
			*m = PublicSSHClosed
		}
		return nil
	}
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	*m = PublicSSHMode(s)
	return nil
}

// PublicSSHMode returns host.firewall.public_ssh with its default.
func (c *Config) PublicSSHMode() PublicSSHMode {
	if c.Host.Firewall.PublicSSH == "" {
		return PublicSSHAuto
	}
	return c.Host.Firewall.PublicSSH
}

// TailnetHostPort splits target.ssh.tailnet_host into host and port; the
// port defaults to target.ssh.port. Empty when unset.
func (c *Config) TailnetHostPort() (string, int) {
	h := c.Target.SSH.TailnetHost
	if h == "" {
		return "", 0
	}
	if host, p, err := net.SplitHostPort(h); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			return host, n
		}
	}
	return strings.Trim(h, "[]"), c.Target.SSH.Port
}

// RemoteControl runs Claude Code Remote Control (`claude remote-control`,
// server mode) as a user service for the agent, so the Claude app and
// claude.ai/code reach sessions on the box (docs/adr/0023-remote-control.md).
type RemoteControl struct {
	Enabled *bool `yaml:"enabled,omitempty"`
	// Directory is where the server runs and its sessions start, relative
	// to the agent's home. Default projects. Not the home itself: Claude
	// Code never saves workspace trust there.
	Directory string `yaml:"directory,omitempty"`
	// Spawn is how the server starts sessions: same-dir or worktree.
	Spawn string `yaml:"spawn,omitempty"`
}

// RemoteControl reports whether Claude Code Remote Control is managed.
func (c *Config) RemoteControl() bool { return On(c.Sessions.RemoteControl.Enabled, false) }

// RemoteControlDir is sessions.remote_control.directory with its default.
func (c *Config) RemoteControlDir() string {
	if d := c.Sessions.RemoteControl.Directory; d != "" {
		return d
	}
	return "projects"
}

// RemoteControlSpawn is sessions.remote_control.spawn with its default.
func (c *Config) RemoteControlSpawn() string {
	if s := c.Sessions.RemoteControl.Spawn; s != "" {
		return s
	}
	return "same-dir"
}

func (c *Config) validateReach(add func(string, ...any)) {
	switch c.PublicSSHMode() {
	case PublicSSHAuto, PublicSSHOpen, PublicSSHClosed, PublicSSHLAN:
	default:
		add("host.firewall.public_ssh: %q must be auto, lan, open or closed", string(c.Host.Firewall.PublicSSH))
	}
	if h := c.Target.SSH.TailnetHost; h != "" {
		host, port := c.TailnetHostPort()
		if host == "" || strings.ContainsAny(host, " \t\n@/") || strings.HasPrefix(host, "-") || port < 1 || port > 65535 {
			add("target.ssh.tailnet_host: %q must be a host name or address, optionally with :port", h)
		}
		if !c.Tailscale() {
			add("target.ssh.tailnet_host: set but access.tailscale is disabled")
		}
	}
	rc := c.Sessions.RemoteControl
	if rc.Enabled == nil && (rc.Directory != "" || rc.Spawn != "") {
		add("sessions.remote_control.enabled: set it to true to use the other remote_control keys")
	}
	if !c.RemoteControl() {
		return
	}
	if !c.HasAgent(AgentClaudeCode) {
		add("sessions.remote_control: needs claude-code in agents")
	}
	d := c.RemoteControlDir()
	if path.IsAbs(d) || path.Clean(d) != d || d == "." || strings.HasPrefix(d, "../") || d == ".." || !safeRelPath(d) {
		add("sessions.remote_control.directory: %q must be a clean path inside the agent's home, such as projects (not the home itself: Claude Code never saves trust there)", d)
	}
	switch c.RemoteControlSpawn() {
	case "same-dir", "worktree":
	default:
		add("sessions.remote_control.spawn: %q must be same-dir or worktree", c.RemoteControlSpawn())
	}
}

// safeRelPath allows letters, digits and . _ - / only: the directory lands
// in a unit file and in a shell command.
func safeRelPath(d string) bool {
	for _, r := range d {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/", r)
		if !ok {
			return false
		}
	}
	return true
}
