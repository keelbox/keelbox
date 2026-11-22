package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/release"
)

// The release workflow's step, with a throwaway key: sign, then verify,
// and refuse a key the trusted list does not hold.
func TestSignVerifyAndRefuseAnUntrustedKey(t *testing.T) {
	dir := t.TempDir()
	keys, other := filepath.Join(dir, "k"), filepath.Join(dir, "o")
	for _, d := range []string{keys, other} {
		if err := run([]string{"testkey", d}); err != nil {
			t.Fatal(err)
		}
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	_ = os.WriteFile(sums, []byte("abc  keelbox-linux-amd64\n"), 0o600)
	priv, _ := os.ReadFile(filepath.Join(keys, "private.pem"))
	t.Setenv("KEELBOX_RELEASE_KEYS", filepath.Join(keys, "keys.pem"))
	t.Setenv("KEELBOX_RELEASE_SIGNING_KEY", string(priv))
	if err := run([]string{"sign", "v1.0.0", sums}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "v1.0.0", sums}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "v1.0.1", sums}); err == nil {
		t.Error("signature accepted for another version")
	}
	t.Setenv("KEELBOX_RELEASE_KEYS", filepath.Join(other, "keys.pem"))
	if err := run([]string{"sign", "v1.0.0", sums}); err == nil || !strings.Contains(err.Error(), "not one keelbox trusts") {
		t.Errorf("untrusted key: %v", err)
	}
	t.Setenv("KEELBOX_RELEASE_KEYS", "")
	if err := run([]string{"sign", "v1.0.0", sums}); err == nil {
		t.Error("signed with a key the embedded list does not hold")
	}
	if len(release.TrustedKeys()) == 0 {
		t.Error("no embedded keys")
	}
}
