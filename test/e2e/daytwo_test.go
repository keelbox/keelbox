//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// agentEventLine prints one agent events line with the box's current time.
const agentEventLine = `line() { printf '{"v":1,"time":"%s","cli":"claude-code","kind":"%s","session":"0a1b2c3d","project":"e2e"%s}\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" "$1" "$2"; }; f=~/.local/state/keelbox/events.jsonl; `

// dayTwoChecks cover the day-two units (ADR 0030, 0032): the watch run
// writes its heartbeat and a status snapshot the admin reads without sudo;
// the events service turns the agent's lines into queued events, drops
// forged ones, and gets nothing from a link to a root-only file; the agent
// cannot read the queue. With KEELBOX_E2E_UPGRADE_FROM, a rollback to that
// keelbox reads the logbook correctly and leaves the units skipped (a
// keelbox that knows no day-two command) or running, never failed.
func dayTwoChecks(t *testing.T, host, box, cfg string) {
	t.Helper()
	must(t, host, "admin", "systemctl is-active keelbox-watch.timer keelbox-events.path", "active\nactive")
	must(t, host, "admin", "sudo systemctl start keelbox-watch.service && systemctl show -p Result keelbox-watch.service", "Result=success")
	raw := must(t, host, "admin", "cat /var/lib/keelbox/status.json") // no sudo: the admin reads it over plain ssh
	s, err := snapshot.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("status snapshot: %v\n%s", err, raw)
	}
	if s.Box != box || s.LastApply == nil || s.LastApply.Result != "ok" || len(s.Disk) == 0 || s.Power == nil || time.Since(s.Written) > time.Minute {
		t.Errorf("status snapshot: %+v", s)
	}
	if strings.Contains(raw, "://") || strings.Contains(raw, "/etc/keelbox/backup") {
		t.Errorf("status snapshot names a URL or a credential file:\n%s", raw)
	}
	must(t, host, "admin", "sudo cat /var/lib/keelbox/watch/state.json", `"heartbeat"`)
	logf(t, "PASS watch run: heartbeat and status snapshot (schema %d, %d mount(s))", s.Schema, len(s.Disk))

	// The agent writes one real event and two forged ones; the path unit
	// starts the events service, which queues only the real one.
	must(t, host, "agent", agentEventLine+`line doctor-failed '' >> $f; line agent-done '' >> $f; line agent-done ',"level":"urgent"' >> $f`)
	q := waitQueue(t, host, 1)
	if len(q) == 0 {
		t.Fatal("no agent event queued")
	}
	if e := q[0].Event; len(q) != 1 || e.Kind != "agent-done" || e.Source != events.SourceAgent || e.Level != events.Info ||
		e.Box != box || e.Key != "agent:0a1b2c3d:agent-done" || e.Fields["project"] != "e2e" {
		t.Errorf("queue after the agent's lines: %+v", q)
	}
	must(t, host, "admin", "sudo journalctl -u keelbox-events.service --no-pager -n 20", "1 event(s) queued, 2 line(s) skipped")
	logf(t, "PASS agent event queued by the path unit; forged root kind and level dropped")

	// A link to /etc/shadow in place of the file: the read runs as the
	// agent, so root reads nothing.
	must(t, host, "agent", "f=~/.local/state/keelbox/events.jsonl; rm -f $f && ln -s /etc/shadow $f")
	must(t, host, "admin", "sudo systemctl start keelbox-events.service; sudo journalctl -u keelbox-events.service --no-pager -n 5", "is a symbolic link")
	if q := agentOnly(readQueue(t, host)); len(q) != 1 {
		t.Errorf("a link to /etc/shadow changed the queue: %+v", q)
	}
	if out, _ := sshRun(t, host, "admin", nil, "sudo grep -rl '^root:' /var/lib/keelbox/notify || echo none"); !strings.Contains(out, "none") {
		t.Errorf("shadow content in the notify state: %s", out)
	}
	must(t, host, "agent", "rm -f ~/.local/state/keelbox/events.jsonl")
	must(t, host, "agent", "ls /var/lib/keelbox/notify 2>&1 || echo denied", "denied")
	must(t, host, "admin", "systemctl is-active keelbox-events.path", "active")
	logf(t, "PASS events: a link to /etc/shadow reads nothing; the agent cannot read the queue")

	if os.Getenv("KEELBOX_E2E_UPGRADE_FROM") != "" {
		rollbackToOld(t, host, box, cfg)
	}
}

// rollbackToOld checks what ADR 0031 and 0030 promise a box rolled back to
// the keelbox it was upgraded from, built by upgradeFrom.
func rollbackToOld(t *testing.T, host, box, cfg string) {
	t.Helper()
	old, oldCfg := filepath.Join(workDir, "keelbox-old"), filepath.Join(workDir, "old.yaml")
	record := func(release string) string {
		return `rel=` + release + `; sudo jq -nc --arg r "$rel" --arg t "$(date -u +%FT%TZ)" '{time:$t,kind:"watch",box:"` + box +
			`",keelbox:"dev",release:$r,config_hash:"",result:"ok",checked:0,changes:[{module:"watch",step:"tmux-server",detail:"e2e: a day-two record"}],seconds:0}' | sudo tee -a /var/lib/keelbox/logbook.jsonl`
	}
	// A day-two record is the newest line: the old keelbox still reads the
	// box's release from it.
	must(t, host, "admin", record(`$(sudo jq -r 'select(.kind == null) | .release' /var/lib/keelbox/logbook.jsonl | tail -1)`), `"kind":"watch"`)
	for _, cmd := range []string{"doctor", "status"} {
		if out, _ := run(t, old, cmd, oldCfg); strings.Contains(out, "panic") || strings.Contains(out, "logbook") && strings.Contains(out, "error") {
			t.Errorf("old keelbox %s over a day-two record:\n%s", cmd, out)
		}
	}
	// A keelbox with has-command knows the day-two records and commands;
	// one without it does not.
	_, err := run(t, old, "has-command", "watch-run")
	knows := err == nil
	// A newer release in the newest record: a keelbox that knows no kinds
	// reads the release from the last line, so its guard refuses. One that
	// knows them reads apply records only (unit-tested); an apply with it
	// here would move the box back, so it is not run.
	if !knows {
		must(t, host, "admin", record("2099.1.0"))
		if out, err := run(t, old, "apply", oldCfg); err == nil || !strings.Contains(out, "--allow-downgrade") {
			t.Errorf("the old keelbox's downgrade guard did not read a day-two record's release: %v\n%s", err, out)
		}
		must(t, host, "admin", "sudo sed -i '$ d' /var/lib/keelbox/logbook.jsonl")
	}
	logf(t, "PASS old keelbox reads a logbook with day-two records; its downgrade guard holds")

	must(t, host, "admin", "sudo keelbox rollback")
	// ExecCondition exits 2 for a keelbox that lacks the command: systemd
	// skips the run.
	must(t, host, "admin", "keelbox has-command watch-run; echo exit=$?", map[bool]string{true: "exit=0", false: "exit=2"}[knows])
	if out := must(t, host, "admin", "sudo systemctl start keelbox-watch.service || true; systemctl show -p Result keelbox-watch.service; systemctl is-failed keelbox-watch.service || true"); strings.Contains(out, "\nfailed") ||
		knows && !strings.Contains(out, "Result=success") {
		t.Errorf("the watch service under the old keelbox:\n%s", out)
	}
	must(t, host, "agent", agentEventLine+`line agent-done '' >> $f`)
	time.Sleep(3 * time.Second)
	out := must(t, host, "admin", "systemctl is-active keelbox-events.path; systemctl is-failed keelbox-events.service keelbox-watch.service || true")
	if !strings.HasPrefix(out, "active") || strings.Contains(out, "\nfailed") {
		t.Errorf("after a rollback to the old keelbox, day-two units:\n%s", out)
	}
	logf(t, "PASS rolled back to the old keelbox: day-two units %s, none failed", map[bool]string{true: "run", false: "skipped"}[knows])
	if out, err := kb(t, "apply", cfg); err != nil || !strings.Contains(out, "keelbox-binary") {
		t.Fatalf("apply forward after the rollback: %v\n%s", err, out)
	}
	must(t, host, "admin", "keelbox has-command watch-run && echo known", "known")
	logf(t, "PASS applied forward again")
}

func readQueue(t *testing.T, host string) []events.Queued {
	t.Helper()
	out, _ := sshRun(t, host, "admin", nil, "sudo sh -c 'cat /var/lib/keelbox/notify/queue/*.json 2>/dev/null' || true")
	var q []events.Queued
	dec := json.NewDecoder(bytes.NewReader([]byte(out)))
	for {
		var e events.Queued
		if err := dec.Decode(&e); errors.Is(err, io.EOF) {
			return q
		} else if err != nil {
			t.Fatalf("queue: %v\n%s", err, out)
		}
		q = append(q, e)
	}
}

// agentOnly keeps the agent's events: scheduled doctor may have queued its
// own by then.
func agentOnly(q []events.Queued) []events.Queued {
	var out []events.Queued
	for _, e := range q {
		if e.Event.Source == events.SourceAgent {
			out = append(out, e)
		}
	}
	return out
}

// waitQueue waits for n agent events in the queue.
func waitQueue(t *testing.T, host string, n int) []events.Queued {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		q := agentOnly(readQueue(t, host))
		if len(q) >= n || time.Now().After(deadline) {
			if len(q) < n {
				t.Fatalf("the events service queued %d event(s) in 20s, want %d", len(q), n)
			}
			return q
		}
		time.Sleep(time.Second)
	}
}
