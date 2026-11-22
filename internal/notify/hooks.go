package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/plain"
)

// HookInput is what keelbox event keeps of a hook's JSON. Everything else
// the CLI sends (the prompt, the transcript path, the last message) is
// never read into keelbox.
type HookInput struct {
	Event            string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	Error            string `json:"error"`
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	Message          string `json:"message"`
}

// Claude Code's StopFailure error values (code.claude.com/docs/en/hooks,
// read 2026-11-07). Limit errors become agent-limit; the rest agent-error.
// The documentation does not say whether a claude.ai plan usage limit
// arrives as rate_limit. If it does not, the quota_auto_resume
// notifications below still report it, and the error reports as
// agent-error with its value.
var (
	claudeLimitErrors = []string{"rate_limit", "billing_error", "authentication_failed", "oauth_org_not_allowed", "account_on_hold"}
	claudeOtherErrors = []string{"overloaded", "invalid_request", "model_not_found", "server_error", "max_output_tokens", "cloud_credential_error", "unknown"}
	// Notification types that wait for the owner, and those about a usage
	// limit (v2.1.234 and later).
	claudeNeedsInput = []string{"permission_prompt", "idle_prompt", "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input"}
	claudeLimitNotes = []string{"quota_auto_resume_fired", "quota_auto_resume_stale", "quota_auto_resume_disabled"}
)

// HookCLIs are the CLIs keelbox event accepts in --from.
var HookCLIs = []string{config.AgentClaudeCode, config.AgentCodex}

// HookKind maps a hook to an agent event kind and reason; ok is false for a
// hook keelbox does not report.
func HookKind(cli string, in HookInput) (kind, reason string, ok bool) {
	switch {
	case in.Event == "Stop":
		return "agent-done", "", true
	case cli == config.AgentClaudeCode && in.Event == "StopFailure":
		switch {
		case has(claudeLimitErrors, in.Error):
			return "agent-limit", in.Error, true
		case has(claudeOtherErrors, in.Error):
			return "agent-error", in.Error, true
		}
		return "agent-error", "unknown", true
	case cli == config.AgentClaudeCode && in.Event == "Notification":
		switch {
		case has(claudeNeedsInput, in.NotificationType):
			return "agent-needs-input", in.NotificationType, true
		case has(claudeLimitNotes, in.NotificationType):
			return "agent-limit", in.NotificationType, true
		}
	case cli == config.AgentCodex && in.Event == "PermissionRequest":
		return "agent-needs-input", "permission_request", true
	}
	return "", "", false
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// HookLine turns a hook's JSON into the line for the agent's events file.
// withText keeps the notification's message, cleaned and cut to
// events.AgentTextMax characters (notify.agent_text).
func HookLine(cli string, raw []byte, now time.Time, withText bool) (events.AgentLine, bool) {
	if !has(HookCLIs, cli) {
		return events.AgentLine{}, false
	}
	var in HookInput
	if json.Unmarshal(raw, &in) != nil {
		return events.AgentLine{}, false
	}
	kind, reason, ok := HookKind(cli, in)
	if !ok {
		return events.AgentLine{}, false
	}
	sum := sha256.Sum256([]byte(in.SessionID))
	l := events.AgentLine{V: 1, Time: now.UTC(), CLI: cli, Kind: kind, Session: hex.EncodeToString(sum[:4]), Reason: reason}
	if in.Cwd != "" {
		if p := unsafeName.ReplaceAllString(path.Base(in.Cwd), "_"); p != "." && p != "/" && p != "" {
			l.Project = truncateBytes(p, 40)
		}
	}
	if withText {
		l.Text = cleanText(in.Message)
	}
	return l, true
}

// cleanText makes s one plain line (plain.Line), at most AgentTextMax.
func cleanText(s string) string {
	s = plain.Line(s)
	if r := []rune(s); len(r) > events.AgentTextMax {
		s = string(r[:events.AgentTextMax])
	}
	return s
}

func truncateBytes(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
