package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	snap "github.com/keelbox/keelbox/internal/snapshot"
)

const gitYAML = "  agent:\n    git: {name: Ada Lovelace, email: ada@example.com}\n"

func gitFinding(t *testing.T, e *engine.Env) engine.Finding {
	t.Helper()
	for _, f := range (Runtime{}).Verify(context.Background(), e) {
		if f.Check == "git-identity" {
			return f
		}
	}
	t.Fatal("no git-identity finding")
	return engine.Finding{}
}

func TestGitIdentity(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, gitYAML))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"user.name": "Ada Lovelace", "user.email": "ada@example.com", "init.defaultBranch": "main"} {
		if got := s.gitConfig["agent "+k]; got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	// Only as the agent, only these keys, and through git: nothing writes
	// ~/.gitconfig as a file.
	for _, c := range s.Commands() {
		if strings.Contains(c, "git config") && !strings.HasPrefix(c, "(as agent) git config ") {
			t.Errorf("git config not run as the agent: %s", c)
		}
	}
	for _, p := range s.Paths() {
		if strings.HasSuffix(p, ".gitconfig") {
			t.Errorf("keelbox wrote %s itself", p)
		}
	}
	if again, err := engine.Apply(ctx, e, All()); err != nil || len(again.Changes) != 0 {
		t.Errorf("second apply changed %v (%v)", again.Changes, err)
	}
	if f := gitFinding(t, e); f.Level != engine.LevelOK {
		t.Errorf("doctor with an identity: %+v", f)
	}
	// A changed identity is drift apply puts back; a default branch someone
	// chose stays.
	s.gitConfig["agent user.email"] = "other@example.com"
	s.gitConfig["agent init.defaultBranch"] = "trunk"
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if s.gitConfig["agent user.email"] != "ada@example.com" || s.gitConfig["agent init.defaultBranch"] != "trunk" {
		t.Errorf("after drift: %v", s.gitConfig)
	}
}

func TestGitIdentityUnset(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if len(s.gitConfig) != 0 || s.Ran("(as agent) git config --global user") || s.Ran("(as agent) git config --global init") {
		t.Errorf("git config written without users.agent.git: %v", s.gitConfig)
	}
	f := gitFinding(t, e)
	if f.Level != engine.LevelWarn || !strings.Contains(f.Detail, "no git identity: commits will fail") ||
		!strings.Contains(f.Detail, "users.agent.git: {name: Your Name, email: you@example.com}") || !strings.Contains(f.Detail, "keelbox apply "+e.Cfg.Box) {
		t.Errorf("doctor without an identity: %+v", f)
	}
	// An identity the agent set itself is enough.
	s.gitConfig["agent user.name"], s.gitConfig["agent user.email"] = "Bot", "bot@example.com"
	if f := gitFinding(t, e); f.Level != engine.LevelOK || !strings.Contains(f.Detail, "not by keelbox") {
		t.Errorf("doctor with the agent's own identity: %+v", f)
	}
}

// Doctor has one finding for the agent's projects: a warning with counts
// and the command that lists them, never a failure and never a name.
func TestDoctorProjects(t *testing.T) {
	if _, found := verifyProjects("acme", nil); found {
		t.Error("a finding without a probe")
	}
	clean := &snap.Projects{Dir: "~/projects", Total: 1, List: []snap.Project{{Name: "site", Repo: true}}}
	if f, _ := verifyProjects("acme", clean); f.Level != engine.LevelOK || f.Detail != "1 in ~/projects, all clean and pushed" {
		t.Errorf("clean: %+v", f)
	}
	p := &snap.Projects{Dir: "~/projects", Total: 6, List: []snap.Project{
		{Name: "secret-name"}, {Name: "b", Repo: true, NoRemote: true, Uncommitted: 2}, {Name: "c", Repo: true, Uncommitted: 1},
		{Name: "d", Repo: true, Unpushed: 3}, {Name: "e", Repo: true, Unchecked: true}, {Name: "f", Repo: true}}}
	f, found := verifyProjects("acme", p)
	want := "5 of 6 in ~/projects hold work that exists only on this box (1 not a git repository, 1 with no remote, 1 with uncommitted changes, " +
		"1 with commits not pushed, 1 that could not be checked). " +
		"List them: keelbox status acme; then commit and push as the agent, or copy them off"
	if !found || f.Level != engine.LevelWarn || !f.Flaky || f.Module != "runtime" || f.Check != "projects" || f.Detail != want {
		t.Errorf("finding:\n got %+v\nwant %s", f, want)
	}
	if strings.Contains(f.Detail, "secret-name") {
		t.Error("a project's name in a finding that notifications carry")
	}

	// On a box: the probe runs as the agent and doctor reports it.
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, gitYAML))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	s.OK("(as agent) sh -s -- /home/agent/projects", "P 73686f70 norepo 0 0 0\nT 1\n")
	var got engine.Finding
	for _, f := range (Runtime{}).Verify(context.Background(), e) {
		if f.Check == "projects" {
			got = f
		}
	}
	if got.Level != engine.LevelWarn || !strings.Contains(got.Detail, "1 of 1 in ~/projects") || !strings.Contains(got.Detail, "keelbox status testbox") {
		t.Errorf("doctor on the box: %+v", got)
	}
}
