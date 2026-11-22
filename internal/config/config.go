// Package config defines the box configuration file: one YAML document per
// box, validated strictly so typos fail loudly instead of being ignored.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Version is the only config schema version this build understands.
const Version = 1

// Config is one box.
type Config struct {
	Version  int      `yaml:"version"`
	Box      string   `yaml:"box"`
	Channel  string   `yaml:"channel,omitempty"`
	Target   Target   `yaml:"target"`
	Host     Host     `yaml:"host"`
	Users    Users    `yaml:"users"`
	Runtime  Runtime  `yaml:"runtime"`
	Agents   []string `yaml:"agents"`
	Sessions Sessions `yaml:"sessions"`
	Access   Access   `yaml:"access"`
	Primer   Primer   `yaml:"primer"`
	Previews Previews `yaml:"previews"`
	Backup   Backup   `yaml:"backup"`
	// Sections with omitempty: a config that sets none of their keys stays
	// readable by an older keelbox, which a rollback runs.
	// AgentUpdates is only read to refuse it with the fix.
	AgentUpdates *removedAgentUpdates `yaml:"agent_updates,omitempty"`
	Notify       Notify               `yaml:"notify,omitempty"`
	Doctor       Doctor               `yaml:"doctor,omitempty"`
	Reports      Reports              `yaml:"reports,omitempty"`
}

// Target says where the box runs and how keelbox reaches it.
type Target struct {
	Provider string  `yaml:"provider"`
	SSH      SSH     `yaml:"ssh,omitempty"`
	Hetzner  Hetzner `yaml:"hetzner,omitempty"`
	AWS      AWS     `yaml:"aws,omitempty"`
	// DigitalOcean is the block for provider digitalocean.
	DigitalOcean DigitalOcean `yaml:"digitalocean,omitempty"`
	// GCP is the block for provider gcp.
	GCP GCP `yaml:"gcp,omitempty"`
}

// SSH is how keelbox connects to the box. For the cloud providers (hetzner, aws,
// digitalocean, gcp), Host is
// optional: keelbox create always uses the address from the API, and later
// commands use Host when set (for example the box's tailnet name once public
// SSH is closed).
type SSH struct {
	Host         string `yaml:"host,omitempty"`
	Port         int    `yaml:"port,omitempty"`
	User         string `yaml:"user,omitempty"`
	IdentityFile string `yaml:"identity_file,omitempty"`
	KnownHosts   string `yaml:"known_hosts,omitempty"`
	// HostKey pins the box's SSH host key by its SHA256 fingerprint for the
	// first connection, instead of confirming it at a prompt.
	HostKey string `yaml:"host_key,omitempty"`
	// TailnetHost is where keelbox reaches the box over the tailnet, as
	// host or host:port. Default: the box's Tailscale IPv4 address, as the
	// box reports it and as this laptop last reached it
	// (docs/adr/0021-close-public-ssh.md).
	TailnetHost string `yaml:"tailnet_host,omitempty"`
}

// Hetzner configures server creation through the Hetzner Cloud API.
type Hetzner struct {
	ServerType string   `yaml:"server_type,omitempty"`
	Location   string   `yaml:"location,omitempty"`
	Image      string   `yaml:"image,omitempty"`
	SSHKeys    []string `yaml:"ssh_keys,omitempty"`
	// Credential names the API token in the OS credential store (default
	// hetzner); keelbox up stores it there. TokenFile, when set, is used
	// instead: a mode 600 file holding the token.
	Credential string `yaml:"credential,omitempty"`
	TokenFile  string `yaml:"token_file,omitempty"`
	// HcloudContext, when set, names a context of Hetzner's hcloud CLI:
	// keelbox asks hcloud for that context's token each time it needs
	// one and keeps no copy (docs/adr/0066-hcloud-cli-token.md).
	HcloudContext string            `yaml:"hcloud_context,omitempty"`
	Labels        map[string]string `yaml:"labels,omitempty"`
}

// AWS configures server creation on AWS, as an Amazon Lightsail instance,
// through the user's installed AWS CLI
// (docs/adr/0065-aws-through-the-aws-cli.md). keelbox holds no AWS
// credential: Profile names the AWS CLI profile whose sign-in is used.
type AWS struct {
	Region string `yaml:"region,omitempty"`
	// Profile is passed to the AWS CLI as --profile; empty uses the CLI's
	// own default (AWS_PROFILE, then "default").
	Profile string `yaml:"profile,omitempty"`
	// Bundle is the Lightsail bundle id (the instance plan).
	Bundle string `yaml:"bundle,omitempty"`
	// Blueprint is the Lightsail blueprint id (the image).
	Blueprint        string `yaml:"blueprint,omitempty"`
	AvailabilityZone string `yaml:"availability_zone,omitempty"`
	// Tags are extra tags for the instance, next to keelbox's own.
	Tags map[string]string `yaml:"tags,omitempty"`
}

// DigitalOcean configures server creation on DigitalOcean, as a Droplet,
// through the user's installed doctl CLI
// (docs/adr/0067-digitalocean-through-doctl.md). keelbox holds no
// DigitalOcean credential: Context names the doctl authentication context
// whose sign-in is used.
type DigitalOcean struct {
	Region string `yaml:"region,omitempty"`
	// Size is the Droplet size slug.
	Size string `yaml:"size,omitempty"`
	// Image is the image slug or id.
	Image string `yaml:"image,omitempty"`
	// Context is passed to doctl as --context; empty uses doctl's current
	// context.
	Context string `yaml:"context,omitempty"`
	// Tags are extra tags for the Droplet, next to keelbox's own.
	Tags []string `yaml:"tags,omitempty"`
}

// ValidDoctlContext checks the name of a doctl authentication context;
// empty is doctl's current one.
func ValidDoctlContext(s string) error {
	if s != "" && !doctlContextRe.MatchString(s) {
		return errors.New("use the name of a doctl context made of letters, digits, '.', '_' and '-' (doctl auth list shows yours)")
	}
	return nil
}

// DefaultDigitalOceanImage is DigitalOcean's Ubuntu 24.04 LTS x64 image.
const DefaultDigitalOceanImage = "ubuntu-24-04-x64"

// GCP configures server creation on Google Cloud, as a Compute Engine VM,
// through the user's installed gcloud CLI
// (docs/adr/0068-google-cloud-through-gcloud.md). keelbox holds no Google
// credential: the sign-in is the one gcloud already has.
type GCP struct {
	// Project is the Google Cloud project id, passed to gcloud as
	// --project; empty uses the project gcloud is configured for.
	Project string `yaml:"project,omitempty"`
	Zone    string `yaml:"zone,omitempty"`
	// MachineType is the Compute Engine machine type.
	MachineType string `yaml:"machine_type,omitempty"`
	// DiskGB is the size of the boot disk in GB.
	DiskGB int `yaml:"disk_gb,omitempty"`
	// Labels are extra labels for the VM, next to keelbox's own.
	Labels map[string]string `yaml:"labels,omitempty"`
}

// DefaultGCPDiskGB is the boot disk size of a Google Cloud box. The
// Ubuntu image's own default, 10 GB, does not hold Docker images and a
// few projects.
const DefaultGCPDiskGB = 50

// ValidGCPProject checks a Google Cloud project id; empty is the project
// gcloud is configured for.
func ValidGCPProject(s string) error {
	if s != "" && !gcpProjectRe.MatchString(s) {
		return errors.New("use a Google Cloud project id: 6 to 30 lowercase letters, digits and hyphens, starting with a letter (gcloud projects list shows yours)")
	}
	return nil
}

// ValidHcloudContext checks the name of an hcloud CLI context; empty is none.
func ValidHcloudContext(s string) error {
	if s != "" && !hcloudContextRe.MatchString(s) {
		return errors.New("use the name of an hcloud context made of letters, digits, '.', '_' and '-' (hcloud context list shows yours; hcloud context rename changes one)")
	}
	return nil
}

// ValidAWSProfile checks an AWS CLI profile name; empty is the CLI's default.
func ValidAWSProfile(s string) error {
	if s != "" && !awsProfileRe.MatchString(s) {
		return errors.New("an AWS CLI profile name: letters, digits and _ . @ + = , / -")
	}
	return nil
}

// DefaultAWSBlueprint is Lightsail's Ubuntu 24.04 LTS image.
const DefaultAWSBlueprint = "ubuntu_24_04"

// Host is the operating-system layer.
type Host struct {
	Hostname           string   `yaml:"hostname,omitempty"`
	Timezone           string   `yaml:"timezone,omitempty"`
	Locale             string   `yaml:"locale,omitempty"`
	Swap               string   `yaml:"swap,omitempty"`
	Packages           []string `yaml:"packages,omitempty"`
	UnattendedUpgrades *bool    `yaml:"unattended_upgrades,omitempty"`
	AutoReboot         string   `yaml:"auto_reboot,omitempty"`
	Firewall           Firewall `yaml:"firewall"`
	Power              Power    `yaml:"power,omitempty"`
	Disk               Disk     `yaml:"disk,omitempty"`
}

// Firewall configures UFW. Docker-published ports bypass it: bind to
// 127.0.0.1.
type Firewall struct {
	Enabled *bool `yaml:"enabled,omitempty"`
	// PublicSSH is auto (close public SSH once keelbox has reached sshd
	// over the tailnet), open or closed; true and false are read as open
	// and closed.
	PublicSSH PublicSSHMode `yaml:"public_ssh,omitempty"`
	// Allow keeps ports open to the LAN or a named network, besides SSH
	// and the tailnet.
	Allow []Allow `yaml:"allow,omitempty"`
	// LinkLocalAccounts are accounts besides root that keep access to
	// link-local addresses, cloud metadata among them, on every port: a
	// monitoring agent on a machine keelbox adopted. It is not a UFW
	// setting: the agent network table reads it whether or not Enabled is
	// set (docs/adr/0076-network-limits-for-every-account.md).
	LinkLocalAccounts []string `yaml:"link_local_accounts,omitempty"`
}

// MaxLinkLocalAccounts bounds host.firewall.link_local_accounts: each is a
// rule every packet to a link-local address passes.
const MaxLinkLocalAccounts = 16

// Users are the two accounts keelbox manages.
type Users struct {
	Admin Account      `yaml:"admin"`
	Agent AgentAccount `yaml:"agent"`
	// LockCloudUser locks the cloud image's default account (the one
	// cloud-init gave passwordless sudo, such as ubuntu) once the admin
	// account is proven to work. Default true.
	LockCloudUser *bool `yaml:"lock_cloud_user,omitempty"`
}

// Account is one Linux user.
type Account struct {
	Name                  string   `yaml:"name,omitempty"`
	SSHAuthorizedKeys     []string `yaml:"ssh_authorized_keys,omitempty"`
	SSHAuthorizedKeyFiles []string `yaml:"ssh_authorized_key_files,omitempty"`
	Sudo                  *bool    `yaml:"sudo,omitempty"`
}

// Runtime is the developer toolchain.
type Runtime struct {
	Docker *bool `yaml:"docker,omitempty"`
	Node   *bool `yaml:"node,omitempty"`
	GH     *bool `yaml:"gh,omitempty"` // GitHub CLI, for the agent's own GitHub sign-in
}

// Sessions configures tmux and Claude Code Remote Control.
type Sessions struct {
	Tmux          *bool         `yaml:"tmux,omitempty"`
	Recovery      *bool         `yaml:"recovery,omitempty"`
	StartOnBoot   *bool         `yaml:"start_on_boot,omitempty"`
	Watchdog      *bool         `yaml:"watchdog,omitempty"` // see Watchdog()
	Welcome       *bool         `yaml:"welcome,omitempty"`  // see SessionWelcome()
	RemoteControl RemoteControl `yaml:"remote_control"`
}

// Access configures how the box is reached privately.
type Access struct {
	Tailscale Tailscale `yaml:"tailscale"`
}

// Tailscale installs tailscaled. Joining the tailnet is the user's step
// unless an auth key file is given; the key is passed to tailscale by path
// and never read into logs.
type Tailscale struct {
	Enabled     *bool  `yaml:"enabled,omitempty"`
	AuthKeyFile string `yaml:"auth_key_file,omitempty"`
	Hostname    string `yaml:"hostname,omitempty"`
}

// Primer configures the generated AGENTS.md.
type Primer struct {
	Enabled *bool  `yaml:"enabled,omitempty"`
	Notes   string `yaml:"notes,omitempty"`
}

// Known agent identifiers.
const (
	AgentClaudeCode = "claude-code"
	AgentCodex      = "codex"
)

// KnownAgents lists the agents this build can install.
var KnownAgents = []string{AgentClaudeCode, AgentCodex}

// Providers.
const (
	ProviderLocal   = "local"
	ProviderSSH     = "ssh"
	ProviderHetzner = "hetzner"
	ProviderAWS     = "aws"
	// ProviderDigitalOcean creates a Droplet through the user's doctl CLI.
	ProviderDigitalOcean = "digitalocean"
	// ProviderGCP creates a Compute Engine VM through the user's gcloud CLI.
	ProviderGCP = "gcp"
)

// On reports a tri-state option, applying its default.
func On(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// ApplyDefaults fills unset fields. Called by Load; safe to call twice.
func (c *Config) ApplyDefaults() {
	if c.Channel == "" {
		c.Channel = "stable"
	}
	if c.Target.Provider == "" {
		c.Target.Provider = ProviderLocal
	}
	if c.Target.SSH.Port == 0 {
		c.Target.SSH.Port = 22
	}
	if c.Users.Admin.Name == "" {
		c.Users.Admin.Name = "admin"
	}
	if c.Users.Agent.Name == "" {
		c.Users.Agent.Name = "agent"
	}
	if c.Target.SSH.User == "" {
		c.Target.SSH.User = c.Users.Admin.Name
	}
	if c.Target.Provider == ProviderHetzner {
		h := &c.Target.Hetzner
		if h.Image == "" {
			h.Image = "ubuntu-24.04"
		}
		if h.Credential == "" {
			h.Credential = "hetzner"
		}
	}
	if c.Target.Provider == ProviderAWS && c.Target.AWS.Blueprint == "" {
		c.Target.AWS.Blueprint = DefaultAWSBlueprint
	}
	if c.Target.Provider == ProviderDigitalOcean && c.Target.DigitalOcean.Image == "" {
		c.Target.DigitalOcean.Image = DefaultDigitalOceanImage
	}
	if c.Target.Provider == ProviderGCP && c.Target.GCP.DiskGB == 0 {
		c.Target.GCP.DiskGB = DefaultGCPDiskGB
	}
	if c.Host.Hostname == "" {
		c.Host.Hostname = c.Box
	}
	if c.Host.Timezone == "" {
		c.Host.Timezone = "UTC"
	}
	if c.Host.Locale == "" {
		c.Host.Locale = "C.UTF-8"
	}
	if c.Host.Swap == "" {
		c.Host.Swap = "2G"
	}
	if c.Host.AutoReboot == "" {
		c.Host.AutoReboot = AutoRebootNightly
	}
	if c.Access.Tailscale.Hostname == "" {
		c.Access.Tailscale.Hostname = c.Host.Hostname
	}
	if c.Agents == nil {
		c.Agents = []string{AgentClaudeCode, AgentCodex}
	}
	c.applyFeatureDefaults()
}

// Helpers with defaults applied, so modules never re-implement them.

func (c *Config) UnattendedUpgrades() bool { return On(c.Host.UnattendedUpgrades, true) }

// host.auto_reboot: nightly has the box reboot itself for an update at
// night once it has been idle, and weekly on Sunday night
// (internal/watch/reboot.go); never leaves it to the owner; a time HH:MM
// has unattended-upgrades reboot then, idle or not.
const (
	AutoRebootNightly = "nightly"
	AutoRebootWeekly  = "weekly"
	AutoRebootNever   = "never"
)

// RebootsItself reports whether the watch run reboots the box for an
// update: host.auto_reboot nightly or weekly.
func (c *Config) RebootsItself() bool {
	return c.Host.AutoReboot == AutoRebootNightly || c.Host.AutoReboot == AutoRebootWeekly
}

// ValidAutoReboot reports whether s is a value of host.auto_reboot.
func ValidAutoReboot(s string) bool {
	return s == AutoRebootNightly || s == AutoRebootWeekly || s == AutoRebootNever || rebootRe.MatchString(s)
}

func (c *Config) FirewallEnabled() bool { return On(c.Host.Firewall.Enabled, true) }
func (c *Config) LockCloudUser() bool   { return On(c.Users.LockCloudUser, true) }
func (c *Config) Docker() bool          { return On(c.Runtime.Docker, true) }
func (c *Config) Node() bool            { return On(c.Runtime.Node, true) }
func (c *Config) GH() bool              { return On(c.Runtime.GH, true) }
func (c *Config) Tmux() bool            { return On(c.Sessions.Tmux, true) }
func (c *Config) TmuxRecovery() bool    { return c.Tmux() && On(c.Sessions.Recovery, true) }
func (c *Config) TmuxStartOnBoot() bool { return c.Tmux() && On(c.Sessions.StartOnBoot, true) }
func (c *Config) SessionWelcome() bool  { return c.Tmux() && On(c.Sessions.Welcome, true) }
func (c *Config) Tailscale() bool       { return On(c.Access.Tailscale.Enabled, true) }
func (c *Config) PrimerEnabled() bool   { return On(c.Primer.Enabled, true) }

// HasAgent reports whether the named agent is requested.
func (c *Config) HasAgent(name string) bool {
	for _, a := range c.Agents {
		if a == name {
			return true
		}
	}
	return false
}

// SwapBytes parses host.swap; 0 means keelbox leaves swap alone.
func (c *Config) SwapBytes() (int64, error) { return parseSize(c.Host.Swap) }

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "0" || s == "OFF" || s == "NONE" {
		return 0, nil
	}
	var mult int64
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	default:
		return 0, fmt.Errorf("%q: use a size like 512M or 2G, or 0 to leave swap alone", s)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q: use a size like 512M or 2G, or 0 to leave swap alone", s)
	}
	return n * mult, nil
}

// Error collects every validation problem, so one run shows them all.
type Error struct {
	File     string
	Problems []string
}

func (e *Error) Error() string {
	var b strings.Builder
	name := e.File
	if name == "" {
		name = "config"
	}
	if len(e.Problems) == 1 {
		fmt.Fprintf(&b, "%s: 1 problem:", name)
	} else {
		fmt.Fprintf(&b, "%s: %d problems:", name, len(e.Problems))
	}
	for _, p := range e.Problems {
		b.WriteString("\n  - " + p)
	}
	return b.String()
}

// Parse decodes and validates a config document. Unknown fields are errors.
func Parse(data []byte, file string) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &Error{File: file, Problems: []string{"file is empty"}}
		}
		return nil, &Error{File: file, Problems: yamlProblems(err)}
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		var ve *Error
		if errors.As(err, &ve) {
			ve.File = file
		}
		return nil, err
	}
	return &c, nil
}

var yamlFieldRe = regexp.MustCompile(`field (\S+) not found in type config\.(\w+)`)

func yamlProblems(err error) []string {
	var te *yaml.TypeError
	var lines []string
	if errors.As(err, &te) {
		lines = te.Errors
	} else {
		lines = []string{err.Error()}
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimPrefix(l, "yaml: ")
		l = yamlFieldRe.ReplaceAllString(l, "unknown field \"$1\" in $2")
		out = append(out, l)
	}
	return out
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, path)
}

// Marshal renders the config back to YAML (used to ship a resolved config
// to a remote box).
func (c *Config) Marshal() ([]byte, error) { return yaml.Marshal(c) }

// Hash is a short content hash of the effective config, recorded in the
// logbook so changes to the config file are traceable.
func (c *Config) Hash() string {
	b, _ := c.Marshal()
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

var (
	labelRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	userRe     = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)
	pkgRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	tzRe       = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)
	localeRe   = regexp.MustCompile(`^[A-Za-z]{1,3}(_[A-Z]{2})?\.(UTF-8|utf8)$`)
	rebootRe   = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	sshKeyRe   = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/=]+( .*)?$`)
	reserved   = map[string]bool{"root": true, "daemon": true, "bin": true, "sys": true, "nobody": true, "syslog": true, "docker": true}
	hetznerRes = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
	// Values keelbox passes to the AWS CLI as arguments: none can start
	// with "-" or hold a space.
	awsRegionRe     = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	awsProfileRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@+=,/-]{0,127}$`)
	hcloudContextRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	awsTagRe        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+=._:/@-]{0,127}$`)
	awsIDRe         = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)
	// Values keelbox passes to doctl as arguments, under the same rule.
	// A tag also holds no comma: doctl splits a list of tags at commas.
	doRegionRe     = regexp.MustCompile(`^[a-z]{2,8}[0-9]{1,2}$`)
	doSlugRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	doTagRe        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$`)
	doctlContextRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	// Values keelbox passes to gcloud as arguments, under the same rule. A
	// project id may carry a domain prefix ("example.com:project").
	gcpProjectRe = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]{0,61}:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	gcpZoneRe    = regexp.MustCompile(`^[a-z]+(-[a-z]+)+[0-9]+-[a-z]$`)
	gcpTypeRe    = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)+$`)
	gcpLabelRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	gcpValueRe   = regexp.MustCompile(`^[a-z0-9_-]{0,63}$`)
	gcpNameRe    = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,47}[a-z0-9])?$`)
	hostKeyRe    = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	// credentialRe matches credstore.ValidKey.
	credentialRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
)

// Validate checks the config after defaults are applied.
func (c *Config) Validate() error {
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }

	if c.Version != Version {
		add("version: must be %d (got %d)", Version, c.Version)
	}
	if c.Box == "" {
		add("box: required, the box name (lowercase letters, digits and hyphens)")
	} else if !labelRe.MatchString(c.Box) {
		add("box: %q must be lowercase letters, digits and hyphens, at most 63 characters", c.Box)
	}
	if c.Channel != "stable" {
		add("channel: %q is not available; only \"stable\" exists", c.Channel)
	}

	switch c.Target.Provider {
	case ProviderLocal:
	case ProviderSSH:
		if c.Target.SSH.Host == "" {
			add("target.ssh.host: required for provider ssh (for example host: 203.0.113.10)")
		}
	case ProviderHetzner:
		h := c.Target.Hetzner
		if h.ServerType == "" {
			add("target.hetzner.server_type: required (for example cpx22)")
		} else if !hetznerRes.MatchString(h.ServerType) {
			add("target.hetzner.server_type: %q is not a server type name", h.ServerType)
		}
		if h.Location == "" {
			add("target.hetzner.location: required (for example fsn1, nbg1, hel1)")
		}
		if !credentialRe.MatchString(h.Credential) {
			add("target.hetzner.credential: %q: use lowercase letters, digits, '.', '_' and '-' (for example hetzner)", h.Credential)
		}
		if err := ValidHcloudContext(h.HcloudContext); err != nil {
			add("target.hetzner.hcloud_context: %q: %v", h.HcloudContext, err)
		}
		if h.HcloudContext != "" && h.TokenFile != "" {
			add("target.hetzner.hcloud_context and token_file are both set; keep one: the hcloud CLI's token, or the token file")
		}
		for k, v := range h.Labels {
			if !labelRe.MatchString(k) && !regexp.MustCompile(`^[a-z0-9.-]+/[a-z0-9-]+$`).MatchString(k) {
				add("target.hetzner.labels: key %q is not a valid label key", k)
			}
			if v != "" && !regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$`).MatchString(v) {
				add("target.hetzner.labels: value %q for %q is not a valid label value", v, k)
			}
		}
	case ProviderAWS:
		a := c.Target.AWS
		switch {
		case a.Region == "":
			add("target.aws.region: required (for example eu-central-1)")
		case !awsRegionRe.MatchString(a.Region):
			add("target.aws.region: %q is not an AWS region name (for example eu-central-1)", a.Region)
		}
		switch {
		case a.Bundle == "":
			add("target.aws.bundle: required, a Lightsail bundle id (for example medium_3_0; keelbox up lists them with prices)")
		case !awsIDRe.MatchString(a.Bundle):
			add("target.aws.bundle: %q is not a Lightsail bundle id (for example medium_3_0)", a.Bundle)
		}
		if !awsIDRe.MatchString(a.Blueprint) {
			add("target.aws.blueprint: %q is not a Lightsail blueprint id (for example %s)", a.Blueprint, DefaultAWSBlueprint)
		}
		if a.Profile != "" && !awsProfileRe.MatchString(a.Profile) {
			add("target.aws.profile: %q is not an AWS CLI profile name (aws configure list-profiles lists yours)", a.Profile)
		}
		if z := a.AvailabilityZone; z != "" && (a.Region == "" || !regexp.MustCompile("^"+regexp.QuoteMeta(a.Region)+"[a-z]$").MatchString(z)) {
			add("target.aws.availability_zone: %q is not a zone of region %s (for example %sa)", z, a.Region, a.Region)
		}
		for k, v := range a.Tags {
			if !awsTagRe.MatchString(k) || strings.HasPrefix(strings.ToLower(k), "aws:") {
				add("target.aws.tags: key %q is not a tag key keelbox can set: letters, digits and + - = . _ : / @, not starting with aws:", k)
			}
			if v != "" && !awsTagRe.MatchString(v) {
				add("target.aws.tags: value %q for %q is not a tag value keelbox can set: letters, digits and + - = . _ : / @", v, k)
			}
		}
		if len(c.Box) < 2 {
			add("box: %q is too short for provider aws: Lightsail names have at least 2 characters", c.Box)
		}
	case ProviderDigitalOcean:
		d := c.Target.DigitalOcean
		switch {
		case d.Region == "":
			add("target.digitalocean.region: required, a region slug (for example fra1)")
		case !doRegionRe.MatchString(d.Region):
			add("target.digitalocean.region: %q is not a DigitalOcean region slug (for example fra1)", d.Region)
		}
		switch {
		case d.Size == "":
			add("target.digitalocean.size: required, a Droplet size slug (for example s-2vcpu-4gb; keelbox up lists them with prices)")
		case !doSlugRe.MatchString(d.Size):
			add("target.digitalocean.size: %q is not a Droplet size slug (for example s-2vcpu-4gb)", d.Size)
		}
		if !doSlugRe.MatchString(d.Image) {
			add("target.digitalocean.image: %q is not an image slug or id (for example %s)", d.Image, DefaultDigitalOceanImage)
		}
		if err := ValidDoctlContext(d.Context); err != nil {
			add("target.digitalocean.context: %q: %v", d.Context, err)
		}
		for _, t := range d.Tags {
			switch {
			case !doTagRe.MatchString(t):
				add("target.digitalocean.tags: %q is not a tag keelbox can set: letters, digits, ':', '_' and '-'", t)
			case t == "keelbox" || strings.HasPrefix(t, "keelbox-box-"):
				add("target.digitalocean.tags: %q is one of the tags keelbox sets itself to know its own Droplets; remove it", t)
			}
		}
	case ProviderGCP:
		g := c.Target.GCP
		if err := ValidGCPProject(g.Project); err != nil {
			add("target.gcp.project: %q: %v", g.Project, err)
		}
		switch {
		case g.Zone == "":
			add("target.gcp.zone: required, a Compute Engine zone (for example europe-west3-a)")
		case !gcpZoneRe.MatchString(g.Zone):
			add("target.gcp.zone: %q is not a Compute Engine zone (for example europe-west3-a)", g.Zone)
		}
		switch {
		case g.MachineType == "":
			add("target.gcp.machine_type: required, a Compute Engine machine type (for example e2-medium; keelbox up lists some)")
		case !gcpTypeRe.MatchString(g.MachineType):
			add("target.gcp.machine_type: %q is not a machine type name (for example e2-medium)", g.MachineType)
		}
		if g.DiskGB < 10 || g.DiskGB > 65536 {
			add("target.gcp.disk_gb: %d is out of range; use 10 or more, or remove it for %d", g.DiskGB, DefaultGCPDiskGB)
		}
		for k, v := range g.Labels {
			switch {
			case !gcpLabelRe.MatchString(k) || !gcpValueRe.MatchString(v):
				add("target.gcp.labels: %q: %q is not a label keelbox can set: lowercase letters, digits, '_' and '-', the key starting with a letter", k, v)
			case k == "keelbox" || k == "keelbox-box":
				add("target.gcp.labels: %q is one of the labels keelbox sets itself to know its own VMs; remove it", k)
			}
		}
		// The VM, its network tag, its address and its firewall rules are
		// named after the box, and Compute Engine names start with a letter
		// and have at most 63 characters.
		if c.Box != "" && !gcpNameRe.MatchString(c.Box) {
			add("box: %q cannot name a Google Cloud VM: start with a letter and use at most 49 characters", c.Box)
		}
	default:
		add("target.provider: %q is not one of local, ssh, hetzner, aws, digitalocean, gcp (keelbox init --provider ssh writes an example)", c.Target.Provider)
	}
	if k := c.Target.SSH.HostKey; k != "" && !hostKeyRe.MatchString(k) {
		add("target.ssh.host_key: %q is not a SHA256 fingerprint (SHA256: and 43 base64 characters, as ssh-keygen -l prints)", k)
	}
	if c.Target.SSH.Port < 1 || c.Target.SSH.Port > 65535 {
		add("target.ssh.port: %d is out of range; use 1 to 65535, or remove it for 22", c.Target.SSH.Port)
	}

	boxOK := labelRe.MatchString(c.Box)
	// host.hostname and the tailnet name default to box; report them only
	// when they differ, so one bad box name is one problem.
	if !labelRe.MatchString(c.Host.Hostname) && (boxOK || c.Host.Hostname != c.Box) {
		add("host.hostname: %q is not a valid hostname label", c.Host.Hostname)
	}
	if !tzRe.MatchString(c.Host.Timezone) {
		add("host.timezone: %q is not a timezone name like UTC or Europe/London", c.Host.Timezone)
	}
	if !localeRe.MatchString(c.Host.Locale) {
		add("host.locale: %q must be a UTF-8 locale like C.UTF-8 or en_GB.UTF-8", c.Host.Locale)
	}
	if _, err := c.SwapBytes(); err != nil {
		add("host.swap: %v", err)
	}
	for _, pkg := range c.Host.Packages {
		if !pkgRe.MatchString(pkg) {
			add("host.packages: %q is not an apt package name", pkg)
		}
	}
	if !ValidAutoReboot(c.Host.AutoReboot) {
		add("host.auto_reboot: %q must be nightly, weekly, never or a time like \"04:00\"", c.Host.AutoReboot)
	}

	for role, a := range map[string]Account{"admin": c.Users.Admin, "agent": c.Users.Agent.Account} {
		if !userRe.MatchString(a.Name) {
			add("users.%s.name: %q is not a valid Linux user name (lowercase, start with a letter)", role, a.Name)
		} else if reserved[a.Name] {
			add("users.%s.name: %q is a system account", role, a.Name)
		}
		for i, k := range a.SSHAuthorizedKeys {
			if !sshKeyRe.MatchString(strings.TrimSpace(k)) {
				add("users.%s.ssh_authorized_keys[%d]: not an OpenSSH public key line", role, i)
			}
		}
	}
	if c.Users.Admin.Name == c.Users.Agent.Name {
		add("users: admin and agent must be different accounts (both are %q)", c.Users.Admin.Name)
	}
	if c.Users.Admin.Sudo != nil && !*c.Users.Admin.Sudo {
		add("users.admin.sudo: the admin account always has sudo; remove this field")
	}
	if len(c.Users.Admin.SSHAuthorizedKeys)+len(c.Users.Admin.SSHAuthorizedKeyFiles) == 0 {
		add("users.admin: at least one SSH key is required (ssh_authorized_keys or ssh_authorized_key_files); keelbox turns off SSH passwords and root login, so a box without one would lock you out")
	}

	seen := map[string]bool{}
	for _, a := range c.Agents {
		if seen[a] {
			add("agents: %q listed twice", a)
		}
		seen[a] = true
		if !contains(KnownAgents, a) {
			add("agents: %q is unknown; available: %s", a, strings.Join(KnownAgents, ", "))
		}
	}

	if c.PublicSSHMode() == PublicSSHClosed && !c.Tailscale() {
		add("host.firewall.public_ssh: closed needs Tailscale, or the box is unreachable; remove access.tailscale.enabled: false, or set public_ssh: auto")
	}
	if c.Access.Tailscale.AuthKeyFile != "" && !c.Tailscale() {
		add("access.tailscale.auth_key_file: set but tailscale is disabled; remove the line, or access.tailscale.enabled: false")
	}
	if c.Access.Tailscale.Hostname != "" && !labelRe.MatchString(c.Access.Tailscale.Hostname) && (boxOK || c.Access.Tailscale.Hostname != c.Box) {
		add("access.tailscale.hostname: %q is not a valid hostname label", c.Access.Tailscale.Hostname)
	}
	if !c.Tmux() && (c.Sessions.Recovery != nil && *c.Sessions.Recovery) {
		add("sessions.recovery: needs tmux; remove sessions.recovery, or sessions.tmux: false")
	}
	c.validateFeatures(add)
	c.validateNotify(add)
	c.validateDoctor(add)
	c.validatePower(add)
	c.validateReach(add)
	c.validateFirewall(add)
	c.validateLimits(add)
	c.validatePermissions(add)
	c.validateGit(add)

	if len(p) > 0 {
		sort.Strings(p)
		return &Error{Problems: p}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ExpandHome expands a leading ~/ against the invoking user's home. Under
// sudo that is SUDO_USER's home, not root's, so `sudo keelbox apply` reads
// the same key files as the user who typed it.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if su := os.Getenv("SUDO_USER"); su != "" && os.Geteuid() == 0 {
		if u, uerr := user.Lookup(su); uerr == nil {
			home, err = u.HomeDir, nil
		}
	}
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// AllKeys returns an account's inline keys plus the contents of its key
// files, read where keelbox runs. Remote providers call this before
// shipping the config so the box never needs the laptop's files.
func (a Account) AllKeys() ([]string, error) {
	keys := append([]string(nil), a.SSHAuthorizedKeys...)
	for _, f := range a.SSHAuthorizedKeyFiles {
		data, err := os.ReadFile(ExpandHome(f))
		if err != nil {
			return nil, fmt.Errorf("ssh_authorized_key_files: %w (no key there? ssh-keygen -t ed25519 makes ~/.ssh/id_ed25519.pub; or list the path of yours)", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !sshKeyRe.MatchString(line) {
				return nil, fmt.Errorf("ssh_authorized_key_files: %s contains a line that is not an OpenSSH public key", f)
			}
			keys = append(keys, line)
		}
	}
	return dedupe(keys), nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, k := range in {
		k = strings.TrimSpace(k)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Resolve inlines key files so the config is self-contained. The caller
// owns the copy.
func (c *Config) Resolve() (*Config, error) {
	out := *c
	if c.Agents != nil {
		// Copied as an empty list, not nil: nil means the default agents.
		out.Agents = append([]string{}, c.Agents...)
	}
	for _, acct := range []*Account{&out.Users.Admin, &out.Users.Agent.Account} {
		keys, err := acct.AllKeys()
		if err != nil {
			return nil, err
		}
		acct.SSHAuthorizedKeys = keys
		acct.SSHAuthorizedKeyFiles = nil
	}
	return &out, nil
}
