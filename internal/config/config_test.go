package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"

func minimal(extra string) string {
	return "version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [\"" + key + "\"]\n" + extra
}

func problems(t *testing.T, y string) []string {
	t.Helper()
	_, err := Parse([]byte(y), "t.yaml")
	if err == nil {
		return nil
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a config error: %v", err)
	}
	return ce.Problems
}

func TestExampleIsValid(t *testing.T) {
	for _, box := range []string{"", "demo-1"} {
		c, err := Parse([]byte(Example(box)), "example")
		if err != nil {
			t.Fatalf("example config (%q) invalid: %v", box, err)
		}
		if box != "" && c.Box != box {
			t.Errorf("box = %s", c.Box)
		}
	}
}

func TestStarterForEveryProvider(t *testing.T) {
	for _, p := range []string{ProviderLocal, ProviderSSH, ProviderHetzner, ProviderAWS, ProviderDigitalOcean, ProviderGCP} {
		out, err := StarterFor("acme", p).Render()
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		c, err := Parse([]byte(out), "starter")
		if err != nil || c.Target.Provider != p || c.Box != "acme" {
			t.Fatalf("%s: %v %+v", p, err, c)
		}
		if strings.Contains(out, "<no value>") || strings.Contains(out, "{{") {
			t.Errorf("%s: unrendered template:\n%s", p, out)
		}
		// The agents comment says how the CLIs are installed: the vendors'
		// own installers, in the agent's home.
		if !strings.Contains(out, "# Each vendor's own installer puts its CLI in the agent's home") {
			t.Errorf("%s: the agents comment does not say how the CLIs are installed:\n%s", p, out)
		}
	}
}

func TestStarterWithChoices(t *testing.T) {
	out, err := Starter{
		Box: "lab", Provider: ProviderHetzner, ServerType: "cpx32", Location: "hel1", Image: "ubuntu-24.04",
		HetznerKeys: []string{"keelbox-mac"}, IdentityFile: "/home/someone/.config/keelbox/laptop_ed25519", KeyFiles: []string{"/home/someone/.config/keelbox/laptop_ed25519.pub"},
		Agents: []string{AgentClaudeCode}, RemoteControl: true, NoTailscale: true, PublicSSH: "open",
	}.Render()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(out), "starter")
	if err != nil {
		t.Fatal(err)
	}
	h := c.Target.Hetzner
	checks := map[string]bool{
		"type":           h.ServerType == "cpx32" && h.Location == "hel1",
		"keys":           len(h.SSHKeys) == 1 && h.SSHKeys[0] == "keelbox-mac",
		"credential":     h.Credential == "hetzner" && h.TokenFile == "",
		"identity":       c.Target.SSH.IdentityFile == "/home/someone/.config/keelbox/laptop_ed25519",
		"key files":      c.Users.Admin.SSHAuthorizedKeyFiles[0] == "/home/someone/.config/keelbox/laptop_ed25519.pub" && c.Users.Agent.SSHAuthorizedKeyFiles[0] == "/home/someone/.config/keelbox/laptop_ed25519.pub",
		"agents":         len(c.Agents) == 1 && c.HasAgent(AgentClaudeCode),
		"remote control": c.RemoteControl(),
		"no tailscale":   !c.Tailscale() && !c.PreviewsEnabled(),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s:\n%s", name, out)
		}
	}
	if _, err := (Starter{Box: "x", Provider: ProviderSSH, SSHHost: "10.0.0.5", Agents: []string{},
		Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}, HostKey: "SHA256:" + strings.Repeat("a", 43)}).Render(); err != nil {
		t.Errorf("inline keys, no agents: %v", err)
	}
	if _, err := (Starter{Box: "Bad_Box"}).Render(); err == nil {
		t.Error("bad box rendered")
	}
}

// keelbox up on this machine keeps its timezone and locale and the ports
// the user wants open on the LAN.
func TestStarterKeepsTheMachine(t *testing.T) {
	out, err := Starter{Box: "nas", Timezone: "Europe/London", Locale: "en_GB.UTF-8",
		Allow: []Allow{{Name: "plex", Port: Port(32400)}, {Port: Port(137), Proto: "udp", From: "192.168.1.0/24"}}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(out), "starter")
	if err != nil {
		t.Fatal(err)
	}
	if c.Host.Timezone != "Europe/London" || c.Host.Locale != "en_GB.UTF-8" {
		t.Errorf("host: %+v", c.Host)
	}
	if a := c.Host.Firewall.Allow; len(a) != 2 || a[0].Name != "plex" || a[1].Proto != "udp" || a[1].From != "192.168.1.0/24" {
		t.Errorf("allow: %+v\n%s", a, out)
	}
	if !strings.Contains(out, "      - {name: plex, port: 32400}\n") {
		t.Errorf("allow entries not written one per line:\n%s", out)
	}
}

func TestYAMLScalar(t *testing.T) {
	for in, want := range map[string]string{"fsn1": "fsn1", "yes": `"yes"`, "1.5": `"1.5"`, "a b": `"a b"`, "~/.ssh/k.pub": "~/.ssh/k.pub", "SHA256:x/y": `"SHA256:x/y"`} {
		if got := yamlScalar(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte(minimal("")), "t")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]bool{
		"provider local":    c.Target.Provider == ProviderLocal,
		"admin name":        c.Users.Admin.Name == "admin",
		"agent name":        c.Users.Agent.Name == "agent",
		"ssh user is admin": c.Target.SSH.User == "admin",
		"hostname from box": c.Host.Hostname == "acme",
		"utc":               c.Host.Timezone == "UTC",
		"firewall on":       c.FirewallEnabled() && c.PublicSSHMode() == PublicSSHAuto,
		"agent no sudo":     !c.AgentSudo(),
		"both agents":       c.HasAgent(AgentClaudeCode) && c.HasAgent(AgentCodex),
		"tmux recovery":     c.TmuxRecovery() && c.TmuxStartOnBoot(),
		"auto reboot":       c.Host.AutoReboot == "nightly",
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("default %s not applied", name)
		}
	}
	if n, _ := c.SwapBytes(); n != 2<<30 {
		t.Errorf("swap = %d", n)
	}
}

func TestUnknownFieldIsAnError(t *testing.T) {
	p := problems(t, minimal("hots:\n  timezone: UTC\n"))
	if len(p) != 1 || !strings.Contains(p[0], `unknown field "hots"`) {
		t.Fatalf("got %v", p)
	}
}

func TestAllProblemsReportedTogether(t *testing.T) {
	p := problems(t, `version: 1
box: acme
agents: [claude-code, gemini, claude-code]
target: {provider: ssh}
host: {swap: lots, timezone: "not a zone", locale: en_GB, auto_reboot: "25:00", packages: ["Bad Pkg"]}
users:
  admin: {name: agent}
  agent: {name: agent, ssh_authorized_keys: ["not a key"]}
`)
	joined := strings.Join(p, "\n")
	for _, want := range []string{
		"target.ssh.host: required", `agents: "gemini" is unknown`, `"claude-code" listed twice`,
		"host.swap", "host.timezone", "host.locale", "host.auto_reboot", "host.packages",
		"admin and agent must be different", "users.agent.ssh_authorized_keys[0]", "users.admin: at least one SSH key",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing problem %q in:\n%s", want, joined)
		}
	}
}

func TestBadBoxNameReportedOnce(t *testing.T) {
	p := problems(t, "version: 1\nbox: Acme_Box\nusers: {admin: {ssh_authorized_keys: [\""+key+"\"]}}\n")
	if len(p) != 1 || !strings.HasPrefix(p[0], "box:") {
		t.Fatalf("got %v", p)
	}
}

func TestUnreachableBoxRejected(t *testing.T) {
	p := problems(t, minimal("host: {firewall: {public_ssh: false}}\naccess: {tailscale: {enabled: false}}\n"))
	if len(p) != 1 || !strings.Contains(p[0], "unreachable") {
		t.Fatalf("got %v", p)
	}
}

func TestHetznerTarget(t *testing.T) {
	p := problems(t, minimal("target: {provider: hetzner, ssh: {host: 1.2.3.4}, hetzner: {labels: {\"Bad Key\": x}}}\n"))
	joined := strings.Join(p, "\n")
	for _, want := range []string{"server_type: required", "location: required", "not a valid label key"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	c, err := Parse([]byte(minimal("target: {provider: hetzner, hetzner: {server_type: cx23, location: fsn1}}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if c.Target.Hetzner.Image == "" || c.Target.Hetzner.Credential != "hetzner" || c.Target.Hetzner.TokenFile != "" {
		t.Error("hetzner defaults missing")
	}
	// The hcloud context is a name, not together with a token file.
	p = problems(t, minimal("target: {provider: hetzner, hetzner: {server_type: cx23, location: fsn1, hcloud_context: \"my work\"}}\n"))
	if len(p) != 1 || !strings.Contains(p[0], "target.hetzner.hcloud_context: \"my work\"") {
		t.Errorf("context name: %v", p)
	}
	p = problems(t, minimal("target: {provider: hetzner, hetzner: {server_type: cx23, location: fsn1, hcloud_context: work, token_file: ~/t}}\n"))
	if len(p) != 1 || !strings.Contains(p[0], "keep one") {
		t.Errorf("context and token file: %v", p)
	}
}

func TestAWSTarget(t *testing.T) {
	p := problems(t, minimal("target: {provider: aws}\n"))
	joined := strings.Join(p, "\n")
	for _, want := range []string{"target.aws.region: required", "target.aws.bundle: required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	// Every value keelbox hands to the AWS CLI as an argument is checked:
	// none can start with "-" or hold a space.
	p = problems(t, minimal("target: {provider: aws, aws: {region: \"--debug\", bundle: \"medium 3\", blueprint: \"-x\", profile: \"--profile\", "+
		"availability_zone: us-east-1a, tags: {\"a,b\": x, ok: \"v w\", \"aws:x\": y}}}\n"))
	joined = strings.Join(p, "\n")
	for _, want := range []string{"target.aws.region: \"--debug\"", "target.aws.bundle: \"medium 3\"", "target.aws.blueprint: \"-x\"",
		"target.aws.profile: \"--profile\"", "target.aws.availability_zone", "key \"a,b\"", "value \"v w\"", "key \"aws:x\""} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	c, err := Parse([]byte(minimal("target: {provider: aws, aws: {region: eu-central-1, bundle: medium_3_0, profile: work, availability_zone: eu-central-1b, tags: {team: web}}}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if c.Target.AWS.Blueprint != DefaultAWSBlueprint {
		t.Errorf("blueprint default: %q", c.Target.AWS.Blueprint)
	}
	if ValidAWSProfile("") != nil || ValidAWSProfile("my-sso.profile") != nil || ValidAWSProfile("-x") == nil || ValidAWSProfile("a b") == nil {
		t.Error("profile names")
	}
}

func TestDigitalOceanTarget(t *testing.T) {
	p := problems(t, minimal("target: {provider: digitalocean}\n"))
	joined := strings.Join(p, "\n")
	for _, want := range []string{"target.digitalocean.region: required", "target.digitalocean.size: required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	// Every value keelbox hands to doctl as an argument is checked: none
	// can start with "-", hold a space, or a comma that would split a list.
	p = problems(t, minimal("target: {provider: digitalocean, digitalocean: {region: \"--trace\", size: \"s 2vcpu\", image: \"-x\", context: \"--context\", "+
		"tags: [\"a,b\", \"-t\", keelbox, keelbox-box-other, \"team:web\"]}}\n"))
	joined = strings.Join(p, "\n")
	for _, want := range []string{"target.digitalocean.region: \"--trace\"", "target.digitalocean.size: \"s 2vcpu\"", "target.digitalocean.image: \"-x\"",
		"target.digitalocean.context: \"--context\"", "tags: \"a,b\"", "tags: \"-t\"", "tags: \"keelbox\" is one of the tags keelbox sets", "tags: \"keelbox-box-other\""} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "team:web") {
		t.Errorf("a valid tag was refused: %s", joined)
	}
	c, err := Parse([]byte(minimal("target: {provider: digitalocean, digitalocean: {region: fra1, size: s-2vcpu-4gb, context: work, tags: [\"team:web\"]}}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if c.Target.DigitalOcean.Image != DefaultDigitalOceanImage {
		t.Errorf("image default: %q", c.Target.DigitalOcean.Image)
	}
	if ValidDoctlContext("") != nil || ValidDoctlContext("my-team.1") != nil || ValidDoctlContext("-x") == nil || ValidDoctlContext("a b") == nil {
		t.Error("context names")
	}
}

func TestGCPTarget(t *testing.T) {
	p := problems(t, minimal("target: {provider: gcp}\n"))
	joined := strings.Join(p, "\n")
	for _, want := range []string{"target.gcp.zone: required", "target.gcp.machine_type: required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	// Every value keelbox hands to gcloud as an argument is checked: none
	// can start with "-", hold a space, or a comma that would split a list.
	p = problems(t, minimal("target: {provider: gcp, gcp: {project: \"--account\", zone: \"--zone\", machine_type: \"e2 medium\", disk_gb: 5, "+
		"labels: {\"a,b\": x, team: \"Web Site\", keelbox: \"true\", keelbox-box: other, owner: sam_1}}}\n"))
	joined = strings.Join(p, "\n")
	for _, want := range []string{"target.gcp.project: \"--account\"", "gcloud projects list", "target.gcp.zone: \"--zone\"", "target.gcp.machine_type: \"e2 medium\"",
		"target.gcp.disk_gb: 5", "labels: \"a,b\"", "labels: \"team\": \"Web Site\"", "labels: \"keelbox\" is one of the labels keelbox sets", "labels: \"keelbox-box\""} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "owner") {
		t.Errorf("a valid label was refused: %s", joined)
	}
	// The VM, its tag, its address and its firewall rules carry the box's
	// name: it starts with a letter and leaves room for their suffixes.
	for _, box := range []string{"1box", strings.Repeat("a", 50)} {
		p = problems(t, strings.Replace(minimal("target: {provider: gcp, gcp: {zone: europe-west3-a, machine_type: e2-medium}}\n"), "box: acme", "box: "+box, 1))
		if joined = strings.Join(p, "\n"); !strings.Contains(joined, "cannot name a Google Cloud VM") {
			t.Errorf("box %s: %s", box, joined)
		}
	}
	c, err := Parse([]byte(minimal("target: {provider: gcp, gcp: {project: \"example.com:acme-prod\", zone: northamerica-northeast1-b, machine_type: n2d-standard-2, labels: {team: web}}}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if c.Target.GCP.DiskGB != DefaultGCPDiskGB {
		t.Errorf("disk default: %d", c.Target.GCP.DiskGB)
	}
	if ValidGCPProject("") != nil || ValidGCPProject("acme-dev-1") != nil || ValidGCPProject("-x") == nil || ValidGCPProject("Acme") == nil || ValidGCPProject("short") == nil {
		t.Error("project ids")
	}
}

func TestEmptyFile(t *testing.T) {
	if p := problems(t, ""); len(p) != 1 || p[0] != "file is empty" {
		t.Fatalf("got %v", p)
	}
}

func TestResolveInlinesKeyFiles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "id.pub")
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOther other@host"
	if err := os.WriteFile(f, []byte("# comment\n"+other+"\n"+key+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(minimal("    ssh_authorized_key_files: [\""+f+"\"]\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Users.Admin.SSHAuthorizedKeys) != 2 || r.Users.Admin.SSHAuthorizedKeyFiles != nil {
		t.Errorf("keys = %v files = %v", r.Users.Admin.SSHAuthorizedKeys, r.Users.Admin.SSHAuthorizedKeyFiles)
	}
	if len(c.Users.Admin.SSHAuthorizedKeyFiles) != 1 {
		t.Error("Resolve mutated the original")
	}
	if err := os.WriteFile(f, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(); err == nil {
		t.Error("non-key line accepted")
	}
}

func TestHashStable(t *testing.T) {
	a, _ := Parse([]byte(minimal("")), "t")
	b, _ := Parse([]byte(minimal("")), "t")
	if a.Hash() != b.Hash() || len(a.Hash()) != 12 {
		t.Error("hash unstable")
	}
	c, _ := Parse([]byte(minimal("host: {timezone: Europe/London}\n")), "t")
	if c.Hash() == a.Hash() {
		t.Error("hash ignores changes")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	a, _ := Parse([]byte(minimal("")), "t")
	data, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(data, "rt")
	if err != nil {
		t.Fatalf("round trip invalid: %v\n%s", err, data)
	}
	if a.Hash() != b.Hash() {
		t.Error("round trip changed the config")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "off": 0, "512M": 512 << 20, "4g": 4 << 30} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "2", "-1G", "2T", "xG"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHostKeyPinFormat(t *testing.T) {
	ok := "target: {provider: ssh, ssh: {host: h, host_key: \"SHA256:" + strings.Repeat("a", 43) + "\"}}\n"
	if p := problems(t, minimal(ok)); p != nil {
		t.Errorf("valid fingerprint rejected: %v", p)
	}
	p := problems(t, minimal("target: {provider: ssh, ssh: {host: h, host_key: \"MD5:aa:bb\"}}\n"))
	if len(p) != 1 || !strings.Contains(p[0], "host_key") {
		t.Errorf("%v", p)
	}
}

func TestPublicSSHModes(t *testing.T) {
	for in, want := range map[string]PublicSSHMode{"": PublicSSHAuto, "auto": PublicSSHAuto, "open": PublicSSHOpen, "closed": PublicSSHClosed, "lan": PublicSSHLAN, "true": PublicSSHOpen, "false": PublicSSHClosed} {
		y := minimal("access:\n  tailscale:\n    enabled: true\n")
		if in != "" {
			y += "host:\n  firewall:\n    public_ssh: " + in + "\n"
		}
		c, err := Parse([]byte(y), "t")
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if c.PublicSSHMode() != want {
			t.Errorf("%q: got %s, want %s", in, c.PublicSSHMode(), want)
		}
	}
	if p := problems(t, minimal("host:\n  firewall:\n    public_ssh: sometimes\n")); len(p) != 1 || !strings.Contains(p[0], "auto, lan, open or closed") {
		t.Errorf("invalid mode: %v", p)
	}
}

func TestTailnetHost(t *testing.T) {
	c, err := Parse([]byte(minimal("target:\n  ssh:\n    host: 192.0.2.10\n    tailnet_host: 127.0.0.1:2222\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if h, p := c.TailnetHostPort(); h != "127.0.0.1" || p != 2222 {
		t.Errorf("got %s %d", h, p)
	}
	c.Target.SSH.TailnetHost = "acme.tail1234.ts.net"
	if h, p := c.TailnetHostPort(); h != "acme.tail1234.ts.net" || p != 22 {
		t.Errorf("got %s %d", h, p)
	}
	if p := problems(t, minimal("access:\n  tailscale:\n    enabled: false\ntarget:\n  ssh:\n    host: 192.0.2.10\n    tailnet_host: acme\n")); len(p) == 0 || !strings.Contains(strings.Join(p, " "), "tailnet_host") {
		t.Errorf("tailnet_host without tailscale: %v", p)
	}
}

func TestRemoteControlConfig(t *testing.T) {
	c, err := Parse([]byte(minimal("sessions:\n  remote_control:\n    enabled: true\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if !c.RemoteControl() || c.RemoteControlDir() != "projects" || c.RemoteControlSpawn() != "same-dir" {
		t.Errorf("defaults: %v %s %s", c.RemoteControl(), c.RemoteControlDir(), c.RemoteControlSpawn())
	}
	if c, _ := Parse([]byte(minimal("")), "t"); c.RemoteControl() {
		t.Error("remote control on by default")
	}
	for y, want := range map[string]string{
		"sessions:\n  remote_control:\n    directory: x\n":                          "enabled",
		"sessions:\n  remote_control:\n    enabled: true\n    directory: .\n":       "directory",
		"sessions:\n  remote_control:\n    enabled: true\n    directory: /srv\n":    "directory",
		"sessions:\n  remote_control:\n    enabled: true\n    directory: ../x\n":    "directory",
		"sessions:\n  remote_control:\n    enabled: true\n    directory: \"a b\"\n": "directory",
		"sessions:\n  remote_control:\n    enabled: true\n    spawn: session\n":     "spawn",
		"agents: [codex]\nsessions:\n  remote_control:\n    enabled: true\n":        "claude-code",
	} {
		if p := problems(t, minimal(y)); len(p) == 0 || !strings.Contains(strings.Join(p, " "), want) {
			t.Errorf("%q: want a problem about %s, got %v", y, want, p)
		}
	}
}

func TestDefaultBackupLeavesOutReinstallableTools(t *testing.T) {
	c, _ := Parse([]byte(minimal("")), "t")
	got := strings.Join(c.BackupExclude(), " ")
	for _, d := range []string{".local/share/claude/versions", ".codex/packages", ".local/share/mise", ".local/share/pnpm", "node_modules"} {
		if !strings.Contains(got, d) {
			t.Errorf("default exclude lacks %s: %s", d, got)
		}
	}
}

// keelbox up keeps a machine's hostname and can leave the GitHub CLI out.
func TestStarterKeepsHostnameAndGH(t *testing.T) {
	out, err := Starter{Box: "acme", Provider: ProviderLocal, Hostname: "kb-srv", NoGH: true, PublicSSH: "lan"}.Render()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte(out), "acme.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Host.Hostname != "kb-srv" || c.Access.Tailscale.Hostname != "kb-srv" || c.GH() || c.PublicSSHMode() != PublicSSHLAN {
		t.Errorf("host %+v gh %v public_ssh %s", c.Host, c.GH(), c.PublicSSHMode())
	}
	if out, _ := (Starter{Box: "acme"}).Render(); strings.Contains(out, "\n  hostname:") {
		t.Errorf("hostname written when it is the box name:\n%s", out)
	}
}

// agents: [] means no agents, through Resolve and the defaults the box
// applies, not the default pair.
func TestResolveKeepsNoAgents(t *testing.T) {
	c, err := Parse([]byte(minimal("agents: []\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	r.ApplyDefaults()
	if len(r.Agents) != 0 {
		t.Errorf("agents: [] became %v", r.Agents)
	}
	if d, _ := Parse([]byte(minimal("")), "t"); len(d.Agents) != 2 {
		t.Errorf("default agents %v", d.Agents)
	}
}

// The commented config names the published page for known services, not
// a path in the repository, which a user of the binary does not have.
func TestExampleLinksThePublishedDocs(t *testing.T) {
	out := Example("demo")
	if !strings.Contains(out, "https://keelbox.dev/docs/reference/services") {
		t.Errorf("the example config does not link the services reference:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "docs/") && !strings.Contains(l, "https://keelbox.dev/docs/") {
			t.Errorf("the example config names a docs path that is not a keelbox.dev URL: %s", l)
		}
	}
}

// One problem is "1 problem", several are counted in the plural.
func TestErrorCountsItsProblems(t *testing.T) {
	one := (&Error{File: "acme.yaml", Problems: []string{"box: is required"}}).Error()
	if one != "acme.yaml: 1 problem:\n  - box: is required" {
		t.Errorf("one problem: %q", one)
	}
	two := (&Error{Problems: []string{"a", "b"}}).Error()
	if two != "config: 2 problems:\n  - a\n  - b" {
		t.Errorf("two problems: %q", two)
	}
}
