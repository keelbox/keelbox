// Package provider gets keelbox onto a box and runs it there. local runs in
// this process; ssh copies this binary and the resolved config to a machine
// and runs it under sudo; hetzner creates the machine through the Hetzner
// Cloud API, then uses ssh.
package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/release"
	"github.com/keelbox/keelbox/internal/runs"
	"github.com/keelbox/keelbox/internal/version"
)

// Remote paths. /run is tmpfs: the staged config and any auth key vanish at
// reboot even if cleanup is interrupted. It is also mounted noexec, so the
// uploaded binary is staged under /var/lib/keelbox. keelbox runs from the
// stage; only apply installs itself to /usr/local/bin (modules.BinaryPath),
// so doctor or status from an older laptop never replaces the box's binary.
const (
	RemoteStage   = "/run/keelbox"
	RemoteBinary  = "/var/lib/keelbox/stage/keelbox"
	RemoteConfig  = RemoteStage + "/keelbox.yaml"
	RemoteAuthKey = RemoteStage + "/tailscale.authkey"
	// Backup secrets, staged for the backup module, which keeps root-only
	// copies under /etc/keelbox/backup.
	RemoteBackupCredentials = RemoteStage + "/backup.credentials"
	RemoteBackupPassword    = RemoteStage + "/backup.password"
)

// RemoteNotifySecret is where a notification channel's secret file is
// staged, such as /run/keelbox/notify.phone.topic; the notify module keeps
// root-only copies under /etc/keelbox/notify.
func RemoteNotifySecret(channel, key string) string {
	return RemoteStage + "/" + notifyStagePrefix + channel + "." + strings.TrimSuffix(key, "_file")
}

// notifyStagePrefix starts every staged notification secret's name. The
// names are a channel name (lowercase letters, digits and hyphens) and a
// fixed key, so the remote script can install them by pattern.
const notifyStagePrefix = "notify."

// stagedSecrets are the secret files a command may upload, by their name in
// the stage. Each is installed 0600 under RemoteStage and removed after the
// command, whatever its outcome.
var stagedSecrets = []string{"tailscale.authkey", "backup.credentials", "backup.password"}

// Secret is a local secret file and its staged path on the box.
type Secret struct {
	Local, Remote string
}

// SSH reaches a box with the system ssh client, so the user's agent, config
// and hardware keys all work. Host keys live in a keelbox-specific
// known_hosts file and are checked strictly; TrustHost pins a new box's key
// explicitly before the first connection.
type SSH struct {
	Host         string
	Port         int
	User         string
	IdentityFile string
	KnownHosts   string
	HostKey      string // expected SHA256 fingerprint for a host not yet pinned
	Binary       string // keelbox binary to upload; default: this executable
	Stdout       io.Writer
	Stderr       io.Writer
	// ConfirmKeys shows the user the host keys a server offers at the first
	// connection and asks whether to trust and pin them; nil when no
	// terminal is attached, so an unknown host key is refused. How they
	// are shown and asked is the caller's: this package asks nothing.
	ConfirmKeys func(host string, offered []HostKey) bool
	// ConnectTimeout is ssh's, in seconds; 0 means 10.
	ConnectTimeout int
	// LostAfter is how long a session on the terminal goes without an
	// answer from the box before ssh gives the connection up: two
	// keepalives, half of it apart. 0 keeps the 45 seconds every other
	// connection has (three, 15 seconds apart), which suits a session that
	// cannot be opened again; keelbox attach, which can, sets it shorter.
	LostAfter time.Duration
	// AskSudo runs the command on the box under one sudo, on this
	// process's terminal (ssh -t), for an account whose sudo asks for a
	// password: sudo reads it there itself, and keelbox never sees it
	// (docs/adr/0049-password-sudo-over-ssh.md). An apply's output goes
	// straight to the terminal; another command's stdout is kept in a file
	// on the box and read back afterwards, so nothing typed passes through
	// keelbox.
	AskSudo bool
	// Outside are the box's other known addresses besides Host: where it
	// is reached outside the tailnet, from the config or the cloud's API,
	// when Host is its tailnet address. The box is told them with Host
	// (EnvSSHAddrs); nothing connects to them here.
	Outside []Addr

	controlDir string
}

// FromConfig builds an SSH target from target.ssh.
func FromConfig(c *config.Config) *SSH {
	s := c.Target.SSH
	return &SSH{Host: s.Host, Port: s.Port, User: s.User, IdentityFile: s.IdentityFile, KnownHosts: s.KnownHosts,
		HostKey: s.HostKey, Stdout: os.Stdout, Stderr: os.Stderr}
}

// DefaultKnownHosts is keelbox's own known_hosts file.
func DefaultKnownHosts() string { return config.ExpandHome("~/.config/keelbox/known_hosts") }

func (s *SSH) knownHosts() string {
	if s.KnownHosts != "" {
		return config.ExpandHome(s.KnownHosts)
	}
	return DefaultKnownHosts()
}

func (s *SSH) baseOpts() []string {
	kh := s.knownHosts()
	_ = os.MkdirAll(filepath.Dir(kh), 0o700)
	opts := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + kh,
		"-o", "ConnectTimeout=" + strconv.Itoa(connectTimeout(s.ConnectTimeout)),
		"-o", "ServerAliveInterval=15",
		"-o", "LogLevel=ERROR",
	}
	if s.controlDir != "" {
		// One TCP connection for the whole run.
		opts = append(opts, "-o", "ControlMaster=auto", "-o", "ControlPath="+s.controlDir+"/%C", "-o", "ControlPersist=60")
	}
	if s.IdentityFile != "" {
		opts = append(opts, "-i", config.ExpandHome(s.IdentityFile), "-o", "IdentitiesOnly=yes")
	}
	return opts
}

func connectTimeout(n int) int {
	if n <= 0 {
		return 10
	}
	return n
}

func (s *SSH) dest() string { return s.User + "@" + s.Host }

// scpDest is dest for scp, whose "host:path" needs an IPv6 address in
// brackets: user@[2001:db8::1]:path. ssh takes the address bare.
func (s *SSH) scpDest() string {
	if strings.Contains(s.Host, ":") {
		return s.User + "@[" + s.Host + "]"
	}
	return s.dest()
}

func (s *SSH) sshArgs(remote string) []string {
	args := append(s.baseOpts(), "-p", strconv.Itoa(s.Port), s.dest(), "--", remote)
	return args
}

// Output runs a remote command and returns its stdout.
func (s *SSH) Output(ctx context.Context, remote string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "ssh", s.sshArgs(remote)...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if e := strings.TrimSpace(errb.String()); e != "" {
			return out.String(), fmt.Errorf("ssh %s: %w: %s", s.dest(), err, e)
		}
		return out.String(), fmt.Errorf("ssh %s: %w", s.dest(), err)
	}
	return out.String(), nil
}

// Input runs a remote command with in as its input, its output discarded
// unless it fails, when the error carries what it printed to stderr.
func (s *SSH) Input(ctx context.Context, remote string, in io.Reader) error {
	var errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "ssh", s.sshArgs(remote)...)
	cmd.Stdin, cmd.Stderr = in, &errb
	if err := cmd.Run(); err != nil {
		if e := strings.TrimSpace(errb.String()); e != "" {
			return fmt.Errorf("ssh %s: %w: %s", s.dest(), err, e)
		}
		return fmt.Errorf("ssh %s: %w", s.dest(), err)
	}
	return nil
}

// Stream runs a remote command with output passed through.
func (s *SSH) Stream(ctx context.Context, remote string) error {
	cmd := exec.CommandContext(ctx, "ssh", s.sshArgs(remote)...)
	cmd.Stdout, cmd.Stderr = s.Stdout, s.Stderr
	return cmd.Run()
}

// uploadRetryWait is how long upload waits before its second try.
var uploadRetryWait = 2 * time.Second

// upload copies local to the box. A copy that fails is tried once more: a
// connection that drops while the binary goes over is the common cause,
// and the copy changes nothing on the box until it is whole. The error
// after the second failure says what to do.
func (s *SSH) upload(ctx context.Context, local, remote string) error {
	args := append(s.baseOpts(), "-q", "-P", strconv.Itoa(s.Port), local, s.scpDest()+":"+remote)
	var err error
	var errb bytes.Buffer
	for try := 0; try < 2; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(uploadRetryWait):
			}
		}
		errb.Reset()
		cmd := exec.CommandContext(ctx, "scp", args...)
		cmd.Stderr = &errb
		if err = cmd.Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("scp %s: %w: %s (tried twice). Check the connection to %s and run the command again", filepath.Base(local), err, strings.TrimSpace(errb.String()), s.Host)
}

// WaitReachable polls until SSH accepts a command or the timeout passes.
func (s *SSH) WaitReachable(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := s.Output(cctx, "true")
		cancel()
		if err == nil {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("%s not reachable over SSH after %s: %w (check the address and that your key logs in: ssh %s true)", s.Host, timeout, last, s.dest())
}

// ForgetHost removes a host's pinned keys, for an address being reused by a
// new server.
func ForgetHost(knownHosts, host string) {
	_ = exec.Command("ssh-keygen", "-R", host, "-f", knownHosts).Run()
	_ = os.Remove(knownHosts + ".old")
}

func dpkgArch(uname string) (string, error) {
	switch strings.TrimSpace(uname) {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported remote architecture %q", strings.TrimSpace(uname))
}

// binaryFor picks the keelbox binary to upload for a remote architecture
// (docs/adr/0006-laptop-cli-and-box-binary.md): an explicit --binary, this
// executable when it already matches, a keelbox-linux-<arch> built beside it,
// or the same version from the release channel, verified and cached.
func (s *SSH) binaryFor(ctx context.Context, arch string) (string, error) {
	if s.Binary != "" {
		return s.Binary, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == arch {
		return self, nil
	}
	sibling := filepath.Join(filepath.Dir(self), release.AssetName(arch))
	if _, err := os.Stat(sibling); err == nil {
		return sibling, nil
	}
	f, err := release.NewFetcher()
	if err != nil {
		return "", err
	}
	bin, err := f.Binary(ctx, version.Version, arch)
	if errors.Is(err, release.ErrDevBuild) {
		return "", fmt.Errorf("the box is linux/%s and this development keelbox is %s/%s; run make build or pass --binary", arch, runtime.GOOS, runtime.GOARCH)
	}
	if err != nil {
		return "", fmt.Errorf("fetch keelbox %s for linux/%s: %w", version.Version, arch, err)
	}
	return bin, nil
}

// ShipConfig prepares the config for the box: key files inlined, secret
// file paths (the Tailscale auth key, backup credentials and password)
// re-pointed at their staged copies, and the target set to local because on
// the box keelbox applies to itself. It returns the secret files to upload.
func ShipConfig(c *config.Config) (*config.Config, []Secret, error) {
	r, err := c.Resolve()
	if err != nil {
		return nil, nil, err
	}
	var secrets []Secret
	stage := func(key string, p *string, remote string) error {
		if *p == "" {
			return nil
		}
		local := config.ExpandHome(*p)
		fi, err := os.Stat(local)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s %s is readable by others (mode %o); chmod 600 it", key, *p, fi.Mode().Perm())
		}
		secrets = append(secrets, Secret{Local: local, Remote: remote})
		*p = remote
		return nil
	}
	for _, s := range []struct {
		key    string
		p      *string
		remote string
	}{
		{"access.tailscale.auth_key_file", &r.Access.Tailscale.AuthKeyFile, RemoteAuthKey},
		{"backup.credentials_file", &r.Backup.CredentialsFile, RemoteBackupCredentials},
		{"backup.password_file", &r.Backup.PasswordFile, RemoteBackupPassword},
	} {
		if err := stage(s.key, s.p, s.remote); err != nil {
			return nil, nil, err
		}
	}
	// Resolve shares the slice with c: copy it before re-pointing paths.
	r.Notify.Channels = append([]config.Channel(nil), r.Notify.Channels...)
	for i := range r.Notify.Channels {
		ch := &r.Notify.Channels[i]
		for _, f := range ch.SecretFiles() {
			if err := stage(fmt.Sprintf("notify.channels[%d].%s", i, f.Key), f.Path, RemoteNotifySecret(ch.Name, f.Key)); err != nil {
				return nil, nil, err
			}
		}
	}
	r.Target = config.Target{Provider: config.ProviderLocal}
	r.ApplyDefaults()
	return r, secrets, nil
}

// setupErr is an error from getting keelbox onto the box, before it runs
// there. It keeps the message and drops the wrapped *exec.ExitError: a
// caller that finds one takes its code for keelbox's own on the box, which
// has already explained itself, and prints nothing (a failed scp exits 1).
func setupErr(err error) error { return errors.New(err.Error()) }

// Run uploads keelbox and the config and runs `keelbox <command>` on the box
// as root. The staged auth key is deleted whatever the outcome.
func (s *SSH) Run(ctx context.Context, command string, c *config.Config, extra ...string) error {
	if err := s.TrustHost(ctx); err != nil {
		return setupErr(err)
	}
	ctl, err := controlDir(os.TempDir())
	if err != nil {
		return err
	}
	defer os.RemoveAll(ctl)
	s.controlDir = ctl
	defer func() {
		_ = exec.Command("ssh", append(s.baseOpts(), "-O", "exit", "-p", strconv.Itoa(s.Port), s.dest())...).Run()
		s.controlDir = ""
	}()

	uname, err := s.Output(ctx, "uname -m")
	if err != nil {
		return setupErr(err)
	}
	arch, err := dpkgArch(uname)
	if err != nil {
		return err
	}
	bin, err := s.binaryFor(ctx, arch)
	if err != nil {
		return err
	}
	shipped, secrets, err := ShipConfig(c)
	if err != nil {
		return err
	}
	data, err := shipped.Marshal()
	if err != nil {
		return err
	}
	local, err := os.MkdirTemp("", "keelbox-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(local)
	cfgFile := filepath.Join(local, "keelbox.yaml")
	if err := os.WriteFile(cfgFile, data, 0o600); err != nil {
		return err
	}

	stage, err := s.Output(ctx, "mktemp -d")
	if err != nil {
		return setupErr(err)
	}
	stage = strings.TrimSpace(stage)
	if !strings.HasPrefix(stage, "/tmp/") {
		return fmt.Errorf("unexpected remote temp dir %q", stage)
	}
	for _, f := range [][2]string{{bin, stage + "/keelbox"}, {cfgFile, stage + "/keelbox.yaml"}} {
		if err := s.upload(ctx, f[0], f[1]); err != nil {
			return setupErr(err)
		}
	}
	for _, sec := range secrets {
		if err := s.upload(ctx, sec.Local, stage+"/"+filepath.Base(sec.Remote)); err != nil {
			return setupErr(err)
		}
	}
	addrs := s.sshAddrs(ctx)
	if s.AskSudo {
		return s.runAskingSudo(ctx, stage, command, extra, addrs)
	}
	return s.Stream(ctx, RemoteScript(stage, command, extra, addrs))
}

// runAskingSudo runs the remote script under one sudo on a terminal. ssh
// gets this process's own file descriptors, as Interactive does, so the
// password goes from the keyboard to sudo on the box. The command's stdout
// is kept in a file on the box and read back afterwards. It runs without
// ctx: on the terminal Ctrl-C reaches the box's keelbox. An apply only
// starts the box's detached run here; the laptop follows it afterwards
// as the same account, without sudo.
func (s *SSH) runAskingSudo(ctx context.Context, stage, command string, extra []string, addrs string) error {
	out := stage + ".out"
	args := append(s.baseOpts(), "-t", "-p", strconv.Itoa(s.Port), s.dest(), "--", AskSudoScript(stage, command, extra, addrs, out))
	cmd := exec.Command("ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	got, ferr := s.Output(ctx, "cat "+out+"; rm -f "+out)
	if _, werr := io.WriteString(s.Stdout, got); ferr == nil {
		ferr = werr
	}
	if err != nil {
		return err
	}
	return ferr
}

// AskSudoScript runs RemoteScript under a single sudo, which asks for the
// password on the session's terminal. Inside, the script's own sudo -n
// calls run as root and need none. The session's $SSH_CONNECTION is passed
// on, since sudo resets the environment. The shell ignores SIGINT, so a
// Ctrl-C on the terminal reaches keelbox, which stops after its current
// step, and the clean-up after it still runs. With out set, the command's
// stdout goes to that file (mode 600, the account's own), and its stderr
// stays on the terminal.
func AskSudoScript(stage, command string, extra []string, addrs, out string) string {
	redirect := ""
	if out != "" {
		redirect = " >" + shellQuote(out)
	}
	inner := "trap '' INT\n" + RemoteScript(stage, command, extra, addrs)
	return `umask 077; exec sudo env "SSH_CONNECTION=$SSH_CONNECTION" sh -c ` + shellQuote(inner) + redirect
}

// Follow runs a long remote command with its stdout passed to w, in its
// own process group and on a connection of its own, so the terminal's
// Ctrl-C does not end it and keelbox decides what happens. Cancelling ctx
// ends it.
func (s *SSH) Follow(ctx context.Context, remote string, w io.Writer) error {
	own := *s
	own.controlDir = ""
	args := append(own.baseOpts(), "-o", "ControlMaster=no", "-o", "ControlPath=none", "-p", strconv.Itoa(s.Port), s.dest(), "--", remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.SysProcAttr = followAttr()
	var errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, w, &errb
	if err := cmd.Run(); err != nil {
		if e := strings.TrimSpace(errb.String()); e != "" {
			return fmt.Errorf("ssh %s: %w: %s", s.dest(), err, e)
		}
		return fmt.Errorf("ssh %s: %w", s.dest(), err)
	}
	return nil
}

// RemoveStagedSecrets removes the secret files a laptop staged in
// RemoteStage, on the box. A detached apply calls it when it ends: its
// own upload left them for it.
func RemoveStagedSecrets() {
	for _, n := range stagedSecrets {
		_ = os.Remove(RemoteStage + "/" + n)
	}
	notify, _ := filepath.Glob(RemoteStage + "/" + notifyStagePrefix + "*")
	for _, f := range notify {
		_ = os.Remove(f)
	}
}

// RemoteScript is the shell run on the box after upload. Arguments are
// quoted for the remote shell. keelbox on the box gets this session's
// $SSH_CONNECTION, which sshd sets: an apply that arrived over the tailnet
// is how the box knows the tailnet works (docs/adr/0021-close-public-ssh.md).
// addrs, when set, is where this machine reaches the box's sshd
// (EnvSSHAddrs), which the box cannot see for itself behind address
// translation.
//
// An apply only starts the box's detached run (KEELBOX_DETACH), which keeps
// the staged secrets until it ends and removes them then; every command
// leaves the secrets in place while a detached run is active
// (docs/adr/0063-detached-apply.md).
func RemoteScript(stage, command string, extra []string, addrs string) string {
	detach := ""
	if addrs != "" {
		detach = " " + shellQuote(EnvSSHAddrs+"="+addrs)
	}
	if command == "apply" {
		detach += " KEELBOX_DETACH=1"
	}
	words := []string{command}
	for _, a := range extra {
		words = append(words, shellQuote(a))
	}
	var install, remove strings.Builder
	for _, n := range stagedSecrets {
		fmt.Fprintf(&install, "if [ -f \"$s/%[1]s\" ]; then sudo -n install -m 0600 \"$s/%[1]s\" %[2]s/%[1]s; fi\n", n, RemoteStage)
		fmt.Fprintf(&remove, "%s/%s ", RemoteStage, n)
	}
	// Notification secrets have one file per channel and key.
	fmt.Fprintf(&install, "for f in \"$s\"/%[1]s*; do if [ -f \"$f\" ]; then sudo -n install -m 0600 \"$f\" %[2]s/\"${f##*/}\"; fi; done\n", notifyStagePrefix, RemoteStage)
	return fmt.Sprintf(`set -e
s=%[1]s
sudo -n install -d -m 0700 %[2]s "$(dirname %[3]s)"
sudo -n install -m 0755 "$s/keelbox" %[3]s
sudo -n install -m 0600 "$s/keelbox.yaml" %[4]s
%[5]srm -rf "$s"
rc=0
sudo -n env "KEELBOX_SSH_CONNECTION=$SSH_CONNECTION"%[8]s %[3]s %[6]s --local %[4]s || rc=$?
sudo -n rm -f %[4]s
if ! systemctl list-units --quiet --no-legend --state=active,activating '%[10]s*' 2>/dev/null | grep -q .; then
sudo -n rm -f %[7]s
sudo -n sh -c 'rm -f %[2]s/%[9]s*'
fi
exit $rc`, stage, RemoteStage, RemoteBinary, RemoteConfig, install.String(), strings.Join(words, " "), remove.String(), detach, notifyStagePrefix, runs.UnitPrefix)
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./=:@+-]+$`)

// shellQuote quotes a word for sh unless it is plainly safe.
func shellQuote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Interactive runs a remote command on a terminal (ssh -t), with this
// process's own stdin, stdout and stderr handed to ssh as file descriptors.
// keelbox never reads what passes through: sign-in flows and sessions stay
// between the user and the box.
func (s *SSH) Interactive(ctx context.Context, remote string, stdin, stdout, stderr *os.File) error {
	_, err := s.interactive(ctx, remote, false, stdin, stdout, stderr)
	return err
}

// InteractiveOpened is Interactive, and reports whether the session was
// opened: ssh connected and logged in before it ended. ssh's exit code is
// 255 both for a connection it could not make and for one it lost, and
// keelbox reads nothing of the session, so ssh is asked to say so itself:
// its LocalCommand, which it runs on this computer once it has logged in,
// makes an empty file in a directory of keelbox's.
func (s *SSH) InteractiveOpened(ctx context.Context, remote string, stdin, stdout, stderr *os.File) (opened bool, err error) {
	return s.interactive(ctx, remote, true, stdin, stdout, stderr)
}

// sessionOpts are the options of a session on the terminal: the base
// options, after the keepalives of LostAfter when it is set, since ssh
// keeps the first value it is given for an option.
func (s *SSH) sessionOpts() []string {
	var opts []string
	if s.LostAfter > 0 {
		every := max(int(s.LostAfter/time.Second)/2, 1)
		opts = append(opts, "-o", "ServerAliveInterval="+strconv.Itoa(every), "-o", "ServerAliveCountMax=2")
	}
	return append(opts, s.baseOpts()...)
}

func (s *SSH) interactive(ctx context.Context, remote string, mark bool, stdin, stdout, stderr *os.File) (bool, error) {
	if err := s.TrustHost(ctx); err != nil {
		return false, err
	}
	args := s.sessionOpts()
	opened := func() bool { return false }
	if mark {
		var done func()
		args, opened, done = withOpenedMark(args)
		defer done()
	}
	args = append(args, "-t", "-p", strconv.Itoa(s.Port), s.dest())
	if remote != "" {
		args = append(args, "--", remote)
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	return opened(), err
}

// withOpenedMark adds to ssh's options a LocalCommand that ssh runs on this
// computer once it has logged in, making an empty file: opened reports
// whether it did. done removes the file's directory.
func withOpenedMark(args []string) (out []string, opened func() bool, done func()) {
	opened, done = func() bool { return false }, func() {}
	// ssh hands LocalCommand to the user's shell and expands % in it.
	dir, err := os.MkdirTemp("", "keelbox-session-")
	if err != nil {
		return args, opened, done
	}
	done = func() { _ = os.RemoveAll(dir) }
	if strings.ContainsAny(dir, "'\\\n") {
		return args, opened, done
	}
	marker := filepath.Join(dir, "opened")
	opened = func() bool { _, err := os.Stat(marker); return err == nil }
	return append(args, "-o", "PermitLocalCommand=yes", "-o", "LocalCommand=touch '"+strings.ReplaceAll(marker, "%", "%%")+"'"), opened, done
}

// Exec runs a remote command without a terminal (ssh -T), with in, out and
// errw passed through: keelbox reads none of them. opened is as for
// InteractiveOpened, so that ssh's own 255 can be told from the command's.
// It never asks about a host key: one not pinned already is refused.
func (s *SSH) Exec(ctx context.Context, remote string, in io.Reader, out, errw io.Writer) (opened bool, err error) {
	if !s.IsKnown() {
		return false, fmt.Errorf("no pinned host key for %s", s.Host)
	}
	args, seen, done := withOpenedMark(s.baseOpts())
	defer done()
	args = append(args, "-T", "-p", strconv.Itoa(s.Port), s.dest(), "--", remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errw
	err = cmd.Run()
	return seen(), err
}

// As returns a copy of the target that logs in as another account.
func (s *SSH) As(user string) *SSH {
	c := *s
	c.User = user
	c.controlDir = ""
	return &c
}

// sunPathMax is the smallest Unix socket path limit keelbox runs on: 104
// bytes on macOS and the BSDs, including the terminating NUL; Linux allows 108.
const sunPathMax = 104

// controlDir makes the directory for ssh's connection-sharing socket, named
// by ssh's %C: 40 hex characters. macOS's per-user temporary directory
// (/var/folders/xx/.../T/) is long enough to push that socket path past
// sunPathMax, and ssh then refuses every connection, so fall back to /tmp,
// which every Unix has, when base is too long. The directory is mode 0700.
func controlDir(base string) (string, error) {
	const prefix, socket = "keelbox-ssh-", 40
	// MkdirTemp appends up to 10 random digits; +2 for the two slashes.
	if len(base)+len(prefix)+10+2+socket >= sunPathMax {
		base = "/tmp"
	}
	return os.MkdirTemp(base, prefix)
}
