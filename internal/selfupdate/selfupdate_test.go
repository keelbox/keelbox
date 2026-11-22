package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/release"
)

// channel serves a channel index and one release, like get.keelbox.dev and
// GitHub together, signed with a throwaway release key. The "binary" is a
// script that prints its version. indexSum overrides the checksum the index
// lists.
func channel(t *testing.T, version, indexSum string) (*release.Fetcher, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bin := []byte("#!/bin/sh\necho keelbox " + version + "\n")
	sum := fmt.Sprintf("%x", sha256.Sum256(bin))
	if indexSum == "" {
		indexSum = sum
	}
	sums := []byte(sum + "  keelbox-test-os\n")
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/stable.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"channel":"stable","version":%q,"releases":%q,"checksums":{"%s/test-os":%q}}`,
			version, release.ReleaseHosts[0], version, indexSum)
	})
	mux.HandleFunc("/dl/"+version+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { w.Write(sums) })
	mux.HandleFunc("/dl/"+version+"/SHA256SUMS.sig", func(w http.ResponseWriter, r *http.Request) {
		w.Write(ed25519.Sign(priv, release.SignedMessage(version, sums)))
	})
	mux.HandleFunc("/dl/"+version+"/keelbox-test-os", func(w http.ResponseWriter, r *http.Request) { w.Write(bin) })
	return &release.Fetcher{IndexURL: srv.URL + "/stable.json", BaseURL: srv.URL + "/dl", HTTP: srv.Client(),
		Keys: []ed25519.PublicKey{pub}}, bin
}

func TestUpdateReplacesSymlinkTarget(t *testing.T) {
	f, bin := channel(t, "v0.2.0", "")
	dir := t.TempDir()
	exe := filepath.Join(dir, "keelbox")
	os.WriteFile(exe, []byte("old"), 0o755)
	link := filepath.Join(dir, "link")
	os.Symlink(exe, link)

	got, err := Update(context.Background(), f, link, "v0.1.0", "test-os", false)
	if err != nil || got != "v0.2.0" {
		t.Fatalf("update: %q %v", got, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(bin) {
		t.Errorf("symlink target not replaced: %q", b)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced instead of its target")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("staging files left behind: %v", entries)
	}
	if _, err := Update(context.Background(), f, link, "v0.2.0", "test-os", false); !errors.Is(err, ErrUpToDate) {
		t.Errorf("second update: %v", err)
	}
}

func TestUpdateRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		indexSum, platform, current, want string
	}{
		"missing platform":              {"", "plan9-arm", "v0.1.0", "lists no checksum for keelbox v0.2.0 plan9-arm"},
		"origins disagree":              {strings.Repeat("0", 64), "test-os", "v0.1.0", "disagree"},
		"older release":                 {"", "test-os", "v0.3.0", "--allow-downgrade"},
		"older final than a prerelease": {"", "test-os", "v0.2.1-rc.1", "--allow-downgrade"},
		"development build":             {"", "test-os", "dev", "development build"},
	} {
		f, _ := channel(t, "v0.2.0", tc.indexSum)
		exe := filepath.Join(t.TempDir(), "keelbox")
		os.WriteFile(exe, []byte("old"), 0o755)
		if _, err := Update(context.Background(), f, exe, tc.current, tc.platform, false); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "old" {
			t.Errorf("%s: executable changed", name)
		}
	}
}

func TestDowngradeNeedsAllowDowngrade(t *testing.T) {
	f, bin := channel(t, "v0.2.0", "")
	exe := filepath.Join(t.TempDir(), "keelbox")
	os.WriteFile(exe, []byte("old"), 0o755)
	if _, err := Update(context.Background(), f, exe, "v0.3.0", "test-os", false); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("downgrade without the flag: %v", err)
	}
	if got, err := Update(context.Background(), f, exe, "v0.3.0", "test-os", true); err != nil || got != "v0.2.0" {
		t.Fatalf("downgrade with the flag: %q %v", got, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(bin) {
		t.Error("downgrade did not install")
	}
}
