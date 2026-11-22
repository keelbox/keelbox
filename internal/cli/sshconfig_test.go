package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
)

// The file has a Host for a box at its tailnet address or configured
// host, and a comment for a parked box and one with no stable address.
func TestSSHConfigText(t *testing.T) {
	home := isolate(t)
	register(t, config.StarterFor("lab", "ssh"))
	for _, box := range []string{"acme", "cold", "bare"} {
		register(t, config.StarterFor(box, "hetzner"))
	}
	acme, err := config.Load(boxConfigPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if err := saveRecord(acme, boxRecord{TailnetHost: "100.64.0.7", TailnetPort: 22}); err != nil {
		t.Fatal(err)
	}
	if err := saveParkMark("cold", parkMark{Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	text := sshConfigText()
	for _, want := range []string{
		"# Managed by keelbox",
		"Host acme.keelbox\n  HostName 100.64.0.7\n  Port 22\n  User agent\n  UserKnownHostsFile " + filepath.Join(home, ".config/keelbox/known_hosts") + "\n  StrictHostKeyChecking yes\n  ForwardAgent no\n",
		"# cold.keelbox is parked: keelbox resume cold brings it back",
		"# bare.keelbox: its public address changes when it is resumed, and it is not on a tailnet; reach it with keelbox ssh bare",
		"Host lab.keelbox\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "admin") {
		t.Errorf("an admin entry:\n%s", text)
	}
}

// ssh-config writes the file, adds the Include as the first line of
// ~/.ssh/config only after a yes, and later commands keep the file current.
func TestSSHConfigCommand(t *testing.T) {
	home := isolate(t)
	register(t, config.StarterFor("lab", "ssh"))
	user := filepath.Join(home, ".ssh/config")
	if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("Host old\n  User me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdSSHConfig(options{}, prompt.New(strings.NewReader("n\n"), &out, true), &out, &out); code != 0 {
		t.Fatal(out.String())
	}
	if b, _ := os.ReadFile(user); string(b) != "Host old\n  User me\n" {
		t.Errorf("changed after a no: %q", b)
	}
	if _, err := os.Stat(sshConfigPath()); err != nil {
		t.Fatal("no file written")
	}
	out.Reset()
	if code := cmdSSHConfig(options{}, prompt.New(strings.NewReader("y\n"), &out, true), &out, &out); code != 0 {
		t.Fatal(out.String())
	}
	b, _ := os.ReadFile(user)
	if !strings.HasPrefix(string(b), "Include "+sshConfigPath()+"\n\nHost old\n") || !includesSSHConfig(string(b)) {
		t.Errorf("~/.ssh/config: %q", b)
	}
	out.Reset()
	cmdSSHConfig(options{}, prompt.New(strings.NewReader(""), &out, false), &out, &out)
	if !strings.Contains(out.String(), "includes it") {
		t.Errorf("second run: %s", out.String())
	}
	// A new box appears after a command that can add one.
	register(t, config.StarterFor("lab2", "ssh"))
	refreshSSHConfig("ls")
	if f, _ := os.ReadFile(sshConfigPath()); strings.Contains(string(f), "lab2") {
		t.Error("refreshed after ls")
	}
	refreshSSHConfig("up")
	if f, _ := os.ReadFile(sshConfigPath()); !strings.Contains(string(f), "Host lab2.keelbox") {
		t.Errorf("not refreshed after up:\n%s", f)
	}
}

// Without the file, nothing writes one: the user opts in with ssh-config.
func TestSSHConfigNotWrittenUnasked(t *testing.T) {
	isolate(t)
	register(t, config.StarterFor("lab", "ssh"))
	refreshSSHConfig("up")
	if _, err := os.Stat(sshConfigPath()); err == nil {
		t.Error("written without keelbox ssh-config")
	}
}
