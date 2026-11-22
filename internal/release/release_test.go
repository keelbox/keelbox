package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// testChannel serves an index and a release host on one test server, like
// get.keelbox.dev and GitHub together, for release v1.2.3 signed with a
// throwaway key. Fields can be changed before the first request.
type testChannel struct {
	srv   *httptest.Server
	bin   []byte
	sums  []byte
	sig   []byte
	index Index
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	hits  map[string]int
}

func newChannel(t *testing.T) *testChannel {
	t.Helper()
	c := &testChannel{bin: []byte("\x7fELF keelbox"), hits: map[string]int{}}
	pub, priv := newKey(t)
	c.pub = pub
	c.sums = []byte(sumOf([]byte("other")) + "  keelbox-linux-amd64\n" + sumOf(c.bin) + "  keelbox-linux-arm64\n")
	c.priv = priv
	c.sig = ed25519.Sign(priv, SignedMessage("v1.2.3", c.sums))
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits[r.URL.Path]++
		switch r.URL.Path {
		case "/stable.json":
			_ = json.NewEncoder(w).Encode(c.index)
		case "/dl/v1.2.3/SHA256SUMS":
			_, _ = w.Write(c.sums)
		case "/dl/v1.2.3/SHA256SUMS.sig":
			if c.sig == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(c.sig)
		case "/dl/v1.2.3/keelbox-linux-arm64":
			_, _ = w.Write(c.bin)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(c.srv.Close)
	c.index = Index{Channel: "stable", Version: "v1.2.3", Releases: ReleaseHosts[0],
		Checksums: map[string]string{"v1.2.3/linux-arm64": sumOf(c.bin)}}
	return c
}

func (c *testChannel) fetcher(t *testing.T) *Fetcher {
	return &Fetcher{IndexURL: c.srv.URL + "/stable.json", BaseURL: c.srv.URL + "/dl", CacheDir: t.TempDir(),
		HTTP: c.srv.Client(), Keys: []ed25519.PublicKey{c.pub}}
}

func TestBinaryVerifiesAndCaches(t *testing.T) {
	c := newChannel(t)
	f := c.fetcher(t)
	p, err := f.Binary(context.Background(), "v1.2.3", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(c.bin) {
		t.Errorf("cached content %q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %o", fi.Mode().Perm())
	}
	before := c.hits["/dl/v1.2.3/keelbox-linux-arm64"]
	if p2, err := f.Binary(context.Background(), "1.2.3", "arm64"); err != nil || p2 != p {
		t.Fatalf("cache miss: %s %v", p2, err)
	}
	if c.hits["/dl/v1.2.3/keelbox-linux-arm64"] != before {
		t.Error("cached binary downloaded again")
	}
}

// Each case breaks one link in the chain; every one must be refused and
// leave nothing in the cache.
func TestBinaryRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		breakIt func(*testChannel, *Fetcher)
		want    string
	}{
		"tampered binary": {func(c *testChannel, _ *Fetcher) {
			c.bin = []byte("tampered")
		}, "does not match SHA256SUMS"},
		"release host and index both tampered, unsigned sums": {func(c *testChannel, _ *Fetcher) {
			c.bin = []byte("evil")
			c.sums = []byte(sumOf(c.bin) + "  keelbox-linux-arm64\n")
			c.index.Checksums["v1.2.3/linux-arm64"] = sumOf(c.bin)
		}, "not signed by a keelbox release key"},
		"another version's signed SHA256SUMS": {func(c *testChannel, _ *Fetcher) {
			c.sig = ed25519.Sign(c.priv, SignedMessage("v1.2.2", c.sums))
		}, "not signed by a keelbox release key"},
		"no signature": {func(c *testChannel, _ *Fetcher) {
			c.sig = nil
		}, "no release key signature"},
		"signed by an untrusted key": {func(c *testChannel, f *Fetcher) {
			other, _ := newKey(t)
			f.Keys = []ed25519.PublicKey{other}
		}, "not signed by a keelbox release key"},
		"index disagrees": {func(c *testChannel, _ *Fetcher) {
			c.index.Checksums["v1.2.3/linux-arm64"] = strings.Repeat("0", 64)
		}, "disagree"},
		"index lists no checksum": {func(c *testChannel, _ *Fetcher) {
			c.index.Checksums = nil
		}, "lists no checksum"},
		"index names another release host": {func(c *testChannel, f *Fetcher) {
			c.index.Releases = c.srv.URL + "/dl"
			f.BaseURL = ""
		}, "not one keelbox downloads from"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newChannel(t)
			f := c.fetcher(t)
			tc.breakIt(c, f)
			_, err := f.Binary(context.Background(), "1.2.3", "arm64")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal containing %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(f.CachedPath("1.2.3", "arm64")); err == nil {
				t.Error("unverified binary cached")
			}
		})
	}
}

func TestRedirectToPlainHTTPRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tls.Close()
	client := tls.Client()
	client.CheckRedirect = noDowngrade
	f := &Fetcher{HTTP: client}
	if _, err := f.get(context.Background(), tls.URL, 10); err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Errorf("HTTPS to HTTP redirect followed: %v", err)
	}
}

func TestIndexChecksumHistory(t *testing.T) {
	idx := Index{Version: "v0.1.1", Binaries: map[string]Binary{"linux-amd64": {SHA256: "AB"}},
		Checksums: map[string]string{"v0.1.0/linux-amd64": "cd"}}
	if s, ok := idx.Checksum("v0.1.0", "linux-amd64"); !ok || s != "cd" {
		t.Errorf("history: %q %v", s, ok)
	}
	if s, ok := idx.Checksum("v0.1.1", "linux-amd64"); !ok || s != "ab" {
		t.Errorf("current from binaries: %q %v", s, ok)
	}
	if _, ok := idx.Checksum("v0.1.0", "darwin-arm64"); ok {
		t.Error("unknown platform found")
	}
}

func TestBinaryErrors(t *testing.T) {
	c := newChannel(t)
	f := c.fetcher(t)
	for _, v := range []string{"dev", "", "v1.2.3-4-gabc-dirty"} {
		if _, err := f.Binary(context.Background(), v, "amd64"); !errors.Is(err, ErrDevBuild) {
			t.Errorf("%q: %v", v, err)
		}
	}
	c.index.Checksums["v9.9.9/linux-arm64"] = sumOf(c.bin)
	if _, err := f.Binary(context.Background(), "9.9.9", "arm64"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing release: %v", err)
	}
}

func TestIndexValidation(t *testing.T) {
	for body, want := range map[string]string{
		`{"version":"../../etc","releases":"https://github.com/keelbox/releases/releases/download"}`: "invalid version",
		`{"version":"1.0.0","releases":"https://github.com/keelbox/releases/releases/download"}`:     "invalid version",
		`{"version":"v1.0.0","releases":"http://plain"}`:                                             "not one keelbox downloads from",
		`{"version":"v1.0.0","releases":"https://github.com/someone/else/releases/download"}`:        "not one keelbox downloads from",
		`not json`: "invalid character",
	} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		f := &Fetcher{IndexURL: srv.URL, HTTP: srv.Client()}
		if _, err := f.Index(context.Background()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", body, err)
		}
		srv.Close()
	}
}

func TestCompareVersions(t *testing.T) {
	order := []string{"v0.1.0-rc.1", "v0.1.0-rc.2", "v0.1.0-rc.10", "v0.1.0", "v0.1.1", "v0.2.0", "v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-beta", "v1.0.0", "v10.0.0"}
	for i := range order {
		for j := range order {
			want := cmpInt(i, j)
			if got := CompareVersions(order[i], order[j]); got != want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{"v0.1.0": true, "0.1.0-rc.1": true, "dev": false, "v0.1.0-3-gabcdef": false, "v0.1.0-3-gabcdef-dirty": false, "": false} {
		if IsRelease(v) != want {
			t.Errorf("IsRelease(%q) != %v", v, want)
		}
	}
}

// The installer carries the same release keys as the binary.
func TestInstallerTrustsTheEmbeddedKeys(t *testing.T) {
	script, err := os.ReadFile("../../get/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	keys := TrustedKeys()
	if len(keys) == 0 {
		t.Fatal("no embedded release keys")
	}
	for i, pemText := range TrustedKeyPEMs() {
		if !strings.Contains(string(script), strings.TrimSpace(pemText)) {
			t.Errorf("install.sh lacks release key PEM %d", i)
		}
	}
	for _, k := range keys {
		if !strings.Contains(string(script), SSHPublicKey(k)) {
			t.Errorf("install.sh lacks %s", SSHPublicKey(k))
		}
	}
}

// SignSSH writes what ssh-keygen -Y verify accepts, the check install.sh
// falls back to where openssl lacks Ed25519.
func TestSSHSignatureVerifiesWithSSHKeygen(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	pub, priv := newKey(t)
	dir := t.TempDir()
	msg := []byte("abc  keelbox-linux-amd64\n")
	sig := filepath.Join(dir, "SHA256SUMS.sshsig")
	allowed := filepath.Join(dir, "allowed")
	_ = os.WriteFile(sig, SignSSH(priv, msg), 0o600)
	_ = os.WriteFile(allowed, []byte(`keelbox-release namespaces="keelbox-release" `+SSHPublicKey(pub)+"\n"), 0o600)
	verify := func(data []byte) error {
		cmd := exec.Command("ssh-keygen", "-Y", "verify", "-f", allowed, "-I", "keelbox-release", "-n", SSHNamespace, "-s", sig)
		cmd.Stdin = strings.NewReader(string(data))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return errors.New(string(out))
		}
		return nil
	}
	if err := verify(msg); err != nil {
		t.Fatalf("ssh-keygen rejected the signature: %v", err)
	}
	if err := verify(append(msg, 'x')); err == nil {
		t.Error("ssh-keygen accepted a changed file")
	}
}

func TestVerifySums(t *testing.T) {
	pub, priv := newKey(t)
	old, oldPriv := newKey(t)
	sums := []byte("x  keelbox-linux-amd64\n")
	if err := VerifySums("v1.0.0", sums, ed25519.Sign(priv, SignedMessage("v1.0.0", sums)), []ed25519.PublicKey{old, pub}); err != nil {
		t.Errorf("signature by the second trusted key: %v", err)
	}
	if err := VerifySums("v1.0.0", sums, ed25519.Sign(oldPriv, SignedMessage("v1.0.0", sums)), []ed25519.PublicKey{pub}); !errors.Is(err, ErrBadSignature) {
		t.Errorf("retired key accepted: %v", err)
	}
	if err := VerifySums("v1.0.0", sums, ed25519.Sign(priv, sums), []ed25519.PublicKey{pub}); !errors.Is(err, ErrBadSignature) {
		t.Errorf("signature without the version line: %v", err)
	}
	if err := VerifySums("v1.0.0", sums, []byte("short"), []ed25519.PublicKey{pub}); !errors.Is(err, ErrBadSignature) {
		t.Errorf("short signature: %v", err)
	}
}

// TestNewFetcherEnvironment: the binary takes another HTTPS index (a mirror)
// but no replacement release host, and never a plain HTTP index.
func TestNewFetcherEnvironment(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, tc := range []struct {
		name, index, release, wantErr string
	}{
		{"defaults", "", "", ""},
		{"https mirror index", "https://mirror.example/stable.json", "", ""},
		{"http index", "http://127.0.0.1:1/stable.json", "", "not an https:// URL"},
		{"release host override", "", "https://mirror.example/dl", "KEELBOX_RELEASE_URL is for keelbox's installer tests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KEELBOX_INDEX_URL", tc.index)
			t.Setenv("KEELBOX_RELEASE_URL", tc.release)
			f, err := NewFetcher()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.BaseURL != "" {
				t.Errorf("BaseURL = %q, want none", f.BaseURL)
			}
			if want := tc.index; want != "" && f.IndexURL != want || want == "" && f.IndexURL != IndexURL {
				t.Errorf("IndexURL = %q", f.IndexURL)
			}
		})
	}
}
