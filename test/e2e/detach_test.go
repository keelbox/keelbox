//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// An apply a laptop starts runs on the box as a transient unit and the
// laptop only follows it (ADR 0063). On LXD, with keelbox in its own HOME
// and no terminal:
//
//   - laptop-killed: keelbox up is killed mid-apply; the box finishes the
//     apply, and keelbox up reports it once and carries on to Ready (sign-in
//     skipped). Then a second apply while one runs follows it instead of
//     starting another; an apply left running that ends unwatched sends one
//     apply-finished to a webhook on the box's loopback and is reported once;
//     and a hard reset mid-apply is reported as interrupted by the follower,
//     status and doctor, and the next apply carries on.
//
//   - connection-dropped: SSH to the box is dropped for 100 seconds mid-apply;
//     the follower says it lost the connection, reconnects, and keelbox up
//     reaches Ready.
//
//     KEELBOX_E2E_TARGET=lxd go test -tags e2e -run TestDetach -v ./test/e2e/
//
// VMs are named with KEELBOX_E2E_DETACH_PREFIX (default kb-detach-) and
// deleted, with a sweep of any other VM with the prefix.
var detachPrefix = envOr("KEELBOX_E2E_DETACH_PREFIX", "kb-detach-")

func TestDetach(t *testing.T) {
	if os.Getenv("KEELBOX_E2E_TARGET") != "lxd" {
		t.Skip("set KEELBOX_E2E_TARGET=lxd")
	}
	ensureKey(t)
	build(t)
	defer sweepPrefix(t, detachPrefix)
	t.Run("laptop-killed", detachLaptopKilled)
	t.Run("connection-dropped", detachConnectionDropped)
	if p := os.Getenv("KEELBOX_E2E_LOG"); p != "" {
		_ = os.WriteFile(p, transcript.Bytes(), 0o600)
	}
}

func sweepPrefix(t *testing.T, prefix string) {
	if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
		return
	}
	out, err := exec.Command("lxc", "list", "--format", "csv", "-c", "n").Output()
	if err != nil {
		t.Errorf("lxc list: %v", err)
		return
	}
	for _, n := range strings.Fields(string(out)) {
		if strings.HasPrefix(n, prefix) {
			logf(t, "sweep: deleting VM %s", n)
			_ = exec.Command("lxc", "delete", "--force", n).Run()
		}
	}
}

// detachVM launches a VM with the e2e key on ubuntu and returns its
// address and host key fingerprint.
func detachVM(t *testing.T, box string) (ip, fpr string) {
	t.Helper()
	ip = launch(t, box, "#cloud-config\nssh_authorized_keys:\n  - "+ensureKey(t)+"\n")
	for i := 0; i < 60; i++ {
		if _, err := sshRun(t, ip, "ubuntu", nil, "cloud-init status --wait >/dev/null 2>&1; true"); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	fpr = strings.Fields(lxc(t, "exec", box, "--", "ssh-keygen", "-l", "-E", "sha256", "-f", "/etc/ssh/ssh_host_ed25519_key.pub"))[1]
	return ip, fpr
}

// bg is keelbox running in the background, as a laptop's would.
type bg struct {
	cmd  *exec.Cmd
	out  *syncBuf
	done chan int
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitOut waits until the background keelbox has printed want.
func (b *bg) waitOut(t *testing.T, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !strings.Contains(b.out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("keelbox did not print %q in %s:\n%s", want, d, b.out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// holdRun starts a watcher on the box that stops (SIGSTOP) the first run
// newer than before as soon as its log has grep, so a test acts while the
// run is surely in the middle. wait returns the run's id.
func holdRun(t *testing.T, box, before, grep string) (wait func() string) {
	t.Helper()
	cmd := exec.Command("lxc", "exec", box, "--", "timeout", "600", "sh", "-c", `
while :; do
  id=$(cat /var/lib/keelbox/runs/current 2>/dev/null)
  if [ -n "$id" ] && [ "$id" != "$2" ] && grep -qF "$1" /var/lib/keelbox/runs/$id.jsonl 2>/dev/null &&
     systemctl kill --signal=SIGSTOP keelbox-apply-$id.service 2>/dev/null; then
    echo "$id"; exit 0
  fi
  sleep 0.05
done`, "sh", grep, before)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() string {
		t.Helper()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("hold a run at %q: %v", grep, err)
		}
		id := strings.TrimSpace(out.String())
		logf(t, "run %s held at %s", id, grep)
		return id
	}
}

// signalRun sends sig to every process of the run's unit; SIGSTOP holds
// the run still, so that a test can act while it is surely running.
func signalRun(t *testing.T, box, id, sig string) {
	t.Helper()
	lxc(t, "exec", box, "--", "systemctl", "kill", "--signal="+sig, "keelbox-apply-"+id+".service")
}

func (h *upHome) start(t *testing.T, args ...string) *bg {
	t.Helper()
	cmd := exec.Command(keelbox, args...)
	cmd.Dir, cmd.Env = workDir, h.env
	b := &bg{cmd: cmd, out: &syncBuf{}, done: make(chan int, 1)}
	cmd.Stdout, cmd.Stderr = b.out, b.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	logf(t, "keelbox %s: started in the background", strings.Join(args, " "))
	go func() {
		err := cmd.Wait()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		b.done <- code
	}()
	return b
}

// wait waits for the background keelbox to exit and returns its output and
// exit code.
func (b *bg) wait(t *testing.T, d time.Duration) (string, int) {
	t.Helper()
	select {
	case code := <-b.done:
		out := b.out.String()
		fmt.Fprintf(&transcript, "%s\n", out)
		return out, code
	case <-time.After(d):
		_ = b.cmd.Process.Kill()
		t.Fatalf("keelbox did not exit in %s:\n%s", d, b.out.String())
	}
	return "", 0
}

// kill kills the background keelbox as a laptop dying would: SIGKILL, no
// chance to say anything.
func (b *bg) kill(t *testing.T) string {
	t.Helper()
	_ = b.cmd.Process.Kill()
	<-b.done
	out := b.out.String()
	fmt.Fprintf(&transcript, "%s\n(killed)\n", out)
	return out
}

var runIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// boxRun reads the box's newest run id and whether its log has a line
// matching grep (a fixed string), through lxc, which works whatever SSH does.
func boxRun(t *testing.T, box, grep string) (id string, found bool) {
	t.Helper()
	out, err := exec.Command("lxc", "exec", box, "--", "sh", "-c",
		`id=$(cat /var/lib/keelbox/runs/current 2>/dev/null) || exit 0; echo "$id"; grep -qF "$1" /var/lib/keelbox/runs/$id.jsonl && echo found; true`, "sh", grep).CombinedOutput()
	if err != nil {
		return "", false
	}
	f := strings.Fields(string(out))
	if len(f) == 0 || !runIDRe.MatchString(f[0]) {
		return "", false
	}
	return f[0], len(f) > 1 && f[1] == "found"
}

// waitRun waits until the box's newest run is not before (a new run, when
// before is set) and its log has grep. On a timeout it says how far it got:
// no run started, or the run and its last records, with what the keelbox
// in the background printed (up, when set).
func waitRun(t *testing.T, box, before, grep string, d time.Duration, up ...*bg) string {
	t.Helper()
	deadline := time.Now().Add(d)
	last := ""
	for time.Now().Before(deadline) {
		id, ok := boxRun(t, box, grep)
		if id != "" && id != before {
			if ok {
				return id
			}
			last = id
		}
		time.Sleep(500 * time.Millisecond)
	}
	var said string
	for _, b := range up {
		said = "\nkeelbox printed:\n" + b.out.String()
	}
	if last == "" {
		t.Fatalf("no run started on %s in %s%s", box, d, said)
	}
	tail, _ := exec.Command("lxc", "exec", box, "--", "sh", "-c",
		`tail -n 5 /var/lib/keelbox/runs/$1.jsonl; systemctl status --no-pager -n 20 keelbox-apply-$1.service`, "sh", last).CombinedOutput()
	t.Fatalf("run %s on %s had no %q in %s; it ended with:\n%s%s", last, box, grep, d, tail, said)
	return ""
}

// waitRunEnd waits until the run's unit is gone and returns its last record.
func waitRunEnd(t *testing.T, box, id string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("lxc", "exec", box, "--", "systemctl", "is-active", "keelbox-apply-"+id+".service").CombinedOutput()
		if st := strings.TrimSpace(string(out)); st != "active" && st != "activating" && st != "deactivating" {
			return strings.TrimSpace(lxc(t, "exec", box, "--", "tail", "-n", "1", "/var/lib/keelbox/runs/"+id+".jsonl"))
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("run %s still active after %s", id, d)
	return ""
}

func runLogs(t *testing.T, box string) int {
	t.Helper()
	return strings.Count(lxc(t, "exec", box, "--", "sh", "-c", "ls /var/lib/keelbox/runs/*.jsonl"), ".jsonl")
}

// waitRunNote waits until keelbox has noted that it follows run id, as it
// does before it shows anything of the run.
func (h *upHome) waitRunNote(t *testing.T, box, id string) {
	t.Helper()
	p := filepath.Join(h.dir, ".config/keelbox/boxes", box+".run.json")
	for i := 0; i < 600; i++ {
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), id) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("keelbox did not note run %s in %s", id, p)
}

// setConfig rewrites lines of the box's registry config.
func (h *upHome) setConfig(t *testing.T, box string, edit func(string) string) {
	t.Helper()
	p := filepath.Join(h.dir, ".config/keelbox/boxes", box+".yaml")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(edit(string(data))), 0o644); err != nil {
		t.Fatal(err)
	}
}

var ghLine = regexp.MustCompile(`(?m)^(\s+gh:) (true|false)`)

func setGH(on bool) func(string) string {
	return func(s string) string { return ghLine.ReplaceAllString(s, fmt.Sprintf("$1 %v", on)) }
}

func detachLaptopKilled(t *testing.T) {
	h := newUpHome(t)
	box := fmt.Sprintf("%s%d", detachPrefix, time.Now().Unix()%1000000)
	ip, fpr := detachVM(t, box)
	defer deleteVM(t, box)

	// 1. keelbox up, killed while the box applies the runtime module.
	up := h.start(t, "up", box, "--provider", "ssh", "--host", ip, "--ssh-user", "ubuntu", "--host-key", fpr, "--ssh-key", keyPath,
		"--agents", "none", "--gh=false", "--yes", "--skip-sign-in")
	id := waitRun(t, box, "", `"module":"runtime"`, 10*time.Minute, up)
	partial := up.kill(t)
	if !regexp.MustCompile(`(?m)^  ok +\S`).MatchString(partial) {
		t.Errorf("the laptop showed no progress before it was killed:\n%s", partial)
	}
	logf(t, "laptop killed during run %s", id)
	end := waitRunEnd(t, box, id, 15*time.Minute)
	if !strings.Contains(end, `"kind":"end"`) || !strings.Contains(end, `"result":"ok"`) {
		t.Fatalf("the box did not finish the apply without the laptop: %s", end)
	}
	logf(t, "PASS the box finished the apply without the laptop: %s", end)

	// 2. keelbox up again: reports the run once and carries on to Ready.
	out, code := h.kb(t, "up", box, "--skip-sign-in")
	if code != 0 || !regexp.MustCompile(`setup on `+box+` finished .*: \d+ steps, \d+ changed, ok`).MatchString(out) || !isSetUp(out, box) {
		t.Fatalf("up did not report the run and reach Ready (exit %d):\n%s", code, out)
	}
	logf(t, "PASS up reported the finished run once and reached Ready")

	// 3. A second apply while one runs follows it.
	h.setConfig(t, box, setGH(true))
	logs := runLogs(t, box)
	before, _ := boxRun(t, box, "")
	held := holdRun(t, box, before, `"kind":"start"`)
	first := h.start(t, "apply", box)
	id = held()
	second := h.start(t, "apply", box)
	second.waitOut(t, "following it", 3*time.Minute)
	signalRun(t, box, id, "SIGCONT")
	firstOut, code1 := first.wait(t, 15*time.Minute)
	secondOut, code2 := second.wait(t, 5*time.Minute)
	switch {
	case code1 != 0 || code2 != 0:
		t.Errorf("concurrent applies exit %d and %d:\n%s\n---\n%s", code1, code2, firstOut, secondOut)
	case runLogs(t, box) != logs+1:
		t.Errorf("two applies started %d runs", runLogs(t, box)-logs)
	default:
		logf(t, "PASS a second apply follows the running one; one run")
	}

	// 4. A run left running that ends unwatched notifies once, and the next
	// apply reports it once instead of applying.
	hookURL := filepath.Join(workDir, box+".hook")
	if err := os.WriteFile(hookURL, []byte("http://127.0.0.1:9099/hook/"+randomHex(t, 8)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	must(t, ip, "admin", "cat > /var/tmp/kb-e2e-recv.py <<'EOF'\n"+receiver+"EOF\nsudo systemd-run --unit kb-e2e-recv --uid admin python3 /var/tmp/kb-e2e-recv.py && sleep 1 && systemctl is-active kb-e2e-recv", "active")
	h.setConfig(t, box, func(s string) string {
		return setGH(false)(s) + fmt.Sprintf("\nnotify:\n  channels:\n    - {name: hook, type: webhook, format: json, url_file: %s}\n", hookURL)
	})
	before, _ = boxRun(t, box, "")
	held = holdRun(t, box, before, `"kind":"start"`)
	left := h.start(t, "apply", box)
	id = held()
	h.waitRunNote(t, box, id)
	left.kill(t)
	signalRun(t, box, id, "SIGCONT")
	if end := waitRunEnd(t, box, id, 10*time.Minute); !strings.Contains(end, `"result":"ok"`) {
		t.Fatalf("the run left running did not finish: %s", end)
	}
	waitFor(t, ip, 1, 30*time.Second, "apply-finished", func(d delivery) bool { return d.Kind == "apply-finished" })
	time.Sleep(3 * time.Second)
	if n := count(deliveries(t, ip), func(d delivery) bool { return strings.HasPrefix(d.Kind, "apply-") }); n != 1 {
		t.Errorf("%d apply notifications, want 1: %+v", n, deliveries(t, ip))
	} else {
		logf(t, "PASS one apply-finished for the run nobody watched")
	}
	out, code = h.kb(t, "apply", box)
	if after, _ := boxRun(t, box, ""); code != 0 || after != id || !regexp.MustCompile(`setup on `+box+` finished .*, ok`).MatchString(out) {
		t.Errorf("apply did not report the run left once (exit %d, run %s):\n%s", code, after, out)
	} else {
		logf(t, "PASS apply reported the run it left, once, and applied nothing")
	}
	if out, code = h.kb(t, "apply", box); code != 0 || !strings.Contains(out, "No changes.") {
		t.Errorf("apply after the report (exit %d):\n%s", code, out)
	}
	if n := count(deliveries(t, ip), func(d delivery) bool { return strings.HasPrefix(d.Kind, "apply-") }); n != 1 {
		t.Errorf("a followed apply notified: %d apply notifications", n)
	}

	// 5. A hard reset mid-apply: reported as interrupted, then carried on.
	// gh on in the config and off the box, so this apply has a package to
	// install in runtime.
	h.setConfig(t, box, setGH(true))
	lxc(t, "exec", box, "--", "apt-get", "remove", "-y", "-qq", "gh")
	before, _ = boxRun(t, box, "")
	held = holdRun(t, box, before, `"module":"runtime"`)
	reset := h.start(t, "apply", box)
	id = held()
	lxc(t, "restart", "--force", box)
	logf(t, "hard reset %s during run %s", box, id)
	out, code = reset.wait(t, 10*time.Minute)
	if code != 1 || !strings.Contains(out, "stopped without finishing") || !strings.Contains(out, "keelbox apply "+box) {
		t.Errorf("the follower did not report the reset run (exit %d):\n%s", code, out)
	} else {
		logf(t, "PASS the follower reported the run the reset stopped")
	}
	for i := 0; i < 60; i++ {
		if _, err := sshRun(t, ip, "admin", []string{"-o", "ConnectTimeout=5"}, "systemctl is-system-running --wait >/dev/null 2>&1; true"); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if out, _ := h.kb(t, "status", box); !regexp.MustCompile(`setup\s+interrupted: stopped without finishing`).MatchString(out) || !strings.Contains(out, "keelbox apply "+box) {
		t.Errorf("status does not show the interrupted run:\n%s", out)
	}
	if out, _ := h.kb(t, "doctor", box); !regexp.MustCompile(`FAIL\s+setup-run`).MatchString(out) || !strings.Contains(out, "stopped without finishing") {
		t.Errorf("doctor does not fail the interrupted run:\n%s", out)
	} else {
		logf(t, "PASS status and doctor report the interrupted run")
	}
	if out, code = h.kb(t, "apply", box); code != 0 {
		t.Errorf("apply after the reset (exit %d):\n%s", code, out)
	}
	if out, _ := h.kb(t, "doctor", box); strings.Contains(out, "setup-run") {
		t.Errorf("doctor still reports the interrupted run after an apply:\n%s", out)
	} else {
		logf(t, "PASS the next apply carried on; doctor clear")
	}
}

func detachConnectionDropped(t *testing.T) {
	h := newUpHome(t)
	box := fmt.Sprintf("%s%d", detachPrefix, time.Now().Unix()%1000000)
	ip, fpr := detachVM(t, box)
	defer deleteVM(t, box)

	up := h.start(t, "up", box, "--provider", "ssh", "--host", ip, "--ssh-user", "ubuntu", "--host-key", fpr, "--ssh-key", keyPath,
		"--agents", "none", "--gh=false", "--yes", "--skip-sign-in")
	id := waitRun(t, box, "", `"module":"runtime"`, 10*time.Minute, up)
	// Drop every SSH packet, both ways, for 100 seconds: longer than ssh's
	// keepalive gives a connection (3 x 15 s after the last data), so the
	// follower's stream ends, and shorter than it keeps trying (3 minutes).
	drop := exec.Command("lxc", "exec", box, "--", "sh", "-c", `nft add table inet kbe2edrop &&
nft add chain inet kbe2edrop in '{ type filter hook input priority -300 ; }' &&
nft add chain inet kbe2edrop out '{ type filter hook output priority -300 ; }' &&
nft add rule inet kbe2edrop in tcp dport 22 drop && nft add rule inet kbe2edrop out tcp sport 22 drop &&
sleep 100; nft delete table inet kbe2edrop`)
	if out, err := drop.CombinedOutput(); err != nil {
		t.Fatalf("drop SSH: %v %s", err, out)
	}
	logf(t, "SSH to %s dropped for 100 s during run %s", box, id)
	out, code := up.wait(t, 20*time.Minute)
	if code != 0 || !strings.Contains(out, "lost the connection to "+box) || !strings.Contains(out, "reconnected to "+box) || !isSetUp(out, box) {
		t.Fatalf("up did not ride out the dropped connection to Ready (exit %d):\n%s", code, out)
	}
	if end := waitRunEnd(t, box, id, time.Minute); !strings.Contains(end, `"result":"ok"`) {
		t.Errorf("the run: %s", end)
	}
	logf(t, "PASS up lost the connection, reconnected and reached Ready; the run finished ok")
}
