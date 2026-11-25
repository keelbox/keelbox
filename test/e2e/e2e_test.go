//go:build e2e

// Package e2e provisions a real, fresh Ubuntu machine with keelbox and checks
// that it works as documented. It is excluded from `go test ./...`; run it with
//
//	KEELBOX_E2E_TARGET=hetzner go test -tags e2e -v -timeout 90m ./test/e2e/
//	KEELBOX_E2E_TARGET=lxd     go test -tags e2e -v -timeout 90m ./test/e2e/
//	KEELBOX_E2E_TARGET=aws     go test -tags e2e -v -timeout 90m ./test/e2e/
//	KEELBOX_E2E_TARGET=digitalocean go test -tags e2e -v -timeout 90m ./test/e2e/
//	KEELBOX_E2E_TARGET=gcp     go test -tags e2e -v -timeout 90m ./test/e2e/
//
// aws: needs the AWS CLI on PATH, signed in (KEELBOX_E2E_AWS_PROFILE names a
// profile, KEELBOX_E2E_AWS_REGION a region, default eu-central-1). Creates
// at most one Lightsail instance per run, tagged keelbox-e2e=true, and
// always deletes it with its static IP.
// digitalocean: needs doctl on PATH, signed in (KEELBOX_E2E_DO_CONTEXT names
// a doctl context, KEELBOX_E2E_DO_REGION a region, default fra1). Creates at
// most one Droplet per run, tagged keelbox-e2e, and always deletes it with
// its firewall and tags.
// gcp: needs gcloud on PATH, signed in (KEELBOX_E2E_GCP_PROJECT names a
// project, default gcloud's own; KEELBOX_E2E_GCP_ZONE a zone, default
// europe-west3-a). Creates at most one VM per run, labelled
// keelbox-e2e=true, and always deletes it with its firewall rules and its
// reserved address.
// hetzner: needs ~/.config/keelbox/hetzner.token (mode 600). Creates at most
// one server per run, labelled keelbox-e2e=true, and always deletes it.
// lxd (primary): needs a working `lxc`; launches `lxc launch ubuntu:26.04
// kb-e2e-N --vm -c limits.cpu=2 -c limits.memory=2GiB` with cloud-init
// installing the e2e SSH key for the image's default user, then deletes it
// and sweeps any other kb-e2e-* VM, so runs that share an LXD host need
// their own KEELBOX_E2E_VM_PREFIX (scripts/e2e.sh gives each test one with
// KEELBOX_E2E_JOBS). At most two exist at once per prefix. Never changes
// LXD server config, pools, networks or profiles.
//
// Common knobs: KEELBOX_E2E_IMAGE (default 26.04, falls back to 24.04 on
// Hetzner), KEELBOX_E2E_KEEP=1 (do not delete, for debugging; the sweep
// still runs on the next Hetzner run), KEELBOX_E2E_UPGRADE_FROM=<commit>
// (lxd: apply first with keelbox built from that commit, then upgrade),
// KEELBOX_E2E_VM_PREFIX (lxd VM name prefix, default kb-e2e-).
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// target is a machine the harness can create and delete.
type target interface {
	// Create starts a fresh machine (or, for hetzner, prepares to) and
	// returns its target: block, the bootstrap SSH user and the host.
	Create(t *testing.T, box string) (targetYAML, bootstrapUser, host string)
	// CreateApplies reports whether `keelbox create` runs the
	// first apply (true for hetzner).
	CreateApplies() bool
	Destroy(t *testing.T, box string)
	// Spend reports the estimated cost so far, or "" if free.
	Spend() string
}

var (
	home       = mustHome()
	keyPath    = filepath.Join(home, ".config/keelbox/e2e_ed25519")
	keelbox    string
	workDir    string
	transcript bytes.Buffer
)

func mustHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return h
}

func logf(t *testing.T, format string, args ...any) {
	t.Helper()
	line := fmt.Sprintf(format, args...)
	t.Log(line)
	fmt.Fprintf(&transcript, "%s %s\n", time.Now().UTC().Format("15:04:05"), line)
}

func ensureKey(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(keyPath); err != nil {
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "keelbox-e2e", "-f", keyPath).CombinedOutput()
		if err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(pub))
}

func build(t *testing.T) {
	t.Helper()
	workDir = t.TempDir()
	keelbox = filepath.Join(workDir, "keelbox")
	cmd := exec.Command("go", "build", "-o", keelbox, "../../cmd/keelbox")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
}

// kb runs the keelbox CLI and returns combined output.
func kb(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, keelbox, args...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	logf(t, "keelbox %s: %v in %s", strings.Join(args, " "), errString(err), time.Since(start).Round(time.Second))
	fmt.Fprintf(&transcript, "%s\n", out)
	return string(out), err
}

func errString(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// sshRun runs a command on the box directly, as user.
func sshRun(t *testing.T, host, user string, extra []string, remote string) (string, error) {
	t.Helper()
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10", "-o", "LogLevel=ERROR", "-i", keyPath, "-o", "IdentitiesOnly=yes"}
	args = append(args, extra...)
	args = append(args, user+"@"+host, "--", remote)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	return string(out), err
}

func writeConfig(t *testing.T, box, targetYAML string) string {
	t.Helper()
	cfg := fmt.Sprintf(`version: 1
box: %s
%s
users:
  admin:
    ssh_authorized_key_files: [%s.pub]
  agent:
    ssh_authorized_key_files: [%s.pub]
agents: [claude-code, codex]
`, box, targetYAML, keyPath, keyPath)
	cfg += featureConfig(t, box)
	p := filepath.Join(workDir, box+".yaml")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func pickTarget(t *testing.T) target {
	switch os.Getenv("KEELBOX_E2E_TARGET") {
	case "hetzner":
		return newHetzner(t)
	case "aws":
		return newAWS(t)
	case "digitalocean":
		return newDigitalOcean(t)
	case "gcp":
		return newGCP(t)
	case "lxd":
		return &lxdTarget{}
	case "":
		t.Skip("set KEELBOX_E2E_TARGET=hetzner, aws, digitalocean, gcp or lxd")
	}
	t.Fatalf("unknown KEELBOX_E2E_TARGET %q", os.Getenv("KEELBOX_E2E_TARGET"))
	return nil
}

func TestBox(t *testing.T) {
	tg := pickTarget(t)
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
		if s := tg.Spend(); s != "" {
			logf(t, "estimated spend: %s", s)
		}
	}()
	cfg := writeConfig(t, box, targetYAML)

	// 1. Fresh machine: apply succeeds.
	if tg.CreateApplies() {
		out, err := kb(t, "create", cfg)
		if err != nil {
			t.Fatalf("create: %v\n%s", err, out)
		}
		host = hostFromCreate(t, out, host)
	} else if from := os.Getenv("KEELBOX_E2E_UPGRADE_FROM"); from != "" {
		upgradeFrom(t, from, cfg, bootstrap, host, box)
	} else {
		out, err := kb(t, "apply", cfg, "--ssh-user", bootstrap)
		if err != nil {
			t.Fatalf("first apply: %v\n%s", err, out)
		}
		if !strings.Contains(out, "keelbox login") {
			t.Errorf("first apply does not point at keelbox login:\n%s", out)
		}
	}
	logf(t, "PASS fresh apply")

	// 2. Second apply changes nothing.
	out, err := kb(t, "apply", cfg)
	if err != nil || !strings.Contains(out, "No changes.") {
		t.Fatalf("second apply not a no-op: %v\n%s", err, out)
	}
	logf(t, "PASS second apply: no changes")

	// 3. Doctor passes, with one sign-in section that names the laptop's
	// config, not the box's staged copy.
	if out, err := kb(t, "doctor", cfg); err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	} else if strings.Count(out, "\nsign-in\n") != 1 || strings.Contains(out, "/run/keelbox") {
		t.Errorf("doctor's sign-in section:\n%s", out)
	}
	logf(t, "PASS doctor")

	// The session the first apply started, before anything else attaches.
	// A box set up by an older keelbox first has no mark to follow.
	if os.Getenv("KEELBOX_E2E_UPGRADE_FROM") == "" {
		firstAttachChecks(t, host, box, cfg)
	}
	dayTwoChecks(t, host, box, cfg)
	watchdogChecks(t, host, cfg)
	securityChecks(t, host, cfg)
	checks(t, host, "")
	isolationChecks(t, host, bootstrap)
	symlinkChecks(t, host, cfg)
	permissionsChecks(t, host, cfg)
	manualPermissionsChecks(t, host, box, cfg)
	sessionChecks(t, host, box, cfg)
	remoteControlChecks(t, host, cfg)
	previewChecks(t, host, box, cfg)
	backupChecks(t, host, cfg)
	upgradeChecks(t, host, cfg)
	if _, ok := tg.(*lxdTarget); ok {
		allowChecks(t, host, cfg) // this machine is on the LXD bridge's private network
	}

	// 4. Reboot, then doctor and sessions again. The reboot is systemd's,
	// not keelbox's: the conversation in tmux opens again all the same.
	resumed := conversationBeforeReboot(t, host)
	reboot(t, host)
	resumed()
	if out, err := kb(t, "doctor", cfg); err != nil {
		t.Fatalf("doctor after reboot: %v\n%s", err, out)
	}
	logf(t, "PASS doctor after reboot")
	must(t, host, "admin", "sudo journalctl -b -u keelbox-boot.service --no-pager -o cat", "the last boot ended with a clean shutdown")
	logf(t, "PASS the reboot was recorded as a clean shutdown")
	checks(t, host, "after reboot ")
	welcomeAfterRebootChecks(t, host)
	isolationChecks(t, host, bootstrap)
	if out, err := kb(t, "status", cfg); err != nil {
		t.Errorf("status: %v\n%s", err, out)
	}

	// 5. Last, because everything above reaches the box at its public
	// address: public SSH closes only after proof over a (stand-in) tailnet.
	if _, ok := tg.(*lxdTarget); ok {
		publicSSHChecks(t, host, box, cfg)
	}
}

func checks(t *testing.T, host, when string) {
	t.Helper()
	expect := func(name, user, remote string, want ...string) {
		t.Helper()
		out, err := sshRun(t, host, user, nil, remote)
		if err != nil {
			t.Errorf("%s%s: %v\n%s", when, name, err, out)
			return
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s%s: output lacks %q:\n%s", when, name, w, out)
				return
			}
		}
		logf(t, "PASS %s%s: %s", when, name, firstLine(out))
	}
	expect("docker hello-world as agent", "agent", "docker run --rm hello-world", "Hello from Docker!")
	// Non-interactive SSH: proves the PATH block works without a login shell.
	expect("agent toolchain", "agent", "node --version && pnpm --version && command -v claude codex && claude --version && codex --version", "/home/agent/.local/bin/claude", "/home/agent/.local/bin/codex")
	expect("firewall active", "admin", "sudo ufw status verbose", "Status: active", "deny (incoming)")
	expect("tmux config present", "agent", "test -s ~/.config/tmux/keelbox.conf && grep -c keelbox ~/.tmux.conf", "1")
	expect("tmux server running", "agent", "tmux has-session -t main && echo up", "up")
	expect("agent has no sudo", "agent", "sudo -n true 2>&1 || echo no-sudo", "no-sudo")

	out, err := sshRun(t, host, "admin", []string{"-o", "PreferredAuthentications=password,keyboard-interactive", "-o", "PubkeyAuthentication=no", "-o", "NumberOfPasswordPrompts=0"}, "true")
	if err == nil || !strings.Contains(out, "Permission denied (publickey)") {
		t.Errorf("%spassword login not refused: %v\n%s", when, err, out)
	} else {
		logf(t, "PASS %sSSH password login refused: %s", when, strings.TrimSpace(out))
	}
	out, err = sshRun(t, host, "root", nil, "true")
	if err == nil {
		t.Errorf("%sroot login allowed", when)
	} else {
		logf(t, "PASS %sroot login refused: %s", when, strings.TrimSpace(out))
	}
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// reboot restarts the box and waits until it is back with a new boot id.
// The reboot is detached so the SSH session returns at once instead of
// hanging on a connection the box drops.
func reboot(t *testing.T, host string) {
	t.Helper()
	bootID := func() string {
		out, err := sshRun(t, host, "admin", []string{"-o", "ConnectTimeout=5"}, "cat /proc/sys/kernel/random/boot_id")
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	before := bootID()
	if before == "" {
		t.Fatal("cannot read boot id before reboot")
	}
	if out, err := sshRun(t, host, "admin", nil, "sudo -n systemd-run --on-active=2 --timer-property=AccuracySec=100ms systemctl reboot"); err != nil {
		t.Fatalf("schedule reboot: %v\n%s", err, out)
	}
	start := time.Now()
	for time.Since(start) < 6*time.Minute {
		time.Sleep(5 * time.Second)
		if id := bootID(); id != "" && id != before {
			_, _ = sshRun(t, host, "admin", nil, "systemctl is-system-running --wait >/dev/null 2>&1; true")
			logf(t, "rebooted (boot id changed) and back in %s", time.Since(start).Round(time.Second))
			return
		}
	}
	t.Fatal("box did not come back after reboot")
}

// conversationBeforeReboot starts a stand-in for Claude Code in a tmux
// session, and returns a check that, after a reboot keelbox did not start,
// the watch run typed the command that opens its conversation (ADR 0096).
// The real claude is installed, so an alias in the agent's ~/.bashrc points
// the typed command at the stand-in; the check takes it out again.
func conversationBeforeReboot(t *testing.T, host string) func() {
	t.Helper()
	const fakeID = "66666666-7777-8888-9999-000000000000"
	must(t, host, "agent", `mkdir -p ~/kb-e2e-fake && cat > ~/kb-e2e-fake/claude <<'FAKE'
#!/bin/bash
echo "$*" >> ~/kb-e2e-claude-args
mkdir -p ~/.claude/sessions
printf '{"pid":%d,"sessionId":"`+fakeID+`","status":"idle"}' $$ > ~/.claude/sessions/$$.json
exec -a claude sleep 86400
FAKE
chmod +x ~/kb-e2e-fake/claude && echo 'alias claude=~/kb-e2e-fake/claude # kb-e2e' >> ~/.bashrc && `+
		`tmux new-session -d -s kb-resume -c /tmp && sleep 1 && tmux send-keys -t kb-resume 'claude --model opus' Enter && sleep 2 && cat ~/kb-e2e-claude-args`, "--model opus")
	// A second window whose program outlives SIGTERM by a few seconds, as
	// docker compose or a database in the foreground does. It keeps the
	// tmux server up while the user manager stops, so the tmux unit's own
	// save at its stop would run moments after keelbox's, and overwrite
	// and delete it within the same second unless it skips.
	must(t, host, "agent", `tmux new-window -d -t kb-resume -n kb-linger -c /tmp && tmux send-keys -t kb-resume:kb-linger "trap 'sleep 3; exit' TERM HUP; while :; do sleep 1; done" Enter && tmux list-windows -t kb-resume -F '#W'`, "kb-linger")
	// At shutdown keelbox-conversations.service notes the pane before the
	// agent's user manager stops, and tmux-resurrect saves the sessions.
	return func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Minute)
		for {
			got, _ := sshRun(t, host, "agent", nil, "tmux has-session -t kb-resume 2>/dev/null && echo back; tmux list-windows -t kb-resume -F '#W' 2>/dev/null; cat ~/kb-e2e-claude-args")
			if strings.Contains(got, "back") && strings.Contains(got, "kb-linger") && strings.Contains(got, "--resume "+fakeID) {
				break
			}
			if time.Now().After(deadline) {
				d, _ := sshRun(t, host, "admin", nil, "sudo -u agent tmux ls; ls -l /home/agent/.local/share/tmux/resurrect/ | tail -4; sudo -u agent tmux capture-pane -p -t kb-resume; sudo cat /home/agent/kb-e2e-claude-args /var/lib/keelbox/watch/resume.json; "+
					"sudo journalctl -b -1 --no-pager -u keelbox-conversations.service; sudo journalctl -b --no-pager -u keelbox-watch.service -n 20")
				t.Fatalf("the conversation in session kb-resume did not open again after systemctl reboot:\n%s", d)
			}
			time.Sleep(5 * time.Second)
		}
		must(t, host, "admin", "sudo journalctl -b -1 --no-pager -o cat -u keelbox-conversations.service", "1 conversation(s) noted", "tmux sessions saved")
		must(t, host, "agent", "tmux kill-session -t kb-resume; sed -i '/# kb-e2e$/d' ~/.bashrc; rm -rf ~/kb-e2e-fake ~/kb-e2e-claude-args; echo cleaned", "cleaned")
		logf(t, "PASS a reboot keelbox did not start: the conversation in tmux opened again, with a window whose program outlived SIGTERM")
	}
}
