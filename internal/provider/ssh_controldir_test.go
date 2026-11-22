package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A long temporary directory, like macOS's /var/folders/.../T/, must not
// push ssh's control socket path past the Unix socket limit.
func TestControlDirFitsSocketLimit(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 60))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{long, t.TempDir()} {
		dir, err := controlDir(base)
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		if n := len(dir) + 1 + 40; n >= sunPathMax {
			t.Errorf("base %s: socket path %d bytes, limit %d", base, n, sunPathMax)
		}
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("base %s: %v, mode %v", base, err, fi.Mode())
		}
	}
	// the macOS case: base itself long, so /tmp is used
	dir, err := controlDir("/var/folders/1f/kjqs91_97f38__jxdlj4mlkh0000gn/T")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if !strings.HasPrefix(dir, "/tmp/keelbox-ssh-") {
		t.Errorf("long base: got %s, want under /tmp", dir)
	}
}
