package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/prompt"
)

// fakeHcloud puts a fake hcloud on PATH with one context, "work", whose
// token is the fake Hetzner API's, and restores the real runner. The
// script logs its arguments and the two variables keelbox sets or removes.
func fakeHcloud(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	script := "#!/bin/sh\n" +
		"echo \"args=$* context=${HCLOUD_CONTEXT-unset} token=${HCLOUD_TOKEN-unset}\" >> '" + log + "'\n" +
		"case \"$*\" in\n" +
		"  'context active') echo work;;\n" +
		"  'config get token --allow-sensitive')\n" +
		"    if [ \"$HCLOUD_CONTEXT\" = work ]; then echo '" + goodToken + "'; else echo \"Warning: active context \\\"$HCLOUD_CONTEXT\\\" not found\" >&2; echo; fi;;\n" +
		"  *) echo \"hcloud: unknown command\" >&2; exit 1;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "hcloud"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The real runner again; fakeHetzner's cleanup undoes this.
	runHcloud, hcloudActiveContext = execHcloud, activeHcloudContext
	return log
}

func TestHetznerTokenFromTheHcloudCLI(t *testing.T) {
	isolate(t)
	_, store := fakeHetzner(t)
	log := fakeHcloud(t)
	// A stray HCLOUD_CONTEXT of the user's does not change which context
	// the config names.
	t.Setenv("HCLOUD_CONTEXT", "other")
	cfg := hetznerCfg(t, ", hcloud_context: work")
	noPrompt := prompt.New(strings.NewReader(""), nil, false)
	tok, err := hetznerToken(context.Background(), cfg, "acme", noPrompt, nil)
	if err != nil || tok != goodToken {
		t.Fatalf("%q %v", tok, err)
	}
	ran, _ := os.ReadFile(log)
	if string(ran) != "args=config get token --allow-sensitive context=work token=unset\n" {
		t.Errorf("hcloud ran as:\n%s", ran)
	}
	// keelbox kept no copy.
	if _, err := store.Get(context.Background(), "hetzner"); err == nil {
		t.Error("the token was stored")
	}
	if entries, _ := filepath.Glob(filepath.Join(configDir(), "credentials", "*")); len(entries) != 0 {
		t.Errorf("credential files: %v", entries)
	}

	// A context hcloud does not have: the error names the fix, not a token.
	_, err = hetznerToken(context.Background(), hetznerCfg(t, ", hcloud_context: gone"), "acme", noPrompt, nil)
	if err == nil || !strings.Contains(err.Error(), `no token for a context named "gone"`) || !strings.Contains(err.Error(), "hcloud context list") ||
		!strings.Contains(err.Error(), "keelbox up acme") || strings.Contains(err.Error(), goodToken) {
		t.Errorf("%v", err)
	}
	// hcloud not installed.
	t.Setenv("PATH", t.TempDir())
	_, err = hetznerToken(context.Background(), cfg, "acme", noPrompt, nil)
	if err == nil || !strings.Contains(err.Error(), "not installed or not on PATH") || !strings.Contains(err.Error(), "remove hcloud_context") {
		t.Errorf("%v", err)
	}
}

// keelbox up offers the hcloud CLI's active context instead of asking for
// a token, and writes the context's name, not the token, to the config.
func TestUpHetznerOffersTheHcloudContext(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	_, store := fakeHetzner(t)
	fakeHcloud(t)
	in := script(
		"3",    // where: a new cloud server
		"1",    // which cloud: Hetzner
		"acme", // name
		"",     // use the hcloud context "work": yes
		"",     // location
		"",     // size
		"",     // agents
		"",     // GitHub CLI
		"",     // Tailscale
		"",     // go ahead
		"n",    // Remote Control
	)
	code, out := upWith(t, &fakeRunner{signed: true}, in, "up")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, `Use the token of the hcloud context "work"?`) || strings.Contains(out, "Hetzner API token (input hidden)") {
		t.Errorf("questions:\n%s", out)
	}
	if strings.Contains(out, goodToken) {
		t.Error("the token is in the output")
	}
	cfg, _ := loadBox(t, "acme")
	if cfg.Target.Hetzner.HcloudContext != "work" || cfg.Target.Hetzner.TokenFile != "" {
		t.Errorf("hetzner target %+v", cfg.Target.Hetzner)
	}
	raw, _ := os.ReadFile(boxConfigPath("acme"))
	if strings.Contains(string(raw), goodToken) || !strings.Contains(string(raw), "hcloud_context: work") {
		t.Errorf("config:\n%s", raw)
	}
	if _, err := store.Get(context.Background(), "hetzner"); err == nil {
		t.Error("the token was stored")
	}
}

// Declining the offer asks for a token as before.
func TestUpHetznerDecliningTheHcloudContext(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	_, store := fakeHetzner(t)
	fakeHcloud(t)
	in := script("3", "1", "acme", "n", goodToken, "", "", "", "", "", "", "n")
	code, out := upWith(t, &fakeRunner{signed: true}, in, "up")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, _ := loadBox(t, "acme")
	if cfg.Target.Hetzner.HcloudContext != "" {
		t.Errorf("hetzner target %+v", cfg.Target.Hetzner)
	}
	if tok, err := store.Get(context.Background(), "hetzner"); err != nil || tok != goodToken {
		t.Errorf("stored %q %v", tok, err)
	}
}

func TestUpHetznerHcloudContextFlag(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	fakeHetzner(t)
	fakeHcloud(t)
	code, out := upWith(t, &fakeRunner{signed: true}, nil, "up", "acme", "--provider", "hetzner", "--hcloud-context", "work", "--ssh-key", key, "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "acme"); cfg.Target.Hetzner.HcloudContext != "work" {
		t.Errorf("hetzner target %+v", cfg.Target.Hetzner)
	}
	code, out = upWith(t, &fakeRunner{}, nil, "up", "acme2", "--provider", "hetzner", "--hcloud-context", "gone", "--ssh-key", key, "--yes")
	if code != 2 || !strings.Contains(out, `no token for a context named "gone"`) || isRegistered("acme2") {
		t.Errorf("exit %d\n%s", code, out)
	}
}
