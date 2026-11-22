package config

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// removedAgentUpdates is the agent_updates key, which keelbox refuses.
// Each vendor's installer puts its CLI in the agent's home, and the CLIs
// update as they would on any computer (docs/adr/0088-vendor-agent-clis.md),
// so a config that has the key gets the fix rather than "unknown field".
type removedAgentUpdates struct{}

func (*removedAgentUpdates) UnmarshalYAML(n *yaml.Node) error {
	return fmt.Errorf("line %d: keelbox has no agent_updates key: Claude Code and Codex are the vendors' own installs and update as on any computer (claude update, codex update); delete the agent_updates block", n.Line)
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func oneOf(s string, options ...string) bool { return contains(options, s) }
