//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// keelbox up on a terminal, as someone new to it would run it: no SSH key
// yet, a server that takes only a password, the sign-ins skipped. The
// answers are typed on a pseudo-terminal when each question shows.

// expectDriver runs a command on a 120x50 pseudo-terminal (TERM
// xterm-256color, UTF-8, colour off: the questions drawn as on a laptop) and answers it:
// steps is a JSON list of [pattern, reply] taken in order, each reply typed
// once its pattern (a Python regex) has shown since the previous one;
// optional is a list of the same, answered whenever they show. It prints
// what the terminal shows (and appends it to $KB_EXPECT_LOG as it comes),
// stops a command that shows nothing new for five minutes while a step
// waits, and exits with the command's status.
const expectDriver = `import fcntl, json, os, pty, re, select, signal, struct, sys, termios, time
steps, optional = json.loads(sys.argv[1]), json.loads(sys.argv[2])
log = open(os.environ["KB_EXPECT_LOG"], "a") if os.environ.get("KB_EXPECT_LOG") else None
pid, fd = pty.fork()
if pid == 0:
    os.execvp(sys.argv[3], sys.argv[3:])
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 120, 0, 0))
seen, pos, i, done, last = "", 0, 0, set(), time.time()
while True:
    if i < len(steps) and time.time() - last > 300:
        sys.stdout.write("\nexpect: nothing new for 5 minutes at step %d: %s\n" % (i, steps[i][0]))
        os.kill(pid, signal.SIGTERM)
        last = time.time()
    r, _, _ = select.select([fd], [], [], 0.5)
    if r:
        try:
            data = os.read(fd, 4096)
        except OSError:
            break
        if not data:
            break
        last = time.time()
        seen += data.decode("utf-8", "replace")
        if log:
            log.write(data.decode("utf-8", "replace"))
            log.flush()
        sys.stdout.write(data.decode("utf-8", "replace"))
        sys.stdout.flush()
    for j, (pat, reply) in enumerate(optional):
        m = re.search(pat, seen[pos:])
        if j not in done and m:
            done.add(j)
            time.sleep(0.3)
            os.write(fd, reply.encode())
    if i < len(steps):
        m = re.search(steps[i][0], seen[pos:], re.S)
        if m:
            pos += m.end()
            time.sleep(0.3)
            os.write(fd, steps[i][1].encode())
            i += 1
_, status = os.waitpid(pid, 0)
if i < len(steps):
    sys.stdout.write("\nexpect: stopped waiting for step %d: %s\n" % (i, steps[i][0]))
sys.exit(os.waitstatus_to_exitcode(status))
`

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\r`)

// onTerminal runs keelbox in h's HOME on a pseudo-terminal.
func (h *upHome) onTerminal(t *testing.T, steps, optional [][2]string, env []string, args ...string) (string, int) {
	t.Helper()
	drv := filepath.Join(t.TempDir(), "expect.py")
	if err := os.WriteFile(drv, []byte(expectDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := json.Marshal(append([][2]string{}, steps...))
	o, _ := json.Marshal(append([][2]string{}, optional...))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", append([]string{drv, string(s), string(o), keelbox}, args...)...)
	term := []string{"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=", "LC_CTYPE=", "NO_COLOR=1", "COLORTERM=", "ACCESSIBLE=", "FORCE_HYPERLINK=0"}
	cmd.Dir, cmd.Env = workDir, append(append(append([]string{}, h.env...), term...), env...)
	if l := os.Getenv("KEELBOX_E2E_LOG"); l != "" {
		cmd.Env = append(cmd.Env, "KB_EXPECT_LOG="+l+".pty")
	}
	start := time.Now()
	raw, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("keelbox on a terminal: %v", err)
	}
	out := ansiRe.ReplaceAllString(string(raw), "")
	// Text is wrapped to the terminal: the checks read it as one line.
	out = strings.Join(strings.Fields(out), " ")
	logf(t, "keelbox %s on a terminal: exit %d in %s", strings.Join(args, " "), code, time.Since(start).Round(time.Second))
	fmt.Fprintf(&transcript, "--- keelbox %s on a terminal ---\n%s\n", strings.Join(args, " "), out)
	return out, code
}

// upNewKey: a server that takes only a password, as a home server or a
// VPS handed over with a root password is, and a laptop with no SSH key.
// keelbox up makes the key, puts it there with ssh-copy-id and the
// password, keeps the server's hostname as the box name, keeps SSH open to
// the LAN, and reaches Ready with the sign-ins skipped, saying what is left.
func upNewKey(t *testing.T) {
	h := newUpHome(t)
	box := upBoxName()
	const password = "kb-e2e-only-Pw1"
	ip := launch(t, box, "#cloud-config\nssh_pwauth: true\nchpasswd:\n  expire: false\n  users:\n    - {name: ubuntu, password: "+password+", type: text}\n")
	defer deleteVM(t, box)
	for i := 0; ; i++ { // the VM's agent starts a little after its address
		if err := exec.Command("lxc", "exec", box, "--", "cloud-init", "status", "--wait").Run(); err == nil {
			break
		} else if i == 60 {
			t.Fatalf("cloud-init on %s: %v", box, err)
		}
		time.Sleep(5 * time.Second)
	}
	fpr := strings.Fields(lxc(t, "exec", box, "--", "ssh-keygen", "-l", "-E", "sha256", "-f", "/etc/ssh/ssh_host_ed25519_key.pub"))[1]

	// ssh reads ~/.ssh from the account's passwd entry, not HOME: point it
	// at the key keelbox makes in this test's HOME, and only that key.
	bin := t.TempDir()
	for _, c := range []string{"ssh", "scp"} {
		real, err := exec.LookPath(c)
		if err != nil {
			t.Fatal(err)
		}
		w := fmt.Sprintf("#!/bin/sh\nexec %s -o IdentityFile=%s/.ssh/id_ed25519 -o IdentitiesOnly=yes \"$@\"\n", real, h.dir)
		if err := os.WriteFile(filepath.Join(bin, c), []byte(w), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "SSH_AUTH_SOCK="}

	q := regexp.QuoteMeta
	// A drawn menu ends with its key hint, a line question with its
	// "›" input line; a number then Enter picks a choice.
	const menu, line = `.*Enter choose`, `.*› `
	steps := [][2]string{
		{`Where should the box run\?` + menu, "1\r"},
		{`Server's IP address or DNS name` + line, ip + "\r"},
		{`Which account do you log in to the server as now\?` + menu, "2\r"},
		{`Host key fingerprint` + line, fpr + "\r"},
		{`Create ~/.ssh/id_ed25519 with ssh-keygen.*\[Y/n\]`, "\r"},
		{`Enter passphrase.*: `, "\n"},
		{`Enter same passphrase again: `, "\n"},
		{`How should the key get there\?` + menu, "1\r"},
		{`password: `, password + "\n"},
		{`Box name` + line, "\r"}, // Enter keeps the server's hostname
		{`Which agents\?` + menu, "4\r"},
		{`Install the GitHub CLI.*\[Y/n\]`, "\r"},
		{`Name on the agent's commits` + line, "E2E Agent\r"},
		{`Email on the agent's commits` + line, "agent@example.com\r"},
		{`How much may the agent change this box\?` + menu, "\r"},
		{`Use Tailscale to reach the box\?.*\[Y/n\]`, "\r"},
		{`Keep SSH open to your LAN as well\?.*\[Y/n\]`, "\r"},
		{`Go ahead\?.*\[Y/n\]`, "\r"},
		{`Tailscale sign-in` + menu, "2\r"},
		{`GitHub sign-in` + menu, "2\r"},
	}
	// Asked only when they apply: a port something listens on, and the
	// identity git has on the machine the test runs on (not the agent's).
	optional := [][2]string{
		{`Ports to keep open on the LAN` + line, "none\r"},
		{`as git does on this computer\?`, "n\r"},
		// Enter keeps the default, nightly restarts once the box is idle.
		{`When should the box restart for an update that needs it\?`, "\r"},
	}
	out, code := h.onTerminal(t, steps, optional, env, "up")
	if code != 0 || !isSetUp(out, box) {
		t.Fatalf("up on a terminal did not reach Ready (exit %d):\n%s", code, out)
	}
	for _, want := range []string{
		"does not take your key ~/.ssh/id_ed25519.pub for ubuntu yet",
		"✓ " + box + ", Ubuntu",
		"SSH from anywhere else closes, your LAN included",
		"SSH stays open to your LAN",
		"Left to do",
		"keelbox login " + box + " tailscale",
		"keelbox login " + box + " gh",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("up's output lacks %q", want)
		}
	}
	if strings.Contains(out, "Rename the machine") || strings.Contains(out, "exit status") || strings.Contains(out, "context canceled") {
		t.Errorf("asked to rename, or a raw error:\n%s", out)
	}
	logf(t, "PASS up on a terminal: new key put on the server with ssh-copy-id, sign-ins skipped, Ready")

	cfg, err := os.ReadFile(filepath.Join(h.dir, ".config/keelbox/boxes", box+".yaml"))
	if err != nil || !regexp.MustCompile(`(?m)^ +public_ssh: lan`).Match(cfg) || !regexp.MustCompile(`(?m)^box: "?`+q(box)).Match(cfg) {
		t.Errorf("config: %v\n%s", err, cfg)
	}
	if got := strings.TrimSpace(lxc(t, "exec", box, "--", "hostname")); got != box {
		t.Errorf("hostname changed to %s", got)
	}
	if ls, _ := h.kb(t, "ls"); !strings.Contains(ls, "ready, sign-ins left (tailscale, gh): keelbox login "+box) {
		t.Errorf("ls:\n%s", ls)
	} else {
		logf(t, "PASS ls: %s", firstLine(ls[strings.Index(ls, box):]))
	}
	if again, code := h.onTerminal(t, nil, nil, env, "up", box); code != 0 || !isSetUp(again, box) || strings.Contains(again, "Applying "+box) {
		t.Errorf("up on a ready box (exit %d):\n%s", code, again)
	}
	keys := lxc(t, "exec", box, "--", "cat", "/home/admin/.ssh/authorized_keys")
	if pub, _ := os.ReadFile(filepath.Join(h.dir, ".ssh/id_ed25519.pub")); !strings.Contains(keys, strings.Fields(string(pub))[1]) {
		t.Errorf("the new key is not the admin's:\n%s", keys)
	}
	if out, err := exec.Command("lxc", "exec", box, "--", "ls", "/home/agent/.local/bin").CombinedOutput(); err == nil && (strings.Contains(string(out), "claude") || strings.Contains(string(out), "codex")) {
		t.Errorf("agents installed although up was told none:\n%s", out)
	}
	// The identity typed at the question is the agent's, set through git.
	ident := strings.TrimSpace(lxc(t, "exec", box, "--", "sudo", "-u", "agent", "-H", "sh", "-c", "git config --global user.name; git config --global user.email; git config --global init.defaultBranch"))
	if ident != "E2E Agent\nagent@example.com\nmain" {
		t.Errorf("the agent's git identity: %q", ident)
	}
	st := lxc(t, "exec", box, "--", "passwd", "-S", "ubuntu")
	if !strings.Contains(st, " L ") {
		t.Errorf("ubuntu not locked after admin proved itself: %s", st)
	}
	logf(t, "PASS hostname kept, admin has the new key, ubuntu locked, public_ssh: lan")
}
