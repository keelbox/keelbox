package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

func TestShellWords(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"ls", "-la", "/tmp"}, "ls -la /tmp"},
		{[]string{"ls", "my dir"}, "ls 'my dir'"},
		{[]string{"echo", "it's", "$HOME"}, `echo 'it'\''s' '$HOME'`},
		{[]string{"sh", "-c", "cd x && make"}, "sh -c 'cd x && make'"},
		{[]string{""}, "''"},
	} {
		if got := shellWords(c.in); got != c.want {
			t.Errorf("%q: %s, want %s", c.in, got, c.want)
		}
	}
}

// On this machine as the box: the command gets its words as given, its
// input, and keelbox exits with its code.
func TestRunOnBoxHere(t *testing.T) {
	cfg, _ := fakeVendors(t)
	var out, errb bytes.Buffer
	o := options{hasRemote: true, remote: []string{"sh", "-c", `printf '%s|' "$0" "$1"; exit 7`, "a b", "c'd"}}
	if code := runOnBox(context.Background(), cfg, o, "kbtest", &out, &errb); code != 7 || out.String() != "a b|c'd|" {
		t.Errorf("%d %q %q", code, out.String(), errb.String())
	}
	o.remote = nil
	if code := runOnBox(context.Background(), cfg, o, "kbtest", &out, &errb); code != 2 || !strings.Contains(errb.String(), "keelbox ssh lab -- uptime") {
		t.Errorf("no command: %d %s", code, errb.String())
	}
}

// Over SSH: no terminal (-T), the command's own exit code, and 255 with
// what to check when ssh did not log in. A host key not pinned is refused
// without a question.
func TestRunOnBoxOverSSH(t *testing.T) {
	cfg, _ := fakeVendors(t)
	cfg.Target.Provider = config.ProviderSSH
	cfg.Target.SSH.Host, cfg.Target.SSH.Port = "192.0.2.10", 22
	bin := t.TempDir()
	// A fake ssh: it fails like ssh itself when FAIL is set; otherwise it
	// runs LocalCommand, as ssh does once logged in, then the command.
	script := `#!/bin/sh
[ -n "$FAIL" ] && { echo "ssh: connect to host: Connection refused" >&2; exit 255; }
tty=no
while [ $# -gt 0 ]; do
  case "$1" in
    -o) case "$2" in LocalCommand=*) sh -c "${2#LocalCommand=}";; esac; shift 2;;
    -T) tty=T; shift;;
    -p|-i) shift 2;;
    --) shift; break;;
    *) shift;;
  esac
done
[ "$tty" = T ] || exit 99
exec sh -c "$1"
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := runOnBox(context.Background(), cfg, options{hasRemote: true, remote: args}, "kbtest", &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, _, e := run("true"); code != exitNoConnection || !strings.Contains(e, "no pinned host key for lab at 192.0.2.10") || !strings.Contains(e, "keelbox ssh lab") {
		t.Errorf("not pinned: %d %s", code, e)
	}
	s := provider.FromConfig(cfg)
	s.Host, s.Port = "192.0.2.10", 22
	if err := s.Pin([]provider.HostKey{{Type: "ssh-ed25519", Blob: "AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}}); err != nil {
		t.Fatal(err)
	}
	if code, out, e := run("sh", "-c", "echo hi; exit 255"); code != 255 || out != "hi\n" || e != "" {
		t.Errorf("the command's own 255: %d %q %q", code, out, e)
	}
	if code, out, _ := run("echo", "a  b"); code != 0 || out != "a  b\n" {
		t.Errorf("words: %d %q", code, out)
	}
	t.Setenv("FAIL", "1")
	if code, _, e := run("true"); code != exitNoConnection || !strings.Contains(e, "could not log in to lab as kbtest at 192.0.2.10; the command did not run") {
		t.Errorf("no connection: %d %s", code, e)
	}
}

// What follows -- is the box's: --help there is not keelbox's help.
func TestSSHDashDashIsTheBoxs(t *testing.T) {
	isolate(t)
	var out, errb bytes.Buffer
	Main([]string{"ssh", "nobox", "--", "ls", "--help"}, &out, &errb)
	if strings.Contains(out.String(), "Examples:") {
		t.Errorf("keelbox's help for a flag after --:\n%s", out.String())
	}
}

// Under an agent CLI, a question without a terminal ends with the command
// for the owner, not the flag that skips it.
func TestOrOwnerUnderAnAgent(t *testing.T) {
	if got := orOwner("without a terminal: keelbox park acme --yes", "keelbox park acme"); got != "without a terminal: keelbox park acme --yes" {
		t.Errorf("script: %s", got)
	}
	t.Setenv("CLAUDECODE", "1")
	if got := orOwner("without a terminal: keelbox park acme --yes", "keelbox park acme"); strings.Contains(got, "--yes") || !strings.HasSuffix(got, "ask them to run it on their terminal: keelbox park acme") {
		t.Errorf("agent: %s", got)
	}
}

// Under an agent with no terminal, ssh --admin refuses: root is the owner's.
func TestAdminRefusedUnderAnAgent(t *testing.T) {
	cfg, _ := fakeVendors(t)
	t.Setenv("CODEX_THREAD_ID", "x")
	var out bytes.Buffer
	old := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	defer func() { stdinIsTerminal = old }()
	o := options{admin: true, hasRemote: true, remote: []string{"true"}}
	if code := cmdShell(context.Background(), cfg, o, &out, &out); code != 2 || !strings.Contains(out.String(), "ask them to run it: keelbox ssh lab --admin") {
		t.Errorf("%d %s", code, out.String())
	}
}
