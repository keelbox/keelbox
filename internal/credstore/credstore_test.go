package credstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secret = "tok3nDoNotLeak0123456789"

// call is one store-tool run the fake saw.
type call struct {
	stdin string
	args  []string
}

// fakeTool imitates security or secret-tool, keeping entries in a map, and
// fails the test if the secret ever appears in a command's arguments.
type fakeTool struct {
	t       *testing.T
	entries map[string]string
	calls   []call
	// down makes every call fail as a tool without a session bus would.
	down bool
}

func (f *fakeTool) run(_ context.Context, stdin, name string, args ...string) (string, error) {
	f.calls = append(f.calls, call{stdin, append([]string{name}, args...)})
	for _, a := range args {
		if strings.Contains(a, secret) {
			f.t.Fatalf("secret in the arguments of %s %v", name, args)
		}
	}
	if f.down {
		return "", &ExitError{Code: 1, Stderr: "Cannot autolaunch D-Bus without X11 $DISPLAY"}
	}
	if f.entries == nil {
		f.entries = map[string]string{}
	}
	switch {
	case name == "security" && len(args) == 1 && args[0] == "-i":
		// add-generic-password -U -s keelbox -a KEY -l "..." -w "SECRET"
		f2 := strings.Fields(stdin)
		key, val := "", ""
		for i := range f2 {
			if f2[i] == "-a" {
				key = f2[i+1]
			}
			if f2[i] == "-w" {
				val = strings.Trim(f2[i+1], `"`)
			}
		}
		f.entries[key] = val
		return "", nil
	case name == "security" && args[0] == "find-generic-password":
		if v, ok := f.entries[args[4]]; ok {
			return v + "\n", nil
		}
		return "", &ExitError{Code: 44, Stderr: "The specified item could not be found in the keychain."}
	case name == "security" && args[0] == "delete-generic-password":
		if _, ok := f.entries[args[4]]; !ok {
			return "", &ExitError{Code: 44}
		}
		delete(f.entries, args[4])
		return "", nil
	case name == "secret-tool":
		key := args[len(args)-1]
		switch args[0] {
		case "store":
			f.entries[key] = stdin
			return "", nil
		case "lookup":
			if v, ok := f.entries[key]; ok {
				return v, nil
			}
			return "", &ExitError{Code: 1}
		case "clear":
			delete(f.entries, key)
			return "", nil
		}
	}
	f.t.Fatalf("unexpected call %s %v", name, args)
	return "", nil
}

func roundTrip(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Get(ctx, "hetzner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty store: %v", err)
	}
	if err := s.Set(ctx, "hetzner", secret); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, "hetzner"); err != nil || got != secret {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := s.Delete(ctx, "hetzner"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "hetzner"); err != nil {
		t.Fatalf("deleting an absent entry: %v", err)
	}
	if _, err := s.Get(ctx, "hetzner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestKeychainPassesTheSecretOnStdin(t *testing.T) {
	f := &fakeTool{t: t}
	roundTrip(t, &Keychain{Run: f.run})
	var sawStdin bool
	for _, c := range f.calls {
		if strings.Contains(c.stdin, secret) {
			sawStdin = true
			if c.args[1] != "-i" {
				t.Errorf("secret on stdin of %v, want security -i", c.args)
			}
		}
	}
	if !sawStdin {
		t.Error("the secret never reached security's stdin")
	}
	if err := (&Keychain{Run: f.run}).Set(context.Background(), "hetzner", `bad"token`); err == nil {
		t.Error("a token with a quote was passed to security -i")
	}
}

func TestSecretServicePassesTheSecretOnStdin(t *testing.T) {
	f := &fakeTool{t: t}
	roundTrip(t, &SecretService{Run: f.run})
	if f.calls[1].stdin != secret || f.calls[1].args[1] != "store" {
		t.Errorf("store call %+v", f.calls[1])
	}
}

func TestFileStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	s := &File{Dir: dir}
	roundTrip(t, s)
	if err := s.Set(context.Background(), "hetzner", secret); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "hetzner"): 0o600} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v (%v)", p, fi.Mode().Perm(), want, err)
		}
	}
	_ = os.Chmod(filepath.Join(dir, "hetzner"), 0o644)
	if _, err := s.Get(context.Background(), "hetzner"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("loose file read: %v", err)
	}
}

func TestChainFallsBackToTheFileAndWarns(t *testing.T) {
	ctx := context.Background()
	var warned []string
	f := &fakeTool{t: t, down: true}
	c := &Chain{OS: &SecretService{Run: f.run}, File: &File{Dir: t.TempDir()}, Warn: func(s string) { warned = append(warned, s) }}
	if err := c.Set(ctx, "hetzner", secret); err != nil {
		t.Fatal(err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "mode 600") || strings.Contains(warned[0], secret) {
		t.Errorf("warning %q", warned)
	}
	if !strings.HasPrefix(c.StoredIn(), "a mode 600 file") {
		t.Errorf("stored in %q", c.StoredIn())
	}
	if got, err := c.Get(ctx, "hetzner"); err != nil || got != secret {
		t.Errorf("get after fallback: %q %v", got, err)
	}
	if err := c.Delete(ctx, "hetzner"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "hetzner"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func TestChainPrefersTheOSStoreAndRemovesAFileCopy(t *testing.T) {
	ctx := context.Background()
	file := &File{Dir: t.TempDir()}
	_ = file.Set(ctx, "hetzner", "old-file-token")
	f := &fakeTool{t: t}
	c := &Chain{OS: &Keychain{Run: f.run}, File: file, Warn: func(s string) { t.Errorf("unexpected warning %s", s) }}
	if got, _ := c.Get(ctx, "hetzner"); got != "old-file-token" {
		t.Errorf("file entry not found through the chain: %q", got)
	}
	if err := c.Set(ctx, "hetzner", secret); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Get(ctx, "hetzner"); !errors.Is(err, ErrNotFound) {
		t.Errorf("file copy kept after storing in the Keychain: %v", err)
	}
	if c.StoredIn() != "the macOS Keychain" {
		t.Errorf("stored in %q", c.StoredIn())
	}
}

func TestForcedFileStore(t *testing.T) {
	t.Setenv("KEELBOX_CREDENTIAL_STORE", "file")
	if c := Default(t.TempDir(), nil); c.OS != nil {
		t.Errorf("OS store %s used despite KEELBOX_CREDENTIAL_STORE=file", c.OS.Name())
	}
}

func TestValidKey(t *testing.T) {
	for k, ok := range map[string]bool{"hetzner": true, "hetzner.work": true, "": false, "../x": false, "Hetzner": false} {
		if (ValidKey(k) == nil) != ok {
			t.Errorf("%q: %v", k, ValidKey(k))
		}
	}
}
