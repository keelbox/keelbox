package config

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// users.agent.git: the name and email the agent's commits carry
// (docs/adr/0064-work-on-the-box.md). Absent means keelbox writes no git
// identity, and git refuses to commit until the agent has one.

// GitIdentity is users.agent.git as written.
type GitIdentity struct {
	Name  string `yaml:"name,omitempty"`
	Email string `yaml:"email,omitempty"`
}

// AgentGit is users.agent.git; ok is false when it is not set.
func (c *Config) AgentGit() (name, email string, ok bool) {
	g := c.Users.Agent.Git
	if g == nil || g.Name == "" || g.Email == "" {
		return "", "", false
	}
	return g.Name, g.Email, true
}

// ProjectsDir is where the agent's projects live, relative to its home:
// the directory the primer names and Remote Control's sessions start in
// (sessions.remote_control.directory, default projects).
func (c *Config) ProjectsDir() string { return c.RemoteControlDir() }

// ValidGitName checks a git user.name: one line of printable text that git
// config takes as a value.
func ValidGitName(s string) error {
	switch {
	case s == "" || strings.TrimSpace(s) != s:
		return errGit("a name, without spaces around it")
	case len(s) > 100 || !printable(s) || strings.HasPrefix(s, "-") || strings.ContainsAny(s, "<>"):
		return errGit("one line of at most 100 characters, without < or >, not starting with -")
	}
	return nil
}

// ValidGitEmail checks a git user.email. git takes any text; keelbox asks
// for the shape of an address, so a typo shows before the first commit.
func ValidGitEmail(s string) error {
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 || len(s) > 254 || !printable(s) || strings.HasPrefix(s, "-") || strings.ContainsAny(s, " <>") {
		return errGit("an address such as you@example.com")
	}
	return nil
}

type errGit string

func (e errGit) Error() string { return "use " + string(e) }

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return utf8.ValidString(s)
}

func (c *Config) validateGit(add func(string, ...any)) {
	g := c.Users.Agent.Git
	if g == nil {
		return
	}
	const fix = "users.agent.git: {name: Your Name, email: you@example.com}"
	if g.Name == "" || g.Email == "" {
		add("users.agent.git: needs both name and email, for example %s", fix)
		return
	}
	if err := ValidGitName(g.Name); err != nil {
		add("users.agent.git.name: %q: %v", g.Name, err)
	}
	if err := ValidGitEmail(g.Email); err != nil {
		add("users.agent.git.email: %q: %v", g.Email, err)
	}
}
