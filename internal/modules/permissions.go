package modules

import (
	"context"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/system"
)

// Permissions sets up what users.agent.permissions needs besides the sudo
// rule the host module writes or removes
// (docs/adr/0074-agent-permissions.md): the root service that takes the
// agent's package requests, the path unit that starts it when the agent's
// request file changes, root's directory for what became of them, and a
// world-readable file that tells the agent's keelbox install which mode the
// box is in. The units are the same in manual, auto and full: the service
// reads the mode from the applied config at each run, so a changed mode
// takes effect with the apply that writes it.
type Permissions struct{}

func (Permissions) Name() string                { return "permissions" }
func (Permissions) Enabled(*config.Config) bool { return true }

// InstallCommand is what the install service runs.
const InstallCommand = installs.Command

var installService = rootService{
	Unit: installs.Service, Header: ". Takes the agent's package requests: checks each, then installs it or keeps it for the owner's approval.",
	Description: "keelbox install: the agent's package requests", Command: InstallCommand, DayTwo: true, Packages: true,
	// An install is minutes at most, but a stopped apt-get leaves dpkg
	// half-way: the limit is for a run that hangs, not a slow one.
	Timeout: "1h",
}

func installPathUnit(agentHome string) rootPath {
	return rootPath{
		Unit: installs.PathUnit, Header: ". Starts the install service when the agent's request file changes.",
		Description: "keelbox install: watch the agent's request file", Service: installs.Service,
		Modified: agentHome + "/" + installs.AgentFile,
	}
}

func (Permissions) Steps(e *engine.Env) []engine.Step {
	agent := e.AgentUser()
	steps := []engine.Step{
		engine.DirStep("installs-dir", installs.Dir, 0o755, "root", "root"),
		engine.FileStep("permissions-mode", engine.File{Path: installs.ModePath, Content: installs.ModeFile(e.Cfg), Mode: 0o644}),
		engine.DirStep("agent-requests-dir", agent.Home+"/"+installs.AgentDir, 0o700, agent.Name, agent.Name),
		serviceStep("install-service-unit", installService),
	}
	return append(steps, pathSteps("install-path", installPathUnit(agent.Home))...)
}

func (Permissions) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	const mod = "permissions"
	box := e.Cfg.Box
	var out []engine.Finding
	switch e.Cfg.AgentPermissions() {
	case config.PermissionsManual:
		out = append(out, ok(mod, "mode", "manual: keelbox install records a request; nothing is installed until keelbox approve "+box))
	case config.PermissionsAuto:
		out = append(out, ok(mod, "mode", "auto: keelbox install installs Ubuntu packages for the agent through root's install service; the agent has no sudo"))
	default:
		out = append(out, ok(mod, "mode", "full: the agent has passwordless sudo"),
			warn(mod, "agent-sudo", "users.agent.permissions is full: with sudo none of keelbox's limits on the agent bind. To restore them: users.agent.permissions: auto, then keelbox apply "+box))
	}
	st, _ := e.X.Stat(unitDir + installs.Service)
	mode, _ := e.X.ReadFile(installs.ModePath)
	switch {
	case !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", installs.PathUnit)):
		out = append(out, fail(mod, "request-service", installs.PathUnit+" is not active (systemctl status "+installs.PathUnit+"), so keelbox install reaches nothing; keelbox apply "+box+" starts it again"))
	case !st.Exists:
		out = append(out, fail(mod, "request-service", installs.Service+" is missing; keelbox apply "+box+" writes it again"))
	case string(mode) != string(installs.ModeFile(e.Cfg)):
		out = append(out, fail(mod, "request-service", installs.ModePath+" does not say "+e.Cfg.AgentPermissions()+"; keelbox apply "+box+" writes it again"))
	default:
		out = append(out, ok(mod, "request-service", installs.PathUnit+" active; "+installs.Service+" reads the agent's requests as the agent"))
	}
	return out
}
