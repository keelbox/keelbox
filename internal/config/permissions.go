package config

import "strings"

// users.agent.permissions: how much the agent account may change the box
// itself (docs/adr/0074-agent-permissions.md).
const (
	// PermissionsManual: the agent changes nothing outside its home.
	// keelbox install records a request, and the owner approves it from
	// their computer (keelbox approve BOX).
	PermissionsManual = "manual"
	// PermissionsAuto, the default: keelbox install installs Ubuntu
	// packages through a root service that checks every request. The agent
	// has no sudo.
	PermissionsAuto = "auto"
	// PermissionsFull: the agent has passwordless sudo, and none of the
	// agent limits bind it.
	PermissionsFull = "full"
)

// PermissionModes lists the values users.agent.permissions takes.
var PermissionModes = []string{PermissionsManual, PermissionsAuto, PermissionsFull}

// ValidPermissions checks a users.agent.permissions value.
func ValidPermissions(s string) bool { return contains(PermissionModes, s) }

// AgentPermissions resolves users.agent.permissions. Without the key the
// mode is auto, or full when users.agent.sudo, another way to write it, is true.
func (c *Config) AgentPermissions() string {
	switch p := c.Users.Agent.Permissions; {
	case p != "":
		return p
	case On(c.Users.Agent.Sudo, false):
		return PermissionsFull
	}
	return PermissionsAuto
}

// AgentSudo reports whether the agent has passwordless sudo: only with
// permissions full.
func (c *Config) AgentSudo() bool { return c.AgentPermissions() == PermissionsFull }

// AgentInstalls reports whether a package the agent asks for is installed
// without the owner's approval: with auto, and with full, where the agent
// could install it with sudo anyway.
func (c *Config) AgentInstalls() bool { return c.AgentPermissions() != PermissionsManual }

func (c *Config) validatePermissions(add func(string, ...any)) {
	p, sudo := c.Users.Agent.Permissions, c.Users.Agent.Sudo
	if p != "" && !ValidPermissions(p) {
		add("users.agent.permissions: %q is not one of %s", p, strings.Join(PermissionModes, ", "))
		return
	}
	if p == "" || sudo == nil {
		return
	}
	// users.agent.sudo: true is another way to write permissions: full.
	switch {
	case *sudo && p != PermissionsFull:
		add("users.agent.sudo: true and users.agent.permissions: %s disagree (sudo: true is another way to write permissions: full); remove users.agent.sudo, or set users.agent.permissions: full", p)
	case !*sudo && p == PermissionsFull:
		add("users.agent.sudo: false and users.agent.permissions: full disagree; remove users.agent.sudo to keep full, or set users.agent.permissions: auto")
	}
}
