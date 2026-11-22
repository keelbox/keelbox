package system

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// shim writes an executable name in dir that leaves a marker when run, as
// an agent's ~/.local/bin/tmux would.
func shim(t *testing.T, dir, name string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return marker
}

// A root command's bare name is never looked up in the PATH keelbox was
// started with: a directory another account can write, first on it, gets
// nothing run.
func TestRootCommandIgnoresTheCallersPATH(t *testing.T) {
	dir := t.TempDir()
	marker := shim(t, dir, "true")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if _, err := (&Real{}).Run(context.Background(), C("true")); err != nil {
		t.Fatalf("true: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a root command ran a program from the caller's PATH")
	}
	out, err := Output(context.Background(), &Real{}, C("sh", "-c", `echo "$PATH"`))
	if err != nil || out != SystemPath {
		t.Errorf("root command PATH = %q, %v; want %s", out, err, SystemPath)
	}
}

// The command line for another account's command names root's programs by
// absolute path, and gives the account's program AccountPath, never a
// directory in its home, however its home is filled.
func TestAccountCommandResolvesInRootDirectoriesOnly(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".local/bin", ".local/share/mise/shims"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
		shim(t, filepath.Join(home, d), "sh")
	}
	prog, err := lookPath("sh", AccountPath, lstat, isExecutable)
	if err != nil || strings.HasPrefix(prog, home) || !filepath.IsAbs(prog) {
		t.Fatalf("sh resolved to %q, %v", prog, err)
	}
	u := User{Name: "agent", UID: 1002, GID: 1002, Home: home}
	for _, scope := range []bool{true, false} {
		argv := accountArgv(u, As("agent", "sh", "-c", "true"), prog, scope)
		if scope && (argv[0] != systemdRunBin || !contains(argv, "--slice=user-1002.slice")) {
			t.Errorf("not in the account's slice: %q", argv)
		}
		i := indexOf(argv, runuserBin)
		if i < 0 || argv[i+4] != envBin || argv[i+5] != "-i" {
			t.Fatalf("runuser and env not by absolute path: %q", argv)
		}
		var path string
		for _, a := range argv {
			if p, ok := strings.CutPrefix(a, "PATH="); ok {
				path = p
			}
		}
		if path != AccountPath || strings.Contains(path, home) {
			t.Errorf("PATH = %q", path)
		}
		if indexOf(argv, prog) < 0 {
			t.Errorf("program not by absolute path: %q", argv)
		}
	}
}

func contains(s []string, v string) bool { return indexOf(s, v) >= 0 }

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestLookPathRefusesDirectoriesAnotherAccountCanChange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the temp dir is root's")
	}
	dir := t.TempDir()
	shim(t, dir, "kbtool")
	if p, err := lookPath("kbtool", dir, lstat, isExecutable); err == nil {
		t.Errorf("found %s in a directory a non-root account owns", p)
	}
	if _, err := lookPath("bin/sh", SystemPath, lstat, isExecutable); err == nil {
		t.Error("a relative path with a slash was run")
	}
}

func TestPATHInACommandsEnvironmentIsRefused(t *testing.T) {
	c := As("agent", "tmux", "ls")
	c.Env = []string{"PATH=/home/agent/.local/bin"}
	if _, err := (&Real{}).Run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "fixed PATH") {
		t.Errorf("Real: %v", err)
	}
	f := NewFake()
	f.OK("(as agent) tmux", "")
	if _, err := f.Run(context.Background(), c); err == nil {
		t.Error("Fake ran a command with its own PATH")
	}
	if _, err := f.Run(context.Background(), As("agent", "./tmux")); err == nil {
		t.Error("Fake ran a relative path")
	}
}

// A program that leaves a child holding its output open, or never exits,
// does not hold Run past its limits.
func TestRunIsBoundedByOutputAndTimeout(t *testing.T) {
	old := waitDelay
	waitDelay = 200 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })
	start := time.Now()
	out, err := Output(context.Background(), &Real{}, C("sh", "-c", "sleep 30 & echo up"))
	if err != nil || out != "up" || time.Since(start) > 5*time.Second {
		t.Errorf("background child: %q %v after %s", out, err, time.Since(start))
	}
	start = time.Now()
	c := C("sleep", "30")
	c.Timeout = 200 * time.Millisecond
	if _, err := (&Real{}).Run(context.Background(), c); err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("timeout: %v after %s", err, time.Since(start))
	}
}

// The read keelbox runs as an account opens without blocking and reads only
// a regular file: a FIFO swapped in for the file cannot hold it, and with
// nofollow neither can a symlink take it elsewhere.
func TestReadAsAccountRefusesWhatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "events.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- readAsAccount(fifo, "100", "nofollow", &out, &errb) }()
	select {
	case code := <-done:
		if code != 1 || !strings.Contains(errb.String(), "not a regular file") {
			t.Errorf("FIFO: exit %d, %q", code, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read blocked on a FIFO")
	}
	file := filepath.Join(dir, "real")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := readAsAccount(link, "100", "nofollow", &out, &errb); code != 1 || !strings.Contains(errb.String(), "symbolic link") {
		t.Errorf("symlink with nofollow: exit %d, %q", code, errb.String())
	}
	out.Reset()
	if code := readAsAccount(link, "4", "follow", &out, &errb); code != 0 || out.String() != "0123" {
		t.Errorf("follow, limited: exit %d, %q", code, out.String())
	}
	if code := readAsAccount(filepath.Join(dir, "none"), "4", "follow", &out, &errb); code != errMissing {
		t.Errorf("missing: exit %d", code)
	}
}

func TestFakeReadDataRefusesLinksAndSpecialFiles(t *testing.T) {
	f := NewFake()
	f.AddUser("agent", 1002)
	f.Files["/home/agent/f"] = &FakeFile{Data: []byte("ok"), Mode: 0o600, Owner: "agent"}
	f.Files["/home/agent/link"] = &FakeFile{Target: "/home/agent/f", Owner: "agent"}
	f.Files["/home/agent/fifo"] = &FakeFile{Special: true, Mode: 0o600, Owner: "agent"}
	fs := f.UserFS("agent")
	if b, err := ReadData(fs, "/home/agent/f", 10); err != nil || string(b) != "ok" {
		t.Errorf("file: %q %v", b, err)
	}
	if _, err := ReadData(fs, "/home/agent/f", 1); err == nil {
		t.Error("read past the limit")
	}
	for _, p := range []string{"/home/agent/link", "/home/agent/fifo"} {
		if _, err := ReadData(fs, p, 10); err == nil {
			t.Errorf("%s read", p)
		}
	}
}

func TestAccountLookPath(t *testing.T) {
	f := NewFake()
	f.AddUser("agent", 1002)
	fs := f.UserFS("agent")
	f.Files["/home/agent/.local/bin/claude"] = &FakeFile{Target: "../share/claude/versions/2", Owner: "agent"}
	f.Files["/home/agent/.local/share/claude/versions/2"] = &FakeFile{Data: []byte("x"), Mode: 0o755, Owner: "agent"}
	f.Files["/home/agent/.local/bin/dangling"] = &FakeFile{Target: "nowhere", Owner: "agent"}
	f.Files["/home/agent/.local/bin/plain"] = &FakeFile{Data: []byte("x"), Mode: 0o644, Owner: "agent"}
	if p, err := AccountLookPath(fs, "/home/agent", "claude"); err != nil || p != "/home/agent/.local/bin/claude" {
		t.Errorf("claude: %q %v", p, err)
	}
	for _, n := range []string{"dangling", "plain", "../x", ""} {
		if p, err := AccountLookPath(fs, "/home/agent", n); err == nil {
			t.Errorf("%q found at %s", n, p)
		}
	}
}
