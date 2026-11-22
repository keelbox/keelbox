package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
)

// realRunner is keelbox up's runner against real machines: the same code
// paths as create, apply and login.
type realRunner struct{}

func (realRunner) create(ctx context.Context, cfg *config.Config, o options, stdout, stderr io.Writer) int {
	cl, err := openCloud(ctx, cfg, o, terminalPrompt(ctx, stdout, o.style), stdout)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	logf := func(f string, a ...any) { say(stdout, o.style, o.style.Pointer(), fmt.Sprintf(f, a...)) }
	return cloudCreate(ctx, cl, cfg, o, logf, stdout, stderr)
}

func (realRunner) apply(ctx context.Context, cfg *config.Config, o options, stdout, stderr io.Writer) int {
	if isCloud(cfg.Target.Provider) {
		return runCloud(ctx, "apply", cfg, o, os.Stdin, stdout, stderr)
	}
	switch cfg.Target.Provider {
	case config.ProviderSSH:
		code, _ := applyBox(ctx, cfg, addr{cfg.Target.SSH.Host, cfg.Target.SSH.Port}, o, stdout, stderr)
		return code
	}
	if os.Geteuid() == 0 {
		return runLocal(ctx, "apply", cfg, o, stdout, stderr)
	}
	self, err := os.Executable()
	if err != nil {
		fail(stderr, err)
		return 1
	}
	// The child gets Ctrl-C from the terminal itself and stops after its
	// current step, so it is not killed when ctx is cancelled.
	args := append([]string{"env", envChild + "=1", self, "apply", "--local", o.configFile}, remoteExtra(o)...)
	if !prompt.IsTerminal(os.Stdin) {
		args = append([]string{"-n"}, args...)
	}
	out, errOut, done := applyView(cfg, o, stdout, stderr)
	defer done()
	cmd := exec.Command("sudo", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, errOut
	return exitCode(cmd.Run(), errOut)
}

func (realRunner) impact(ctx context.Context, cfg *config.Config, o options) (impactReport, error) {
	o.impact, o.json = true, true
	var out, errOut bytes.Buffer
	code := 0
	switch {
	case cfg.Target.Provider == config.ProviderSSH:
		code = runSSH(ctx, "plan", cfg, addr{cfg.Target.SSH.Host, cfg.Target.SSH.Port}, o, &out, &errOut)
	case os.Geteuid() == 0:
		code = runLocal(ctx, "plan", cfg, o, &out, &errOut)
	default:
		// The config is not written yet: the impact runs on a copy.
		data, err := cfg.Marshal()
		if err != nil {
			return impactReport{}, err
		}
		f, err := os.CreateTemp("", "keelbox-impact-*.yaml")
		if err != nil {
			return impactReport{}, err
		}
		defer os.Remove(f.Name())
		if _, err := f.Write(data); err != nil {
			f.Close()
			return impactReport{}, err
		}
		f.Close()
		self, err := os.Executable()
		if err != nil {
			return impactReport{}, err
		}
		cmd := exec.CommandContext(ctx, "sudo", "-n", "env", envChild+"=1", self, "plan", "--local", f.Name(), "--impact", "--json")
		cmd.Stdout, cmd.Stderr = &out, &errOut
		code = exitCode(cmd.Run(), &errOut)
	}
	if code != 0 && code != 1 {
		return impactReport{}, fmt.Errorf("plan --impact exited %d: %s", code, strings.TrimSpace(errOut.String()))
	}
	var r impactReport
	b := out.Bytes()
	if i := bytes.IndexByte(b, '{'); i >= 0 {
		b = b[i:]
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Impact != 1 {
		return impactReport{}, fmt.Errorf("plan --impact gave no report: %s", strings.TrimSpace(errOut.String()))
	}
	return r, nil
}

func (realRunner) reachable(ctx context.Context, cfg *config.Config, host, user string) bool {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := sshTarget(cfg, addr{host, cfg.Target.SSH.Port}, options{sshUser: user}, io.Discard, io.Discard).Output(cctx, "true")
	return err == nil
}

func (realRunner) firstContact(ctx context.Context, cfg *config.Config, hostKey string, stdout io.Writer) error {
	a := addr{cfg.Target.SSH.Host, cfg.Target.SSH.Port}
	s := sshTarget(cfg, a, options{}, stdout, os.Stderr)
	fmt.Fprintf(stdout, "keelbox: waiting for SSH at %s", a)
	if hostKey != "" {
		keys := provider.ParseHostKeys(hostKey)
		if len(keys) == 0 {
			return fmt.Errorf("the saved host key is not a public key line")
		}
		// Pinned before the first packet: the server should present only
		// the key keelbox generated for it.
		if err := s.Pin(keys); err != nil {
			return err
		}
		fmt.Fprintf(stdout, " (host key %s, from the cloud-config)", keys[0].Fingerprint())
	}
	fmt.Fprintln(stdout)
	deadline := time.Now().Add(10 * time.Minute)
	for dialable(ctx, a, 5*time.Second) != nil {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s does not answer on port %d after 10 minutes", a.host, a.port)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	if hostKey == "" {
		if err := s.TrustHost(ctx); err != nil {
			return err
		}
	}
	if err := s.WaitReachable(ctx, 5*time.Minute); err != nil {
		return fmt.Errorf("%w (the cloud-config creates %s with your key; was it pasted into the user data field?)", err, cfg.Users.Admin.Name)
	}
	// cloud-init exits 2 for "done with recoverable errors"; only the wait matters.
	_, _ = s.Output(ctx, "cloud-init status --wait >/dev/null 2>&1 || true")
	if hostKey != "" {
		if err := s.RotateHostKeys(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "keelbox: host keys rotated and pinned")
	}
	return nil
}

func (realRunner) notSignedIn(ctx context.Context, cfg *config.Config, o options) (left, notChecked []string) {
	for _, s := range signInStates(ctx, cfg, o, "", io.Discard) {
		if s.unchecked != nil {
			notChecked = append(notChecked, fmt.Sprintf("the %s sign-in was not checked: %v. Then: keelbox login %s %s", signInTitle(s.name), s.unchecked, cfg.Box, s.name))
		}
		if !s.done {
			left = append(left, s.name)
		}
	}
	return left, notChecked
}

func (realRunner) login(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, stdout, stderr io.Writer) loginResult {
	return signInAll(ctx, cfg, o, "", p, stdout, stderr)
}

func (realRunner) remoteControl(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, stdout, stderr io.Writer) bool {
	agent, err := boxShell(ctx, cfg, o, cfg.Users.Agent.Name, stdout, stderr)
	if err != nil {
		fail(stderr, err)
		return false
	}
	return remoteControlLogin(ctx, cfg, agent, p, signInView{st: p.Style(), verbose: o.verbose}, stdout, stderr)
}

func (realRunner) skills(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, stdout, stderr io.Writer) {
	home, err := skillHome()
	if err != nil || !p.Interactive() || len(localSkills(home)) == 0 {
		return
	}
	o.list, o.skill = false, ""
	cmdSkills(ctx, cfg, o, p, stdout, stderr)
}

func (realRunner) copyID(ctx context.Context, cfg *config.Config, user, pubPath string, out io.Writer) error {
	// The host key was pinned by the check that found the key missing, so
	// ssh-copy-id checks it strictly against keelbox's known_hosts.
	kh := provider.DefaultKnownHosts()
	if f := cfg.Target.SSH.KnownHosts; f != "" {
		kh = config.ExpandHome(f)
	}
	// -f: the check just saw the key refused, so skip ssh-copy-id's own
	// login with it. That login is one more failed authentication, and
	// OpenSSH 10's PerSourcePenalties then resets the next connection,
	// the one that would ask for the password.
	cmd := exec.CommandContext(ctx, "ssh-copy-id", "-f", "-i", pubPath,
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+kh,
		"-p", fmt.Sprint(firstPort(cfg.Target.SSH.Port)), user+"@"+cfg.Target.SSH.Host)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, out
	return cmd.Run()
}

// --- SSH keys on this machine -------------------------------------------

// sshKey is a key pair on this machine.
type sshKey struct {
	privPath, pubPath, public string
	isDefault                 bool // one ssh tries without -i
}

// defaultKeyNames are the keys ssh offers without -i, in its order.
var defaultKeyNames = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

// findKey returns the key named by --ssh-key (private or .pub path), else
// the first default key in ~/.ssh, else nil.
func findKey(flag string) (*sshKey, error) {
	if flag != "" {
		p := config.ExpandHome(flag)
		priv := strings.TrimSuffix(p, ".pub")
		pub, err := readPublicKey(priv + ".pub")
		if err != nil {
			return nil, fmt.Errorf("--ssh-key %s: %w; %s", flag, err, keysHere())
		}
		return &sshKey{privPath: priv, pubPath: priv + ".pub", public: pub, isDefault: isDefaultKey(priv)}, nil
	}
	for _, n := range defaultKeyNames {
		priv := config.ExpandHome("~/.ssh/" + n)
		if pub, err := readPublicKey(priv + ".pub"); err == nil {
			return &sshKey{privPath: priv, pubPath: priv + ".pub", public: pub, isDefault: true}, nil
		}
	}
	return nil, nil
}

// keysHere names the key pairs in ~/.ssh, for an error about a wrong one.
func keysHere() string {
	pubs, _ := filepath.Glob(config.ExpandHome("~/.ssh/*.pub"))
	var names []string
	for _, p := range pubs {
		if _, err := os.Stat(strings.TrimSuffix(p, ".pub")); err == nil {
			names = append(names, tildePath(strings.TrimSuffix(p, ".pub")))
		}
	}
	if len(names) == 0 {
		return "there is no key pair in ~/.ssh; create one: ssh-keygen -t ed25519"
	}
	return fmt.Sprintf("key pairs in ~/.ssh: %s; name one: --ssh-key %s", strings.Join(names, ", "), names[0])
}

func isDefaultKey(priv string) bool {
	for _, n := range defaultKeyNames {
		if priv == config.ExpandHome("~/.ssh/"+n) {
			return true
		}
	}
	return false
}

// readPublicKey reads the first OpenSSH public key line of a file.
func readPublicKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if len(provider.ParseHostKeys(l)) == 1 && !strings.Contains(l, "PRIVATE KEY") {
			return l, nil
		}
		return "", fmt.Errorf("%s is not an OpenSSH public key", path)
	}
	return "", fmt.Errorf("%s is empty", path)
}

// authorizedKeys are the keys that may log in to this account now.
func authorizedKeys() []string {
	f, err := os.Open(config.ExpandHome("~/.ssh/authorized_keys"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		f := strings.Fields(l)
		// Lines with options (from=, command=) are left out: the box's
		// accounts get plain keys.
		if len(f) >= 2 && (strings.HasPrefix(f[0], "ssh-") || strings.HasPrefix(f[0], "ecdsa-sha2-") || strings.HasPrefix(f[0], "sk-")) {
			out = append(out, l)
		}
	}
	return out
}

// makeKey creates ~/.ssh/id_ed25519 with ssh-keygen on the user's
// terminal, so the user chooses the passphrase, then offers ssh-add.
func (w *wizard) makeKey(ctx context.Context) (*sshKey, error) {
	priv := config.ExpandHome("~/.ssh/id_ed25519")
	w.hint("No SSH key in ~/.ssh. keelbox logs in to the box with one; ssh-keygen can make it now.")
	ok, err := w.p.Confirm("Create "+tildePath(priv)+" with ssh-keygen (you choose a passphrase)?", true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no SSH key; create one (ssh-keygen -t ed25519) or name one: %s", w.upCommand(map[string]string{"ssh-key": "PATH"}))
	}
	if err := os.MkdirAll(filepath.Dir(priv), 0o700); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-t", "ed25519", "-f", priv, "-C", "keelbox-"+suggestName(laptopName()))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, w.out, w.errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh-keygen: %w", err)
	}
	pub, err := readPublicKey(priv + ".pub")
	if err != nil {
		return nil, err
	}
	k := &sshKey{privPath: priv, pubPath: priv + ".pub", public: pub, isDefault: true}
	if hasPassphrase(ctx, k) && os.Getenv("SSH_AUTH_SOCK") != "" {
		w.hint("keelbox runs ssh without prompts, so the key goes in your ssh agent: ssh-add asks for the passphrase once.")
		args := []string{priv}
		if runtime.GOOS == "darwin" {
			args = []string{"--apple-use-keychain", priv}
		}
		add := exec.CommandContext(ctx, "ssh-add", args...)
		add.Stdin, add.Stdout, add.Stderr = os.Stdin, w.out, w.errOut
		_ = add.Run()
	}
	return k, nil
}

func hasPassphrase(ctx context.Context, k *sshKey) bool {
	return exec.CommandContext(ctx, "ssh-keygen", "-y", "-P", "", "-f", k.privPath).Run() != nil
}

// keyAgentNote warns when keelbox's prompt-free ssh could not use the key:
// it has a passphrase and no agent holds it.
func keyAgentNote(ctx context.Context, k *sshKey) string {
	if _, err := os.Stat(k.privPath); err != nil || !hasPassphrase(ctx, k) {
		return ""
	}
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		out, _ := exec.CommandContext(ctx, "ssh-add", "-L").Output()
		if strings.Contains(string(out), strings.Join(strings.Fields(k.public)[:2], " ")) {
			return ""
		}
	}
	return "the key has a passphrase and is not in your ssh agent; keelbox runs ssh without prompts, so first run: ssh-add " + tildePath(k.privPath)
}

func (realRunner) sudo(ctx context.Context, ask bool, out io.Writer) bool {
	if exec.CommandContext(ctx, "sudo", "-n", "true").Run() == nil {
		return true
	}
	if !ask {
		return false
	}
	// sudo -v asks for the password on the terminal and caches it, so the
	// apply that follows runs without asking again.
	cmd := exec.CommandContext(ctx, "sudo", "-v")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, out
	return cmd.Run() == nil && exec.CommandContext(ctx, "sudo", "-n", "true").Run() == nil
}

func (realRunner) inspect(ctx context.Context, cfg *config.Config, user string, out io.Writer) (machineFacts, error) {
	if cfg == nil {
		b, err := exec.CommandContext(ctx, "sh", "-c", probeScript).Output()
		if err != nil {
			return machineFacts{}, err
		}
		return parseProbe(string(b)), nil
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	s := sshTarget(cfg, addr{cfg.Target.SSH.Host, cfg.Target.SSH.Port}, options{sshUser: user}, out, out)
	// Whether anything answers comes first: ssh-keyscan's own failure says
	// nothing about why.
	if err := dialable(cctx, addr{s.Host, s.Port}, 10*time.Second); err != nil {
		return machineFacts{}, sshProblem(err, s.User, s.Host, s.Port)
	}
	if err := s.TrustHost(cctx); err != nil {
		return machineFacts{}, sshProblem(err, s.User, s.Host, s.Port)
	}
	b, err := s.Output(cctx, "sh -c "+quoteSh(probeScript))
	if err != nil {
		if want := imageAccount(b); want != "" && want != s.User {
			return machineFacts{}, &wrongAccount{host: s.Host, user: s.User, want: want}
		}
		return machineFacts{}, sshProblem(err, s.User, s.Host, s.Port)
	}
	return parseProbe(b), nil
}

// wrongAccount is a server that takes the key but not for this account: a
// cloud image whose root logs in only to be told which account to use.
type wrongAccount struct {
	host, user, want string
}

func (e *wrongAccount) Error() string {
	return fmt.Sprintf("%s does not let %s log in: its image says to log in as %s", e.host, e.user, e.want)
}

var imageAccountRe = regexp.MustCompile(`Please login as the user "([a-z_][a-z0-9_-]{0,31})" rather than the user "`)

// imageAccount is the account an Ubuntu cloud image names when root logs
// in: cloud-init gives root's key a forced command that prints the line,
// waits and exits 142, instead of the command asked for. out is what the
// failed probe printed; "" when it names no account.
func imageAccount(out string) string {
	if m := imageAccountRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

func quoteSh(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// keyRefused is a server that answers SSH but does not take the user's key
// for the account: up offers to put the key there.
type keyRefused struct {
	host, user, dest string
}

func (e *keyRefused) Error() string {
	return fmt.Sprintf("%s does not accept your SSH key for %s.", e.host, e.user)
}

// noAnswer is a machine that cannot be reached at all, whichever account
// logs in: its name does not resolve, or its SSH port refuses or does not
// answer.
type noAnswer struct{ msg string }

func (e *noAnswer) Error() string { return e.msg }

// sshProblem turns what ssh said into what to do about it.
func sshProblem(err error, user, host string, port int) error {
	msg := err.Error()
	low := strings.ToLower(msg)
	dest := user + "@" + host
	if port != 0 && port != 22 {
		dest = "-p " + fmt.Sprint(port) + " " + dest
	}
	var hostKey *provider.HostKeyError
	switch {
	case errors.As(err, &hostKey):
		return err // about the server's key, not the user's: the caller says what to pass
	case errors.Is(err, provider.ErrHostKeyUnconfirmed):
		return fmt.Errorf("the host key of %s was not confirmed, so keelbox did not connect. Compare it with the provider's console, or with ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the server, then run it again", host)
	case strings.Contains(msg, "IDENTIFICATION HAS CHANGED") || strings.Contains(msg, "Host key verification failed"):
		return fmt.Errorf("%s offers a different SSH host key from the one keelbox pinned. If the server was reinstalled, check its new key in the provider's console, then forget the old one: ssh-keygen -R %s -f %s", host, host, tildePath(provider.DefaultKnownHosts()))
	case strings.Contains(msg, "Permission denied"):
		return &keyRefused{host: host, user: user, dest: dest}
	case strings.Contains(msg, "Could not resolve") || strings.Contains(low, "no such host"):
		return &noAnswer{fmt.Sprintf("%s does not resolve to an address; check the name, or use the IP address", host)}
	case strings.Contains(low, "connection refused"):
		return &noAnswer{fmt.Sprintf("%s refuses SSH on port %d; check SSH runs there (sudo systemctl status ssh) and the port: ssh %s", host, firstPort(port), dest)}
	case strings.Contains(low, "timed out") || strings.Contains(low, "timeout") || strings.Contains(low, "no route") || strings.Contains(low, "deadline exceeded") ||
		strings.Contains(low, "unreachable") || strings.Contains(msg, "ssh-keyscan"):
		return &noAnswer{fmt.Sprintf("%s does not answer on port %d; check it is on and reachable from here: ssh %s", host, firstPort(port), dest)}
	}
	return fmt.Errorf("%w\nCheck your key logs in: ssh %s true", err, dest)
}

func firstPort(p int) int {
	if p == 0 {
		return 22
	}
	return p
}
