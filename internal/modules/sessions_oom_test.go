package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

// Every box, with limits or without: a pane keeps running when the kernel
// stops one of its processes for memory. The agent's user manager reads
// the default at a reload, which restarts nothing.
func TestPaneKeepsRunningWhenTheKernelStopsOneOfItsProcesses(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	c := s.content(PaneOOMFile)
	for _, want := range []string{"Managed by keelbox", "[Manager]\nDefaultOOMPolicy=continue\n"} {
		if !strings.Contains(c, want) {
			t.Errorf("%s lacks %q:\n%s", PaneOOMFile, want, c)
		}
	}
	if !s.Ran("systemctl --user -M agent@ daemon-reload") {
		t.Error("the agent's user manager was not told to read it")
	}
	for _, call := range s.Calls {
		if line := call.String(); strings.Contains(line, "restart") && strings.Contains(line, tmuxService) {
			t.Errorf("restarted tmux: %s", line)
		}
	}
}
