package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
)

// After the box joins the tailnet, keelbox says its key expires and where
// to turn that off, waits on a terminal and checks again, by exit code.
func TestKeyExpiryStep(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	state := filepath.Join(dir, "expiry")
	// A tailscale that reports the box's key expiry while the state file
	// exists, and always a peer whose key expires: only the box's counts.
	script := "#!/bin/sh\npeer=',\"Peer\":{\"x\":{\"KeyExpiry\":\"2027-01-01T00:00:00Z\"}}'\nfor a; do [ \"$a\" = --peers=false ] && peer=; done\n" +
		"if [ -e " + state + " ]; then echo '{\"Self\":{\"KeyExpiry\":\"2027-06-01T00:00:00Z\"}'\"$peer}\"; else echo '{\"Self\":{}'\"$peer}\"; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "tailscale"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	sh := &shell{user: os.Getenv("USER")}
	if os.Geteuid() == 0 || sh.user == "" {
		t.Skip("runs as an ordinary user")
	}
	cfg := &config.Config{Box: "acme"}
	run := func(input string, turnOff bool) string {
		var out bytes.Buffer
		p := prompt.New(strings.NewReader(input), &out, input != "")
		if turnOff {
			// The user turns it off in the console while keelbox waits.
			p = prompt.New(readerFunc(func(b []byte) (int, error) { _ = os.Remove(state); return copy(b, "\n"), nil }), &out, true)
		}
		keyExpiryStep(context.Background(), cfg, sh, p, &out)
		return out.String()
	}

	if got := run("", false); got != "" {
		t.Errorf("expiry off: %q", got)
	}
	_ = os.WriteFile(state, nil, 0o644)
	if got := run("", false); !strings.Contains(got, "https://login.tailscale.com/admin/machines, the box, then Disable key expiry") || strings.Contains(got, "Press Enter") {
		t.Errorf("no terminal: %q", got)
	}
	if got := run("\n", false); !strings.Contains(got, "key expiry is still on for acme; doctor warns 30 days before it expires") {
		t.Errorf("left on: %q", got)
	}
	if got := run("", true); !strings.Contains(got, "key expiry is off for acme") {
		t.Errorf("turned off: %q", got)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(b []byte) (int, error) { return f(b) }
