package notify

import (
	"strings"
	"testing"
)

func TestHookLine(t *testing.T) {
	now := t0
	cases := []struct {
		cli, in      string
		kind, reason string
	}{
		{"claude-code", `{"hook_event_name":"Stop","session_id":"s1","cwd":"/home/agent/src/shop-api"}`, "agent-done", ""},
		{"claude-code", `{"hook_event_name":"StopFailure","error":"rate_limit","session_id":"s1"}`, "agent-limit", "rate_limit"},
		{"claude-code", `{"hook_event_name":"StopFailure","error":"overloaded","session_id":"s1"}`, "agent-error", "overloaded"},
		{"claude-code", `{"hook_event_name":"StopFailure","error":"Robert'); DROP","session_id":"s1"}`, "agent-error", "unknown"},
		{"claude-code", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs permission\nto run rm"}`, "agent-needs-input", "permission_prompt"},
		{"claude-code", `{"hook_event_name":"Notification","notification_type":"quota_auto_resume_stale"}`, "agent-limit", "quota_auto_resume_stale"},
		{"claude-code", `{"hook_event_name":"Notification","notification_type":"auth_success"}`, "", ""},
		{"claude-code", `{"hook_event_name":"UserPromptSubmit","prompt":"secret plans"}`, "", ""},
		{"codex", `{"hook_event_name":"PermissionRequest","session_id":"x","cwd":"/w/my repo!"}`, "agent-needs-input", "permission_request"},
		{"codex", `{"hook_event_name":"Stop"}`, "agent-done", ""},
		{"gemini", `{"hook_event_name":"Stop"}`, "", ""},
		{"claude-code", `not json`, "", ""},
	}
	for _, c := range cases {
		l, ok := HookLine(c.cli, []byte(c.in), now, true)
		if ok != (c.kind != "") || l.Kind != c.kind || l.Reason != c.reason {
			t.Errorf("%s %s: %+v %v", c.cli, c.in, l, ok)
			continue
		}
		if !ok {
			continue
		}
		e, err := l.Event("acme", now)
		if err != nil {
			t.Errorf("%s: the line root reads back: %v", c.in, err)
		}
		if strings.Contains(c.in, "/w/my repo!") && e.Fields["project"] != "my_repo_" {
			t.Errorf("project: %q", e.Fields["project"])
		}
		if strings.Contains(c.in, "permission\\nto") && l.Text != "Claude needs permission to run rm" {
			t.Errorf("text: %q", l.Text)
		}
	}
	if l, _ := HookLine("claude-code", []byte(`{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"hi"}`), now, false); l.Text != "" {
		t.Error("text kept without --text")
	}
}
