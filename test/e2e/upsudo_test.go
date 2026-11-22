//go:build e2e

package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upPasswordSudo: a home server whose owner's sudo asks for a password,
// the most common kind. keelbox up runs on a terminal; sudo on the server
// asks for the password over ssh -t, once for the read-only look and once
// for the first apply, and keelbox never handles it: it is in no output,
// no file and no process list, and after Ready the owner's sudo still asks
// for it. Then a passwordless bootstrap sudoers file: doctor fails on it,
// and an apply removes it and says so.
func upPasswordSudo(t *testing.T) {
	h := newUpHome(t)
	box := upBoxName()
	pub := ensureKey(t)
	const password = "kb-e2e-sudo-Pw7"
	ip := launch(t, box, "#cloud-config\nusers:\n  - {name: sam, groups: [sudo], shell: /bin/bash, lock_passwd: false, ssh_authorized_keys: ['"+pub+"']}\n"+
		"chpasswd:\n  expire: false\n  users:\n    - {name: sam, password: "+password+", type: text}\n")
	defer deleteVM(t, box)
	for i := 0; ; i++ {
		// Exit 2 is "done, with recoverable errors": the accounts are made.
		err := exec.Command("lxc", "exec", box, "--", "cloud-init", "status", "--wait").Run()
		var ee *exec.ExitError
		if err == nil || errors.As(err, &ee) && ee.ExitCode() == 2 {
			break
		} else if i == 60 {
			t.Fatalf("cloud-init on %s: %v", box, err)
		}
		time.Sleep(5 * time.Second)
	}
	if out, err := sshRun(t, ip, "sam", nil, "sudo -n true 2>&1 && echo nopasswd"); err == nil || strings.Contains(out, "nopasswd") {
		t.Fatalf("sam's sudo does not ask for a password: %v %s", err, out)
	}
	fpr := strings.Fields(lxc(t, "exec", box, "--", "ssh-keygen", "-l", "-E", "sha256", "-f", "/etc/ssh/ssh_host_ed25519_key.pub"))[1]
	since := strings.TrimSpace(lxc(t, "exec", box, "--", "date", "+%s"))

	// sudo's prompt: "[sudo] password for sam: " (sudo) or
	// "[sudo: authenticate] Password: " (sudo-rs, Ubuntu 25.10 and later).
	const sudoPrompt = `\[sudo[^\]]*\] [Pp]assword[^:]*: `
	steps := [][2]string{
		{`sudo on .* asks for sam's password.*` + sudoPrompt, password + "\n"},
		{`Go ahead\?.*\[Y/n\]`, "\r"},
		{sudoPrompt, password + "\n"},
	}
	out, code := h.onTerminal(t, steps, nil, nil, "up", box, "--provider", "ssh", "--host", ip, "--ssh-user", "sam", "--host-key", fpr,
		"--ssh-key", keyPath, "--agents", "none", "--gh=false", "--tailscale=false", "--git-name", "kb-e2e", "--git-email", "kb-e2e@example.com", "--agent-permissions", "auto", "--auto-reboot", "nightly")
	if code != 0 || !isSetUp(out, box) {
		t.Fatalf("up with password sudo did not reach Ready (exit %d):\n%s", code, out)
	}
	for _, want := range []string{"sudo with sam's password", "keelbox never sees or keeps it", "logs in with your key and uses sudo"} {
		if !strings.Contains(out, want) {
			t.Errorf("up's output lacks %q", want)
		}
	}
	for _, bad := range []string{password, "NOPASSWD", "90-keelbox-bootstrap", "logs in over SSH with a password"} {
		if strings.Contains(out, bad) {
			t.Errorf("up's output has %q:\n%s", bad, out)
		}
	}
	logf(t, "PASS up on a terminal with password sudo: sudo asked on the box twice, Ready")

	// The password is nowhere on the box: not in a file keelbox wrote, the
	// journal since up started, or a process's arguments. The pattern is
	// read from a file, so the search's own arguments do not hold it.
	pat := filepath.Join(t.TempDir(), "pattern")
	if err := os.WriteFile(pat, []byte(password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lxc(t, "file", "push", pat, box+"/root/kb-pattern", "--mode", "0600")
	got := lxc(t, "exec", box, "--", "sh", "-c", "grep -rlFf /root/kb-pattern /var/lib/keelbox /etc/keelbox /run/keelbox /home/sam /home/admin /tmp 2>/dev/null; "+
		"journalctl --since @"+since+" --no-pager | grep -cFf /root/kb-pattern; cat /proc/[0-9]*/cmdline 2>/dev/null | tr '\\0' ' ' | grep -cFf /root/kb-pattern; true")
	if strings.TrimSpace(got) != "0\n0" {
		t.Errorf("the sudo password is on the box:\n%s", got)
	} else {
		logf(t, "PASS the sudo password is in no keelbox file, the journal or a command line")
	}
	// sam's sudo still asks for the password: nothing passwordless left.
	if got, err := sshRun(t, ip, "sam", nil, "sudo -n true 2>&1 && echo nopasswd; ls /etc/sudoers.d"); strings.Contains(got, "nopasswd") || strings.Contains(got, "bootstrap") {
		t.Errorf("sam has passwordless sudo after setup: %v\n%s", err, got)
	} else {
		logf(t, "PASS sam's sudo still asks for a password; /etc/sudoers.d: %s", strings.Join(strings.Fields(got[strings.Index(got, "\n")+1:]), " "))
	}
	if plan, code := h.kb(t, "plan", box); code != 0 || !strings.Contains(plan, "No changes.") {
		t.Errorf("plan after up (exit %d):\n%s", code, plan)
	}

	// A passwordless bootstrap sudoers file for sam.
	sshRun(t, ip, "admin", nil, "echo 'sam ALL=(ALL) NOPASSWD:ALL' | sudo tee /etc/sudoers.d/90-keelbox-bootstrap >/dev/null && sudo chmod 440 /etc/sudoers.d/90-keelbox-bootstrap")
	if doc, code := h.kb(t, "doctor", box); code == 0 || !strings.Contains(doc, "FAIL  bootstrap-sudo") || !strings.Contains(doc, "gives sam sudo without a password") {
		t.Errorf("doctor with a bootstrap sudoers file (exit %d):\n%s", code, doc)
	} else {
		logf(t, "PASS doctor fails on /etc/sudoers.d/90-keelbox-bootstrap")
	}
	if ap, code := h.kb(t, "apply", box); code != 0 || !strings.Contains(ap, "Removed /etc/sudoers.d/90-keelbox-bootstrap, which gives sam sudo without a password; sudo asks for the password again.") {
		t.Errorf("apply did not remove the bootstrap file and say so (exit %d):\n%s", code, ap)
	}
	if got, _ := sshRun(t, ip, "sam", nil, "sudo -n true 2>&1 && echo nopasswd; test -e /etc/sudoers.d/90-keelbox-bootstrap && echo still-there; true"); strings.Contains(got, "nopasswd") || strings.Contains(got, "still-there") {
		t.Errorf("bootstrap file not removed:\n%s", got)
	} else if doc, _ := h.kb(t, "doctor", box); !strings.Contains(doc, "ok    bootstrap-sudo") {
		t.Errorf("doctor after the apply:\n%s", doc)
	} else {
		logf(t, "PASS apply removed the bootstrap file and said so; doctor ok again")
	}
}
