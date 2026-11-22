package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
)

// The agent CLIs' administrator policy files (docs/adr/0033-agent-policy-files.md).
// Both CLIs rank them above anything the agent's own settings say.
const (
	// ClaudeManagedDir is Claude Code's drop-in directory for managed
	// settings on Linux; it merges every *.json there, in name order, after
	// /etc/claude-code/managed-settings.json, nested blocks key by key.
	ClaudeManagedDir  = "/etc/claude-code/managed-settings.d"
	ClaudeManagedFile = "/etc/claude-code/managed-settings.json"
	// claudeSettingsFile holds keelbox's env and settings, claudeHooksFile
	// its hooks. The names are keelbox's: any file there by those names is.
	claudeSettingsFile = ClaudeManagedDir + "/50-keelbox.json"
	claudeHooksFile    = ClaudeManagedDir + "/51-keelbox-hooks.json"
	// CodexRequirements is Codex's admin-enforced requirements file. It may
	// belong to an organisation's policy: keelbox writes it only when it is
	// absent or carries keelbox's marker.
	CodexRequirements = "/etc/codex/requirements.toml"
	codexMarker       = "# Managed by keelbox."
)

// AgentPolicy collects what keelbox puts in the agent CLIs' policy files.
// Each feature that needs a vendor setting adds it from its policy source
// (policySources); this builder alone renders and owns the files, so two
// features never write the same file. Settings for a CLI the box does not
// install are dropped.
type AgentPolicy struct {
	claudeEnv      map[string]string
	claudeSettings map[string]string
	codex          map[string]bool // dotted keys, such as features.hooks
	hooks          map[string]*hookSet
	errs           []string
}

// Hook is one command hook. Command must start with an absolute path; it
// runs as the agent, with the CLI's hook JSON on stdin.
type Hook struct {
	Command string
	Timeout int  // seconds; 0 leaves the CLI's default
	Async   bool // run in the background, never blocking the CLI
}

type hookGroup struct {
	Matcher string
	Hooks   []Hook
}

// hookSet is one CLI's hooks by event, in the order they were added.
type hookSet struct {
	events []string
	groups map[string][]hookGroup
}

// Agent CLI identifiers for AddHook.
const (
	PolicyClaude = config.AgentClaudeCode
	PolicyCodex  = config.AgentCodex
)

var (
	envKeyRe     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	settingKeyRe = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)
	codexKeyRe   = regexp.MustCompile(`^([a-z][a-z0-9_]{0,63}\.)?[a-z][a-z0-9_]{0,63}$`)
	hookEventRe  = regexp.MustCompile(`^[A-Z][A-Za-z]{1,40}$`)
)

func (p *AgentPolicy) fail(format string, args ...any) {
	p.errs = append(p.errs, fmt.Sprintf(format, args...))
}

// AddEnv sets an environment variable in Claude Code's managed settings,
// which overrides the agent's shell and its own settings.
func (p *AgentPolicy) AddEnv(key, value string) {
	if !envKeyRe.MatchString(key) || !printable(value) {
		p.fail("claude env %q: a variable name and one line of printable text", key)
		return
	}
	if p.claudeEnv == nil {
		p.claudeEnv = map[string]string{}
	}
	p.claudeEnv[key] = value
}

// SetClaude sets a top-level managed setting with a string value, such as
// requiredMinimumVersion. env and hooks have their own methods.
func (p *AgentPolicy) SetClaude(key, value string) {
	if !settingKeyRe.MatchString(key) || key == "env" || key == "hooks" || !printable(value) {
		p.fail("claude setting %q: a camelCase name other than env and hooks, and one line of printable text", key)
		return
	}
	if p.claudeSettings == nil {
		p.claudeSettings = map[string]string{}
	}
	p.claudeSettings[key] = value
}

// SetCodex sets a boolean requirement for Codex, at the top level
// or in one table (features.hooks). Codex
// refuses to start when a requirement has the wrong type, so only booleans
// are offered; it ignores keys it does not know.
func (p *AgentPolicy) SetCodex(key string, value bool) {
	if !codexKeyRe.MatchString(key) || strings.HasPrefix(key, "hooks.") {
		p.fail("codex requirement %q: snake_case, at most one table", key)
		return
	}
	if p.codex == nil {
		p.codex = map[string]bool{}
	}
	p.codex[key] = value
}

// AddHook adds a managed command hook for cli on event, for tools matching
// matcher ("" for all). Managed hooks run whatever the agent's own settings
// say about hooks.
func (p *AgentPolicy) AddHook(cli, event, matcher string, h Hook) {
	switch {
	case cli != PolicyClaude && cli != PolicyCodex:
		p.fail("hook for %q: the CLI is %s or %s", cli, PolicyClaude, PolicyCodex)
		return
	case !hookEventRe.MatchString(event):
		p.fail("hook event %q is not an event name", event)
		return
	case !path.IsAbs(strings.SplitN(h.Command, " ", 2)[0]) || !printable(h.Command):
		p.fail("hook command %q: one line, starting with an absolute path", h.Command)
		return
	case h.Timeout < 0 || !printable(matcher) && matcher != "":
		p.fail("hook on %s: a timeout of 0 or more seconds and a one-line matcher", event)
		return
	}
	if p.hooks == nil {
		p.hooks = map[string]*hookSet{}
	}
	s := p.hooks[cli]
	if s == nil {
		s = &hookSet{groups: map[string][]hookGroup{}}
		p.hooks[cli] = s
	}
	if _, ok := s.groups[event]; !ok {
		s.events = append(s.events, event)
	}
	groups := s.groups[event]
	for i := range groups {
		if groups[i].Matcher == matcher {
			groups[i].Hooks = append(groups[i].Hooks, h)
			return
		}
	}
	s.groups[event] = append(groups, hookGroup{Matcher: matcher, Hooks: []Hook{h}})
}

func printable(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// policySource adds one feature's settings. A feature that needs a vendor
// setting adds its function to policySources and nothing else: the builder
// decides the files.
type policySource func(e *engine.Env, p *AgentPolicy)

// policySources is every feature's contribution, in a fixed order.
var policySources = []policySource{notifyHooks}

// buildPolicy runs the sources.
func buildPolicy(e *engine.Env, sources []policySource) *AgentPolicy {
	p := &AgentPolicy{}
	for _, src := range sources {
		src(e, p)
	}
	return p
}

// policyFiles is the rendered content of each file, nil for "absent".
type policyFiles struct {
	claudeSettings, claudeHooks, codex []byte
}

func (p *AgentPolicy) render(c *config.Config) (policyFiles, error) {
	var f policyFiles
	if len(p.errs) > 0 {
		return f, fmt.Errorf("agent policy: %s", strings.Join(p.errs, "; "))
	}
	if c.HasAgent(config.AgentClaudeCode) {
		settings := map[string]any{}
		for k, v := range p.claudeSettings {
			settings[k] = v
		}
		if len(p.claudeEnv) > 0 {
			settings["env"] = p.claudeEnv
		}
		if len(settings) > 0 {
			f.claudeSettings = jsonFile(settings)
		}
		if s := p.hooks[PolicyClaude]; s != nil {
			f.claudeHooks = jsonFile(map[string]any{"hooks": s.claudeJSON()})
		}
	}
	if c.HasAgent(config.AgentCodex) && (len(p.codex) > 0 || p.hooks[PolicyCodex] != nil) {
		f.codex = p.codexTOML()
	}
	return f, nil
}

func jsonFile(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ") // maps of strings: cannot fail
	return append(b, '\n')
}

// claudeJSON is Claude Code's hooks block:
// {"Stop": [{"matcher": "", "hooks": [{"type": "command", ...}]}]}.
func (s *hookSet) claudeJSON() map[string]any {
	out := map[string]any{}
	for _, ev := range s.events {
		var groups []map[string]any
		for _, g := range s.groups[ev] {
			var hooks []map[string]any
			for _, h := range g.Hooks {
				m := map[string]any{"type": "command", "command": h.Command}
				if h.Timeout > 0 {
					m["timeout"] = h.Timeout
				}
				if h.Async {
					m["async"] = true
				}
				hooks = append(hooks, m)
			}
			grp := map[string]any{"hooks": hooks}
			if g.Matcher != "" {
				grp["matcher"] = g.Matcher
			}
			groups = append(groups, grp)
		}
		out[ev] = groups
	}
	return out
}

// codexTOML renders requirements.toml: top-level keys, then one-level
// tables, then [[hooks.EVENT]] groups with their [[hooks.EVENT.hooks]].
// Every value is a boolean, an integer or a quoted string, so the file
// always parses with the types Codex expects.
func (p *AgentPolicy) codexTOML() []byte {
	var b bytes.Buffer
	b.WriteString(codexMarker + " Requirements for Codex on this box, which settings in ~/.codex cannot override.\n")
	b.WriteString("# Edit the box's keelbox config and re-apply; keelbox rewrites this file.\n")
	keys := make([]string, 0, len(p.codex))
	for k := range p.codex {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tables := map[string][]string{}
	var tableNames []string
	for _, k := range keys {
		if t, name, ok := strings.Cut(k, "."); ok {
			if _, seen := tables[t]; !seen {
				tableNames = append(tableNames, t)
			}
			tables[t] = append(tables[t], fmt.Sprintf("%s = %t", name, p.codex[k]))
			continue
		}
		fmt.Fprintf(&b, "%s = %t\n", k, p.codex[k])
	}
	for _, t := range tableNames {
		fmt.Fprintf(&b, "\n[%s]\n%s\n", t, strings.Join(tables[t], "\n"))
	}
	if s := p.hooks[PolicyCodex]; s != nil {
		for _, ev := range s.events {
			for _, g := range s.groups[ev] {
				fmt.Fprintf(&b, "\n[[hooks.%s]]\n", ev)
				if g.Matcher != "" {
					fmt.Fprintf(&b, "matcher = %s\n", tomlString(g.Matcher))
				}
				for _, h := range g.Hooks {
					fmt.Fprintf(&b, "[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = %s\n", ev, tomlString(h.Command))
					if h.Timeout > 0 {
						fmt.Fprintf(&b, "timeout = %d\n", h.Timeout)
					}
					if h.Async {
						b.WriteString("async = true\n")
					}
				}
			}
		}
	}
	return b.Bytes()
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// AgentPolicyModule installs the policy files. It runs before the agents
// module, so a CLI never starts on the box without them.
type AgentPolicyModule struct {
	// sources replaces policySources in tests.
	sources []policySource
}

func (AgentPolicyModule) Name() string { return "agent-policy" }

// Enabled until a feature needs a policy file, there is nothing to install.
func (m AgentPolicyModule) Enabled(*config.Config) bool { return len(m.src()) > 0 }

func (m AgentPolicyModule) src() []policySource {
	if m.sources == nil {
		return policySources
	}
	return m.sources
}

func (m AgentPolicyModule) policy(e *engine.Env) (*AgentPolicy, policyFiles, error) {
	p := buildPolicy(e, m.src())
	f, err := p.render(e.Cfg)
	return p, f, err
}

func (m AgentPolicyModule) Steps(e *engine.Env) []engine.Step {
	_, files, err := m.policy(e)
	ours := func([]byte) bool { return true }
	return []engine.Step{
		policyFileStep("claude-managed-settings", claudeSettingsFile, files.claudeSettings, ours, err),
		policyFileStep("claude-managed-hooks", claudeHooksFile, files.claudeHooks, ours, err),
		policyFileStep("codex-requirements", CodexRequirements, files.codex, codexOurs, err),
	}
}

func codexOurs(data []byte) bool { return bytes.HasPrefix(data, []byte(codexMarker)) }

// policyFileStep converges one policy file to want, or to absent when want
// is nil. A file keelbox does not own (ours is false) is left as it is, and
// doctor says what that leaves unset.
func policyFileStep(name, p string, want []byte, ours func([]byte) bool, buildErr error) engine.Step {
	state := func(e *engine.Env) (exists, owned bool, cur []byte, err error) {
		st, err := e.X.Stat(p)
		if err != nil || !st.Exists {
			return false, false, nil, err
		}
		if st.IsDir || st.IsSymlink {
			return true, false, nil, nil
		}
		cur, err = e.X.ReadFile(p)
		return true, err == nil && ours(cur), cur, err
	}
	file := engine.File{Path: p, Content: want, Mode: 0o644}
	return engine.Step{
		Name: name,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if buildErr != nil {
				return engine.Status{}, buildErr
			}
			exists, owned, _, err := state(e)
			switch {
			case err != nil:
				return engine.Status{}, err
			case exists && !owned:
				return engine.Converged(), nil
			case want == nil && exists:
				return engine.Pending("remove %s (nothing for keelbox to set)", p), nil
			case want == nil:
				return engine.Converged(), nil
			}
			return engine.FileStep(name, file).Check(ctx, e)
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if want == nil {
				return e.X.Remove(p)
			}
			if err := e.X.MkdirAll(path.Dir(p), 0o755, "root", "root"); err != nil {
				return err
			}
			return engine.FileStep(name, file).Apply(ctx, e)
		},
	}
}

func (m AgentPolicyModule) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	const mod = "agent-policy"
	var out []engine.Finding
	if e.Cfg.HasAgent(config.AgentClaudeCode) {
		// One managed settings file that does not parse makes Claude Code
		// refuse to start sessions, whoever wrote it.
		paths := []string{ClaudeManagedFile}
		if names, err := e.X.ReadDir(ClaudeManagedDir); err == nil {
			for _, n := range names {
				if strings.HasSuffix(n, ".json") {
					paths = append(paths, ClaudeManagedDir+"/"+n)
				}
			}
		}
		for _, p := range paths {
			data, err := e.X.ReadFile(p)
			if err != nil {
				continue
			}
			var obj map[string]any
			if json.Unmarshal(data, &obj) != nil {
				out = append(out, fail(mod, "claude-managed-settings", p+" is not a JSON object, so Claude Code starts no session for the agent: fix or remove it"))
			}
		}
	}
	p, files, err := m.policy(e)
	if err == nil && files.codex != nil {
		if cur, rerr := e.X.ReadFile(CodexRequirements); rerr == nil && !codexOurs(cur) {
			out = append(out, warn(mod, "codex-requirements", CodexRequirements+" was not written by keelbox, so keelbox leaves it and its own Codex requirements are not set: merge them in by hand ("+strings.Join(p.codexNames(), ", ")+"), or remove the file and keelbox apply "+e.Cfg.Box))
		}
	}
	return out
}

// codexNames lists the requirements and hook events keelbox sets for
// Codex, for doctor's message.
func (p *AgentPolicy) codexNames() []string {
	var out []string
	for k := range p.codex {
		out = append(out, k)
	}
	sort.Strings(out)
	if s := p.hooks[PolicyCodex]; s != nil {
		for _, ev := range s.events {
			out = append(out, "hooks."+ev)
		}
	}
	return out
}
