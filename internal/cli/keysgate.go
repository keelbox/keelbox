package cli

import (
	"crypto/sha256"
	"encoding/base64"
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
// box, per account. An apply writes it, under the lock, before any step
// runs, once the check has passed: the record's keys are then all in this
// apply's config, or the user asked for the others to come off. An apply
// that stops partway, after authorized_keys changed, leaves every key it
// may have put there in the record.
const KeysRecord = "/etc/keelbox/ssh-keys.yaml"

// keysOnBox are the keys of the admin and the agent, in the record.
type keysOnBox struct {
	Admin []string `yaml:"admin"`
	Agent []string `yaml:"agent"`
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

// errRecord is a record keelbox cannot read.
var errRecord = errors.New("unreadable")

// lastKeys reads the record, or on a box set up before it existed, the
// config the last apply left: only its key lists, so a key keelbox no
// longer knows elsewhere in it cannot make the check pass by default.
// found is false on a box never applied. A record that does not parse is
// errRecord: the check cannot pass on it.
func lastKeys(x interface {
	ReadFile(string) ([]byte, error)
}) (k keysOnBox, found bool, err error) {
	if data, rerr := x.ReadFile(KeysRecord); rerr == nil {
		if yaml.Unmarshal(data, &k) != nil {
			return k, true, errRecord
		}
		return k, true, nil
	}
	data, rerr := x.ReadFile(modules.ConfigPath)
	if rerr != nil {
		return k, false, nil
	}
	k, ok := copyKeys(data)
	return k, ok, nil
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

// recordKeys writes the record: this apply's keys.
func recordKeys(e *engine.Env) error {
	now, err := configKeys(e.Cfg)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(now)
	if err != nil {
		return err
	}
	if err := e.X.MkdirAll("/etc/keelbox", 0o755, "root", "root"); err != nil {
		return err
	}
	return e.X.WriteFile(KeysRecord, append([]byte("# Managed by keelbox: the SSH keys applies put on this box.\n"), data...), 0o600, "root", "root")
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
		failf(stderr, "%s is %s, so keelbox cannot tell which SSH keys applies put on %s. Nothing has changed. If this config lists every key the box should have: keelbox apply %s --remove-keys\n", KeysRecord, err, e.Cfg.Box, e.Cfg.Box)
		return false
	}
	if len(gone) == 0 {
		return true
	}
	box := e.Cfg.Box
	distinct := map[string]bool{}
	for _, k := range gone {
		distinct[keyBlob(k.key)] = true
	}
	one := len(distinct) == 1
	failf(stderr, "this apply would take %s off %s that an apply put there. Nothing has changed.\n", plural(len(distinct), "SSH key", "SSH keys"), box)
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

// rollbackKeys stops a rollback that would take off keys a later apply
// put on the box: rollback applies the config the apply before left in
// /etc/keelbox, which may lack them, as when an apply that added a
// computer's key failed after it. --remove-keys lets it. Otherwise the
// record is removed, since the older keelbox may not keep it.
func rollbackKeys(x interface {
	ReadFile(string) ([]byte, error)
	Remove(string) error
}, box string, removeKeys bool, stderr io.Writer) bool {
	last, found, err := lastKeys(x)
	if found && err == nil && !removeKeys {
		data, rerr := x.ReadFile(modules.ConfigPath)
		if copied, ok := copyKeys(data); rerr == nil && ok {
			if gone := takenOff(last, copied); len(gone) > 0 {
				failf(stderr, "a rollback to the config applied before would take SSH keys off %s that a later apply put there. Nothing has changed.\n", box)
				for _, k := range gone {
					fmt.Fprintf(stderr, "  %s: %s\n", k.account, keyLabel(k.key))
				}
				fmt.Fprintf(stderr, "\nTo keep them, apply the config that lists them instead of rolling back: keelbox apply %s. To roll back and take them off: keelbox rollback %s --remove-keys\n", box, box)
				return false
			}
		}
	}
	if err := x.Remove(KeysRecord); err != nil && !errors.Is(err, fs.ErrNotExist) {
		failf(stderr, "rollback: %v", err)
		return false
	}
	return true
}
