package engine

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/keelbox/keelbox/internal/system"
)

// File is a managed file: exact content, mode and ownership. It is read and
// written as its owner (Env.FS).
type File struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
	Owner   string // default root
	Group   string // default Owner
	// Validate, when set, runs after writing; on error the previous content
	// is restored (or the file removed) and the error returned.
	Validate func(ctx context.Context, e *Env) error
	// OnChange runs after a successful write.
	OnChange func(ctx context.Context, e *Env) error
}

func (f File) owner() (string, string) {
	o, g := f.Owner, f.Group
	if o == "" {
		o = "root"
	}
	if g == "" {
		g = o
	}
	return o, g
}

// FileStep converges one file.
func FileStep(name string, f File) Step {
	o, _ := f.owner()
	return forOwner(Step{
		Name: name,
		Check: func(ctx context.Context, e *Env) (Status, error) {
			return checkFile(e, f)
		},
		Apply: func(ctx context.Context, e *Env) error {
			return writeFile(ctx, e, f)
		},
	}, o)
}

func checkFile(e *Env, f File) (Status, error) {
	o, g := f.owner()
	x := e.FS(o)
	st, err := x.Stat(f.Path)
	if err != nil {
		return Status{}, err
	}
	if !st.Exists {
		return Pending("create %s", f.Path), nil
	}
	if st.IsSymlink || st.IsDir {
		return Pending("replace %s (not a regular file)", f.Path), nil
	}
	data, err := x.ReadFile(f.Path)
	if err != nil {
		return Status{}, err
	}
	var diffs []string
	if !bytes.Equal(data, f.Content) {
		diffs = append(diffs, "content")
	}
	if st.Mode != f.Mode {
		diffs = append(diffs, fmt.Sprintf("mode %o->%o", st.Mode, f.Mode))
	}
	if st.Owner != o || st.Group != g {
		diffs = append(diffs, fmt.Sprintf("owner %s:%s->%s:%s", st.Owner, st.Group, o, g))
	}
	if len(diffs) > 0 {
		return Pending("update %s (%s)", f.Path, strings.Join(diffs, ", ")), nil
	}
	return Converged(), nil
}

func writeFile(ctx context.Context, e *Env, f File) error {
	o, g := f.owner()
	x := e.FS(o)
	var prev []byte
	st, err := x.Stat(f.Path)
	if err != nil {
		return err
	}
	if st.Exists && !st.IsDir && !st.IsSymlink {
		// A file that cannot be read cannot be restored: stop before
		// replacing it, rather than remove it if validation fails.
		if prev, err = x.ReadFile(f.Path); err != nil {
			return err
		}
	}
	if err := x.WriteFile(f.Path, f.Content, f.Mode, o, g); err != nil {
		return err
	}
	if f.Validate != nil {
		if verr := f.Validate(ctx, e); verr != nil {
			if prev != nil {
				_ = x.WriteFile(f.Path, prev, st.Mode, o, g)
			} else {
				_ = x.Remove(f.Path)
			}
			return fmt.Errorf("validate %s: %w (change rolled back)", f.Path, verr)
		}
	}
	if f.OnChange != nil {
		return f.OnChange(ctx, e)
	}
	return nil
}

// DirStep converges a directory, as its owner. Mode 0 means any mode is
// accepted (for directories a tool also manages, such as ~/.codex).
func DirStep(name, path string, mode fs.FileMode, owner, group string) Step {
	if group == "" {
		group = owner
	}
	return forOwner(Step{
		Name: name,
		Check: func(ctx context.Context, e *Env) (Status, error) {
			st, err := e.FS(owner).Stat(path)
			if err != nil {
				return Status{}, err
			}
			switch {
			case !st.Exists:
				return Pending("create %s", path), nil
			case !st.IsDir:
				return Status{}, fmt.Errorf("%s exists and is not a directory", path)
			case (mode != 0 && st.Mode != mode) || st.Owner != owner || st.Group != group:
				return Pending("fix %s to %o %s:%s", path, mode, owner, group), nil
			}
			return Converged(), nil
		},
		Apply: func(ctx context.Context, e *Env) error {
			m := mode
			if m == 0 {
				m = 0o755
			}
			return e.FS(owner).MkdirAll(path, m, owner, group)
		},
	}, owner)
}

// InstalledPackages returns which of pkgs dpkg reports as installed.
func InstalledPackages(ctx context.Context, e *Env, pkgs []string) (map[string]bool, error) {
	args := append([]string{"-W", "-f=${Package} ${db:Status-Abbrev}\n"}, pkgs...)
	// dpkg-query exits 1 when any package is unknown; stdout still lists the
	// known ones, so the exit code is ignored.
	r, _ := e.X.Run(ctx, system.C("dpkg-query", args...))
	out := map[string]bool{}
	for _, line := range strings.Split(r.Stdout, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.HasPrefix(f[1], "ii") {
			out[strings.Split(f[0], ":")[0]] = true
		}
	}
	return out, nil
}

// Apt runs apt-get with keelbox's non-interactive options. The lock timeout
// matters on fresh cloud servers, where apt-daily often holds the lock.
func Apt(ctx context.Context, e *Env, args ...string) error {
	_, err := e.X.Run(ctx, AptCmd(args...))
	return err
}

// AptCmd is apt-get with keelbox's non-interactive options and lock
// timeout, for a caller that reads what apt-get prints.
func AptCmd(args ...string) system.Cmd {
	base := []string{"-o", "DPkg::Lock::Timeout=600", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	return system.C("apt-get", append(base, args...)...)
}

// AptUpdate refreshes package lists when sources changed during this run
// (or on the first install of a run).
func AptUpdate(ctx context.Context, e *Env) error {
	if !e.AptStale {
		return nil
	}
	if err := Apt(ctx, e, "update"); err != nil {
		return err
	}
	e.AptStale = false
	return nil
}

// PackagesStep ensures apt packages are installed.
func PackagesStep(name string, pkgs func(e *Env) []string) Step {
	missing := func(ctx context.Context, e *Env) ([]string, error) {
		want := pkgs(e)
		have, err := InstalledPackages(ctx, e, want)
		if err != nil {
			return nil, err
		}
		var m []string
		for _, p := range want {
			if !have[p] {
				m = append(m, p)
			}
		}
		sort.Strings(m)
		return m, nil
	}
	return Step{
		Name: name,
		Check: func(ctx context.Context, e *Env) (Status, error) {
			m, err := missing(ctx, e)
			if err != nil || len(m) == 0 {
				return Converged(), err
			}
			return Pending("install %s", strings.Join(m, " ")), nil
		},
		Apply: func(ctx context.Context, e *Env) error {
			m, err := missing(ctx, e)
			if err != nil {
				return err
			}
			if err := AptUpdate(ctx, e); err != nil {
				return err
			}
			return Apt(ctx, e, append([]string{"install", "-y"}, m...)...)
		},
	}
}

// ServiceStep ensures a systemd unit is enabled and running.
func ServiceStep(name, unit string) Step {
	return Step{
		Name: name,
		Check: func(ctx context.Context, e *Env) (Status, error) {
			enabled := system.Succeeds(ctx, e.X, system.C("systemctl", "is-enabled", "--quiet", unit))
			active := system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", unit))
			switch {
			case enabled && active:
				return Converged(), nil
			case !enabled && !active:
				return Pending("enable and start %s", unit), nil
			case !enabled:
				return Pending("enable %s", unit), nil
			default:
				return Pending("start %s", unit), nil
			}
		},
		Apply: func(ctx context.Context, e *Env) error {
			_, err := e.X.Run(ctx, system.C("systemctl", "enable", "--now", unit))
			return err
		},
	}
}

// LineStep ensures a file contains an exact line, appending it if missing,
// leaving the rest of the file alone. New files get the given mode/owner;
// the file is read and written as owner.
func LineStep(name, path, line string, mode fs.FileMode, owner string) Step {
	has := func(e *Env) (bool, []byte, error) {
		data, err := e.FS(owner).ReadFile(path)
		if err != nil {
			st, serr := e.FS(owner).Stat(path)
			if serr == nil && !st.Exists {
				return false, nil, nil
			}
			return false, nil, err
		}
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimRight(l, "\r") == line {
				return true, data, nil
			}
		}
		return false, data, nil
	}
	return forOwner(Step{
		Name: name,
		Check: func(ctx context.Context, e *Env) (Status, error) {
			ok, _, err := has(e)
			if err != nil || ok {
				return Converged(), err
			}
			return Pending("add line to %s", path), nil
		},
		Apply: func(ctx context.Context, e *Env) error {
			_, data, err := has(e)
			if err != nil {
				return err
			}
			m := mode
			if st, _ := e.FS(owner).Stat(path); st.Exists {
				m = st.Mode
			}
			if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
				data = append(data, '\n')
			}
			data = append(data, []byte(line+"\n")...)
			return e.FS(owner).WriteFile(path, data, m, owner, owner)
		},
	}, owner)
}

// CommandStep is a check command plus an apply command, for state with no
// file to compare. Check exits 0 when converged.
func CommandStep(name, pending string, check, apply system.Cmd) Step {
	return Step{
		Name: name,
		Check: func(ctx context.Context, e *Env) (Status, error) {
			if system.Succeeds(ctx, e.X, check) {
				return Converged(), nil
			}
			return Pending("%s", pending), nil
		},
		Apply: func(ctx context.Context, e *Env) error {
			_, err := e.X.Run(ctx, apply)
			return err
		},
	}
}

// Block is a keelbox-managed region inside a file the user also edits
// (authorized_keys, .bashrc). Text outside the markers is never touched.
type Block struct {
	Path    string
	ID      string // marker id
	Content string // without markers; trailing newline optional
	Top     bool   // insert at the top of the file (before an rc file's interactive guard)
	Mode    fs.FileMode
	Owner   string
}

func (b Block) markers() (string, string) {
	return "# >>> keelbox " + b.ID + " >>>", "# <<< keelbox " + b.ID + " <<<"
}

// Render returns the file content with the block converged.
func (b Block) Render(existing string) string {
	begin, end := b.markers()
	body := strings.TrimRight(b.Content, "\n")
	block := begin + "\n" + body + "\n" + end + "\n"
	if body == "" {
		block = begin + "\n" + end + "\n"
	}
	if i := strings.Index(existing, begin); i >= 0 {
		if j := strings.Index(existing[i:], end); j >= 0 {
			rest := existing[i+j+len(end):]
			rest = strings.TrimPrefix(rest, "\n")
			return existing[:i] + block + rest
		}
	}
	if b.Top {
		return block + existing
	}
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	return existing + block
}

// BlockStep converges a managed block, reading and writing the file as the
// block's owner.
func BlockStep(name string, b Block) Step {
	read := func(e *Env) (string, system.FileInfo, error) {
		st, err := e.FS(b.Owner).Stat(b.Path)
		if err != nil || !st.Exists {
			return "", st, err
		}
		data, err := e.FS(b.Owner).ReadFile(b.Path)
		return string(data), st, err
	}
	return forOwner(Step{
		Name: name,
		Check: func(ctx context.Context, e *Env) (Status, error) {
			cur, st, err := read(e)
			if err != nil {
				return Status{}, err
			}
			if !st.Exists {
				return Pending("create %s", b.Path), nil
			}
			if b.Render(cur) != cur {
				return Pending("update keelbox block in %s", b.Path), nil
			}
			if st.Owner != b.Owner {
				return Pending("chown %s to %s", b.Path, b.Owner), nil
			}
			return Converged(), nil
		},
		Apply: func(ctx context.Context, e *Env) error {
			cur, st, err := read(e)
			if err != nil {
				return err
			}
			mode := b.Mode
			if st.Exists && st.Mode != 0 && b.Mode == 0 {
				mode = st.Mode
			}
			return e.FS(b.Owner).WriteFile(b.Path, []byte(b.Render(cur)), mode, b.Owner, "")
		},
	}, b.Owner)
}
