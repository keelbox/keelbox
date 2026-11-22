//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestVendorAgentCLIs: Claude Code and Codex are the vendors' own installs
// in the agent's home, and their own update commands work there
// (docs/adr/0088-vendor-agent-clis.md).
func TestVendorAgentCLIs(t *testing.T) {
	ensureKey(t)
	build(t)
	_, host := agentsBox(t)
	out := must(t, host, "agent", "command -v claude codex; claude --version; codex --version",
		"/home/agent/.local/bin/claude", "/home/agent/.local/bin/codex", "(Claude Code)", "codex-cli ")
	logf(t, "PASS vendor installs: %s", firstLine(out))
	must(t, host, "agent", "codex update >/dev/null 2>&1 && echo codex-updated; claude update >/dev/null 2>&1 && echo claude-updated",
		"codex-updated", "claude-updated")
	must(t, host, "agent", "test ! -e /usr/local/lib/keelbox/agents && test ! -e /opt/keelbox/agents && echo no-store", "no-store")
	logf(t, "PASS codex update and claude update run as the agent; no agent store")
}

// agentsBox creates an LXD VM and applies a minimal config with Claude
// Code and Codex, using the keelbox kb runs. The VM goes when the test
// ends.
func agentsBox(t *testing.T) (cfg, host string) {
	t.Helper()
	return minimalBox(t, "agents: [claude-code, codex]\n", "")
}

// minimalBox creates an LXD VM and applies a config with both accounts'
// keys, agentYAML under users.agent and extra at the top level, using the
// keelbox kb runs. The VM goes when the test ends.
func minimalBox(t *testing.T, extra, agentYAML string) (cfg, host string) {
	t.Helper()
	tg := pickTarget(t)
	if _, ok := tg.(*lxdTarget); !ok {
		t.Skip("this e2e runs on LXD")
	}
	box := fmt.Sprintf("%s%d", VMPrefix, time.Now().Unix()%1000000)
	t.Cleanup(func() {
		if p := os.Getenv("KEELBOX_E2E_LOG"); p != "" {
			_ = os.WriteFile(p, transcript.Bytes(), 0o600)
		}
	})
	targetYAML, bootstrap, host := tg.Create(t, box)
	t.Cleanup(func() {
		if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
			logf(t, "KEELBOX_E2E_KEEP=1: leaving %s", box)
			return
		}
		tg.Destroy(t, box)
	})
	cfg = filepath.Join(workDir, box+".yaml")
	yaml := fmt.Sprintf(`version: 1
box: %s
%s
users:
  admin:
    ssh_authorized_key_files: [%s.pub]
  agent:
    ssh_authorized_key_files: [%s.pub]
%s%s`, box, targetYAML, keyPath, keyPath, agentYAML, extra)
	if err := os.WriteFile(cfg, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := kb(t, "apply", cfg, "--ssh-user", bootstrap); err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	return cfg, host
}
