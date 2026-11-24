package provider

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// HostKey is one public host key, as a known_hosts or .pub line carries it.
type HostKey struct {
	Type string // ssh-ed25519
	Blob string // base64 key blob
}

// Fingerprint is OpenSSH's SHA256 fingerprint ("SHA256:..."), as
// ssh-keygen -l and the first-connection prompt print it.
func (k HostKey) Fingerprint() string {
	raw, err := base64.StdEncoding.DecodeString(k.Blob)
	if err != nil {
		return "invalid key"
	}
	sum := sha256.Sum256(raw)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// ParseHostKeys reads key lines in known_hosts, ssh-keyscan or .pub form,
// skipping comments and anything that is not a key.
func ParseHostKeys(text string) []HostKey {
	var out []HostKey
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		// .pub lines start with the type; known_hosts and keyscan lines
		// start with the host.
		if !isKeyType(f[0]) {
			f = f[1:]
		}
		if len(f) >= 2 && isKeyType(f[0]) {
			out = append(out, HostKey{Type: f[0], Blob: f[1]})
		}
	}
	return out
}

func isKeyType(s string) bool {
	return strings.HasPrefix(s, "ssh-") || strings.HasPrefix(s, "ecdsa-sha2-") || strings.HasPrefix(s, "sk-")
}

// knownHostsName is how known_hosts names a host: bare on port 22,
// [host]:port otherwise.
func knownHostsName(host string, port int) string {
	if port == 0 || port == 22 {
		return host
	}
	return "[" + host + "]:" + strconv.Itoa(port)
}

// IsKnown reports whether known_hosts already pins the host.
func (s *SSH) IsKnown() bool {
	err := exec.Command("ssh-keygen", "-F", knownHostsName(s.Host, s.Port), "-f", s.knownHosts()).Run()
	return err == nil
}

// Pin replaces whatever known_hosts holds for the host with keys.
func (s *SSH) Pin(keys []HostKey) error {
	ForgetHost(s.knownHosts(), knownHostsName(s.Host, s.Port))
	kh := s.knownHosts()
	if err := os.MkdirAll(filepath.Dir(kh), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(kh, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	name := knownHostsName(s.Host, s.Port)
	for _, k := range keys {
		if _, err := fmt.Fprintf(f, "%s %s %s\n", name, k.Type, k.Blob); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// ErrHostKeyUnconfirmed means the user did not accept an unknown host key.
var ErrHostKeyUnconfirmed = errors.New("host key not confirmed")

// HostKeyError is a host whose keys TrustHost did not pin: it does not
// offer the fingerprint asked for (Want), or there was no fingerprint to go
// by and no terminal to confirm one on. The message names the config key;
// a command that has no config yet (keelbox up) words its own from the
// fields.
type HostKeyError struct {
	Host    string
	Want    string // the fingerprint asked for; empty when there was none
	Offered []HostKey
	msg     string
}

func (e *HostKeyError) Error() string { return e.msg }

// Fingerprints lists the keys the host offered, "fingerprint (type)" each:
// the fingerprint first, so what is copied is what target.ssh.host_key and
// --host-key accept.
func (e *HostKeyError) Fingerprints() string { return fingerprints(e.Offered) }

// TrustHost makes sure the host key is pinned before the first connection.
// A key already in known_hosts is trusted (ssh then refuses a changed one).
// Otherwise the offered keys are fetched and pinned only if one matches
// target.ssh.host_key, or the user confirms the fingerprint at the prompt.
// With neither, keelbox refuses: it never trusts a key silently.
func (s *SSH) TrustHost(ctx context.Context) error {
	if s.IsKnown() {
		return nil
	}
	out, err := exec.CommandContext(ctx, "ssh-keyscan", "-T", "10", "-p", strconv.Itoa(s.Port), s.Host).Output()
	if err != nil {
		return fmt.Errorf("ssh-keyscan %s: %w", s.Host, err)
	}
	keys, err := s.choose(ParseHostKeys(string(out)))
	if err != nil {
		return err
	}
	return s.Pin(keys)
}

// choose decides which offered keys to pin: the one matching the configured
// fingerprint, or all of them once the user confirms at the prompt.
func (s *SSH) choose(offered []HostKey) ([]HostKey, error) {
	if len(offered) == 0 {
		return nil, fmt.Errorf("%s offered no SSH host keys", s.Host)
	}
	if s.HostKey != "" {
		for _, k := range offered {
			if k.Fingerprint() == s.HostKey {
				return []HostKey{k}, nil
			}
		}
		return nil, &HostKeyError{Host: s.Host, Want: s.HostKey, Offered: offered,
			msg: fmt.Sprintf("%s does not offer the pinned host key %s (it offers %s); refusing to connect. If the server was reinstalled, check the new key in the provider's console and set target.ssh.host_key to it", s.Host, s.HostKey, fingerprints(offered))}
	}
	if s.ConfirmKeys == nil {
		return nil, &HostKeyError{Host: s.Host, Offered: offered,
			msg: fmt.Sprintf("%s is not in %s and no terminal is attached to confirm its host key; set target.ssh.host_key to one of: %s", s.Host, s.knownHosts(), fingerprints(offered))}
	}
	if !s.ConfirmKeys(s.Host, offered) {
		return nil, ErrHostKeyUnconfirmed
	}
	return offered, nil
}

func fingerprints(keys []HostKey) string {
	var fs []string
	for _, k := range keys {
		fs = append(fs, k.Fingerprint()+" ("+k.Type+")")
	}
	return strings.Join(fs, ", ")
}

// RotateHostKeys replaces the box's host keys with fresh ones generated on
// the box and pins the new public keys, read over the current trusted
// connection. Used after a first connection made with a key injected
// through cloud-init, whose private half the provider's metadata service
// still serves.
func (s *SSH) RotateHostKeys(ctx context.Context) error {
	const script = `set -e
sudo -n rm -f /etc/ssh/ssh_host_*_key /etc/ssh/ssh_host_*_key.pub
sudo -n ssh-keygen -A >/dev/null
if systemctl is-active --quiet ssh.service; then sudo -n systemctl reload ssh.service; fi
cat /etc/ssh/ssh_host_*_key.pub`
	out, err := s.Output(ctx, script)
	if err != nil {
		return fmt.Errorf("rotate host keys: %w", err)
	}
	keys := ParseHostKeys(out)
	if len(keys) == 0 {
		return errors.New("rotate host keys: the box returned no public keys")
	}
	return s.Pin(keys)
}

// GenerateHostKey makes an ed25519 host key pair with ssh-keygen, for a
// server that will receive it through cloud-init. Returns the OpenSSH
// private key and the public key line.
func GenerateHostKey(ctx context.Context, comment string) (private, public string, err error) {
	dir, err := os.MkdirTemp("", "keelbox-hostkey-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "key")
	if out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", path).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("ssh-keygen: %w: %s", err, strings.TrimSpace(string(out)))
	}
	priv, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return "", "", err
	}
	return string(priv), string(pub), nil
}

// CopyPins pins the keys already trusted for another name of the same
// machine (its public address, say) under this target's host name (its
// tailnet name, or a tunnel's port), replacing what that name had: a box
// recreated under the same name keeps its tailnet name but not its keys.
// Connecting then succeeds only if the machine behind the new name presents
// one of the keys keelbox already trusts, so nothing new is trusted. With
// nothing pinned for the other name, this name's pins stay as they are and
// TrustHost asks as for any new host.
func (s *SSH) CopyPins(from string, fromPort int) error {
	if knownHostsName(from, fromPort) == knownHostsName(s.Host, s.Port) {
		return nil
	}
	out, err := exec.Command("ssh-keygen", "-F", knownHostsName(from, fromPort), "-f", s.knownHosts()).Output()
	if err != nil {
		return nil
	}
	keys := ParseHostKeys(string(out))
	if len(keys) == 0 {
		return nil
	}
	return s.Pin(keys)
}
