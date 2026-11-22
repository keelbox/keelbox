// Package system is keelbox's only route to the machine it manages. Modules
// never call os/exec or touch the filesystem directly: they go through an
// Executor, so every module can be planned and tested against a Fake without
// root or a real Ubuntu host.
package system

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/plain"
)

// Cmd describes one command. Secrets must never be placed in Args or Env:
// commands are printed in verbose output and recorded in errors. Pass secrets
// by file path instead (for example tailscale's --auth-key=file:PATH).
type Cmd struct {
	Name  string
	Args  []string
	User  string   // run as this user (login-like environment) instead of root
	Env   []string // extra KEY=VALUE pairs
	Stdin []byte
	Dir   string
	// Timeout bounds the command; zero is none for root's commands and
	// AccountTimeout for another account's.
	Timeout time.Duration
}

// String renders the command for logs and error messages.
func (c Cmd) String() string {
	s := strings.Join(append([]string{c.Name}, c.Args...), " ")
	if c.User != "" {
		s = "(as " + c.User + ") " + s
	}
	return s
}

// Result is the outcome of a command that ran.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExitError is returned by Run when a command ran but exited non-zero.
type ExitError struct {
	Cmd    Cmd
	Result Result
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Result.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Result.Stdout)
	}
	if len(msg) > 2000 {
		msg = "..." + msg[len(msg)-2000:]
	}
	// The output may be another account's program's: printed plain.
	return plain.Text(fmt.Sprintf("%s: exit %d: %s", e.Cmd, e.Result.ExitCode, msg))
}

// FileInfo is the subset of file metadata keelbox converges.
type FileInfo struct {
	Exists     bool
	IsDir      bool
	IsSymlink  bool
	LinkTarget string
	Mode       fs.FileMode // permission bits only
	Owner      string
	Group      string
	Size       int64
}

// User is a resolved local account.
type User struct {
	Name string
	UID  int
	GID  int
	Home string
}

// ErrNoUser is returned by LookupUser for a missing account.
var ErrNoUser = errors.New("no such user")

// FS is file access with one account's rights.
type FS interface {
	ReadFile(path string) ([]byte, error)
	// Stat describes path itself: a final symlink is reported, not followed.
	Stat(path string) (FileInfo, error)
	// WriteFile replaces path atomically with the given content, mode and
	// ownership, creating missing parent directories 0755.
	WriteFile(path string, data []byte, mode fs.FileMode, owner, group string) error
	MkdirAll(path string, mode fs.FileMode, owner, group string) error
	Symlink(target, path string) error
	Remove(path string) error
}

// ReadData reads a file another account controls, which root takes as data
// (the agent's events or previews file): a final symlink is refused, only a
// regular file is read, its open never blocks (a FIFO in its place cannot
// hold the read), and a file larger than max is refused.
func ReadData(f FS, path string, max int64) ([]byte, error) {
	d, ok := f.(interface {
		ReadData(path string, max int64) ([]byte, error)
	})
	if !ok {
		return nil, fmt.Errorf("%s: this file access cannot read an account's data file", path)
	}
	return d.ReadData(path, max)
}

// Executor runs commands and manages files on the target machine.
//
// Its own file methods act as root, and only on paths no other account can
// redirect: CheckRootPath refuses a path unless every directory it passes
// through is owned by root and writable by no one else. Paths an account can
// change, such as anything in its home, go through UserFS, which acts with
// exactly that account's rights. Root therefore never follows a link another
// account planted, and an account gains nothing from apply that it could not
// already do itself (docs/adr/0015-file-access-as-the-owner.md).
type Executor interface {
	// Run executes a command. A non-zero exit returns *ExitError together
	// with the populated Result.
	Run(ctx context.Context, c Cmd) (Result, error)
	FS
	// AppendFile adds data to the end of a root-owned file in one write,
	// creating it with mode and ownership when missing. Writers that append
	// whole lines never interleave, so concurrent records need no lock and
	// no rewrite of what is already there. A final symlink is refused.
	AppendFile(path string, data []byte, mode fs.FileMode, owner, group string) error
	// ReadDir returns the sorted names in a directory that root's file
	// access may use; fs.ErrNotExist when it is missing.
	ReadDir(path string) ([]string, error)
	// UserFS returns file access performed by a process running as user.
	// An account that does not exist owns nothing: Stat reports nothing
	// there and ReadFile fs.ErrNotExist.
	UserFS(user string) FS
	LookupUser(name string) (User, error)
}

// UnsafePathError is returned for a root file operation on a path that a
// non-root account could redirect.
type UnsafePathError struct {
	Path, Dir, Why string
}

func (e *UnsafePathError) Error() string {
	return fmt.Sprintf("refusing to act as root on %s: %s %s; root only touches paths whose directories only root can change", e.Path, e.Dir, e.Why)
}

// maxHops bounds symlink resolution, as the kernel's ELOOP does.
const maxHops = 40

// CheckRootPath returns an *UnsafePathError unless every directory that
// resolving p walks through, including the targets of symlinks on the way,
// is owned by root and not writable by group or others. The final component
// is not checked: it sits in a directory only root can change. A component
// that does not exist ends the walk, since only root can create it. p must
// be absolute and clean. lstat
// describes a path without following a final symlink.
func CheckRootPath(p string, lstat func(string) (FileInfo, error)) error {
	if !path.IsAbs(p) || path.Clean(p) != p {
		// A ".." in p would resolve against the real directory, not the
		// lexical one, so only clean paths are walked.
		return fmt.Errorf("%s: not a clean absolute path", p)
	}
	check := func(dir string, st FileInfo) error {
		switch {
		case !st.IsDir:
			return &UnsafePathError{Path: p, Dir: dir, Why: "is not a directory"}
		case st.Owner != "root":
			return &UnsafePathError{Path: p, Dir: dir, Why: "is owned by " + st.Owner}
		case st.Mode&0o022 != 0:
			return &UnsafePathError{Path: p, Dir: dir, Why: fmt.Sprintf("is writable by group or others (mode %o)", st.Mode)}
		}
		return nil
	}
	root, err := lstat("/")
	if err != nil {
		return err
	}
	if root.Exists {
		if err := check("/", root); err != nil {
			return err
		}
	}
	dir, rest, hops := "/", components(path.Dir(p)), 0
	for len(rest) > 0 {
		c := rest[0]
		rest = rest[1:]
		if c == ".." {
			dir = path.Dir(dir) // dir holds no symlinks, so this is its real parent
			continue
		}
		next := path.Join(dir, c)
		st, err := lstat(next)
		if err != nil {
			return err
		}
		if !st.Exists {
			return nil
		}
		if st.IsSymlink {
			if hops++; hops > maxHops {
				return fmt.Errorf("%s: too many levels of symbolic links", p)
			}
			if path.IsAbs(st.LinkTarget) {
				dir = "/"
			}
			rest = append(components(st.LinkTarget), rest...)
			continue
		}
		if err := check(next, st); err != nil {
			return err
		}
		dir = next
	}
	return nil
}

// components splits a path into its names, dropping empty and "." parts.
func components(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

// Succeeds reports whether the command exits zero.
func Succeeds(ctx context.Context, x Executor, c Cmd) bool {
	_, err := x.Run(ctx, c)
	return err == nil
}

// Output runs a command and returns trimmed stdout.
func Output(ctx context.Context, x Executor, c Cmd) (string, error) {
	r, err := x.Run(ctx, c)
	return strings.TrimSpace(r.Stdout), err
}

// C is shorthand for a root command.
func C(name string, args ...string) Cmd { return Cmd{Name: name, Args: args} }

// As is shorthand for a command run as user.
func As(user, name string, args ...string) Cmd { return Cmd{Name: name, Args: args, User: user} }

// AccountLookPath finds name on an account's own PATH (UserPath) through f,
// the account's file access: the first directory holding an executable of
// that name, following symlinks, as the account's shell would. Commands run
// by keelbox never look a name up there themselves; this is for running an
// account's own tools, as the account, on purpose: a vendor-installed CLI,
// or a mise shim.
func AccountLookPath(f FS, home, name string) (string, error) {
	if name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("%q is not a command name", name)
	}
	for _, d := range strings.Split(UserPath(home), ":") {
		p := d + "/" + name
		if executable(f, p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not on the account's PATH", name)
}

// executable reports whether p, after up to maxHops symlinks, is a file
// with an execute bit.
func executable(f FS, p string) bool {
	for range maxHops {
		st, err := f.Stat(p)
		switch {
		case err != nil || !st.Exists:
			return false
		case st.IsSymlink:
			t := st.LinkTarget
			if !path.IsAbs(t) {
				t = path.Join(path.Dir(p), t)
			}
			p = t
			continue
		}
		return !st.IsDir && st.Mode&0o111 != 0
	}
	return false
}
