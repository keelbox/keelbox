// Package release fetches keelbox's own binaries from the release channel:
// the linux binary a laptop uploads to a box, and the binary self-update
// installs. Both go through Fetch, the one verification path: SHA256SUMS
// must carry a signature by an embedded release key, and the binary's
// checksum must agree with SHA256SUMS from an allowlisted release host and
// with the channel index on get.keelbox.dev. See
// docs/adr/0006-laptop-cli-and-box-binary.md and
// docs/adr/0016-release-signatures.md.
package release

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/testbuild"
)

// IndexURL is the stable channel index. It names the current version, the
// release host and every published release's checksums.
const IndexURL = "https://get.keelbox.dev/stable.json"

// ReleaseHosts are the only bases keelbox downloads its releases from. The
// index picks one (keelbox/releases or the source repository's releases)
// but cannot name any other.
// Only tests replace them (Fetcher.BaseURL); the binary honours no
// environment override of the release host.
var ReleaseHosts = []string{
	"https://github.com/keelbox/releases/releases/download",
	"https://github.com/keelbox/keelbox/releases/download",
}

// ErrDevBuild is returned for a build with no matching release.
var ErrDevBuild = errors.New("a development build has no release to download")

// maxBinary bounds a download; keelbox is a few megabytes.
const maxBinary = 64 << 20

var validVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// IsRelease reports whether v (with or without a leading v) names a release,
// rather than a development build ("dev", git describe, -dirty).
func IsRelease(v string) bool {
	return validVersion.MatchString(strings.TrimPrefix(v, "v")) && !strings.Contains(v, "-dirty")
}

// Index is a channel's pointer to its current release.
type Index struct {
	Channel  string            `json:"channel"`
	Version  string            `json:"version"`  // vX.Y.Z
	Releases string            `json:"releases"` // assets at <releases>/<version>/<asset>
	Binaries map[string]Binary `json:"binaries"` // the current version's, key "<os>-<arch>"
	// Checksums lists every published release's binaries, key
	// "vX.Y.Z/<os>-<arch>", so a laptop on an older release can still
	// check the box binary of its own version against the index.
	Checksums map[string]string `json:"checksums"`
	// Security lists security problems and the releases that fix them,
	// from get/security.json. Clients show the valid ones (Notices).
	Security []SecurityNotice `json:"security,omitempty"`
}

// Binary is one release binary as the index lists it.
type Binary struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Checksum returns the index's SHA-256 for a release binary.
func (idx *Index) Checksum(version, platform string) (string, bool) {
	if s, ok := idx.Checksums[version+"/"+platform]; ok {
		return strings.ToLower(s), true
	}
	if b, ok := idx.Binaries[platform]; ok && version == idx.Version && b.SHA256 != "" {
		return strings.ToLower(b.SHA256), true
	}
	return "", false
}

// Fetcher downloads and caches release binaries.
type Fetcher struct {
	IndexURL string // default IndexURL; KEELBOX_INDEX_URL, which must be HTTPS
	BaseURL  string // replaces the index's release host; tests only
	CacheDir string // default <user cache dir>/keelbox
	HTTP     *http.Client
	Keys     []ed25519.PublicKey // trusted release keys; default TrustedKeys()
}

// Platform names a release platform, e.g. "darwin-arm64".
func Platform(goos, goarch string) string { return goos + "-" + goarch }

// PlatformAsset is the release asset for a platform.
func PlatformAsset(platform string) string { return "keelbox-" + platform }

// AssetName is the release asset for a linux architecture.
func AssetName(arch string) string { return PlatformAsset(Platform("linux", arch)) }

// NewFetcher returns a Fetcher for the stable channel. KEELBOX_INDEX_URL
// may name another HTTPS index (a mirror); the release host it names must
// still be one of ReleaseHosts. KEELBOX_RELEASE_URL, which the installer
// honours only with --test, is refused here rather than ignored.
func NewFetcher() (*Fetcher, error) {
	if os.Getenv("KEELBOX_RELEASE_URL") != "" {
		return nil, errors.New("KEELBOX_RELEASE_URL is for keelbox's installer tests (install.sh --test); keelbox downloads only from its release hosts, so unset it")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locate the cache directory: %w", err)
	}
	idx := os.Getenv("KEELBOX_INDEX_URL")
	if idx == "" {
		idx = IndexURL
	}
	if !strings.HasPrefix(idx, "https://") {
		return nil, fmt.Errorf("KEELBOX_INDEX_URL %q is not an https:// URL", idx)
	}
	return &Fetcher{
		IndexURL: idx,
		CacheDir: filepath.Join(cache, "keelbox"),
		HTTP:     &http.Client{Timeout: 5 * time.Minute, CheckRedirect: noDowngrade},
	}, nil
}

// noDowngrade refuses a redirect from HTTPS to anything else.
func noDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from HTTPS to %s", req.URL.Redacted())
	}
	return nil
}

func (f *Fetcher) keys() []ed25519.PublicKey {
	if f.Keys != nil {
		return f.Keys
	}
	return append(TrustedKeys(), testKeys()...)
}

// testKeys are a test build's throwaway release keys
// (KEELBOX_TEST_RELEASE_KEYS); none in any other build.
func testKeys() []ed25519.PublicKey {
	p := testbuild.Getenv(testbuild.ReleaseKeysEnv)
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []ed25519.PublicKey
	for {
		var b *pem.Block
		if b, data = pem.Decode(data); b == nil {
			return out
		}
		if k, err := ParsePublicKey(pem.EncodeToMemory(b)); err == nil {
			out = append(out, k)
		}
	}
}

// Index fetches and validates the channel index.
func (f *Fetcher) Index(ctx context.Context) (*Index, error) {
	body, err := f.get(ctx, f.IndexURL, 4<<20)
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("channel index %s: %w", f.IndexURL, err)
	}
	if !strings.HasPrefix(idx.Version, "v") || !validVersion.MatchString(idx.Version[1:]) {
		return nil, fmt.Errorf("channel index %s: invalid version %q", f.IndexURL, idx.Version)
	}
	if f.BaseURL == "" && !slices.Contains(ReleaseHosts, strings.TrimRight(idx.Releases, "/")) {
		return nil, fmt.Errorf("channel index %s: release host %q is not one keelbox downloads from (%s); refusing",
			f.IndexURL, idx.Releases, strings.Join(ReleaseHosts, ", "))
	}
	return &idx, nil
}

// CachedPath is where a version's linux binary is kept once verified.
func (f *Fetcher) CachedPath(version, arch string) string {
	return filepath.Join(f.CacheDir, strings.TrimPrefix(version, "v"), AssetName(arch))
}

// Binary returns a verified linux binary for version and arch, from the
// cache or downloaded from the release channel.
func (f *Fetcher) Binary(ctx context.Context, version, arch string) (string, error) {
	v := strings.TrimPrefix(version, "v")
	if !IsRelease(v) {
		return "", ErrDevBuild
	}
	dest := f.CachedPath(v, arch)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	idx, err := f.Index(ctx)
	if err != nil {
		return "", err
	}
	if err := f.Fetch(ctx, idx, "v"+v, Platform("linux", arch), dest); err != nil {
		return "", err
	}
	return dest, nil
}

// Fetch writes a release binary to dest, mode 0755, only after all of:
// SHA256SUMS from the release host is signed by a trusted release key; the
// binary's SHA-256 matches SHA256SUMS; and the index (from get.keelbox.dev)
// lists the same checksum. A compromise of either host alone, or both
// without the signing key, is refused.
func (f *Fetcher) Fetch(ctx context.Context, idx *Index, version, platform, dest string) error {
	asset := PlatformAsset(platform)
	indexSum, ok := idx.Checksum(version, platform)
	if !ok {
		return fmt.Errorf("the channel index lists no checksum for keelbox %s %s; refusing to download it", version, platform)
	}
	dir := strings.TrimRight(f.BaseURL, "/")
	if dir == "" {
		dir = strings.TrimRight(idx.Releases, "/")
	}
	dir += "/" + version + "/"
	sums, err := f.signedSums(ctx, dir, version)
	if err != nil {
		return err
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return fmt.Errorf("release %s: %w", version, err)
	}
	if want != indexSum {
		return fmt.Errorf("release %s: SHA256SUMS and the channel index disagree on %s; refusing to download it", version, asset)
	}
	bin, err := f.get(ctx, dir+asset, maxBinary)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("release %s %s: checksum %s does not match SHA256SUMS (%s); refusing to use it", version, asset, got, want)
	}
	if err := writeAtomic(dest, bin); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// signedSums downloads a release's SHA256SUMS and its signature and returns
// the file only if a trusted release key signed it.
func (f *Fetcher) signedSums(ctx context.Context, dir, version string) ([]byte, error) {
	sums, err := f.get(ctx, dir+"SHA256SUMS", 1<<20)
	if err != nil {
		return nil, err
	}
	sig, err := f.get(ctx, dir+"SHA256SUMS.sig", 1<<10)
	if err != nil {
		return nil, fmt.Errorf("release %s has no release key signature: %w", version, err)
	}
	if err := VerifySums(version, sums, sig, f.keys()); err != nil {
		return nil, fmt.Errorf("release %s: %w; refusing it", version, err)
	}
	return sums, nil
}

// CompareVersions orders two release versions (vX.Y.Z[-pre]) by semantic
// versioning precedence: -1, 0 or 1. A prerelease sorts before its release.
func CompareVersions(a, b string) int {
	pa, qa := splitVersion(a)
	pb, qb := splitVersion(b)
	for i := range 3 {
		if c := cmpInt(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	switch {
	case qa == "" && qb == "":
		return 0
	case qa == "":
		return 1
	case qb == "":
		return -1
	}
	xa, xb := strings.Split(qa, "."), strings.Split(qb, ".")
	for i := 0; i < len(xa) && i < len(xb); i++ {
		na, ea := strconv.Atoi(xa[i])
		nb, eb := strconv.Atoi(xb[i])
		var c int
		switch {
		case ea == nil && eb == nil:
			c = cmpInt(na, nb)
		case ea == nil:
			c = -1 // numeric identifiers sort first
		case eb == nil:
			c = 1
		default:
			c = strings.Compare(xa[i], xb[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmpInt(len(xa), len(xb))
}

func splitVersion(v string) ([3]int, string) {
	core, pre, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	var n [3]int
	for i, p := range strings.SplitN(core, ".", 3) {
		n[i], _ = strconv.Atoi(p)
	}
	return n, pre
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (f *Fetcher) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent())
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download %s: larger than %d bytes", url, limit)
	}
	return data, nil
}

// checksumFor finds an asset's SHA-256 in sha256sum output.
func checksumFor(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS lists no %s", asset)
}

func writeAtomic(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}
