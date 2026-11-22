package modules

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	snap "github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

// The agent's git identity (docs/adr/0064-work-on-the-box.md). keelbox sets
// two keys of the agent's global git config with git itself, run as the
// agent: every other setting in ~/.gitconfig, the agent's or the owner's,
// stays as it is, and git decides which file holds them.

// gitTimeout bounds one git config call made as the agent.
const gitTimeout = 20 * time.Second

func agentGit(agent string, args ...string) system.Cmd {
	c := system.As(agent, "git", args...)
	c.Timeout = gitTimeout
	return c
}

// gitIdentitySteps set user.name and user.email from users.agent.git, and
// init.defaultBranch to main when nothing sets one. Without users.agent.git
// there are no steps: keelbox writes nothing.
func gitIdentitySteps(e *engine.Env) []engine.Step {
	name, email, ok := e.Cfg.AgentGit()
	if !ok {
		return nil
	}
	agent := e.Cfg.Users.Agent.Name
	want := [][2]string{{"user.name", name}, {"user.email", email}}
	return []engine.Step{
		{
			Name: "agent-git-identity",
			User: agent,
			Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
				for _, kv := range want {
					if got, _ := system.Output(ctx, e.X, agentGit(agent, "config", "--global", "--get", kv[0])); got != kv[1] {
						return engine.Pending("set %s's git %s", agent, kv[0]), nil
					}
				}
				return engine.Converged(), nil
			},
			Apply: func(ctx context.Context, e *engine.Env) error {
				for _, kv := range want {
					if _, err := e.X.Run(ctx, agentGit(agent, "config", "--global", kv[0], kv[1])); err != nil {
						return err
					}
				}
				return nil
			},
		},
		{
			// Only when unset, in any config git reads for the agent: a
			// default branch the agent or the owner chose stays.
			Name: "agent-git-default-branch",
			User: agent,
			Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
				if got, _ := system.Output(ctx, e.X, agentGit(agent, "config", "--get", "init.defaultBranch")); got != "" {
					return engine.Converged(), nil
				}
				return engine.Pending("set %s's git init.defaultBranch to main", agent), nil
			},
			Apply: func(ctx context.Context, e *engine.Env) error {
				_, err := e.X.Run(ctx, agentGit(agent, "config", "--global", "init.defaultBranch", "main"))
				return err
			},
		},
	}
}

// verifyGitIdentity warns when git has no name or email for the agent,
// whoever was to set them: git then refuses every commit.
func verifyGitIdentity(ctx context.Context, e *engine.Env) engine.Finding {
	agent := e.Cfg.Users.Agent.Name
	name, _ := system.Output(ctx, e.X, agentGit(agent, "config", "--get", "user.name"))
	email, _ := system.Output(ctx, e.X, agentGit(agent, "config", "--get", "user.email"))
	if name == "" || email == "" {
		return warn("runtime", "git-identity", fmt.Sprintf("no git identity: commits will fail. Add users.agent.git: {name: Your Name, email: you@example.com} "+
			"to the config, then keelbox apply %s", e.Cfg.Box))
	}
	if _, _, managed := e.Cfg.AgentGit(); !managed {
		return ok("runtime", "git-identity", "set on the box, not by keelbox")
	}
	return ok("runtime", "git-identity", "commits as the name and email in users.agent.git")
}

// verifyProjects is doctor's one finding about the agent's projects: a
// warning, never a failure, while any holds work that exists only on the
// box. It gives counts and no names: a project's name is the agent's to
// write, and a scheduled doctor's findings go out as notifications. It is
// marked flaky so that such a notification waits for two runs in a row:
// work in progress is uncommitted most of the day.
func verifyProjects(box string, p *snap.Projects) (engine.Finding, bool) {
	if p == nil {
		return engine.Finding{}, false
	}
	here := p.OnlyHere()
	if len(here) == 0 {
		return ok("runtime", "projects", p.Summary()), true
	}
	var norepo, unchecked, noremote, dirty, unpushed int
	for _, pr := range here {
		switch {
		case !pr.Repo:
			norepo++
		case pr.Unchecked:
			unchecked++
		case pr.NoRemote:
			noremote++
		case pr.Uncommitted > 0:
			dirty++
		default:
			unpushed++
		}
	}
	var parts []string
	for _, c := range []struct {
		n    int
		what string
	}{{norepo, "not a git repository"}, {noremote, "with no remote"}, {dirty, "with uncommitted changes"}, {unpushed, "with commits not pushed"}, {unchecked, "that could not be checked"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	of := fmt.Sprint(p.Total)
	if p.Unlisted() > 0 {
		of = fmt.Sprintf("the first %d of %d", len(p.List), p.Total)
	}
	return flaky(warn("runtime", "projects", fmt.Sprintf("%d of %s in %s hold work that exists only on this box (%s). "+
		"List them: keelbox status %s; then commit and push as the agent, or copy them off", len(here), of, p.Dir, strings.Join(parts, ", "), box))), true
}
