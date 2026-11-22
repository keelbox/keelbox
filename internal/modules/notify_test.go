package modules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/notify"
	"github.com/keelbox/keelbox/internal/system"
)

const notifyConfig = `notify:
  channels:
    - {name: phone, type: ntfy, topic_file: /run/keelbox/notify.phone.topic, token_file: /run/keelbox/notify.phone.token}
    - {name: ha, type: webhook, format: json, url_file: /run/keelbox/notify.ha.url}
`

func stageNotify(s *sim) {
	for _, n := range []string{"phone.topic", "phone.token", "ha.url"} {
		s.Files["/run/keelbox/notify."+n] = &system.FakeFile{Data: []byte("secret-" + n), Mode: 0o600, Owner: "root", Group: "root"}
	}
}

func TestNotifyInstallsSecretsAndHooks(t *testing.T) {
	s := newSim(t)
	stageNotify(s)
	e := simEnv(t, s, testConfig(t, notifyConfig))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"phone.topic", "phone.token", "ha.url"} {
		f := s.Files[notify.SecretDir+"/"+n]
		if f == nil || f.Mode != 0o600 || f.Owner != "root" || string(f.Data) != "secret-"+n {
			t.Errorf("%s: %+v", n, f)
		}
	}
	if d := s.Files[notify.SecretDir]; d == nil || d.Mode != 0o700 {
		t.Errorf("secret dir: %+v", d)
	}

	var hooks struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []Hook `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(s.content(claudeHooksFile)), &hooks); err != nil {
		t.Fatalf("hooks file: %v\n%s", err, s.content(claudeHooksFile))
	}
	for _, ev := range []string{"Stop", "StopFailure", "Notification"} {
		g := hooks.Hooks[ev]
		if len(g) != 1 || len(g[0].Hooks) != 1 || g[0].Hooks[0].Command != "/usr/local/bin/keelbox event --from claude-code" || !g[0].Hooks[0].Async || g[0].Hooks[0].Timeout != 10 {
			t.Errorf("claude %s hook: %+v", ev, g)
		}
	}
	codex := s.content(CodexRequirements)
	for _, want := range []string{"[[hooks.Stop]]", "[[hooks.PermissionRequest]]", `command = "/usr/local/bin/keelbox event --from codex"`, "async = true"} {
		if !strings.Contains(codex, want) {
			t.Errorf("codex requirements lack %q:\n%s", want, codex)
		}
	}
	for _, p := range []string{claudeHooksFile, CodexRequirements} {
		if f := s.Files[p]; f.Mode != 0o644 || f.Owner != "root" {
			t.Errorf("%s: %o %s", p, f.Mode, f.Owner)
		}
	}

	// The stage is cleared after apply; doctor from the applied config
	// keeps the installed copies and changes nothing.
	for _, n := range []string{"phone.topic", "phone.token", "ha.url"} {
		delete(s.Files, "/run/keelbox/notify."+n)
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK {
			t.Errorf("after the stage is gone, %s/%s pending: %s %v", it.Module, it.Step, it.Detail, it.Err)
		}
	}
	for _, f := range (Notify{}).Verify(ctx, e) {
		if f.Level == engine.LevelFail {
			t.Errorf("doctor: %+v", f)
		}
	}

	// agent_text adds --text; one channel fewer prunes its secret.
	e = simEnv(t, s, testConfig(t, "notify:\n  agent_text: true\n  channels:\n    - {name: phone, type: ntfy, topic_file: /run/keelbox/notify.phone.topic, token_file: /run/keelbox/notify.phone.token}\n"))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.content(claudeHooksFile), "event --from claude-code --text") {
		t.Errorf("agent_text: %s", s.content(claudeHooksFile))
	}
	if s.has(notify.SecretDir+"/ha.url") || !s.has(notify.SecretDir+"/phone.topic") {
		t.Error("removed channel's secret kept, or the remaining one removed")
	}

	// No channel: hooks and secrets go.
	e = simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if s.has(claudeHooksFile) || s.has(notify.SecretDir) || strings.Contains(s.content(CodexRequirements), "hooks") {
		t.Errorf("after removing every channel: hooks %v, secrets %v", s.has(claudeHooksFile), s.has(notify.SecretDir))
	}
}

func TestNotifyRefusesAReadableSecret(t *testing.T) {
	s := newSim(t)
	stageNotify(s)
	s.Files["/run/keelbox/notify.ha.url"].Mode = 0o644
	e := simEnv(t, s, testConfig(t, notifyConfig))
	_, err := engine.Apply(context.Background(), e, All())
	if err == nil || !strings.Contains(err.Error(), "notify.channels[1].url_file") || strings.Contains(err.Error(), "secret-ha.url") {
		t.Errorf("a readable secret: %v", err)
	}
}

func TestNotifyDoctorWarnsOnAFailedSend(t *testing.T) {
	s := newSim(t)
	stageNotify(s)
	e := simEnv(t, s, testConfig(t, notifyConfig))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	s.Files[notify.LastPath] = &system.FakeFile{Data: []byte(`{"ha":{"attempt":"2026-11-07T09:00:00Z","kind":"agent-done","error":"ha (webhook): HTTP 404: not found"}}`), Mode: 0o600, Owner: "root"}
	var got []string
	for _, f := range (Notify{}).Verify(context.Background(), e) {
		got = append(got, f.Level.String()+" "+f.Check+" "+f.Detail)
	}
	if !strings.Contains(strings.Join(got, "\n"), "warn ha last send failed: ha (webhook): HTTP 404") {
		t.Errorf("doctor: %v", got)
	}
}
