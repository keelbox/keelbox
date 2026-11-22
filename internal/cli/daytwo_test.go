package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/events"
)

func TestHasCommand(t *testing.T) {
	for args, want := range map[string]int{"apply": 0, "previews-sync": 0, "watch-run": 0, "events-sync": 0, "doctor-run": 0, "no-such-run": 1} {
		if code, _, _ := run("has-command", args); code != want {
			t.Errorf("has-command %s: exit %d, want %d", args, code, want)
		}
	}
	if code, _, _ := run("has-command"); code != 2 {
		t.Errorf("has-command without a name: exit %d", code)
	}
}

func TestEvent(t *testing.T) {
	home := t.TempDir()
	eventHome = func() string { return home }
	defer func() { eventHome = func() string { return "" } }()
	hook := func(from, input string, extra ...string) (int, string) {
		var stderr bytes.Buffer
		code := cmdEvent(append([]string{"--from", from}, extra...), strings.NewReader(input), &stderr)
		return code, stderr.String()
	}
	for _, c := range [][2]string{
		{"claude-code", `{"hook_event_name":"Stop","session_id":"abc","cwd":"/home/agent/shop-api","prompt":"secret plans"}`},
		{"claude-code", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Allow?"}`},
		{"codex", `{"hook_event_name":"PermissionRequest","session_id":"x"}`},
		{"claude-code", `{"hook_event_name":"UserPromptSubmit"}`}, // not reported
		{"claude-code", `garbage`},
		{"nobody", `{"hook_event_name":"Stop"}`},
	} {
		if code, errOut := hook(c[0], c[1]); code != 0 || errOut != "" {
			t.Errorf("%s %s: exit %d %s", c[0], c[1], code, errOut)
		}
	}
	if code, _ := hook("claude-code", "", "--no-such-flag"); code != 0 {
		t.Error("a bad flag made the hook fail")
	}
	data, err := os.ReadFile(filepath.Join(home, events.AgentFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"kind":"agent-done"`) || !strings.Contains(lines[0], `"project":"shop-api"`) ||
		strings.Contains(string(data), "secret plans") || strings.Contains(string(data), "Allow?") || !strings.Contains(lines[2], `"cli":"codex"`) {
		t.Errorf("events file:\n%s", data)
	}
}
