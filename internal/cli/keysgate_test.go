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

// recorded is the record an apply writes before its steps: keys for both
// accounts, and the config copy there then.
func recorded(t *testing.T, x *system.Fake, keys ...string) {
	t.Helper()
	if err := writeRecord(x, keysOnBox{Admin: keys, Agent: keys}); err != nil {
		t.Fatal(err)
	}
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

// A first apply, the same keys with other comments and an added key take
// nothing off.
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

	// B applies A and B; the apply fails after Host.
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

// The apply sequence: the check again under the lock, then the config's
// keys recorded before the steps. A failed apply leaves them recorded;
// with --remove-keys the keys it takes off are not kept, so a step that
// fails later, before or after the config copy, does not make every next
// apply stop.
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

	// B's laptop is lost: --remove-keys from A, and a step before the
	// config copy fails, so the copy still lists B. The next apply goes
	// ahead without the flag and does not offer B's key back.
	x = system.NewFake()
	lastApplied(t, x, laptopA, laptopB)
	recorded(t, x, laptopA, laptopB)
	if code := applyKeepingKeys(&engine.Env{X: x, Cfg: cfg(laptopA)}, true, &stderr, func() int { return 1 }); code != 1 {
		t.Fatalf("exit %d", code)
	}
	stderr.Reset()
	if code := applyKeepingKeys(&engine.Env{X: x, Cfg: cfg(laptopA)}, false, &stderr, func() int { return 0 }); code != 0 {
		t.Fatalf("after a failed --remove-keys apply, the next one stopped: exit %d\n%s", code, stderr.String())
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
	// No record, and a last config keelbox cannot read: it stops too.
	x = system.NewFake()
	x.AddFile(modules.ConfigPath, "version: [", 0o600)
	stderr.Reset()
	if keysGate(&engine.Env{X: x, Cfg: c}, false, &stderr) {
		t.Errorf("an unreadable last config passed:\n%s", stderr.String())
	}
}

// Rollback applies the config the apply before left, with its SSH keys
// those applies put on the box. When a later apply that added a key failed
// before it copied its config, that config lacks the key: rollback keeps
// it, so the computer that added it is not locked out. --remove-keys
// applies the config as it is. A record or copy keelbox cannot read stops
// rollback.
func TestRollbackConfig(t *testing.T) {
	// B's apply adds its key and fails before the config copy.
	setup := func() *system.Fake {
		x := system.NewFake()
		lastApplied(t, x, laptopA)
		recorded(t, x, laptopA, laptopB)
		return x
	}
	var stderr bytes.Buffer
	x := setup()
	path, ok := rollbackConfig(x, "acme", false, &stderr)
	if !ok || path != RollbackConfig {
		t.Fatalf("path %q ok %v\n%s", path, ok, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "rollback leaves the SSH keys on acme as applies put them") || !strings.Contains(got, "keeps users.agent: ssh-ed25519 SHA256:") {
		t.Errorf("rollback said:\n%s", got)
	}
	data, _ := x.ReadFile(RollbackConfig)
	c, err := config.Parse(data, RollbackConfig)
	if err != nil {
		t.Fatalf("the config rollback applies does not parse: %v\n%s", err, data)
	}
	for _, a := range []config.Account{c.Users.Admin, c.Users.Agent.Account} {
		if len(a.SSHAuthorizedKeys) != 2 || a.SSHAuthorizedKeys[1] != laptopB {
			t.Errorf("keys %q\n%s", a.SSHAuthorizedKeys, data)
		}
	}
	if !strings.HasPrefix(string(data), "# Applied by keelbox") {
		t.Errorf("the rest of the config was not kept:\n%s", data)
	}
	onlyA := &engine.Env{X: x, Cfg: &config.Config{Box: "acme"}}
	onlyA.Cfg.Users.Admin.SSHAuthorizedKeys = []string{laptopA}
	onlyA.Cfg.Users.Agent.SSHAuthorizedKeys = []string{laptopA}
	// The older keelbox's apply fails too: B's key is still known.
	if keysGate(onlyA, false, &stderr) {
		t.Error("after a rollback whose apply failed, A's apply took B's key off unasked")
	}
	// It gets through and writes its copy: B's key is known from that.
	x.AddFile(modules.ConfigPath, string(data), 0o600)
	if keysGate(onlyA, false, &stderr) {
		t.Error("after a rollback, A's apply took B's key off unasked")
	}

	// --remove-keys: the config applied before, as it is.
	x = setup()
	if path, ok := rollbackConfig(x, "acme", true, &stderr); !ok || path != modules.ConfigPath {
		t.Errorf("--remove-keys: path %q ok %v", path, ok)
	}
	if last, _, _ := lastKeys(x); len(last.Agent) != 1 {
		t.Errorf("keys after rollback --remove-keys: %q", last.Agent)
	}

	// A --remove-keys apply that took B off failed before the copy, which
	// still lists B: rollback leaves B off, and says so.
	x = system.NewFake()
	lastApplied(t, x, laptopA, laptopB)
	recorded(t, x, laptopA)
	stderr.Reset()
	if path, ok := rollbackConfig(x, "acme", false, &stderr); !ok || path != RollbackConfig || !strings.Contains(stderr.String(), "leaves off users.admin:") {
		t.Errorf("path %q ok %v\n%s", path, ok, stderr.String())
	}
	if data, _ := x.ReadFile(RollbackConfig); strings.Contains(string(data), "laptop-b") {
		t.Errorf("rollback put the removed key back:\n%s", data)
	}

	// An account given with nothing under it gets the keys.
	merged, err := setKeys([]byte("version: 1\nbox: acme\nusers:\n  admin:\n"), keysOnBox{[]string{laptopB}, []string{laptopB}})
	if err != nil || strings.Count(string(merged), "laptop-b") != 2 {
		t.Errorf("null mapping: %v\n%s", err, merged)
	}

	// The keys as the config lists them, or no record: as it is, quietly.
	x = system.NewFake()
	lastApplied(t, x, laptopA)
	stderr.Reset()
	if path, ok := rollbackConfig(x, "acme", false, &stderr); !ok || path != modules.ConfigPath || stderr.Len() != 0 {
		t.Errorf("no record: path %q ok %v\n%s", path, ok, stderr.String())
	}

	// A record keelbox cannot read, or one it did not write, stops.
	for _, bad := range []string{"admin: [", "# empty\n"} {
		x = setup()
		x.AddFile(KeysRecord, bad, 0o600)
		stderr.Reset()
		if _, ok := rollbackConfig(x, "acme", false, &stderr); ok || !strings.HasSuffix(stderr.String(), "keelbox rollback acme --remove-keys\n") {
			t.Errorf("record %q: rollback went ahead:\n%s", bad, stderr.String())
		}
	}
}

// After a rollback to a keelbox from before the record, that keelbox's
// applies change the config copy and not the record. The copy is not the
// one the record saw, so the check reads the copy: a key that keelbox
// added does not come off unasked, and one it took off does not come back.
func TestKeysGateReadsANewerCopy(t *testing.T) {
	const laptopD = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDdddddddddddddddddddddddddddddddddddddddddd laptop-d"
	x := system.NewFake()
	lastApplied(t, x, laptopA, laptopB)
	recorded(t, x, laptopA, laptopB)    // rollback's record
	lastApplied(t, x, laptopA, laptopD) // the older keelbox's apply
	c := &config.Config{Box: "acme"}
	c.Users.Admin.SSHAuthorizedKeys = []string{laptopA}
	c.Users.Agent.SSHAuthorizedKeys = []string{laptopA}
	var stderr bytes.Buffer
	if keysGate(&engine.Env{X: x, Cfg: c}, false, &stderr) || !strings.Contains(stderr.String(), "laptop-d") || strings.Contains(stderr.String(), "laptop-b") {
		t.Errorf("gate:\n%s", stderr.String())
	}
	// A second rollback leaves B off quietly.
	stderr.Reset()
	if path, ok := rollbackConfig(x, "acme", false, &stderr); !ok || path != modules.ConfigPath || stderr.Len() != 0 {
		t.Errorf("second rollback: path %q ok %v\n%s", path, ok, stderr.String())
	}
	// The message names the file that cannot be read.
	x = system.NewFake()
	x.AddFile(modules.ConfigPath, "version: [", 0o600)
	stderr.Reset()
	keysGate(&engine.Env{X: x, Cfg: c}, false, &stderr)
	if !strings.Contains(stderr.String(), modules.ConfigPath+" is unreadable") || !strings.Contains(stderr.String(), "sudo cat "+modules.ConfigPath) {
		t.Errorf("message:\n%s", stderr.String())
	}
}
