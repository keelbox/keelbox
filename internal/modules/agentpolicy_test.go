package modules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// testPolicy is what a feature adds: an env setting and a hook
// for each CLI.
func testPolicy(_ *engine.Env, p *AgentPolicy) {
	p.AddEnv("DISABLE_AUTOUPDATER", "1")
	p.SetClaude("requiredMinimumVersion", "2.1.300")
	p.AddHook(PolicyClaude, "Stop", "", Hook{Command: "/usr/local/bin/keelbox event --from-hook claude-code", Timeout: 10, Async: true})
	p.AddHook(PolicyClaude, "Notification", "permission_prompt", Hook{Command: "/usr/local/bin/keelbox event --from-hook claude-code", Timeout: 10, Async: true})
	p.SetCodex("check_for_update_on_startup", false)
	p.SetCodex("features.hooks", true)
	p.AddHook(PolicyCodex, "Stop", "", Hook{Command: `/usr/local/bin/keelbox event --from-hook codex --note "a \"quoted\" word"`, Timeout: 10})
}

func applyPolicy(t *testing.T, s *sim, e *engine.Env, m AgentPolicyModule) engine.Applied {
	t.Helper()
	res, err := engine.Apply(context.Background(), e, []engine.Module{m})
	if err != nil {
		t.Fatal(err)
	}
	again, err := engine.Apply(context.Background(), e, []engine.Module{m})
	if err != nil || len(again.Changes) != 0 || len(again.Failed) != 0 {
		t.Errorf("second apply was not a no-op: %+v %v", again, err)
	}
	return res
}

const wantCodexTOML = `# Managed by keelbox. Requirements for Codex on this box, which settings in ~/.codex cannot override.
# Edit the box's keelbox config and re-apply; keelbox rewrites this file.
check_for_update_on_startup = false

[features]
hooks = true

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "/usr/local/bin/keelbox event --from-hook codex --note \"a \\\"quoted\\\" word\""
timeout = 10
`

func TestAgentPolicyFiles(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	m := AgentPolicyModule{sources: []policySource{testPolicy}}
	applyPolicy(t, s, e, m)

	var settings struct {
		Env                    map[string]string `json:"env"`
		RequiredMinimumVersion string            `json:"requiredMinimumVersion"`
	}
	if err := json.Unmarshal([]byte(s.content(claudeSettingsFile)), &settings); err != nil || settings.Env["DISABLE_AUTOUPDATER"] != "1" || settings.RequiredMinimumVersion != "2.1.300" {
		t.Errorf("claude settings %+v %v:\n%s", settings, err, s.content(claudeSettingsFile))
	}
	var hooks struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type, Command string
				Timeout       int
				Async         bool
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(s.content(claudeHooksFile)), &hooks); err != nil {
		t.Fatal(err)
	}
	stop, note := hooks.Hooks["Stop"], hooks.Hooks["Notification"]
	if len(stop) != 1 || stop[0].Hooks[0].Type != "command" || !stop[0].Hooks[0].Async || stop[0].Hooks[0].Timeout != 10 ||
		len(note) != 1 || note[0].Matcher != "permission_prompt" {
		t.Errorf("claude hooks:\n%s", s.content(claudeHooksFile))
	}
	if got := s.content(CodexRequirements); got != wantCodexTOML {
		t.Errorf("requirements.toml:\n%s\nwant:\n%s", got, wantCodexTOML)
	}
	for _, p := range []string{claudeSettingsFile, claudeHooksFile, CodexRequirements} {
		if f := s.Files[p]; f.Mode != 0o644 || f.Owner != "root" {
			t.Errorf("%s: %o %s (the CLIs read it as the agent)", p, f.Mode, f.Owner)
		}
	}
	for _, f := range m.Verify(context.Background(), e) {
		if f.Level != engine.LevelOK {
			t.Errorf("doctor: %+v", f)
		}
	}

	// A feature turned off: keelbox's files go, the directories stay.
	applyPolicy(t, s, e, AgentPolicyModule{sources: []policySource{func(*engine.Env, *AgentPolicy) {}}})
	for _, p := range []string{claudeSettingsFile, claudeHooksFile, CodexRequirements} {
		if s.has(p) {
			t.Errorf("%s left behind", p)
		}
	}
}

func TestAgentPolicyNothingToSet(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if (AgentPolicyModule{sources: []policySource{}}).Enabled(e.Cfg) {
		t.Error("enabled with nothing to install")
	}
	res := applyPolicy(t, s, e, AgentPolicyModule{sources: []policySource{func(*engine.Env, *AgentPolicy) {}}})
	if len(res.Changes) != 0 || s.has("/etc/claude-code") || s.has("/etc/codex") {
		t.Errorf("with no policy sources keelbox wrote %+v", res.Changes)
	}
}

func TestAgentPolicyOnlyForInstalledCLIs(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "agents: [codex]\n"))
	applyPolicy(t, s, e, AgentPolicyModule{sources: []policySource{testPolicy}})
	if s.has(claudeSettingsFile) || s.has(claudeHooksFile) || !s.has(CodexRequirements) {
		t.Error("policy files for a CLI the box does not install")
	}
}

func TestAgentPolicyLeavesForeignFiles(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	s.Files["/etc/codex"] = &system.FakeFile{Dir: true, Mode: 0o755, Owner: "root", Group: "root"}
	s.Files[CodexRequirements] = &system.FakeFile{Data: []byte("allowed_approval_policies = [\"never\"]\n"), Mode: 0o644, Owner: "root", Group: "root"}
	s.Files[ClaudeManagedDir] = &system.FakeFile{Dir: true, Mode: 0o755, Owner: "root", Group: "root"}
	s.Files[ClaudeManagedDir+"/10-org.json"] = &system.FakeFile{Data: []byte(`{"env": {"A": "1"},}`), Mode: 0o644, Owner: "root", Group: "root"}
	m := AgentPolicyModule{sources: []policySource{testPolicy}}
	applyPolicy(t, s, e, m)
	if !strings.HasPrefix(s.content(CodexRequirements), "allowed_approval_policies") {
		t.Error("overwrote an organisation's requirements.toml")
	}
	var codexWarned, claudeFailed bool
	for _, f := range m.Verify(context.Background(), e) {
		codexWarned = codexWarned || f.Check == "codex-requirements" && f.Level == engine.LevelWarn && strings.Contains(f.Detail, "check_for_update_on_startup, features.hooks, hooks.Stop")
		claudeFailed = claudeFailed || f.Check == "claude-managed-settings" && f.Level == engine.LevelFail && strings.Contains(f.Detail, "10-org.json")
	}
	if !codexWarned || !claudeFailed {
		t.Errorf("doctor: codex warned %v, broken claude file failed %v: %+v", codexWarned, claudeFailed, m.Verify(context.Background(), e))
	}
}

func TestAgentPolicyRefusesBadInput(t *testing.T) {
	for name, add := range map[string]func(*AgentPolicy){
		"env name":             func(p *AgentPolicy) { p.AddEnv("lower case", "1") },
		"env newline":          func(p *AgentPolicy) { p.AddEnv("A", "1\nB=2") },
		"setting is env":       func(p *AgentPolicy) { p.SetClaude("env", "x") },
		"codex nested table":   func(p *AgentPolicy) { p.SetCodex("a.b.c", true) },
		"codex hooks key":      func(p *AgentPolicy) { p.SetCodex("hooks.stop", true) },
		"relative hook":        func(p *AgentPolicy) { p.AddHook(PolicyClaude, "Stop", "", Hook{Command: "keelbox event"}) },
		"hook for another CLI": func(p *AgentPolicy) { p.AddHook("gemini", "Stop", "", Hook{Command: "/bin/true"}) },
		"hook event":           func(p *AgentPolicy) { p.AddHook(PolicyCodex, "stop]]\n[x", "", Hook{Command: "/bin/true"}) },
	} {
		s := newSim(t)
		e := simEnv(t, s, testConfig(t, ""))
		src := func(_ *engine.Env, p *AgentPolicy) { add(p) }
		// A source that asks for something the CLIs would misread is
		// keelbox's bug: apply stops before writing any policy file.
		res, err := engine.Apply(context.Background(), e, []engine.Module{AgentPolicyModule{sources: []policySource{src}}})
		if (err == nil && len(res.Failed) == 0) || s.has("/etc/claude-code") || s.has("/etc/codex") {
			t.Errorf("%s: accepted (%+v)", name, res)
		}
	}
}

func TestTOMLString(t *testing.T) {
	for in, want := range map[string]string{`plain`: `"plain"`, `a"b\c`: `"a\"b\\c"`, "tab\there": `"tab\u0009here"`} {
		if got := tomlString(in); got != want {
			t.Errorf("tomlString(%q) = %s, want %s", in, got, want)
		}
	}
}
