// Package manifest holds the release manifest: the versions of every tool
// keelbox installs, tested together and shipped inside the binary.
package manifest

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed stable.yaml
var stable []byte

// Manifest pins tool versions for one release channel.
type Manifest struct {
	Manifest  int    `yaml:"manifest"`
	Channel   string `yaml:"channel"`
	Release   string `yaml:"release"`
	OS        OS     `yaml:"os"`
	Docker    Docker `yaml:"docker"`
	Tailscale Key    `yaml:"tailscale"`
	GitHubCLI Key    `yaml:"github_cli"`
	Mise      Mise   `yaml:"mise"`
	Node      string `yaml:"node"`
	Pnpm      string `yaml:"pnpm"`
	Tmux      Tmux   `yaml:"tmux"`
	Caddy     Caddy  `yaml:"caddy"`
	Restic    Restic `yaml:"restic"`
}

// Caddy pins the Caddy release used for previews and its tarball checksums
// (SHA-512, as Caddy publishes them).
type Caddy struct {
	Version string            `yaml:"version"`
	SHA512  map[string]string `yaml:"sha512"`
}

// Restic pins the restic release used for backups and its binary checksums.
type Restic struct {
	Version string            `yaml:"version"`
	SHA256  map[string]string `yaml:"sha256"`
}

// OS lists supported platforms.
type OS struct {
	Ubuntu []string `yaml:"ubuntu"`
	Arch   []string `yaml:"arch"`
}

// Docker pins the Engine major series and the apt signing key.
type Docker struct {
	Series         string `yaml:"series"`
	GPGFingerprint string `yaml:"gpg_fingerprint"`
}

// Key is an apt signing key fingerprint.
type Key struct {
	GPGFingerprint string `yaml:"gpg_fingerprint"`
}

// Mise pins the mise release and its tarball checksums per architecture.
type Mise struct {
	Version string            `yaml:"version"`
	SHA256  map[string]string `yaml:"sha256"`
}

// Tmux pins plugin commits.
type Tmux struct {
	Resurrect string `yaml:"resurrect"`
	Continuum string `yaml:"continuum"`
}

var (
	semverRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha512Re = regexp.MustCompile(`^[0-9a-f]{128}$`)
	fprRe    = regexp.MustCompile(`^[0-9A-F]{40}$`)
)

// Parse decodes and validates a manifest.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, m.Validate()
}

// Validate checks every pin is well formed.
func (m *Manifest) Validate() error {
	var errs []string
	check := func(ok bool, what string) {
		if !ok {
			errs = append(errs, what)
		}
	}
	check(m.Manifest == 1, "manifest: must be 1")
	check(m.Channel != "", "channel: required")
	check(m.Release != "", "release: required")
	check(len(m.OS.Ubuntu) > 0, "os.ubuntu: required")
	check(regexp.MustCompile(`^[0-9]+$`).MatchString(m.Docker.Series), "docker.series: must be a major version")
	check(fprRe.MatchString(m.Docker.GPGFingerprint), "docker.gpg_fingerprint: 40 upper-case hex characters")
	check(fprRe.MatchString(m.Tailscale.GPGFingerprint), "tailscale.gpg_fingerprint: 40 upper-case hex characters")
	check(fprRe.MatchString(m.GitHubCLI.GPGFingerprint), "github_cli.gpg_fingerprint: 40 upper-case hex characters")
	check(regexp.MustCompile(`^[0-9]{4}\.[0-9]+\.[0-9]+$`).MatchString(m.Mise.Version), "mise.version: CalVer like 2026.11.2")
	for _, arch := range m.OS.Arch {
		check(shaRe.MatchString(m.Mise.SHA256[arch]), "mise.sha256."+arch+": 64 hex characters")
	}
	check(semverRe.MatchString(m.Caddy.Version), "caddy.version: exact x.y.z")
	check(semverRe.MatchString(m.Restic.Version), "restic.version: exact x.y.z")
	for _, arch := range m.OS.Arch {
		check(sha512Re.MatchString(m.Caddy.SHA512[arch]), "caddy.sha512."+arch+": 128 hex characters")
		check(shaRe.MatchString(m.Restic.SHA256[arch]), "restic.sha256."+arch+": 64 hex characters")
	}
	check(semverRe.MatchString(m.Node), "node: exact x.y.z")
	check(semverRe.MatchString(m.Pnpm), "pnpm: exact x.y.z")
	check(commitRe.MatchString(m.Tmux.Resurrect), "tmux.resurrect: full commit SHA")
	check(commitRe.MatchString(m.Tmux.Continuum), "tmux.continuum: full commit SHA")
	if len(errs) > 0 {
		return fmt.Errorf("manifest %s invalid: %v", m.Release, errs)
	}
	return nil
}

// Supports reports whether an Ubuntu version and dpkg architecture are
// in the tested matrix.
func (m *Manifest) Supports(ubuntu, arch string) bool {
	okOS, okArch := false, false
	for _, v := range m.OS.Ubuntu {
		okOS = okOS || v == ubuntu
	}
	for _, a := range m.OS.Arch {
		okArch = okArch || a == arch
	}
	return okOS && okArch
}

// MiseArch maps a dpkg architecture to mise's release asset name.
func MiseArch(dpkgArch string) string {
	if dpkgArch == "amd64" {
		return "x64"
	}
	return dpkgArch
}

// MiseURL is the release tarball for an architecture.
func (m *Manifest) MiseURL(dpkgArch string) string {
	return fmt.Sprintf("https://github.com/jdx/mise/releases/download/v%s/mise-v%s-linux-%s.tar.gz",
		m.Mise.Version, m.Mise.Version, MiseArch(dpkgArch))
}

// CaddyURL is the Caddy release tarball for an architecture.
func (m *Manifest) CaddyURL(dpkgArch string) string {
	return fmt.Sprintf("https://github.com/caddyserver/caddy/releases/download/v%s/caddy_%s_linux_%s.tar.gz",
		m.Caddy.Version, m.Caddy.Version, dpkgArch)
}

// ResticURL is the restic release binary (bzip2) for an architecture.
func (m *Manifest) ResticURL(dpkgArch string) string {
	return fmt.Sprintf("https://github.com/restic/restic/releases/download/v%s/restic_%s_linux_%s.bz2",
		m.Restic.Version, m.Restic.Version, dpkgArch)
}

// CompareReleases orders release ids such as "2026.11.0" field by field,
// numerically where both fields are numbers. It returns -1, 0 or 1.
func CompareReleases(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil && xn != yn:
			if xn < yn {
				return -1
			}
			return 1
		case (xerr != nil || yerr != nil) && x != y:
			return strings.Compare(x, y)
		}
	}
	return 0
}

// ForChannel returns the embedded manifest for a channel.
func ForChannel(channel string) (*Manifest, error) {
	if channel != "stable" {
		return nil, fmt.Errorf("no manifest for channel %q", channel)
	}
	return Parse(stable)
}
