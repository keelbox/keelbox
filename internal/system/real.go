package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Real executes on the local machine. It is used by `keelbox apply` on the
// box itself; remote providers copy the binary over and run Real there.
type Real struct {
	// Log, when set, receives one line per command (verbose mode).
	Log io.Writer
}

// UserPath is the PATH of the agent's own shells, as the managed shell
// profile sets it: the user's mise shims and ~/.local/bin, where the
// vendors' installers put the agent CLIs, then the system's. Root's runs
// never look a bare name up in it: directories in a home are the account's
// to fill (ADR 0053). AccountLookPath reads it on purpose, to find the
// account's own tools, the agent CLIs among them, which root then starts as
// the account, never as root.
func UserPath(home string) string {
	return home + "/.local/share/mise/shims:" + home + "/.local/bin:" + home + "/.local/share/pnpm:" + SystemPath
}

// SystemPath is the PATH of every command keelbox runs as root, whatever
// PATH keelbox itself was started with: directories only root can change.
const SystemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// AccountPath is the PATH of every command keelbox runs as another account.
// No directory in it is one the account can write.
const AccountPath = SystemPath

// The programs that start a command as another account, by absolute path.
const (
	runuserBin    = "/usr/sbin/runuser"
	envBin        = "/usr/bin/env"
	systemdRunBin = "/usr/bin/systemd-run"
)

// AccountTimeout bounds a command run as another account whose Cmd sets no
// Timeout, so an account's program cannot hold a root run without end.
const AccountTimeout = 30 * time.Minute

// waitDelay is how long Run waits for a command's output to close after the
// command exits or is stopped: a child it left in the background holding
// stdout open does not hold Run.
var waitDelay = 5 * time.Second

// lookPath resolves name the way keelbox runs it: a name with a slash must
// be absolute and is used as it is; a bare name is looked up only in dirs,
// and only where every directory on the way is root's and writable by no
// one else (CheckRootPath). The PATH keelbox was started with is never read.
func lookPath(name, dirs string, lstat func(string) (FileInfo, error), isExec func(string) bool) (string, error) {
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(name) {
			return "", fmt.Errorf("%s: a command with a slash must be an absolute path", name)
		}
		return name, nil
	}
	if name == "" {
		return "", errors.New("empty command name")
	}
	for _, d := range filepath.SplitList(dirs) {
		p := d + "/" + name
		if CheckRootPath(p, lstat) != nil {
			continue
		}
		if isExec(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in %s", name, dirs)
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// booted reports whether systemd runs this machine, which a scope needs.
func booted() bool {
	fi, err := os.Stat("/run/systemd/system")
	return err == nil && fi.IsDir()
}

// accountArgv is the command line that runs c as u. It starts from root's
// absolute paths only, in a scope in the account's own slice when systemd
// runs the machine, so the account's limits cover what it starts there and
// anything left running stays the account's (users.agent.limits). env -i
// starts the command with a fixed environment and AccountPath: a name is
// never looked up in the account's home.
func accountArgv(u User, c Cmd, prog string, scope bool) []string {
	var argv []string
	if scope {
		argv = append(argv, systemdRunBin, "--scope", "--quiet", "--collect",
			"--slice=user-"+strconv.Itoa(u.UID)+".slice", "--")
	}
	argv = append(argv, runuserBin, "-u", u.Name, "--", envBin, "-i",
		"HOME="+u.Home, "USER="+u.Name, "LOGNAME="+u.Name, "SHELL=/bin/bash",
		"LANG=C.UTF-8", "PATH="+AccountPath,
		"XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(u.UID))
	argv = append(argv, c.Env...)
	argv = append(argv, prog)
	return append(argv, c.Args...)
}

// checkEnv refuses a PATH in a command's extra environment: commands run
// with SystemPath or AccountPath only.
func checkEnv(c Cmd) error {
	for _, kv := range c.Env {
		if strings.HasPrefix(kv, "PATH=") {
			return fmt.Errorf("%s: commands run with keelbox's fixed PATH; resolve the program to an absolute path instead", c)
		}
	}
	return nil
}

// rootEnv is root's environment for a command: keelbox's own, with
// SystemPath for PATH.
func rootEnv(extra []string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	env = append(env, "PATH="+SystemPath, "DEBIAN_FRONTEND=noninteractive", "LANG=C.UTF-8")
	return append(env, extra...)
}

func (r *Real) Run(ctx context.Context, c Cmd) (Result, error) {
	if err := checkEnv(c); err != nil {
		return Result{ExitCode: -1}, err
	}
	var argv, env []string
	dir := c.Dir
	timeout := c.Timeout
	attr := &syscall.SysProcAttr{}
	if c.User != "" {
		u, err := r.LookupUser(c.User)
		if err != nil {
			return Result{ExitCode: -1}, fmt.Errorf("run as %s: %w", c.User, err)
		}
		prog, err := lookPath(c.Name, AccountPath, lstat, isExecutable)
		if err != nil {
			return Result{ExitCode: -1}, fmt.Errorf("run as %s: %w", c.User, err)
		}
		// A system account is no one's limits to keep.
		argv = accountArgv(u, c, prog, booted() && u.UID >= 1000)
		// runuser applies the account's groups (docker, for example). A
		// new session has no controlling terminal: a program of the
		// account's cannot open the admin's terminal through /dev/tty
		// while an apply runs on one.
		attr.Setsid = true
		if dir == "" {
			dir = u.Home
		}
		if timeout == 0 {
			timeout = AccountTimeout
		}
	} else {
		prog, err := lookPath(c.Name, SystemPath, lstat, isExecutable)
		if err != nil {
			return Result{ExitCode: -1}, fmt.Errorf("%s: %w", c, err)
		}
		argv = append([]string{prog}, c.Args...)
		env = rootEnv(c.Env)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if r.Log != nil {
		fmt.Fprintf(r.Log, "    $ %s\n", c)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = attr
	if attr.Setsid {
		// Stop everything the command started in its session, not only
		// the command.
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	}
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// It exited 0, but something it started kept its output open.
		err = nil
	}
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			if timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				res.Stderr += "\nkeelbox: stopped after " + timeout.String()
			}
			return res, &ExitError{Cmd: c, Result: res}
		}
		res.ExitCode = -1
		return res, fmt.Errorf("%s: %w", c, err)
	}
	return res, nil
}

// guard refuses root file operations on paths another account could redirect.
func (r *Real) guard(p string) error { return CheckRootPath(p, lstat) }

func (r *Real) ReadFile(path string) ([]byte, error) {
	if err := r.guard(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (r *Real) Stat(path string) (FileInfo, error) {
	if err := r.guard(path); err != nil {
		return FileInfo{}, err
	}
	return lstat(path)
}

func lstat(path string) (FileInfo, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return FileInfo{}, nil
	}
	if err != nil {
		return FileInfo{}, err
	}
	info := FileInfo{Exists: true, IsDir: fi.IsDir(), Mode: fi.Mode().Perm(), Size: fi.Size()}
	if fi.Mode()&fs.ModeSymlink != 0 {
		info.IsSymlink = true
		info.LinkTarget, _ = os.Readlink(path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		info.Owner = uidName(int(st.Uid))
		info.Group = gidName(int(st.Gid))
	}
	return info, nil
}

func uidName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

func gidName(gid int) string {
	if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
		return g.Name
	}
	return strconv.Itoa(gid)
}

func ids(owner, group string) (int, int, error) {
	uid, gid := 0, 0
	if owner != "" {
		u, err := user.Lookup(owner)
		if err != nil {
			return 0, 0, fmt.Errorf("owner %s: %w", owner, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
		gid, _ = strconv.Atoi(u.Gid)
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return 0, 0, fmt.Errorf("group %s: %w", group, err)
		}
		gid, _ = strconv.Atoi(g.Gid)
	}
	return uid, gid, nil
}

func (r *Real) WriteFile(path string, data []byte, mode fs.FileMode, owner, group string) error {
	if err := r.guard(path); err != nil {
		return err
	}
	uid, gid, err := ids(owner, group)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".keelbox-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chown(uid, gid); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if r.Log != nil {
		fmt.Fprintf(r.Log, "    write %s (%o %s:%s)\n", path, mode, owner, group)
	}
	return os.Rename(tmp.Name(), path)
}

func (r *Real) AppendFile(path string, data []byte, mode fs.FileMode, owner, group string) error {
	if err := r.guard(path); err != nil {
		return err
	}
	uid, gid, err := ids(owner, group)
	if err != nil {
		return err
	}
	// O_EXCL tells a file this call created, which gets mode and owner,
	// from one that was there, which keeps its own.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	switch {
	case err == nil:
		if err := f.Chmod(mode); err != nil {
			f.Close()
			return err
		}
		if err := f.Chown(uid, gid); err != nil {
			f.Close()
			return err
		}
	case errors.Is(err, fs.ErrExist):
		if f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0); err != nil {
			return err
		}
	default:
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if r.Log != nil {
		fmt.Fprintf(r.Log, "    append %s (%d bytes)\n", path, len(data))
	}
	return f.Close()
}

func (r *Real) ReadDir(path string) ([]string, error) {
	if err := r.guard(path); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}

func (r *Real) MkdirAll(path string, mode fs.FileMode, owner, group string) error {
	if err := r.guard(path); err != nil {
		return err
	}
	uid, gid, err := ids(owner, group)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return os.Lchown(path, uid, gid)
}

// Symlink replaces path with a symlink. The parent must exist.
func (r *Real) Symlink(target, path string) error {
	if err := r.guard(path); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Symlink(target, path)
}

func (r *Real) Remove(path string) error {
	if err := r.guard(path); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (r *Real) LookupUser(name string) (User, error) {
	u, err := user.Lookup(name)
	if err != nil {
		var unk user.UnknownUserError
		if errors.As(err, &unk) {
			return User{}, ErrNoUser
		}
		return User{}, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return User{Name: u.Username, UID: uid, GID: gid, Home: u.HomeDir}, nil
}
