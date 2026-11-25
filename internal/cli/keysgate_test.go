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
		"this apply would take 1 SSH key off acme that an apply put there. Nothing has changed.",
		"If it belonged to a computer you no longer use: keelbox apply acme --remove-keys\nTo keep it, add each line under the account's ssh_authorized_keys",
		"  users.admin: ssh-ed25519 SHA256:",
		" laptop-a\n  users.agent: ssh-ed25519 SHA256:",
		"  under users.admin.ssh_authorized_keys:\n    - \"" + laptopA + "\"\n  under users.agent.ssh_authorized_keys:\n    - \"" + laptopA + "\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\nThen: keelbox apply acme\n") {
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
// last config keelbox cannot read take nothing off.
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
		{name: "unreadable last config", raw: "version: [", now: cfg(laptopB)}, // the pre-record fallback only
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := system.NewFake()
			if tc.last != nil {
				lastApplied(t, x, tc.last...)
			}
			if tc.raw != "" {
				x.AddFile(modules.ConfigPath, tc.raw, 0o600)
			}
			var stderr bytes.Buffer
			if !keysGate(&engine.Env{X: x, Cfg: tc.now}, false, &stderr) {
				t.Errorf("stopped:\n%s", stderr.String())
			}
		})
	}
}

// An apply that stops partway, after authorized_keys changed, leaves every
// key it may have put there in root's record: the next apply, from the
// other computer, stops before taking one off. One that passes records its
// keys alone, so --remove-keys takes a key off for good.
func TestKeysRecordAcrossApplies(t *testing.T) {
	cfg := func(keys ...string) *config.Config {
		c := &config.Config{Box: "acme"}
		c.Users.Admin.SSHAuthorizedKeys = keys
		c.Users.Agent.SSHAuthorizedKeys = keys
		return c
	}
	x := system.NewFake()
	lastApplied(t, x, laptopA) // a box set up before the record existed
	var stderr bytes.Buffer

	// B applies A and B; the apply fails after Host: no record after it.
	e := &engine.Env{X: x, Cfg: cfg(laptopA, laptopB)}
	if !keysGate(e, false, &stderr) {
		t.Fatalf("adding a key stopped:\n%s", stderr.String())
	}
	if err := recordKeys(e); err != nil {
		t.Fatal(err)
	}
	// A applies its config with A alone: B would come off.
	e = &engine.Env{X: x, Cfg: cfg(laptopA)}
	if keysGate(e, false, &stderr) || !strings.Contains(stderr.String(), "take 1 SSH key off") || !strings.Contains(stderr.String(), "laptop-b") {
		t.Fatalf("an apply after a partial one took the key off unasked:\n%s", stderr.String())
	}
	// With --remove-keys the apply goes ahead, passes, and records A alone.
	stderr.Reset()
	if !keysGate(e, true, &stderr) {
		t.Fatal("--remove-keys stopped")
	}
	if err := recordKeys(e); err != nil {
		t.Fatal(err)
	}
	if !keysGate(e, false, &stderr) {
		t.Errorf("the key taken off still stops apply:\n%s", stderr.String())
	}
	data, _ := x.ReadFile(KeysRecord)
	if strings.Contains(string(data), "laptop-b") || strings.Count(string(data), "laptop-a") != 2 {
		t.Errorf("record:\n%s", data)
	}
	if fi, ok := x.Files[KeysRecord]; ok && (fi.Mode != 0o600 || fi.Owner != "root") {
		t.Errorf("record is %o %s", fi.Mode, fi.Owner)
	}
}

// --remove-keys reaches the box, and the detached run started there.
func TestRemoveKeysReachesTheBox(t *testing.T) {
	if got := strings.Join(remoteExtra(options{removeKeys: true}), " "); !strings.Contains(got, "--remove-keys") {
		t.Errorf("remote arguments %q", got)
	}
}

// The apply sequence: the check again under the lock, the union recorded
// before the steps, the keys alone once apply passed. A failed apply keeps
// the union; with --remove-keys the keys it takes off are not kept for it,
// so a step that fails later does not make every next apply stop.
func TestApplyKeepingKeys(t *testing.T) {
	cfg := func(keys ...string) *config.Config {
		c := &config.Config{Box: "acme"}
		c.Users.Admin.SSHAuthorizedKeys = keys
		c.Users.Agent.SSHAuthorizedKeys = keys
		return c
	}
	record := func(x *system.Fake) string {
		data, _ := x.ReadFile(KeysRecord)
		return string(data)
	}
	var stderr bytes.Buffer

	// B adds its key; a later step fails: both stay recorded, and A's
	// apply without B stops before its steps run.
	x := system.NewFake()
	lastApplied(t, x, laptopA)
	ran := false
	if code := applyKeepingKeys(&engine.Env{X: x, Cfg: cfg(laptopA, laptopB)}, false, &stderr, func() int { ran = true; return 1 }); code != 1 || !ran {
		t.Fatalf("exit %d, ran %v", code, ran)
	}
	if r := record(x); !strings.Contains(r, "laptop-a") || !strings.Contains(r, "laptop-b") {
		t.Fatalf("record after a failed apply:\n%s", r)
	}
	ran = false
	if code := applyKeepingKeys(&engine.Env{X: x, Cfg: cfg(laptopA)}, false, &stderr, func() int { ran = true; return 0 }); code != 1 || ran {
		t.Fatalf("A's apply went ahead: exit %d, ran %v\n%s", code, ran, stderr.String())
	}

	// --remove-keys, and the agents step fails: B is not kept, and the
	// next apply goes ahead without the flag.
	stderr.Reset()
	if code := applyKeepingKeys(&engine.Env{X: x, Cfg: cfg(laptopA)}, true, &stderr, func() int { return 1 }); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if r := record(x); strings.Contains(r, "laptop-b") {
		t.Fatalf("--remove-keys kept the key:\n%s", r)
	}
	if code := applyKeepingKeys(&engine.Env{X: x, Cfg: cfg(laptopA)}, false, &stderr, func() int { return 0 }); code != 0 {
		t.Fatalf("the next apply stopped: exit %d\n%s", code, stderr.String())
	}
	if fi := x.Files[KeysRecord]; fi == nil || fi.Mode != 0o600 || fi.Owner != "root" {
		t.Errorf("record file %+v", fi)
	}
}

// A record that does not parse stops apply, with the flag that passes.
func TestKeysGateUnreadableRecord(t *testing.T) {
	x := system.NewFake()
	x.AddFile(KeysRecord, "admin: [", 0o600)
	c := &config.Config{Box: "acme"}
	c.Users.Admin.SSHAuthorizedKeys = []string{laptopA}
	var stderr bytes.Buffer
	if keysGate(&engine.Env{X: x, Cfg: c}, false, &stderr) || !strings.HasSuffix(stderr.String(), "keelbox apply acme --remove-keys\n") {
		t.Errorf("an unreadable record passed:\n%s", stderr.String())
	}
	if !keysGate(&engine.Env{X: x, Cfg: c}, true, &stderr) {
		t.Error("--remove-keys stopped")
	}
}

// Rollback applies the config the apply before left. When a later apply
// that added a key failed before it copied its config, that config lacks
// the key: rollback stops, unless --remove-keys. Otherwise it removes the
// record, which the older keelbox may not keep.
func TestRollbackKeys(t *testing.T) {
	x := system.NewFake()
	lastApplied(t, x, laptopA)
	x.AddFile(KeysRecord, "admin: [\""+laptopA+"\", \""+laptopB+"\"]\nagent: [\""+laptopA+"\"]\n", 0o600)
	var stderr bytes.Buffer
	if rollbackKeys(x, "acme", false, &stderr) {
		t.Fatal("rollback took B's key off unasked")
	}
	if got := stderr.String(); !strings.Contains(got, "users.admin: ssh-ed25519 SHA256:") || !strings.Contains(got, "laptop-b") || !strings.HasSuffix(got, "keelbox rollback acme --remove-keys\n") {
		t.Errorf("message:\n%s", got)
	}
	if _, err := x.ReadFile(KeysRecord); err != nil {
		t.Error("the record went with a stopped rollback")
	}
	if !rollbackKeys(x, "acme", true, &stderr) {
		t.Fatal("--remove-keys stopped")
	}
	if _, err := x.ReadFile(KeysRecord); err == nil {
		t.Error("the record stayed for the older keelbox")
	}
	// Nothing a later apply added: the record goes, and rollback runs.
	x = system.NewFake()
	lastApplied(t, x, laptopA)
	x.AddFile(KeysRecord, "admin: [\""+laptopA+"\"]\nagent: [\""+laptopA+"\"]\n", 0o600)
	if !rollbackKeys(x, "acme", false, &stderr) {
		t.Errorf("stopped:\n%s", stderr.String())
	}
}
