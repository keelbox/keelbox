package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"

func cfg(t *testing.T, extra string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: ssh, ssh: {host: 192.0.2.1}}\nusers:\n  admin:\n    ssh_authorized_keys: [\""+key+"\"]\n"+extra), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestShipConfig(t *testing.T) {
	dir := t.TempDir()
	kf := filepath.Join(dir, "ts.key")
	if err := os.WriteFile(kf, []byte("tskey-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := cfg(t, "access: {tailscale: {auth_key_file: \""+kf+"\"}}\n")
	shipped, secrets, err := ShipConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0].Local != kf || shipped.Access.Tailscale.AuthKeyFile != RemoteAuthKey {
		t.Errorf("auth key: secrets %v remote %s", secrets, shipped.Access.Tailscale.AuthKeyFile)
	}
	if shipped.Target.Provider != config.ProviderLocal || shipped.Target.SSH.Host != "" {
		t.Errorf("target not reset: %+v", shipped.Target)
	}
	data, _ := shipped.Marshal()
	if strings.Contains(string(data), "tskey-secret") {
		t.Error("auth key content in shipped config")
	}
	if _, err := config.Parse(data, "shipped"); err != nil {
		t.Errorf("shipped config invalid: %v", err)
	}
	if c.Target.Provider != config.ProviderSSH {
		t.Error("ShipConfig mutated the original")
	}
	if err := os.Chmod(kf, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ShipConfig(c); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("readable key accepted: %v", err)
	}
}

func TestRemoteScript(t *testing.T) {
	s := RemoteScript("/tmp/tmp.abc", "apply", []string{"--verbose"}, "")
	for _, want := range []string{
		"sudo -n install -m 0755 \"$s/keelbox\" /var/lib/keelbox/stage/keelbox",
		"sudo -n install -m 0600 \"$s/keelbox.yaml\" /run/keelbox/keelbox.yaml",
		`sudo -n env "KEELBOX_SSH_CONNECTION=$SSH_CONNECTION" KEELBOX_DETACH=1 /var/lib/keelbox/stage/keelbox apply --verbose --local /run/keelbox/keelbox.yaml || rc=$?`,
		// The config goes at once; the secrets only while no detached
		// apply is running, which removes them itself when it ends.
		"sudo -n rm -f /run/keelbox/keelbox.yaml\nif ! systemctl list-units --quiet --no-legend --state=active,activating 'keelbox-apply-*' 2>/dev/null | grep -q .; then\n" +
			"sudo -n rm -f /run/keelbox/tailscale.authkey /run/keelbox/backup.credentials /run/keelbox/backup.password \nsudo -n sh -c 'rm -f /run/keelbox/notify.*'\nfi\nexit $rc",
		`for f in "$s"/notify.*; do if [ -f "$f" ]; then sudo -n install -m 0600 "$f" /run/keelbox/"${f##*/}"; fi; done`,
		`if [ -f "$s/backup.password" ]; then sudo -n install -m 0600 "$s/backup.password" /run/keelbox/backup.password; fi`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
}

// Under AskSudo the whole script runs under one sudo, which asks on the
// terminal: run with a stand-in sudo that records its arguments, the inner
// script must arrive intact, with the session's address passed on and
// stdout going to the file.
func TestAskSudoScript(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "args")
	sudo := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done > " + rec + "\necho on-stdout\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(sudo), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "it's.out")
	const addrs = "203.0.113.7:22,[2001:db8::7]:22"
	script := AskSudoScript("/tmp/tmp.abc", "plan", []string{"--impact", "--json"}, addrs, out)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "SSH_CONNECTION=192.168.1.5 50000 192.168.1.20 22"}
	if b, err := cmd.CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("%v %s", err, b)
	}
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	want := []string{"env", "SSH_CONNECTION=192.168.1.5 50000 192.168.1.20 22", "sh", "-c", "trap '' INT\n" + RemoteScript("/tmp/tmp.abc", "plan", []string{"--impact", "--json"}, addrs)}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Errorf("sudo got\n%q\nwant\n%q", args, want)
	}
	if b, _ := os.ReadFile(out); string(b) != "on-stdout\n" {
		t.Errorf("stdout not in the file: %q", b)
	}
	if fi, err := os.Stat(out); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("out file mode: %v %v", fi, err)
	}
	// An apply only starts the detached run under sudo; its stdout, the
	// run's id, goes to the file like any other command's.
	if a := AskSudoScript("/tmp/tmp.abc", "apply", nil, "", out); !strings.Contains(a, "KEELBOX_DETACH=1") || !strings.HasSuffix(a, " >"+shellQuote(out)) {
		t.Errorf("apply under sudo:\n%s", a)
	}
}

func TestShipConfigStagesNotifySecrets(t *testing.T) {
	dir := t.TempDir()
	topic, url := filepath.Join(dir, "topic"), filepath.Join(dir, "hook")
	for _, f := range []string{topic, url} {
		if err := os.WriteFile(f, []byte("secret-value"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := cfg(t, "notify:\n  channels:\n    - {name: phone, type: ntfy, topic_file: "+topic+"}\n    - {name: ha, type: webhook, format: json, url_file: "+url+"}\n")
	shipped, secrets, err := ShipConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Notify.Channels[0].TopicFile != "/run/keelbox/notify.phone.topic" || shipped.Notify.Channels[1].URLFile != "/run/keelbox/notify.ha.url" || len(secrets) != 2 {
		t.Errorf("notify secrets not staged: %+v %v", shipped.Notify, secrets)
	}
	if err := os.Chmod(url, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ShipConfig(c); err == nil || !strings.Contains(err.Error(), "notify.channels[1].url_file") {
		t.Errorf("world-readable url_file accepted: %v", err)
	}
}

func TestShipConfigStagesBackupSecrets(t *testing.T) {
	dir := t.TempDir()
	creds, pass := filepath.Join(dir, "s3"), filepath.Join(dir, "pw")
	for _, f := range []string{creds, pass} {
		if err := os.WriteFile(f, []byte("secret-value"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := cfg(t, "backup:\n  repository: s3:https://s3.example.com/bucket/acme\n  credentials_file: "+creds+"\n  password_file: "+pass+"\n")
	shipped, secrets, err := ShipConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Backup.CredentialsFile != RemoteBackupCredentials || shipped.Backup.PasswordFile != RemoteBackupPassword || len(secrets) != 2 {
		t.Errorf("backup secrets not staged: %+v %v", shipped.Backup, secrets)
	}
	data, _ := shipped.Marshal()
	if strings.Contains(string(data), "secret-value") {
		t.Error("secret content in the shipped config")
	}
	if err := os.Chmod(pass, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ShipConfig(c); err == nil || !strings.Contains(err.Error(), "backup.password_file") {
		t.Errorf("group-readable password file accepted: %v", err)
	}
}

// Where this machine reaches the box goes to keelbox on the box beside the
// session's own $SSH_CONNECTION, quoted for the shell, and survives sudo.
func TestRemoteScriptPassesTheSSHAddresses(t *testing.T) {
	for addrs, want := range map[string]string{
		"203.0.113.7:22":                  `sudo -n env "KEELBOX_SSH_CONNECTION=$SSH_CONNECTION" KEELBOX_SSH_ADDRS=203.0.113.7:22 KEELBOX_DETACH=1 /var/lib/keelbox/stage/keelbox apply --local`,
		"203.0.113.7:22,[2001:db8::7]:22": `sudo -n env "KEELBOX_SSH_CONNECTION=$SSH_CONNECTION" 'KEELBOX_SSH_ADDRS=203.0.113.7:22,[2001:db8::7]:22' KEELBOX_DETACH=1 /var/lib/keelbox/stage/keelbox apply --local`,
	} {
		if s := RemoteScript("/tmp/tmp.abc", "apply", nil, addrs); !strings.Contains(s, want) {
			t.Errorf("script for %q missing %q:\n%s", addrs, want, s)
		}
	}
	if s := RemoteScript("/tmp/tmp.abc", "doctor", nil, ""); strings.Contains(s, EnvSSHAddrs) {
		t.Errorf("no addresses, yet the script names them:\n%s", s)
	}
	// Run the env line with a stand-in sudo: the value arrives as one word.
	dir := t.TempDir()
	sudo := "#!/bin/sh\nfor a in \"$@\"; do case $a in KEELBOX_SSH_ADDRS=*) printf '%s\\n' \"$a\";; esac; done\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(sudo), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", RemoteScript("/tmp/tmp.abc", "doctor", nil, "203.0.113.7:22,[2001:db8::7]:22"))
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	if out, _ := cmd.Output(); string(out) != "KEELBOX_SSH_ADDRS=203.0.113.7:22,[2001:db8::7]:22\n" {
		t.Errorf("sudo got %q", out)
	}
}

func TestRemoteScriptQuotesArguments(t *testing.T) {
	s := RemoteScript("/tmp/tmp.abc", "restore", []string{"abcd1234", "--target", "/srv/a b; rm -rf /"}, "")
	if !strings.Contains(s, `restore abcd1234 --target '/srv/a b; rm -rf /' --local`) {
		t.Errorf("arguments not quoted:\n%s", s)
	}
}

func TestDpkgArch(t *testing.T) {
	for in, want := range map[string]string{"x86_64\n": "amd64", "aarch64": "arm64"} {
		if got, err := dpkgArch(in); err != nil || got != want {
			t.Errorf("%q: %s %v", in, got, err)
		}
	}
	if _, err := dpkgArch("riscv64"); err == nil {
		t.Error("riscv64 accepted")
	}
}

func TestSSHOptions(t *testing.T) {
	s := &SSH{Host: "h", Port: 2222, User: "admin", IdentityFile: "/k", KnownHosts: filepath.Join(t.TempDir(), "kh")}
	args := strings.Join(s.sshArgs("true"), " ")
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "UserKnownHostsFile=", "-i /k", "IdentitiesOnly=yes", "-p 2222 admin@h -- true"} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh args missing %q: %s", want, args)
		}
	}
}

// A copy to the box that fails exits 1, like a keelbox command on the box
// that failed. Run must not hand that on as the box's own exit code: the
// caller would print nothing.
func TestRunSetupFailureKeepsItsMessage(t *testing.T) {
	dir := t.TempDir()
	stubs := map[string]string{
		"ssh-keygen": "exit 0\n",
		"ssh": `for a in "$@"; do last=$a; done
case $last in
"uname -m") echo x86_64;;
"mktemp -d") echo /tmp/tmp.abc;;
esac
`,
		"scp": "echo 'lost connection' >&2\nexit 1\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // G306: an executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	bin := filepath.Join(dir, "keelbox")
	if err := os.WriteFile(bin, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	uploadRetryWait = 0
	s := &SSH{Host: "192.0.2.1", Port: 22, User: "admin", KnownHosts: filepath.Join(dir, "kh"), Binary: bin}
	err := s.Run(context.Background(), "doctor", cfg(t, ""))
	if err == nil || !strings.Contains(err.Error(), "scp keelbox") || !strings.Contains(err.Error(), "lost connection (tried twice). Check the connection to 192.0.2.1 and run the command again") {
		t.Fatalf("error %v", err)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		t.Errorf("the copy's exit code would pass for the box's: %v", err)
	}
}

// A copy that fails once is tried again before the command gives up: a
// connection that drops mid-copy is not an error to hand the user.
func TestUploadTriesTwice(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "tries")
	scp := "#!/bin/sh\necho x >> " + count + "\n[ \"$(wc -l < " + count + ")\" -ge 2 ] && exit 0\necho 'lost connection' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "scp"), []byte(scp), 0o755); err != nil { //nolint:gosec // G306: an executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	uploadRetryWait = 0
	s := &SSH{Host: "192.0.2.1", Port: 22, User: "admin", KnownHosts: filepath.Join(dir, "kh")}
	if err := s.upload(context.Background(), filepath.Join(dir, "scp"), "/tmp/x"); err != nil {
		t.Fatalf("a copy that works the second time: %v", err)
	}
	if data, _ := os.ReadFile(count); strings.Count(string(data), "x") != 2 {
		t.Errorf("scp ran %d times, want 2", strings.Count(string(data), "x"))
	}
	// A command that was cancelled is not tried again.
	if err := os.Remove(count); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.upload(ctx, filepath.Join(dir, "scp"), "/tmp/x"); err == nil {
		t.Error("a cancelled copy reported success")
	}
	if data, _ := os.ReadFile(count); strings.Count(string(data), "x") > 1 {
		t.Errorf("a cancelled copy was tried again: %q", data)
	}
}

// A session that can be opened again gives a silent connection up sooner.
// Its keepalives come before the base options', since ssh keeps the first
// value it is given.
func TestSessionOptsGiveUpSooner(t *testing.T) {
	s := &SSH{Host: "192.0.2.10", Port: 22, User: "agent", KnownHosts: t.TempDir() + "/known_hosts"}
	if got := strings.Join(s.sessionOpts(), " "); strings.Contains(got, "ServerAliveCountMax") || !strings.Contains(got, "ServerAliveInterval=15") {
		t.Errorf("without LostAfter: %s", got)
	}
	s.LostAfter = 10 * time.Second
	got := strings.Join(s.sessionOpts(), " ")
	if !strings.HasPrefix(got, "-o ServerAliveInterval=5 -o ServerAliveCountMax=2 ") || !strings.Contains(got, "ServerAliveInterval=15") {
		t.Errorf("with LostAfter: %s", got)
	}
}
