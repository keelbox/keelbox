package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
)

func TestCompletionScripts(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		code, out, errs := run("completion", sh)
		if code != 0 || errs != "" {
			t.Fatalf("%s: %d %s", sh, code, errs)
		}
		for _, want := range []string{"keelbox", "attach", "ssh-user", "__boxes"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s completion lacks %q", sh, want)
			}
		}
		// Syntax-check with the shell itself where it is installed.
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		f := filepath.Join(t.TempDir(), "keelbox."+sh)
		if err := os.WriteFile(f, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", sh, err, out)
		}
	}
	if code, _, errs := run("completion", "tcsh"); code != 2 || !strings.Contains(errs, "bash, zsh or fish") {
		t.Errorf("unknown shell: %d %s", code, errs)
	}
	if code, _, _ := run("completion"); code != 2 {
		t.Error("no shell accepted")
	}
}

// bash completes commands, a command's flags and box names from the
// registry, by calling keelbox __boxes.
func TestBashCompletionCompletes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	isolate(t)
	register(t, config.Starter{Box: "devbox", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}})
	bin := filepath.Join(t.TempDir(), "keelbox")
	// A stand-in keelbox on PATH that answers __boxes from this registry.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = __boxes ] && echo devbox\n"), 0o755); err != nil { //nolint:gosec // G306: an executable
		t.Fatal(err)
	}
	_, script, _ := run("completion", "bash")
	complete := func(words ...string) string {
		t.Helper()
		line := strings.Join(words, " ")
		cmd := exec.Command(bash, "-c", script+"\nCOMP_WORDS=("+line+"); COMP_CWORD=$((${#COMP_WORDS[@]}-1)); _keelbox; echo \"${COMPREPLY[*]}\"")
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(bin)+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", line, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := complete("keelbox", "at"); got != "attach" {
		t.Errorf("command: %q", got)
	}
	if got := complete("keelbox", "attach", "''"); !strings.Contains(got, "devbox") || !strings.Contains(got, "--session") {
		t.Errorf("box names and flags: %q", got)
	}
	if got := complete("keelbox", "up", "--provider", "''"); got != "ssh local hetzner aws digitalocean gcp cloud-init" {
		t.Errorf("--provider values: %q", got)
	}
}
