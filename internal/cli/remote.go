package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/ui"
	"github.com/keelbox/keelbox/internal/version"
)

// addr is where keelbox reaches the box's sshd.
type addr struct {
	host string
	port int
}

func (a addr) String() string {
	if a.port == 0 || a.port == 22 {
		return a.host
	}
	return net.JoinHostPort(a.host, strconv.Itoa(a.port))
}

func sshTarget(cfg *config.Config, a addr, o options, stdout, stderr io.Writer) *provider.SSH {
	s := provider.FromConfig(cfg)
	s.Host = a.host
	if a.port != 0 {
		s.Port = a.port
	}
	s.Stdout, s.Stderr = stdout, stderr
	s.Binary = o.binary
	if o.sshUser != "" {
		s.User = o.sshUser
	}
	s.AskSudo = o.askSudo
	s.Outside = outsideAddrs(cfg, o)
	if prompt.IsTerminal(os.Stdin) {
		s.ConfirmKeys = func(host string, offered []provider.HostKey) bool {
			return confirmHostKeys(terminalPrompt(questionCtx(), stdout, o.style), stdout, host, offered)
		}
	}
	return s
}

// outsideAddrs is where the box is reached outside the tailnet: the
// configured target.ssh.host and, for a cloud box, the public address its
// provider reports. A run over the tailnet tells the box these as well as
// the address it connected to, so the box refuses the agent's SSH to its
// public address whichever way keelbox came in
// (docs/adr/0069-agent-ssh-to-outside-addresses.md).
func outsideAddrs(cfg *config.Config, o options) []provider.Addr {
	var out []provider.Addr
	for _, h := range []string{cfg.Target.SSH.Host, o.publicIP} {
		if h != "" {
			out = append(out, provider.Addr{Host: h, Port: cfg.Target.SSH.Port})
		}
	}
	return out
}

func remoteExtra(o options) []string {
	var extra []string
	if o.verbose {
		extra = append(extra, "--verbose")
	}
	if o.allowDowngrade {
		extra = append(extra, "--allow-downgrade")
	}
	if o.adopt {
		extra = append(extra, "--adopt")
	}
	if o.impact {
		extra = append(extra, "--impact")
	}
	if o.json {
		extra = append(extra, "--json")
	}
	if o.list {
		extra = append(extra, "--list")
	}
	if o.overwrite {
		extra = append(extra, "--overwrite")
	}
	if o.target != "" {
		extra = append(extra, "--target", o.target)
	}
	if o.snapshot != "" {
		extra = append(extra, o.snapshot)
	}
	if o.request != "" {
		extra = append(extra, o.request)
	}
	if o.all {
		extra = append(extra, "--all") // requests and approve; status --all never reaches a box
	}
	if o.sub != "" {
		extra = append(extra, o.sub)
	}
	if o.channel != "" {
		extra = append(extra, "--channel", o.channel)
	}
	for _, f := range [][2]string{{"--show", o.show}, {"--delete", o.del}} {
		if f[1] != "" {
			extra = append(extra, f[0], f[1])
		}
	}
	return extra
}

// bootstrapApply runs the first apply as the bootstrap account (root on a
// fresh Hetzner server, the image's default user elsewhere), then applies
// again as the admin account. The second run proves the admin can log in
// and use sudo before anything that would lock the bootstrap account out:
// such steps defer while keelbox runs as the account they lock.
// bootstrap.sshUser names the bootstrap account.
func bootstrapApply(ctx context.Context, cfg *config.Config, a addr, bootstrap options, stdout, stderr io.Writer) int {
	if code := runSSH(ctx, "apply", cfg, a, bootstrap, stdout, stderr); code != 0 {
		return code
	}
	if bootstrap.sshUser != "" && bootstrap.sshUser != cfg.Target.SSH.User {
		admin := bootstrap
		admin.sshUser, admin.askSudo = "", false // the admin's sudo needs no password
		if !bootstrap.style.Live || bootstrap.verbose {
			fmt.Fprintf(stdout, "\nkeelbox: applying again as %s to confirm admin access\n", cfg.Target.SSH.User)
			return runSSH(ctx, "apply", cfg, a, admin, stdout, stderr)
		}
		// On a terminal, the second apply is one line: it changes little
		// besides locking the bootstrap account. Its log is shown if it fails.
		if st := bootstrap.style; st.Live {
			fmt.Fprint(stdout, st.Section("Access"))
			fmt.Fprintf(stdout, "  %s %s\n", st.Dim(st.Pointer()), st.Dim("logging in as "+cfg.Target.SSH.User+" to confirm admin access..."))
		} else {
			fmt.Fprintf(stdout, "\nkeelbox: logging in as %s to confirm admin access...\n", cfg.Target.SSH.User)
		}
		var log bytes.Buffer
		admin.style.Live = false
		if code := runSSH(ctx, "apply", cfg, a, admin, &log, &log); code != 0 {
			_, _ = stdout.Write(log.Bytes())
			return code
		}
		fmt.Fprintln(stdout, adminConfirmed(log.String(), cfg.Target.SSH.User, bootstrap.sshUser, bootstrap.style))
	}
	return 0
}

// adminConfirmed sums up the admin's apply from its log.
func adminConfirmed(log, admin, bootstrap string, st ui.Style) string {
	n, locked, removed := 0, "", ""
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, "  change ") {
			n++
			if strings.Contains(l, " cloud-user-locked:") {
				locked = "; " + bootstrap + " locked"
			}
		}
		if strings.HasPrefix(l, "Removed /etc/sudoers.d/") {
			removed = "\n  " + l
		}
	}
	return fmt.Sprintf("  %s %s logs in with your key and uses sudo%s (%s)%s", st.Tick(), admin, locked, plural(n, "change", "changes"), removed)
}

// applyBox applies over the tailnet when this machine knows and reaches the
// box there, otherwise over the public address (as the bootstrap account
// first when --ssh-user is given). After an apply over the public address
// it tries to close public SSH (closeOverTailnet). It returns the exit code
// and the address the last apply used.
func applyBox(ctx context.Context, cfg *config.Config, public addr, o options, stdout, stderr io.Writer) (int, addr) {
	a := reach(ctx, cfg, public, stderr)
	var code int
	if o.sshUser != "" {
		code = bootstrapApply(ctx, cfg, a, o, stdout, stderr)
	} else {
		code = runSSH(ctx, "apply", cfg, a, o, stdout, stderr)
	}
	if code == 0 {
		saveAppliedNote(cfg.Box, version.Version, time.Now())
	}
	if code != 0 || a != public {
		return code, a
	}
	return closeOverTailnet(ctx, cfg, public, o, stdout, stderr)
}

func runSSH(ctx context.Context, cmd string, cfg *config.Config, a addr, o options, stdout, stderr io.Writer) int {
	s := sshTarget(cfg, a, o, stdout, stderr)
	switch {
	case cmd == "apply":
		// The apply runs on the box detached from this session; this
		// machine follows it (follow.go).
		return applyDetached(ctx, cfg, s, o, stdout, stderr)
	case cmd == "status" && !o.json && prompt.IsTerminal(os.Stdin) && !o.askSudo:
		// A setup still running is followed first; one this machine left
		// is reported once.
		if err := s.TrustHost(ctx); err != nil {
			return exitCode(sshFailure(ctx, err, cfg, s, o, cmd), stderr)
		}
		if code, done := newFollower(s, cfg, o, stdout, stderr).rejoin(ctx, false); done && code == exitDetached {
			return code
		}
	}
	if o.askSudo {
		// The terminal goes to sudo on the box, then to the run's own
		// output; keelbox here reads none of what passes through.
		if err := s.TrustHost(ctx); err != nil {
			return exitCode(sshFailure(ctx, err, cfg, s, o, cmd), stderr)
		}
		// Said on the terminal itself: stdout may be a buffer (plan --impact).
		fmt.Fprintf(os.Stderr, "keelbox: sudo on %s asks for %s's password (keelbox never sees it)\n", a.host, s.User)
		return exitCode(sshFailure(ctx, s.Run(ctx, cmd, cfg, remoteExtra(o)...), cfg, s, o, cmd), stderr)
	}
	return exitCode(sshFailure(ctx, s.Run(ctx, cmd, cfg, remoteExtra(o)...), cfg, s, o, cmd), stderr)
}

// sshAnswers reports whether anything accepts connections at the box's SSH
// port. Tests replace it.
var sshAnswers = func(ctx context.Context, a addr) error { return dialable(ctx, a, 5*time.Second) }

// keyscanFailed reports whether err is TrustHost getting no host key from a
// host keelbox has not pinned: ssh-keyscan's failure, which exits 1 and
// says nothing about why.
func keyscanFailed(err error) bool { return strings.HasPrefix(err.Error(), "ssh-keyscan ") }

// keyscanFailure says why there was no host key to pin, from a look at the
// port: a name that does not resolve, a port that refuses or does not
// answer, or one that answers without offering a key.
func keyscanFailure(ctx context.Context, err error, s *provider.SSH) error {
	if derr := sshAnswers(ctx, addr{s.Host, s.Port}); derr != nil {
		return sshProblem(derr, s.User, s.Host, s.Port)
	}
	return fmt.Errorf("%s answers on port %d but offered no SSH host key to pin (%v). Check SSH runs there: ssh %s true", s.Host, firstPort(s.Port), err, sshDest(s))
}

// sshDest is the destination as ssh takes it on a command line.
func sshDest(s *provider.SSH) string {
	if s.Port != 0 && s.Port != 22 {
		return "-p " + strconv.Itoa(s.Port) + " " + s.User + "@" + s.Host
	}
	return s.User + "@" + s.Host
}

// sshFailed reports whether err is ssh itself failing (no host key, no
// connection, no login, a connection that broke) and not the command it
// ran on the box: ssh exits 255 for its own errors, and keelbox's ssh calls
// name the destination in theirs. ssh-keyscan comes first: its error wraps
// an exit status of 1.
func sshFailed(err error, s *provider.SSH) bool {
	if keyscanFailed(err) {
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode() == 255
	}
	m := err.Error()
	return strings.HasPrefix(m, "ssh "+s.User+"@"+s.Host+": ") && (strings.Contains(m, "exit status 255") || !strings.Contains(m, "exit status "))
}

// sessionFailure is sshFailure for a session on the user's terminal
// (attach, ssh). ssh printed its own reason there and keelbox read none of
// it, and the session's own shell may itself have ended with 255. So
// keelbox adds words only where it can tell why: no host key to pin, or a
// port that does not answer. Anything else is returned as it is.
func sessionFailure(ctx context.Context, err error, s *provider.SSH) error {
	if err == nil || s == nil || ctx.Err() != nil {
		return err
	}
	var ee *exec.ExitError
	switch {
	case keyscanFailed(err):
		return keyscanFailure(ctx, err, s)
	case errors.As(err, &ee) && ee.ExitCode() == 255:
		if derr := sshAnswers(ctx, addr{s.Host, s.Port}); derr != nil {
			return sshProblem(derr, s.User, s.Host, s.Port)
		}
	}
	return err
}

// sshFailure ends an error from ssh itself with what to do about it, for a
// box that is set up: one that does not answer, an account that no longer
// logs in, a key that is refused. Any other error is returned as it is.
// cmd is the command to name again, "" when its arguments are not known
// here. Where ssh gave no reason, keelbox has only the exit code, and looks
// whether the port answers. The result is one line: callers that show an
// error's last line (destroy's check for work) keep ssh's reason.
func sshFailure(ctx context.Context, err error, cfg *config.Config, s *provider.SSH, o options, cmd string) error {
	if err == nil || s == nil || ctx.Err() != nil {
		return err
	}
	var hk *provider.HostKeyError
	if errors.As(err, &hk) {
		return hostKeyFix(hk, cfg, o, cmd)
	}
	if !sshFailed(err, s) {
		return err
	}
	if keyscanFailed(err) {
		return keyscanFailure(ctx, err, s)
	}
	p := sshProblem(err, s.User, s.Host, s.Port)
	var refused *keyRefused
	if !errors.As(p, &refused) && !errors.Is(p, err) {
		return p // the host key, the name, a port that refuses or does not answer: it says what to check
	}
	dest := sshDest(s)
	var ee *exec.ExitError
	bare := errors.As(err, &ee) && err.Error() == ee.Error() || strings.HasSuffix(err.Error(), "exit status 255")
	if refused == nil && bare {
		if derr := sshAnswers(ctx, addr{s.Host, s.Port}); derr != nil {
			return sshProblem(derr, s.User, s.Host, s.Port)
		}
	}
	box := boxRef(cfg, o.configFile)
	// Only for an account the user named: keelbox picks one itself for a
	// new cloud server and for keelbox up's look at a machine.
	if u := o.sshUser; o.set["ssh-user"] && u != "" && u == s.User && u != cfg.Target.SSH.User && !o.inUp {
		again, said := "and run it again", ""
		switch cmd {
		case "plan", "apply", "doctor", "status", "rollback", "backup":
			again = "keelbox " + cmd + " " + box
		}
		if refused == nil && !bare {
			said = " (" + err.Error() + ")"
		}
		return fmt.Errorf("%s could not log in to %s%s. If %s is already set up, that is expected: root and the image's account no longer log in there, and keelbox connects as %s. "+
			"Leave out --ssh-user: %s. On a machine that is not set up yet, check your key logs in: ssh %s true", u, s.Host, said, box, cfg.Target.SSH.User, again, dest)
	}
	switch {
	case refused != nil:
		return fmt.Errorf("%s does not accept your SSH key for %s. Check which key logs in: ssh %s true", s.Host, s.User, dest)
	case bare:
		return fmt.Errorf("ssh %s ended with exit status 255. Check it logs in: ssh %s true", dest, dest)
	}
	// What ssh said, then what to try, on one line.
	return fmt.Errorf("%s. Check your key logs in: ssh %s true", strings.TrimSuffix(err.Error(), "."), dest)
}

func exitCode(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 && ee.ExitCode() != 255 {
		return ee.ExitCode() // keelbox on the box already explained itself
	}
	fail(stderr, err)
	return 1
}

func knownHosts(cfg *config.Config) string {
	if cfg.Target.SSH.KnownHosts != "" {
		return config.ExpandHome(cfg.Target.SSH.KnownHosts)
	}
	return provider.DefaultKnownHosts()
}

// printNextSteps points at the user's own steps after a first apply.
func printNextSteps(out io.Writer, cfg *config.Config, ref string) {
	fmt.Fprintf(out, "\nNext:\n")
	fmt.Fprintf(out, "  keelbox login %s     sign in to Tailscale and each agent CLI; keelbox never sees the credentials\n", ref)
	if cfg.Tmux() {
		fmt.Fprintf(out, "  keelbox attach %s    open the agent's tmux session\n", ref)
	}
}

// forLaptop reports whether this run is the box's half of a command a
// laptop started over SSH: --local with the config the laptop staged.
func forLaptop(o options) bool {
	return o.local && o.configFile == provider.RemoteConfig
}

// hostKeyFix ends a refused host key with where to set the key and the
// command to run again. As in connectFix, the fingerprint is not filled in
// from what the server offers: comparing it is the user's step.
func hostKeyFix(hk *provider.HostKeyError, cfg *config.Config, o options, cmd string) error {
	path := o.configFile
	if path == "" {
		path = boxConfigPath(cfg.Box)
	}
	why := fmt.Sprintf("%s is not a host keelbox has connected to, and no terminal is attached to confirm its host key. It offers %s.", hk.Host, hk.Fingerprints())
	if hk.Want != "" {
		why = fmt.Sprintf("%s does not offer the host key %s that target.ssh.host_key pins; it offers %s. keelbox did not connect; if the server was reinstalled, that is expected.", hk.Host, hk.Want, hk.Fingerprints())
	}
	return &hostKeyFixed{hk: hk, msg: fmt.Sprintf("%s Read the server's key in %s. In %s, under target.ssh, set host_key: %s to that key. Then run: keelbox %s %s",
		why, compareHostKey, tildePath(path), hostKeyPlaceholder, cmd, boxRef(cfg, o.configFile))}
}

// hostKeyFixed is a refused host key with its fix; it unwraps to the
// refusal, so callers that say more about it (connectFix) still find it.
type hostKeyFixed struct {
	hk  *provider.HostKeyError
	msg string
}

func (e *hostKeyFixed) Error() string { return e.msg }
func (e *hostKeyFixed) Unwrap() error { return e.hk }
