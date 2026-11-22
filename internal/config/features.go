package config

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Previews publishes the agent's loopback dev servers on the tailnet, over
// HTTPS with the box's Tailscale certificate (docs/adr/0019-previews.md).
type Previews struct {
	// Enabled defaults to true when Tailscale is enabled: previews are
	// served only on the tailnet.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Ports is the range of HTTPS ports previews are published on, on the
	// box's tailnet addresses only. Default 8401-8499.
	Ports string `yaml:"ports,omitempty"`
}

// Backup sends restic snapshots to S3-compatible storage
// (docs/adr/0020-backups.md). Configured when repository is set.
type Backup struct {
	// Repository is a restic S3 repository: s3:https://host/bucket[/prefix].
	Repository string `yaml:"repository,omitempty"`
	// CredentialsFile is an AWS-style shared credentials file holding the
	// bucket's access key, mode 600. Read by path only.
	CredentialsFile string `yaml:"credentials_file,omitempty"`
	// PasswordFile holds the restic repository password, mode 600. Keep a
	// copy off the box: without it the backups cannot be read.
	PasswordFile string `yaml:"password_file,omitempty"`
	// Schedule is a systemd OnCalendar expression. Default daily.
	Schedule string `yaml:"schedule,omitempty"`
	Keep     Keep   `yaml:"keep"`
	// Paths to back up. Default: the agent's home.
	Paths []string `yaml:"paths,omitempty"`
	// Exclude replaces the default exclusions (caches, node_modules, the
	// agent's container storage) when set.
	Exclude []string `yaml:"exclude,omitempty"`
	// StaleAfter is how old the last successful backup may be before
	// doctor fails. Default 48h.
	StaleAfter string `yaml:"stale_after,omitempty"`
}

// Keep is the retention policy, as restic forget --keep-daily and so on.
type Keep struct {
	Daily   *int `yaml:"daily,omitempty"`
	Weekly  *int `yaml:"weekly,omitempty"`
	Monthly *int `yaml:"monthly,omitempty"`
}

// DefaultBackupExclude is what backups leave out unless backup.exclude is
// set: caches and dependencies that are rebuilt, the agent CLIs' own
// installs (apply reinstalls a missing CLI), and the agent's rootless
// container storage, which holds files the agent itself cannot read
// (docs/adr/0024-backups-leave-out-tools.md).
var DefaultBackupExclude = []string{
	"node_modules", ".cache", ".npm", ".pnpm-store", ".local/share/pnpm",
	".local/share/mise", ".local/share/claude/versions", ".codex/packages",
	".local/share/docker", ".local/share/containers",
	".rustup", ".cargo/registry", "go/pkg/mod", "keelbox-restore",
}

// DefaultPreviewPorts is previews.ports unless set.
const DefaultPreviewPorts = "8401-8499"

func (c *Config) applyFeatureDefaults() {
	if c.Previews.Ports == "" {
		c.Previews.Ports = DefaultPreviewPorts
	}
	if c.Backup.Repository != "" {
		if c.Backup.Schedule == "" {
			c.Backup.Schedule = "daily"
		}
		if c.Backup.StaleAfter == "" {
			c.Backup.StaleAfter = "48h"
		}
	}
}

// PreviewsEnabled reports whether previews are installed.
func (c *Config) PreviewsEnabled() bool { return On(c.Previews.Enabled, c.Tailscale()) }

// PreviewPorts returns the first and last HTTPS port for previews.
func (c *Config) PreviewPorts() (int, int) {
	a, b, _ := parseRange(c.Previews.Ports)
	return a, b
}

// PreviewSkipped are the ports in previews.ports that host.firewall.allow
// keeps open to the LAN for TCP (a service preset's included, such as
// UniFi's 8443). Previews never use them: a preview there would be open
// off the tailnet too, and a service of the box's may be listening there.
func (c *Config) PreviewSkipped() []int {
	lo, hi := c.PreviewPorts()
	var out []int
	rules := c.AllowRules()
	for p := lo; p > 0 && p <= hi; p++ {
		for _, a := range rules {
			if a.Protocol() == "tcp" && a.Port.Contains(p) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// PreviewSkipNote says which preview ports are skipped and why, or "".
func (c *Config) PreviewSkipNote() string {
	skip := c.PreviewSkipped()
	if len(skip) == 0 {
		return ""
	}
	var ps, names []string
	for _, p := range skip {
		ps = append(ps, strconv.Itoa(p))
		if a, ok := c.Allowed(p, "tcp"); ok {
			n := a.Label()
			if a.Name == "" && a.Service == "" {
				n = a.Port.String()
			}
			if !containsString(names, n) {
				names = append(names, n)
			}
		}
	}
	return fmt.Sprintf("previews skip %s, kept open to the LAN by host.firewall.allow (%s)", strings.Join(ps, ", "), strings.Join(names, ", "))
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// BackupEnabled reports whether backups are configured.
func (c *Config) BackupEnabled() bool { return c.Backup.Repository != "" }

// BackupExclude returns the effective exclusions.
func (c *Config) BackupExclude() []string {
	if c.Backup.Exclude != nil {
		return c.Backup.Exclude
	}
	return DefaultBackupExclude
}

// KeepPolicy returns daily, weekly and monthly retention with defaults.
func (c *Config) KeepPolicy() (daily, weekly, monthly int) {
	get := func(p *int, def int) int {
		if p == nil {
			return def
		}
		return *p
	}
	k := c.Backup.Keep
	return get(k.Daily, 7), get(k.Weekly, 4), get(k.Monthly, 6)
}

// BackupStaleAfter parses backup.stale_after.
func (c *Config) BackupStaleAfter() time.Duration {
	d, err := time.ParseDuration(c.Backup.StaleAfter)
	if err != nil {
		return 48 * time.Hour
	}
	return d
}

func parseRange(s string) (int, int, error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("%q: use a range like 8401-8499", s)
	}
	lo, err1 := strconv.Atoi(a)
	hi, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("%q: use a range like 8401-8499", s)
	}
	return lo, hi, nil
}

var (
	// s3:https://host[:port]/bucket[/prefix]. No user info: credentials
	// travel in the credentials file, never in the URL.
	repoRe     = regexp.MustCompile(`^s3:https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?/[A-Za-z0-9._-]+(/[A-Za-z0-9._/-]*)?$`)
	scheduleRe = regexp.MustCompile(`^[A-Za-z0-9*:,./~ -]{1,64}$`)
	excludeRe  = regexp.MustCompile(`^[^-\x00-\x1f\x7f][^\x00-\x1f\x7f]{0,255}$`)
)

func (c *Config) validateFeatures(add func(string, ...any)) {
	p := c.Previews
	if lo, hi, err := parseRange(p.Ports); err != nil {
		add("previews.ports: %v", err)
	} else if lo < 1024 || hi > 65535 || lo > hi || hi-lo >= 1000 {
		add("previews.ports: %q must be a range of at most 1000 ports between 1024 and 65535", p.Ports)
	} else if len(c.PreviewSkipped()) == hi-lo+1 && c.PreviewsEnabled() {
		add("previews.ports: host.firewall.allow keeps every port of %s open to the LAN, so none is left for previews; choose another range", p.Ports)
	}
	if p.Enabled != nil && *p.Enabled && !c.Tailscale() {
		add("previews.enabled: previews are served only on the tailnet and need access.tailscale enabled")
	}

	b := c.Backup
	if b.Repository == "" {
		if b.CredentialsFile != "" || b.PasswordFile != "" || b.Schedule != "" || b.Paths != nil || b.Exclude != nil || b.StaleAfter != "" ||
			b.Keep.Daily != nil || b.Keep.Weekly != nil || b.Keep.Monthly != nil {
			add("backup.repository: required when any backup key is set")
		}
		return
	}
	if !repoRe.MatchString(b.Repository) {
		add("backup.repository: %q must be a restic S3 repository such as s3:https://s3.example.com/bucket/box, without credentials in the URL", b.Repository)
	}
	if b.CredentialsFile == "" {
		add("backup.credentials_file: required, an S3 credentials file (mode 600)")
	}
	if b.PasswordFile == "" {
		add("backup.password_file: required, the restic repository password file (mode 600)")
	}
	if b.CredentialsFile != "" && b.CredentialsFile == b.PasswordFile {
		add("backup.password_file: must differ from backup.credentials_file")
	}
	if !scheduleRe.MatchString(b.Schedule) {
		add("backup.schedule: %q is not a systemd calendar expression such as daily or *-*-* 03:00", b.Schedule)
	}
	d, w, m := c.KeepPolicy()
	if d < 0 || w < 0 || m < 0 {
		add("backup.keep: counts cannot be negative")
	} else if d+w+m == 0 {
		add("backup.keep: keeps no snapshot; set at least one of daily, weekly, monthly")
	}
	seen := map[string]bool{}
	for _, bp := range b.Paths {
		switch {
		case !path.IsAbs(bp) || path.Clean(bp) != bp:
			add("backup.paths: %q must be a clean absolute path", bp)
		case bp == "/":
			add("backup.paths: \"/\" is the whole machine; list the directories to keep")
		case seen[bp]:
			add("backup.paths: %q listed twice", bp)
		case strings.ContainsAny(bp, ",\n\t"):
			add("backup.paths: %q contains a comma or a control character", bp)
		}
		seen[bp] = true
	}
	for _, x := range b.Exclude {
		if !excludeRe.MatchString(x) {
			add("backup.exclude: %q must not start with - or contain control characters", x)
		}
	}
	if dur, err := time.ParseDuration(b.StaleAfter); err != nil || dur < time.Hour {
		add("backup.stale_after: %q must be a duration of at least 1h, such as 48h", b.StaleAfter)
	}
}
