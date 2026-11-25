package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/system"
)

const (
	laptopA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa laptop-a"
	laptopB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb laptop-b"
)

// lastApplied is the config the box's last apply left in /etc/keelbox,
// resolved on the laptop that ran it: key files already read there.
func lastApplied(t *testing.T, x *system.Fake, keys ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("# Applied by keelbox; edit the source config and re-apply.\nversion: 1\nbox: acme\nusers:\n")
	for _, acct := range []string{"admin", "agent"} {
		b.WriteString("  " + acct + ":\n    ssh_authorized_keys:\n")
		for _, k := range keys {
			b.WriteString("      - \"" + k + "\"\n")
		}
	}
	x.AddFile(modules.ConfigPath, b.String(), 0o600)
}

// The config as keelbox up wrote it before ADR 0097, applied from a second
// computer: the key file holds that computer's key, so the first
// computer's would come off both accounts. apply stops before any change
// and prints the YAML that keeps it.
func TestKeysGateSecondComputer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh/id_ed25519.pub"), []byte(laptopB+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	y := "version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_key_files: [~/.ssh/id_ed25519.pub]\n  agent:\n    ssh_authorized_key_files: [~/.ssh/id_ed25519.pub]\n"
	c, err := config.Parse([]byte(y), "acme.yaml")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := c.Resolve() // as the laptop ships it
	if err != nil {
		t.Fatal(err)
	}
	x := system.NewFake()
	lastApplied(t, x, laptopA)
	e := &engine.Env{X: x, Cfg: resolved}
	var stderr bytes.Buffer
	if keysGate(e, false, &stderr) {
		t.Fatal("apply from the second computer would take the first one's key off")
	}
	got := stderr.String()
	for _, want := range []string{
		"this apply would take 2 SSH keys off acme that its last apply put there. Nothing has changed.",
		"  users.admin: ssh-ed25519 SHA256:",
		" laptop-a\n  users.agent: ssh-ed25519 SHA256:",
		"  users:\n    admin:\n      ssh_authorized_keys:\n        - \"" + laptopA + "\"\n    agent:\n      ssh_authorized_keys:\n        - \"" + laptopA + "\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "To take it off the box: keelbox apply acme --remove-keys\n") {
		t.Errorf("message does not end with the fix:\n%s", got)
	}
	for _, c := range x.Calls {
		t.Errorf("the check ran %s", c)
	}

	// --remove-keys says the key comes off.
	stderr.Reset()
	if !keysGate(e, true, &stderr) || stderr.Len() != 0 {
		t.Errorf("--remove-keys still stopped:\n%s", stderr.String())
	}

	// Both keys listed: nothing comes off.
	c.Users.Admin.SSHAuthorizedKeys = []string{laptopA}
	c.Users.Agent.SSHAuthorizedKeys = []string{laptopA}
	if resolved, err = c.Resolve(); err != nil {
		t.Fatal(err)
	}
	e.Cfg = resolved
	if !keysGate(e, false, &stderr) {
		t.Errorf("stopped with both keys listed:\n%s", stderr.String())
	}
}

// A first apply, the same keys with other comments, an added key, and a
// last config keelbox cannot read take nothing off. Keys the accounts
// added to their own authorized_keys do not count: only root's record of
// the last apply does.
func TestKeysGateTakesNothingOff(t *testing.T) {
	cfg := func(keys ...string) *config.Config {
		c := &config.Config{Box: "acme"}
		c.Users.Admin.SSHAuthorizedKeys = keys
		c.Users.Agent.SSHAuthorizedKeys = keys
		return c
	}
	for _, tc := range []struct {
		name string
		last []string
		raw  string
		now  *config.Config
	}{
		{name: "first apply", now: cfg(laptopA)},
		{name: "same keys", last: []string{laptopA}, now: cfg(laptopA)},
		{name: "another comment", last: []string{laptopA}, now: cfg(strings.TrimSuffix(laptopA, "laptop-a") + "renamed")},
		{name: "a key added", last: []string{laptopA}, now: cfg(laptopA, laptopB)},
		{name: "unreadable last config", raw: "version: [", now: cfg(laptopB)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := system.NewFake()
			if tc.last != nil {
				lastApplied(t, x, tc.last...)
			}
			if tc.raw != "" {
				x.AddFile(modules.ConfigPath, tc.raw, 0o600)
			}
			x.AddFile("/home/agent/.ssh/authorized_keys", laptopB+"\n", 0o600)
			var stderr bytes.Buffer
			if !keysGate(&engine.Env{X: x, Cfg: tc.now}, false, &stderr) {
				t.Errorf("stopped:\n%s", stderr.String())
			}
		})
	}
}
