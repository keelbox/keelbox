package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
)

// addable is one thing keelbox add puts on a box that is set up already:
// software keelbox installs and looks after, which the box's config left
// out. Adding it is the config change, the apply and the sign-in in one
// command, so nobody has to find the line of YAML.
type addable struct {
	name, title, what string
	aliases           []string
	agent             bool     // a name in the config's agents list
	key               []string // or a switch in the config, turned on
	signIn            string   // the sign-in that follows, as keelbox login names it
}

var addables = []addable{
	{name: config.AgentClaudeCode, title: "Claude Code", what: "Anthropic's coding agent", aliases: []string{"claude"}, agent: true, signIn: config.AgentClaudeCode},
	{name: config.AgentCodex, title: "Codex", what: "OpenAI's coding agent", agent: true, signIn: config.AgentCodex},
	{name: "gh", title: "the GitHub CLI", what: "gh, for the agent's git push and pull requests", aliases: []string{"github"}, key: []string{"runtime", "gh"}, signIn: "gh"},
	{name: "docker", title: "Docker", what: "Docker Engine, rootless for the agent", key: []string{"runtime", "docker"}},
	{name: "node", title: "Node", what: "Node LTS and pnpm, for the agent", key: []string{"runtime", "node"}},
}

// addableNamed finds what a word on the command line names.
func addableNamed(s string) (addable, bool) {
	s = strings.ToLower(s)
	for _, a := range addables {
		if a.name == s {
			return a, true
		}
		for _, alias := range a.aliases {
			if alias == s {
				return a, true
			}
		}
	}
	return addable{}, false
}

func addableNames() string {
	var names []string
	for _, a := range addables {
		names = append(names, a.name)
	}
	return strings.Join(names, ", ")
}

// has reports whether the box's config asks for a.
func (a addable) has(cfg *config.Config) bool {
	switch a.name {
	case "gh":
		return cfg.GH()
	case "docker":
		return cfg.Docker()
	case "node":
		return cfg.Node()
	}
	return cfg.HasAgent(a.name)
}

// line is the config that adds a, for the user who must set it by hand.
func (a addable) line(cfg *config.Config) string {
	if a.agent {
		return "agents: [" + strings.Join(append(append([]string{}, cfg.Agents...), a.name), ", ") + "]"
	}
	return a.key[len(a.key)-1] + ": true under " + strings.Join(a.key[:len(a.key)-1], ".")
}

// addArg takes add's last word, what to add, out of the positional
// arguments, the way signInArg takes a sign-in. A second word that names
// nothing keelbox adds is an error that lists what it does.
func addArg(pos []string) ([]string, string, error) {
	if len(pos) == 0 {
		return pos, "", nil
	}
	last := pos[len(pos)-1]
	a, ok := addableNamed(last)
	switch {
	case !ok && len(pos) > 1:
		return pos, "", fmt.Errorf("%s is not something keelbox adds. It adds: %s", last, addableNames())
	case !ok || (len(pos) == 1 && (isRegistered(last) || fileExists(last))):
		return pos, "", nil
	}
	return pos[:len(pos)-1], a.name, nil
}

var (
	agentsFlow  = regexp.MustCompile(`(?m)^agents:[ \t]*\[([^\]\n]*)\]`)
	agentsBlock = regexp.MustCompile(`(?m)^agents:[ \t]*(?:#[^\n]*)?\n((?:[ \t]*#[^\n]*\n|[ \t]*\n)*(?:([ \t]+)-[ \t]*[^\n]*\n)(?:[ \t]+-[ \t]*[^\n]*\n|[ \t]*#[^\n]*\n)*)`)
)

// addAgentLine puts name in the agents list of a config, keeping every
// other byte of the file: in the one-line list keelbox writes, or as one
// more item of a list written out in lines.
func addAgentLine(doc, name string) (string, error) {
	if m := agentsFlow.FindStringSubmatchIndex(doc); m != nil {
		list := strings.TrimSpace(doc[m[2]:m[3]])
		if list != "" {
			list += ", "
		}
		return doc[:m[2]] + list + name + doc[m[3]:], nil
	}
	if m := agentsBlock.FindStringSubmatchIndex(doc); m != nil {
		indent := doc[m[4]:m[5]]
		return doc[:m[3]] + indent + "- " + name + "\n" + doc[m[3]:], nil
	}
	return "", errors.New("the file has no agents list on a line of its own")
}

// addToConfig makes the config at path ask for a, and returns the config
// as it then loads. The file changes only when the edit took and the file
// still loads; otherwise it is as it was.
func addToConfig(path string, a addable) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out string
	if a.agent {
		out, err = addAgentLine(string(data), a.name)
	} else {
		out, err = setYAMLKey(string(data), a.key, "true")
	}
	if err != nil {
		return nil, err
	}
	fi, _ := os.Stat(path)
	mode := os.FileMode(0o644)
	if fi != nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(out), mode); err != nil { //nolint:gosec // G703: the box's own config, the file this command was given
		return nil, err
	}
	next, err := config.Load(path)
	if err == nil && !a.has(next) {
		err = errors.New("the edited file would not ask for it")
	}
	if err != nil {
		_ = os.WriteFile(path, data, mode) //nolint:gosec // G703: the file as it was
		return nil, err
	}
	return next, nil
}

// addList says what the box has of the things keelbox adds, and the
// command for each of the rest.
func addList(cfg *config.Config, ref string, stdout io.Writer) {
	var has []string
	var rest []addable
	for _, a := range addables {
		if a.has(cfg) {
			has = append(has, a.name)
		} else {
			rest = append(rest, a)
		}
	}
	if len(has) > 0 {
		fmt.Fprintf(stdout, "%s has: %s\n", cfg.Box, strings.Join(has, ", "))
	}
	if len(rest) == 0 {
		fmt.Fprintf(stdout, "Nothing left to add.\n")
		return
	}
	fmt.Fprintln(stdout, "Add:")
	width := 0
	for _, a := range rest {
		width = max(width, len(a.name))
	}
	for _, a := range rest {
		fmt.Fprintf(stdout, "  keelbox add %s %-*s  %s\n", ref, width, a.name, a.what)
	}
}

// cmdAdd is keelbox add [box] [what]: the config change, the apply that
// installs it and the sign-in it needs. apply runs keelbox apply for the
// config as changed.
func cmdAdd(ctx context.Context, cfg *config.Config, o options, path string, apply func(*config.Config) int, stdout, stderr io.Writer) int {
	ref := boxRef(cfg, path)
	if o.add == "" {
		addList(cfg, ref, stdout)
		return 0
	}
	a, _ := addableNamed(o.add)
	if o.api && !a.agent {
		// Before anything changes: the sign-in after would refuse it.
		failf(stderr, "--api is for the sign-in of an agent CLI, claude-code or codex, and %s has none of that kind. Without it: keelbox add %s %s\n", a.name, ref, a.name)
		return 2
	}
	signIn := func() string {
		if a.signIn == "" {
			return ""
		}
		if o.api {
			return fmt.Sprintf("keelbox login %s %s --api", ref, a.signIn)
		}
		return fmt.Sprintf("keelbox login %s %s", ref, a.signIn)
	}
	if a.has(cfg) {
		fmt.Fprintf(stdout, "%s has %s already.", cfg.Box, a.name)
		if s := signIn(); s != "" {
			fmt.Fprintf(stdout, " Sign in: %s", s)
		}
		fmt.Fprintln(stdout)
		return 0
	}
	next, err := addToConfig(path, a)
	if err != nil {
		failf(stderr, "could not add %s to %s (%v); nothing changed. Set %s there yourself, then: keelbox apply %s\n", a.name, tildePath(path), err, a.line(cfg), ref)
		return 1
	}
	fmt.Fprintf(stdout, "Added %s to %s. Applying it to %s.\n", a.name, tildePath(path), cfg.Box)
	if code := apply(next); code != 0 {
		// apply said why. The config asks for it now, so the next apply
		// brings it.
		notef(stderr, "the config asks for %s now, and the apply did not finish. Again: keelbox apply %s", a.name, ref)
		return code
	}
	switch {
	case a.signIn == "":
		return 0
	case !prompt.IsTerminal(os.Stdin):
		fmt.Fprintf(stdout, "Sign in: %s\n", signIn())
		return 0
	}
	o.signIn = a.signIn
	return cmdLogin(ctx, next, o, a.signIn, stdout, stderr)
}
