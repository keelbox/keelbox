//go:build e2e

package e2e

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kbIn runs the keelbox CLI with env added, stdin as its input, and
// returns its stdout and exit code; stderr goes to the transcript.
func kbIn(t *testing.T, env []string, stdin string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, keelbox, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("keelbox %s: %v", strings.Join(args, " "), err)
	}
	logf(t, "keelbox %s: exit %d; stderr: %s", strings.Join(args, " "), code, strings.TrimSpace(errb.String()))
	return string(out), code
}

// TestLaptopCommands: keelbox ssh BOX -- CMD and keelbox skills over real
// SSH: the command's own exit code and words, input passed through, 255
// when ssh does not log in, and a skill copied as the agent without the
// files that may hold a secret.
func TestLaptopCommands(t *testing.T) {
	ensureKey(t)
	build(t)
	cfg, host := minimalBox(t, "", "")

	if out, code := kbIn(t, nil, "", "ssh", cfg, "--", "sh", "-c", "echo on-box; exit 7"); code != 7 || out != "on-box\n" {
		t.Fatalf("exit code: %d %q", code, out)
	}
	if out, code := kbIn(t, nil, "", "ssh", cfg, "--", "printf", "%s|", "a b", "c'd", "$HOME"); code != 0 || out != "a b|c'd|$HOME|" {
		t.Fatalf("words: %d %q", code, out)
	}
	if out, code := kbIn(t, nil, "from the laptop\n", "ssh", cfg, "--", "cat"); code != 0 || out != "from the laptop\n" {
		t.Fatalf("stdin: %d %q", code, out)
	}
	if out, code := kbIn(t, nil, "", "ssh", cfg, "--", "id", "-un"); code != 0 || out != "agent\n" {
		t.Fatalf("as the agent: %d %q", code, out)
	}
	// --admin gives root, so without a terminal it refuses before connecting
	// and nothing runs on the box.
	if out, code := kbIn(t, nil, "", "ssh", cfg, "--admin", "--", "sh", "-c", "echo ran-as-admin"); code != 2 || strings.Contains(out, "ran-as-admin") {
		t.Fatalf("--admin without a terminal: %d %q, want exit 2 and nothing run", code, out)
	}
	logf(t, "PASS ssh --: exit code, words, stdin, agent, and --admin refused without a terminal")

	// A port nothing listens on: ssh does not log in, and keelbox says so.
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(workDir, "unreachable.yaml")
	if err := os.WriteFile(bad, []byte(strings.Replace(string(data), "  ssh:\n", "  ssh:\n    port: 2\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := kbIn(t, nil, "", "ssh", bad, "--", "true"); code != 255 {
		t.Fatalf("no login: exit %d, want 255", code)
	}
	logf(t, "PASS ssh --: 255 when ssh does not log in")

	home := t.TempDir()
	skill := filepath.Join(home, ".claude/skills/demo")
	for rel, body := range map[string]string{"SKILL.md": "---\nname: demo\ndescription: A demo.\n---\nBody.\n", "scripts/run.sh": "#!/bin/sh\necho ran\n", ".env": "SECRET=1\n"} {
		p := filepath.Join(skill, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"HOME=" + home}
	if out, code := kbIn(t, env, "", "skills", cfg, "--skill", "demo"); code != 0 || !strings.Contains(out, "copied demo") {
		t.Fatalf("skills: %d %s", code, out)
	}
	if out, code := kbIn(t, nil, "", "ssh", cfg, "--", "sh", "-c", "stat -c %U .claude/skills/demo/SKILL.md; .claude/skills/demo/scripts/run.sh; test ! -e .claude/skills/demo/.env && echo no-env"); code != 0 || out != "agent\nran\nno-env\n" {
		t.Fatalf("on the box: %d %q", code, out)
	}
	if out, _ := kbIn(t, env, "", "skills", cfg, "--skill", "demo"); !strings.Contains(out, "demo is already on") {
		t.Fatalf("second copy: %s", out)
	}
	logf(t, "PASS skills: copied as the agent, script runs, .env left out, second run sees it")

	// keelbox reboot: asks without a terminal, then the box comes back
	// with the agent's sessions, and the logbook says why.
	probe, _ := sshRun(t, host, "admin", nil, "for s in $(loginctl list-sessions --no-legend | awk '{print $1}'); do loginctl show-session $s -p Name -p Class -p Type -p State -p IdleHint | tr '\\n' ' '; echo; done")
	logf(t, "logind sessions during an SSH command:\n%s", probe)
	must(t, host, "agent", "tmux new-session -d -s before-reboot -c /tmp && echo made", "made")
	// A stand-in for Claude Code in the session: it notes its arguments and
	// writes Claude Code's own note on its process, as claude does.
	const fakeID = "11111111-2222-3333-4444-555555555555"
	must(t, host, "agent", `mkdir -p ~/.local/bin && cat > ~/.local/bin/claude <<'FAKE'
#!/bin/bash
echo "$*" >> ~/claude-args
mkdir -p ~/.claude/sessions
printf '{"pid":%d,"sessionId":"`+fakeID+`","status":"idle"}' $$ > ~/.claude/sessions/$$.json
exec -a claude sleep 86400
FAKE
chmod +x ~/.local/bin/claude && tmux send-keys -t before-reboot '~/.local/bin/claude --model opus' Enter && sleep 2 && cat ~/claude-args`, "--model opus")
	if out, code := kbIn(t, nil, "", "reboot", cfg); code != 2 {
		t.Fatalf("reboot without a terminal or --yes: exit %d %q", code, out)
	}
	before := bootIDOf(t, host)
	out, code := kbIn(t, nil, "", "reboot", cfg, "--yes")
	if code != 0 || !strings.Contains(out, "reboots in 5 seconds") || !strings.Contains(out, "is back after") {
		t.Fatalf("reboot: exit %d\n%s", code, out)
	}
	if id := bootIDOf(t, host); id == "" || id == before {
		t.Fatalf("boot id %q, was %q", id, before)
	}
	// The session comes back, and the watch run types the command that
	// opens the conversation into its pane.
	deadline := time.Now().Add(5 * time.Minute)
	for {
		got, _ := sshRun(t, host, "agent", nil, "tmux has-session -t before-reboot 2>/dev/null && echo back; cat ~/claude-args")
		if strings.Contains(got, "back") && strings.Contains(got, "--resume "+fakeID) {
			break
		}
		if time.Now().After(deadline) {
			d, _ := sshRun(t, host, "admin", nil, "sudo -u agent tmux ls; sudo -u agent tmux capture-pane -p -t before-reboot; sudo cat /home/agent/claude-args /var/lib/keelbox/watch/resume.json; "+
				"sudo ls -l /home/agent/.tmux/resurrect/; sudo journalctl -b --no-pager -u keelbox-watch.service -n 20; sudo journalctl -b --no-pager _UID=1001 -n 40")
			t.Fatalf("the session before-reboot, or its conversation, did not come back after keelbox reboot:\n%s", d)
		}
		time.Sleep(5 * time.Second)
	}
	must(t, host, "admin", "sudo test -e /var/lib/keelbox/watch/resume.json || echo gone", "gone")
	must(t, host, "admin", `sudo grep '"step":"reboot"' /var/lib/keelbox/logbook.jsonl`, "rebooted: asked with keelbox reboot")
	logf(t, "PASS reboot: --yes needed without a terminal; back with a new boot, the session restored with its conversation, in the logbook")
}
