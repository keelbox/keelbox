package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
)

// reboot runs keelbox reboot on acme, an SSH box, with input typed on a
// terminal or, with tty false, no terminal at all.
func reboot(t *testing.T, input string, tty bool, o options) (int, string, string) {
	t.Helper()
	isolate(t)
	register(t, config.StarterFor("acme", config.ProviderSSH))
	cfg, _ := loadBox(t, "acme")
	var out, errOut bytes.Buffer
	o.configFile = boxConfigPath("acme")
	code := cmdReboot(context.Background(), cfg, o, o.configFile, config.ProviderSSH, prompt.New(strings.NewReader(input), &out, tty), &out, &errOut)
	return code, out.String(), errOut.String()
}

// Without a terminal it asks for --yes, before it reaches the box.
func TestRebootWithoutATerminalNeedsYes(t *testing.T) {
	code, out, errOut := reboot(t, "", false, options{})
	if code != 2 || !strings.Contains(errOut, "to reboot anyway: keelbox reboot acme --yes") || out != "" {
		t.Errorf("exit %d\n%s%s", code, out, errOut)
	}
}

// Under an agent the refusal names the command for the owner, not the
// flag that skips the question.
func TestRebootUnderAnAgentAsksTheOwner(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	code, out, errOut := reboot(t, "", false, options{})
	if code != 2 || !strings.Contains(errOut, "ask them to run it on their terminal: keelbox reboot acme") || strings.Contains(errOut, "--yes") || out != "" {
		t.Errorf("exit %d\n%s%s", code, out, errOut)
	}
}

// On a terminal it says what stops and asks, No by default.
func TestRebootAsksFirst(t *testing.T) {
	code, out, errOut := reboot(t, "\n", true, options{})
	wantAll(t, "the question", out,
		"What runs in the agent's sessions stops. The tmux sessions and Remote Control come back by themselves, and Claude Code and Codex open their conversations again.\n",
		"Reboot acme now? [y/N]",
		"Left it running.\n")
	if code != 0 || errOut != "" {
		t.Errorf("exit %d: %s", code, errOut)
	}
}

func TestRebootNote(t *testing.T) {
	c := &config.Config{Box: "acme", Host: config.Host{AutoReboot: config.AutoRebootNightly}}
	if got := rebootNote(c); got != "the box reboots itself at night once idle; now: keelbox reboot acme" {
		t.Errorf("nightly: %q", got)
	}
	c.Host.AutoReboot = config.AutoRebootWeekly
	if got := rebootNote(c); got != "the box reboots itself on Sunday night once idle; now: keelbox reboot acme" {
		t.Errorf("weekly: %q", got)
	}
	c.Host.AutoReboot = config.AutoRebootNever
	if got := rebootNote(c); got != "keelbox reboot acme" {
		t.Errorf("never: %q", got)
	}
}
