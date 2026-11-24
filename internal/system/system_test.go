package system

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRootPath(t *testing.T) {
	f := NewFake()
	dir := func(p string, mode fs.FileMode, owner string) {
		f.Files[p] = &FakeFile{Dir: true, Mode: mode, Owner: owner, Group: owner}
	}
	link := func(p, target string) { f.Files[p] = &FakeFile{Target: target, Owner: "root"} }
	dir("/etc", 0o755, "root")
	dir("/run", 0o755, "root")
	dir("/home/agent", 0o750, "agent")
	dir("/home/agent/proj", 0o755, "agent")
	dir("/srv/shared", 0o775, "root")
	dir("/tmp", 0o777, "root")
	link("/var/run", "/run")
	link("/etc/alt", "../home/agent")
	link("/home/agent/etc", "/etc")
	link("/etc/loop", "/etc/loop")

	for p, unsafe := range map[string]bool{
		"/etc/docker/daemon.json":  false, // missing dirs: only root can create them
		"/var/run/keelbox.pid":     false, // root's own symlink
		"/etc/hostname":            false,
		"/home/agent/.bashrc":      true, // a user's home
		"/home/agent/etc/docker/x": true, // a user's link to /etc is still the user's
		"/etc/alt/x":               true, // root's link into a user's home
		"/srv/shared/x":            true, // group-writable
		"/tmp/x":                   true, // world-writable, sticky or not
	} {
		err := CheckRootPath(p, f.lstat)
		var u *UnsafePathError
		if got := errors.As(err, &u); got != unsafe {
			t.Errorf("%s: unsafe=%v, want %v (%v)", p, got, unsafe, err)
		}
	}
	if err := CheckRootPath("/etc/loop/x", f.lstat); err == nil {
		t.Error("symlink loop accepted")
	}
	for _, p := range []string{"etc/hostname", "/home/agent/etc/../x", "/etc//hostname"} {
		if err := CheckRootPath(p, f.lstat); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
}

// The real lstat feeds the same check: a directory this test owns is
// refused, a system directory is not.
// In an unprivileged container /proc and /sys belong to the host's root,
// which the container sees as nobody. Only those kernel trees get that
// allowance: nobody's directory anywhere else, a writable one, or one in
// the cgroup tree, which systemd delegates to accounts, is still refused.
func TestCheckRootPathInAnUnprivilegedContainer(t *testing.T) {
	f := NewFake()
	dir := func(p string, mode fs.FileMode, owner string) {
		f.Files[p] = &FakeFile{Dir: true, Mode: mode, Owner: owner, Group: owner}
	}
	dir("/proc", 0o555, "nobody")
	dir("/proc/sys", 0o555, "nobody")
	dir("/proc/sys/kernel", 0o555, "nobody")
	dir("/proc/1", 0o555, "root")
	f.Files["/proc/self"] = &FakeFile{Target: "1", Owner: "root"}
	dir("/sys", 0o555, "nobody")
	dir("/sys/fs", 0o755, "nobody")
	dir("/sys/fs/cgroup", 0o755, "nobody")
	dir("/sys/fs/cgroup/user.slice", 0o755, "nobody")
	dir("/sys/kernel", 0o755, "nobody")
	dir("/sys/kernel/open", 0o777, "nobody")
	dir("/srv", 0o755, "nobody")
	dir("/proc/2", 0o755, "agent")

	for p, unsafe := range map[string]bool{
		"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": false,
		"/proc/self/exe":              false,
		"/sys/kernel/mm":              false,
		"/sys/fs/cgroup/user.slice/x": true, // delegated to accounts
		"/sys/kernel/open/x":          true, // writable by others
		"/srv/x":                      true, // nobody outside /proc and /sys
		"/proc/2/x":                   true, // another account's
	} {
		err := CheckRootPath(p, f.lstat)
		var u *UnsafePathError
		if got := errors.As(err, &u); got != unsafe {
			t.Errorf("%s: unsafe=%v, want %v (%v)", p, got, unsafe, err)
		}
	}
}

func TestCheckRootPathOnThisMachine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the temp dir is root's")
	}
	var u *UnsafePathError
	if err := CheckRootPath(filepath.Join(t.TempDir(), "f"), lstat); !errors.As(err, &u) {
		t.Errorf("own temp dir: %v", err)
	}
	if err := CheckRootPath("/etc/hostname", lstat); err != nil {
		t.Errorf("/etc/hostname: %v", err)
	}
	if _, err := (&Real{}).ReadFile(filepath.Join(t.TempDir(), "f")); !errors.As(err, &u) {
		t.Errorf("Real.ReadFile in a user's dir: %v", err)
	}
}

func TestFakeUserFSFollowsLinksWithTheUsersRights(t *testing.T) {
	f := NewFake()
	f.AddUser("agent", 1001)
	f.AddFile("/etc/shadow", "secret", 0o640)
	f.AddFile("/etc/hosts", "hosts", 0o644)
	f.Files["/home/agent/s"] = &FakeFile{Target: "/etc/shadow", Owner: "agent"}
	f.Files["/home/agent/h"] = &FakeFile{Target: "/etc/hosts", Owner: "agent"}
	f.Files["/home/agent/etc"] = &FakeFile{Target: "/etc", Owner: "agent"}
	u := f.UserFS("agent")
	if _, err := u.ReadFile("/home/agent/s"); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("read through a link to /etc/shadow: %v", err)
	}
	if d, err := u.ReadFile("/home/agent/h"); err != nil || string(d) != "hosts" {
		t.Errorf("read a world-readable file: %q %v", d, err)
	}
	if err := u.WriteFile("/home/agent/etc/x", []byte("x"), 0o644, "agent", ""); err == nil {
		t.Error("wrote into /etc through a link")
	}
	if err := u.MkdirAll("/home/agent/etc/docker", 0o700, "agent", ""); err == nil {
		t.Error("made a directory in /etc through a link")
	}
	// Replacing the link itself is the user's own business.
	if err := u.WriteFile("/home/agent/h", []byte("mine"), 0o644, "agent", ""); err != nil {
		t.Fatal(err)
	}
	if string(f.Files["/etc/hosts"].Data) != "hosts" || f.Files["/home/agent/h"].Target != "" {
		t.Error("write through a final symlink changed its target instead of replacing it")
	}
	if err := u.WriteFile("/home/agent/y", nil, 0o644, "root", ""); err == nil {
		t.Error("acting as agent, made a file root's")
	}
	if st, err := f.UserFS("nobody").Stat("/home/nobody/x"); err != nil || st.Exists {
		t.Errorf("an account that does not exist owns nothing: %+v %v", st, err)
	}
}

func TestParseStat(t *testing.T) {
	st, err := parseStat("/p", "a1ff 0 0 9\n/etc\n")
	if err != nil || !st.IsSymlink || st.LinkTarget != "/etc" || st.Owner != "root" || st.Mode != 0o777 {
		t.Errorf("symlink: %+v %v", st, err)
	}
	st, err = parseStat("/p", "41c0 0 0 4096\n")
	if err != nil || !st.IsDir || st.Mode != 0o700 {
		t.Errorf("dir: %+v %v", st, err)
	}
	st, err = parseStat("/p", "81a4 0 0 12\n")
	if err != nil || st.IsDir || st.IsSymlink || st.Mode != 0o644 || st.Size != 12 {
		t.Errorf("file: %+v %v", st, err)
	}
	if _, err := parseStat("/p", "garbage"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestFakeAppendFileAndReadDir(t *testing.T) {
	f := NewFake()
	f.AddUser("agent", 1001)
	f.AddFile("/var/lib/keelbox/q/b.json", "b", 0o600)
	f.AddFile("/var/lib/keelbox/q/a.json", "a", 0o600)
	f.AddFile("/var/lib/keelbox/q/sub/c.json", "c", 0o600)
	if err := f.AppendFile("/var/lib/keelbox/log", []byte("one\n"), 0o644, "root", "root"); err != nil {
		t.Fatal(err)
	}
	if err := f.AppendFile("/var/lib/keelbox/log", []byte("two\n"), 0o600, "agent", "agent"); err != nil {
		t.Fatal(err)
	}
	if ff := f.Files["/var/lib/keelbox/log"]; string(ff.Data) != "one\ntwo\n" || ff.Mode != 0o644 || ff.Owner != "root" {
		t.Errorf("append: %q %o %s; an existing file keeps its mode and owner", ff.Data, ff.Mode, ff.Owner)
	}
	f.Files["/var/lib/keelbox/link"] = &FakeFile{Target: "/etc/shadow", Owner: "root"}
	if err := f.AppendFile("/var/lib/keelbox/link", []byte("x"), 0o644, "", ""); err == nil {
		t.Error("appended through a final symlink")
	}
	var u *UnsafePathError
	if err := f.AppendFile("/home/agent/log", []byte("x"), 0o644, "", ""); !errors.As(err, &u) {
		t.Errorf("root appended in a user's home: %v", err)
	}
	names, err := f.ReadDir("/var/lib/keelbox/q")
	if err != nil || strings.Join(names, ",") != "a.json,b.json,sub" {
		t.Errorf("ReadDir: %v %v", names, err)
	}
	if _, err := f.ReadDir("/var/lib/keelbox/none"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadDir of a missing directory: %v", err)
	}
	if _, err := f.ReadDir("/home/agent/x"); !errors.As(err, &u) {
		t.Errorf("root listed a directory in a user's home: %v", err)
	}
}

func TestRealAppendFileRefusesAUsersDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the temp dir is root's")
	}
	var u *UnsafePathError
	dir := t.TempDir()
	if err := (&Real{}).AppendFile(filepath.Join(dir, "f"), []byte("x"), 0o644, "", ""); !errors.As(err, &u) {
		t.Errorf("AppendFile in a user's dir: %v", err)
	}
	if _, err := (&Real{}).ReadDir(dir); !errors.As(err, &u) {
		t.Errorf("ReadDir of a user's dir: %v", err)
	}
}
