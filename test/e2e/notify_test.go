//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// receiver is a webhook receiver on the box's loopback: it appends each
// request's body as one line to /var/tmp/kb-e2e-hooks.jsonl and answers 204.
// The webhook channel reaches it over plain http, which keelbox allows only
// to loopback and the tailnet.
const receiver = `import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        with open("/var/tmp/kb-e2e-hooks.jsonl", "ab") as f:
            f.write(body.replace(b"\n", b" ") + b"\n")
        self.send_response(204)
        self.end_headers()
    def log_message(self, *a):
        pass
http.server.HTTPServer(("127.0.0.1", 9099), H).serve_forever()
`

// delivery is one request the receiver got.
type delivery struct {
	Kind   string            `json:"kind"`
	Level  string            `json:"level"`
	Source string            `json:"source"`
	Key    string            `json:"key"`
	Text   string            `json:"text"`
	Fields map[string]string `json:"fields"`
}

func deliveries(t *testing.T, host string) []delivery {
	t.Helper()
	out, _ := sshRun(t, host, "admin", nil, "cat /var/tmp/kb-e2e-hooks.jsonl 2>/dev/null || true")
	var ds []delivery
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var d delivery
		if err := json.Unmarshal([]byte(l), &d); err != nil {
			t.Fatalf("receiver line %q: %v", l, err)
		}
		ds = append(ds, d)
	}
	return ds
}

func count(ds []delivery, match func(delivery) bool) int {
	n := 0
	for _, d := range ds {
		if match(d) {
			n++
		}
	}
	return n
}

// waitFor waits up to wait for at least n deliveries matching match.
func waitFor(t *testing.T, host string, n int, wait time.Duration, what string, match func(delivery) bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		ds := deliveries(t, host)
		if count(ds, match) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d deliveries in %s, want %d; got:\n%+v", what, count(ds, match), wait, n, ds)
		}
		time.Sleep(time.Second)
	}
}

// TestNotify is notifications and scheduled doctor on a fresh LXD VM
// (ADR 0042, 0043): keelbox notify test over a webhook on the box's
// loopback and a local ntfy container; a managed Claude Code hook (a real
// claude -p with a bogus key ends in StopFailure) and a Codex hook give
// exactly one delivery each; forged agent lines raise nothing; doctor
// transitions alert once each way; the secrets appear nowhere they should
// not. No service outside the VM is used.
func TestNotify(t *testing.T) {
	tg := pickTarget(t)
	if _, ok := tg.(*lxdTarget); !ok {
		t.Skip("notifications e2e runs on LXD")
	}
	ensureKey(t)
	build(t)
	box := fmt.Sprintf("%s%d", VMPrefix, time.Now().Unix()%1000000)
	defer func() {
		if p := os.Getenv("KEELBOX_E2E_LOG"); p != "" {
			_ = os.WriteFile(p, transcript.Bytes(), 0o600)
		}
	}()
	targetYAML, bootstrap, host := tg.Create(t, box)
	defer func() {
		if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
			logf(t, "KEELBOX_E2E_KEEP=1: leaving %s", box)
			return
		}
		tg.Destroy(t, box)
	}()

	topic, hookPath := "kbe2e"+randomHex(t, 12), "/hook/"+randomHex(t, 16)
	secrets := map[string]string{
		"hook.url":   "http://127.0.0.1:9099" + hookPath + "\n",
		"ntfy.topic": topic + "\n",
	}
	for name, v := range secrets {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(workDir, box+".yaml")
	yaml := fmt.Sprintf(`version: 1
box: %s
%s
users:
  admin:
    ssh_authorized_key_files: [%s.pub]
  agent:
    ssh_authorized_key_files: [%s.pub]
agents: [claude-code, codex]
notify:
  channels:
    - {name: hook, type: webhook, format: json, url_file: %s}
    - {name: ntfy, type: ntfy, server: "http://127.0.0.1:8090", topic_file: %s}
`, box, targetYAML, keyPath, keyPath, filepath.Join(workDir, "hook.url"), filepath.Join(workDir, "ntfy.topic"))
	if err := os.WriteFile(cfg, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := kb(t, "apply", cfg, "--ssh-user", bootstrap)
	if err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	if out, err := kb(t, "apply", cfg); err != nil || !strings.Contains(out, "No changes.") {
		t.Fatalf("second apply not a no-op: %v\n%s", err, out)
	}
	logf(t, "PASS apply with notify channels, then no changes")

	// The secret copies are root's; the agent reads neither them nor the
	// queue; the hooks are root's files that parse.
	must(t, host, "admin", "sudo stat -c '%a %U' /etc/keelbox/notify /etc/keelbox/notify/hook.url /etc/keelbox/notify/ntfy.topic", "700 root\n600 root\n600 root")
	must(t, host, "agent", "cat /etc/keelbox/notify/hook.url 2>&1 || echo denied", "denied")
	must(t, host, "admin", "jq -r '.hooks.Stop[0].hooks[0].command, .hooks.StopFailure[0].hooks[0].async' /etc/claude-code/managed-settings.d/51-keelbox-hooks.json",
		"/usr/local/bin/keelbox event --from claude-code\ntrue")
	must(t, host, "admin", "grep -c 'keelbox event --from codex' /etc/codex/requirements.toml", "2")
	must(t, host, "admin", "systemctl is-active keelbox-doctor.timer keelbox-events.path", "active\nactive")
	logf(t, "PASS secrets 600 root, unreadable by the agent; managed hooks for both CLIs; doctor timer active")

	// The receiver and a local ntfy, both on the box's loopback only.
	must(t, host, "admin", "cat > /var/tmp/kb-e2e-recv.py <<'EOF'\n"+receiver+"EOF\nsudo systemd-run --unit kb-e2e-recv --uid admin python3 /var/tmp/kb-e2e-recv.py && sleep 1 && systemctl is-active kb-e2e-recv", "active")
	must(t, host, "admin", "sudo docker run -d --name kb-e2e-ntfy -p 127.0.0.1:8090:80 binwiederhier/ntfy serve >/dev/null && "+
		"for i in $(seq 30); do curl -fs http://127.0.0.1:8090/v1/health && break; sleep 1; done", "healthy")

	if out, err := kb(t, "notify", "test", cfg); err != nil || !strings.Contains(out, "hook (webhook)") || !strings.Contains(out, "Sent a test message on 2 channels.") {
		t.Fatalf("notify test: %v\n%s", err, out)
	}
	waitFor(t, host, 1, 10*time.Second, "test on the webhook", func(d delivery) bool { return d.Kind == "test" })
	must(t, host, "admin", "curl -fs 'http://127.0.0.1:8090/"+topic+"/json?poll=1'", "test message for channel ntfy")
	logf(t, "PASS keelbox notify test: webhook and ntfy got the test message")
	squatChecks(t, host, cfg, hookPath, topic)

	// A real managed hook: claude -p with a bogus key ends its turn with
	// StopFailure (authentication_failed), which keelbox reports as a limit.
	_, _ = sshRun(t, host, "agent", nil, "cd /tmp && mkdir -p shop-api && cd shop-api && ANTHROPIC_API_KEY=sk-ant-e2e-bogus timeout 120 claude -p hello >/dev/null 2>&1; true")
	isLimit := func(d delivery) bool { return d.Kind == "agent-limit" && d.Source == "agent" }
	waitFor(t, host, 1, 30*time.Second, "agent-limit from the managed StopFailure hook", isLimit)
	must(t, host, "admin", "sudo systemctl start keelbox-watch.service") // a second dispatch must not repeat it
	time.Sleep(5 * time.Second)
	ds := deliveries(t, host)
	if n := count(ds, isLimit); n != 1 {
		t.Errorf("the StopFailure hook was delivered %d times, want once: %+v", n, ds)
	}
	for _, d := range ds {
		if isLimit(d) && (d.Fields["reason"] != "authentication_failed" || d.Fields["cli"] != "claude-code" || d.Fields["project"] != "shop-api" || !strings.Contains(d.Text, "· claude-code: usage limit hit")) {
			t.Errorf("limit delivery: %+v", d)
		}
	}
	must(t, host, "admin", "curl -fs 'http://127.0.0.1:8090/"+topic+"/json?poll=1'", "usage limit hit")
	logf(t, "PASS managed Claude Code hook (StopFailure) delivered exactly once to the webhook, and to ntfy")

	// The Codex hook, run as the managed file says, with Codex's input.
	must(t, host, "agent", `cmd=$(sed -n 's/^command = "\(.*keelbox event --from codex\)"$/\1/p' /etc/codex/requirements.toml | head -1); `+
		`echo '{"hook_event_name":"PermissionRequest","session_id":"e2e-codex","cwd":"/home/agent/api"}' | $cmd`)
	isCodex := func(d delivery) bool { return d.Kind == "agent-needs-input" && d.Fields["cli"] == "codex" }
	waitFor(t, host, 1, 20*time.Second, "agent-needs-input from the Codex hook", isCodex)
	logf(t, "PASS Codex PermissionRequest hook delivered as agent-needs-input")

	// Forged lines: a root kind, a level, a source, text with agent_text
	// off. None becomes a root event; the text never leaves the box.
	must(t, host, "agent", agentEventLine+`line doctor-failed '' >> $f; line watch-gave-up '' >> $f; line agent-done ',"level":"urgent"' >> $f; line agent-done ',"source":"doctor"' >> $f; `+
		`printf '{"v":1,"time":"%s","cli":"claude-code","kind":"agent-done","session":"fe11fe11","project":"e2e","text":"FORGED-TEXT"}\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" >> $f`)
	isForgedDone := func(d delivery) bool { return d.Kind == "agent-done" && d.Fields["session"] == "fe11fe11" }
	waitFor(t, host, 1, 20*time.Second, "the one valid line", isForgedDone)
	time.Sleep(3 * time.Second)
	ds = deliveries(t, host)
	for _, d := range ds {
		if d.Source == "agent" && !strings.HasPrefix(d.Kind, "agent-") || d.Kind == "watch-gave-up" || d.Kind == "doctor-failed" && d.Source != "doctor" || strings.Contains(d.Text, "FORGED-TEXT") {
			t.Errorf("a forged line got through: %+v", d)
		}
		if d.Source == "agent" && d.Level == "urgent" {
			t.Errorf("an agent event at urgent: %+v", d)
		}
	}
	if count(ds, isForgedDone) != 1 || count(ds, func(d delivery) bool { return d.Kind == "watch-gave-up" }) != 0 {
		t.Errorf("forged lines: %+v", ds)
	}
	must(t, host, "admin", "sudo journalctl -u keelbox-events.service --no-pager -n 40", "is not an agent event")
	logf(t, "PASS forged root kinds, levels and sources dropped; agent text kept off the channel")

	// Scheduled doctor: stop the watch timer, a FAIL; once, however many
	// runs; start it again, cleared once.
	doctor := func() string {
		return must(t, host, "admin", "sudo systemctl start keelbox-doctor.service; sudo journalctl -u keelbox-doctor.service --no-pager -n 3 -o cat")
	}
	doctor()
	isWatchFail := func(d delivery) bool { return d.Kind == "doctor-failed" && d.Fields["check"] == "watch/timer" }
	isWatchClear := func(d delivery) bool { return d.Kind == "doctor-cleared" && d.Fields["check"] == "watch/timer" }
	must(t, host, "admin", "sudo systemctl stop keelbox-watch.timer")
	doctor()
	waitFor(t, host, 1, 15*time.Second, "doctor-failed watch/timer", isWatchFail)
	doctor()
	time.Sleep(2 * time.Second)
	if n := count(deliveries(t, host), isWatchFail); n != 1 {
		t.Errorf("doctor-failed sent %d times over two failing runs, want once", n)
	}
	must(t, host, "admin", "sudo systemctl start keelbox-watch.timer")
	doctor()
	waitFor(t, host, 1, 15*time.Second, "doctor-cleared watch/timer", isWatchClear)
	doctor()
	time.Sleep(2 * time.Second)
	ds = deliveries(t, host)
	if count(ds, isWatchFail) != 1 || count(ds, isWatchClear) != 1 {
		t.Errorf("doctor transitions: %d failed, %d cleared, want 1 and 1", count(ds, isWatchFail), count(ds, isWatchClear))
	}
	for _, d := range ds {
		if isWatchFail(d) && (d.Level != "urgent" || !strings.Contains(d.Text, "doctor FAIL watch/timer")) {
			t.Errorf("doctor-failed delivery: %+v", d)
		}
	}
	must(t, host, "admin", "sudo grep -c '\"kind\":\"doctor\"' /var/lib/keelbox/logbook.jsonl")
	must(t, host, "admin", "cat /var/lib/keelbox/doctor/last.json", `"seconds"`)
	must(t, host, "admin", "sudo systemctl start keelbox-watch.service && jq -e '.doctor.time' /var/lib/keelbox/status.json")
	logf(t, "PASS scheduled doctor: watch timer stopped alerted once, restarted cleared once; logbook, last.json and snapshot")

	out, err = kb(t, "status", cfg)
	if err != nil || !strings.Contains(out, "notify") || !strings.Contains(out, "2 channels; last sent") || !strings.Contains(out, "doctor") {
		t.Errorf("status: %v\n%s", err, out)
	}
	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "notify") {
		t.Errorf("doctor: %v\n%s", err, out)
	}
	logf(t, "PASS status and doctor show notify and scheduled doctor")

	// The secrets: nowhere but root's copies. The search reads them from a
	// file, so neither sudo's journal lines nor a command line carries them.
	plan, _ := kb(t, "plan", cfg)
	for _, s := range []string{topic, strings.TrimPrefix(hookPath, "/hook/")} {
		if strings.Contains(transcript.String(), s) || strings.Contains(plan, s) {
			t.Errorf("a secret is in keelbox's output")
		}
	}
	must(t, host, "admin", "printf '%s\\n%s\\n' "+topic+" "+strings.TrimPrefix(hookPath, "/hook/")+" > /var/tmp/kb-e2e-secrets")
	out = must(t, host, "admin", "f=/var/tmp/kb-e2e-secrets; echo files: $(sudo grep -rlFf $f /var/lib/keelbox /etc/keelbox/keelbox.yaml /etc/claude-code /etc/codex 2>/dev/null | wc -l); "+
		"echo journal: $(sudo journalctl --no-pager -o cat | grep -cFf $f); echo cmdline: $(sudo sh -c 'cat /proc/[0-9]*/cmdline 2>/dev/null' | tr '\\0' '\\n' | grep -cFf $f)")
	if !strings.Contains(out, "files: 0") || !strings.Contains(out, "journal: 0") || !strings.Contains(out, "cmdline: 0") {
		t.Errorf("a secret on the box outside /etc/keelbox/notify:\n%s", out)
	}
	logf(t, "PASS channel secrets in no output, logbook, state, policy file, journal or command line")
}
