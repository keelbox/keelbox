package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
)

// keelbox ssh-config writes an OpenSSH config with a Host for each box, so
// that ssh, scp, rsync, VS Code and agents on this computer reach a box as
// BOX.keelbox, with keelbox's pinned host keys. It is written again after
// every command that can change where a box is, so it does not go stale.

// sshConfigAlias is the name a box has in the file.
const sshConfigAlias = ".keelbox"

// sshConfigPath is the file keelbox writes; ~/.ssh/config includes it.
func sshConfigPath() string {
	return filepath.Join(filepath.Dir(provider.DefaultKnownHosts()), "ssh_config")
}

// sshConfigInclude is the line in ~/.ssh/config that reads it.
func sshConfigInclude() string { return "Include " + sshQuote(sshConfigPath()) }

// sshConfigRefreshes are the commands after which the file is written
// again, when it exists: those that create, move, park or remove a box, or
// learn its tailnet address.
var sshConfigRefreshes = map[string]bool{
	"up": true, "create": true, "apply": true, "resume": true, "park": true, "resize": true, "destroy": true, "forget": true,
}

// sshQuote quotes a value for ssh_config when it has a space.
func sshQuote(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// sshConfigText is the file's content for the boxes registered here.
func sshConfigText() string {
	var b strings.Builder
	b.WriteString("# Managed by keelbox: keelbox ssh-config writes it, and commands that\n" +
		"# move a box write it again, so changes made here do not last.\n" +
		"# Each box is BOX.keelbox, as its agent account: ssh acme.keelbox\n")
	for _, box := range registeredBoxes() {
		cfg, err := config.Load(boxConfigPath(box))
		if err != nil {
			fmt.Fprintf(&b, "\n# %s: its config does not load; keelbox doctor %s says why\n", box, box)
			continue
		}
		if cfg.Target.Provider == config.ProviderLocal {
			continue
		}
		if _, parked := loadParkMark(box); parked {
			fmt.Fprintf(&b, "\n# %s%s is parked: keelbox resume %s brings it back\n", box, sshConfigAlias, box)
			continue
		}
		a, ok := sshConfigAddr(cfg)
		if !ok {
			fmt.Fprintf(&b, "\n# %s%s: its public address changes when it is resumed, and it is not on a tailnet; reach it with keelbox ssh %s\n", box, sshConfigAlias, box)
			continue
		}
		values := []string{a.host, cfg.Users.Agent.Name, knownHosts(cfg), cfg.Target.SSH.IdentityFile}
		bad := false
		for _, v := range values {
			bad = bad || strings.ContainsAny(v, "\"\n\r")
		}
		if bad {
			fmt.Fprintf(&b, "\n# %s%s: a value has a quote or a line break; reach it with keelbox ssh %s\n", box, sshConfigAlias, box)
			continue
		}
		fmt.Fprintf(&b, "\nHost %s%s\n", box, sshConfigAlias)
		fmt.Fprintf(&b, "  HostName %s\n  Port %d\n  User %s\n", a.host, firstPort(a.port), cfg.Users.Agent.Name)
		fmt.Fprintf(&b, "  UserKnownHostsFile %s\n  StrictHostKeyChecking yes\n  ForwardAgent no\n", sshQuote(knownHosts(cfg)))
		if f := cfg.Target.SSH.IdentityFile; f != "" {
			fmt.Fprintf(&b, "  IdentityFile %s\n  IdentitiesOnly yes\n", sshQuote(config.ExpandHome(f)))
		}
	}
	return b.String()
}

// sshConfigAddr is where the file sends ssh for a box, from what this
// computer knows without asking anything: the tailnet address, which stays
// the same when a box is parked and resumed, else the configured host.
func sshConfigAddr(cfg *config.Config) (addr, bool) {
	if cfg.Tailscale() {
		if a, ok := tailnetAddr(cfg); ok {
			return a, true
		}
	}
	if cfg.Target.Provider == config.ProviderSSH && cfg.Target.SSH.Host != "" {
		return addr{cfg.Target.SSH.Host, cfg.Target.SSH.Port}, true
	}
	return addr{}, false
}

// writeSSHConfig writes the file, only when it changes.
func writeSSHConfig() (bool, error) {
	p := sshConfigPath()
	text := []byte(sshConfigText())
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, text) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return false, err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, text, 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, p)
}

// refreshSSHConfig writes the file again after cmd, when the user has one.
func refreshSSHConfig(cmd string) {
	if !sshConfigRefreshes[cmd] {
		return
	}
	if _, err := os.Stat(sshConfigPath()); err != nil {
		return
	}
	_, _ = writeSSHConfig()
}

// userSSHConfig is ~/.ssh/config.
func userSSHConfig() string { return config.ExpandHome("~/.ssh/config") }

// includesSSHConfig reports whether ~/.ssh/config's first line that is not
// blank or a comment includes keelbox's file.
func includesSSHConfig(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		return l == sshConfigInclude()
	}
	return false
}

// cmdSSHConfig is keelbox ssh-config: write the file, and add the Include
// to ~/.ssh/config after a yes. The Include goes first: ssh reads a Host
// block after another Host line as part of that block.
func cmdSSHConfig(o options, p *prompt.Prompter, stdout, stderr io.Writer) int {
	if o.list {
		fmt.Fprint(stdout, sshConfigText())
		return 0
	}
	if _, err := writeSSHConfig(); err != nil {
		fail(stderr, err)
		return 1
	}
	path := tildePath(sshConfigPath())
	fmt.Fprintf(stdout, "Wrote %s: each box as BOX.keelbox, as its agent, with keelbox's pinned host keys. keelbox writes it again when a box moves.\n", path)
	user := linkTarget(userSSHConfig())
	old, err := os.ReadFile(user)
	if err != nil && !os.IsNotExist(err) {
		fail(stderr, err)
		return 1
	}
	if includesSSHConfig(string(old)) {
		fmt.Fprintf(stdout, "%s includes it.\n", tildePath(user))
		return 0
	}
	line := sshConfigInclude()
	if !p.Interactive() {
		fmt.Fprintf(stdout, "For ssh to read it, add this as the first line of %s:\n  %s\n", tildePath(user), line)
		return 0
	}
	ok, err := p.Confirm(fmt.Sprintf("Add %q as the first line of %s?", line, tildePath(user)), false)
	if err != nil || !ok {
		fmt.Fprintf(stdout, "Left %s as it is. For ssh to read the file, add this as its first line:\n  %s\n", tildePath(user), line)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
		fail(stderr, err)
		return 1
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(user); err == nil {
		mode = fi.Mode().Perm()
	}
	text := line + "\n"
	if len(old) > 0 {
		text += "\n" + string(old)
	}
	tmp := user + ".keelbox-tmp"
	if err := os.WriteFile(tmp, []byte(text), mode); err != nil { //nolint:gosec // G703: ~/.ssh/config, a fixed path
		fail(stderr, err)
		return 1
	}
	if err := os.Rename(tmp, user); err != nil {
		fail(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Added it to %s. Try: ssh %s\n", tildePath(user), firstBoxAlias())
	return 0
}

// firstBoxAlias is a box's name in the file, for an example.
func firstBoxAlias() string {
	if boxes := registeredBoxes(); len(boxes) > 0 {
		return boxes[0] + sshConfigAlias
	}
	return "acme" + sshConfigAlias
}
