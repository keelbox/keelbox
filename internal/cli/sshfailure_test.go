package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// What a command says when ssh itself fails on a box that is set up: the
// box is off, its host key cannot be fetched, the account named with
// --ssh-user no longer logs in, the key is refused. Each ends with what to
// run, and a check that ssh could not run is never read as its answer.

const labKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"

// labBox is a box reached over SSH at host, with keys for both accounts.
func labBox(t *testing.T, host string, port int) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(fmt.Sprintf(`version: 1
box: lab
target:
  provider: ssh
  ssh:
    host: %s
    port: %d
users:
  admin:
    ssh_authorized_keys: ["%s"]
  agent:
    ssh_authorized_keys: ["%s"]
`, host, port, labKey, labKey)), "lab.yaml")
	if err != nil || cfg.Target.Provider != config.ProviderSSH {
		t.Fatalf("not a box reached over SSH: %v", err) // a local box would run sudo here
	}
	return cfg
}

// setUpBox is a box at 192.0.2.10 whose host key this machine has pinned,
// and whose SSH port answers unless a test says otherwise.
func setUpBox(t *testing.T) *config.Config {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	isolate(t)
	cfg := labBox(t, "192.0.2.10", 22)
	if err := sshTarget(cfg, addr{"192.0.2.10", 22}, options{}, io.Discard, io.Discard).Pin(provider.ParseHostKeys(boxHostKey)); err != nil {
		t.Fatal(err)
	}
	portAnswers(t, nil)
	return cfg
}

// portAnswers sets what a look at the box's SSH port finds.
func portAnswers(t *testing.T, err error) {
	t.Helper()
	old := sshAnswers
	sshAnswers = func(context.Context, addr) error { return err }
	t.Cleanup(func() { sshAnswers = old })
}

// sshStub stands in for ssh, and for the box behind it. Per account it
// fails as ssh itself does, or runs the kinds of command these tests let
// through: a check whose output is discarded and a read of a systemd unit's
// state, against the stand-ins in bin (the vendor CLIs, and systemctl,
// pkill and python3, so that this machine's own are never run). A session
// on the terminal is the test's own script, if it wrote one. It never runs
// anything else on this machine.
type sshStub struct {
	t   *testing.T
	dir string
}

const sshStubScript = `#!/bin/sh
d="$KB_SSH_STUB"
dest=""; remote=""
while [ $# -gt 0 ]; do
  case "$1" in
    --) shift; remote="$*"; break ;;
    -O) exit 0 ;;
    -o|-p|-i) shift ;;
    -*) ;;
    *) [ -n "$dest" ] || dest="$1" ;;
  esac
  shift
done
user="${dest%%@*}"
echo "$user" >> "$d/log"
n=$(grep -c "^$user\$" "$d/log")
after=0
[ -f "$d/$user.after" ] && after=$(cat "$d/$user.after")
if [ -f "$d/$user.code" ] && [ "$n" -gt "$after" ]; then
  [ -f "$d/$user.out" ] && cat "$d/$user.out"
  [ -f "$d/$user.err" ] && cat "$d/$user.err" >&2
  exit "$(cat "$d/$user.code")"
fi
case "$remote" in
  *" >/dev/null 2>&1 || exit 1"|*" >/dev/null 2>&1"|*"systemctl --user show -p "*) PATH="$d/bin:$PATH" exec sh -c "$remote" ;;
esac
[ -x "$d/session" ] && exec "$d/session" "$remote"
[ -f "$d/sessions-end" ] && exit "$(cat "$d/sessions-end")"
echo "the ssh stand-in runs status checks only: $remote" >&2
exit 97
`

// stubSSH puts the stand-in on PATH. Every account logs in, and every
// vendor CLI says "not signed in", until a test says otherwise.
func stubSSH(t *testing.T) *sshStub {
	t.Helper()
	s := &sshStub{t: t, dir: t.TempDir()}
	s.write("ssh", sshStubScript, 0o755)
	if err := os.Mkdir(filepath.Join(s.dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cli := range []string{"tailscale", "claude", "codex", "gh"} {
		s.vendor(cli, 1)
	}
	for _, tool := range []string{"sudo", "systemctl", "pkill", "python3"} {
		s.write("bin/"+tool, "#!/bin/sh\nexit 1\n", 0o755)
	}
	t.Setenv("KB_SSH_STUB", s.dir)
	t.Setenv("PATH", s.dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return s
}

func (s *sshStub) write(name, body string, mode os.FileMode) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(body), mode); err != nil {
		s.t.Fatal(err)
	}
}

// fails makes ssh as user exit with code after printing msg on stderr, as
// ssh does for its own errors (255) or a forced command does (142).
func (s *sshStub) fails(user string, code int, msg string) {
	s.write(user+".code", strconv.Itoa(code), 0o644)
	s.write(user+".err", msg+"\n", 0o644)
	_ = os.Remove(filepath.Join(s.dir, user+".after"))
}

// vendor sets the exit code of a vendor CLI's status command on the box.
func (s *sshStub) vendor(cli string, code int) {
	body := fmt.Sprintf("#!/bin/sh\nexit %d\n", code)
	if cli == "tailscale" && code == 0 {
		body = "#!/bin/sh\necho '{\"BackendState\": \"Running\"}'\n"
	}
	s.write("bin/"+cli, body, 0o755)
}

// noHostKey makes ssh-keyscan fail, as for a host that does not answer.
func (s *sshStub) noHostKey() { s.write("ssh-keyscan", "#!/bin/sh\nexit 1\n", 0o755) }

// calls are the accounts ssh was run as, in order.
func (s *sshStub) calls() string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "log"))
	return strings.Join(strings.Fields(string(b)), " ")
}

// exitStatus is the error of a command that exited with code.
func exitStatus(code int) error { return exec.Command("sh", "-c", fmt.Sprint("exit ", code)).Run() }

const (
	timedOut = "ssh: connect to host 192.0.2.10 port 22: Connection timed out"
	offLine  = "192.0.2.10 does not answer on port 22; check it is on and reachable from here: ssh admin@192.0.2.10"
)

var (
	errNoRoute = errors.New("dial tcp 192.0.2.10:22: connect: no route to host")
	userFlag   = map[string]bool{"ssh-user": true}
)

func TestSSHFailureEndsWithTheFix(t *testing.T) {
	cfg := setUpBox(t)
	const leaveOut = "If lab is already set up, that is expected: root and the image's account no longer log in there, and keelbox connects as admin. Leave out --ssh-user: "
	keyscan := fmt.Errorf("ssh-keyscan 192.0.2.10: %w", exitStatus(1)) // as TrustHost wraps it
	for name, c := range map[string]struct {
		err   error
		user  string
		o     options
		cmd   string
		port  error // what a look at the port finds
		want  string
		keeps bool // the error is returned as it is
	}{
		"the box is off, ssh said so": {
			err: errors.New("ssh admin@192.0.2.10: exit status 255: " + timedOut), user: "admin", cmd: "status", want: offLine,
		},
		"the box is off, ssh gave no reason": {
			err: exitStatus(255), user: "admin", cmd: "apply", port: errNoRoute, want: offLine,
		},
		"the image's account after setup": {
			err: errors.New("ssh ubuntu@192.0.2.10: exit status 255"), user: "ubuntu", o: options{sshUser: "ubuntu", set: userFlag}, cmd: "plan",
			want: "ubuntu could not log in to 192.0.2.10. " + leaveOut + "keelbox plan lab. On a machine that is not set up yet, check your key logs in: ssh ubuntu@192.0.2.10 true",
		},
		"the image's account after setup, with what ssh said": {
			err: errors.New("ssh ubuntu@192.0.2.10: exit status 255: Connection closed by 192.0.2.10 port 22"), user: "ubuntu", o: options{sshUser: "ubuntu", set: userFlag}, cmd: "status",
			want: "ubuntu could not log in to 192.0.2.10 (ssh ubuntu@192.0.2.10: exit status 255: Connection closed by 192.0.2.10 port 22). " + leaveOut +
				"keelbox status lab. On a machine that is not set up yet, check your key logs in: ssh ubuntu@192.0.2.10 true",
		},
		"root after setup": {
			err: errors.New("ssh root@192.0.2.10: exit status 255: root@192.0.2.10: Permission denied (publickey)."), user: "root", o: options{sshUser: "root", set: userFlag}, cmd: "notify",
			want: "root could not log in to 192.0.2.10. " + leaveOut + "and run it again. On a machine that is not set up yet, check your key logs in: ssh root@192.0.2.10 true",
		},
		// An account keelbox chose itself (a new cloud server's, the one
		// keelbox up looks at a machine as) is not the user's flag.
		"an account keelbox chose": {
			err: errors.New("ssh ubuntu@192.0.2.10: exit status 255"), user: "ubuntu", o: options{sshUser: "ubuntu"}, cmd: "apply",
			want: "ssh ubuntu@192.0.2.10 ended with exit status 255. Check it logs in: ssh ubuntu@192.0.2.10 true",
		},
		"the user's flag, inside keelbox up": {
			err: errors.New("ssh ubuntu@192.0.2.10: exit status 255"), user: "ubuntu", o: options{sshUser: "ubuntu", set: userFlag, inUp: true}, cmd: "apply",
			want: "ssh ubuntu@192.0.2.10 ended with exit status 255. Check it logs in: ssh ubuntu@192.0.2.10 true",
		},
		"the admin's key is refused": {
			err: errors.New("ssh admin@192.0.2.10: exit status 255: admin@192.0.2.10: Permission denied (publickey)."), user: "admin", cmd: "status",
			want: "192.0.2.10 does not accept your SSH key for admin. Check which key logs in: ssh admin@192.0.2.10 true",
		},
		// What ssh said stays, on the one line.
		"another reason from ssh": {
			err: errors.New("ssh admin@192.0.2.10: exit status 255: kex_exchange_identification: Connection reset by peer"), user: "admin", cmd: "status",
			want: "ssh admin@192.0.2.10: exit status 255: kex_exchange_identification: Connection reset by peer. Check your key logs in: ssh admin@192.0.2.10 true",
		},
		// A host keelbox has not pinned: ssh-keyscan fails with exit 1 and
		// no reason, so a look at the port says which it is.
		"no host key, the port does not answer": {err: keyscan, user: "admin", cmd: "apply", port: errNoRoute, want: offLine},
		"no host key, the name does not resolve": {
			err: keyscan, user: "admin", cmd: "apply", port: errors.New("dial tcp: lookup 192.0.2.10: no such host"),
			want: "192.0.2.10 does not resolve to an address; check the name, or use the IP address",
		},
		"no host key, the port answers": {
			err: keyscan, user: "admin", cmd: "apply",
			want: "192.0.2.10 answers on port 22 but offered no SSH host key to pin (ssh-keyscan 192.0.2.10: exit status 1). Check SSH runs there: ssh admin@192.0.2.10 true",
		},
		"keelbox on the box failed": {err: exitStatus(1), user: "admin", cmd: "doctor", keeps: true},
		"not ssh":                   {err: errors.New("no keelbox binary for arm64"), user: "admin", cmd: "plan", keeps: true},
		"the command on the box":    {err: errors.New("ssh admin@192.0.2.10: exit status 1: cat: nothing"), user: "admin", keeps: true},
		"a host key nobody confirmed": {
			err: &provider.HostKeyError{Host: "192.0.2.10", Offered: []provider.HostKey{{Type: "ssh-ed25519", Blob: "AAAAC3NzaC1lZDI1NTE5AAAAIKeelbox"}}}, user: "admin", cmd: "plan",
			want: "192.0.2.10 is not a host keelbox has connected to, and no terminal is attached to confirm its host key. It offers " + hostKeyFP + " (ssh-ed25519). " +
				"Read the server's key in the provider's console, or ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the server. In ~/.config/keelbox/boxes/lab.yaml, under target.ssh, set host_key: SHA256:... to that key. Then run: keelbox plan lab",
		},
		"a host key other than the pinned one": {
			err: &provider.HostKeyError{Host: "192.0.2.10", Want: "SHA256:" + strings.Repeat("A", 43), Offered: []provider.HostKey{{Type: "ssh-ed25519", Blob: "AAAAC3NzaC1lZDI1NTE5AAAAIKeelbox"}}}, user: "admin", cmd: "apply",
			want: "192.0.2.10 does not offer the host key SHA256:" + strings.Repeat("A", 43) + " that target.ssh.host_key pins; it offers " + hostKeyFP + " (ssh-ed25519). keelbox did not connect; if the server was reinstalled, that is expected. " +
				"Read the server's key in the provider's console, or ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the server. In ~/.config/keelbox/boxes/lab.yaml, under target.ssh, set host_key: SHA256:... to that key. Then run: keelbox apply lab",
		},
	} {
		if hk := (*provider.HostKeyError)(nil); errors.As(c.err, &hk) {
			if got := sshFailure(context.Background(), c.err, cfg, sshTarget(cfg, addr{"192.0.2.10", 22}, options{}, io.Discard, io.Discard), c.o, c.cmd); !errors.As(got, &hk) {
				t.Errorf("%s: the refusal is no longer found in %T", name, got)
			}
		}
		portAnswers(t, c.port)
		s := sshTarget(cfg, addr{"192.0.2.10", 22}, options{}, io.Discard, io.Discard).As(c.user)
		got := sshFailure(context.Background(), c.err, cfg, s, c.o, c.cmd)
		switch {
		case c.keeps && got != c.err:
			t.Errorf("%s: rewritten to %q", name, got)
		case !c.keeps && got.Error() != c.want:
			t.Errorf("%s:\ngot  %s\nwant %s", name, got, c.want)
		}
		// One line: a caller that shows the last line shows the reason.
		if strings.Contains(got.Error(), "\n") {
			t.Errorf("%s: more than one line: %q", name, got)
		}
		// Ctrl-C: the error is the user's doing, and stays as it is.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := sshFailure(ctx, c.err, cfg, s, c.o, c.cmd); got != c.err {
			t.Errorf("%s, after Ctrl-C: rewritten to %q", name, got)
		}
	}
}

// A session on the terminal: ssh printed its own reason there, and the
// session's own shell may end with 255. keelbox adds a line only when the
// port does not answer or there was no host key to pin.
func TestSessionFailureAddsWordsOnlyWhenTheBoxDoesNotAnswer(t *testing.T) {
	cfg := setUpBox(t)
	s := sshTarget(cfg, addr{"192.0.2.10", 22}, options{}, io.Discard, io.Discard).As("agent")
	ctx := context.Background()
	ended := exitStatus(255)
	if got := sessionFailure(ctx, ended, s); got != ended {
		t.Errorf("a session that ended with 255 on a box that answers: %q", got)
	}
	portAnswers(t, errNoRoute)
	const off = "192.0.2.10 does not answer on port 22; check it is on and reachable from here: ssh agent@192.0.2.10"
	if got := sessionFailure(ctx, ended, s); got.Error() != off {
		t.Errorf("a box that does not answer: %q", got)
	}
	if got := sessionFailure(ctx, fmt.Errorf("ssh-keyscan 192.0.2.10: %w", exitStatus(1)), s); got.Error() != off {
		t.Errorf("no host key from a box that does not answer: %q", got)
	}
	if left := exitStatus(3); sessionFailure(ctx, left, s) != left {
		t.Error("the session's own exit code was rewritten")
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if got := sessionFailure(stopped, ended, s); got != ended {
		t.Errorf("after Ctrl-C: %q", got)
	}
}

// The commands print it, each through its own path to ssh.
func TestCommandsSayWhatToDoWhenSSHFails(t *testing.T) {
	cfg := setUpBox(t)
	ctx := context.Background()
	box := addr{"192.0.2.10", 22}
	stub := stubSSH(t)
	var out, errOut bytes.Buffer
	said := func(code int) string {
		t.Helper()
		defer errOut.Reset()
		if code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
		return errOut.String()
	}

	// The box is off, and ssh says so.
	stub.fails("admin", 255, timedOut)
	stub.fails("agent", 255, timedOut)
	for _, cmd := range []string{"status", "plan", "apply"} {
		if got := said(runSSH(ctx, cmd, cfg, box, options{}, &out, &errOut)); got != "keelbox: "+offLine+"\n" {
			t.Errorf("%s on a box that is off:\n%s", cmd, got)
		}
	}
	if got := said(cmdSessions(ctx, cfg, options{}, &out, &errOut)); got != "keelbox: 192.0.2.10 does not answer on port 22; check it is on and reachable from here: ssh agent@192.0.2.10\n" {
		t.Errorf("sessions on a box that is off:\n%s", got)
	}

	// A session: ssh's reason went to the terminal, so keelbox looks at
	// the port.
	portAnswers(t, errors.New("dial tcp 192.0.2.10:22: i/o timeout"))
	stub.fails("admin", 255, "")
	stub.fails("agent", 255, "")
	const off = "keelbox: lab does not answer at 192.0.2.10 on port 22: check this computer is online and that lab is on. A session left open there keeps running. Then open it again: "
	if got := said(cmdAttach(ctx, cfg, options{}, &out, &errOut)); got != off+"keelbox attach lab\n" {
		t.Errorf("attach to a box that is off:\n%s", got)
	}
	if got := said(cmdShell(ctx, cfg, options{admin: true}, &out, &errOut)); got != off+"keelbox ssh lab --admin\n" {
		t.Errorf("ssh --admin to a box that is off:\n%s", got)
	}
	// The box answers: keelbox has the exit code and no more, and says how
	// to come back.
	portAnswers(t, nil)
	if got := said(cmdShell(ctx, cfg, options{}, &out, &errOut)); got != "keelbox: the session on lab ended with exit code 255, which is ssh's for a connection that failed. A session left open there keeps running. Open it again: keelbox ssh lab\n" {
		t.Errorf("a session that ended with 255:\n%s", got)
	}

	// An account the user named that no longer logs in.
	stub.fails("ubuntu", 255, "")
	// keelbox's own choice of account (a new cloud server's) is not a flag
	// to leave out.
	if got := said(runSSH(ctx, "plan", cfg, box, options{sshUser: "ubuntu"}, &out, &errOut)); got != "keelbox: ssh ubuntu@192.0.2.10 ended with exit status 255. Check it logs in: ssh ubuntu@192.0.2.10 true\n" {
		t.Errorf("plan as an account keelbox chose:\n%s", got)
	}
	if got := said(runSSH(ctx, "plan", cfg, box, options{sshUser: "ubuntu", set: userFlag}, &out, &errOut)); !strings.HasPrefix(got, "keelbox: ubuntu could not log in to 192.0.2.10. If lab is already set up, that is expected") ||
		!strings.Contains(got, "Leave out --ssh-user: keelbox plan lab. ") || strings.Contains(got, "exit status") {
		t.Errorf("plan --ssh-user ubuntu after setup:\n%s", got)
	}
}

// A host keelbox has not pinned and that does not answer gives no host
// key. Every command says so, and none exits without a word.
func TestCommandsSayWhenThereIsNoHostKeyToPin(t *testing.T) {
	setUpBox(t)
	cfg := labBox(t, "192.0.2.77", 22) // not pinned
	ctx := context.Background()
	box := addr{"192.0.2.77", 22}
	stub := stubSSH(t)
	stub.noHostKey()
	portAnswers(t, errors.New("dial tcp 192.0.2.77:22: i/o timeout"))
	const off = "192.0.2.77 does not answer on port 22; check it is on and reachable from here: ssh %s@192.0.2.77"
	var out bytes.Buffer
	for name, c := range map[string]struct {
		user string
		run  func(errOut io.Writer) int
	}{
		"apply":             {"admin", func(e io.Writer) int { return runSSH(ctx, "apply", cfg, box, options{}, &out, e) }},
		"plan":              {"admin", func(e io.Writer) int { return runSSH(ctx, "plan", cfg, box, options{}, &out, e) }},
		"plan, sudo asking": {"admin", func(e io.Writer) int { return runSSH(ctx, "plan", cfg, box, options{askSudo: true}, &out, e) }},
		"attach":            {"agent", func(e io.Writer) int { return cmdAttach(ctx, cfg, options{}, &out, e) }},
		"ssh":               {"agent", func(e io.Writer) int { return cmdShell(ctx, cfg, options{}, &out, e) }},
		"sessions":          {"agent", func(e io.Writer) int { return cmdSessions(ctx, cfg, options{}, &out, e) }},
	} {
		var errOut bytes.Buffer
		if code := c.run(&errOut); code != 1 || errOut.String() != "keelbox: "+fmt.Sprintf(off, c.user)+"\n" {
			t.Errorf("%s: exit %d\n%s", name, code, errOut.String())
		}
	}
	out.Reset()
	doctorSignIns(ctx, cfg, options{}, false, &out, io.Discard)
	if got, want := out.String(), "\nsign-in\n  warn  sign-ins                 not checked: "+fmt.Sprintf(off, "admin")+"\n"; got != want {
		t.Errorf("doctor's sign-in rows:\ngot  %q\nwant %q", got, want)
	}
	if stub.calls() != "" {
		t.Errorf("ssh ran without a pinned host key: %s", stub.calls())
	}
}

// destroy shows the last line of why it could not look for work on the
// box: that line carries ssh's reason, not only what to try.
func TestDestroyWorkCheckKeepsWhatSSHSaid(t *testing.T) {
	cfg := setUpBox(t)
	stubSSH(t).fails("admin", 255, "kex_exchange_identification: Connection reset by peer")
	_, err := boxProjects(context.Background(), cfg, "192.0.2.10", options{})
	if want := "ssh admin@192.0.2.10: exit status 255: kex_exchange_identification: Connection reset by peer. Check your key logs in: ssh admin@192.0.2.10 true"; err == nil || err.Error() != want {
		t.Errorf("got  %v\nwant %s", err, want)
	}
}

// keelbox up's first look at a server reads the account an Ubuntu cloud
// image names when it turns root away, from what the probe printed.
func TestInspectReadsTheAccountTheImageNames(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	isolate(t)
	// Something has to answer on the port before ssh is tried.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen on loopback:", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	cfg := labBox(t, "127.0.0.1", port)
	if err := sshTarget(cfg, addr{"127.0.0.1", port}, options{}, io.Discard, io.Discard).Pin(provider.ParseHostKeys(boxHostKey)); err != nil {
		t.Fatal(err)
	}
	stub := stubSSH(t)
	stub.fails("root", 142, "")
	stub.write("root.out", "Please login as the user \"ubuntu\" rather than the user \"root\".\n\n", 0o644)
	_, err = realRunner{}.inspect(context.Background(), cfg, "root", io.Discard)
	var acct *wrongAccount
	if !errors.As(err, &acct) || acct.user != "root" || acct.want != "ubuntu" || acct.host != "127.0.0.1" {
		t.Errorf("root on an Ubuntu cloud image: %v", err)
	}
	// Any other failure as that account is not that.
	stub.fails("ubuntu", 255, "ubuntu@127.0.0.1: Permission denied (publickey).")
	var refused *keyRefused
	if _, err := (realRunner{}).inspect(context.Background(), cfg, "ubuntu", io.Discard); !errors.As(err, &refused) {
		t.Errorf("a key the server refuses: %v", err)
	}
}

// A session ssh ended with its own exit code: keelbox's line has to say
// what to do without ssh's, which went with the full screen.
func TestSessionCutOffSaysTheSessionIsKeptAndHowToComeBack(t *testing.T) {
	cfg := setUpBox(t)
	ctx := context.Background()
	const again = "keelbox attach " + "acme"
	ended := exitStatus(255)
	line := func(host string) string {
		s := sshTarget(cfg, addr{host, 22}, options{}, io.Discard, io.Discard).As("agent")
		return sessionCutOff(ctx, sessionFailure(ctx, ended, s), ended, s, "acme", again).Error()
	}
	if got, want := line("192.0.2.10"), "the session on acme ended with exit code 255, which is ssh's for a connection that failed. A session left open there keeps running. Open it again: keelbox attach acme"; got != want {
		t.Errorf("a box that answers:\n got %q\nwant %q", got, want)
	}
	portAnswers(t, errNoRoute)
	if got, want := line("192.0.2.10"), "acme does not answer at 192.0.2.10 on port 22: check this computer is online and that acme is on. A session left open there keeps running. Then open it again: keelbox attach acme"; got != want {
		t.Errorf("a box that does not answer:\n got %q\nwant %q", got, want)
	}
	if got := line("100.79.108.37"); !strings.Contains(got, "check this computer is online and connected to Tailscale and that acme is on") {
		t.Errorf("a tailnet address: %q", got)
	}
	s := sshTarget(cfg, addr{"192.0.2.10", 22}, options{}, io.Discard, io.Discard).As("agent")
	// The session's own exit code, and Ctrl-C, stay as they are.
	if left := exitStatus(3); sessionCutOff(ctx, left, left, s, "acme", again) != left {
		t.Error("the session's own exit code was rewritten")
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if got := sessionCutOff(stopped, ended, ended, s, "acme", again); got != ended {
		t.Errorf("after Ctrl-C: %q", got)
	}
}

func TestCutOffAndTheTerminalRestore(t *testing.T) {
	if !cutOff(exitStatus(255)) || cutOff(exitStatus(1)) || cutOff(nil) || cutOff(errors.New("x")) {
		t.Error("cutOff is ssh's own exit code, 255, and nothing else that exits")
	}
	for _, off := range []string{"\x1b[?1049l", "\x1b[?1000l", "\x1b[?1003l", "\x1b[?1006l", "\x1b[?2004l", "\x1b[?25h"} {
		if !strings.Contains(terminalRestore, off) {
			t.Errorf("the restore lacks %q", off)
		}
	}
	// The cursor is saved before the alternate screen is left.
	if !strings.HasPrefix(terminalRestore, "\x1b7\x1b[?1049l") {
		t.Errorf("the restore starts %q", terminalRestore[:12])
	}
}

// hostKeyFP is the fingerprint of the host key the refusals above offer.
var hostKeyFP = provider.HostKey{Type: "ssh-ed25519", Blob: "AAAAC3NzaC1lZDI1NTE5AAAAIKeelbox"}.Fingerprint()
