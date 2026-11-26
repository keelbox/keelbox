package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/modules"
	"go.yaml.in/yaml/v3"
)

// KeysRecord is root's record of the SSH keys applies have put on the
// box, per account, and of the config copy in /etc/keelbox when it was
// written. An apply writes it, under the lock, before any step runs, once
// the check has passed: the record's keys are then all in this apply's
// config, or the user asked for the others to come off. An apply that
// stops partway, after authorized_keys changed, leaves every key it may
// have put there in the record.
const KeysRecord = "/etc/keelbox/ssh-keys.yaml"

// keysOnBox are the keys of the admin and the agent.
type keysOnBox struct {
	Admin []string `yaml:"admin"`
	Agent []string `yaml:"agent"`
}

// keysRecord is the record: the keys, and the sha256 of the config copy
// there when it was written ("" for none).
type keysRecord struct {
	keysOnBox `yaml:",inline"`
	Config    string `yaml:"config_sha256"`
}

// configKeys are the keys a config puts on the box, key files read here.
func configKeys(c *config.Config) (keysOnBox, error) {
	admin, err := c.Users.Admin.AllKeys()
	if err != nil {
		return keysOnBox{}, err
	}
	agent, err := c.Users.Agent.AllKeys()
	if err != nil {
		return keysOnBox{}, err
	}
	return keysOnBox{admin, agent}, nil
}

// unreadable is a record, or a config copy, keelbox cannot read: the
// check cannot pass on it.
type unreadable struct{ path string }

func (u *unreadable) Error() string { return u.path + " is unreadable" }

// lastKeys are the keys applies put on the box. The config copy in
// /etc/keelbox is written late in an apply, by Logbook, after Host has
// changed authorized_keys, so which of the record and the copy holds them
// depends on who wrote the copy last:
//
//   - the copy the record saw: an apply since stopped before writing its
//     own, so the record is what it may have put there, and the copy's
//     other keys are ones the user asked to come off;
//   - another copy: that apply got through, and its keys are the
//     record's, or a keelbox from before the record, run by rollback,
//     wrote it and the record is stale: the copy, either way;
//   - no record: a box set up before it, the copy.
//
// Only the copy's key lists are read, so a key keelbox no longer knows
// elsewhere in it cannot make the check pass by default. found is false
// on a box never applied.
func lastKeys(x interface {
	ReadFile(string) ([]byte, error)
}) (k keysOnBox, found bool, err error) {
	data, rerr := x.ReadFile(KeysRecord)
	var rec keysRecord
	hasRecord := false
	switch {
	case rerr == nil:
		// keelbox always writes both lists; a record without them is
		// not one it wrote.
		var shape map[string]any
		if yaml.Unmarshal(data, &rec) != nil || yaml.Unmarshal(data, &shape) != nil || shape["admin"] == nil && shape["agent"] == nil {
			return keysOnBox{}, true, &unreadable{KeysRecord}
		}
		hasRecord = true
	case !errors.Is(rerr, fs.ErrNotExist):
		return keysOnBox{}, true, &unreadable{KeysRecord}
	}
	data, rerr = x.ReadFile(modules.ConfigPath)
	switch {
	case errors.Is(rerr, fs.ErrNotExist):
		return rec.keysOnBox, hasRecord, nil
	case rerr != nil:
		return keysOnBox{}, true, &unreadable{modules.ConfigPath}
	case hasRecord && rec.Config == sha256Hex(data):
		return rec.keysOnBox, true, nil
	}
	c, ok := copyKeys(data)
	if !ok {
		return keysOnBox{}, true, &unreadable{modules.ConfigPath}
	}
	return c, true, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// copyHash is the sha256 of the config copy, "" when there is none.
func copyHash(x interface {
	ReadFile(string) ([]byte, error)
}) (string, error) {
	data, err := x.ReadFile(modules.ConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

// copyKeys are the key lists of the config an apply left in /etc/keelbox.
func copyKeys(data []byte) (keysOnBox, bool) {
	var last struct {
		Users struct {
			Admin struct {
				Keys []string `yaml:"ssh_authorized_keys"`
			} `yaml:"admin"`
			Agent struct {
				Keys []string `yaml:"ssh_authorized_keys"`
			} `yaml:"agent"`
		} `yaml:"users"`
	}
	if yaml.Unmarshal(data, &last) != nil {
		return keysOnBox{}, false
	}
	return keysOnBox{last.Users.Admin.Keys, last.Users.Agent.Keys}, true
}

// recordKeys writes the record: this apply's keys, and the copy there now.
func recordKeys(e *engine.Env) error {
	now, err := configKeys(e.Cfg)
	if err != nil {
		return err
	}
	return writeRecord(e.X, now)
}

func writeRecord(x interface {
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, fs.FileMode, string, string) error
	MkdirAll(string, fs.FileMode, string, string) error
}, k keysOnBox) error {
	if k.Admin == nil {
		k.Admin = []string{}
	}
	if k.Agent == nil {
		k.Agent = []string{}
	}
	sum, err := copyHash(x)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(keysRecord{k, sum})
	if err != nil {
		return err
	}
	if err := x.MkdirAll("/etc/keelbox", 0o755, "root", "root"); err != nil {
		return err
	}
	return x.WriteFile(KeysRecord, append([]byte("# Managed by keelbox: the SSH keys applies put on this box.\n"), data...), 0o600, "root", "root")
}

// removedKey is an SSH key the box's last apply put in an account's
// authorized_keys that this apply would take off.
type removedKey struct {
	account, key string
}

// keysGate stops an apply, before any change, that would take SSH keys off
// the box that its last apply put there, unless --remove-keys says so. The
// keys from ssh_authorized_key_files are read where keelbox runs, so the
// same config applied from a second computer would otherwise replace the
// first computer's key with its own and lock the first one out. The keys
// applies put there come from root's record (KeysRecord), or on a box set
// up before it, from the config the last apply left in /etc/keelbox; the
// agent's own authorized_keys does not count.
func keysGate(e *engine.Env, removeKeys bool, stderr io.Writer) bool {
	if removeKeys {
		return true
	}
	gone, err := keysTakenOff(e)
	if err != nil {
		failf(stderr, "%v, so keelbox cannot tell which SSH keys applies put on %s. Nothing has changed. See what it holds: sudo cat %s. If this config lists every key the box should have: keelbox apply %s --remove-keys\n", err, e.Cfg.Box, unreadablePath(err), e.Cfg.Box)
		return false
	}
	if len(gone) == 0 {
		return true
	}
	box := e.Cfg.Box
	n := distinctKeys(gone)
	one := n == 1
	failf(stderr, "this apply would take %s off %s that an apply put there. Nothing has changed.\n", plural(n, "SSH key", "SSH keys"), box)
	for _, k := range gone {
		fmt.Fprintf(stderr, "  %s: %s\n", k.account, keyLabel(k.key))
	}
	it, them := "it", "it"
	if !one {
		it, them = "they", "them"
	}
	fmt.Fprintf(stderr, "\nA key comes off when ssh_authorized_key_files names a file that holds another key on this computer, as on a second computer, or when it was taken out of the config. If %s belonged to a computer you no longer use: keelbox apply %s --remove-keys\n", it, box)
	fmt.Fprintf(stderr, "To keep %s, add each line under the account's ssh_authorized_keys in the box's config on this computer:\n\n", them)
	fmt.Fprint(stderr, keepKeysYAML(gone))
	fmt.Fprintf(stderr, "\nThen: keelbox apply %s\n", box)
	return false
}

// keysTakenOff compares the keys applies put on the box with this
// apply's, for the admin and the agent. A box never applied takes nothing
// off; a record keelbox cannot read is an error.
func keysTakenOff(e *engine.Env) ([]removedKey, error) {
	last, found, err := lastKeys(e.X)
	if err != nil || !found {
		return nil, err
	}
	now, err := configKeys(e.Cfg)
	if err != nil {
		return nil, nil // the config's own check says why
	}
	return takenOff(last, now), nil
}

// takenOff are the keys in last that now lacks, per account.
func takenOff(last, now keysOnBox) []removedKey {
	var gone []removedKey
	for _, a := range []struct {
		name      string
		was, will []string
	}{
		{"users.admin", last.Admin, now.Admin},
		{"users.agent", last.Agent, now.Agent},
	} {
		keep := map[string]bool{}
		for _, k := range a.will {
			keep[keyBlob(k)] = true
		}
		for _, k := range a.was {
			if !keep[keyBlob(k)] {
				gone = append(gone, removedKey{a.name, k})
			}
		}
	}
	return gone
}

// unreadablePath is the file an unreadable error names.
func unreadablePath(err error) string {
	var u *unreadable
	if errors.As(err, &u) {
		return u.path
	}
	return KeysRecord
}

// distinctKeys counts the keys in gone, a key on both accounts once.
func distinctKeys(gone []removedKey) int {
	seen := map[string]bool{}
	for _, k := range gone {
		seen[keyBlob(k.key)] = true
	}
	return len(seen)
}

// keyBlob is a key's type and data, without its comment: the same key with
// another comment is the same key.
func keyBlob(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return line
	}
	return f[0] + " " + f[1]
}

// keyLabel is a key as ssh-keygen -l shows it: type, fingerprint, comment.
func keyLabel(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return line
	}
	label := f[0]
	if raw, err := base64.StdEncoding.DecodeString(f[1]); err == nil {
		sum := sha256.Sum256(raw)
		label += " SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	}
	if len(f) > 2 {
		label += " " + strings.Join(f[2:], " ")
	}
	return label
}

// keepKeysYAML is a list item per key, under the account it is for.
func keepKeysYAML(gone []removedKey) string {
	var b strings.Builder
	for _, acct := range []string{"users.admin", "users.agent"} {
		var keys []string
		for _, k := range gone {
			if k.account == acct {
				keys = append(keys, k.key)
			}
		}
		if len(keys) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  under %s.ssh_authorized_keys:\n", acct)
		for _, k := range keys {
			fmt.Fprintf(&b, "    - %q\n", k)
		}
	}
	return b.String()
}

// applyKeepingKeys runs apply under the lock with root's record of the
// keys written first. The check runs again here, under the lock: another
// apply may have recorded a key since the first look. Once it passes, the
// record's keys are all in this config, or the user asked for the others
// to come off, so the record is this config's keys: an apply that stops
// partway leaves every key it may have put on the box in it.
func applyKeepingKeys(e *engine.Env, removeKeys bool, stderr io.Writer, apply func() int) int {
	if !keysGate(e, removeKeys, stderr) {
		return 1
	}
	if err := recordKeys(e); err != nil {
		failf(stderr, "recording the SSH keys before the apply: %v. Nothing has changed. Check that root can write %s (sudo ls -ld /etc/keelbox), then: keelbox apply %s\n", err, KeysRecord, e.Cfg.Box)
		return 1
	}
	return apply()
}

// RollbackConfig is the config rollback applies when the SSH keys of the
// one the apply before left are not the keys on the box.
const RollbackConfig = "/var/lib/keelbox/stage/rollback.yaml"

// rollbackConfig is the config rollback has the older keelbox apply: the
// one the apply before left in /etc/keelbox, with its SSH keys those
// applies put on the box (lastKeys), written to RollbackConfig when they
// differ. That copy lacks a key a later apply added and then failed before
// copying its config, which is when rollback is most often run, and it
// still holds a key a later apply took off; rollback leaves the keys as
// they are and says so. Keys change with an apply. --remove-keys applies
// the config as it is, keys and all. The record is then the keys handed
// over, with the copy there now, so an older keelbox's apply that fails
// too leaves the record standing, and one that gets through, writing its
// own copy, is read from that copy. A record or copy keelbox cannot read
// stops rollback unless --remove-keys.
func rollbackConfig(x interface {
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, fs.FileMode, string, string) error
	MkdirAll(string, fs.FileMode, string, string) error
	Remove(string) error
}, box string, removeKeys bool, stderr io.Writer) (string, bool) {
	last, found, err := lastKeys(x)
	if err != nil && !removeKeys {
		failf(stderr, "%v, so keelbox cannot tell which SSH keys applies put on %s. Nothing has changed. See what it holds: sudo cat %s. To roll back to the config applied before, keys as it lists them: keelbox rollback %s --remove-keys\n", err, box, unreadablePath(err), box)
		return "", false
	}
	data, rerr := x.ReadFile(modules.ConfigPath)
	if rerr != nil {
		failf(stderr, "rollback: %v. Nothing has changed. keelbox rollback needs the config an apply left there; run keelbox apply %s instead\n", rerr, box)
		return "", false
	}
	copied, ok := copyKeys(data)
	if !ok { // --remove-keys, so the config as it is
		if rmErr := x.Remove(KeysRecord); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			failf(stderr, "rollback: %v. Nothing has changed. Check that root can remove %s, then: keelbox rollback %s --remove-keys\n", rmErr, KeysRecord, box)
			return "", false
		}
		return modules.ConfigPath, true
	}
	path, handed := modules.ConfigPath, copied
	if found && err == nil && !removeKeys {
		kept, dropped := takenOff(last, copied), takenOff(copied, last)
		if len(kept)+len(dropped) > 0 {
			merged, merr := setKeys(data, last)
			if merr != nil {
				failf(stderr, "rollback: setting the SSH keys on %s in the config applied before: %v. Nothing has changed. To roll back to its keys as they are: keelbox rollback %s --remove-keys\n", box, merr, box)
				return "", false
			}
			if werr := x.WriteFile(RollbackConfig, merged, 0o600, "root", "root"); werr != nil {
				failf(stderr, "rollback: %v. Nothing has changed. Check that root can write %s, then: keelbox rollback %s\n", werr, RollbackConfig, box)
				return "", false
			}
			path, handed = RollbackConfig, last
			notef(stderr, "rollback leaves the SSH keys on %s as applies put them, not as the config applied before lists them:", box)
			for _, k := range kept {
				fmt.Fprintf(stderr, "  keeps %s: %s\n", k.account, keyLabel(k.key))
			}
			for _, k := range dropped {
				fmt.Fprintf(stderr, "  leaves off %s: %s\n", k.account, keyLabel(k.key))
			}
		}
	}
	if werr := writeRecord(x, handed); werr != nil {
		failf(stderr, "rollback: recording the SSH keys: %v. Nothing has changed. Check that root can write %s, then: keelbox rollback %s\n", werr, KeysRecord, box)
		return "", false
	}
	return path, true
}

// setKeys sets a config's ssh_authorized_keys to k, account by account,
// and leaves the rest of it as it is.
func setKeys(data []byte, k keysOnBox) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping")
	}
	child := func(m *yaml.Node, key string, kind yaml.Kind) (*yaml.Node, error) {
		tag := map[yaml.Kind]string{yaml.MappingNode: "!!map", yaml.SequenceNode: "!!seq"}[kind]
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				v := m.Content[i+1]
				if v.Tag == "!!null" { // "admin:" with nothing after it
					v.Kind, v.Tag, v.Value = kind, tag, ""
				}
				if v.Kind != kind {
					return nil, fmt.Errorf("%s is not a %s", key, map[yaml.Kind]string{yaml.MappingNode: "mapping", yaml.SequenceNode: "list"}[kind])
				}
				return v, nil
			}
		}
		v := &yaml.Node{Kind: kind, Tag: tag}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
		return v, nil
	}
	users, err := child(doc.Content[0], "users", yaml.MappingNode)
	if err != nil {
		return nil, err
	}
	for _, a := range []struct {
		name string
		keys []string
	}{{"admin", k.Admin}, {"agent", k.Agent}} {
		acct, err := child(users, a.name, yaml.MappingNode)
		if err != nil {
			return nil, err
		}
		list, err := child(acct, "ssh_authorized_keys", yaml.SequenceNode)
		if err != nil {
			return nil, err
		}
		list.Content = nil
		for _, key := range a.keys {
			list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: key})
		}
	}
	return yaml.Marshal(&doc)
}
