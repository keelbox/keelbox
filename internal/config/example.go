package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

//go:embed example.yaml.tmpl
var exampleTemplate string

var starter = template.Must(template.New("config").Funcs(template.FuncMap{
	"q":     yamlScalar,
	"join":  joinScalars,
	"pad":   func(w int, s string) string { return fmt.Sprintf("%-*s ", w-1, s) }, // always one space before a comment
	"allow": AllowYAML,
}).Parse(exampleTemplate))

// Starter is what a new, commented config is written from: the target and
// the few choices keelbox init and keelbox up make. Everything else gets
// the documented defaults.
type Starter struct {
	Box      string
	Provider string // local, ssh, hetzner, aws, digitalocean or gcp

	// Provider ssh.
	SSHHost         string
	HostPlaceholder bool // SSHHost is an example to change
	SSHUser         string
	HostKey         string
	// Provider ssh and the cloud providers.
	IdentityFile string

	// Provider digitalocean (with Region, and Image below).
	Size         string
	DoctlContext string

	// Provider gcp. Project empty is the project gcloud is configured for.
	Project     string
	Zone        string
	MachineType string

	// Provider aws; Region also for digitalocean.
	Region    string
	Profile   string
	Bundle    string
	Blueprint string

	// Provider hetzner.
	ServerType  string
	Location    string
	Image       string
	HetznerKeys []string
	Credential  string
	TokenFile   string
	// HcloudContext reads the token from this hcloud CLI context instead.
	HcloudContext string

	// SSH public keys for both accounts: inline Keys, else KeyFiles.
	KeyFiles []string
	Keys     []string

	// Agents to install; nil means the default (all known agents).
	Agents        []string
	RemoteControl bool
	NoTailscale   bool
	PublicSSH     string

	// Host settings keelbox up keeps from the machine it sets up: its own
	// timezone and locale (default UTC and C.UTF-8), and its hostname when
	// the user keeps it (default: the box name).
	Timezone string
	Locale   string
	Hostname string
	// NoGH leaves the GitHub CLI out (runtime.gh: false).
	NoGH bool
	// Allow keeps ports open to the LAN (host.firewall.allow).
	Allow []Allow
	// AgentLimit caps the agent's CPU and memory at this percentage
	// (users.agent.limits); 0 leaves it unlimited.
	AgentLimit int
	// AgentUnlimited writes limits: {memory: none}: the user asked for no
	// limits, which lifts the default memory ceiling too.
	AgentUnlimited bool
	// GitName and GitEmail are the identity the agent's commits carry
	// (users.agent.git); both empty leaves it unset.
	GitName, GitEmail string
	// KeepCloudUser leaves the cloud image's account unlocked: on this
	// machine it is the user's own (users.lock_cloud_user: false).
	KeepCloudUser bool
	// Permissions is users.agent.permissions: manual, auto or full. Empty
	// and auto leave the key out, which means auto.
	Permissions string
	// AutoReboot is host.auto_reboot: nightly, weekly or never. Empty is
	// nightly.
	AutoReboot string
}

// ExplicitPermissions reports whether the config names the agent's
// permissions: only when they are not the default. A config without the
// key stays readable by a keelbox from before the key existed.
func (s Starter) ExplicitPermissions() bool {
	return s.Permissions != "" && s.Permissions != PermissionsAuto
}

// Example returns the commented starter config `keelbox init` writes for
// provider local.
func Example(box string) string {
	s, err := StarterFor(box, ProviderLocal).Render()
	if err != nil {
		panic(err) // the template and its defaults are tested
	}
	return s
}

// StarterFor is a starter with placeholder values for a provider, as
// `keelbox init --provider` writes it.
func StarterFor(box, provider string) Starter {
	if box == "" {
		box = "my-box"
	}
	s := Starter{Box: box, Provider: provider}
	switch provider {
	case ProviderSSH:
		s.SSHHost, s.HostPlaceholder = "203.0.113.10", true
	case ProviderHetzner:
		s.ServerType, s.Location, s.Image = "cpx22", "fsn1", "ubuntu-24.04"
		s.HetznerKeys = []string{"keelbox-laptop"}
	case ProviderAWS:
		s.Region, s.Bundle, s.Blueprint = "eu-central-1", "medium_3_0", DefaultAWSBlueprint
	case ProviderDigitalOcean:
		s.Region, s.Size, s.Image = "fra1", "s-2vcpu-4gb", DefaultDigitalOceanImage
	case ProviderGCP:
		s.Zone, s.MachineType = "europe-west3-a", "e2-medium"
	}
	return s
}

// Render writes the commented config and checks that it parses.
func (s Starter) Render() (string, error) {
	if s.Provider == "" {
		s.Provider = ProviderLocal
	}
	if s.Agents == nil {
		s.Agents = KnownAgents
	}
	if len(s.KeyFiles) == 0 && len(s.Keys) == 0 {
		s.KeyFiles = []string{"~/.ssh/id_ed25519.pub"}
	}
	if s.PublicSSH == "" {
		s.PublicSSH = "auto"
	}
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	if s.Locale == "" {
		s.Locale = "C.UTF-8"
	}
	if s.AutoReboot == "" {
		s.AutoReboot = AutoRebootNightly
	}
	var b bytes.Buffer
	if err := starter.Execute(&b, s); err != nil {
		return "", err
	}
	out := b.String()
	if _, err := Parse([]byte(out), s.Box+".yaml"); err != nil {
		return "", err
	}
	return out, nil
}

var bareScalar = regexp.MustCompile(`^[A-Za-z0-9~/.][A-Za-z0-9._/~+-]*$`)

// yamlScalar writes a string so YAML reads it back unchanged: bare when
// that is unambiguous, double-quoted otherwise.
func yamlScalar(s string) string {
	switch strings.ToLower(s) {
	case "", "true", "false", "yes", "no", "on", "off", "null", "~":
		return strconv.Quote(s)
	}
	if bareScalar.MatchString(s) {
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return s
		}
	}
	return strconv.Quote(s)
}

func joinScalars(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = yamlScalar(s)
	}
	return strings.Join(q, ", ")
}
