package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/modules"
)

// removedKey is an SSH key the box's last apply put in an account's
// authorized_keys that this apply would take off.
type removedKey struct {
	account, key string
}

// keysGate stops an apply, before any change, that would take SSH keys off
// the box that its last apply put there, unless --remove-keys says so. The
// keys from ssh_authorized_key_files are read where keelbox runs, so the
// same config applied from a second computer would otherwise replace the
// first computer's key with its own and lock the first one out. The last
// apply's keys come from the config it left in /etc/keelbox, which only
// root can change; the agent's own authorized_keys does not count.
func keysGate(e *engine.Env, removeKeys bool, stderr io.Writer) bool {
	if removeKeys {
		return true
	}
	gone := keysTakenOff(e)
	if len(gone) == 0 {
		return true
	}
	box := e.Cfg.Box
	failf(stderr, "this apply would take %s off %s that its last apply put there. Nothing has changed.\n", plural(len(gone), "SSH key", "SSH keys"), box)
	for _, k := range gone {
		fmt.Fprintf(stderr, "  %s: %s\n", k.account, keyLabel(k.key))
	}
	fmt.Fprintf(stderr, "\nA key comes off when ssh_authorized_key_files names a file that holds another key on this computer, as on a second computer, or when it was taken out of the config. To keep it, add it to the box's config on this computer, under the account it is listed for:\n\n")
	fmt.Fprint(stderr, keepKeysYAML(gone))
	fmt.Fprintf(stderr, "\nTo take it off the box: keelbox apply %s --remove-keys\n", box)
	return false
}

// keysTakenOff compares the keys of the last apply with this one's, for
// the admin and the agent. A box never applied, or a last config keelbox
// cannot read, takes nothing off.
func keysTakenOff(e *engine.Env) []removedKey {
	data, err := e.X.ReadFile(modules.ConfigPath)
	if err != nil {
		return nil
	}
	last, err := config.Parse(data, modules.ConfigPath)
	if err != nil {
		return nil
	}
	var gone []removedKey
	for _, a := range []struct {
		name      string
		was, will config.Account
	}{
		{"users.admin", last.Users.Admin, e.Cfg.Users.Admin},
		{"users.agent", last.Users.Agent.Account, e.Cfg.Users.Agent.Account},
	} {
		was, err := a.was.AllKeys()
		if err != nil {
			continue
		}
		will, err := a.will.AllKeys()
		if err != nil {
			continue
		}
		keep := map[string]bool{}
		for _, k := range will {
			keep[keyBlob(k)] = true
		}
		for _, k := range was {
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

// keepKeysYAML is the config that keeps the keys, account by account.
func keepKeysYAML(gone []removedKey) string {
	var b strings.Builder
	b.WriteString("  users:\n")
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
		fmt.Fprintf(&b, "    %s:\n      ssh_authorized_keys:\n", strings.TrimPrefix(acct, "users."))
		for _, k := range keys {
			fmt.Fprintf(&b, "        - %q\n", k)
		}
	}
	return b.String()
}
