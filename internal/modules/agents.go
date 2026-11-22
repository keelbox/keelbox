package modules

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/system"
)

// Agents installs the agent CLIs with each vendor's own installer, as the
// agent, into its home, and runs Remote Control. The CLIs update as they
// would on any computer: Claude Code by itself, Codex when the user says
// yes to its prompt or runs codex update (docs/adr/0088-vendor-agent-clis.md).
// It never signs in: the user runs each CLI's own login on the box, and
// keelbox never reads, stores or proxies those credentials.
type Agents struct{}

func (Agents) Name() string { return "agents" }

// Enabled always: a box with an older agent store under /opt/keelbox/agents
// has it removed, whatever agents it has now.
func (Agents) Enabled(*config.Config) bool { return true }

// agentSpec describes one CLI's official install.
type agentSpec struct {
	id, bin, url, interp string
	env                  []string
}

var agentSpecs = map[string]agentSpec{
	config.AgentClaudeCode: {id: config.AgentClaudeCode, bin: "claude", url: "https://claude.ai/install.sh", interp: "bash"},
	config.AgentCodex: {
		id: config.AgentCodex, bin: "codex", url: "https://chatgpt.com/codex/install.sh", interp: "sh",
		env: []string{"CODEX_NON_INTERACTIVE=1"},
	},
}

// AgentBins are the commands of the agent CLIs keelbox installs, as a
// person types them: claude, codex.
func AgentBins() []string {
	return []string{agentSpecs[config.AgentClaudeCode].bin, agentSpecs[config.AgentCodex].bin}
}

func (a Agents) Steps(e *engine.Env) []engine.Step {
	// The vendors' installs come before the old store goes, so a box whose
	// installer download fails keeps the old binaries until one succeeds.
	var steps []engine.Step
	for _, id := range e.Cfg.Agents {
		if spec, ok := agentSpecs[id]; ok {
			steps = append(steps, agentStep(e.Cfg.Users.Agent.Name, spec))
		}
	}
	steps = append(steps, oldStoreSteps()...)
	if e.Cfg.RemoteControl() {
		steps = append(steps, remoteControlSteps(e)...)
	}
	return steps
}

// An older agent store and its nightly upgrade run, which ADR 0088
// replaced with the vendors' installs. Everything here is keelbox's own;
// with it gone the agent's PATH finds the vendors' installs.
var (
	oldStoreDirs  = []string{"/usr/local/lib/keelbox/agents", "/opt/keelbox/agents", "/var/lib/keelbox/agents", "/var/lib/keelbox/smoke"}
	oldStoreUnits = []string{"keelbox-agents.timer", "keelbox-agents.service"}
	oldSmokeUser  = "keelbox-smoke"
)

func oldStoreSteps() []engine.Step {
	var steps []engine.Step
	for _, u := range oldStoreUnits {
		steps = append(steps, removeRootUnitStep("old-"+u, u))
	}
	for _, dir := range oldStoreDirs {
		steps = append(steps, engine.Step{
			Name: "old-agent-store" + strings.ReplaceAll(dir, "/", "-"),
			Soft: true,
			Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
				st, err := e.X.Stat(dir)
				if err != nil || !st.Exists {
					return engine.Converged(), err
				}
				return engine.Pending("remove %s: the agent CLIs are now the vendors' own installs", dir), nil
			},
			Apply: func(ctx context.Context, e *engine.Env) error {
				if missing := missingAgentCLIs(ctx, e); len(missing) > 0 {
					return fmt.Errorf("kept %s, since %s did not install; the next keelbox apply %s tries again", dir, strings.Join(missing, " and "), e.Cfg.Box)
				}
				_, err := e.X.Run(ctx, system.C("rm", "-rf", "--", dir))
				return err
			},
		})
	}
	return append(steps, engine.Step{
		Name: "old-smoke-user",
		Soft: true,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if _, err := e.X.LookupUser(oldSmokeUser); err != nil {
				return engine.Converged(), nil
			}
			return engine.Pending("remove system user %s", oldSmokeUser), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			// A process of the account left running makes userdel fail.
			_, _ = e.X.Run(ctx, system.C("pkill", "-KILL", "-u", oldSmokeUser))
			_, err := e.X.Run(ctx, system.C("userdel", oldSmokeUser))
			return err
		},
	})
}

// missingAgentCLIs are the configured agent CLIs not yet on the agent's
// PATH.
func missingAgentCLIs(ctx context.Context, e *engine.Env) []string {
	var out []string
	for _, id := range e.Cfg.Agents {
		if spec, ok := agentSpecs[id]; ok && !onAgentPath(ctx, e, spec.bin) {
			out = append(out, spec.bin)
		}
	}
	return out
}

// onAgentPath reports whether bin resolves on the agent's PATH, wherever
// the vendor's installer chose to put it.
func onAgentPath(_ context.Context, e *engine.Env, bin string) bool {
	a := e.AgentUser()
	_, err := system.AccountLookPath(e.X.UserFS(a.Name), a.Home, bin)
	return err == nil
}

// agentStep installs a CLI. Both CLIs update themselves, so the check is
// that one is on the agent's PATH and runs, whatever its version: apply
// never fights the vendor's updater by downgrading. One that does not run
// (a cut-off self-update) is installed again.
// The step is soft: a changed vendor installer fails this agent, not the box.
func agentStep(user string, spec agentSpec) engine.Step {
	return engine.Step{
		Name: spec.id,
		Soft: true,
		User: user,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if onAgentPath(ctx, e, spec.bin) {
				if _, err := versionOf(ctx, e, e.AgentTool(spec.bin)); err == nil {
					return engine.Converged(), nil
				}
				return engine.Pending("reinstall %s for %s with %s: %s --version fails", spec.id, e.Cfg.Users.Agent.Name, spec.url, spec.bin), nil
			}
			return engine.Pending("install %s for %s with %s", spec.id, e.Cfg.Users.Agent.Name, spec.url), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			user := e.Cfg.Users.Agent.Name
			// The installer is downloaded to a file first, then run, so a
			// truncated download cannot execute half a script.
			dir, cleanup, err := tempDir(ctx, e, user)
			if err != nil {
				return err
			}
			defer cleanup()
			script := dir + "/install.sh"
			dl := curl(spec.url, script)
			dl.User = user
			if _, err := e.X.Run(ctx, dl); err != nil {
				return err
			}
			run := system.As(user, spec.interp, script)
			run.Env = spec.env
			if _, err := e.X.Run(ctx, run); err != nil {
				return fmt.Errorf("%s installer: %w", spec.id, err)
			}
			return nil
		},
	}
}

func (Agents) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	var out []engine.Finding
	for _, id := range e.Cfg.Agents {
		spec, found := agentSpecs[id]
		if !found {
			continue
		}
		if !onAgentPath(ctx, e, spec.bin) {
			out = append(out, fail("agents", id, spec.bin+" is not on "+e.Cfg.Users.Agent.Name+"'s PATH: keelbox apply "+e.Cfg.Box+" installs it"))
			continue
		}
		v, err := versionOf(ctx, e, e.AgentTool(spec.bin))
		if err != nil {
			out = append(out, fail("agents", id, spec.bin+" does not run for "+e.Cfg.Users.Agent.Name+" ("+plain.Line(err.Error())+"): keelbox apply "+e.Cfg.Box+" installs it again"))
			continue
		}
		out = append(out, ok("agents", id, plain.Line(v))) // the agent's own binary wrote it
	}
	if len(e.Cfg.Agents) > 0 {
		out = append(out, verifySandbox(ctx, e))
	}
	if slices.Contains(e.Cfg.Agents, "codex") {
		out = append(out, verifyCodexSandbox(ctx, e))
	}
	if e.Cfg.RemoteControl() {
		out = append(out, verifyRemoteControl(ctx, e))
	}
	return out
}
