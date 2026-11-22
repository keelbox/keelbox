package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
)

// addConfig writes a starter config as keelbox up does, with Claude Code
// alone and gh off, and returns its path and what it loads as.
func addConfig(t *testing.T) (string, *config.Config) {
	t.Helper()
	isolate(t)
	out, err := config.Starter{Box: "lab", Provider: config.ProviderLocal, NoTailscale: true, NoGH: true,
		Agents: []string{config.AgentClaudeCode}, Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lab.yaml")
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

// keelbox add changes one value in the config and nothing else, applies
// the config as changed, and leaves a box that has the thing alone.
func TestAddChangesTheConfigAndApplies(t *testing.T) {
	path, _ := addConfig(t)
	before, _ := os.ReadFile(path)
	var applied []*config.Config
	apply := func(c *config.Config) int { applied = append(applied, c); return 0 }

	var out, errOut bytes.Buffer
	// docker has no sign-in, so the run ends with the apply.
	off := strings.Replace(string(before), "docker: true ", "docker: false", 1)
	if err := os.WriteFile(path, []byte(off), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	if code := cmdAdd(context.Background(), cfg, options{add: "docker"}, path, apply, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
	if got, _ := os.ReadFile(path); string(got) != string(before) {
		t.Errorf("the file is not the one with docker on:\n%s", got)
	}
	if len(applied) != 1 || !applied[0].Docker() || !strings.Contains(out.String(), "Added docker to ") {
		t.Errorf("applied %d times\n%s", len(applied), out.String())
	}

	// An agent: the list on one line gets one more name. The test has no
	// terminal, so the sign-in is named and not run.
	out.Reset()
	cfg, _ = config.Load(path)
	if code := cmdAdd(context.Background(), cfg, options{add: "codex"}, path, apply, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
	got, _ := os.ReadFile(path)
	if want := strings.Replace(string(before), "agents: [claude-code]", "agents: [claude-code, codex]", 1); string(got) != want {
		t.Errorf("the file after adding codex:\n%s", got)
	}
	if len(applied) != 2 || !applied[1].HasAgent(config.AgentCodex) || !strings.Contains(out.String(), "Sign in: keelbox login "+path+" codex") {
		t.Errorf("applied %d times\n%s", len(applied), out.String())
	}

	// What the box has is not applied again.
	out.Reset()
	cfg, _ = config.Load(path)
	if code := cmdAdd(context.Background(), cfg, options{add: "codex"}, path, apply, &out, &errOut); code != 0 || len(applied) != 2 ||
		!strings.Contains(out.String(), "lab has codex already. Sign in: keelbox login "+path+" codex") {
		t.Errorf("exit %d, applied %d times\n%s", code, len(applied), out.String())
	}

	// With nothing named: what it has, and the command for the rest.
	out.Reset()
	if code := cmdAdd(context.Background(), cfg, options{}, path, apply, &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "lab has: claude-code, codex, docker, node\n") || !strings.Contains(out.String(), "keelbox add "+path+" gh  gh, for the agent's git push") {
		t.Errorf("exit %d\n%s", code, out.String())
	}
}

// An apply that fails leaves the config asking for the thing, and says
// the command that tries again. A file that cannot be edited is left as
// it was, with the line to set.
func TestAddWhenApplyFailsOrTheFileCannotBeEdited(t *testing.T) {
	path, cfg := addConfig(t)
	var out, errOut bytes.Buffer
	if code := cmdAdd(context.Background(), cfg, options{add: "gh"}, path, func(*config.Config) int { return 1 }, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "the config asks for gh now, and the apply did not finish. Again: keelbox apply "+path) {
		t.Errorf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
	if c, err := config.Load(path); err != nil || !c.GH() {
		t.Errorf("the config after a failed apply: %v", err)
	}

	// agents as a flow mapping value keelbox does not write.
	data, _ := os.ReadFile(path)
	odd := strings.Replace(string(data), "agents: [claude-code]", "agents: !!seq [claude-code]", 1)
	if err := os.WriteFile(path, []byte(odd), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Skip("the odd form does not load: ", err)
	}
	errOut.Reset()
	applied := false
	if code := cmdAdd(context.Background(), cfg, options{add: "codex"}, path, func(*config.Config) int { applied = true; return 0 }, &out, &errOut); code != 1 || applied ||
		!strings.Contains(errOut.String(), "nothing changed. Set agents: [claude-code, codex] there yourself, then: keelbox apply "+path) {
		t.Errorf("exit %d, applied %v\n%s", code, applied, errOut.String())
	}
	if got, _ := os.ReadFile(path); string(got) != odd {
		t.Errorf("the file changed:\n%s", got)
	}
}

func TestAddAgentLine(t *testing.T) {
	for in, want := range map[string]string{
		"a: 1\nagents: [claude-code]\nb: 2\n":           "a: 1\nagents: [claude-code, codex]\nb: 2\n",
		"agents: []   # none yet\n":                     "agents: [codex]   # none yet\n",
		"agents: [ claude-code ] # one\n":               "agents: [claude-code, codex] # one\n",
		"agents:\n  - claude-code\nsessions:\n  x: 1\n": "agents:\n  - claude-code\n  - codex\nsessions:\n  x: 1\n",
		"agents: # the CLIs\n    - claude-code\n":       "agents: # the CLIs\n    - claude-code\n    - codex\n",
	} {
		if got, err := addAgentLine(in, "codex"); err != nil || got != want {
			t.Errorf("%q:\n got %q, %v\nwant %q", in, got, err, want)
		}
	}
	for _, in := range []string{"runtime:\n  gh: true\n", "x:\n  agents: [claude-code]\n"} {
		if got, err := addAgentLine(in, "codex"); err == nil {
			t.Errorf("%q: edited to %q", in, got)
		}
	}
}

func TestAddArg(t *testing.T) {
	isolate(t)
	for _, c := range []struct {
		in        []string
		pos, what string
		err       string
	}{
		{nil, "", "", ""},
		{[]string{"acme"}, "acme", "", ""},
		{[]string{"acme", "codex"}, "acme", "codex", ""},
		{[]string{"acme", "claude"}, "acme", "claude-code", ""},
		{[]string{"acme", "GitHub"}, "acme", "gh", ""},
		{[]string{"codex"}, "", "codex", ""},
		{[]string{"acme", "opencode"}, "", "", "opencode is not something keelbox adds. It adds: claude-code, codex, gh, docker, node"},
	} {
		pos, what, err := addArg(c.in)
		switch {
		case c.err != "":
			if err == nil || err.Error() != c.err {
				t.Errorf("%v: %v, want %s", c.in, err, c.err)
			}
		case err != nil || strings.Join(pos, " ") != c.pos || what != c.what:
			t.Errorf("%v: %v, %q, %v", c.in, pos, what, err)
		}
	}
}

// --api with something that has no agent sign-in stops before the config
// changes; with an agent it is carried to the sign-in that follows.
func TestAddWithAPI(t *testing.T) {
	path, _ := addConfig(t)
	before, _ := os.ReadFile(path)
	off := strings.Replace(string(before), "docker: true ", "docker: false", 1)
	if err := os.WriteFile(path, []byte(off), 0o600); err != nil {
		t.Fatal(err)
	}
	applied := 0
	apply := func(*config.Config) int { applied++; return 0 }
	var out, errOut bytes.Buffer
	cfg, _ := config.Load(path)
	code := cmdAdd(context.Background(), cfg, options{add: "docker", api: true}, path, apply, &out, &errOut)
	if got, _ := os.ReadFile(path); code != 2 || applied != 0 || string(got) != off || !strings.Contains(errOut.String(), "--api is for the sign-in of an agent CLI") {
		t.Errorf("docker --api: exit %d, applied %d\n%s", code, applied, errOut.String())
	}
	// No terminal in the test: the sign-in is named, with the flag.
	out.Reset()
	cfg, _ = config.Load(path)
	if code := cmdAdd(context.Background(), cfg, options{add: "codex", api: true}, path, apply, &out, &errOut); code != 0 || applied != 1 || !strings.Contains(out.String(), " codex --api\n") {
		t.Errorf("codex --api: exit %d, applied %d\n%s", code, applied, out.String())
	}
}
