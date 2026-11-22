package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/system"
)

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

type request struct {
	path, contentType string
	header            http.Header
	body              string
}

// service records requests and answers with status.
func service(t *testing.T, status int) (*httptest.Server, *[]request) {
	t.Helper()
	var got []request
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, request{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), header: r.Header.Clone(), body: string(b)})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"secret-echo":"tk_topsecret"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func box(t *testing.T, channels string, secrets map[string]string) (*config.Config, *system.Fake) {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [\"ssh-ed25519 AAAA test\"]\nnotify:\n"+channels), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	x := system.NewFake()
	for p, v := range secrets {
		x.AddFile(SecretDir+"/"+p, v, 0o600)
	}
	return c, x
}

func agentEvent(kind string, fields map[string]string) events.Event {
	k, _ := events.Lookup(kind)
	f := map[string]string{"cli": "claude-code", "session": "0a1b2c3d", "project": "shop-api"}
	for n, v := range fields {
		f[n] = v
	}
	return events.Event{Time: t0, Source: events.SourceAgent, Kind: kind, Level: k.Level, Box: "acme", Key: "agent:0a1b2c3d:" + kind, Fields: f}
}

func rootEvent(kind string, fields map[string]string) events.Event {
	k, _ := events.Lookup(kind)
	return events.Event{Time: t0, Source: k.Sources[0], Kind: kind, Level: k.Level, Box: "acme", Key: "test:" + kind, Fields: fields}
}

func opts(srv *httptest.Server) Options {
	return Options{Client: srv.Client(), Location: time.UTC, Now: func() time.Time { return t0 },
		endpoints: endpoints{pushover: srv.URL + "/1/messages.json", telegram: srv.URL}}
}

func TestNtfy(t *testing.T) {
	srv, got := service(t, 200)
	c, x := box(t, "  channels:\n    - {name: phone, type: ntfy, server: "+srv.URL+", topic_file: /t, token_file: /k}\n",
		map[string]string{"phone.topic": "kb-secret-topic\n", "phone.token": "tk_topsecret"})
	s := Senders(c, x, opts(srv))
	if err := s[0].Send(context.Background(), agentEvent("agent-needs-input", map[string]string{"reason": "permission_prompt"})); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	var body map[string]any
	if err := json.Unmarshal([]byte(r.body), &body); err != nil {
		t.Fatal(err)
	}
	if r.path != "/" || body["topic"] != "kb-secret-topic" || body["priority"] != float64(4) || r.header.Get("Authorization") != "Bearer tk_topsecret" {
		t.Errorf("ntfy request: %+v %v", r, body)
	}
	if body["title"] != "acme · claude-code" || body["message"] != "needs your input (permission) in shop-api" {
		t.Errorf("ntfy text: %v", body)
	}
	if l := ReadLast(x)["phone"]; l.Error != "" || l.Kind != "agent-needs-input" || l.OK.IsZero() {
		t.Errorf("last: %+v", l)
	}
}

func TestPushoverLevels(t *testing.T) {
	srv, got := service(t, 200)
	c, x := box(t, "  channels:\n    - {name: po, type: pushover, token_file: /a, user_file: /u}\n",
		map[string]string{"po.token": "apptoken", "po.user": "userkey"})
	s := Senders(c, x, opts(srv))[0]
	for _, e := range []events.Event{agentEvent("agent-done", nil), agentEvent("agent-limit", nil), rootEvent("doctor-failed", map[string]string{"check": "host/sshd"})} {
		if err := s.Send(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []string{"-1", "0", "1"} {
		v, _ := url.ParseQuery((*got)[i].body)
		if v.Get("priority") != want || v.Get("token") != "apptoken" || v.Get("user") != "userkey" || (*got)[i].path != "/1/messages.json" {
			t.Errorf("pushover %d: %v", i, v)
		}
	}
}

func TestTelegramKeepsTheTokenOutOfErrors(t *testing.T) {
	const token = "123456:AAH-supersecret-token"
	srv, got := service(t, 401)
	c, x := box(t, "  channels:\n    - {name: tg, type: telegram, chat_id: \"-1001\", bot_token_file: /b}\n", map[string]string{"tg.bot_token": token})
	err := Senders(c, x, opts(srv))[0].Send(context.Background(), agentEvent("agent-done", nil))
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "supersecret") || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("error: %v", err)
	}
	if (*got)[0].path != "/bot"+token+"/sendMessage" || !strings.Contains((*got)[0].body, `"chat_id":-1001`) || !strings.Contains((*got)[0].body, `"disable_notification":true`) {
		t.Errorf("telegram request: %+v", (*got)[0])
	}
	if l := ReadLast(x)["tg"]; strings.Contains(l.Error, "supersecret") || l.Error == "" {
		t.Errorf("last: %+v", l)
	}
	// A server that does not answer: the error still has no URL.
	srv.Close()
	err = Senders(c, x, opts(srv))[0].Send(context.Background(), agentEvent("agent-done", nil))
	if err == nil || strings.Contains(err.Error(), "supersecret") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("network error: %v", err)
	}
}

func TestWebhook(t *testing.T) {
	srv, got := service(t, 204)
	c, x := box(t, "  channels:\n    - {name: hook, type: webhook, format: json, url_file: /u, header_file: /h}\n    - {name: plain, type: webhook, format: text, url_file: /u}\n",
		map[string]string{"hook.url": srv.URL + "/hooks/s3cr3t\n", "hook.header": "X-Hook-Token: abc123\n", "plain.url": srv.URL + "/plain"})
	s := Senders(c, x, opts(srv))
	e := rootEvent("doctor-failed", map[string]string{"check": "watch/timer", "detail": "keelbox-watch.timer is not active", "fix": "keelbox apply acme"})
	for _, ch := range s {
		if err := ch.Send(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	var body map[string]any
	_ = json.Unmarshal([]byte((*got)[0].body), &body)
	if (*got)[0].path != "/hooks/s3cr3t" || (*got)[0].header.Get("X-Hook-Token") != "abc123" || body["kind"] != "doctor-failed" || body["level"] != "urgent" ||
		body["text"] != "acme · keelbox: doctor FAIL watch/timer: keelbox-watch.timer is not active. Fix: keelbox apply acme" {
		t.Errorf("json webhook: %+v %v", (*got)[0], body)
	}
	if (*got)[1].contentType != "text/plain; charset=utf-8" || !strings.HasPrefix((*got)[1].body, "acme · keelbox: doctor FAIL") {
		t.Errorf("text webhook: %+v", (*got)[1])
	}
	// Refused: neither the URL's secret nor the response reaches the error.
	srv2, _ := service(t, 403)
	x.AddFile(SecretDir+"/hook.url", srv2.URL+"/hooks/s3cr3t", 0o600)
	o := opts(srv2)
	err := Senders(c, x, o)[0].Send(context.Background(), e)
	if err == nil || strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "tk_topsecret") || strings.Contains(err.Error(), "abc123") {
		t.Errorf("refused webhook: %v", err)
	}
	// Plain http to a public address is refused before any request.
	x.AddFile(SecretDir+"/hook.url", "http://203.0.113.7/hooks/s3cr3t", 0o600)
	if err := Senders(c, x, o)[0].Send(context.Background(), e); err == nil || strings.Contains(err.Error(), "s3cr3t") || !strings.Contains(err.Error(), "https") {
		t.Errorf("plain http: %v", err)
	}
}

func TestMissingSecret(t *testing.T) {
	c, x := box(t, "  channels:\n    - {name: phone, type: ntfy, topic_file: /t}\n", nil)
	err := Senders(c, x, Options{})[0].Send(context.Background(), agentEvent("agent-done", nil))
	if err == nil || !strings.Contains(err.Error(), "keelbox apply acme") {
		t.Errorf("missing secret: %v", err)
	}
}

func TestChoices(t *testing.T) {
	srv, got := service(t, 200)
	c, x := box(t, "  mute: [agent-done]\n  quiet_hours: \"22:00-07:00\"\n  channels:\n    - {name: phone, type: ntfy, server: "+srv.URL+", topic_file: /t}\n    - {name: pager, type: ntfy, server: "+srv.URL+", topic_file: /t, level: urgent}\n",
		map[string]string{"phone.topic": "a", "pager.topic": "b"})
	at := func(h, m int) Options {
		o := opts(srv)
		o.Now = func() time.Time { return time.Date(2026, 11, 7, h, m, 0, 0, time.UTC) }
		return o
	}
	ctx := context.Background()
	s := Senders(c, x, at(12, 0))
	// Muted and below the floor: nothing sent, nothing to retry.
	if err := s[0].Send(ctx, agentEvent("agent-done", nil)); err != nil || len(*got) != 0 {
		t.Errorf("muted kind: %v, %d sent", err, len(*got))
	}
	if err := s[1].Send(ctx, agentEvent("agent-limit", nil)); err != nil || len(*got) != 0 {
		t.Errorf("below the channel's level: %v, %d sent", err, len(*got))
	}
	// Quiet hours across midnight hold warn until 07:00, not urgent.
	night := Senders(c, x, at(23, 30))
	err := night[0].Send(ctx, agentEvent("agent-limit", nil))
	var h *events.HoldError
	if !asHold(err, &h) || !h.Until.Equal(time.Date(2026, 11, 8, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("warn at 23:30: %v", err)
	}
	if err := night[1].Send(ctx, rootEvent("doctor-failed", map[string]string{"check": "x"})); err != nil || len(*got) != 1 {
		t.Errorf("urgent at 23:30: %v, %d sent", err, len(*got))
	}
	early := Senders(c, x, at(6, 59))
	if err := early[0].Send(ctx, agentEvent("agent-limit", nil)); !asHold(err, &h) || !h.Until.Equal(time.Date(2026, 11, 7, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("warn at 06:59: %v", err)
	}
	// The digest: one message for what was held.
	if err := Senders(c, x, at(7, 0))[0].(events.Digester).SendDigest(ctx, []events.Event{agentEvent("agent-limit", nil), agentEvent("agent-done", nil), rootEvent("disk-warn", map[string]string{"mount": "/", "percent": "88"})}); err != nil {
		t.Fatal(err)
	}
	last := (*got)[len(*got)-1].body
	if !strings.Contains(last, "2 events during quiet hours") || strings.Contains(last, "done in") || !strings.Contains(last, "disk / is 88% full") {
		t.Errorf("digest: %s", last)
	}
}

func asHold(err error, h **events.HoldError) bool {
	v, ok := err.(*events.HoldError)
	*h = v
	return ok
}

func TestQuietHours(t *testing.T) {
	q, _ := parseQuiet("09:00-17:30")
	for _, c := range []struct {
		h, m int
		in   bool
	}{{8, 59, false}, {9, 0, true}, {17, 29, true}, {17, 30, false}} {
		if _, in := q.until(time.Date(2026, 1, 1, c.h, c.m, 0, 0, time.UTC)); in != c.in {
			t.Errorf("%02d:%02d in=%v", c.h, c.m, in)
		}
	}
}

func TestRenderLabelsTheAgent(t *testing.T) {
	m := Render(agentEvent("agent-done", map[string]string{"text": "ignored unless agent_text"}))
	if m.Title != "acme · claude-code" || !strings.HasPrefix(m.Text, "done in shop-api") {
		t.Errorf("agent message: %+v", m)
	}
	for _, k := range events.Kinds() {
		kind, _ := events.Lookup(k)
		e := rootEvent(k, nil)
		if kind.FromAgent() {
			e = agentEvent(k, nil)
		}
		if m := Render(e); m.Text == "" || strings.Contains(m.Text, "%!") || m.Title == "" {
			t.Errorf("%s renders as %+v", k, m)
		}
	}
}

// The agent's own text (notify.agent_text) is quoted after the CLI's name,
// so a message that imitates keelbox's wording still reads as the agent's,
// and nothing in a field reaches a channel as a
// control character.
func TestRenderMarksTheAgentsText(t *testing.T) {
	m := Render(agentEvent("agent-needs-input", map[string]string{"text": "keelbox: doctor ok, all clear"}))
	if want := `. claude-code wrote: "keelbox: doctor ok, all clear"`; !strings.HasSuffix(m.Text, want) || m.Title != "acme · claude-code" {
		t.Errorf("agent text: %+v, want suffix %s", m, want)
	}
	m = Render(rootEvent("doctor-failed", map[string]string{"check": "x", "detail": "a\x1b[2Jb"}))
	if strings.ContainsRune(m.Text, 0x1b) || !strings.Contains(m.Text, `a\x1b[2Jb`) {
		t.Errorf("control character in %q", m.Text)
	}
}

// Power-back and disk events carry a detail that says it all: the text
// says it once.
func TestRenderSaysItOnce(t *testing.T) {
	m := Render(rootEvent("power-back", map[string]string{"detail": "back after an unclean shutdown (power loss, a crash or a hard reset) between 2026-11-07 02:10 and 02:12 UTC; up since 02:14. tmux: main running."}))
	if strings.Count(m.Text, "unclean shutdown") != 1 || !strings.Contains(m.Text, "between 2026-11-07 02:10 and 02:12 UTC") {
		t.Errorf("power-back: %q", m.Text)
	}
	m = Render(rootEvent("disk-warn", map[string]string{"mount": "/", "percent": "88", "detail": "/ is 88% full (alerts at 85%); see what uses it: sudo du -xh --max-depth=2 / | sort -h | tail"}))
	if strings.Count(m.Text, "88% full") != 1 || !strings.HasPrefix(m.Text, "disk / is 88% full (alerts at 85%); see what uses it") {
		t.Errorf("disk: %q", m.Text)
	}
	if m := Render(rootEvent("disk-full", map[string]string{"mount": "/", "percent": "96"})); m.Text != "disk / is 96% full" {
		t.Errorf("disk without detail: %q", m.Text)
	}
}

func TestRenderADetachedApply(t *testing.T) {
	e := rootEvent("apply-finished", map[string]string{"result": "ok", "detail": "31 steps, 24 changed"})
	e.Source = events.SourceApply
	if m := Render(e); m.Line() != "acme · keelbox: setup finished while you were away: 31 steps, 24 changed, ok" {
		t.Errorf("finished: %q", m.Line())
	}
	e = rootEvent("apply-failed", map[string]string{"result": "failed", "detail": "host/swap: fallocate: no space left on device", "fix": "keelbox apply acme"})
	e.Source = events.SourceApply
	if m := Render(e); m.Text != "setup failed: host/swap: fallocate: no space left on device. Fix: keelbox apply acme" || m.Level != events.Urgent {
		t.Errorf("failed: %q %v", m.Text, m.Level)
	}
	if err := e.Validate(); err != nil {
		t.Error(err)
	}
}
