package system

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

// Fake is an in-memory Executor for tests. Commands are answered by
// registered handlers matched on their rendered string ("(as agent) mise ...");
// unmatched commands fail with exit 1, which most checks read as "not yet
// converged". Files and users live in maps.
type Fake struct {
	mu       sync.Mutex
	Files    map[string]*FakeFile
	Users    map[string]User
	Calls    []Cmd
	handlers []fakeHandler
}

// FakeFile is one in-memory file, directory or symlink.
type FakeFile struct {
	Data   []byte
	Mode   fs.FileMode
	Owner  string
	Group  string
	Dir    bool
	Target string // symlink target when non-empty
	// Size is what Stat reports for a file without Data, such as an
	// allocated swap file whose bytes no test reads.
	Size int64
	// Special marks a FIFO, socket or device: reads refuse it, as Real's
	// do, rather than block on it.
	Special bool
}

func (ff *FakeFile) size() int64 {
	if ff.Data == nil {
		return ff.Size
	}
	return int64(len(ff.Data))
}

type fakeHandler struct {
	prefix string
	fn     func(Cmd) (Result, error)
}

// NewFake returns an empty fake with a root user.
func NewFake() *Fake {
	return &Fake{
		Files: map[string]*FakeFile{},
		Users: map[string]User{"root": {Name: "root", Home: "/root"}},
	}
}

// On answers commands whose rendered form starts with prefix. Later
// registrations win, so a test can override a default.
func (f *Fake) On(prefix string, res Result) {
	f.OnFunc(prefix, func(c Cmd) (Result, error) {
		if res.ExitCode != 0 {
			return res, &ExitError{Cmd: c, Result: res}
		}
		return res, nil
	})
}

// OK answers commands starting with prefix with exit 0 and the given stdout.
func (f *Fake) OK(prefix, stdout string) { f.On(prefix, Result{Stdout: stdout}) }

// Fail answers commands starting with prefix with exit 1.
func (f *Fake) Fail(prefix string) { f.On(prefix, Result{ExitCode: 1}) }

// OnFunc registers a handler function.
func (f *Fake) OnFunc(prefix string, fn func(Cmd) (Result, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append(f.handlers, fakeHandler{prefix, fn})
}

// Ran reports whether any recorded command starts with prefix.
func (f *Fake) Ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if strings.HasPrefix(c.String(), prefix) {
			return true
		}
	}
	return false
}

// AddFile seeds a root-owned file.
func (f *Fake) AddFile(p, content string, mode fs.FileMode) {
	f.Files[p] = &FakeFile{Data: []byte(content), Mode: mode, Owner: "root", Group: "root"}
}

// AddUser seeds an account and its home directory.
func (f *Fake) AddUser(name string, uid int) {
	home := "/home/" + name
	f.Users[name] = User{Name: name, UID: uid, GID: uid, Home: home}
	if _, ok := f.Files[home]; !ok {
		f.Files[home] = &FakeFile{Dir: true, Mode: 0o750, Owner: name, Group: name}
	}
}

func (f *Fake) Run(_ context.Context, c Cmd) (Result, error) {
	if err := checkEnv(c); err != nil {
		return Result{ExitCode: -1}, err
	}
	if strings.Contains(c.Name, "/") && !path.IsAbs(c.Name) {
		return Result{ExitCode: -1}, fmt.Errorf("%s: a command with a slash must be an absolute path", c.Name)
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, c)
	s := c.String()
	var h *fakeHandler
	for i := len(f.handlers) - 1; i >= 0; i-- {
		if strings.HasPrefix(s, f.handlers[i].prefix) {
			h = &f.handlers[i]
			break
		}
	}
	f.mu.Unlock()
	if h == nil {
		res := Result{ExitCode: 1, Stderr: "fake: no handler"}
		return res, &ExitError{Cmd: c, Result: res}
	}
	return h.fn(c)
}

// The fake's file system resolves symlinks in every component the way the
// kernel does. Directories not in Files are plain root-owned directories.
// Root's methods apply CheckRootPath first, as Real's do; UserFS checks a
// simplified form of the account's permissions (owner bits and other bits,
// no groups).

func (f *Fake) lstat(p string) (FileInfo, error) {
	p = path.Clean(p)
	ff, ok := f.Files[p]
	if !ok {
		// A directory holding seeded entries exists, root's, 0755.
		prefix := strings.TrimSuffix(p, "/") + "/"
		for k := range f.Files {
			if strings.HasPrefix(k, prefix) {
				return FileInfo{Exists: true, IsDir: true, Mode: 0o755, Owner: "root", Group: "root"}, nil
			}
		}
		return FileInfo{}, nil
	}
	return FileInfo{Exists: true, IsDir: ff.Dir, IsSymlink: ff.Target != "", LinkTarget: ff.Target,
		Mode: ff.Mode, Owner: ff.Owner, Group: ff.Group, Size: ff.size()}, nil
}

// resolve returns the real path p names: symlinks are followed in every
// component but the last, and in the last too when follow is set.
func (f *Fake) resolve(p string, follow bool) (string, error) {
	dir, rest, hops := "/", components(path.Clean(p)), 0
	for len(rest) > 0 {
		c := rest[0]
		rest = rest[1:]
		next := path.Join(dir, c)
		if ff, ok := f.Files[next]; ok && ff.Target != "" && (len(rest) > 0 || follow) {
			if hops++; hops > maxHops {
				return "", fmt.Errorf("%s: too many levels of symbolic links", p)
			}
			if path.IsAbs(ff.Target) {
				dir = "/"
			}
			rest = append(components(ff.Target), rest...)
			continue
		}
		if ff, ok := f.Files[next]; ok && !ff.Dir && ff.Target == "" && len(rest) > 0 {
			return "", fmt.Errorf("%s: %s is not a directory", p, next)
		}
		dir = next
	}
	return dir, nil
}

// asRoot resolves p for a root operation after CheckRootPath allows it.
func (f *Fake) asRoot(p string, follow bool) (string, error) {
	if err := CheckRootPath(p, f.lstat); err != nil {
		return "", err
	}
	return f.resolve(p, follow)
}

func (f *Fake) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.asRoot(p, true)
	if err != nil {
		return nil, err
	}
	ff, ok := f.Files[r]
	if !ok || ff.Dir {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), ff.Data...), nil
}

func (f *Fake) Stat(p string) (FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.asRoot(p, false)
	if err != nil {
		return FileInfo{}, err
	}
	return f.lstat(r)
}

func defaults(owner, group string) (string, string) {
	if owner == "" {
		owner = "root"
	}
	if group == "" {
		group = owner
	}
	return owner, group
}

func (f *Fake) WriteFile(p string, data []byte, mode fs.FileMode, owner, group string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.asRoot(p, false)
	if err != nil {
		return err
	}
	owner, group = defaults(owner, group)
	f.Files[r] = &FakeFile{Data: append([]byte(nil), data...), Mode: mode, Owner: owner, Group: group}
	return nil
}

func (f *Fake) AppendFile(p string, data []byte, mode fs.FileMode, owner, group string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.asRoot(p, false)
	if err != nil {
		return err
	}
	ff, ok := f.Files[r]
	switch {
	case !ok:
		owner, group = defaults(owner, group)
		f.Files[r] = &FakeFile{Data: append([]byte(nil), data...), Mode: mode, Owner: owner, Group: group}
	case ff.Dir:
		return fmt.Errorf("append %s: is a directory", p)
	case ff.Target != "":
		return fmt.Errorf("append %s: is a symbolic link", p)
	default:
		ff.Data = append(ff.Data, data...)
	}
	return nil
}

func (f *Fake) ReadDir(p string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.asRoot(p, true)
	if err != nil {
		return nil, err
	}
	if st, _ := f.lstat(r); !st.Exists || !st.IsDir {
		return nil, fs.ErrNotExist
	}
	seen := map[string]bool{}
	prefix := strings.TrimSuffix(r, "/") + "/"
	for k := range f.Files {
		if rest, ok := strings.CutPrefix(k, prefix); ok && rest != "" {
			name, _, _ := strings.Cut(rest, "/")
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func (f *Fake) MkdirAll(p string, mode fs.FileMode, owner, group string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.asRoot(p, true)
	if err != nil {
		return err
	}
	owner, group = defaults(owner, group)
	f.Files[r] = &FakeFile{Dir: true, Mode: mode, Owner: owner, Group: group}
	return nil
}

func (f *Fake) Symlink(target, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.asRoot(p, false)
	if err != nil {
		return err
	}
	f.Files[r] = &FakeFile{Target: target, Mode: 0o777, Owner: "root", Group: "root"}
	return nil
}

func (f *Fake) Remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.asRoot(p, false)
	if err != nil {
		return err
	}
	delete(f.Files, r)
	return nil
}

// UserFS returns the fake's file access as user.
func (f *Fake) UserFS(user string) FS { return &fakeUserFS{f: f, user: user} }

type fakeUserFS struct {
	f    *Fake
	user string
}

func (u *fakeUserFS) denied(op, p string) error {
	return fmt.Errorf("%s %s as %s: %w", op, p, u.user, fs.ErrPermission)
}

// canWrite reports whether the account may create or replace entries in dir,
// or in the directory mkdir -p would create them under.
func (u *fakeUserFS) canWrite(dir string) error {
	for d := dir; ; d = path.Dir(d) {
		if ff, ok := u.f.Files[d]; ok {
			if !ff.Dir {
				return fmt.Errorf("%s is not a directory", d)
			}
			if ff.Owner == u.user || ff.Mode&0o002 != 0 {
				return nil
			}
			return fs.ErrPermission
		}
		if d == "/" {
			return fs.ErrPermission // unmodelled directories are root's
		}
	}
}

func (u *fakeUserFS) exists() bool {
	_, ok := u.f.Users[u.user]
	return ok
}

func (u *fakeUserFS) ReadFile(p string) ([]byte, error) {
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	if !u.exists() {
		return nil, fs.ErrNotExist
	}
	r, err := u.f.resolve(p, true)
	if err != nil {
		return nil, err
	}
	ff, ok := u.f.Files[r]
	switch {
	case !ok:
		return nil, fs.ErrNotExist
	case ff.Dir || ff.Special:
		return nil, fmt.Errorf("%s: not a regular file", p)
	case (ff.Owner != u.user || ff.Mode&0o400 == 0) && ff.Mode&0o004 == 0:
		return nil, u.denied("read", p)
	}
	return append([]byte(nil), ff.Data...), nil
}

func (u *fakeUserFS) ReadData(p string, max int64) ([]byte, error) {
	u.f.mu.Lock()
	r, err := u.f.resolve(p, false)
	ff, ok := u.f.Files[r]
	u.f.mu.Unlock()
	switch {
	case err != nil:
		return nil, err
	case ok && ff.Target != "":
		return nil, fmt.Errorf("%s: is a symbolic link", p)
	}
	data, err := u.ReadFile(p)
	if err == nil && int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, max)
	}
	return data, err
}

func (u *fakeUserFS) Stat(p string) (FileInfo, error) {
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	if !u.exists() {
		return FileInfo{}, nil
	}
	r, err := u.f.resolve(p, false)
	if err != nil {
		return FileInfo{}, err
	}
	return u.f.lstat(r)
}

func (u *fakeUserFS) group(p, owner, group string) (string, error) {
	if owner != "" && owner != u.user {
		return "", fmt.Errorf("%s: acting as %s, cannot make it owned by %s", p, u.user, owner)
	}
	if group == "" {
		group = u.user
	}
	return group, nil
}

// mkdirParents creates the missing directories above r as the account.
func (u *fakeUserFS) mkdirParents(r string) {
	for d := path.Dir(r); d != "/"; d = path.Dir(d) {
		if _, ok := u.f.Files[d]; ok {
			return
		}
		u.f.Files[d] = &FakeFile{Dir: true, Mode: 0o755, Owner: u.user, Group: u.user}
	}
}

func (u *fakeUserFS) WriteFile(p string, data []byte, mode fs.FileMode, owner, group string) error {
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	group, err := u.group(p, owner, group)
	if err != nil {
		return err
	}
	r, err := u.f.resolve(p, false)
	if err != nil {
		return err
	}
	if err := u.canWrite(path.Dir(r)); err != nil {
		return u.denied("write", p)
	}
	if ff, ok := u.f.Files[r]; ok && ff.Dir {
		return fmt.Errorf("write %s as %s: is a directory", p, u.user)
	}
	u.mkdirParents(r)
	u.f.Files[r] = &FakeFile{Data: append([]byte(nil), data...), Mode: mode, Owner: u.user, Group: group}
	return nil
}

func (u *fakeUserFS) MkdirAll(p string, mode fs.FileMode, owner, group string) error {
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	group, err := u.group(p, owner, group)
	if err != nil {
		return err
	}
	r, err := u.f.resolve(p, true)
	if err != nil {
		return err
	}
	if ff, ok := u.f.Files[r]; ok {
		if !ff.Dir {
			return fmt.Errorf("mkdir %s as %s: not a directory", p, u.user)
		}
		if ff.Owner != u.user {
			return u.denied("chmod", p) // only the owner may chmod
		}
		ff.Mode, ff.Group = mode, group
		return nil
	}
	if err := u.canWrite(path.Dir(r)); err != nil {
		return u.denied("mkdir", p)
	}
	u.mkdirParents(r)
	u.f.Files[r] = &FakeFile{Dir: true, Mode: mode, Owner: u.user, Group: group}
	return nil
}

func (u *fakeUserFS) Symlink(target, p string) error {
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	r, err := u.f.resolve(p, false)
	if err != nil {
		return err
	}
	if err := u.canWrite(path.Dir(r)); err != nil {
		return u.denied("symlink", p)
	}
	if ff, ok := u.f.Files[r]; ok && ff.Dir {
		return fmt.Errorf("symlink %s as %s: is a directory", p, u.user)
	}
	u.f.Files[r] = &FakeFile{Target: target, Mode: 0o777, Owner: u.user, Group: u.user}
	return nil
}

func (u *fakeUserFS) Remove(p string) error {
	u.f.mu.Lock()
	defer u.f.mu.Unlock()
	r, err := u.f.resolve(p, false)
	if err != nil {
		return err
	}
	if _, ok := u.f.Files[r]; !ok {
		return nil
	}
	if err := u.canWrite(path.Dir(r)); err != nil {
		return u.denied("remove", p)
	}
	delete(u.f.Files, r)
	return nil
}

func (f *Fake) LookupUser(name string) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.Users[name]
	if !ok {
		return User{}, ErrNoUser
	}
	return u, nil
}

// Commands returns the rendered commands run so far, for assertions.
func (f *Fake) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = c.String()
	}
	return out
}

// Paths returns the sorted file paths, for assertions.
func (f *Fake) Paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.Files))
	for p := range f.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
