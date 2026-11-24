package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"github.com/keelbox/keelbox/internal/provider"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/ui"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestInit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "acme.yaml")
	if code, out, errs := run("init", p, "--box", "acme"); code != 0 || !strings.Contains(out, "Wrote") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	c, err := config.Load(p)
	if err != nil || c.Box != "acme" {
		t.Fatalf("init output invalid: %v", err)
	}
	if code, _, errs := run("init", p); code != 1 || !strings.Contains(errs, "--force") {
		t.Errorf("overwrite allowed: %d %s", code, errs)
	}
	if code, _, _ := run("init", "--force", p); code != 0 {
		t.Error("--force failed")
	}
	if code, _, errs := run("init", filepath.Join(t.TempDir(), "x.yaml"), "--box", "Bad_Box"); code != 2 || !strings.Contains(errs, "box:") {
		t.Errorf("bad box accepted: %s", errs)
	}
}

func TestStartScreenHelpAndUnknown(t *testing.T) {
	isolate(t)
	code, out, _ := run()
	if code != 0 || !strings.Contains(out, "keelbox up") || !strings.Contains(out, "keelbox ls") || !strings.Contains(out, docsURL) {
		t.Errorf("start screen: %d\n%s", code, out)
	}
	if n := strings.Count(out, "\n"); n > 12 {
		t.Errorf("start screen is %d lines; keep it short:\n%s", n, out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("colour on a buffer:\n%q", out)
	}
	if code, out, _ := run("help"); code != 0 || !strings.Contains(out, "apply [box]") || !strings.Contains(out, "--help") {
		t.Error("help")
	} else if n := strings.Count(out, "keelbox: sets up Ubuntu machines for AI coding agents.\n"); n != 1 {
		t.Errorf("help says what keelbox is %d times, want once:\n%s", n, out)
	}
	// One sentence says what keelbox is, wherever it is said.
	if !strings.Contains(out, "\n"+ui.Tagline+"\n") {
		t.Errorf("the start screen says what keelbox is in other words than %q:\n%s", ui.Tagline, out)
	}
	for _, args := range [][]string{{"up", "--help"}, {"help", "up"}, {"up", "lab", "-h"}} {
		if code, out, _ := run(args...); code != 0 || !strings.HasPrefix(out, "keelbox up [box]:") || !strings.Contains(out, "Examples:") || !strings.Contains(out, "--provider WHERE") {
			t.Errorf("%v: %d\n%s", args, code, out)
		}
	}
	if code, _, errs := run("frobnicate"); code != 2 || !strings.Contains(errs, "unknown command") || strings.Count(errs, "\n") > 1 {
		t.Errorf("unknown command: %s", errs)
	}
	if _, _, errs := run("aply"); !strings.Contains(errs, "Did you mean keelbox apply?") {
		t.Errorf("no suggestion: %s", errs)
	}
	// A flag keelbox does not know: what is wrong in its own words, with
	// two dashes, and the command that lists the flags. Nothing of the
	// flag package's text.
	for args, want := range map[string]string{
		"apply --nope":        "keelbox: apply has no flag --nope; list its flags: keelbox help apply\n",
		"apply -frob":         "keelbox: apply has no flag --frob; list its flags: keelbox help apply\n",
		"apply --adpot":       "keelbox: apply has no flag --adpot; did you mean --adopt? List its flags: keelbox help apply\n",
		"up web --size":       "keelbox: --size needs a value; see what it takes: keelbox help up\n",
		"forget web --yes=si": "keelbox: --yes is on or off and takes no value like \"si\"; write --yes or --yes=false. List the flags: keelbox help forget\n",
	} {
		if code, _, errs := run(strings.Fields(args)...); code != 2 || errs != want {
			t.Errorf("keelbox %s: exit %d\n got %q\nwant %q", args, code, errs, want)
		}
	}
	for _, v := range []string{"version", "-v", "--version", "--v"} {
		if code, out, _ := run(v); code != 0 || !strings.Contains(out, "release ") {
			t.Errorf("%s: %s", v, out)
		}
	}
}

// With boxes in the registry, the start screen lists them with how far
// each is set up.
func TestStartScreenListsBoxes(t *testing.T) {
	isolate(t)
	register(t, config.Starter{Box: "devbox", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}})
	if err := saveUpState("devbox", upState{Via: viaLocal, Stage: stageApplied}); err != nil {
		t.Fatal(err)
	}
	_, out, _ := run()
	if !strings.Contains(out, "Your boxes:") || !strings.Contains(out, "devbox") || !strings.Contains(out, "needs sign-in: keelbox up devbox") {
		t.Errorf("start screen:\n%s", out)
	}
}

func TestConfigErrorsExit2(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.yaml")
	_ = os.WriteFile(p, []byte("version: 1\nbox: x\nbogus: 1\n"), 0o644)
	code, _, errs := run("plan", p)
	if code != 2 || !strings.Contains(errs, `unknown field "bogus"`) {
		t.Errorf("%d %s", code, errs)
	}
	if code, _, errs := run("plan", p, p); code != 2 || !strings.Contains(errs, "one box name or config file") {
		t.Errorf("two configs: %s", errs)
	}
}

func TestCreateNeedsACloudProvider(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.yaml")
	if code, _, _ := run("init", p); code != 0 {
		t.Fatal("init")
	}
	if code, _, errs := run("create", p); code != 2 || !strings.Contains(errs, "provider hetzner") {
		t.Errorf("%d %s", code, errs)
	}
	if code, _, errs := run("destroy", p); code != 2 || !strings.Contains(errs, "Hetzner, AWS, DigitalOcean or Google Cloud server") || !strings.Contains(errs, "keelbox forget my-box") {
		t.Errorf("%d %s", code, errs)
	}
}

// A box-local command run as a user runs again as root, with the config
// and flags it was given, instead of failing.
func TestLocalRunsAgainWithSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	p := filepath.Join(t.TempDir(), "k.yaml")
	run("init", p)
	sudoCalls = nil
	if code, _, errs := run("status", p, "--verbose"); code != 0 || len(sudoCalls) != 1 {
		t.Fatalf("%d %s %v", code, errs, sudoCalls)
	}
	got := strings.Join(sudoCalls[0], " ")
	if !strings.HasPrefix(got, "-n env KEELBOX_CHILD=1 ") || !strings.HasSuffix(got, " status "+p+" --verbose=true") {
		t.Errorf("sudo %s", got)
	}
}

func TestFlagsAnywhere(t *testing.T) {
	o, pos, err := parse("apply", []string{"--verbose", "a.yaml", "--ssh-user", "root", "--yes"}, &bytes.Buffer{})
	if err != nil || !o.verbose || !o.yes || o.sshUser != "root" || len(pos) != 1 || pos[0] != "a.yaml" {
		t.Errorf("%+v %v %v", o, pos, err)
	}
}

// A flag only another command takes is an error that names the command's
// own flags, where it was read and did nothing. --yes and --verbose pass
// everywhere.
func TestAFlagOfAnotherCommandIsRefused(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		args []string
		want string
	}{
		{"park", []string{"acme", "--discard-work"}, "park has no flag --discard-work; list its flags: keelbox help park"},
		{"ls", []string{"--size", "cx22"}, "ls has no flag --size; list its flags: keelbox help ls"},
		{"install", []string{"jq", "--all"}, "install has no flag --all; list its flags: keelbox help install"},
		{"apply", []string{"acme", "--yes", "--verbose"}, ""},
		{"resume", []string{"acme", "--yes"}, ""},
		{"status", []string{"--all", "--json"}, ""},
		// Commands the help does not list take what their callers give them.
		{"welcome", []string{"--box", "acme"}, ""},
	} {
		_, _, err := parse(tc.cmd, tc.args, &bytes.Buffer{})
		if got := fmt.Sprint(err); (tc.want == "" && err != nil) || (tc.want != "" && got != tc.want) {
			t.Errorf("keelbox %s %v: %v, want %q", tc.cmd, tc.args, err, tc.want)
		}
	}
	if code, _, errs := run("destroy", "acme", "--overwrite"); code != 2 || errs != "keelbox: destroy has no flag --overwrite; list its flags: keelbox help destroy\n" {
		t.Errorf("destroy --overwrite: exit %d, %q", code, errs)
	}
}

func TestReleaseGuard(t *testing.T) {
	x := system.NewFake()
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Env{X: x, Cfg: &config.Config{Box: "acme"}, M: m}
	if from, err := releaseGuard(e, false); err != nil || from != "" {
		t.Fatalf("fresh box: %q %v", from, err)
	}
	add := func(release, result string) {
		if err := logbook.Append(x, logbook.Record{Time: time.Now(), Release: release, Result: result, Keelbox: "v9"}); err != nil {
			t.Fatal(err)
		}
	}
	add("2000.1.0", "ok")
	if from, err := releaseGuard(e, false); err != nil || from != "2000.1.0" {
		t.Errorf("upgrade: %q %v", from, err)
	}
	add("9999.1.0", "ok")
	add("2000.1.0", "failed")
	if _, err := releaseGuard(e, false); err == nil || !strings.Contains(err.Error(), "--allow-downgrade") {
		t.Errorf("downgrade allowed: %v", err)
	}
	if from, err := releaseGuard(e, true); err != nil || from != "9999.1.0" {
		t.Errorf("explicit downgrade: %q %v", from, err)
	}
	add(m.Release, "partial")
	if from, err := releaseGuard(e, false); err != nil || from != "" {
		t.Errorf("same release: %q %v", from, err)
	}
}

func TestSelfUpdateRefusesDevBuild(t *testing.T) {
	if code, _, errs := run("self-update"); code != 1 || !strings.Contains(errs, "development build") {
		t.Errorf("%d %s", code, errs)
	}
}

func TestParseSessions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	out := "main\t3\t1799990000\t1\nrefactor\t1\t1799999970\t0\nbroken line\n"
	got := parseSessions(out, now)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].name != "main" || got[0].windows != 3 || got[0].age != "2h 46m" || got[0].attached != "attached" {
		t.Errorf("%+v", got[0])
	}
	if got[1].age != "<1m" || got[1].attached != "" {
		t.Errorf("%+v", got[1])
	}
}

func TestSignInsUseEachVendorsOwnLogin(t *testing.T) {
	c, err := config.Parse([]byte("version: 1\nbox: acme\nusers: {admin: {ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop\"]}}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range signIns(c) {
		names = append(names, s.name)
		if strings.Contains(s.login, "token") || strings.Contains(s.login, "--with-api-key") {
			t.Errorf("%s: login must be the interactive vendor flow, got %q", s.name, s.login)
		}
	}
	if strings.Join(names, ",") != "tailscale,claude-code,codex,gh" {
		t.Errorf("order %v", names)
	}
}

func TestInteractiveCommandsRefuseBadInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.yaml")
	run("init", p)
	if code, _, errs := run("attach", p, "--session", "claude:refactor"); code != 2 || !strings.Contains(errs, "separator") {
		t.Errorf("attach accepted a tmux-invalid name: %d %s", code, errs)
	}
	if code, _, errs := run("login", p); code != 2 || !strings.Contains(errs, "terminal") {
		t.Errorf("login without a terminal: %d %s", code, errs)
	}
}

// Rollback started from a laptop replaces the staged keelbox while that
// very binary is running; writing it in place fails with ETXTBSY.
func TestReplaceExecutableWhileItRuns(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keelbox")
	if err := os.WriteFile(path, data, 0o755); err != nil { //nolint:gosec // G306: an executable
		t.Fatal(err)
	}
	running := exec.Command(path, "30")
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = running.Process.Kill(); _ = running.Wait() }()
	want := []byte("#!/bin/sh\necho previous\n")
	if err := replaceExecutable(path, want); err != nil {
		t.Fatalf("replace a running executable: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("content after replace: %q, %v", got, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode after replace: %v, %v", fi.Mode(), err)
	}
}

func TestInitWritesTheProvidersTarget(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"local", "ssh", "hetzner"} {
		path := filepath.Join(dir, p+".yaml")
		code, out, errs := run("init", path, "--box", "acme", "--provider", p)
		if code != 0 {
			t.Fatalf("%s: %d %s", p, code, errs)
		}
		c, err := config.Load(path)
		if err != nil || c.Target.Provider != p {
			t.Fatalf("%s: %v", p, err)
		}
		if !strings.Contains(out, "keelbox ") {
			t.Errorf("%s: no next command in %q", p, out)
		}
	}
	if code, _, errs := run("init", filepath.Join(dir, "x.yaml"), "--provider", "azure"); code != 2 || !strings.Contains(errs, "local, ssh, hetzner, aws, digitalocean or gcp") {
		t.Errorf("unknown provider: %d %s", code, errs)
	}
}

func TestSelfUpdateLeavesPackageManagersAlone(t *testing.T) {
	arch := false
	defer func(f func() bool) { onArch = f }(onArch)
	onArch = func() bool { return arch }
	for _, c := range []struct {
		exe  string
		arch bool
		by   string
		cmd  string
	}{
		{"/opt/homebrew/Cellar/keelbox/0.1.0/bin/keelbox", false, "Homebrew", "brew upgrade keelbox"},
		{"/home/linuxbrew/.linuxbrew/Cellar/keelbox/0.1.0/bin/keelbox", false, "Homebrew", "brew upgrade keelbox"},
		{"/usr/bin/keelbox", true, "an Arch package", "yay -Syu (or your AUR helper's update)"},
		{"/usr/bin/keelbox", false, "your system's package manager", "your package manager's upgrade"},
		{"/home/you/.local/bin/keelbox", true, "", ""},
		{"/usr/local/bin/keelbox", true, "", ""},
	} {
		arch = c.arch
		if by, cmd := packageManager(c.exe); by != c.by || cmd != c.cmd {
			t.Errorf("%s (arch %v): %q %q, want %q %q", c.exe, c.arch, by, cmd, c.by, c.cmd)
		}
	}
}

// The docs links keelbox prints, and the installer's, go to the published
// docs on keelbox.dev, not to files in the repository.
func TestDocsLinksArePublic(t *testing.T) {
	installer, err := os.ReadFile("../../get/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	isolate(t)
	if !strings.HasPrefix(docsURL, "https://keelbox.dev/docs") {
		t.Errorf("docsURL %s", docsURL)
	}
	_, start, _ := run()
	texts := map[string]string{"start screen": start, "help": overview()}
	for _, c := range commands {
		texts[c.name+" --help"] = commandHelp(c)
	}
	texts["install.sh"] = string(installer)
	for name, s := range texts {
		for _, l := range strings.Split(s, "\n") {
			if strings.Contains(l, "github.com/keelbox/keelbox") && !strings.Contains(l, "/releases/download") && !strings.Contains(l, "workflows/release.yml") {
				t.Errorf("%s links to the repository, not keelbox.dev: %s", name, l)
			}
			if strings.Contains(l, "docs/") && !strings.Contains(l, "https://keelbox.dev/docs/") {
				t.Errorf("%s names a docs path that is not a keelbox.dev URL: %s", name, l)
			}
		}
	}
}

// keelbox doctor over SSH prints the sign-in rows once, on the laptop.
func TestForLaptop(t *testing.T) {
	if !forLaptop(options{local: true, configFile: provider.RemoteConfig}) {
		t.Error("the box's half of a laptop's doctor")
	}
	for _, o := range []options{{local: true, configFile: "/etc/keelbox/keelbox.yaml"}, {configFile: provider.RemoteConfig}} {
		if forLaptop(o) {
			t.Errorf("%+v", o)
		}
	}
}

// keelbox status prints the agent's tmux session names as escaped text:
// the agent names its sessions.
func TestStatusPrintsAgentOutputPlain(t *testing.T) {
	p := filepath.Join(t.TempDir(), "acme.yaml")
	if code, _, errs := run("init", p, "--box", "acme"); code != 0 {
		t.Fatal(errs)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	x := system.NewFake()
	x.OK("(as agent) tmux list-sessions", "\x1b[2J\x1b[H\x1b]0;owned\x07\x1b[32mALL OK\x1b[0m (1 window)\n")
	e := &engine.Env{X: x, Cfg: c, M: m}
	var out bytes.Buffer
	Status(context.Background(), e, &out)
	if strings.ContainsAny(out.String(), "\x1b\x07") || !strings.Contains(out.String(), `sessions   \x1b[2J\x1b[H\x1b]0;owned\x07`) {
		t.Errorf("status:\n%q", out.String())
	}
}

// keelbox status, off the tailnet, names the command for where it is read:
// keelbox login from another computer, tailscale's own only on the box.
func TestStatusTailnetHintIsForWhereItIsRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "acme.yaml")
	if code, _, errs := run("init", p, "--box", "acme"); code != 0 {
		t.Fatal(errs)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Env{X: system.NewFake(), Cfg: c, M: m, SSHConnection: "198.51.100.7 50000 203.0.113.10 22"}
	var out bytes.Buffer
	Status(context.Background(), e, &out)
	if !strings.Contains(out.String(), "tailnet    not connected; join the tailnet: keelbox login acme tailscale, then keelbox apply acme\n") {
		t.Errorf("from another computer:\n%s", out.String())
	}
	e.SSHConnection = ""
	out.Reset()
	Status(context.Background(), e, &out)
	if !strings.Contains(out.String(), "tailnet    not connected; join the tailnet on the box: sudo tailscale up --hostname=acme\n") {
		t.Errorf("on the box:\n%s", out.String())
	}
}

// keelbox status keeps its watchdog row after the agents row.
func TestStatusKeepsTheWatchdogRow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "acme.yaml")
	if code, _, errs := run("init", p, "--box", "acme"); code != 0 {
		t.Fatal(errs)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	x := system.NewFake()
	e := &engine.Env{X: x, Cfg: c, M: m}
	var out bytes.Buffer
	Status(context.Background(), e, &out)
	if !strings.Contains(out.String(), "\nagents ") || !strings.Contains(out.String(), "\nwatchdog ") {
		t.Errorf("status:\n%s", out.String())
	}
}

// keelbox status lists the agent's projects, read as the agent, with their
// names as plain text; status --json carries the same for keelbox destroy.
func TestStatusListsProjects(t *testing.T) {
	p := filepath.Join(t.TempDir(), "acme.yaml")
	if code, _, errs := run("init", p, "--box", "acme"); code != 0 {
		t.Fatal(errs)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	x := system.NewFake()
	x.AddUser("agent", 1001)
	hx := func(s string) string { return hex.EncodeToString([]byte(s)) }
	x.OK("(as agent) sh -s -- /home/agent/projects", strings.Join([]string{
		"P " + hx("site") + " repo 0 0 1", "P " + hx("shop") + " norepo 0 0 0", "P " + hx("api") + " repo 3 0 1",
		"P " + hx("blog") + " repo 0 2 1", "P " + hx("notes\x1b]0;x\x07") + " repo 0 1 0", "T 5"}, "\n")+"\n")
	e := &engine.Env{X: x, Cfg: c, M: m}
	var out bytes.Buffer
	Status(context.Background(), e, &out)
	want := "projects   5 in ~/projects, 4 with work that is only on this box\n" +
		"           api: 3 uncommitted changes\n" +
		"           blog: 2 commits not pushed\n" +
		`           notes\x1b]0;x\x07: no remote` + "\n" +
		"           shop: not a git repository\n" +
		"           site: clean and pushed\n"
	if !strings.Contains(out.String(), want) || strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("status:\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	if code := StatusJSON(context.Background(), e, &out); code != 0 {
		t.Fatalf("status --json: exit %d\n%s", code, out.String())
	}
	s, err := snapshot.Parse(out.Bytes())
	if err != nil || s.Box != "acme" || s.Projects == nil || s.Projects.Total != 5 || len(s.Projects.OnlyHere()) != 4 || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("status --json: %v\n%s", err, out.String())
	}
}
