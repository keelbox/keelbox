package notify

import (
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/plain"
)

// Message is an event rendered for a person: a title naming the box and who
// raised it, and one line of text. Rendering uses fixed templates, so an
// agent event always reads as the agent's and carries only checked fields.
type Message struct {
	Title string
	Text  string
	Level events.Level
	Event events.Event
}

// Line is the message on one line, for channels without a title.
func (m Message) Line() string { return m.Title + ": " + m.Text }

// reasons are the hook reasons in words; others are shown as they are,
// underscores as spaces.
var reasons = map[string]string{
	"permission_prompt":          "permission",
	"permission_request":         "permission",
	"idle_prompt":                "waiting for you",
	"elicitation_dialog":         "a question",
	"elicitation_url_dialog":     "a sign-in link",
	"agent_needs_input":          "a question",
	"quota_auto_resume_fired":    "resumed after the limit",
	"quota_auto_resume_stale":    "waiting for the limit to reset",
	"quota_auto_resume_disabled": "auto-resume off",
}

func reasonWords(r string) string {
	if w, ok := reasons[r]; ok {
		return w
	}
	return strings.ReplaceAll(r, "_", " ")
}

// Render turns an event into its message.
func Render(e events.Event) Message {
	f := e.Fields
	in := ""
	if p := f["project"]; p != "" {
		in = " in " + p
	}
	why := ""
	if r := f["reason"]; r != "" {
		why = " (" + reasonWords(r) + ")"
	}
	fix := ""
	if x := f["fix"]; x != "" {
		fix = ". Fix: " + x
	}
	detail := func(prefix string) string {
		if d := f["detail"]; d != "" {
			return prefix + d
		}
		return ""
	}
	who := "keelbox"
	if e.Source == events.SourceAgent {
		who = f["cli"]
	}
	var text string
	switch e.Kind {
	case "agent-done":
		text = "done" + in
	case "agent-needs-input":
		text = "needs your input" + why + in
	case "agent-limit":
		text = "usage limit hit" + why + in
	case "agent-error":
		text = "stopped on an error" + why + in
	case "watch-recovered":
		text = "recovered " + f["unit"] + detail(": ")
	case "watch-gave-up":
		text = "gave up restarting " + f["unit"] + detail(": ") + fix
	case "doctor-failed":
		text = "doctor FAIL " + f["check"] + detail(": ") + fix
	case "doctor-warning":
		text = "doctor warn " + f["check"] + detail(": ") + fix
	case "doctor-cleared":
		text = "doctor ok again: " + f["check"]
	case "updates-pending":
		text = "updates waiting" + detail(": ")
	case "rebooting":
		text = "rebooting for an update" + detail(": ")
	case "backup-failed":
		text = "backup failed" + detail(": ") + fix
	case "backup-stale":
		text = "no backup since " + f["last_success"] + detail(": ") + fix
	// The detail of these says it all, the headline included: said once.
	case "disk-warn", "disk-full":
		text = fmt.Sprintf("disk %s is %s%% full", f["mount"], f["percent"])
		if d := f["detail"]; d != "" {
			text = "disk " + d
		}
	case "out-of-memory":
		text = "memory ran out: the kernel stopped " + f["count"] + " of the agent's processes"
		if d := f["detail"]; d != "" {
			text = d
		}
	case "power-back":
		text = "back after an unclean shutdown (power loss or crash)"
		if d := f["detail"]; d != "" {
			text = d
		}
	case "apply-finished":
		text = "setup finished while you were away: " + f["detail"] + ", " + f["result"]
		if f["result"] == "interrupted" {
			text = "setup stopped as asked, after " + f["detail"]
		}
	case "apply-failed":
		text = "setup " + f["result"] + detail(": ") + fix
	case "install-request":
		// The package names passed root's check; the agent's reason is
		// not in the event.
		text = "the agent asks to install " + f["packages"] + ". Approve: " + f["fix"]
	case "test":
		text = "test message for channel " + f["channel"] + ": notifications from this box reach you"
	case "notify-throttled":
		text = "agent events throttled: " + f["count"] + " not sent" + detail("; ")
	default:
		text = e.Kind + detail(": ")
	}
	if t := f["text"]; t != "" && e.Source == events.SourceAgent {
		// The agent's own words, marked as its own and quoted, so they
		// cannot pass for keelbox's.
		text += ". " + who + " wrote: \"" + t + "\""
	}
	return Message{Title: plain.Line(e.Box + " · " + who), Text: plain.Line(text), Level: e.Level, Event: e}
}

// Digest renders held events as one message, at the highest of their
// levels: a count, then one line each.
func Digest(es []events.Event) Message {
	var lines []string
	level := events.Info
	for _, e := range es {
		m := Render(e)
		lines = append(lines, m.Line())
		level = max(level, e.Level)
	}
	box := ""
	if len(es) > 0 {
		box = es[0].Box
	}
	return Message{
		Title: box + " · keelbox",
		Text:  fmt.Sprintf("%d events during quiet hours:\n%s", len(es), strings.Join(lines, "\n")),
		Level: level,
	}
}

// truncate keeps at most n runes of s.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
