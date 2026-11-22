package config

import (
	"strings"
	"testing"
)

func TestAgentGit(t *testing.T) {
	c, err := Parse([]byte(minimal("  agent:\n    git: {name: Ada Lovelace, email: ada@example.com}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if n, e, ok := c.AgentGit(); !ok || n != "Ada Lovelace" || e != "ada@example.com" {
		t.Errorf("AgentGit: %q %q %v", n, e, ok)
	}
	c, _ = Parse([]byte(minimal("")), "t")
	if _, _, ok := c.AgentGit(); ok {
		t.Error("a git identity by default")
	}
	if out, _ := c.Marshal(); strings.Contains(string(out), "git:") {
		t.Error("an unset identity is in the applied config, which an older keelbox would refuse")
	}
	for y, want := range map[string]string{
		"{name: Ada}":                           "needs both name and email",
		"{email: ada@example.com}":              "needs both name and email",
		`{name: "Ada\nL", email: a@b.c}`:        "users.agent.git.name",
		`{name: "-c core.x=y", email: a@b.c}`:   "users.agent.git.name",
		`{name: "Ada <x>", email: a@b.c}`:       "users.agent.git.name",
		"{name: Ada, email: ada}":               "users.agent.git.email",
		`{name: Ada, email: "a b@example.com"}`: "users.agent.git.email",
		`{name: Ada, email: "-x@example.com"}`:  "users.agent.git.email",
	} {
		p := problems(t, minimal("  agent:\n    git: "+y+"\n"))
		if !strings.Contains(strings.Join(p, "\n"), want) {
			t.Errorf("git: %s: want %q in %v", y, want, p)
		}
	}
}

func TestStarterGit(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdmin admin@laptop"
	out, err := Starter{Box: "nas", Provider: ProviderLocal, Keys: []string{key}, GitName: `Ada "Countess" Lovelace`, GitEmail: "ada@example.com"}.Render()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(out), "t")
	if err != nil {
		t.Fatal(err)
	}
	if n, e, ok := c.AgentGit(); !ok || n != `Ada "Countess" Lovelace` || e != "ada@example.com" {
		t.Errorf("AgentGit from the starter: %q %q %v", n, e, ok)
	}
	out, _ = Starter{Box: "nas", Provider: ProviderLocal, Keys: []string{key}}.Render()
	if c, _ := Parse([]byte(out), "t"); c == nil || c.Users.Agent.Git != nil || !strings.Contains(out, "# git: {name: Your Name") {
		t.Errorf("a starter without an identity:\n%s", out)
	}
}

func TestProjectsDir(t *testing.T) {
	c, _ := Parse([]byte(minimal("")), "t")
	if c.ProjectsDir() != "projects" {
		t.Errorf("ProjectsDir: %q", c.ProjectsDir())
	}
	c, err := Parse([]byte(minimal("")+"sessions:\n  remote_control: {enabled: true, directory: work/src}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	if c.ProjectsDir() != "work/src" {
		t.Errorf("ProjectsDir with Remote Control's directory: %q", c.ProjectsDir())
	}
}
