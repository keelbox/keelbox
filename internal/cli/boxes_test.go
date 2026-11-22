package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
)

// isolate gives the test its own home, so the registry, known_hosts and
// file credential store are empty, and an empty working directory.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KEELBOX_CREDENTIAL_STORE", "file")
	t.Chdir(t.TempDir())
	return home
}

func register(t *testing.T, s config.Starter) string {
	t.Helper()
	out, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	p := boxConfigPath(s.Box)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveConfig(t *testing.T) {
	isolate(t)
	if _, err := resolveConfig("plan", nil, nil); err == nil || !strings.Contains(err.Error(), "keelbox up") {
		t.Errorf("no boxes: %v", err)
	}
	acme := register(t, config.StarterFor("acme", "ssh"))
	if p, err := resolveConfig("plan", nil, nil); err != nil || p != acme {
		t.Errorf("only box: %q %v", p, err)
	}
	if p, err := resolveConfig("plan", []string{"acme"}, nil); err != nil || p != acme {
		t.Errorf("by name: %q %v", p, err)
	}
	lab := register(t, config.StarterFor("lab", "hetzner"))
	if _, err := resolveConfig("attach", nil, nil); err == nil || !strings.Contains(err.Error(), "acme, lab") || !strings.Contains(err.Error(), "keelbox attach acme") {
		t.Errorf("two boxes: %v", err)
	}
	if p, err := resolveConfig("plan", []string{"lab"}, nil); err != nil || p != lab {
		t.Errorf("second box: %q %v", p, err)
	}
	if _, err := resolveConfig("plan", []string{"nope"}, nil); err == nil || !strings.Contains(err.Error(), "keelbox up nope") {
		t.Errorf("unknown box: %v", err)
	}
	if _, err := resolveConfig("plan", []string{"missing.yaml"}, nil); err == nil || !strings.Contains(err.Error(), "keelbox init missing.yaml") {
		t.Errorf("missing file: %v", err)
	}
	// A file in the working directory wins over a box of the same name,
	// and ./keelbox.yaml is the default before the registry.
	if err := os.WriteFile("acme", []byte(config.Example("here")), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, _ := resolveConfig("plan", []string{"acme"}, nil); p != "acme" {
		t.Errorf("file shadowed by box: %q", p)
	}
	if err := os.WriteFile("keelbox.yaml", []byte(config.Example("here")), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, _ := resolveConfig("plan", nil, nil); p != "keelbox.yaml" {
		t.Errorf("./keelbox.yaml not the default: %q", p)
	}
}

func TestCommandsTakeABoxName(t *testing.T) {
	isolate(t)
	register(t, config.StarterFor("acme", "local"))
	if code, _, errs := run("create", "acme"); code != 2 || !strings.Contains(errs, "provider hetzner") {
		t.Errorf("create by name: %d %s", code, errs)
	}
	if code, _, errs := run("create"); code != 2 || !strings.Contains(errs, "provider hetzner") {
		t.Errorf("create with the only box: %d %s", code, errs)
	}
}

func TestBoxRef(t *testing.T) {
	isolate(t)
	p := register(t, config.StarterFor("acme", "ssh"))
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := boxRef(cfg, p); got != "acme" {
		t.Errorf("registry config: %q", got)
	}
	if got := boxRef(cfg, "boxes/elsewhere.yaml"); got != "boxes/elsewhere.yaml" {
		t.Errorf("other path: %q", got)
	}
}

func TestList(t *testing.T) {
	isolate(t)
	if code, out, _ := run("ls"); code != 0 || !strings.Contains(out, "keelbox up") {
		t.Errorf("empty: %d %s", code, out)
	}
	register(t, config.StarterFor("acme", "ssh"))
	register(t, config.StarterFor("home", "local"))
	register(t, config.StarterFor("lab", "hetzner"))
	if err := saveUpState("lab", upState{Via: viaHetzner, Stage: stageApplied, Address: "192.0.2.7"}); err != nil {
		t.Fatal(err)
	}
	if err := saveUpState("acme", upState{Via: viaCloudInit, Stage: stageReady}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boxConfigPath("broken"), []byte("version: 1\nbox: broken\nbogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run("boxes")
	if code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{
		"BOX     PROVIDER          ADDRESS       STATUS",
		"acme    ssh (cloud-init)  203.0.113.10  ready",
		"broken  ?                 -             config error: keelbox plan broken",
		"home    local             this machine  -",
		"lab     hetzner           192.0.2.7     needs sign-in: keelbox up lab",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ls lacks %q:\n%s", want, out)
		}
	}
}

// Tests never run sudo: a command that would is recorded instead.
var sudoCalls [][]string

func init() {
	sudoRun = func(args []string, _, _ io.Writer) int {
		sudoCalls = append(sudoCalls, args)
		return 0
	}
	sudoNeedsPassword = func() bool { return false }
}
