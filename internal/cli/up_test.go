package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider/aws"
)

// fakeRunner records what keelbox up asks of the box.
type fakeRunner struct {
	calls []string
	looks []string // read-only: sudo -n, the machine probe
	// skillsOffered counts the offers to copy skills.
	skillsOffered int
	// results by call name; default 0 / true.
	createCode, applyCode int
	signed                bool
	login_                loginResult // what the sign-in step ends with
	rootReachable         bool
	onCreate              func(o options)
	onApply               func()
	hostKeys              []string
	noSudo                bool
	probe                 string // what the machine probe prints; default ubuntuProbe
	inspectErr            error
	inspectErrs           []error // returned by the first inspects, in turn, before inspectErr
	copyErr               error
	onCopy                func()
	report                *impactReport // what plan --impact finds; default: nothing in use
	impactErr             error
	log                   string        // what create writes, through the progress view
	logDelay              time.Duration // between the log's lines
	rcFails               bool          // Remote Control does not start
	notChecked            []string      // sign-ins the check could not run for, as up prints them
}

func (f *fakeRunner) impact(_ context.Context, cfg *config.Config, o options) (impactReport, error) {
	where := " as " + o.sshUser
	if cfg.Target.Provider == config.ProviderLocal {
		where = " here"
	}
	if o.askSudo {
		where += " (sudo asks)"
	}
	f.looks = append(f.looks, "impact "+cfg.Box+where)
	if f.impactErr != nil {
		return impactReport{}, f.impactErr
	}
	if f.report != nil {
		return *f.report, nil
	}
	// By default UFW is off: everything the probe found listening is cut
	// off unless the config keeps it.
	probe := f.probe
	if probe == "" {
		probe = ubuntuProbe
	}
	r := impactReport{Impact: 1, Box: cfg.Box, FirstApply: true}
	// As the modules do: a known service is one item, kept by name.
	svc := map[string]int{}
	for _, l := range parseProbe(probe).Listeners {
		if l.Service != "" {
			if i, ok := svc[l.Service]; ok {
				r.Items[i].Ports = append(r.Items[i].Ports, l.Key())
				continue
			}
			svc[l.Service] = len(r.Items)
			r.Items = append(r.Items, engine.ImpactItem{Group: engine.ImpactCuts, Module: "host", Subject: l.Service, Gate: true, Ports: []string{l.Key()},
				Detail: l.Label + " listens on the network now", Keep: &config.Allow{Service: l.Service}})
			continue
		}
		r.Items = append(r.Items, engine.ImpactItem{Group: engine.ImpactCuts, Module: "host", Subject: l.Key(), Gate: true,
			Detail: l.Label + " listens on the network now", Keep: &config.Allow{Name: l.Name, Port: config.Port(l.Port)}})
	}
	return r, nil
}

// ubuntuProbe is a fresh Ubuntu server: nothing listening but SSH.
const ubuntuProbe = "os=ubuntu 24.04\nhostname=lab\nuser=ubuntu\nsudo=yes\nsessions=0\n" +
	"listen=tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\n"

func (f *fakeRunner) create(_ context.Context, cfg *config.Config, o options, stdout, _ io.Writer) int {
	f.calls = append(f.calls, "create "+cfg.Box)
	if f.onCreate != nil {
		f.onCreate(o)
	}
	if f.log != "" {
		out, _, done := applyView(cfg, o, stdout, stdout)
		for _, l := range strings.SplitAfter(f.log, "\n") {
			_, _ = io.WriteString(out, l)
			time.Sleep(f.logDelay)
		}
		done()
	}
	return f.createCode
}

func (f *fakeRunner) apply(_ context.Context, cfg *config.Config, o options, _, _ io.Writer) int {
	user := o.sshUser
	if user == "" {
		user = "admin"
	}
	if o.adopt {
		user += " --adopt"
	}
	if o.askSudo {
		user += " (sudo asks)"
	}
	f.calls = append(f.calls, "apply "+cfg.Box+" as "+user)
	if f.onApply != nil {
		f.onApply()
	}
	return f.applyCode
}

func (f *fakeRunner) reachable(_ context.Context, _ *config.Config, host, user string) bool {
	f.calls = append(f.calls, "reachable "+host+" "+user)
	return f.rootReachable
}

func (f *fakeRunner) firstContact(_ context.Context, cfg *config.Config, hostKey string, _ io.Writer) error {
	f.calls = append(f.calls, "first-contact "+cfg.Target.SSH.Host)
	f.hostKeys = append(f.hostKeys, hostKey)
	return nil
}

func (f *fakeRunner) notSignedIn(_ context.Context, cfg *config.Config, _ options) (left, notChecked []string) {
	f.calls = append(f.calls, "signed-in?")
	if f.signed {
		return nil, nil
	}
	return strings.Fields(signInNamesList(cfg)), f.notChecked
}

func signInNamesList(cfg *config.Config) string {
	var n []string
	for _, s := range signIns(cfg) {
		n = append(n, s.name)
	}
	return strings.Join(n, " ")
}

func (f *fakeRunner) copyID(_ context.Context, cfg *config.Config, user, pub string, _ io.Writer) error {
	f.calls = append(f.calls, "ssh-copy-id "+user+"@"+cfg.Target.SSH.Host+" "+filepath.Base(pub))
	if f.onCopy != nil {
		f.onCopy()
	}
	return f.copyErr
}

func (f *fakeRunner) sudo(context.Context, bool, io.Writer) bool {
	f.looks = append(f.looks, "sudo")
	return !f.noSudo
}

func (f *fakeRunner) inspect(_ context.Context, cfg *config.Config, user string, _ io.Writer) (machineFacts, error) {
	if cfg == nil {
		f.looks = append(f.looks, "inspect here")
	} else {
		f.looks = append(f.looks, "inspect "+cfg.Target.SSH.Host+" as "+user)
	}
	err := f.inspectErr
	if len(f.inspectErrs) > 0 {
		err, f.inspectErrs = f.inspectErrs[0], f.inspectErrs[1:]
	}
	if f.probe == "" {
		return parseProbe(ubuntuProbe), err
	}
	return parseProbe(f.probe), err
}

func (f *fakeRunner) login(_ context.Context, cfg *config.Config, _ options, _ *prompt.Prompter, _, _ io.Writer) loginResult {
	f.calls = append(f.calls, "login "+cfg.Box)
	return f.login_
}

func (f *fakeRunner) skills(context.Context, *config.Config, options, *prompt.Prompter, io.Writer, io.Writer) {
	f.skillsOffered++
}

func (f *fakeRunner) remoteControl(_ context.Context, cfg *config.Config, _ options, _ *prompt.Prompter, _, _ io.Writer) bool {
	f.calls = append(f.calls, "remote-control "+cfg.Box)
	return !f.rcFails
}

// gitSet is true while a test asks keelbox up's git identity question.
var gitSet bool

// permissionsAsked is true while a test has keelbox up ask how much the
// agent may change the box.
var permissionsAsked bool

// askingPermissions has keelbox up ask its permissions question.
func askingPermissions(t *testing.T) {
	t.Helper()
	permissionsAsked = true
	t.Cleanup(func() { permissionsAsked = false })
}

// rebootsAsked is true while a test has keelbox up ask when the box
// restarts for updates.
var rebootsAsked bool

func askingReboots(t *testing.T) {
	t.Helper()
	rebootsAsked = true
	t.Cleanup(func() { rebootsAsked = false })
}

// withLaptopGit has keelbox up ask for the agent's git identity, with this
// one in the laptop's git config (both empty: none).
func withLaptopGit(t *testing.T, name, email string) {
	t.Helper()
	old := laptopGit
	laptopGit = func(k string) string { return map[string]string{"user.name": name, "user.email": email}[k] }
	gitSet = true
	t.Cleanup(func() { laptopGit, gitSet = old, false })
}

// up runs keelbox up with scripted input: interactive when input is not nil.
func upWith(t *testing.T, r *fakeRunner, input *string, args ...string) (int, string) {
	t.Helper()
	// The default answer to "where" must not depend on the test machine.
	defer func(f func() bool) { localLikely = f }(localLikely)
	localLikely = func() bool { return false }
	// The git identity question is skipped, as --git-name "" skips it,
	// unless the test is about it (withLaptopGit).
	if !gitSet {
		args = append(args, "--git-name", "", "--git-email", "")
	}
	// Nor is the permissions question asked, as --agent-permissions ""
	// leaves it at the default, unless the test is about it.
	if !permissionsAsked && !contains(args, "--agent-permissions") {
		args = append(args, "--agent-permissions", "")
	}
	// Nor the reboots question, unless the test is about it.
	if !rebootsAsked && !contains(args, "--auto-reboot") {
		args = append(args, "--auto-reboot", "")
	}
	o, pos, err := parse(args[0], args[1:], io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var p *prompt.Prompter
	if input != nil {
		p = prompt.New(strings.NewReader(*input), &out, true)
	} else {
		p = prompt.New(strings.NewReader(""), &out, false)
	}
	code := cmdUp(context.Background(), o, pos, p, r, &out, &out)
	return code, out.String()
}

func script(lines ...string) *string {
	s := strings.Join(lines, "\n") + "\n"
	return &s
}

// laptopKey makes ~/.ssh/id_ed25519 in the test's home.
func laptopKey(t *testing.T, home string) string {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	p := filepath.Join(home, ".ssh", "id_ed25519")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "you@laptop", "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	return p
}

func loadBox(t *testing.T, box string) (*config.Config, upState) {
	t.Helper()
	cfg, err := config.Load(boxConfigPath(box))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := loadUpState(cfg)
	return cfg, st
}

func TestUpHetznerInteractive(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	api, store := fakeHetzner(t)
	r := &fakeRunner{}
	in := script(
		"3",       // where: a new cloud server
		"1",       // which cloud: Hetzner
		"acme",    // name
		goodToken, // token (hidden)
		"",        // location: default fsn1
		"",        // size: default
		"",        // agents: both
		"",        // GitHub CLI: yes
		"",        // Tailscale: yes
		"",        // go ahead
		"y",       // after the Claude Code sign-in: Remote Control
	)
	code, out := upWith(t, r, in, "up")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	h := cfg.Target.Hetzner
	if h.ServerType != "cx23" || h.Location != "fsn1" || len(h.SSHKeys) != 1 || !strings.HasPrefix(h.SSHKeys[0], "keelbox-") {
		t.Errorf("hetzner target %+v", h)
	}
	if !cfg.RemoteControl() || !cfg.Tailscale() || len(cfg.Agents) != 2 {
		t.Errorf("choices not in the config: %+v", cfg)
	}
	if st.Stage != stageReady || st.Via != viaHetzner {
		t.Errorf("state %+v", st)
	}
	if len(api.Keys) != 1 || api.Keys[0].Labels["keelbox"] != "true" {
		t.Errorf("key not uploaded: %+v", api.Keys)
	}
	if s, _ := store.Get(context.Background(), "hetzner"); s != goodToken {
		t.Error("token not stored")
	}
	// Remote Control is asked about after the Claude Code sign-in, and
	// starts after the last apply has installed it.
	if got := strings.Join(r.calls, "; "); got != "create acme; signed-in?; login acme; apply acme as admin; remote-control acme" {
		t.Errorf("calls: %s", got)
	}
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{
		"after the Claude Code sign-in, keelbox asks whether to reach the box from the Claude app too",
		"Claude Code Remote Control: start and continue Claude Code sessions on acme from the Claude app or claude.ai/code, with no SSH app or terminal.",
		"Anyone signed in to your Claude account can then run commands on acme as the agent.",
		"Turn on Remote Control, so the Claude app can start sessions on acme? [y/N]",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(out, "To turn it on later") {
		t.Errorf("a yes is told how to turn it on later:\n%s", out)
	}
	if strings.Index(out, "Turn on Remote Control") < strings.Index(out, "Create the server") {
		t.Errorf("Remote Control asked before the go-ahead:\n%s", out)
	}
	for _, want := range []string{
		"Where should the box run?", "3) A new cloud server", "Which cloud?", "1) Hetzner", "cx23   2 vCPU (shared), 4 GB, 40 GB disk, EUR 3.79/month",
		"EUR 3.79 a month or EUR 0.0060 an hour", "\nacme is set up", "Start working:", "keelbox attach acme", "add your phone's key to the box first", "Claude app      Remote Control",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(out, goodToken) {
		t.Error("token in the output")
	}
	if strings.Contains(out, "cpx11") || strings.Contains(out, "cx33") {
		t.Errorf("offered a type that cannot be ordered:\n%s", out)
	}
	// The Arm64 type is offered, marked as one, and is not the default.
	if !strings.Contains(out, "cax11  2 vCPU (shared, Arm64), 4 GB") && !strings.Contains(out, "cax11   2 vCPU (shared, Arm64), 4 GB") {
		t.Errorf("the Arm64 type is not offered as one:\n%s", out)
	}
	// Running up again on a ready box only prints the summary.
	r.calls = nil
	code, out = upWith(t, r, nil, "up", "acme")
	if code != 0 || len(r.calls) != 0 || !saysSetUp(out, "acme") {
		t.Errorf("ready box: %d %v\n%s", code, r.calls, out)
	}
}

// A no to Remote Control says how to turn it on later, and only then.
func TestUpRemoteControlDeclined(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	r := &fakeRunner{}
	code, out := upWith(t, r, script("3", "1", "acme", goodToken, "", "", "", "", "", "", "n"), "up")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	flat := strings.Join(strings.Fields(out), " ")
	want := "To turn it on later: set sessions.remote_control.enabled: true in ~/.config/keelbox/boxes/acme.yaml, then keelbox apply acme and keelbox login acme claude-code."
	if !strings.Contains(flat, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if cfg, _ := loadBox(t, "acme"); cfg.RemoteControl() || contains(r.calls, "remote-control acme") {
		t.Errorf("Remote Control on after a no: %v", r.calls)
	}
}

func TestUpNonInteractiveNeedsInputs(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	r := &fakeRunner{}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"up", "acme"}, "--provider"},
		{[]string{"up", "--provider", "ssh"}, "keelbox up acme"},
		{[]string{"up", "acme", "--provider", "ssh"}, "--host"},
		{[]string{"up", "acme", "--provider", "ssh", "--host", "10.0.0.5"}, "--yes"},
		{[]string{"up", "acme", "--provider", "hetzner", "--yes"}, "--token-file"},
		{[]string{"up", "acme", "--provider", "azure"}, "ssh, local, hetzner, aws, digitalocean, gcp or cloud-init"},
		{[]string{"up", "Bad_Name"}, "keelbox up bad-name"},
		{[]string{"up", "acme", "--provider", "ssh", "--host", "10.0.0.5", "--agents", "gemini", "--yes"}, "--agents"},
	}
	for _, c := range cases {
		code, out := upWith(t, r, nil, c.args...)
		if code == 0 || !strings.Contains(out, c.want) {
			t.Errorf("%v: exit %d, want a message with %q:\n%s", c.args, code, c.want, out)
		}
	}
	if len(r.calls) != 0 || len(registeredBoxes()) != 0 {
		t.Errorf("changed something: %v %v", r.calls, registeredBoxes())
	}
}

func TestUpSSHStopsAtSignInAndResumes(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{}
	code, out := upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "codex", "--tailscale=false", "--ssh-key", key, "--yes")
	if code != exitNeedsYou || !strings.Contains(out, "In a terminal, run:\n  keelbox up lab") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := strings.Join(r.calls, "; "); got != "apply lab as ubuntu; signed-in?" || strings.Join(r.looks, "; ") != "inspect 10.0.0.5 as ubuntu; impact lab as ubuntu" {
		t.Errorf("calls: %s; looked: %v", got, r.looks)
	}
	cfg, st := loadBox(t, "lab")
	if cfg.Target.SSH.Host != "10.0.0.5" || cfg.Tailscale() || !cfg.HasAgent("codex") || cfg.HasAgent("claude-code") || st.Stage != stageApplied {
		t.Errorf("config %+v state %+v", cfg.Target, st)
	}
	if code, out, _ := run("ls"); code != 0 || !strings.Contains(out, "needs sign-in: keelbox up lab") {
		t.Errorf("ls: %s", out)
	}
	// Again without a terminal: nothing is re-applied, it stops at sign-in.
	r.calls = nil
	code, out = upWith(t, r, nil, "up", "lab", "--size", "cx23")
	if code != exitNeedsYou || strings.Join(r.calls, "; ") != "signed-in?" || !strings.Contains(out, "continuing lab") || !strings.Contains(out, "--size change nothing") {
		t.Errorf("resume: %d %v\n%s", code, r.calls, out)
	}
	// On a terminal: sign in, apply again as the admin, ready.
	r.calls = nil
	code, out = upWith(t, r, script(), "up", "lab")
	if code != 0 || strings.Join(r.calls, "; ") != "signed-in?; login lab; apply lab as admin" || !saysSetUp(out, "lab") {
		t.Errorf("finish: %d %v\n%s", code, r.calls, out)
	}
	if !strings.Contains(out, "ssh agent@10.0.0.5") {
		t.Errorf("no public phone address without Tailscale:\n%s", out)
	}
}

func TestUpCloudInit(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	r := &fakeRunner{}
	code, out := upWith(t, r, nil, "up", "web", "--provider", "cloud-init", "--yes")
	if code != exitNeedsYou || !strings.Contains(out, "#cloud-config") || !strings.Contains(out, "keelbox up web --host") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Contains(out, "ssh_keys:") == false || !strings.Contains(out, "ed25519_private") {
		t.Errorf("no host key in the snippet:\n%s", out)
	}
	if len(r.calls) != 0 || isRegistered("web") {
		t.Errorf("acted before the server exists: %v", r.calls)
	}
	if code, out, _ := run("ls"); !strings.Contains(out, "web") || !strings.Contains(out, "waiting for the server") {
		t.Errorf("ls: %d %s", code, out)
	}
	code, out = upWith(t, r, nil, "up", "web", "--host", "198.51.100.9")
	if code != exitNeedsYou {
		t.Fatalf("with host: exit %d\n%s", code, out)
	}
	if got := strings.Join(r.calls, "; "); got != "first-contact 198.51.100.9; apply web as admin; signed-in?" {
		t.Errorf("calls: %s", got)
	}
	if len(r.hostKeys) != 1 || !strings.HasPrefix(r.hostKeys[0], "ssh-ed25519 ") {
		t.Errorf("host key not pinned from the snippet: %q", r.hostKeys)
	}
	cfg, st := loadBox(t, "web")
	if cfg.Target.SSH.Host != "198.51.100.9" || st.Pending != nil || st.Stage != stageApplied {
		t.Errorf("config host %q, state %+v", cfg.Target.SSH.Host, st)
	}
	if _, err := os.Stat(cloudConfigPath("web")); err == nil {
		t.Error("cloud-config with the single-use host key kept after rotation")
	}
}

func TestUpCloudInitInteractiveWithoutHostKey(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	r := &fakeRunner{}
	in := script("4", "web", "n", "", "", "", "", "203.0.113.20", "n") // Remote Control: no, after the sign-in
	code, out := upWith(t, r, in, "up")
	if code != 0 || !saysSetUp(out, "web") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Contains(out, "ed25519_private") {
		t.Error("host key in the snippet after declining it")
	}
	if len(r.hostKeys) != 1 || r.hostKeys[0] != "" {
		t.Errorf("host keys %q", r.hostKeys)
	}
}

func TestUpHetznerResumesAfterAFailedCreate(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	api, store := fakeHetzner(t)
	_ = store.Set(context.Background(), "hetzner", goodToken)
	r := &fakeRunner{createCode: 1, onCreate: func(o options) { o.hooks.server("192.0.2.44") }}
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "hetzner", "--location", "ash", "--size", "cpx02", "--ssh-key", key, "--yes")
	if code != 1 || !strings.Contains(out, "keelbox up acme") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.Hetzner.Location != "ash" || cfg.Target.Hetzner.ServerType != "cpx02" || st.Stage != stageServer || st.Address != "192.0.2.44" {
		t.Fatalf("config %+v state %+v", cfg.Target.Hetzner, st)
	}
	if len(api.Keys) != 1 {
		t.Errorf("keys %+v", api.Keys)
	}
	r.calls, r.rootReachable = nil, true
	code, out = upWith(t, r, nil, "up", "acme")
	if code != exitNeedsYou || strings.Join(r.calls, "; ") != "reachable 192.0.2.44 root; apply acme as root; signed-in?" {
		t.Errorf("resume: %d %v\n%s", code, r.calls, out)
	}
	// The same key is reused, not uploaded again, for a second box.
	_, _ = upWith(t, &fakeRunner{signed: true}, nil, "up", "acme2", "--provider", "hetzner", "--ssh-key", key, "--yes")
	if len(api.Keys) != 1 {
		t.Errorf("key uploaded twice: %+v", api.Keys)
	}
}

func TestUpHetznerRejectsUnorderableSize(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	_, store := fakeHetzner(t)
	_ = store.Set(context.Background(), "hetzner", goodToken)
	code, out := upWith(t, &fakeRunner{}, nil, "up", "acme", "--provider", "hetzner", "--size", "cpx11", "--yes")
	if code != 2 || !strings.Contains(out, "cannot be ordered in fsn1") || !strings.Contains(out, "cx23") {
		t.Errorf("%d %s", code, out)
	}
}

func TestUpLocalUsesAuthorizedKeys(t *testing.T) {
	home := isolate(t)
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	keys := "# comment\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop\nfrom=\"10.0.0.1\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOther x\n"
	_ = os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte(keys), 0o600)
	r := &fakeRunner{signed: true}
	code, out := upWith(t, r, nil, "up", "home", "--provider", "local", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, _ := loadBox(t, "home")
	if len(cfg.Users.Admin.SSHAuthorizedKeys) != 1 || !strings.HasSuffix(cfg.Users.Admin.SSHAuthorizedKeys[0], "you@laptop") {
		t.Errorf("keys %v", cfg.Users.Admin.SSHAuthorizedKeys)
	}
	// No second apply on the machine itself: closing public SSH needs an
	// apply that arrives over the tailnet.
	if got := strings.Join(r.calls, "; "); got != "apply home as admin; signed-in?" || strings.Join(r.looks, "; ") != "sudo; inspect here; impact home here" {
		t.Errorf("calls: %s; looked: %v", got, r.looks)
	}
}

func TestUpDeclinedChangesNothing(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	r := &fakeRunner{}
	// where, address, account, host key, name (the hostname), agents,
	// gh, Tailscale, LAN SSH, go ahead: no.
	code, out := upWith(t, r, script("1", "10.0.0.5", "1", "", "", "", "", "", "", "n"), "up")
	if code != 0 || !strings.Contains(out, "keelbox up: nothing changed. Your answers are kept; continue: keelbox up lab") || isRegistered("lab") || len(r.calls) != 0 {
		t.Errorf("%d %v\n%s", code, r.calls, out)
	}
	if d, ok := loadDraft(); !ok || d.Box != "lab" {
		t.Errorf("answers not kept after no: %+v", d)
	}
	// Carrying on asks only the go-ahead again.
	r = &fakeRunner{}
	code, out = upWith(t, r, script("", "", ""), "up") // carry on, go ahead, Remote Control: no
	if code != 0 || !strings.Contains(out, "You started setting up lab, an existing server at 10.0.0.5, as root. Carry on with those answers?") ||
		strings.Contains(out, "Which agents?") || !strings.HasPrefix(strings.Join(r.calls, "; "), "apply lab as root") {
		t.Errorf("carry on: %d %v\n%s", code, r.calls, out)
	}
}

func TestUpNoSSHKey(t *testing.T) {
	isolate(t)
	code, out := upWith(t, &fakeRunner{}, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--yes")
	if code != 2 || !strings.Contains(out, "ssh-keygen -t ed25519") || !strings.Contains(out, "--ssh-key") {
		t.Errorf("%d %s", code, out)
	}
	// The example repeats the flags given, so it runs as printed.
	if !strings.Contains(out, "for example: keelbox up lab --provider ssh --host 10.0.0.5 --ssh-key ~/.ssh/id_ed25519 --yes\n") {
		t.Errorf("the example drops the flags given:\n%s", out)
	}
}

func TestUpHelpMentionsUp(t *testing.T) {
	if !strings.Contains(overview(), "up [box]") {
		t.Error("help lacks up")
	}
}

// keelbox up on this machine, a home server: it shows what the firewall
// would block and keeps the chosen ports open to the LAN.
func TestUpThisMachineKeepsChosenPortsOpen(t *testing.T) {
	home := isolate(t)
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop\n"), 0o600)
	r := &fakeRunner{signed: true, probe: homeServer}
	in := script(
		"2",    // where: this machine
		"",     // name: the hostname, nas
		"",     // agents: both
		"",     // GitHub CLI
		"",     // Tailscale: yes
		"",     // keep Plex: yes
		"",     // keep Samba: yes
		"80",   // not a listed port: asked again
		"none", // other LAN ports
		"",     // LAN keeps SSH: yes
		"",     // limit the agent: yes
		"",     // go ahead
		"n",    // Remote Control, once Claude Code is signed in
	)
	code, out := upWith(t, r, in, "up")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"Listening on this machine now; the firewall will block these from the network:",
		"  plex       Plex Media Server: 32400/tcp", "  samba      Samba file sharing (smbd): 445/tcp, 137/udp", "  8096/tcp   container jellyfin (Docker)",
		"  {service: samba} opens 445, 139/tcp; 137, 138/udp; 137, 138/udp are how devices and apps find it on the LAN.",
		"Keep Samba file sharing reachable on your LAN? [Y/n]",
		"Answer all, none, or port numbers such as 53.", "Other ports to keep open on the LAN [all]: ",
		"80 is not one of those listed",
		"this machine (nas, Ubuntu 24.04), with sudo", "open to the LAN: plex (32400, 8324, 32469/tcp; 1900, 5353, 32410, 32412-32414/udp), samba (445, 139/tcp; 137, 138/udp)",
		"SSH takes keys only: password and root logins stop.",
		"Blocked from the network: 53/tcp, 3000/tcp, 8096/tcp, 9100/tcp.",
		"Your account sam keeps its password and sudo.",
		"No keelbox command undoes this.",
		"Other services run here (Plex Media Server, Samba file sharing, 4 other services). Agents can take all the CPU and 90% of the memory unless limited further (users.agent.limits in the config).",
		"Limit the agent to 75% of CPU and memory, so the other services keep the rest? [Y/n]",
		"limits      the agent gets at most 75% of CPU and memory",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	cfg, _ := loadBox(t, "nas")
	if a := cfg.Host.Firewall.Allow; len(a) != 2 || a[0].Service != "plex" || a[1].Service != "samba" {
		t.Errorf("allow %+v", a)
	}
	if l, ok := cfg.AgentLimits(); !ok || l.CPUPercent != 75 || l.MemoryPercent != 75 {
		t.Errorf("limits %+v %v", l, ok)
	}
	if cfg.Host.Timezone != "Europe/London" || cfg.Host.Locale != "en_GB.UTF-8" || !cfg.LockCloudUser() {
		t.Errorf("host %+v lock %v", cfg.Host, cfg.LockCloudUser())
	}
}

func TestUpThisMachineWithoutATerminal(t *testing.T) {
	home := isolate(t)
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop\n"), 0o600)
	// Listening services and no answer for them: stop before any change.
	r := &fakeRunner{probe: homeServer}
	code, out := upWith(t, r, nil, "up", "nas", "--provider", "local", "--yes")
	if code != 2 || !strings.Contains(out, "the firewall would block plex, samba, 53/tcp, 3000/tcp, 8096/tcp, 9100/tcp") || !strings.Contains(out, "keelbox up nas --lan-ports all") || len(r.calls) != 0 {
		t.Errorf("%d %v\n%s", code, r.calls, out)
	}
	code, out = upWith(t, r, nil, "up", "nas", "--provider", "local", "--yes", "--lan-ports", "8096")
	if code != exitNeedsYou {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "nas"); len(cfg.Host.Firewall.Allow) != 1 || cfg.Host.Firewall.Allow[0].Name != "jellyfin" {
		t.Errorf("allow %+v", cfg.Host.Firewall.Allow)
	}
	// sudo with a password and no terminal: the exact command to run instead.
	isolate(t)
	r = &fakeRunner{noSudo: true}
	code, out = upWith(t, r, nil, "up", "lab", "--provider", "local", "--yes")
	if code != 2 || !strings.Contains(out, `sudo "$(command -v keelbox)" up lab --provider local --yes`) {
		t.Errorf("no sudo: %d\n%s", code, out)
	}
	// Not Ubuntu: said before anything else is asked.
	r = &fakeRunner{probe: "os=fedora 41\nsudo=yes\n"}
	code, out = upWith(t, r, nil, "up", "lab", "--provider", "local", "--yes")
	if code != 2 || !strings.Contains(out, "this is fedora 41") {
		t.Errorf("fedora: %d\n%s", code, out)
	}
}

// A desktop gets a note; the account cloud-init made, which up runs from
// here, is not locked.
func TestUpThisMachineDesktop(t *testing.T) {
	home := isolate(t)
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop\n"), 0o600)
	r := &fakeRunner{probe: "os=ubuntu 24.04\nhostname=desk\nuser=sam\nsudo=yes\ncloud_users=sam \ndisplay_manager=gdm3\n"}
	code, out := upWith(t, r, nil, "up", "desk", "--provider", "local", "--yes")
	if code != exitNeedsYou || !strings.Contains(out, "This looks like a desktop (a display manager (gdm3) is running)") {
		t.Errorf("%d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "desk"); cfg.LockCloudUser() {
		t.Error("would lock the user's own account")
	}
}

// Over SSH, keelbox looks before it changes anything. sudo with a password
// needs a terminal, where sudo asks for it itself; never a sudoers line.
// An account without sudo stops with what to do.
func TestUpSSHPasswordSudo(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{probe: "os=ubuntu 24.04\nuser=sam\nsudo=password\n"}
	code, out := upWith(t, r, nil, "up", "nas", "--provider", "ssh", "--host", "192.168.1.20", "--ssh-user", "sam", "--ssh-key", key, "--yes")
	if code != exitNeedsYou || !strings.Contains(out, "sam's sudo on 192.168.1.20 asks for a password, which sudo reads on a terminal. Run keelbox up on one: keelbox up nas") || strings.Contains(out, "NOPASSWD") {
		t.Errorf("%d\n%s", code, out)
	}
	if len(r.calls) != 0 || isRegistered("nas") {
		t.Errorf("changed something: %v", r.calls)
	}
	r = &fakeRunner{probe: "os=ubuntu 24.04\nuser=sam\nsudo=no\n"}
	code, out = upWith(t, r, nil, "up", "nas", "--provider", "ssh", "--host", "192.168.1.20", "--ssh-user", "sam", "--ssh-key", key, "--yes")
	if code != 2 || !strings.Contains(out, "sam cannot use sudo on 192.168.1.20") || !strings.Contains(out, "sudo usermod -aG sudo sam") {
		t.Errorf("no sudo: %d\n%s", code, out)
	}

	// On a terminal: the read-only look and the first apply ask sudo on
	// it, as sam; the admin's applies do not.
	isolate(t)
	key = laptopKey(t, mustHomeT(t))
	r = &fakeRunner{probe: "os=ubuntu 24.04\nhostname=nas\nuser=sam\nsudo=password\n"}
	args := []string{"up", "nas", "--provider", "ssh", "--host", "192.168.1.20", "--ssh-user", "sam", "--ssh-key", key, "--agents", "none", "--gh=false", "--tailscale=false", "--skip-sign-in"}
	code, out = upWith(t, r, script("", ""), args...) // host key, go ahead
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"ok: nas, Ubuntu 24.04, sudo with sam's password", "sudo reads it on this terminal itself; keelbox never sees or keeps it"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if got := strings.Join(r.looks, "; "); !strings.Contains(got, "impact nas as sam (sudo asks)") {
		t.Errorf("looks: %s", got)
	}
	if got := strings.Join(r.calls, "; "); got != "apply nas as sam (sudo asks); signed-in?; apply nas as admin" {
		t.Errorf("calls: %s", got)
	}
	if _, st := loadBox(t, "nas"); !st.SudoPassword {
		t.Error("not kept for a resumed first apply")
	}
	// A server that works keeps its clock and language.
	isolate(t)
	key = laptopKey(t, mustHomeT(t))
	r = &fakeRunner{probe: "os=ubuntu 24.04\nuser=sam\nsudo=yes\ntimezone=Europe/Berlin\nlocale=de_DE.UTF-8\n"}
	if code, out := upWith(t, r, nil, "up", "nas", "--provider", "ssh", "--host", "192.168.1.20", "--ssh-user", "sam", "--ssh-key", key, "--yes"); code != exitNeedsYou {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "nas"); cfg.Host.Timezone != "Europe/Berlin" || cfg.Host.Locale != "de_DE.UTF-8" {
		t.Errorf("host %+v", cfg.Host)
	}
	isolate(t)
	key = laptopKey(t, mustHomeT(t))
	r = &fakeRunner{inspectErr: errors.New("192.168.1.20 does not accept your SSH key")}
	if code, out := upWith(t, r, nil, "up", "nas", "--provider", "ssh", "--host", "192.168.1.20", "--ssh-key", key, "--yes"); code != 2 || !strings.Contains(out, "does not accept your SSH key") {
		t.Errorf("unreachable: %d\n%s", code, out)
	}
}

// Ctrl-C at a question ends keelbox up with nothing changed; Ctrl-C
// after the questions stops between steps and says how to continue.
func TestUpInterrupted(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	pr, pw := io.Pipe()
	defer pw.Close()
	o, pos, _ := parse("up", nil, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan int)
	go func() {
		done <- cmdUp(ctx, o, pos, prompt.New(pr, &out, true), &fakeRunner{}, &out, &out)
	}()
	cancel()
	if code := <-done; code != exitInterrupted || !strings.Contains(out.String(), "stopped; nothing was changed. Start again: keelbox up") {
		t.Errorf("at a question: %d\n%s", code, out.String())
	}

	ctx, cancel = context.WithCancel(context.Background())
	r := &fakeRunner{applyCode: exitInterrupted}
	r.onApply = cancel
	o, pos, _ = parse("up", []string{"lab", "--provider", "ssh", "--host", "10.0.0.5", "--yes"}, io.Discard)
	out.Reset()
	code := cmdUp(ctx, o, pos, prompt.New(strings.NewReader(""), &out, false), r, &out, &out)
	if code != exitInterrupted || !strings.Contains(out.String(), "Continue: keelbox up lab") {
		t.Errorf("during apply: %d\n%s", code, out.String())
	}
	if _, st := loadBox(t, "lab"); st.Stage != stageConfigured {
		t.Errorf("stage %s", st.Stage)
	}
}

// partWay leaves lab as a first apply over SSH that failed leaves it.
func partWay(t *testing.T, home string) {
	t.Helper()
	key := laptopKey(t, home)
	if code, _ := upWith(t, &fakeRunner{applyCode: 1}, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-key", key, "--yes"); code != 1 {
		t.Fatal("first apply should fail")
	}
}

// waiting leaves web as a printed cloud-config leaves it: no server yet.
func waiting(t *testing.T) {
	t.Helper()
	if code, out := upWith(t, &fakeRunner{}, nil, "up", "web", "--provider", "cloud-init", "--yes"); code != exitNeedsYou {
		t.Fatalf("cloud-config: exit %d\n%s", code, out)
	}
}

// keelbox up with no name and one box left part way asks in one menu:
// continue it (the default) or set up another. The box has a server, so
// the menu names the command that removes it instead of offering to.
func TestUpUnfinishedBoxMenu(t *testing.T) {
	partWay(t, isolate(t))
	const menu = `lab is not finished. Continue it or set up another box?
  1) Continue lab        stopped part-way
  2) Set up another box  lab stays; remove it: keelbox forget lab
Choose 1-2 [1]: `

	r := &fakeRunner{}
	code, out := upWith(t, r, script("2"), "up")
	if !strings.Contains(out, menu) {
		t.Errorf("menu:\n%s", out)
	}
	if !strings.Contains(out, "Where should the box run?") || strings.Contains(out, "continuing lab") || len(r.calls) != 0 {
		t.Errorf("another box: %d %v\n%s", code, r.calls, out)
	}
	if _, st := loadBox(t, "lab"); st.Stage != stageConfigured || !st.Applying {
		t.Errorf("lab changed by setting up another: %+v", st)
	}

	// No third entry: the answer is asked again.
	_, out = upWith(t, &fakeRunner{}, script("3", "2"), "up")
	if strings.Contains(out, "Discard") || !strings.Contains(out, "type a number from 1 to 2") {
		t.Errorf("discard offered for a box with a server:\n%s", out)
	}

	r = &fakeRunner{signed: true}
	code, out = upWith(t, r, script("", ""), "up")
	if code != 0 || !strings.Contains(out, menu) || !strings.Contains(out, "continuing lab (stopped part-way)") || len(r.calls) == 0 || r.calls[0] != "apply lab as root" {
		t.Errorf("continue: %d %v\n%s", code, r.calls, out)
	}
}

// A box with no server yet can be discarded from the menu: keelbox forget,
// which asks first even under --yes and calls nothing.
func TestUpUnfinishedBoxDiscard(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	waiting(t)
	const menu = `web is not finished. Continue it or set up another box?
  1) Continue web        waiting for the server
  2) Set up another box  web stays as it is
  3) Discard web         no server yet; the same as: keelbox forget web
Choose 1-3 [1]: `

	r := &fakeRunner{}
	code, out := upWith(t, r, script("3", ""), "up", "--yes")
	if code != 1 || !strings.Contains(out, menu) || !strings.Contains(out, "Forget web? [y/N]") || !strings.Contains(out, "Nothing changed.") || !isKnown("web") {
		t.Errorf("discard declined: %d\n%s", code, out)
	}
	if _, err := os.Stat(cloudConfigPath("web")); err != nil {
		t.Errorf("cloud-config removed without a yes: %v", err)
	}

	code, out = upWith(t, r, script("1"), "up")
	if !strings.Contains(out, "#cloud-config") || !strings.Contains(out, "The server's IP address, once it is running: ") {
		t.Errorf("continue: %d\n%s", code, out)
	}

	code, out = upWith(t, r, script("3", "y"), "up")
	if code != 0 || !strings.Contains(out, "Forgot web. A server you created from its cloud-config is not touched.") || strings.Contains(out, "Where should the box run?") {
		t.Errorf("discard: %d\n%s", code, out)
	}
	if isKnown("web") || len(r.calls) != 0 {
		t.Errorf("after discard: known %v, calls %v", isKnown("web"), r.calls)
	}
	if _, err := os.Stat(cloudConfigPath("web")); err == nil {
		t.Error("cloud-config kept after discard")
	}
	// Nothing is left to ask about.
	if _, out = upWith(t, r, script(), "up"); strings.Contains(out, "not finished") || !strings.Contains(out, "Where should the box run?") {
		t.Errorf("asked about a discarded box:\n%s", out)
	}
}

// A cloud box is never discarded from the menu, whatever up recorded: the
// menu names destroy and forget, and runs neither.
func TestUpUnfinishedCloudBoxMenu(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   upState
		left string
	}{
		{"not created", upState{Stage: stageConfigured}, "not created"},
		{"create started", upState{Stage: stageConfigured, Applying: true}, "stopped part-way"},
		{"server exists", upState{Stage: stageServer, Applying: true, Address: "203.0.113.7"}, "stopped part-way"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			register(t, config.StarterFor("lab", "hetzner"))
			tc.st.Via = viaHetzner
			if err := saveUpState("lab", tc.st); err != nil {
				t.Fatal(err)
			}
			r := &fakeRunner{}
			_, out := upWith(t, r, script("3", "2"), "up")
			const hint = "  2) Set up another box  lab stays; remove it: keelbox forget lab (first keelbox destroy lab, to delete a server keelbox created)\nChoose 1-2 [1]: "
			if !strings.Contains(out, "  1) Continue lab        "+tc.left+"\n"+hint) || strings.Contains(out, "Discard") || !strings.Contains(out, "type a number from 1 to 2") {
				t.Errorf("menu:\n%s", out)
			}
			if _, st := loadBox(t, "lab"); len(r.calls) != 0 || st.Stage != tc.st.Stage {
				t.Errorf("calls %v, state %+v", r.calls, st)
			}
		})
	}
}

// With several boxes left part way the menu lists each, the default being
// the one touched last, and has no discard entry.
func TestUpUnfinishedBoxesMenu(t *testing.T) {
	partWay(t, isolate(t))
	waiting(t)
	const list = `2 boxes are not finished. Continue one or set up another?
  1) Continue lab        stopped part-way
  2) Continue web        waiting for the server
  3) Set up another box  they stay; remove one: keelbox forget NAME
`

	r := &fakeRunner{}
	code, out := upWith(t, r, script("3"), "up")
	if !strings.Contains(out, list+"Choose 1-3 [2]: ") || strings.Contains(out, "Discard") {
		t.Errorf("menu:\n%s", out)
	}
	if !strings.Contains(out, "Where should the box run?") || strings.Contains(out, "continuing") || len(r.calls) != 0 {
		t.Errorf("another box: %d %v\n%s", code, r.calls, out)
	}

	// Enter takes web, touched last.
	if _, out = upWith(t, r, script(""), "up"); !strings.Contains(out, "continuing web (waiting for the server)") {
		t.Errorf("default:\n%s", out)
	}
	_, st := loadBox(t, "lab")
	if err := saveUpState("lab", st); err != nil {
		t.Fatal(err)
	}
	if _, out = upWith(t, r, script("3"), "up"); !strings.Contains(out, list+"Choose 1-3 [1]: ") {
		t.Errorf("default after lab was touched:\n%s", out)
	}

	r = &fakeRunner{signed: true}
	code, out = upWith(t, r, script("lab", ""), "up")
	if code != 0 || !strings.Contains(out, "continuing lab (stopped part-way)") || len(r.calls) == 0 || r.calls[0] != "apply lab as root" {
		t.Errorf("pick one: %d %v\n%s", code, r.calls, out)
	}
}

// Without a terminal, or with --provider, up asks nothing about the boxes
// left part way and sets up the one the flags describe.
func TestUpUnfinishedNotOffered(t *testing.T) {
	partWay(t, isolate(t))
	r := &fakeRunner{}
	code, out := upWith(t, r, nil, "up")
	if code != 2 || !strings.Contains(out, "where the box runs: no terminal to ask on") || strings.Contains(out, "not finished") || strings.Contains(out, "Continue lab") {
		t.Errorf("no terminal: %d\n%s", code, out)
	}
	code, out = upWith(t, r, script(), "up", "--provider", "ssh")
	if strings.Contains(out, "not finished") || strings.Contains(out, "Continue lab") || !strings.Contains(out, "Server's IP address or DNS name") {
		t.Errorf("--provider: %d\n%s", code, out)
	}
	if len(r.calls) != 0 {
		t.Errorf("calls: %v", r.calls)
	}
}

func TestLockBox(t *testing.T) {
	isolate(t)
	unlock, err := lockBox("lab", "keelbox up lab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockBox("lab", "keelbox up lab"); err == nil || !strings.Contains(err.Error(), "another keelbox command is working on lab; nothing changed. Wait for it to finish, then: keelbox up lab") {
		t.Errorf("second lock: %v", err)
	}
	unlock()
	if u, err := lockBox("lab", "keelbox up lab"); err != nil {
		t.Error(err)
	} else {
		u()
	}
}

func TestUpWrongKeyNamesTheOnesThere(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	r := &fakeRunner{}
	code, out := upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-key", "~/.ssh/id_work", "--yes")
	if code != 2 || !strings.Contains(out, "key pairs in ~/.ssh: ~/.ssh/id_ed25519; name one: --ssh-key ~/.ssh/id_ed25519") {
		t.Errorf("%d\n%s", code, out)
	}
}

func mustHomeT(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// keelbox up asks for the identity the agent's commits carry, offering the
// laptop's; it can be typed or skipped, and without a terminal only the
// flags set it (ADR 0064).
func TestUpGitIdentity(t *testing.T) {
	const base = "--provider ssh --host 203.0.113.7 --ssh-user root --host-key= --agents none --gh=false --tailscale=false --skip-sign-in"
	run := func(t *testing.T, input *string, extra ...string) (string, *config.Config) {
		t.Helper()
		home := isolate(t)
		key := laptopKey(t, home)
		args := append([]string{"up", "lab", "--ssh-key", key}, strings.Fields(base)...)
		code, out := upWith(t, &fakeRunner{}, input, append(args, extra...)...)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		cfg, _ := loadBox(t, "lab")
		return out, cfg
	}
	t.Run("the laptop's", func(t *testing.T) {
		withLaptopGit(t, "Ada Lovelace", "ada@example.com")
		out, cfg := run(t, script("", "")) // the offered identity, go ahead
		if n, e, ok := cfg.AgentGit(); !ok || n != "Ada Lovelace" || e != "ada@example.com" {
			t.Errorf("identity %q %q %v\n%s", n, e, ok, out)
		}
		for _, want := range []string{"Commit as Ada Lovelace <ada@example.com>, as git does on this computer? [Y/n]", "the agent commits as Ada Lovelace <ada@example.com>"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("another", func(t *testing.T) {
		withLaptopGit(t, "Ada Lovelace", "ada@example.com")
		_, cfg := run(t, script("n", "Box Agent", "not-an-address", "agent@example.com", ""))
		if n, e, _ := cfg.AgentGit(); n != "Box Agent" || e != "agent@example.com" {
			t.Errorf("identity %q %q", n, e)
		}
	})
	t.Run("skipped", func(t *testing.T) {
		withLaptopGit(t, "", "")
		out, cfg := run(t, script("", "")) // Enter skips the name, go ahead
		if _, _, ok := cfg.AgentGit(); ok || !strings.Contains(out, "Name on the agent's commits [Enter: skip]") || strings.Contains(out, "Email on the agent's commits") {
			t.Errorf("skipped: %v\n%s", ok, out)
		}
	})
	t.Run("flags, no terminal", func(t *testing.T) {
		withLaptopGit(t, "Ada Lovelace", "ada@example.com")
		out, cfg := run(t, nil, "--yes", "--git-name", "CI Bot", "--git-email", "ci@example.com")
		if n, e, _ := cfg.AgentGit(); n != "CI Bot" || e != "ci@example.com" {
			t.Errorf("identity %q %q", n, e)
		}
		if strings.Contains(out, "refuses to commit") || strings.Contains(out, "Left to do") {
			t.Errorf("an identity that is set is reported as missing:\n%s", out)
		}
	})
	t.Run("no terminal, no flags", func(t *testing.T) {
		// The laptop's identity is not taken without a question.
		withLaptopGit(t, "Ada Lovelace", "ada@example.com")
		out, cfg := run(t, nil, "--yes")
		if cfg.Users.Agent.Git != nil {
			t.Errorf("an identity nobody gave: %+v", cfg.Users.Agent.Git)
		}
		// The run says so twice: before it starts, with the flags, and on
		// the end, with what to add to the config that now exists.
		flat := strings.Join(strings.Fields(out), " ")
		for _, want := range []string{
			`git none: without a name and email git on the box refuses to commit. Set them with --git-name "Your Name" --git-email you@example.com, or later under users.agent.git in the config`,
			"Left to do git identity: none in the config, and without one git on lab refuses to commit. " +
				"Add users.agent.git: {name: Your Name, email: you@example.com} to ~/.config/keelbox/boxes/lab.yaml, then keelbox apply lab",
		} {
			if !strings.Contains(flat, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("half", func(t *testing.T) {
		withLaptopGit(t, "", "")
		home := isolate(t)
		key := laptopKey(t, home)
		args := append([]string{"up", "lab", "--ssh-key", key, "--yes", "--git-name", "CI Bot"}, strings.Fields(base)...)
		code, out := upWith(t, &fakeRunner{}, nil, args...)
		if code == 0 || !strings.Contains(out, "--git-name and --git-email go together") || isRegistered("lab") {
			t.Errorf("exit %d\n%s", code, out)
		}
	})
}

// The "Which cloud?" question says what each cloud needs on this computer
// and whether it is there, and takes its default from that.
func TestUpWhichCloud(t *testing.T) {
	isolate(t)
	_, store := fakeHetzner(t)
	defer func(a, d, g func() bool) { awsInstalled, doInstalled, gcpInstalled = a, d, g }(awsInstalled, doInstalled, gcpInstalled)
	ctx := context.Background()
	for _, c := range []struct {
		name            string
		aws, do, gcp    bool
		token           bool
		hcloud          string
		want            string
		hetzner, others string
	}{
		{"nothing here", false, false, false, false, "", viaHetzner, "needs an API token", "needs the AWS CLI|needs doctl|needs gcloud"},
		{"only doctl", false, true, false, false, "", viaDO, "needs an API token", "needs the AWS CLI|uses your doctl sign-in|needs gcloud"},
		{"only the AWS CLI", true, false, false, false, "", viaAWS, "needs an API token", "uses your AWS CLI sign-in|needs doctl|needs gcloud"},
		{"only gcloud", false, false, true, false, "", viaGCP, "needs an API token", "needs the AWS CLI|needs doctl|uses your gcloud sign-in"},
		{"two CLIs", true, true, false, false, "", viaHetzner, "needs an API token", "uses your AWS CLI sign-in|uses your doctl sign-in|needs gcloud"},
		{"a stored token and doctl", false, true, false, true, "", viaHetzner, "token stored", "needs the AWS CLI|uses your doctl sign-in|needs gcloud"},
		{"hcloud signed in", false, false, false, false, "work", viaHetzner, "hcloud CLI signed in", "needs the AWS CLI|needs doctl|needs gcloud"},
	} {
		awsInstalled, doInstalled, gcpInstalled = func() bool { return c.aws }, func() bool { return c.do }, func() bool { return c.gcp }
		hcloudActiveContext = func(context.Context) string { return c.hcloud }
		_ = store.Delete(ctx, "hetzner")
		if c.token {
			if err := store.Set(ctx, "hetzner", goodToken); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		w := &wizard{p: prompt.New(strings.NewReader("\n"), &out, true), out: &out}
		via, err := w.askCloud(ctx)
		if err != nil || via != c.want {
			t.Errorf("%s: Enter picks %q %v, want %s\n%s", c.name, via, err, c.want, out.String())
		}
		others := strings.Split(c.others, "|")
		for _, want := range []string{"Which cloud?", "1) Hetzner", c.hetzner, "2) AWS (Lightsail)", others[0], "3) DigitalOcean", others[1], "4) Google Cloud", others[2]} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%s: the question lacks %q:\n%s", c.name, want, out.String())
			}
		}
		if strings.Contains(out.String(), goodToken) {
			t.Errorf("%s: the token was shown", c.name)
		}
	}

	// A cloud whose CLI is missing can be picked, and says how to get it.
	awsInstalled = func() bool { return false }
	code, out := upWith(t, &fakeRunner{}, script("3", "aws", "acme"), "up")
	if code != exitNeedsYou || !strings.Contains(out, aws.InstallURL) || !strings.Contains(out, "keelbox up acme") {
		t.Errorf("exit %d\n%s", code, out)
	}
}
