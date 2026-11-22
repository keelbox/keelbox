package modules

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

func TestFreshBoxPlansChangesInEveryModule(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	before := s.Paths()
	items := engine.Plan(context.Background(), e, All())
	pending := map[string]int{}
	for _, it := range items {
		if it.Err != nil {
			t.Errorf("%s/%s: check error: %v", it.Module, it.Step, it.Err)
		}
		if !it.OK {
			pending[it.Module]++
		}
	}
	// With no notify.channels, notify has nothing to install: it runs to
	// remove what an earlier config left, and agent-policy has no setting
	// to write.
	settles := map[string]bool{"notify": true, "agent-policy": true}
	for _, m := range All() {
		if m.Enabled(e.Cfg) && pending[m.Name()] == 0 && !settles[m.Name()] {
			t.Errorf("module %s: expected pending changes on a fresh box", m.Name())
		}
	}
	// Plan must not change anything.
	for _, c := range s.Commands() {
		for _, mut := range []string{"apt-get", "useradd", "ufw --force", "usermod", "hostnamectl", "systemctl enable"} {
			if strings.HasPrefix(c, mut) {
				t.Errorf("plan ran a mutating command: %s", c)
			}
		}
	}
	if after := s.Paths(); !slices.Equal(after, before) {
		t.Errorf("plan wrote files: before %v, after %v", before, after)
	}
}

func TestApplyConvergesAndIsIdempotent(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	first, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if len(first.Changes) < 30 {
		t.Errorf("first apply made only %d changes", len(first.Changes))
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK {
			t.Errorf("after apply, %s/%s still pending: %s (%v)", it.Module, it.Step, it.Detail, it.Err)
		}
	}
	second, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(second.Changes) != 0 {
		t.Errorf("second apply changed %v", second.Changes)
	}
	findings := engine.Doctor(ctx, e, All())
	for _, f := range findings {
		if f.Level == engine.LevelFail {
			t.Errorf("doctor failure %s/%s: %s", f.Module, f.Check, f.Detail)
		}
	}
}

func TestApplyLeavesUserContentAlone(t *testing.T) {
	s := newSim(t)
	s.AddUser("admin", 1000)
	s.Files["/home/admin/.ssh/authorized_keys"] = &system.FakeFile{Data: []byte("ssh-ed25519 AAAAhand added@by-hand\n"), Mode: 0o600, Owner: "admin", Group: "admin"}
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	data := s.content("/home/admin/.ssh/authorized_keys")
	if !strings.Contains(data, "added@by-hand") || !strings.Contains(data, adminKey) {
		t.Errorf("authorized_keys lost content:\n%s", data)
	}
	if !s.Ran("usermod -aG sudo admin") {
		t.Error("existing admin not added to sudo")
	}
	if s.Ran("useradd --create-home --shell /bin/bash --user-group admin") {
		t.Error("tried to recreate an existing user")
	}
}

func TestSSHDHardeningRefusesWithoutAdminKey(t *testing.T) {
	s := newSim(t)
	s.AddUser("admin", 1000)
	e := simEnv(t, s, testConfig(t, ""))
	err := sshdStep("agent").Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("want refusal, got %v", err)
	}
	if st, _ := s.Stat(sshdDropIn); st.Exists {
		t.Error("drop-in written despite missing key")
	}
}

func TestSSHDValidateFailureRollsBack(t *testing.T) {
	s := newSim(t)
	s.AddUser("admin", 1000)
	s.AddFile("/home/admin/.ssh/authorized_keys", adminKey+"\n", 0o600)
	s.Fail("sshd -t")
	e := simEnv(t, s, testConfig(t, ""))
	if err := sshdStep("agent").Apply(context.Background(), e); err == nil {
		t.Fatal("want validation error")
	}
	if st, _ := s.Stat(sshdDropIn); st.Exists {
		t.Error("invalid drop-in left in place")
	}
	if s.Ran("systemctl reload") {
		t.Error("reloaded sshd after a failed validation")
	}
}

func TestSSHDropInSortsFirstAndNeverStartsService(t *testing.T) {
	if !strings.HasPrefix(sshdDropIn, "/etc/ssh/sshd_config.d/0") {
		t.Errorf("drop-in %s must sort before 50-cloud-init.conf: sshd keeps the first value", sshdDropIn)
	}
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Commands() {
		if strings.Contains(c, "enable --now ssh") || c == "systemctl start ssh.service" || c == "systemctl restart ssh.service" {
			t.Errorf("must never start or restart ssh.service (socket activation): %s", c)
		}
	}
}

func TestAgentSudoOptIn(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "  agent:\n    sudo: true\n"))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Stat(agentSudoers); !st.Exists {
		t.Error("agent sudoers missing with sudo: true")
	}
	s2 := newSim(t)
	e2 := simEnv(t, s2, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e2, All()); err != nil {
		t.Fatal(err)
	}
	if st, _ := s2.Stat(agentSudoers); st.Exists {
		t.Error("agent has sudoers by default")
	}
}

func TestTailscaleAuthKeyNeverOnCommandLine(t *testing.T) {
	s := newSim(t)
	const secret = "tskey-auth-SECRETVALUE"
	s.AddFile("/run/keelbox/tailscale.authkey", secret, 0o600)
	e := simEnv(t, s, testConfig(t, "access:\n  tailscale:\n    auth_key_file: /run/keelbox/tailscale.authkey\n"))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if !s.Ran("tailscale up --auth-key=file:/run/keelbox/tailscale.authkey") {
		t.Error("tailscale up not run with file: auth key")
	}
	for _, c := range s.Calls {
		if strings.Contains(c.String(), secret) || strings.Contains(strings.Join(c.Env, " "), secret) {
			t.Errorf("secret leaked into a command: %s", c)
		}
	}
}

func TestTailscaleAuthKeyModeChecked(t *testing.T) {
	s := newSim(t)
	s.AddFile("/k", "x", 0o644)
	e := simEnv(t, s, testConfig(t, "access:\n  tailscale:\n    auth_key_file: /k\n"))
	if err := tailscaleUpStep().Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("want mode refusal, got %v", err)
	}
}

func TestExistingSwapIsLeftAlone(t *testing.T) {
	s := newSim(t)
	s.swap = []string{"/swap.img"}
	e := simEnv(t, s, testConfig(t, ""))
	st, err := swapStep(2<<30).Check(context.Background(), e)
	if err != nil || !st.OK {
		t.Fatalf("existing swap should satisfy the step: %+v %v", st, err)
	}
}

func TestDisabledModulesAreSkipped(t *testing.T) {
	s := newSim(t)
	cfg := testConfig(t, "agents: []\naccess:\n  tailscale:\n    enabled: false\nsessions:\n  tmux: false\nruntime:\n  docker: false\n  node: false\n")
	e := simEnv(t, s, cfg)
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Commands() {
		for _, bad := range []string{"tailscale", "docker", "mise", "git clone", "(as agent) bash"} {
			if strings.Contains(c, bad) {
				t.Errorf("disabled feature ran %q", c)
			}
		}
	}
}

func TestDockerDaemonMergeKeepsOtherKeys(t *testing.T) {
	out, changed, err := mergeDaemonJSON("daemon.json", []byte(`{"data-root":"/srv/docker","log-opts":{"max-size":"1m"}}`))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(out), `"data-root": "/srv/docker"`) || !strings.Contains(string(out), `"max-size": "10m"`) {
		t.Errorf("bad merge:\n%s", out)
	}
	if _, changed, _ := mergeDaemonJSON("daemon.json", out); changed {
		t.Error("merge not idempotent")
	}
	if _, _, err := mergeDaemonJSON("daemon.json", []byte("{bad")); err == nil {
		t.Error("invalid JSON accepted")
	}
}

func TestDockerSeriesDriftPlansChange(t *testing.T) {
	s := newSim(t)
	for _, p := range dockerPackages {
		s.pkgs[p] = "1"
	}
	s.pkgs["docker-ce"] = "5:28.5.0-1~ubuntu.26.04~resolute"
	e := simEnv(t, s, testConfig(t, ""))
	st, _ := dockerPackagesStep().Check(context.Background(), e)
	if st.OK || !strings.Contains(st.Detail, "29 series") {
		t.Fatalf("want series change, got %+v", st)
	}
	if err := dockerPackagesStep().Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if s.pkgs["docker-ce"] != "5:29.9.3-1~ubuntu.26.04~resolute" {
		t.Errorf("installed %s", s.pkgs["docker-ce"])
	}
}

func TestSigningKeyFingerprintMismatchRefused(t *testing.T) {
	s := newSim(t)
	s.OK("gpg", "fpr:::::::::0000000000000000000000000000000000000000:\n")
	e := simEnv(t, s, testConfig(t, ""))
	err := aptKeyStep("k", "https://example.invalid/gpg", "/etc/apt/keyrings/x.asc", s.m.Docker.GPGFingerprint).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("want refusal, got %v", err)
	}
	if st, _ := s.Stat("/etc/apt/keyrings/x.asc"); st.Exists {
		t.Error("untrusted key installed")
	}
}

func TestMiseChecksumMismatchRefused(t *testing.T) {
	s := newSim(t)
	s.OK("sha256sum", strings.Repeat("0", 64)+"  x\n")
	e := simEnv(t, s, testConfig(t, ""))
	if err := miseBinaryStep().Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum refusal, got %v", err)
	}
	if s.Ran("install -m 0755") {
		t.Error("installed an unverified mise")
	}
}

func TestTmuxConfigIsGeneric(t *testing.T) {
	c := TmuxConfig(TmuxOptions{Box: "acme", Home: "/home/agent", Recovery: true})
	for _, want := range []string{"acme:#S", "bind % split-window", "tmux-continuum/continuum.tmux"} {
		if !strings.Contains(c, want) {
			t.Errorf("tmux config missing %q", want)
		}
	}
	if strings.Contains(c, "%!") {
		t.Errorf("tmux config has format debris:\n%s", c)
	}
	if strings.Contains(TmuxConfig(TmuxOptions{Box: "acme", Home: "/home/agent"}), "run-shell") {
		t.Error("plugins loaded with recovery off")
	}
}

func TestPrimerContent(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "primer:\n  notes: Use the staging database only.\n"))
	p := RenderPrimer(e)
	for _, want := range []string{"testbox", "127.0.0.1", "has no sudo and is not in the `docker` group", "rootless daemon", "can send anywhere", "Use the staging database only.", "Node " + s.m.Node} {
		if !strings.Contains(p, want) {
			t.Errorf("primer missing %q", want)
		}
	}
	if strings.Contains(p, "<no value>") {
		t.Error("primer has unfilled template fields")
	}
}

// The primer says the box keeps files but backs nothing up unless backups
// are on, and tells Codex users where generated images are, and to touch
// nothing else under ~/.codex.
func TestPrimerPersistenceAndCodexImages(t *testing.T) {
	s := newSim(t)
	p := RenderPrimer(simEnv(t, s, testConfig(t, "agents: [codex]\n")))
	for _, want := range []string{"This box is persistent", "**nothing on it is backed up**", "Push it to a remote", "`git init`",
		"`~/.codex/generated_images/`", "Never copy, list or read other\n  files under `~/.codex`"} {
		if !strings.Contains(p, want) {
			t.Errorf("primer missing %q:\n%s", want, p)
		}
	}
	p = RenderPrimer(simEnv(t, s, testConfig(t, "agents: [claude-code]\n")))
	if strings.Contains(p, "generated_images") {
		t.Error("Codex image note without Codex")
	}
}

// The primer tells agents how work survives the box: git init, a
// .gitignore, commits as they go, and that what is not pushed lives only
// here (ADR 0064).
func TestPrimerWork(t *testing.T) {
	s := newSim(t)
	p := RenderPrimer(simEnv(t, s, testConfig(t, "")))
	for _, want := range []string{
		"## Your work", "**Work that is not pushed to a remote exists only on this box.**",
		"Start every new project with `git init` in `~/projects/<name>`", "`.gitignore`", "Commit as you go",
		"ask the\n  human where to push", "`keelbox status` (no sudo) lists each project",
		"\"Please tell me who you are\"", "`users.agent.git`",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("primer missing %q:\n%s", want, p)
		}
	}
	p = RenderPrimer(simEnv(t, s, testConfig(t, gitYAML+"agents: [claude-code]\nsessions:\n  remote_control: {enabled: true, directory: work}\n")))
	for _, want := range []string{"Commits carry the name and email the human chose", "`git init` in `~/work/<name>`", "Projects: `~/work/<name>`"} {
		if !strings.Contains(p, want) {
			t.Errorf("primer missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "Please tell me who you are") || strings.Contains(p, "Ada Lovelace") || strings.Contains(p, "~/projects") {
		t.Errorf("primer with an identity and another directory:\n%s", p)
	}
}

func TestShellPathBlockBeforeInteractiveGuard(t *testing.T) {
	b := engine.Block{Path: "x", ID: "path", Content: ShellPath, Top: true}
	ubuntu := "# ~/.bashrc\ncase $- in\n    *i*) ;;\n      *) return;;\nesac\n"
	out := b.Render(ubuntu)
	if strings.Index(out, "mise/shims") > strings.Index(out, "return;;") {
		t.Error("PATH block must precede the interactive guard")
	}
	if b.Render(out) != out {
		t.Error("render not idempotent")
	}
}

func TestBrokenAgentInstallerFailsOnlyThatAgent(t *testing.T) {
	s := newSim(t)
	s.Fail("(as agent) bash /tmp/")
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatalf("a vendor installer failure stopped the run: %v", err)
	}
	if len(res.Failed) != 1 || res.Failed[0].Step != "claude-code" {
		t.Fatalf("failed = %+v", res.Failed)
	}
	if !s.has("/home/agent/.local/bin/codex") {
		t.Error("codex not installed after claude-code failed")
	}
	failedChecks := map[string]bool{}
	for _, f := range engine.Doctor(ctx, e, All()) {
		if f.Level == engine.LevelFail {
			failedChecks[f.Module+"/"+f.Check] = true
		}
	}
	if !failedChecks["agents/claude-code"] {
		t.Errorf("doctor does not fail the missing agent: %v", failedChecks)
	}
}

func TestApplyInstallsItselfAndKeepsThePrevious(t *testing.T) {
	s := newSim(t)
	s.AddFile(BinaryPath, "older keelbox", 0o755)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if err := binaryStep().Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.ReadFile(BinaryPath); string(cur) != "keelbox binary" {
		t.Errorf("installed %q", cur)
	}
	if prev, _ := s.ReadFile(PreviousBinary); string(prev) != "older keelbox" {
		t.Errorf("previous %q", prev)
	}
	if st, _ := binaryStep().Check(ctx, e); !st.OK {
		t.Errorf("not converged: %s", st.Detail)
	}
	e.Self = BinaryPath
	if st, _ := binaryStep().Check(ctx, e); !st.OK {
		t.Error("running the installed binary is a change")
	}
}

func TestCloudUserLockDefersInItsOwnSession(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	e.Facts.InvokingUser = "ubuntu"
	ctx := context.Background()
	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deferred) != 1 || res.Deferred[0].Step != "cloud-user-locked" || s.Ran("usermod --lock") {
		t.Fatalf("not deferred: %+v", res.Deferred)
	}
	e.Facts.InvokingUser = "admin"
	res, err = engine.Apply(ctx, e, All())
	if err != nil || len(res.Changes) != 1 || res.Changes[0].Step != "cloud-user-locked" {
		t.Fatalf("admin apply: %+v %v", res, err)
	}
	if !s.Ran("usermod --lock --expiredate 1 ubuntu") {
		t.Error("ubuntu not locked")
	}
}

func TestCloudUsersIgnoresManagedAndUnknownAccounts(t *testing.T) {
	s := newSim(t)
	s.AddUser("admin", 1001)
	s.AddFile(cloudInitSudoers, "ubuntu ALL=(ALL) NOPASSWD:ALL\nadmin ALL=(ALL) NOPASSWD:ALL\nghost ALL=(ALL) NOPASSWD:ALL\n%sudo ALL=(ALL) ALL\n", 0o440)
	e := simEnv(t, s, testConfig(t, ""))
	if got := CloudUsers(e); len(got) != 1 || got[0] != "ubuntu" {
		t.Errorf("got %v", got)
	}
	s.Remove(cloudInitSudoers)
	if got := CloudUsers(e); got != nil {
		t.Errorf("without cloud-init: %v", got)
	}
	e2 := simEnv(t, newSim(t), testConfig(t, "  lock_cloud_user: false\n"))
	for _, st := range (Host{}).Steps(e2) {
		if st.Name == "cloud-user-locked" {
			t.Error("lock step present with lock_cloud_user: false")
		}
	}
}

// On Hetzner the image's account is root. Locking it would expire root,
// which stops root's cron jobs; sshd already refuses root logins.
func TestCloudUserLockLeavesRootAlone(t *testing.T) {
	s := newSim(t)
	s.Remove("/home/ubuntu")
	delete(s.Users, "ubuntu")
	s.AddFile(cloudInitSudoers, "# Created by cloud-init\n\n# User rules for root\nroot ALL=(ALL) NOPASSWD:ALL\n", 0o440)
	e := simEnv(t, s, testConfig(t, ""))
	if got := CloudUsers(e); got != nil {
		t.Errorf("root listed as a cloud user: %v", got)
	}
	e.Facts.InvokingUser = "admin"
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if s.Ran("usermod --lock") {
		t.Error("apply locked root")
	}
}

// A box whose root has an expiry date: apply clears the expiry and nothing
// else.
func TestApplyClearsAnExpiredRoot(t *testing.T) {
	s := newSim(t)
	s.AddFile("/etc/shadow", "root:!*:17478:0:99999:7::1:\nubuntu:!:20000:0:99999:7:::\n", 0o640)
	e := simEnv(t, s, testConfig(t, ""))
	e.Facts.InvokingUser = "admin"
	res, err := engine.Apply(context.Background(), e, All())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Changes {
		found = found || c.Step == "root-not-expired"
	}
	if !found || !s.Ran("chage --expiredate -1 root") {
		t.Fatalf("expired root not repaired: %+v", res.Changes)
	}
	shadow, _ := s.ReadFile("/etc/shadow")
	if !strings.HasPrefix(string(shadow), "root:!*:17478:0:99999:7:::\n") {
		t.Errorf("shadow after repair: %q", shadow)
	}
}

func TestAgentCannotReachTheAdminThroughSSH(t *testing.T) {
	if c := sshdConfig("agent"); !strings.HasSuffix(c, "Match User agent\n\tAllowAgentForwarding no\n") {
		t.Errorf("agent forwarding not refused, or Match block not last:\n%s", c)
	}
	rules := AgentNetRules([]uidRange{{1001, 1001}, {165536, 231071}}, nil, nil)
	for _, want := range []string{"meta skuid { 1001, 165536-231071 } counter reject", "tcp dport 22 fib daddr type local meta skuid != 0 counter reject", "169.254.0.0/16", "fd00:ec2::254", "delete table inet keelbox_agent"} {
		if !strings.Contains(rules, want) {
			t.Errorf("agent rules missing %q", want)
		}
	}
	for _, line := range strings.Split(TmuxConfig(TmuxOptions{Box: "acme", Home: "/home/agent", Recovery: true}), "\n") {
		if strings.HasPrefix(line, "set -g update-environment") && strings.Contains(line, "SSH_AUTH_SOCK") {
			t.Errorf("tmux re-imports the forwarded agent: %s", line)
		}
	}
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if !s.Ran("nft --check -f " + agentNetRules) {
		t.Error("rules not validated before loading")
	}
	for _, f := range (Host{}).Verify(ctx, e) {
		if (f.Check == "agent-net" || f.Check == "ssh-agent-forwarding") && f.Level != engine.LevelOK {
			t.Errorf("%s: %s", f.Check, f.Detail)
		}
	}
}

func TestExistingDockerIOIsReplacedOnlyWithAdopt(t *testing.T) {
	s := newSim(t)
	s.pkgs["docker.io"] = "1"
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	st, _ := dockerConflictsStep().Check(ctx, e)
	if st.OK || !strings.Contains(st.Detail, "--adopt") {
		t.Errorf("plan does not warn: %+v", st)
	}
	if err := dockerConflictsStep().Apply(ctx, e); err == nil || !strings.Contains(err.Error(), "--adopt") {
		t.Fatalf("removed docker.io without --adopt: %v", err)
	}
	if _, still := s.pkgs["docker.io"]; !still || s.Ran("apt-get") {
		t.Error("touched apt without --adopt")
	}
	e.Adopt = true
	if err := dockerConflictsStep().Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, still := s.pkgs["docker.io"]; still {
		t.Error("docker.io kept with --adopt")
	}
	stop, remove := -1, -1
	for i, c := range s.Commands() {
		if c == "systemctl stop docker.socket docker.service" && stop < 0 {
			stop = i
		}
		if strings.HasPrefix(c, "apt-get") && strings.Contains(c, " remove ") && remove < 0 {
			remove = i
		}
	}
	if stop < 0 || remove < stop {
		t.Errorf("docker.socket not stopped before docker.io was removed (stop %d, remove %d)", stop, remove)
	}
	// The daemon starts through its socket, which is restarted first.
	if err := dockerServiceStep().Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !s.Ran("systemctl restart docker.socket") || !s.Ran("systemctl enable --now docker.service") {
		t.Errorf("docker.service started without restarting docker.socket: %v", s.Commands())
	}
}

func TestAgentLeavesTheDockerGroupForRootlessDocker(t *testing.T) {
	s := newSim(t)
	s.AddUser("agent", 1001)
	s.groups["agent"] = nil
	s.groups["docker"] = []string{"agent"} // a box from before ADR 0012
	s.AddFile("/etc/subuid", "ubuntu:100000:65536\n", 0o644)
	e := simEnv(t, s, testConfig(t, gitYAML))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if contains(s.groups["docker"], "agent") {
		t.Error("agent still in the docker group")
	}
	if sub, _ := s.ReadFile("/etc/subuid"); !strings.Contains(string(sub), "agent:165536:65536") {
		t.Errorf("subuid range overlaps or is missing:\n%s", sub)
	}
	for _, f := range (Runtime{}).Verify(ctx, e) {
		if f.Level != engine.LevelOK {
			t.Errorf("%s: %s", f.Check, f.Detail)
		}
	}
}

func TestUserUnitChangeBeforeTheUnitExistsIsNotAnError(t *testing.T) {
	s := newSim(t)
	s.AddUser("agent", 1001)
	s.Fail("systemctl --user -M agent@ is-active --quiet docker.service")
	s.Fail("systemctl --user -M agent@ restart")
	e := simEnv(t, s, testConfig(t, ""))
	if err := restartUserUnit(context.Background(), e, e.AgentUser(), "docker.service", true); err != nil {
		t.Fatal(err)
	}
	if s.Ran("systemctl --user -M agent@ restart") {
		t.Error("restarted a unit that is not running")
	}
}

func TestTmuxConfigFollowsTheInstalledVersion(t *testing.T) {
	for v, want := range map[string]bool{"tmux 3.4": false, "tmux 3.5a": true, "tmux 3.6": true, "tmux next-3.7": false} {
		s := newSim(t)
		s.OK("tmux -V", v+"\n")
		e := simEnv(t, s, testConfig(t, ""))
		if got := tmuxAtLeast(context.Background(), e, 3, 5); got != want {
			t.Errorf("%s: %v", v, got)
		}
	}
	if strings.Contains(TmuxConfig(TmuxOptions{Box: "a", Home: "/h"}), "extended-keys-format") {
		t.Error("tmux 3.4 gets extended-keys-format")
	}
	if !strings.Contains(TmuxConfig(TmuxOptions{Box: "a", Home: "/h", CSIu: true}), "set -g extended-keys-format csi-u") {
		t.Error("tmux 3.5 lacks extended-keys-format")
	}
}

func TestGitHubCLIComesFromItsSignedRepository(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.pkgs["gh"]; !ok {
		t.Fatal("gh not installed")
	}
	src, err := s.ReadFile(ghSources)
	if err != nil || !strings.Contains(string(src), "signed-by="+ghKeyring) {
		t.Errorf("sources: %s %v", src, err)
	}

	off := newSim(t)
	e = simEnv(t, off, testConfig(t, "runtime:\n  gh: false\n"))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if _, ok := off.pkgs["gh"]; ok {
		t.Error("gh installed with runtime.gh: false")
	}
}

// Below 2 CPUs or 4 GB, doctor warns and the primer tells the agent to run
// one heavy process at a time.
func TestSmallHost(t *testing.T) {
	s := newSim(t)
	s.AddFile("/proc/meminfo", "MemTotal:         953000 kB\nMemAvailable:     400000 kB\n", 0o444)
	s.OK("nproc", "1\n")
	e := simEnv(t, s, testConfig(t, ""))
	var size engine.Finding
	for _, f := range (Host{}).Verify(context.Background(), e) {
		if f.Check == "size" {
			size = f
		}
	}
	if size.Level != engine.LevelWarn || !strings.Contains(size.Detail, "1 CPU and") ||
		!strings.HasSuffix(size.Detail, ": use a larger machine; for a cloud server keelbox created: keelbox resize testbox") {
		t.Errorf("size finding: %+v", size)
	}
	p := RenderPrimer(e)
	if !strings.Contains(p, "It has 1 CPU(s) and") || !strings.Contains(p, "That is small: below the 2 CPUs and 4 GB") {
		t.Errorf("primer:\n%s", p)
	}
	if SmallHost(2, 3900<<20) != "" || SmallHost(4, 8<<30) != "" {
		t.Error("a 2 CPU / 4 GB machine reads as small")
	}
}

// Off the tailnet, doctor names the command for where its reader is: from
// another computer keelbox's own, which name the box; tailscale's only for
// a run on the box itself, and then it says so.
func TestTailnetHintIsForWhereTheReaderIs(t *testing.T) {
	tailnet := func(e *engine.Env) engine.Finding {
		t.Helper()
		for _, f := range (Access{}).Verify(context.Background(), e) {
			if f.Check == "tailnet" {
				return f
			}
		}
		t.Fatal("no tailnet finding")
		return engine.Finding{}
	}
	// $SSH_CONNECTION of a run that arrived over SSH from another computer.
	const fromLaptop = "198.51.100.7 50000 203.0.113.10 22"
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	e.SSHConnection = fromLaptop
	if f := tailnet(e); f.Level != engine.LevelWarn || f.Detail != "not connected; join the tailnet: keelbox login testbox tailscale, then keelbox apply testbox" {
		t.Errorf("from another computer: %+v", f)
	}
	e.SSHConnection = ""
	if f := tailnet(e); f.Detail != "not connected; join the tailnet on the box: sudo tailscale up --hostname=testbox" {
		t.Errorf("on the box: %+v", f)
	}
	// With an auth key file there is no sign-in to run: from another
	// computer the fix is apply, which joins with the key. On the box it is
	// still tailscale's own command.
	keyed := simEnv(t, s, testConfig(t, "access:\n  tailscale:\n    auth_key_file: /run/keelbox/tailscale.authkey\n"))
	if f := tailnet(keyed); f.Level != engine.LevelFail || f.Detail != "not connected; join the tailnet on the box: sudo tailscale up --hostname=testbox" {
		t.Errorf("with an auth key file, on the box: %+v", f)
	}
	keyed.SSHConnection = fromLaptop
	if f := tailnet(keyed); f.Level != engine.LevelFail || f.Detail != "not connected; join the tailnet with the auth key file: keelbox apply testbox" {
		t.Errorf("with an auth key file: %+v", f)
	}
}

// The primer tells the agent to ask before it files a keelbox report, and
// leaves that out with reports.enabled: false (ADR 0062).
func TestPrimerReports(t *testing.T) {
	s := newSim(t)
	p := RenderPrimer(simEnv(t, s, testConfig(t, "")))
	for _, want := range []string{"## If keelbox gets in your way", "**Never file one without\nthe human's yes in this conversation.**", "keelbox report \"short title\"", "One report per problem", "exact error", "nothing is sent"} {
		if !strings.Contains(p, want) {
			t.Errorf("primer lacks %q", want)
		}
	}
	if p := RenderPrimer(simEnv(t, s, testConfig(t, "reports:\n  enabled: false\n"))); strings.Contains(p, "keelbox report") {
		t.Error("report section with reports.enabled: false")
	}
}

// A box with an older agent store, its links and its nightly unit: apply
// removes them, and the vendors' installers put the CLIs in the agent's
// home (ADR 0088).
func TestApplyReplacesTheOldAgentStore(t *testing.T) {
	s := newSim(t)
	for _, f := range []string{
		"/opt/keelbox/agents/claude-code/2.1.311/claude", "/opt/keelbox/agents/codex/0.166.0/bin/codex",
		"/usr/local/lib/keelbox/agents/bin/claude", "/var/lib/keelbox/agents/state.json",
		"/etc/systemd/system/keelbox-agents.timer", "/etc/systemd/system/keelbox-agents.service",
	} {
		s.AddFile(f, "old", 0o755)
	}
	s.Users["keelbox-smoke"] = system.User{Name: "keelbox-smoke", UID: 990, GID: 990, Home: "/nonexistent"}
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for p := range s.Files {
		if strings.HasPrefix(p, "/opt/keelbox/agents") || strings.HasPrefix(p, "/usr/local/lib/keelbox/agents") || strings.HasPrefix(p, "/var/lib/keelbox/agents") || strings.Contains(p, "keelbox-agents.") {
			t.Errorf("left behind: %s", p)
		}
	}
	if _, err := s.LookupUser("keelbox-smoke"); err == nil {
		t.Error("keelbox-smoke still exists")
	}
	if !s.has("/home/agent/.local/bin/claude") || !s.has("/home/agent/.local/bin/codex") {
		t.Error("the vendors' installers did not run")
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK && strings.HasPrefix(it.Step, "old-") {
			t.Errorf("after apply, %s still pending: %s", it.Step, it.Detail)
		}
	}
}

// A vendor installer that fails leaves the old store where it is, and the
// box's own steps still pass; the next apply that installs removes it.
func TestApplyKeepsTheOldStoreUntilTheVendorInstalls(t *testing.T) {
	s := newSim(t)
	s.AddFile("/opt/keelbox/agents/claude-code/2.1.311/claude", "old", 0o755)
	s.AddFile("/etc/systemd/system/keelbox-agents.timer", "old", 0o644)
	s.installerFails = true
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	_, _ = engine.Apply(ctx, e, All())
	if !s.has("/opt/keelbox/agents/claude-code/2.1.311/claude") {
		t.Error("the old store went before the vendor's CLI was installed")
	}
	if s.has("/etc/systemd/system/keelbox-agents.timer") {
		t.Error("the old timer is still there")
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK && !strings.HasPrefix(it.Step, "old-agent-store") && it.Step != "claude-code" && it.Step != "codex" {
			t.Errorf("%s pending after an apply whose installers failed: %s", it.Step, it.Detail)
		}
	}
	s.installerFails = false
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if s.has("/opt/keelbox/agents/claude-code/2.1.311/claude") {
		t.Error("the old store stayed once the vendor's CLI was installed")
	}
}

// Doctor says when the box's Tailscale key expires: a warning a month
// before, a failure in the last week when SSH from the internet is closed.
func TestTailnetKeyExpiry(t *testing.T) {
	now := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	closed := simEnv(t, newSim(t), testConfig(t, "host:\n  firewall:\n    public_ssh: closed\n"))
	for _, c := range []struct {
		env   *engine.Env
		exp   time.Time
		level engine.Level
		want  string
	}{
		{e, time.Time{}, engine.LevelOK, "key expiry off"},
		{e, now.AddDate(0, 3, 0), engine.LevelOK, "key expires 2027-04-01"},
		{e, now.AddDate(0, 0, 20), engine.LevelWarn, "expires 2027-01-21, in 20 days"},
		{e, now.AddDate(0, 0, 5), engine.LevelWarn, "Disable key expiry"},
		{closed, now.AddDate(0, 0, 5), engine.LevelFail, "the tailnet is the only way in"},
		{closed, now.AddDate(0, 0, 20), engine.LevelWarn, "https://login.tailscale.com/admin/machines"},
	} {
		f := tailnetKeyFinding(c.env, c.exp, now)
		if f.Level != c.level || !strings.Contains(f.Detail, c.want) {
			t.Errorf("expires %v: %v %q", c.exp, f.Level, f.Detail)
		}
	}
}

// The primer names the credential files, says to run long jobs detached,
// explains keelbox's hooks where notifications are on, and says how to
// get previews where they are off.
func TestPrimerAgentNotes(t *testing.T) {
	s := newSim(t)
	p := RenderPrimer(simEnv(t, s, testConfig(t, "previews:\n  enabled: false\n")))
	for _, want := range []string{"`~/.claude/.credentials.json` (Claude Code)", "`~/.codex/auth.json` (Codex)", "`~/.config/gh/hosts.yml`",
		"`systemd-run --user`", "Previews are off on this box", "`previews.enabled: true`", "`keelbox apply testbox`"} {
		if !strings.Contains(p, want) {
			t.Errorf("primer missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "Hooks:") {
		t.Error("hooks without notifications")
	}
	p = RenderPrimer(simEnv(t, s, testConfig(t, notifyConfig)))
	if !strings.Contains(p, "Hooks: Claude Code and Codex here run a hook keelbox manages") || strings.Contains(p, "Previews are off") {
		t.Errorf("with notifications and previews:\n%s", p)
	}
}
