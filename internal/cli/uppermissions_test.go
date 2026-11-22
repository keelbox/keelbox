package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
)

// keelbox up asks how much the agent may change the box, with Auto first
// and preselected; without a terminal --agent-permissions answers, and the
// default is auto with no question (ADR 0074).
func TestUpPermissions(t *testing.T) {
	const base = "--provider ssh --host 203.0.113.7 --ssh-user root --host-key= --agents codex --gh=false --tailscale=false --skip-sign-in"
	run := func(t *testing.T, input *string, extra ...string) (int, string) {
		t.Helper()
		home := isolate(t)
		key := laptopKey(t, home)
		args := append([]string{"up", "lab", "--ssh-key", key}, strings.Fields(base)...)
		return upWith(t, &fakeRunner{}, input, append(args, extra...)...)
	}
	written := func(t *testing.T) (*config.Config, string) {
		t.Helper()
		cfg, _ := loadBox(t, "lab")
		data, err := os.ReadFile(boxConfigPath("lab"))
		if err != nil {
			t.Fatal(err)
		}
		return cfg, string(data)
	}
	const menu = "How much may the agent change this box?\n" +
		"  1) Auto (recommended)  installs Ubuntu packages itself with keelbox install; no root shell\n" +
		"  2) Manual              asks first; you approve with keelbox approve lab\n" +
		"  3) Full                sudo: it can change anything, and none of keelbox's limits on the agent then hold\n" +
		"Choose 1-3 [1]: "

	t.Run("Enter takes Auto", func(t *testing.T) {
		askingPermissions(t)
		code, out := run(t, script("", "")) // the question, go ahead
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		cfg, text := written(t)
		if cfg.AgentPermissions() != config.PermissionsAuto || cfg.AgentSudo() {
			t.Errorf("permissions %q", cfg.AgentPermissions())
		}
		// The default is not written, so an older keelbox still reads the config.
		if !strings.Contains(text, "    # permissions: auto\n") || strings.Contains(text, "\n    permissions:") {
			t.Errorf("the config names the default:\n%s", text)
		}
		for _, want := range []string{
			menu,
			"permission  auto: the agent installs Ubuntu packages itself with keelbox install; it has no sudo",
			"Codex is installed and runs as the agent, an account without sudo that can install Ubuntu packages (keelbox install).",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("Manual by number", func(t *testing.T) {
		askingPermissions(t)
		code, out := run(t, script("2", ""))
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		cfg, text := written(t)
		if cfg.AgentPermissions() != config.PermissionsManual || !strings.Contains(text, "\n    permissions: manual\n") {
			t.Errorf("permissions %q:\n%s", cfg.AgentPermissions(), text)
		}
		for _, want := range []string{
			"permission  manual: the agent asks for Ubuntu packages; you approve each with keelbox approve lab",
			"Codex is installed and runs as the agent, an account without sudo. ",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("Full by name", func(t *testing.T) {
		askingPermissions(t)
		code, out := run(t, script("full", ""))
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		cfg, text := written(t)
		if !cfg.AgentSudo() || !strings.Contains(text, "\n    permissions: full\n") || strings.Contains(text, "sudo: true") {
			t.Errorf("permissions %q:\n%s", cfg.AgentPermissions(), text)
		}
		for _, want := range []string{
			"permission  full: the agent has sudo, and none of keelbox's limits on it then hold",
			"Codex is installed and runs as the agent, an account with sudo. ",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("the flag, no terminal", func(t *testing.T) {
		askingPermissions(t)
		code, out := run(t, nil, "--yes", "--agent-permissions", "manual")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if cfg, _ := written(t); cfg.AgentPermissions() != config.PermissionsManual {
			t.Errorf("permissions %q", cfg.AgentPermissions())
		}
		if strings.Contains(out, "How much may the agent change this box?") {
			t.Errorf("asked without a terminal:\n%s", out)
		}
	})
	t.Run("no flag, no terminal: auto", func(t *testing.T) {
		askingPermissions(t)
		code, out := run(t, nil, "--yes")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if cfg, _ := written(t); cfg.AgentPermissions() != config.PermissionsAuto {
			t.Errorf("permissions %q", cfg.AgentPermissions())
		}
		if strings.Contains(out, "How much may the agent change this box?") || !strings.Contains(out, "permission  auto: ") {
			t.Errorf("without a terminal:\n%s", out)
		}
	})
	t.Run("the flag on a terminal: not asked", func(t *testing.T) {
		askingPermissions(t)
		code, out := run(t, script(""), "--agent-permissions", "full") // go ahead
		if code != 0 || strings.Contains(out, "How much may the agent change this box?") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if cfg, _ := written(t); !cfg.AgentSudo() {
			t.Errorf("permissions %q", cfg.AgentPermissions())
		}
	})
	t.Run("a value that is no mode", func(t *testing.T) {
		askingPermissions(t)
		code, out := run(t, nil, "--yes", "--agent-permissions", "bypass")
		if code == 0 || !strings.Contains(out, `--agent-permissions "bypass": use auto, manual or full, for example: --agent-permissions auto`) {
			t.Errorf("exit %d\n%s", code, out)
		}
		if isRegistered("lab") {
			t.Error("a config was written")
		}
	})
}
