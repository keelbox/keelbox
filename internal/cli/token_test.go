package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/credstore"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider/hetzner/hetznertest"
)

const goodToken = "goodTokenAbc123DoNotLeak"

// fakeHetzner points keelbox at a fake API and an in-memory credential
// store, with no browser to open.
func fakeHetzner(t *testing.T) (*hetznertest.API, *credstore.Memory) {
	t.Helper()
	api := hetznertest.New(t, goodToken)
	store := &credstore.Memory{}
	oldEndpoint, oldStore, oldBrowser := hetznerEndpoint, openStore, openBrowser
	// No hcloud CLI unless a test sets one up (fakeHcloud).
	oldRun, oldActive := runHcloud, hcloudActiveContext
	runHcloud = func(context.Context, string, ...string) (string, string, error) { return "", "", errNoHcloud }
	hcloudActiveContext = func(context.Context) string { return "" }
	t.Cleanup(func() { runHcloud, hcloudActiveContext = oldRun, oldActive })
	hetznerEndpoint = api.URL
	openStore = func(func(string)) credstore.Store { return store }
	openBrowser = func(string) error { t.Error("browser opened"); return nil }
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("HCLOUD_TOKEN", "")
	t.Cleanup(func() { hetznerEndpoint, openStore, openBrowser = oldEndpoint, oldStore, oldBrowser })
	return api, store
}

func hetznerCfg(t *testing.T, extra string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\ntarget: {provider: hetzner, hetzner: {server_type: cx23, location: fsn1, ssh_keys: [k]"+extra+"}}\n"+
		"users: {admin: {ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop\"]}}\n"), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHetznerTokenFromTheStore(t *testing.T) {
	isolate(t)
	_, store := fakeHetzner(t)
	ctx := context.Background()
	_ = store.Set(ctx, "hetzner", goodToken)
	var out bytes.Buffer
	got, err := hetznerToken(ctx, hetznerCfg(t, ""), "acme", prompt.New(strings.NewReader(""), &out, false), &out)
	if err != nil || got != goodToken || out.Len() != 0 {
		t.Fatalf("%q %v %s", got, err, out.String())
	}
	_ = store.Set(ctx, "work", "other")
	if got, _ := hetznerToken(ctx, hetznerCfg(t, ", credential: work"), "acme", prompt.New(nil, &out, false), &out); got != "other" {
		t.Errorf("named credential: %q", got)
	}
}

func TestHetznerTokenFileComesFirst(t *testing.T) {
	isolate(t)
	_, store := fakeHetzner(t)
	_ = store.Set(context.Background(), "hetzner", "from-store")
	f := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(f, []byte("from-file\n"), 0o600)
	got, err := hetznerToken(context.Background(), hetznerCfg(t, ", token_file: "+f), "acme", prompt.New(nil, &bytes.Buffer{}, false), &bytes.Buffer{})
	if err != nil || got != "from-file" {
		t.Errorf("%q %v", got, err)
	}
}

func TestHetznerTokenMissingWithoutATerminal(t *testing.T) {
	isolate(t)
	fakeHetzner(t)
	_, err := hetznerToken(context.Background(), hetznerCfg(t, ""), "acme", prompt.New(strings.NewReader(goodToken+"\n"), &bytes.Buffer{}, false), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "keelbox up acme") || !strings.Contains(err.Error(), "token_file") {
		t.Errorf("%v", err)
	}
}

func TestHetznerTokenAskedCheckedAndStored(t *testing.T) {
	isolate(t)
	api, store := fakeHetzner(t)
	api.Servers = []map[string]any{{"id": 1, "name": "web", "labels": map[string]string{}}}
	var out bytes.Buffer
	in := "wrong-token\n" + api.ReadOnlyToken + "\n" + goodToken + "\n"
	got, err := hetznerToken(context.Background(), hetznerCfg(t, ""), "acme", prompt.New(strings.NewReader(in), &out, true), &out)
	if err != nil || got != goodToken {
		t.Fatalf("%q %v\n%s", got, err, out.String())
	}
	if s, _ := store.Get(context.Background(), "hetzner"); s != goodToken {
		t.Errorf("stored %q", s)
	}
	o := out.String()
	for _, want := range []string{"console.hetzner.com/projects", "Read & Write", "refused the token", "read-only", "stored as \"hetzner\"", "1 server in this project was not created by keelbox"} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q:\n%s", want, o)
		}
	}
	if strings.Contains(o, goodToken) || strings.Contains(o, "wrong-token") {
		t.Errorf("a token reached the output:\n%s", o)
	}
}

func TestForgetToken(t *testing.T) {
	isolate(t)
	_, store := fakeHetzner(t)
	register(t, config.StarterFor("acme", "hetzner"))
	_ = store.Set(context.Background(), "hetzner", goodToken)
	code, out, errs := run("forget-token", "acme")
	if code != 0 || !strings.Contains(out, "Removed the Hetzner token \"hetzner\"") || !strings.Contains(out, "Security, API tokens") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if _, err := store.Get(context.Background(), "hetzner"); !errors.Is(err, credstore.ErrNotFound) {
		t.Errorf("still stored: %v", err)
	}
	if code, out, _ := run("forget-token"); code != 0 || !strings.Contains(out, "No Hetzner token") {
		t.Errorf("again: %d %s", code, out)
	}
	register(t, config.StarterFor("home", "local"))
	if code, _, errs := run("forget-token", "home"); code != 1 || !strings.Contains(errs, "uses no API token") {
		t.Errorf("local box: %d %s", code, errs)
	}
}
