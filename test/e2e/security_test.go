//go:build e2e

package e2e

import (
	"strconv"
	"strings"
	"testing"
)

// securityChecks repeat known attacks as the agent, on the box, and check
// what stops each (ADR 0053 to 0057).
func securityChecks(t *testing.T, host, cfg string) {
	t.Helper()
	uid := strings.TrimSpace(must(t, host, "agent", "id -u"))

	// A tmux of the agent's first on its PATH. Root's watch run and
	// keelbox status run /usr/bin/tmux, so the shim never runs, and
	// nothing the agent prints reaches the owner's terminal as a sequence.
	must(t, host, "agent", `mkdir -p ~/.local/bin && printf '#!/bin/sh\necho ran > /tmp/kb-shim-ran\nprintf "\\033[2J\\033]0;owned\\007ALL OK\\n"\nexec /usr/bin/tmux "$@"\n' > ~/.local/bin/tmux && chmod +x ~/.local/bin/tmux && rm -f /tmp/kb-shim-ran`)
	must(t, host, "admin", "sudo systemctl start keelbox-watch.service && systemctl show -p Result keelbox-watch.service", "Result=success")
	out, err := kb(t, "status", cfg)
	if err != nil || strings.ContainsAny(out, "\x1b\x07") || strings.Contains(out, "ALL OK") {
		t.Errorf("status with the agent's tmux shim: %v\n%q", err, out)
	}
	must(t, host, "admin", "test -e /tmp/kb-shim-ran && echo RAN || echo not-run", "not-run")
	if raw := must(t, host, "admin", "cat /var/lib/keelbox/status.json"); strings.Contains(raw, `\u001b`) {
		t.Errorf("status snapshot holds an escape:\n%s", raw)
	}
	must(t, host, "agent", "rm -f ~/.local/bin/tmux /tmp/kb-shim-ran")
	logf(t, "PASS tmux shim: a tmux shim on the agent's PATH is never run by root's runs; status prints plain text")

	// An agent program root runs while an admin's sudo has a terminal
	// gets no controlling terminal. Doctor runs the agent's node through
	// its own mise shim, which the agent can replace.
	must(t, host, "agent", `s=~/.local/share/mise/shims/node; cp -P $s ~/node-shim.bak && rm -f $s && printf '#!/bin/bash\n{ tty; if ( : </dev/tty ) 2>/dev/null; then echo tty-open; else echo no-tty; fi; } > /tmp/kb-node-tty 2>&1\nexec -a node /usr/local/bin/mise "$@"\n' > $s && chmod +x $s && rm -f /tmp/kb-node-tty`)
	if out, err := sshRun(t, host, "admin", []string{"-tt"}, "sudo keelbox doctor --local /etc/keelbox/keelbox.yaml"); err != nil && !strings.Contains(out, "node") {
		t.Errorf("doctor on a terminal: %v\n%s", err, out)
	}
	if got := must(t, host, "admin", "cat /tmp/kb-node-tty"); !strings.Contains(got, "not a tty") || !strings.Contains(got, "no-tty") {
		t.Errorf("the agent's program had the admin's terminal:\n%s", got)
	}
	must(t, host, "agent", "rm -f ~/.local/share/mise/shims/node && mv ~/node-shim.bak ~/.local/share/mise/shims/node && rm -f /tmp/kb-node-tty")
	logf(t, "PASS no terminal: an agent program run by root's doctor on a terminal has no controlling terminal")

	// sshd -T refuses to run without /run/sshd, which socket activation
	// makes only at the first SSH connection after a boot. Doctor then
	// reads sshd's config in a mount namespace of its own: the checks pass
	// and the box's /run is left as it was. The directory is moved aside
	// for the one command and put back whatever happens: the command runs
	// as a unit of its own, so it finishes even if this SSH session ends,
	// and no new SSH connection is refused for longer than it takes.
	sshdOut := must(t, host, "admin", `sudo systemd-run --quiet --wait --pipe --collect /bin/sh -c 'mv /run/sshd /run/sshd.kb-e2e && trap "mv /run/sshd.kb-e2e /run/sshd" EXIT HUP INT TERM; `+
		`before=$(findmnt -n -o SOURCE,FSTYPE,OPTIONS /run); keelbox doctor --local /etc/keelbox/keelbox.yaml 2>&1; `+
		`test -e /run/sshd && echo sshd-dir-made; [ "$before" = "$(findmnt -n -o SOURCE,FSTYPE,OPTIONS /run)" ] && echo run-unchanged'`, "run-unchanged")
	if strings.Contains(sshdOut, "sshd -T failed") || strings.Contains(sshdOut, "sshd-dir-made") || !strings.Contains(sshdOut, "passwordauthentication no") || !strings.Contains(sshdOut, "ssh-agent-forwarding") {
		t.Errorf("doctor without /run/sshd:\n%s", sshdOut)
	} else {
		logf(t, "PASS doctor reads sshd's config before the first SSH connection after a boot, and leaves /run as it was")
	}
	must(t, host, "admin", "test -d /run/sshd && echo back", "back")

	// The agent's slice: tmux-resurrect's save, the agent's file,
	// leaves a child holding its output. The watch run still ends within
	// seconds, and the save ran in the agent's slice, not root's service.
	must(t, host, "agent", `s=~/.tmux/plugins/tmux-resurrect/scripts/save.sh; cp $s ~/save.sh.bak && printf '#!/bin/sh\ncat /proc/self/cgroup > /tmp/kb-save-cg\nsleep 300 &\nexit 0\n' > $s && rm -f /tmp/kb-save-cg`)
	must(t, host, "admin", `sudo python3 -c "import json;p='/var/lib/keelbox/watch/state.json';d=json.load(open(p));d.pop('tmux_saved',None);json.dump(d,open(p,'w'))"`)
	took := must(t, host, "admin", `s=$(date +%s); sudo systemctl start keelbox-watch.service; echo $(( $(date +%s) - s )); systemctl show -p Result keelbox-watch.service`, "Result=success")
	if n, _ := strconv.Atoi(strings.Fields(took)[0]); n > 30 {
		t.Errorf("the watch run took %d s with a child holding the save's output", n)
	}
	must(t, host, "admin", "cat /tmp/kb-save-cg", "/user.slice/user-"+uid+".slice/")
	must(t, host, "agent", "pkill -u agent -x sleep; mv ~/save.sh.bak ~/.tmux/plugins/tmux-resurrect/scripts/save.sh && rm -f /tmp/kb-save-cg")
	logf(t, "PASS slice: the watch run ends in %s s past a child holding its output; the agent's save ran in user-%s.slice", strings.Fields(took)[0], uid)

	// A FIFO in place of the events file is refused without being
	// opened for a blocking read.
	must(t, host, "agent", agentEventLine+"line agent-done '' >> $f; sleep 8; rm -f $f && mkfifo $f")
	took = must(t, host, "admin", `s=$(date +%s); sudo systemctl start keelbox-events.service; echo $(( $(date +%s) - s )); systemctl show -p Result keelbox-events.service; sudo journalctl -u keelbox-events.service --no-pager -n 5`, "Result=success", "not a regular file")
	if n, _ := strconv.Atoi(strings.Fields(took)[0]); n > 20 {
		t.Errorf("the events run took %d s on a FIFO", n)
	}
	must(t, host, "agent", "rm -f ~/.local/state/keelbox/events.jsonl")
	logf(t, "PASS FIFO: a FIFO for the events file is refused in %s s", strings.Fields(took)[0])

	// The agent cannot use cron, and a crontab it has is removed by
	// apply (cron runs a denied user's crontab all the same).
	must(t, host, "agent", "crontab -l 2>&1; true", "not allowed")
	// crontab refuses a denied user's crontab even from root: lift the
	// deny line for a moment, as an owner editing the file might.
	must(t, host, "admin", "sudo sed -i '/^agent$/d' /etc/cron.deny && echo '* * * * * true' | sudo crontab -u agent - && sudo crontab -l -u agent", "* * * * * true")
	if out, err := kb(t, "doctor", cfg); err == nil || !strings.Contains(out, "agent-no-cron") {
		t.Errorf("doctor with an agent crontab: %v\n%s", err, out)
	}
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Errorf("apply: %v\n%s", err, out)
	}
	must(t, host, "admin", "sudo test -e /var/spool/cron/crontabs/agent && echo present || echo gone; grep -x agent /etc/cron.deny", "gone\nagent")
	if out, err := kb(t, "doctor", cfg); err != nil {
		t.Errorf("doctor after apply: %v\n%s", err, out)
	}
	logf(t, "PASS cron: crontab refused to the agent; an agent crontab is removed by apply, doctor fails until then")
}

// squatter listens on loopback ports as the agent and appends whatever
// each connection sends to ~/squat.log, for a minute.
const squatter = `import socket, threading, time
def serve(p):
    s = socket.socket()
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", p))
    s.listen()
    while True:
        c, _ = s.accept()
        c.settimeout(3)
        try:
            d = c.recv(65536)
        except Exception:
            d = b""
        with open("/home/agent/squat.log", "ab") as f:
            f.write(b"%d:" % p + d + b"\n")
        c.close()
for p in (9099, 8090):
    threading.Thread(target=serve, args=(p,), daemon=True).start()
time.sleep(60)
`

// squatChecks: with the owner's webhook receiver and ntfy down, the
// agent listens on their loopback ports first. keelbox finds the agent's
// socket behind its connection and sends nothing: no hook path, topic or
// header reaches the agent, and the send fails with the reason.
func squatChecks(t *testing.T, host, cfg, hookPath, topic string) {
	t.Helper()
	must(t, host, "admin", "sudo systemctl stop kb-e2e-recv && sudo docker stop kb-e2e-ntfy >/dev/null && echo stopped", "stopped")
	must(t, host, "agent", "cat > ~/squat.py <<'EOF'\n"+squatter+"EOF\nrm -f ~/squat.log; setsid python3 ~/squat.py >/dev/null 2>&1 < /dev/null & sleep 2; ss -Hltn | grep -c '127.0.0.1:\\(9099\\|8090\\)'", "2")
	out, err := kb(t, "notify", "test", cfg)
	if err == nil || !strings.Contains(out, "not root's, a system account's or the admin's") {
		t.Errorf("notify test to the agent's listeners: %v\n%s", err, out)
	}
	got := must(t, host, "agent", "cat ~/squat.log 2>/dev/null; echo end")
	if !strings.Contains(got, "9099:") || strings.Contains(got, hookPath) || strings.Contains(got, topic) || strings.Contains(got, "POST") {
		t.Errorf("the agent's listener got:\n%s", got)
	}
	must(t, host, "agent", "pkill -u agent -x python3; rm -f ~/squat.py ~/squat.log; true")
	must(t, host, "admin", "sudo systemctl reset-failed kb-e2e-recv 2>/dev/null; sudo systemd-run --unit kb-e2e-recv --uid admin python3 /var/tmp/kb-e2e-recv.py && sudo docker start kb-e2e-ntfy >/dev/null && "+
		"for i in $(seq 30); do curl -fs http://127.0.0.1:8090/v1/health && break; sleep 1; done", "healthy")
	if out, err := kb(t, "notify", "test", cfg); err != nil || !strings.Contains(out, "Sent a test message on 2 channels.") {
		t.Errorf("notify test with the owner's services back: %v\n%s", err, out)
	}
	logf(t, "PASS loopback squat: loopback listeners the agent holds get a connection and no byte of the request; the owner's get it again")
}
