// Package credstore keeps provider API tokens in the operating system's
// credential store: the macOS Keychain, or a Secret Service (GNOME Keyring,
// KWallet) through libsecret's secret-tool on Linux. Where neither is
// available it falls back to a mode 600 file and says so.
//
// A secret never appears in a command's arguments or environment: it is
// written to the store tool's standard input and read from its standard
// output (docs/adr/0026-credential-store.md).
package credstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// Service is the name every keelbox entry is stored under.
const Service = "keelbox"

// ErrNotFound means the store holds no entry for the key.
var ErrNotFound = errors.New("not in the credential store")

// ErrUnavailable means this store cannot be used on this machine.
var ErrUnavailable = errors.New("credential store unavailable")

// Store holds secrets by key, such as "hetzner".
type Store interface {
	// Name says where secrets go, for messages.
	Name() string
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, secret string) error
	// Delete removes the entry; an absent entry is not an error.
	Delete(ctx context.Context, key string) error
}

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidKey reports whether key can name an entry.
func ValidKey(key string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("credential name %q: use lowercase letters, digits, '.', '_' and '-'", key)
	}
	return nil
}

// Runner runs a store tool with stdin and returns its stdout. Tests
// replace it; the real one is exec.
type Runner func(ctx context.Context, stdin string, name string, args ...string) (stdout string, err error)

// ExitError is a tool's non-zero exit, with what it wrote to stderr.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return fmt.Sprintf("exit status %d: %s", e.Code, e.Stderr)
}

func execRunner(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), &ExitError{Code: ee.ExitCode(), Stderr: strings.TrimSpace(errb.String())}
	}
	return out.String(), err
}

// Chain tries an OS store first and a file second. Get returns the first
// entry found; Set uses the OS store and falls back to the file only when
// the OS store is unavailable, reporting the fallback through Warn.
type Chain struct {
	OS   Store // nil: none on this machine
	File *File
	// Warn receives a message when a secret is written to the file.
	Warn func(string)
	// last is where the last Set wrote.
	last Store
}

// Name says where a secret would be written.
func (c *Chain) Name() string {
	if c.OS != nil {
		return c.OS.Name()
	}
	return c.File.Name()
}

// Get looks in the OS store, then the file.
func (c *Chain) Get(ctx context.Context, key string) (string, error) {
	if c.OS != nil {
		s, err := c.OS.Get(ctx, key)
		switch {
		case err == nil:
			return s, nil
		case !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUnavailable):
			return "", err
		}
	}
	return c.File.Get(ctx, key)
}

// Set stores the secret in the OS store, or in the file when there is none.
func (c *Chain) Set(ctx context.Context, key, secret string) error {
	if c.OS != nil {
		err := c.OS.Set(ctx, key, secret)
		if err == nil {
			c.last = c.OS
			// An older copy in the file would shadow nothing, but would
			// outlive forget-token's intent; remove it.
			_ = c.File.Delete(ctx, key)
			return nil
		}
		if !errors.Is(err, ErrUnavailable) {
			return err
		}
	}
	if err := c.File.Set(ctx, key, secret); err != nil {
		return err
	}
	c.last = c.File
	if c.Warn != nil {
		c.Warn(fmt.Sprintf("no OS credential store here (macOS Keychain, or a Secret Service such as GNOME Keyring with secret-tool); "+
			"the token is in %s, mode 600, readable by any program running as you", c.File.path(key)))
	}
	return nil
}

// StoredIn says where the last Set wrote.
func (c *Chain) StoredIn() string {
	if c.last == nil {
		return c.Name()
	}
	if f, ok := c.last.(*File); ok {
		return f.Name()
	}
	return c.last.Name()
}

// Delete removes the entry from both stores.
func (c *Chain) Delete(ctx context.Context, key string) error {
	var errs []error
	if c.OS != nil {
		if err := c.OS.Delete(ctx, key); err != nil && !errors.Is(err, ErrUnavailable) {
			errs = append(errs, err)
		}
	}
	if err := c.File.Delete(ctx, key); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Default is the chain for this machine. KEELBOX_CREDENTIAL_STORE=file
// skips the OS store (for CI and tests); the file lives in dir.
func Default(dir string, warn func(string)) *Chain {
	c := &Chain{File: &File{Dir: dir}, Warn: warn}
	if os.Getenv("KEELBOX_CREDENTIAL_STORE") == "file" {
		return c
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("security"); err == nil {
			c.OS = &Keychain{Run: execRunner}
		}
	case "linux":
		if _, err := exec.LookPath("secret-tool"); err == nil && sessionBus() {
			c.OS = &SecretService{Run: execRunner}
		}
	}
	return c
}

// sessionBus reports whether a D-Bus session bus is likely reachable, which
// the Secret Service needs. Over plain SSH there is usually none.
func sessionBus() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		if _, err := os.Stat(filepath.Join(d, "bus")); err == nil { //nolint:gosec // G703: only stats the session bus socket
			return true
		}
	}
	return false
}

// --- macOS Keychain ---------------------------------------------------------

// Keychain stores generic passwords in the user's login keychain with
// /usr/bin/security. Writes go through `security -i`, which reads its
// command from stdin, so the secret is never in any process's arguments.
type Keychain struct{ Run Runner }

// Name implements Store.
func (k *Keychain) Name() string { return "the macOS Keychain" }

// errSecItemNotFound is security's exit status for a missing item.
const errSecItemNotFound = 44

// Get implements Store.
func (k *Keychain) Get(ctx context.Context, key string) (string, error) {
	out, err := k.Run(ctx, "", "security", "find-generic-password", "-s", Service, "-a", key, "-w")
	var ee *ExitError
	if errors.As(err, &ee) && ee.Code == errSecItemNotFound {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read %s from the Keychain: %w", key, err)
	}
	return strings.TrimSpace(out), nil
}

// keychainSafe are the characters a secret may hold to be passed inside
// double quotes to security -i without escaping.
var keychainSafe = regexp.MustCompile(`^[A-Za-z0-9._~+/=:@-]+$`)

// Set implements Store.
func (k *Keychain) Set(ctx context.Context, key, secret string) error {
	if !keychainSafe.MatchString(secret) {
		return errors.New("the token has characters keelbox does not pass to the Keychain; API tokens are letters and digits")
	}
	cmd := fmt.Sprintf("add-generic-password -U -s %s -a %s -l \"keelbox %s\" -w \"%s\"\n", Service, key, key, secret)
	if _, err := k.Run(ctx, cmd, "security", "-i"); err != nil {
		return fmt.Errorf("store %s in the Keychain: %w", key, err)
	}
	// security -i reports failures on stdout and still exits 0: read back.
	got, err := k.Get(ctx, key)
	if err != nil || got != secret {
		return fmt.Errorf("store %s in the Keychain: the entry could not be read back", key)
	}
	return nil
}

// Delete implements Store.
func (k *Keychain) Delete(ctx context.Context, key string) error {
	_, err := k.Run(ctx, "", "security", "delete-generic-password", "-s", Service, "-a", key)
	var ee *ExitError
	if errors.As(err, &ee) && ee.Code == errSecItemNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete %s from the Keychain: %w", key, err)
	}
	return nil
}

// --- Secret Service (libsecret) ---------------------------------------------

// SecretService stores secrets through libsecret's secret-tool, which reads
// the secret from stdin when stdin is not a terminal.
type SecretService struct{ Run Runner }

// Name implements Store.
func (s *SecretService) Name() string { return "the Secret Service (libsecret)" }

func attrs(key string) []string { return []string{"service", Service, "account", key} }

// Get implements Store. secret-tool exits 1 with nothing on stderr when the
// entry is missing.
func (s *SecretService) Get(ctx context.Context, key string) (string, error) {
	out, err := s.Run(ctx, "", "secret-tool", append([]string{"lookup"}, attrs(key)...)...)
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Stderr == "" {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("%w: %s", ErrUnavailable, ee.Stderr)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return strings.TrimSpace(out), nil
}

// Set implements Store. A failure to reach the service (no session bus, a
// locked keyring nobody can unlock) is ErrUnavailable, so the chain falls
// back to the file.
func (s *SecretService) Set(ctx context.Context, key, secret string) error {
	args := append([]string{"store", "--label=keelbox " + key}, attrs(key)...)
	if _, err := s.Run(ctx, secret, "secret-tool", args...); err != nil {
		return fmt.Errorf("%w: secret-tool store: %v", ErrUnavailable, err)
	}
	got, err := s.Get(ctx, key)
	if err != nil || got != secret {
		return fmt.Errorf("%w: secret-tool stored the token but it could not be read back", ErrUnavailable)
	}
	return nil
}

// Delete implements Store.
func (s *SecretService) Delete(ctx context.Context, key string) error {
	_, err := s.Run(ctx, "", "secret-tool", append([]string{"clear"}, attrs(key)...)...)
	var ee *ExitError
	if errors.As(err, &ee) && ee.Stderr == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: secret-tool clear: %v", ErrUnavailable, err)
	}
	return nil
}

// --- file fallback ----------------------------------------------------------

// File keeps each secret in its own mode 600 file in a mode 700 directory.
type File struct{ Dir string }

// Name implements Store.
func (f *File) Name() string { return "a mode 600 file in " + f.Dir }

func (f *File) path(key string) string { return filepath.Join(f.Dir, key) }

// Get implements Store, refusing a file others can read.
func (f *File) Get(_ context.Context, key string) (string, error) {
	p := f.path(key)
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by others (mode %o); fix it: chmod 600 %s", p, fi.Mode().Perm(), p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", ErrNotFound
	}
	return s, nil
}

// Set implements Store: written to a temporary file created mode 600, then
// renamed into place.
func (f *File) Set(_ context.Context, key, secret string) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(f.Dir, 0o700); err != nil { //nolint:gosec // G302: a directory, owner-only
		return err
	}
	tmp, err := os.CreateTemp(f.Dir, "."+key+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(secret + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path(key))
}

// Delete implements Store.
func (f *File) Delete(_ context.Context, key string) error {
	err := os.Remove(f.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// --- in memory, for tests ---------------------------------------------------

// Memory is a Store in memory, for tests of code that uses a store.
type Memory struct {
	mu sync.Mutex
	m  map[string]string
}

// Name implements Store.
func (m *Memory) Name() string { return "memory" }

// Get implements Store.
func (m *Memory) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.m[key]
	if !ok {
		return "", ErrNotFound
	}
	return s, nil
}

// Set implements Store.
func (m *Memory) Set(_ context.Context, key, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]string{}
	}
	m.m[key] = secret
	return nil
}

// Delete implements Store.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, key)
	return nil
}
