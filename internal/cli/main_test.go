package cli

import (
	"context"
	"os"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
)

// TestMain keeps every test off real boxes: destroy's Tailscale sign-out
// would otherwise run ssh to the fake clouds' addresses.
func TestMain(m *testing.M) {
	signOutTailscale = func(context.Context, *config.Config, string, options) error { return nil }
	// Tests run as a script would, also when an agent runs them.
	_ = os.Unsetenv("CLAUDECODE")
	for _, v := range []string{"KEELBOX_AGENT", "CODEX_THREAD_ID", "CODEX_SESSION_ID"} {
		_ = os.Unsetenv(v)
	}
	// No test reads the skills of whoever runs it.
	empty, err := os.MkdirTemp("", "kb-skills-")
	if err != nil {
		panic(err)
	}
	skillHome = func() (string, error) { return empty, nil }
	code := m.Run()
	_ = os.RemoveAll(empty)
	os.Exit(code)
}
