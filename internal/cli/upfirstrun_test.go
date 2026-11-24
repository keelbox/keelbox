package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
)

// The first-run paths of keelbox up on a server the user already has: a
// key the server does not take yet, a host that does not answer, a name
// that is not the hostname, the LAN keeping SSH, and sign-ins left for
// later.

const srvProbe = "os=ubuntu 26.04\nhostname=kb-srv\nuser=ubuntu\nsudo=yes\ncloud_users=ubuntu \n" +
	"listen=tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\n"

func noCopyID(t *testing.T, have bool) {
	t.Helper()
	old := haveCopyID
	haveCopyID = func() bool { return have }
	t.Cleanup(func() { haveCopyID = old })
}

func refused(host, user string) error {
	return &keyRefused{host: host, user: user, dest: user + "@" + host}
}

// A key the server does not take: put there through the provider's console
// while up waits, then checked again; nothing is asked twice.
func TestUpPutsANewKeyOnTheServerByConsole(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	noCopyID(t, true)
	r := &fakeRunner{probe: srvProbe, inspectErrs: []error{refused("10.0.0.5", "ubuntu")}}
	in := script(
		"1",         // where: existing server
		"10.0.0.5",  // address
		"2",         // ubuntu
		"",          // no fingerprint
		"2",         // the key: the provider's console
		"",          // Enter once it has run
		"",          // name: the hostname
		"4", "", "", // no agents, gh, Tailscale
		"", // LAN keeps SSH
		"", // go ahead
	)
	code, out := upWith(t, r, in, "up", "--ssh-key", key, "--skip-sign-in")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	pub, _ := readPublicKey(key + ".pub")
	for _, want := range []string{
		"10.0.0.5 does not take your key ~/.ssh/id_ed25519.pub for ubuntu yet",
		"1) Copy it with ssh-copy-id", "2) Add it in the provider's console",
		"as ubuntu, run:\n  mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '" + pub + "' >> ~/.ssh/authorized_keys",
		"ok: kb-srv, Ubuntu 26.04, sudo works, nothing else listening",
		"Enter keeps the machine's hostname as the box's name.", "Box name [kb-srv]: ",
		"\nkb-srv is set up", "Start working:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if got := strings.Join(r.looks, "; "); got != "inspect 10.0.0.5 as ubuntu; inspect 10.0.0.5 as ubuntu; impact kb-srv as ubuntu" {
		t.Errorf("looked: %s", got)
	}
	if strings.Count(out, "Server's IP address or DNS name") != 1 || strings.Contains(out, "Rename the machine") {
		t.Errorf("asked again, or asked to rename:\n%s", out)
	}
	cfg, _ := loadBox(t, "kb-srv")
	if cfg.Host.Hostname != "kb-srv" || cfg.PublicSSHMode() != config.PublicSSHLAN || cfg.GH() == false {
		t.Errorf("host %+v public_ssh %s", cfg.Host, cfg.PublicSSHMode())
	}
}

// ssh-copy-id with the user's password: it runs on the terminal, then the
// check passes. When it fails, the console line is the fallback.
func TestUpPutsANewKeyOnTheServerWithSSHCopyID(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	noCopyID(t, true)
	r := &fakeRunner{probe: srvProbe, inspectErrs: []error{refused("10.0.0.5", "sam")}}
	code, out := upWith(t, r, script("1", "", "", "", "y"), "up", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "sam",
		"--ssh-key", key, "--agents", "none", "--tailscale=false", "--gh=false", "--host-key", "", "--skip-sign-in")
	if code != 0 || !strings.Contains(strings.Join(r.calls, "; "), "ssh-copy-id sam@10.0.0.5 id_ed25519.pub") {
		t.Fatalf("exit %d %v\n%s", code, r.calls, out)
	}
	isolate(t)
	key = laptopKey(t, mustHomeT(t))
	r = &fakeRunner{probe: srvProbe, inspectErrs: []error{refused("10.0.0.5", "sam")}, copyErr: errors.New("exit status 1")}
	code, out = upWith(t, r, script("1", "", "", "", "", "y"), "up", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "sam",
		"--ssh-key", key, "--agents", "none", "--tailscale=false", "--gh=false", "--host-key", "", "--skip-sign-in")
	if code != 0 || !strings.Contains(out, "ssh-copy-id did not put the key there") || !strings.Contains(out, "as sam, run:") {
		t.Errorf("fallback: %d\n%s", code, out)
	}
}

// Without a terminal, a key the server does not take stops before any
// change with exit 3 and both ways to put it there.
func TestUpKeyRefusedWithoutATerminal(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{inspectErr: refused("10.0.0.5", "root")}
	code, out := upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-key", key, "--yes")
	for _, want := range []string{"does not accept your SSH key for root", "ssh-copy-id -i ~/.ssh/id_ed25519.pub root@10.0.0.5", "as root:\n    mkdir -p ~/.ssh"} {
		if code != exitNeedsYou || !strings.Contains(out, want) {
			t.Errorf("%d, want %q\n%s", code, want, out)
		}
	}
	if len(r.calls) != 0 || isRegistered("lab") {
		t.Errorf("changed something: %v", r.calls)
	}
}

// Without a terminal, a first connection that one more flag would fix ends
// with the command to run, flags already given repeated: the account the
// server's image names, or the server's host key. Before the config is
// written no error names a config key.
func TestUpFirstConnectionErrorsEndWithTheCommand(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	offered := provider.ParseHostKeys("10.0.0.5 ssh-rsa AAAAB3NzaC1yc2E=\n10.0.0.5 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey\n")
	rsa, ed := offered[0].Fingerprint(), offered[1].Fingerprint()
	given := "SHA256:" + strings.Repeat("A", 43)
	for name, c := range map[string]struct {
		err  error
		args []string
		want []string
	}{
		"root on an Ubuntu cloud image": {
			err: &wrongAccount{host: "10.0.0.5", user: "root", want: "ubuntu"},
			want: []string{"keelbox up: 10.0.0.5 does not let root log in: its image says to log in as ubuntu. " +
				"Run it as that account: keelbox up lab --provider ssh --host 10.0.0.5 --ssh-user ubuntu --ssh-key " + key + " --yes\n"},
		},
		"a host key nobody can confirm": {
			err:  &provider.HostKeyError{Host: "10.0.0.5", Offered: offered},
			args: []string{"--ssh-user", "ubuntu"},
			want: []string{"10.0.0.5 is not a host keelbox has connected to, and no terminal is attached to confirm its host key. It offers " + rsa + " (ssh-rsa), " + ed + " (ssh-ed25519). " +
				"Read the server's key in the provider's console, or ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the server; if it is one of these, pass it: " +
				// Not filled in: the command does not run until someone has compared.
				"keelbox up lab --provider ssh --host 10.0.0.5 --ssh-user ubuntu --host-key SHA256:... --ssh-key " + key + " --yes\n"},
		},
		"a host key that is not the one given": {
			err:  &provider.HostKeyError{Host: "10.0.0.5", Want: given, Offered: offered},
			args: []string{"--host-key", given},
			want: []string{"10.0.0.5 does not offer the host key " + given + " given for it (it offers " + rsa + " (ssh-rsa), " + ed + " (ssh-ed25519)), so keelbox did not connect.",
				// The key it offers instead is not handed back as the answer.
				"and pass that one: keelbox up lab --provider ssh --host 10.0.0.5 --host-key SHA256:... --ssh-key " + key + " --yes\n"},
		},
	} {
		r := &fakeRunner{inspectErr: c.err}
		code, out := upWith(t, r, nil, append([]string{"up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-key", key, "--yes"}, c.args...)...)
		for _, want := range c.want {
			if code != 2 || !strings.Contains(out, want) {
				t.Errorf("%s: exit %d, want %q\n%s", name, code, want, out)
			}
		}
		if strings.Contains(out, "target.ssh.host_key") || strings.Contains(out, "Check your key logs in") {
			t.Errorf("%s: names a config key that does not exist yet, or the user's key:\n%s", name, out)
		}
		// No command keelbox prints carries a fingerprint the server offered.
		if _, cmd, _ := strings.Cut(out, ": keelbox up "); strings.Contains(cmd, rsa) || strings.Contains(cmd, ed) {
			t.Errorf("%s: the command carries a fingerprint nobody compared:\n%s", name, out)
		}
		if len(r.calls) != 0 || isRegistered("lab") {
			t.Errorf("%s: changed something: %v", name, r.calls)
		}
	}
}

// On a terminal, the account the server's image names is taken and the
// check runs again: the summary names it before anything changes, and it
// is the account the first apply logs in as.
func TestUpTakesTheAccountTheImageNames(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{probe: srvProbe, signed: true, inspectErrs: []error{&wrongAccount{host: "10.0.0.5", user: "root", want: "ubuntu"}}}
	// An existing server, its address, root, no fingerprint, the hostname
	// as the box name, go ahead.
	code, out := upWith(t, r, script("1", "10.0.0.5", "1", "", "", ""), "up", "--ssh-key", key, "--agents", "codex", "--gh=false", "--tailscale=false", "--skip-sign-in")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := strings.Join(r.looks[:2], "; "); got != "inspect 10.0.0.5 as root; inspect 10.0.0.5 as ubuntu" {
		t.Errorf("looked: %v", r.looks)
	}
	for _, want := range []string{"10.0.0.5 does not let root log in: its image says to log in as ubuntu. Checking as ubuntu instead.", "10.0.0.5 over SSH, first as ubuntu"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(r.calls) == 0 || !strings.HasSuffix(r.calls[0], " as ubuntu") {
		t.Errorf("the first apply: %v", r.calls)
	}

	// The account is kept with the other answers: a run that stops after
	// it carries on as ubuntu, not as root again.
	home = isolate(t)
	key = laptopKey(t, home)
	down := sshProblem(errors.New("dial tcp 10.0.0.5:22: i/o timeout"), "ubuntu", "10.0.0.5", 22)
	r = &fakeRunner{inspectErrs: []error{&wrongAccount{host: "10.0.0.5", user: "root", want: "ubuntu"}, down}}
	if code, out := upWith(t, r, script("1", "10.0.0.5", "1", ""), "up", "--ssh-key", key); code == 0 || !strings.Contains(out, "10.0.0.5 does not answer on port 22") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if d, ok := loadDraft(); !ok || d.Answers["ssh-user"] != "ubuntu" {
		t.Errorf("the answers kept: %+v", d.Answers)
	}
}

// A sign-in keelbox could not check does not stop keelbox up. With
// --skip-sign-in it is said and left for later, and up goes on to the last
// apply and the last screen; without a terminal it stops where it always
// does, at the sign-ins.
func TestUpGoesOnWhenASignInCouldNotBeChecked(t *testing.T) {
	const note = "the Codex sign-in was not checked: 10.0.0.5 does not accept your SSH key for agent. Check which key logs in: ssh agent@10.0.0.5 true. Then: keelbox login lab codex"
	args := func(key string, more ...string) []string {
		return append([]string{"up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--ssh-key", key, "--agents", "codex", "--gh=false", "--tailscale=false", "--yes"}, more...)
	}
	home := isolate(t)
	r := &fakeRunner{notChecked: []string{note}}
	code, out := upWith(t, r, nil, args(laptopKey(t, home), "--skip-sign-in")...)
	if code != 0 || !strings.Contains(out, "keelbox up: "+note+"\n") || !saysSetUp(out, "lab") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := strings.Join(r.calls, "; "); got != "apply lab as ubuntu; signed-in?; apply lab as admin" {
		t.Errorf("calls: %s", got)
	}
	if _, st := loadBox(t, "lab"); st.Stage != stageReady || strings.Join(st.Left, " ") != "codex" {
		t.Errorf("state %+v", st)
	}
	if !strings.Contains(out, "sign in to Codex: keelbox login lab codex") {
		t.Errorf("not left to do:\n%s", out)
	}

	home = isolate(t)
	r = &fakeRunner{notChecked: []string{note}}
	code, out = upWith(t, r, nil, args(laptopKey(t, home))...)
	if code != exitNeedsYou || !strings.Contains(out, "keelbox up: "+note+"\n") || !strings.Contains(out, "signing in is yours to do") {
		t.Errorf("without a terminal: exit %d\n%s", code, out)
	}
}

// The command an error prints repeats the flags this run was given, in a
// form a shell takes, and leaves out answers that came from a kept draft.
func TestUpCommandRepeatsTheFlagsGiven(t *testing.T) {
	o, _, err := parse("up", []string{"--yes", "--git-name", "Ada Lovelace", "--tailscale=false", "--host", "10.0.0.5", "--git-email", "ada@example.com",
		"--provider", "ssh", "--name", "lab", "--agents", "codex", "--lan-ports", ""}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	o.set["limits"], o.agentLimits = true, "75%"
	w := &wizard{o: o, box: "lab", drafted: map[string]bool{"limits": true}}
	if got, want := w.upCommand(map[string]string{"ssh-user": "ubuntu"}),
		"keelbox up lab --provider ssh --host 10.0.0.5 --ssh-user ubuntu --agents codex --git-email ada@example.com --git-name 'Ada Lovelace' --tailscale=false --yes"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// What it prints parses back to the same answers.
	again, pos, err := parse("up", strings.Fields(strings.ReplaceAll(strings.TrimPrefix(w.upCommand(nil), "keelbox up "), "'Ada Lovelace'", "Ada")), os.Stderr)
	if err != nil || len(pos) != 1 || pos[0] != "lab" || again.provider != "ssh" || again.host != "10.0.0.5" || again.tailscale || !again.yes || again.agents != "codex" {
		t.Errorf("parsed back: %+v %v %v", again, pos, err)
	}
}

// What the first connection's probe printed names the account, when an
// Ubuntu cloud image turned root away.
func TestImageAccount(t *testing.T) {
	for out, want := range map[string]string{
		"Please login as the user \"ubuntu\" rather than the user \"root\".\n\n": "ubuntu",
		"Please login as the user \"ec2-user\" rather than the user \"root\".":   "ec2-user",
		"os=ubuntu 26.04\nhostname=kb-srv\n":                                     "",
		"Please login as the user \"$(reboot)\" rather than the user \"root\".":  "",
		"": "",
	} {
		if got := imageAccount(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}

// A host that does not answer is said to be unreachable, and the answers
// given are kept: the next keelbox up offers them and asks only the rest.
func TestUpKeepsAnswersWhenTheHostDoesNotAnswer(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	down := sshProblem(errors.New("dial tcp 10.0.0.250:22: i/o timeout"), "ubuntu", "10.0.0.250", 22)
	r := &fakeRunner{inspectErr: down}
	code, out := upWith(t, r, script("1", "10.0.0.250", "2", ""), "up", "--ssh-key", key)
	for _, want := range []string{"10.0.0.250 does not answer on port 22; check it is on and reachable from here: ssh ubuntu@10.0.0.250",
		"Your answers so far are kept. Once that is fixed, continue: keelbox up"} {
		if code == 0 || !strings.Contains(out, want) {
			t.Errorf("%d, want %q\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "SSH key") {
		t.Errorf("an unreachable host reported as a key problem:\n%s", out)
	}
	d, ok := loadDraft()
	if !ok || d.Answers["host"] != "10.0.0.250" || d.Answers["ssh-user"] != "ubuntu" {
		t.Fatalf("draft %+v", d)
	}
	// Carry on: only the questions after the check are asked.
	r = &fakeRunner{probe: srvProbe, signed: true}
	code, out = upWith(t, r, script("", "", "4", "", "", "", ""), "up")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 || !strings.Contains(out, "You started setting up an existing server at 10.0.0.250, as ubuntu. Carry on with those answers?") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, asked := range []string{"Where should the box run?", "Server's IP address or DNS name", "Which account do you log in", "Host key fingerprint"} {
		if strings.Contains(out, asked) {
			t.Errorf("asked again: %s", asked)
		}
	}
	if _, ok := loadDraft(); ok {
		t.Error("answers kept after the config was written")
	}
}

// A box name that is not the hostname asks before renaming the machine;
// no keeps the hostname in the config, and the summary says which.
func TestUpAsksBeforeRenaming(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{probe: srvProbe, signed: true}
	in := script("1", "203.0.113.7", "2", "", "acme", "n", "4", "", "", "")
	code, out := upWith(t, r, in, "up", "--ssh-key", key)
	if code != 0 || !strings.Contains(out, "Rename the machine from kb-srv to acme as well (its hostname)? [y/N]") ||
		!strings.Contains(out, "It keeps its hostname, kb-srv; keelbox calls the box acme.") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, _ := loadBox(t, "acme")
	if cfg.Host.Hostname != "kb-srv" || cfg.Access.Tailscale.Hostname != "kb-srv" {
		t.Errorf("hostname %q tailnet %q", cfg.Host.Hostname, cfg.Access.Tailscale.Hostname)
	}
	// A public address: no LAN question, SSH closes everywhere but the tailnet.
	if strings.Contains(out, "Keep SSH open to your LAN") || cfg.PublicSSHMode() != config.PublicSSHAuto {
		t.Errorf("public address offered LAN SSH: %s", cfg.PublicSSHMode())
	}
	// Without a terminal the hostname is kept.
	isolate(t)
	key = laptopKey(t, mustHomeT(t))
	if code, out := upWith(t, &fakeRunner{probe: srvProbe, signed: true}, nil, "up", "acme", "--provider", "ssh", "--host", "10.0.0.5",
		"--ssh-key", key, "--yes"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "acme"); cfg.Host.Hostname != "kb-srv" || cfg.PublicSSHMode() != config.PublicSSHLAN {
		t.Errorf("no terminal: hostname %q public_ssh %s", cfg.Host.Hostname, cfg.PublicSSHMode())
	}
}

// Skipped sign-ins: up reaches Ready and says what is left; keelbox login
// takes them off the list.
func TestUpReadyWithSignInsSkipped(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{login_: loginResult{signed: []string{"claude-code"}, skipped: []string{"tailscale", "codex"}, failed: []string{"gh"}}}
	code, out := upWith(t, r, script("", "", "", ""), "up", "lab", "--provider", "ssh", "--host", "203.0.113.7", "--ssh-user", "root", "--host-key", "", "--ssh-key", key, "--yes")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"applying lab again; public SSH stays open until it is on the tailnet",
		"\nlab is set up", "Start working:", "ssh agent@203.0.113.7",
		"Left to do      join the tailnet: keelbox login lab tailscale, then keelbox apply lab (closes public SSH)",
		"sign in to Codex: keelbox login lab codex", "sign in to GitHub: keelbox login lab gh",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	// Skipping GitHub says what it means, in one line with the command.
	if flat := strings.Join(strings.Fields(out), " "); !strings.Contains(flat,
		"sign in to GitHub: keelbox login lab gh (until then the agent's work stays on lab, unless it is pushed another way or copied off)") {
		t.Errorf("the GitHub line does not say what skipping means:\n%s", out)
	}
	cfg, st := loadBox(t, "lab")
	if st.Stage != stageReady || strings.Join(st.Left, " ") != "tailscale codex gh" {
		t.Errorf("state %+v", st)
	}
	if _, ls, _ := run("ls"); !strings.Contains(ls, "ready, sign-ins left (tailscale, codex, gh): keelbox login lab") {
		t.Errorf("ls: %s", ls)
	}
	noteSignedIn(cfg, []string{"codex"})
	if _, st := loadBox(t, "lab"); strings.Join(st.Left, " ") != "tailscale gh" {
		t.Errorf("after login: %v", st.Left)
	}
	// Without a terminal: --skip-sign-in finishes, naming what is left.
	isolate(t)
	key = laptopKey(t, mustHomeT(t))
	r = &fakeRunner{}
	code, out = upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "203.0.113.7", "--ssh-key", key, "--agents", "codex", "--yes", "--skip-sign-in")
	if code != 0 || !strings.Contains(out, "leaving the sign-ins for later (--skip-sign-in): tailscale, codex, gh") || !strings.Contains(out, "Left to do") {
		t.Errorf("--skip-sign-in: %d\n%s", code, out)
	}
}

// Declining Remote Control's first-run questions is a choice about an
// optional feature: up still reaches Ready and lists it under Left to do.
func TestUpReadyWithRemoteControlDeclined(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{login_: loginResult{signed: []string{"tailscale", "claude-code"}, failed: []string{"remote-control"}}}
	code, out := upWith(t, r, script("", "", "", ""), "up", "lab", "--provider", "ssh", "--host", "203.0.113.7", "--ssh-user", "root", "--host-key", "",
		"--ssh-key", key, "--agents", "claude-code", "--remote-control", "--gh=false", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !saysSetUp(out, "lab") || !strings.Contains(out, "Left to do      Claude Code Remote Control: keelbox login lab claude-code, and answer y") {
		t.Errorf("output:\n%s", out)
	}
	if strings.Contains(out, "Claude app") {
		t.Errorf("Claude app row while Remote Control is not running:\n%s", out)
	}
	cfg, _ := loadBox(t, "lab")
	noteSignedIn(cfg, []string{"remote-control"})
	if _, st := loadBox(t, "lab"); len(st.Left) != 0 {
		t.Errorf("after Remote Control runs: left %v", st.Left)
	}
}

// Without --remote-control, up asks about Remote Control once Claude Code
// is signed in, and not at all while it is not. A yes
// that does not start leaves it under Left to do.
func TestUpAsksRemoteControlAfterClaudeSignIn(t *testing.T) {
	for _, c := range []struct {
		name    string
		login   loginResult
		answer  string
		rcFails bool
		asked   bool
		calls   string
		left    string
	}{
		{"skipped", loginResult{signed: []string{"tailscale"}, skipped: []string{"claude-code"}}, "", false, false, "apply lab as root; signed-in?; login lab; apply lab as admin", "claude-code"},
		{"no", loginResult{signed: []string{"tailscale", "claude-code"}}, "n", false, true, "apply lab as root; signed-in?; login lab; apply lab as admin", ""},
		{"yes", loginResult{signed: []string{"tailscale", "claude-code"}}, "y", false, true, "apply lab as root; signed-in?; login lab; apply lab as admin; remote-control lab", ""},
		{"yes, not started", loginResult{signed: []string{"tailscale", "claude-code"}}, "y", true, true, "apply lab as root; signed-in?; login lab; apply lab as admin; remote-control lab", "remote-control"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := isolate(t)
			key := laptopKey(t, home)
			r := &fakeRunner{login_: c.login, rcFails: c.rcFails}
			code, out := upWith(t, r, script(c.answer), "up", "lab", "--provider", "ssh", "--host", "203.0.113.7", "--ssh-user", "root", "--host-key", "",
				"--ssh-key", key, "--agents", "claude-code", "--gh=false", "--tailscale", "--yes")
			if code != 0 || !saysSetUp(out, "lab") {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if got := strings.Contains(out, "so the Claude app can start sessions on lab?"); got != c.asked {
				t.Errorf("asked %v:\n%s", got, out)
			}
			if got := strings.Join(r.calls, "; "); got != c.calls {
				t.Errorf("calls: %s", got)
			}
			cfg, st := loadBox(t, "lab")
			if cfg.RemoteControl() != (c.answer == "y") || strings.Join(st.Left, " ") != c.left || st.OfferRemoteControl || st.RemoteControlNext {
				t.Errorf("remote control %v, state %+v", cfg.RemoteControl(), st)
			}
		})
	}
}

// Ctrl-C during a sign-in ends the whole step with the command that
// continues; the box stays at the sign-in stage.
func TestUpSignInInterrupted(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{login_: loginResult{interrupted: "codex"}}
	code, out := upWith(t, r, script("", "", "", ""), "up", "lab", "--provider", "ssh", "--host", "203.0.113.7", "--ssh-user", "root", "--host-key", "", "--ssh-key", key, "--yes")
	if code != exitInterrupted || !strings.Contains(out, "stopped at the codex sign-in; nothing is half-done. Continue: keelbox up lab") {
		t.Errorf("%d\n%s", code, out)
	}
	if _, st := loadBox(t, "lab"); st.Stage != stageApplied {
		t.Errorf("stage %s", st.Stage)
	}
}

// keelbox login on fake vendor CLIs, as the user on this machine: each
// sign-in is a choice, skipping is not a failure, and Ctrl-C in a vendor's
// flow ends the step even when the CLI exits with its own code.
func TestSignInChoicesAndCtrlC(t *testing.T) {
	isolate(t)
	bin := t.TempDir()
	state := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("claude", `case "$1 $2" in "auth status") test -f `+state+`/claude;; "auth login") touch `+state+`/claude;; esac`)
	// codex's own Ctrl-C handling exits 255; the wrapper still says 130.
	write("codex", `case "$1 $2" in "login status") exit 1;; *) kill -INT $PPID; exit 255;; esac`)
	write("gh", `exit 1`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("USER", "kbtest")
	out, err := config.Starter{Box: "lab", Provider: config.ProviderLocal, NoTailscale: true, Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(out), "lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Users.Admin.Name, cfg.Users.Agent.Name = "kbtest", "kbtest"
	var buf strings.Builder
	// claude: sign in now; codex: skip; gh (its token first): check again,
	// still not, skip.
	p := prompt.New(strings.NewReader("1\n2\n4\n3\n"), &buf, true)
	res := signInAll(context.Background(), cfg, options{}, "", p, &buf, &buf)
	if res.err != nil || strings.Join(res.signed, " ") != "claude-code" || strings.Join(res.skipped, " ") != "codex gh" || res.interrupted != "" {
		t.Fatalf("%+v\n%s", res, buf.String())
	}
	for _, want := range []string{
		"Next, 3 sign-ins on lab: Claude Code, Codex, GitHub.", "Ctrl-C stops signing in.",
		"Claude Code: your Claude account, for Claude Code", "2) Skip for now      later: keelbox login lab codex",
		"Open the link it shows in a browser", "GitHub still reports not signed in on lab.",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "Enter the code Codex shows") {
		t.Errorf("the Codex code line without a Codex sign-in:\n%s", buf.String())
	}
	// codex alone, as keelbox login lab codex: Ctrl-C in its flow.
	buf.Reset()
	res = signInAll(context.Background(), cfg, options{}, config.AgentCodex, p, &buf, &buf)
	if res.interrupted != config.AgentCodex || len(res.failed) != 0 {
		t.Errorf("%+v\n%s", res, buf.String())
	}
	// Codex prints a new code each attempt: use that one.
	if !strings.Contains(strings.Join(strings.Fields(buf.String()), " "), "Enter the code Codex shows below, from this attempt: not one from an earlier attempt") {
		t.Errorf("no line about the newest Codex code:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "exit status") || strings.Contains(buf.String(), "context canceled") {
		t.Errorf("raw error shown:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "gh shows a one-time code") {
		t.Errorf("the GitHub line without a GitHub sign-in:\n%s", buf.String())
	}
	// gh alone shows its menu too; its browser sign-in, the second choice,
	// runs with its questions off, so it waits for the approval
	// at once, and the line before it says where the code goes.
	buf.Reset()
	_ = signInAll(context.Background(), cfg, options{}, "gh", prompt.New(strings.NewReader("2\n3\n"), &buf, true), &buf, &buf)
	got := strings.Join(strings.Fields(buf.String()), " ")
	if !strings.Contains(got, "Open the link in a browser on this computer and type the code there. gh finishes once you approve.") ||
		!strings.Contains(got, "GH_PROMPT_DISABLED=1 gh auth login --hostname github.com --git-protocol https --web --clipboard=false") {
		t.Errorf("no line about where gh's code goes, or gh run with its questions on:\n%s", buf.String())
	}
	if strings.Contains(got, "paste any code it asks for back here") {
		t.Errorf("gh's sign-in has nothing to paste back:\n%s", buf.String())
	}
	if _, n := signInArg([]string{"lab", "claude"}); n != config.AgentClaudeCode {
		t.Errorf("signInArg: %q", n)
	}
	if pos, n := signInArg([]string{"github"}); n != "gh" || len(pos) != 0 {
		t.Errorf("signInArg one: %v %q", pos, n)
	}
}

func TestForgetTokenWithoutABoxForgetsTheDefault(t *testing.T) {
	isolate(t)
	register(t, config.Starter{Box: "acme", Provider: config.ProviderSSH, SSHHost: "10.0.0.5"})
	store := openStore(nil)
	_ = store.Set(context.Background(), "hetzner", goodToken)
	code, out, errs := run("forget-token")
	if code != 0 || !strings.Contains(out, `Removed the Hetzner token "hetzner"`) {
		t.Errorf("%d %s %s", code, out, errs)
	}
	if code, _, errs := run("forget-token", "acme"); code != 1 || !strings.Contains(errs, "uses no API token") {
		t.Errorf("ssh box: %d %s", code, errs)
	}
}

func TestForgetAndBoxNameTypos(t *testing.T) {
	isolate(t)
	register(t, config.Starter{Box: "acme", Provider: config.ProviderSSH, SSHHost: "10.0.0.5"})
	_ = saveUpState("acme", upState{Via: viaSSH, Stage: stageReady})
	if _, _, errs := run("status", "acmee"); !strings.Contains(errs, "did you mean keelbox status acme?") || strings.Contains(errs, "keelbox up acmee") {
		t.Errorf("typo: %s", errs)
	}
	if code, _, errs := run("forget", "acme"); code != 2 || !strings.Contains(errs, "keelbox forget acme --yes") {
		t.Errorf("no terminal: %d %s", code, errs)
	}
	if code, out, errs := run("forget", "acme", "--yes"); code != 0 || !strings.Contains(out, "Forgot acme. The machine is not touched.") {
		t.Errorf("%d %s %s", code, out, errs)
	}
	if isRegistered("acme") || isKnown("acme") {
		t.Error("acme still known")
	}
}

func TestBoxNamesNeedALetter(t *testing.T) {
	if validBoxName("1") == nil || validBoxName("devbox-2") != nil || suggestName("42") != "devbox" {
		t.Error("digit-only names")
	}
}
