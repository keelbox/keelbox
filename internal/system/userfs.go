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
	"strconv"
	"strings"
	"syscall"
	"time"
)

// UserFS returns file access performed by a child process that runs as user,
// with its UID, primary GID and supplementary groups set by the kernel before
// exec. Whatever the path resolves to, symlinks included, the operation has
// exactly the rights that account has; root's are never used.
func (r *Real) UserFS(name string) FS { return &realUserFS{r: r, name: name} }

type realUserFS struct {
	r    *Real
	name string
}

// The scripts are constants: values reach them only as positional
// arguments, never as script text. Each uses coreutils by absolute PATH.
const (
	// errMissing is the exit code the scripts use for "does not exist".
	errMissing = 66

	statScript = `[ -e "$1" ] || [ -L "$1" ] || exit 66
stat -c '%f %u %g %s' -- "$1" || exit 1
if [ -L "$1" ]; then readlink -- "$1" || exit 1; fi`

	writeScript = `set -e
umask 022
dir=$(dirname -- "$1")
mkdir -p -- "$dir"
tmp=$(mktemp -- "$dir/.keelbox.XXXXXXXX")
trap 'rm -f -- "$tmp"' EXIT
cat >"$tmp"
chmod -- "$2" "$tmp"
if [ -n "$3" ]; then chgrp -- "$3" "$tmp"; fi
mv -fT -- "$tmp" "$1"
trap - EXIT`

	mkdirScript = `set -e
umask 022
mkdir -p -- "$1"
chmod -- "$2" "$1"
if [ -n "$3" ]; then chgrp -- "$3" "$1"; fi`

	symlinkScript = `exec ln -sfT -- "$2" "$1"`
	removeScript  = `exec rm -f -- "$1"`
)

// maxUserFile bounds a read: managed files are small, and a user's file must
// not make root's keelbox read without end.
const maxUserFile = 16 << 20

// userFSTimeout bounds one file operation as an account. They touch small
// files only, so this is reached only when something holds them up.
const userFSTimeout = 30 * time.Second

// readArg is the first argument of keelbox started again as an account to
// read one of its files (readAsAccount). It reads with the account's rights
// and nothing else: any account may run it.
const readArg = "__keelbox-read-as-account"

func init() {
	if len(os.Args) == 5 && os.Args[1] == readArg {
		os.Exit(readAsAccount(os.Args[2], os.Args[3], os.Args[4], os.Stdout, os.Stderr))
	}
}

// readAsAccount writes at most max bytes of path to w. It opens without
// blocking (a FIFO in place of the file cannot hold the read), without
// taking a controlling terminal, and with follow "nofollow" refuses a final
// symlink; only a regular file, as fstat reports the opened file, is read,
// so swapping the file between a check and the open gains nothing.
func readAsAccount(path, max, follow string, w, errw io.Writer) int {
	n, err := strconv.ParseInt(max, 10, 64)
	if err != nil || n < 0 {
		fmt.Fprintln(errw, "bad limit")
		return 2
	}
	flags := os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOCTTY
	if follow == "nofollow" {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(path, flags, 0) //nolint:gosec // G703: runs as the account, with only its rights; the path is root's choice
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errMissing
	case errors.Is(err, syscall.ELOOP):
		fmt.Fprintln(errw, path+": is a symbolic link")
		return 1
	case err != nil:
		fmt.Fprintln(errw, err)
		return 1
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	if !fi.Mode().IsRegular() {
		fmt.Fprintln(errw, path+": not a regular file")
		return 1
	}
	if _, err := io.Copy(w, io.LimitReader(f, n)); err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	return 0
}

var errNoAccount = errors.New("no such account")

// run executes script as the account with args, the first being the path.
func (u *realUserFS) run(op string, stdin []byte, script string, args ...string) (string, int, error) {
	return u.exec(op, stdin, "/bin/sh", append([]string{"-c", script, "keelbox-" + op}, args...), args)
}

// exec runs prog as the account, in a session of its own (no controlling
// terminal), from /, with a fixed environment and a time limit.
// args are what the log line shows, the first being the path.
func (u *realUserFS) exec(op string, stdin []byte, prog string, argv, args []string) (string, int, error) {
	acct, err := user.Lookup(u.name)
	if err != nil {
		var unk user.UnknownUserError
		if errors.As(err, &unk) {
			err = errNoAccount
		}
		return "", 0, fmt.Errorf("%s %s as %s: %w", op, args[0], u.name, err)
	}
	uid, _ := strconv.ParseUint(acct.Uid, 10, 32)
	gid, _ := strconv.ParseUint(acct.Gid, 10, 32)
	var groups []uint32
	gids, err := acct.GroupIds()
	if err != nil {
		return "", 0, fmt.Errorf("groups of %s: %w", u.name, err)
	}
	for _, g := range gids {
		if n, err := strconv.ParseUint(g, 10, 32); err == nil {
			groups = append(groups, uint32(n))
		}
	}
	if u.r.Log != nil {
		fmt.Fprintf(u.r.Log, "    (as %s) %s %s\n", u.name, op, strings.Join(args, " "))
	}
	ctx, cancel := context.WithTimeout(context.Background(), userFSTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + acct.HomeDir}
	cmd.Dir = "/"
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && ee.ExitCode() == errMissing:
		return "", errMissing, nil
	case err != nil:
		return "", 0, fmt.Errorf("%s %s as %s: %w: %s", op, args[0], u.name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), 0, nil
}

func (u *realUserFS) ReadFile(path string) ([]byte, error) {
	return u.read(path, maxUserFile, "follow")
}

func (u *realUserFS) ReadData(path string, max int64) ([]byte, error) {
	return u.read(path, max, "nofollow")
}

// read runs this keelbox again as the account to read path (readAsAccount),
// through /proc/self/exe: the running binary, root's, whichever path it was
// started from.
func (u *realUserFS) read(path string, max int64, follow string) ([]byte, error) {
	out, code, err := u.exec("read", nil, "/proc/self/exe", []string{readArg, path, strconv.FormatInt(max+1, 10), follow}, []string{path})
	switch {
	case errors.Is(err, errNoAccount) || code == errMissing:
		return nil, fs.ErrNotExist
	case err != nil:
		return nil, err
	case int64(len(out)) > max:
		return nil, fmt.Errorf("%s is larger than %d bytes", path, max)
	}
	return []byte(out), nil
}

func (u *realUserFS) Stat(path string) (FileInfo, error) {
	out, code, err := u.run("stat", nil, statScript, path)
	switch {
	case errors.Is(err, errNoAccount) || code == errMissing:
		return FileInfo{}, nil
	case err != nil:
		return FileInfo{}, err
	}
	return parseStat(path, out)
}

// parseStat reads `stat -c '%f %u %g %s'` and, for a symlink, its target.
func parseStat(path, out string) (FileInfo, error) {
	line, target, _ := strings.Cut(out, "\n")
	f := strings.Fields(line)
	if len(f) != 4 {
		return FileInfo{}, fmt.Errorf("stat %s: unexpected output %q", path, out)
	}
	raw, err1 := strconv.ParseUint(f[0], 16, 32)
	uid, err2 := strconv.Atoi(f[1])
	gid, err3 := strconv.Atoi(f[2])
	size, err4 := strconv.ParseInt(f[3], 10, 64)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return FileInfo{}, fmt.Errorf("stat %s: %w", path, err)
	}
	const typeMask, dir, link = 0o170000, 0o040000, 0o120000
	info := FileInfo{Exists: true, IsDir: raw&typeMask == dir, IsSymlink: raw&typeMask == link,
		Mode: fs.FileMode(raw & 0o777), Owner: uidName(uid), Group: gidName(gid), Size: size}
	if info.IsSymlink {
		info.LinkTarget = strings.TrimSuffix(target, "\n")
	}
	return info, nil
}

func (u *realUserFS) owns(path, owner string) error {
	if owner != "" && owner != u.name {
		return fmt.Errorf("%s: acting as %s, cannot make it owned by %s", path, u.name, owner)
	}
	return nil
}

func (u *realUserFS) WriteFile(path string, data []byte, mode fs.FileMode, owner, group string) error {
	if err := u.owns(path, owner); err != nil {
		return err
	}
	_, _, err := u.run("write", data, writeScript, path, strconv.FormatUint(uint64(mode.Perm()), 8), group)
	return err
}

func (u *realUserFS) MkdirAll(path string, mode fs.FileMode, owner, group string) error {
	if err := u.owns(path, owner); err != nil {
		return err
	}
	_, _, err := u.run("mkdir", nil, mkdirScript, path, strconv.FormatUint(uint64(mode.Perm()), 8), group)
	return err
}

func (u *realUserFS) Symlink(target, path string) error {
	_, _, err := u.run("symlink", nil, symlinkScript, path, target)
	return err
}

func (u *realUserFS) Remove(path string) error {
	_, _, err := u.run("remove", nil, removeScript, path)
	return err
}
