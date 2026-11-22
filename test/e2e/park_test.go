//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// parker is a target on whose boxes keelbox can resize, park and resume
// (docs/adr/0071-resize-park-resume.md). A cloud target gets
// TestResizeParkResume by implementing it.
type parker interface {
	target
	// SizeKey is the config key Create wrote the size under, as the line
	// starts in the file ("server_type:"), and Size its value.
	SizeKey() string
	Size() string
	// BiggerSize is the next size up the box can change to and back from:
	// more vCPUs or more memory.
	BiggerSize(t *testing.T) string
	// Holds counts what the cloud holds for the box, by keelbox's labels:
	// servers, parked snapshots and firewalls.
	Holds(t *testing.T, box string) (servers, snapshots, firewalls int)
}

// oneWay is a target whose cloud cannot change a server back to a smaller
// size: the box stays at the bigger one for the rest of the test.
type oneWay interface {
	CannotShrink() bool
}

// addressHolder is a target that can make it likely that a parked box
// resumes at another address than the one it had.
type addressHolder interface {
	HoldAddress(t *testing.T, box, addr string) (release func())
}

// TestResizeParkResume takes one box through keelbox resize (up, then back
// down), park, resume, a second park and destroy, and checks at each step
// what the cloud holds and that the box is the same box: its data, its
// SSH host key, and everything the feature and isolation checks cover.
func TestResizeParkResume(t *testing.T) {
	name := os.Getenv("KEELBOX_E2E_TARGET")
	tg := pickTarget(t)
	pk, ok := tg.(parker)
	if !ok {
		t.Skipf("target %s is not a cloud target, which keelbox resize, park and resume need", name)
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
	destroyed := false
	defer func() {
		if destroyed {
			return
		}
		if os.Getenv("KEELBOX_E2E_KEEP") == "1" {
			logf(t, "KEELBOX_E2E_KEEP=1: leaving %s", box)
			return
		}
		tg.Destroy(t, box)
	}()
	cfg := writeConfig(t, box, targetYAML)
	holds := func(when string, servers, snapshots, firewalls int) {
		t.Helper()
		s, p, f := pk.Holds(t, box)
		if s != servers || p != snapshots || f != firewalls {
			t.Fatalf("%s: the cloud holds %d server(s), %d parked snapshot(s), %d firewall(s) for %s; want %d, %d, %d", when, s, p, f, box, servers, snapshots, firewalls)
		}
		logf(t, "PASS %s: the cloud holds %d server(s), %d parked snapshot(s), %d firewall(s) for %s", when, s, p, f, box)
	}
	timed := func(what string, args ...string) string {
		t.Helper()
		start := time.Now()
		out, err := kb(t, args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", what, err, out)
		}
		logf(t, "TIMING %s: %s", what, time.Since(start).Round(time.Second))
		return out
	}
	doctor := func(when string) {
		t.Helper()
		if out, err := kb(t, "doctor", cfg); err != nil {
			t.Fatalf("doctor %s: %v\n%s", when, err, out)
		}
		logf(t, "PASS doctor %s", when)
	}

	out, err := kb(t, "create", cfg)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	host = hostFromCreate(t, out, host)
	holds("after create", 1, 0, 1)

	// What must still be there at the end: a file of the agent's, data in
	// a Docker volume of the agent's, a container that restarts by itself.
	marker := "kb-park-" + randomHex(t, 8)
	must(t, host, "agent", "echo "+marker+" > ~/park-marker && mkdir -p ~/projects/park && echo "+marker+" > ~/projects/park/marker")
	must(t, host, "agent", "docker volume create kbpark >/dev/null && docker run -d --name kbpark --restart unless-stopped -v kbpark:/data busybox sh -c 'echo "+marker+" > /data/marker; exec sleep infinity' >/dev/null && sleep 2 && docker exec kbpark cat /data/marker", marker)
	kept := func(when, host string) {
		t.Helper()
		must(t, host, "agent", "cat ~/park-marker ~/projects/park/marker", marker+"\n"+marker)
		must(t, host, "agent", "docker run --rm -v kbpark:/data busybox cat /data/marker", marker)
		// The container comes back by its restart policy, shortly after
		// the agent's Docker does.
		var state string
		for i := 0; i < 30; i++ {
			state, _ = sshRun(t, host, "agent", nil, "docker inspect -f '{{.State.Running}}' kbpark")
			if strings.TrimSpace(state) == "true" {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if strings.TrimSpace(state) != "true" {
			t.Errorf("%s: the agent's container did not come back: %s", when, state)
		}
		logf(t, "PASS %s: the agent's files, its Docker volume and its container are there", when)
	}
	fpr := hostKey(t, host)
	logf(t, "host key before: %s", fpr)
	shape := func(host string) string {
		return strings.TrimSpace(must(t, host, "admin", "echo $(nproc) vCPU, $(awk '/MemTotal/ {print int($2/1024/1024+0.5)}' /proc/meminfo) GB"))
	}
	small := shape(host)

	// Resize up, then back down.
	big := pk.BiggerSize(t)
	out = timed("resize up", "resize", cfg, "--size", big, "--yes")
	if !strings.Contains(out, "is "+big+" now") {
		t.Errorf("resize up does not say the new size:\n%s", out)
	}
	if got := shape(host); got == small {
		t.Fatalf("after resize to %s the box is still %s", big, got)
	} else {
		logf(t, "PASS resize up: %s (%s) -> %s (%s)", pk.Size(), small, big, got)
	}
	if data, _ := os.ReadFile(cfg); !strings.Contains(string(data), pk.SizeKey()+" "+big) {
		t.Errorf("resize did not write %s %s to the config:\n%s", pk.SizeKey(), big, data)
	}
	doctor("after resize up")
	kept("after resize up", host)

	if ow, ok := tg.(oneWay); ok && ow.CannotShrink() {
		// The smaller size is refused before anything changes.
		out, err := kb(t, "resize", cfg, "--size", pk.Size(), "--yes")
		if exit(err) != 2 || !strings.Contains(out, "is not a size") {
			t.Fatalf("resize back to %s on a cloud that cannot shrink: %v\n%s", pk.Size(), err, out)
		}
		small = shape(host)
		logf(t, "PASS resize down to %s is refused; the box stays %s (%s)", pk.Size(), big, small)
	} else {
		out = timed("resize down", "resize", cfg, "--size", pk.Size(), "--yes")
		if got := shape(host); got != small {
			t.Fatalf("after resize back to %s the box is %s, not %s\n%s", pk.Size(), got, small, out)
		}
		logf(t, "PASS resize down: back to %s (%s)", pk.Size(), small)
		if data, _ := os.ReadFile(cfg); !strings.Contains(string(data), pk.SizeKey()+" "+pk.Size()) {
			t.Errorf("resize did not write %s %s to the config:\n%s", pk.SizeKey(), pk.Size(), data)
		}
		doctor("after resize down")
	}
	if got := hostKey(t, host); got != fpr {
		t.Errorf("host key changed over the resizes: %s, was %s", got, fpr)
	}
	// Without a size and without a terminal resize changes nothing.
	if out, err := kb(t, "resize", cfg); exit(err) != 2 || !strings.Contains(out, "--size") {
		t.Errorf("resize without --size and without a terminal: %v\n%s", err, out)
	}

	// Park.
	before := logins(t, host)
	if out, err := kb(t, "park", cfg); exit(err) != 2 || !strings.Contains(out, "--yes") {
		t.Errorf("park without --yes and without a terminal: %v\n%s", err, out)
	}
	holds("after a park that was not confirmed", 1, 0, 1)
	out = timed("park", "park", cfg, "--yes")
	if !strings.Contains(out, "is parked") || !strings.Contains(out, "keelbox resume") {
		t.Errorf("park does not say how to resume:\n%s", out)
	}
	holds("after park", 0, 1, 1)
	for _, cmd := range []string{"status", "apply", "doctor", "sessions", "create"} {
		out, err := kb(t, cmd, cfg)
		if err == nil || !strings.Contains(out, "has been parked since") || !strings.Contains(out, "keelbox resume "+cfg) {
			t.Errorf("%s on a parked box: %v\n%s", cmd, err, out)
		}
	}
	logf(t, "PASS status, apply, doctor, sessions and create on a parked box stop with the resume line")
	if out, err := kb(t, "park", cfg, "--yes"); err != nil || !strings.Contains(out, "already parked") {
		t.Errorf("park on a parked box: %v\n%s", err, out)
	}
	holds("after commands on the parked box", 0, 1, 1)

	// Resume, at another address where the target can arrange it: the host
	// keys keelbox pinned for the box must follow it there.
	release := func() {}
	if ah, ok := tg.(addressHolder); ok {
		release = ah.HoldAddress(t, box, host)
	}
	was := host
	out = timed("resume", "resume", cfg)
	release()
	host = hostFromCreate(t, out, "")
	holds("after resume", 1, 0, 1)
	doctor("after resume")
	if host == was {
		logf(t, "the box resumed at the address it had, %s: the move of the pinned host keys was not exercised", host)
	} else {
		// doctor above connected with strict host key checking, so the
		// keys are pinned under the new address; the old one is forgotten.
		if out, err := exec.Command("ssh-keygen", "-F", was, "-f", filepath.Join(home, ".config/keelbox/known_hosts")).Output(); err == nil && len(out) > 0 {
			t.Errorf("the address the box gave up, %s, is still pinned:\n%s", was, out)
		}
		if !strings.Contains(out, "(it was "+was+")") {
			t.Errorf("resume does not say the address changed from %s:\n%s", was, out)
		}
		logf(t, "PASS the box resumed at a new address, %s (it was %s), and its pinned host keys moved with it", host, was)
	}
	if got := hostKey(t, host); got != fpr {
		t.Errorf("host key after resume: %s, was %s", got, fpr)
	} else {
		logf(t, "PASS the SSH host key is unchanged after resume: %s", got)
	}
	kept("after resume", host)
	if after := logins(t, host); after != before {
		t.Errorf("the accounts, sudo rules or authorised keys differ after resume.\nbefore park:\n%s\nafter resume:\n%s", before, after)
	} else {
		logf(t, "PASS the accounts, their password states, the sudo rules and the authorised keys are the same after resume")
	}
	if got := shape(host); got != small {
		t.Errorf("after resume the box is %s, was %s", got, small)
	}
	if out, err := kb(t, "apply", cfg); err != nil || !strings.Contains(out, "No changes.") {
		t.Errorf("apply after resume is not a no-op: %v\n%s", err, out)
	}
	checks(t, host, "after resume ")
	isolationChecks(t, host, bootstrap)
	if out, err := kb(t, "resume", cfg); err != nil || !strings.Contains(out, "is not parked") {
		t.Errorf("resume on a running box: %v\n%s", err, out)
	}

	// Destroy a parked box: the snapshot and the firewall go.
	timed("second park", "park", cfg, "--yes")
	holds("after the second park", 0, 1, 1)
	if out, err := kb(t, "destroy", cfg, "--yes"); err == nil || !strings.Contains(out, "--discard-work") {
		t.Errorf("destroy of a parked box without --discard-work and without a terminal: %v\n%s", err, out)
	}
	holds("after a destroy that was refused", 0, 1, 1)
	destroyed = true
	tg.Destroy(t, box)
	holds("after destroy", 0, 0, 0)
	if s := tg.Spend(); s != "" {
		logf(t, "estimated spend: %s", s)
	}
}

// logins is what decides who can log in to the box and who can become
// root, read on the box as the admin: every account with its uid, shell
// and password state, the members of the groups that give privilege, the
// sudo rules and each account's authorised SSH keys (as hashes).
func logins(t *testing.T, host string) string {
	t.Helper()
	out, err := sshRun(t, host, "admin", nil, `sudo sh -c '
getent passwd | cut -d: -f1,3,7 | sort
passwd -S -a | cut -d" " -f1,2 | sort
getent group sudo adm lxd docker root
for f in /etc/sudoers /etc/sudoers.d/*; do [ -f "$f" ] && sha256sum "$f"; done
for f in /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys /etc/ssh/authorized_keys/*; do [ -f "$f" ] && echo "$f $(sort "$f" | sha256sum)"; done
true'`)
	if err != nil {
		t.Fatalf("reading the logins on %s: %v\n%s", host, err, out)
	}
	return out
}

func exit(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

var fingerprintRe = regexp.MustCompile(`SHA256:[A-Za-z0-9+/]+`)

// hostKey is the fingerprint of the ed25519 host key the server presents,
// read from this machine and not through keelbox.
func hostKey(t *testing.T, host string) string {
	t.Helper()
	var last string
	for i := 0; i < 10; i++ {
		scan, err := exec.Command("ssh-keyscan", "-T", "10", "-t", "ed25519", host).Output()
		if err == nil && len(scan) > 0 {
			cmd := exec.Command("ssh-keygen", "-lf", "-")
			cmd.Stdin = strings.NewReader(string(scan))
			out, _ := cmd.Output()
			if f := fingerprintRe.FindString(string(out)); f != "" {
				return f
			}
			last = string(out)
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("no ed25519 host key from %s: %s", host, last)
	return ""
}
