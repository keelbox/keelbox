package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/credstore"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider/hetzner"
	"github.com/keelbox/keelbox/internal/ui"
)

// The Hetzner API token (docs/adr/0026-credential-store.md). Hetzner has no
// delegated sign-in (docs/adr/0027-delegated-auth.md), so the user creates
// a project token in the console; keelbox asks for it without echo, checks
// it against the API and keeps it in the OS credential store. It is never
// written to a config, a command line, the environment of a child process,
// output or a log.

// hetznerEndpoint is the API keelbox talks to; tests point it at a fake.
var hetznerEndpoint = hetzner.DefaultEndpoint

// legacyTokenFile is a token saved by hand at keelbox's default path. It is
// read when nothing else is set.
const legacyTokenFile = "~/.config/keelbox/hetzner.token" //nolint:gosec // G101: a path, not a credential

// openStore returns the credential store; tests replace it.
var openStore = func(warn func(string)) credstore.Store {
	return credstore.Default(filepath.Join(configDir(), "credentials"), warn)
}

// openBrowser opens a URL in the user's browser, where there is one.
var openBrowser = func(url string) error {
	switch {
	case runtime.GOOS == "darwin":
		return exec.Command("open", url).Run()
	case os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "":
		return exec.Command("xdg-open", url).Run()
	}
	return errors.New("no browser here")
}

func canOpenBrowser() bool {
	return runtime.GOOS == "darwin" || os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func newHetznerClient(token string) *hetzner.Client {
	c := hetzner.New(token)
	c.Endpoint = hetznerEndpoint
	return c
}

// errNoToken explains how to give keelbox a token when it cannot ask.
func errNoToken(cfg *config.Config, ref string) error {
	return fmt.Errorf("no Hetzner API token for %s (credential %q is not in the credential store), and no terminal to ask on. "+
		"Store one, in a terminal: keelbox up %s; or for CI set target.hetzner.token_file to a mode 600 file", cfg.Box, cfg.Target.Hetzner.Credential, ref)
}

// hetznerToken finds the box's API token: HCLOUD_TOKEN, then
// target.hetzner.token_file, then the hcloud CLI's context when
// target.hetzner.hcloud_context names one (hcloud.go), then the credential
// store, then the token file at the default path. With none and a
// terminal, it asks.
func hetznerToken(ctx context.Context, cfg *config.Config, ref string, p *prompt.Prompter, out io.Writer) (string, error) {
	if t := os.Getenv("HCLOUD_TOKEN"); t != "" {
		return t, nil
	}
	h := cfg.Target.Hetzner
	if h.TokenFile != "" {
		return hetzner.TokenFromFile(config.ExpandHome(h.TokenFile))
	}
	if h.HcloudContext != "" {
		return hcloudToken(ctx, h.HcloudContext, ref)
	}
	store := openStore(warnTo(out))
	t, err := store.Get(ctx, h.Credential)
	switch {
	case err == nil:
		return t, nil
	case !errors.Is(err, credstore.ErrNotFound):
		return "", fmt.Errorf("read the Hetzner token from %s: %w", store.Name(), err)
	}
	if legacy := config.ExpandHome(legacyTokenFile); fileExists(legacy) {
		return hetzner.TokenFromFile(legacy)
	}
	if !p.Interactive() {
		return "", errNoToken(cfg, ref)
	}
	sayAsIs(out, p.Style(), p.Style().Faint(p.Style().Bullet()), fmt.Sprintf("%s needs the Hetzner API token, and %s has none named %q.", cfg.Box, tildeText(store.Name()), h.Credential))
	t, _, err = askHetznerToken(ctx, p, out, store, h.Credential)
	return t, err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func warnTo(out io.Writer) func(string) {
	return func(s string) { fmt.Fprintln(out, "keelbox: warning:", s) }
}

// askHetznerToken walks the user through creating a project token, reads
// it without echo, checks it against the API and stores it as key.
func askHetznerToken(ctx context.Context, p *prompt.Prompter, out io.Writer, store credstore.Store, key string) (string, hetzner.TokenCheck, error) {
	st := p.Style()
	num := func(n string) string { return st.BoldAccent(n) }
	fmt.Fprint(out, "\n"+st.Card(ui.Card{
		Title: "Create a Hetzner API token",
		Lines: []ui.Line{
			ui.Text("keelbox creates and manages the server with a Hetzner Cloud API token. " +
				"Hetzner has no sign-in that other tools can use, so you create the token."),
			ui.Text(st.Dim("A token can reach every server in its project and nothing outside it: " +
				"in a project used only for keelbox, your other servers are out of its reach.")),
			ui.Blank(),
			ui.Row(num("1."), "Open "+st.URL(hetzner.ConsoleURL)+` and create a project (say "keelbox"), or open the one you keep boxes in.`),
			ui.Row(num("2."), "In the project: "+st.Bold("Security, API tokens, Generate API token")+". Name it keelbox and choose "+st.Bold("Read & Write")+"."),
			ui.Row(num("3."), "Copy the token and paste it below. It is not shown, and keelbox keeps it in "+tildeText(store.Name())+"."),
		},
	})+"\n")
	if canOpenBrowser() {
		if ok, err := p.Confirm("Open the Hetzner Console in your browser?", true); err != nil {
			return "", hetzner.TokenCheck{}, err
		} else if ok {
			if err := openBrowser(hetzner.ConsoleURL); err != nil {
				fmt.Fprintf(out, "  %s could not open a browser (%v); open %s yourself\n", st.Bang(), err, hetzner.ConsoleURL)
			}
		}
	}
	// On a terminal the token is asked for until one works; Ctrl-C stops.
	var last error
	for i := 0; p.OnTerminal() || i < 3; i++ {
		t, err := p.Secret("Hetzner API token")
		if err != nil {
			return "", hetzner.TokenCheck{}, err
		}
		tc, err := newHetznerClient(t).CheckToken(ctx)
		if err != nil {
			last = err
			fmt.Fprintf(out, "  %s %v\n", st.Cross(), err)
			continue
		}
		if err := store.Set(ctx, key, t); err != nil {
			return "", tc, fmt.Errorf("store the token: %w", err)
		}
		where := store.Name()
		if c, ok := store.(*credstore.Chain); ok {
			where = c.StoredIn()
		}
		where = tildeText(where)
		say(out, st, st.Tick(), fmt.Sprintf("token works (%s in the project); stored as %q in %s. Remove it: %s", plural(tc.Servers, "server", "servers"), key, where, st.Cmd(ui.Unbroken("keelbox forget-token"))))
		if tc.Unmanaged > 0 {
			say(out, st, st.Bang(), fmt.Sprintf("note: %s in this project %s not created by keelbox. keelbox never changes them (it touches only servers labelled keelbox=true), but the token can; a project of its own keeps them out of reach.", plural(tc.Unmanaged, "server", "servers"), map[bool]string{true: "was", false: "were"}[tc.Unmanaged == 1]))
		}
		return t, tc, nil
	}
	return "", hetzner.TokenCheck{}, fmt.Errorf("no working token after three tries (%v); create one at %s, then run keelbox up again", last, hetzner.ConsoleURL)
}

// cmdForgetToken removes a box's Hetzner token from the credential store.
func cmdForgetToken(ctx context.Context, pos []string, stdout, stderr io.Writer) int {
	// With no box named, the token keelbox up stores: whatever box is the
	// only one, keelbox up's own prompt says this command removes it.
	key, ref := "hetzner", ""
	if len(pos) > 0 {
		path, err := resolveConfig("forget-token", pos, nil)
		if err != nil {
			fail(stderr, err)
			return 2
		}
		cfg, err := config.Load(path)
		if err != nil {
			fail(stderr, err)
			return 2
		}
		if cfg.Target.Provider != config.ProviderHetzner {
			failf(stderr, "%s is provider %s and uses no API token; nothing to forget\n", cfg.Box, cfg.Target.Provider)
			return 1
		}
		key, ref = cfg.Target.Hetzner.Credential, boxRef(cfg, path)
		if c := cfg.Target.Hetzner.HcloudContext; c != "" {
			fmt.Fprintf(stdout, "%s reads its token from the hcloud CLI's context %q each time; keelbox keeps no copy of it. Remove it there: hcloud context delete %s\n", cfg.Box, c, c)
		}
		if f := cfg.Target.Hetzner.TokenFile; f != "" {
			fmt.Fprintf(stdout, "%s reads its token from target.hetzner.token_file (%s); keelbox does not delete files you made. Remove it: rm %s\n", cfg.Box, f, f)
		}
	}
	store := openStore(nil)
	if _, err := store.Get(ctx, key); errors.Is(err, credstore.ErrNotFound) {
		fmt.Fprintf(stdout, "No Hetzner token named %q is stored.\n", key)
		return 0
	}
	if err := store.Delete(ctx, key); err != nil {
		failf(stderr, "forget-token: %v", err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed the Hetzner token %q from %s.\n", key, tildeText(store.Name()))
	fmt.Fprintf(stdout, "The token still works until you delete it in the Hetzner Console: %s, the project, Security, API tokens.\n", hetzner.ConsoleURL)
	if ref != "" {
		fmt.Fprintf(stdout, "The next command that needs it asks again, or: keelbox up %s\n", ref)
	}
	return 0
}
