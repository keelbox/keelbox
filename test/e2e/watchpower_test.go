//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/snapshot"
)

// watchdogChecks: the agent's tmux server dies (tmux kill-server, as the
// agent); within three minutes the watch run has restarted it, Continuum
// has brought back the sessions from the last save, and the logbook and
// the event queue say so (ADR 0039).
func watchdogChecks(t *testing.T, host, cfg string) {
	t.Helper()
	count := func(remote string) int {
		out, _ := sshRun(t, host, "admin", nil, remote)
		n := 0
		_, _ = fmt.Sscan(strings.TrimSpace(out), &n)
		return n
	}
	watchRecords := func() int { return count(`sudo grep -c '"kind":"watch"' /var/lib/keelbox/logbook.jsonl`) }
	recovered := func() int {
		return count("sudo sh -c 'cat /var/lib/keelbox/notify/queue/*.json 2>/dev/null' | grep -c watch-recovered")
	}
	records, events := watchRecords(), recovered()
	must(t, host, "agent", "tmux new-session -d -s wd -c /tmp && tmux new-window -t wd && ~/.tmux/plugins/tmux-resurrect/scripts/save.sh && "+
		"tmux list-windows -t wd | wc -l", "2")
	must(t, host, "agent", "tmux kill-server; sleep 1; tmux has-session 2>/dev/null || echo gone", "gone")
	killed := time.Now()
	deadline := killed.Add(3*time.Minute + 30*time.Second)
	for {
		out, _ := sshRun(t, host, "agent", nil, "tmux list-windows -t wd 2>/dev/null | wc -l")
		if strings.TrimSpace(out) == "2" {
			break
		}
		if time.Now().After(deadline) {
			j, _ := sshRun(t, host, "admin", nil, "sudo journalctl -u keelbox-watch.service --no-pager -n 30")
			t.Fatalf("tmux not back with session wd %s after kill-server; watch journal:\n%s", time.Since(killed).Round(time.Second), j)
		}
		time.Sleep(5 * time.Second)
	}
	logf(t, "PASS watchdog: tmux server back with session wd (2 windows) %s after kill-server", time.Since(killed).Round(time.Second))
	must(t, host, "agent", "tmux has-session -t main && echo main", "main")
	must(t, host, "admin", `sudo grep '"kind":"watch"' /var/lib/keelbox/logbook.jsonl | tail -1`, `"step":"tmux"`, "the tmux server was gone", `"result":"ok"`)
	if watchRecords() != records+1 || recovered() != events+1 {
		t.Errorf("after the restart: %d watch records (was %d), %d watch-recovered events (was %d)", watchRecords(), records, recovered(), events)
	}
	if out, err := kb(t, "status", cfg); err != nil || !strings.Contains(out, "last action") || !strings.Contains(out, "the tmux server was gone") {
		t.Errorf("status after the restart: %v\n%s", err, out)
	}
	logf(t, "PASS watchdog: logbook record, watch-recovered queued, status shows the last action")
	// A running server is left alone: another watch run restarts nothing.
	must(t, host, "admin", "sudo systemctl start keelbox-watch.service")
	if n := watchRecords(); n != records+1 {
		t.Errorf("a watch run over a running tmux server recorded an action (%d records, want %d)", n, records+1)
	}
}

// bootID reads the box's boot id, or "" when it does not answer.
func bootIDOf(t *testing.T, host string) string {
	out, err := sshRun(t, host, "admin", []string{"-o", "ConnectTimeout=5"}, "cat /proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// waitBoot waits for the box to answer with a boot id other than before.
func waitBoot(t *testing.T, host, before string) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < 6*time.Minute {
		time.Sleep(5 * time.Second)
		if id := bootIDOf(t, host); id != "" && id != before {
			_, _ = sshRun(t, host, "admin", nil, "systemctl is-system-running --wait >/dev/null 2>&1; true")
			return
		}
	}
	t.Fatal("box did not come back")
}

// powerChecks: a clean restart (lxc restart) records nothing; a hard reset
// (lxc stop --force, the VM's power pulled) leaves a power record in the
// logbook, a power-back event once the box has settled, the snapshot's
// unclean flag, and the agent's sessions back (ADR 0040).
func powerChecks(t *testing.T, host, box, cfg string) {
	t.Helper()
	before := bootIDOf(t, host)
	lxc(t, "restart", box)
	waitBoot(t, host, before)
	must(t, host, "admin", "sudo journalctl -b -u keelbox-boot.service --no-pager -o cat", "the last boot ended with a clean shutdown")
	if n := must(t, host, "admin", `sudo grep -c '"kind":"power"' /var/lib/keelbox/logbook.jsonl || true`); strings.TrimSpace(n) != "0" {
		t.Fatalf("a clean restart left %s power record(s)", strings.TrimSpace(n))
	}
	logf(t, "PASS power: a clean restart records nothing")

	// Let a watch run write this boot's heartbeat, then pull the power.
	must(t, host, "admin", "sudo systemctl start keelbox-watch.service")
	before = bootIDOf(t, host)
	lxc(t, "stop", "--force", box)
	lxc(t, "start", box)
	waitBoot(t, host, before)
	must(t, host, "admin", "sudo journalctl -b -u keelbox-boot.service --no-pager -o cat", "without a clean shutdown")
	must(t, host, "admin", `sudo grep '"kind":"power"' /var/lib/keelbox/logbook.jsonl`, `"step":"unclean-shutdown"`, "power loss, a crash or a hard reset", " between ")
	logf(t, "PASS power: a hard reset is in the logbook as an unclean shutdown")

	// The report waits until the box has been up three minutes.
	deadline := time.Now().Add(6 * time.Minute)
	for {
		out, _ := sshRun(t, host, "admin", nil, "sudo sh -c 'cat /var/lib/keelbox/notify/queue/*.json 2>/dev/null' | grep -c power-back")
		if strings.TrimSpace(out) == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no power-back event 6 minutes after the reset")
		}
		time.Sleep(10 * time.Second)
	}
	raw := must(t, host, "admin", "cat /var/lib/keelbox/status.json")
	s, err := snapshot.Parse([]byte(raw))
	if err != nil || s.Power == nil || !s.Power.Unclean || s.Sessions == nil || !s.Sessions.Tmux {
		t.Errorf("snapshot after the reset: %v %+v %+v", err, s.Power, s.Sessions)
	}
	must(t, host, "agent", "tmux has-session -t main && echo main", "main")
	if out, _ := kb(t, "status", cfg); !strings.Contains(out, "without a clean shutdown") {
		t.Errorf("status after the reset:\n%s", out)
	}
	logf(t, "PASS power: power-back queued after the settle, snapshot unclean, tmux back, status says so")
}

// sleepChecks: host.power.sleep: never masks the sleep targets and
// refuses a suspend.
func sleepChecks(t *testing.T, host string) {
	t.Helper()
	must(t, host, "admin", "for u in sleep suspend hibernate hybrid-sleep; do systemctl is-enabled $u.target; done; true", "masked\nmasked\nmasked\nmasked")
	must(t, host, "admin", "cat /etc/systemd/logind.conf.d/60-keelbox.conf", "HandleLidSwitch=ignore", "IdleAction=ignore")
	// Only once masked is certain: a suspend that went through would hang
	// the VM.
	out := must(t, host, "admin", "sudo systemctl suspend 2>&1; echo exit=$?")
	if strings.Contains(out, "exit=0") {
		t.Errorf("systemctl suspend was not refused:\n%s", out)
	}
	must(t, host, "admin", "uptime -p", "up")
	logf(t, "systemctl suspend: %s", strings.TrimSpace(out))
	logf(t, "PASS sleep never: targets masked, logind ignores the lid and idle, systemctl suspend refused")
}

// statusAllChecks: keelbox status --all over two boxes and one address that
// never answers, from a registry in a HOME of its own.
func statusAllChecks(t *testing.T, cfgs []string) {
	t.Helper()
	home := t.TempDir()
	boxes := filepath.Join(home, ".config/keelbox/boxes")
	if err := os.MkdirAll(boxes, 0o700); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cfgs {
		data, err := os.ReadFile(c)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(c), ".yaml")
		names = append(names, name)
		if err := os.WriteFile(filepath.Join(boxes, name+".yaml"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 192.0.2.0/24 is TEST-NET-1: nothing answers, so ssh times out.
	lab := fmt.Sprintf("version: 1\nbox: lab\ntarget:\n  provider: ssh\n  ssh:\n    host: 192.0.2.10\n    identity_file: %s\n    known_hosts: %s\nusers:\n  admin:\n    ssh_authorized_key_files: [%s.pub]\n",
		keyPath, filepath.Join(workDir, "known_hosts"), keyPath)
	if err := os.WriteFile(filepath.Join(boxes, "lab.yaml"), []byte(lab), 0o600); err != nil {
		t.Fatal(err)
	}
	statusAll := func(args ...string) (string, int, time.Duration) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, keelbox, append([]string{"status", "--all"}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		start := time.Now()
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("status --all: %v", err)
		}
		took := time.Since(start)
		logf(t, "keelbox status --all %s: exit %d in %s", strings.Join(args, " "), code, took.Round(100*time.Millisecond))
		fmt.Fprintf(&transcript, "%s\n", out)
		return string(out), code, took
	}
	out, code, took := statusAll()
	if code != 1 || took > 15*time.Second {
		t.Errorf("status --all: exit %d in %s, want 1 within 15 s:\n%s", code, took, out)
	}
	for _, n := range names {
		if !strings.Contains(out, "\n"+n+" ") {
			t.Errorf("status --all lacks box %s:\n%s", n, out)
		}
	}
	for _, want := range []string{"BOX", "lab", "unreachable: ssh timed out", "keelbox ssh lab", "3 boxes: 2 ok, 1 unreachable."} {
		if !strings.Contains(out, want) {
			t.Errorf("status --all lacks %q:\n%s", want, out)
		}
	}
	jout, _, _ := statusAll("--json")
	var rows []struct {
		Box, Reach, Error string
		Status            *snapshot.Status
	}
	if err := json.Unmarshal([]byte(jout), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("status --all --json: %v\n%s", err, jout)
	}
	for _, r := range rows {
		ok := r.Reach == "ok" && r.Status != nil && r.Status.Sessions != nil && r.Status.Sessions.Tmux && len(r.Status.Disk) > 0
		if r.Box == "lab" {
			ok = r.Reach == "unreachable" && r.Status == nil
		}
		if !ok {
			t.Errorf("status --all --json row %+v", r)
		}
	}
	logf(t, "PASS status --all: 2 boxes ok, the silent one unreachable, in %s", took.Round(100*time.Millisecond))
}

// TestDayTwoWatchPowerStatusAll covers the watchdog, power-loss detection,
// sleep: never and status --all on two LXD VMs (KEELBOX_E2E_TARGET=lxd).
// It is lighter than TestBox: no agent CLIs, previews or backups.
func TestDayTwoWatchPowerStatusAll(t *testing.T) {
	if os.Getenv("KEELBOX_E2E_TARGET") != "lxd" {
		t.Skip("needs KEELBOX_E2E_TARGET=lxd")
	}
	tg := &lxdTarget{}
	ensureKey(t)
	build(t)
	defer func() {
		if p := os.Getenv("KEELBOX_E2E_LOG"); p != "" {
			_ = os.WriteFile(p+".daytwo", transcript.Bytes(), 0o600)
		}
	}()
	n := time.Now().Unix() % 100000
	names := []string{fmt.Sprintf("%sa%d", VMPrefix, n), fmt.Sprintf("%sb%d", VMPrefix, n)}
	hosts := make([]string, 2)
	cfgs := make([]string, 2)
	defer func() {
		if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
			logf(t, "KEELBOX_E2E_KEEP=1: leaving %v", names)
			return
		}
		// Not lxdTarget.Destroy: it sweeps every VM with the prefix, so the
		// second call finds its VM gone.
		for _, b := range names {
			if out, err := exec.Command("lxc", "delete", "--force", b).CombinedOutput(); err != nil && !strings.Contains(string(out), "not found") {
				t.Errorf("lxc delete %s: %v\n%s", b, err, out)
			} else {
				logf(t, "deleted VM %s", b)
			}
		}
	}()
	extra := []string{"", "host:\n  power:\n    sleep: never\n"}
	for i, b := range names {
		targetYAML, _, host := tg.Create(t, b)
		hosts[i] = host
		cfg := fmt.Sprintf("version: 1\nbox: %s\n%s\nusers:\n  admin:\n    ssh_authorized_key_files: [%s.pub]\n  agent:\n    ssh_authorized_key_files: [%s.pub]\nagents: []\n%s",
			b, targetYAML, keyPath, keyPath, extra[i])
		cfgs[i] = filepath.Join(workDir, b+".yaml")
		if err := os.WriteFile(cfgs[i], []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Both first applies at once.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	outs := make([]string, 2)
	for i := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, keelbox, "apply", cfgs[i], "--ssh-user", "ubuntu")
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			outs[i], errs[i] = string(out), err
		}()
	}
	wg.Wait()
	for i := range names {
		fmt.Fprintf(&transcript, "apply %s:\n%s\n", names[i], outs[i])
		if errs[i] != nil {
			t.Fatalf("first apply of %s: %v\n%s", names[i], errs[i], outs[i])
		}
		if out, err := kb(t, "doctor", cfgs[i]); err != nil {
			t.Fatalf("doctor %s: %v\n%s", names[i], err, out)
		}
		must(t, hosts[i], "admin", "sudo systemctl start keelbox-watch.service && systemctl is-active keelbox-boot.service", "active")
	}
	logf(t, "PASS both boxes applied, doctor clean")

	statusAllChecks(t, cfgs)
	sleepChecks(t, hosts[1])
	watchdogChecks(t, hosts[0], cfgs[0])
	powerChecks(t, hosts[0], names[0], cfgs[0])
	if out, err := kb(t, "doctor", cfgs[0]); err != nil {
		t.Errorf("doctor after the reset: %v\n%s", err, out)
	}
}
