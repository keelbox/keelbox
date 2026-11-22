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

// fakeVendors puts a claude, codex and gh on PATH that record how each was
// asked to sign in, and a config for this machine as the user.
func fakeVendors(t *testing.T) (*config.Config, string) {
	t.Helper()
	isolate(t)
	bin, state := t.TempDir(), t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("claude", `case "$1 $2" in "auth status") test -f `+state+`/claude;; "auth login") echo "$*" >`+state+`/claude;; esac`)
	write("codex", `case "$1 $2" in "login status") test -f `+state+`/codex;; "login --with-api-key") cat >`+state+`/codex;; *) exit 1;; esac`)
	write("gh", `exit 1`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("USER", "kbtest")
	out, err := config.Starter{Box: "lab", Provider: config.ProviderLocal, NoTailscale: true, Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(out), "lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Users.Admin.Name, cfg.Users.Agent.Name = "kbtest", "kbtest"
	return cfg, state
}

// The agent CLIs offer a fourth choice, their sign-in for API billing.
// The other choices keep their numbers. GitHub offers a fine-grained token
// first, preselected, before its browser sign-in.
func TestSignInOffersAPIBilling(t *testing.T) {
	cfg, state := fakeVendors(t)
	var buf strings.Builder
	// claude: a Console account; codex: skip; gh: skip, its third choice.
	p := prompt.New(strings.NewReader("4\n2\n3\n"), &buf, true)
	res := signInAll(context.Background(), cfg, options{}, "", p, &buf, &buf)
	if res.err != nil || strings.Join(res.signed, " ") != "claude-code" || strings.Join(res.skipped, " ") != "codex gh" {
		t.Fatalf("%+v\n%s", res, buf.String())
	}
	if got, _ := os.ReadFile(filepath.Join(state, "claude")); strings.TrimSpace(string(got)) != "auth login --console" {
		t.Errorf("claude was run as %q, want auth login --console", got)
	}
	flat := strings.Join(strings.Fields(buf.String()), " ")
	for _, want := range []string{
		"4) Use a Console account API billing, in place of a Claude plan",
		"4) Use an API key an OpenAI API key, in place of a ChatGPT account",
		"1) Use a fine-grained token only the repositories you choose",
		"2) Sign in with GitHub in a browser every repository your account can reach",
		"running `claude auth login --console` on lab as kbtest.",
		"Open the link it shows in a browser",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("output lacks %q:\n%s", want, buf.String())
		}
	}
	if strings.Count(flat, "4) ") != 3 {
		t.Errorf("not four choices for each sign-in:\n%s", buf.String())
	}
}

// keelbox login BOX codex --api runs Codex's key sign-in without a
// question, also where Codex is signed in already, and says nothing about
// a link or a device code.
func TestLoginAPIRunsTheKeySignIn(t *testing.T) {
	cfg, state := fakeVendors(t)
	if err := os.WriteFile(filepath.Join(state, "codex"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	// The test has no terminal to type a key at: the sign-in ends with no
	// key, which is the path this checks. The key's own path is below.
	res := signInAll(context.Background(), cfg, options{api: true}, config.AgentCodex, prompt.New(strings.NewReader(""), &buf, true), &buf, &buf)
	flat := strings.Join(strings.Fields(buf.String()), " ")
	if !strings.Contains(flat, "running `codex login --with-api-key` on lab as kbtest.") || !strings.Contains(flat, "Paste the key and press Enter; it is not shown.") {
		t.Errorf("%+v\n%s", res, buf.String())
	}
	for _, not := range []string{"Open the link", "Enter the code Codex shows", "printf", "stty"} {
		if strings.Contains(flat, not) {
			t.Errorf("output has %q:\n%s", not, buf.String())
		}
	}
	if strings.Join(res.failed, " ") != "codex" {
		t.Errorf("a sign-in with no key entered: %+v", res)
	}
}

// The line that takes the key hands Codex exactly what was typed, shows
// none of it, and stops when nothing was typed.
func TestCodexKeyLoginPassesTheKeyOnStdin(t *testing.T) {
	bin, state := t.TempDir(), t.TempDir()
	body := "#!/bin/sh\n[ \"$1 $2\" = \"login --with-api-key\" ] || exit 9\n[ $# -eq 2 ] || exit 9\ncat >" + state + "/key\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(stdin string) (string, error) {
		c := exec.Command("sh", "-c", onCtrlC(codexKeyLogin))
		c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		c.Stdin = strings.NewReader(stdin)
		out, err := c.CombinedOutput()
		return string(out), err
	}
	const key = `sk-proj-A1 b$2'c"d\e`
	out, err := run(key + "\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(state, "key")); string(got) != key {
		t.Errorf("codex read %q, want %q", got, key)
	}
	if strings.Contains(out, "sk-proj") || !strings.Contains(out, "OpenAI API key (not shown), then Enter: ") {
		t.Errorf("output: %q", out)
	}
	if out, err := run("\n"); err == nil || !strings.Contains(out, "No key entered.") {
		t.Errorf("no key: %v, %q", err, out)
	}
}

func TestAPISignInRefusal(t *testing.T) {
	cfg, _ := fakeVendors(t)
	for only, want := range map[string]string{
		config.AgentCodex:      "",
		config.AgentClaudeCode: "",
		"":                     "--api takes the sign-in it is for: keelbox login lab codex --api",
		"gh":                   "the GitHub sign-in has no API route; --api is for claude-code and codex: keelbox login lab codex --api",
	} {
		if got := apiSignInRefusal(cfg, only, "lab"); got != want {
			t.Errorf("%q: %q, want %q", only, got, want)
		}
	}
	// A box without Codex: --api is not what is wrong, and the sign-in step
	// says how to add Codex.
	cfg.Agents = []string{config.AgentClaudeCode}
	if got := apiSignInRefusal(cfg, config.AgentCodex, "lab"); got != "" {
		t.Errorf("codex --api on a box without Codex: %q", got)
	}
	var buf bytes.Buffer
	res := signInAll(context.Background(), cfg, options{api: true}, config.AgentCodex, prompt.New(strings.NewReader(""), &buf, true), &buf, &buf)
	if res.err == nil || !strings.HasSuffix(res.err.Error(), "Add it, which installs it and then signs in: keelbox add lab codex --api") {
		t.Errorf("codex --api on a box without Codex: %v", res.err)
	}
	cfg.Agents = nil
	if got := apiSignInRefusal(cfg, "gh", "lab"); !strings.Contains(got, "lab has neither agent") {
		t.Errorf("no agents: %q", got)
	}
}

// A sign-in the box's config leaves out is an error that ends with the
// command that adds it.
func TestNoSuchSignInSaysHowToAddIt(t *testing.T) {
	cfg, _ := fakeVendors(t)
	cfg.Agents = []string{config.AgentClaudeCode}
	off := false
	cfg.Runtime.GH, cfg.Access.Tailscale.Enabled = &off, &off
	for only, want := range map[string]string{
		config.AgentCodex: "lab has no codex sign-in: its config does not list codex under agents. Add it, which installs it and then signs in: keelbox add lab codex",
		"gh":              "lab has no gh sign-in: its config has runtime.gh off. Add it, which installs it and then signs in: keelbox add lab gh",
		"tailscale": "lab has no tailscale sign-in: its config has Tailscale off. Set `enabled: true` under access.tailscale in " + tildePath(boxConfigPath("lab")) +
			". Apply it, then run keelbox login lab tailscale again: keelbox apply lab",
	} {
		var buf bytes.Buffer
		res := signInAll(context.Background(), cfg, options{}, only, prompt.New(strings.NewReader(""), &buf, true), &buf, &buf)
		if res.err == nil || res.err.Error() != want {
			t.Errorf("%s:\n got %v\nwant %s", only, res.err, want)
		}
	}
}

// The GitHub token sign-in reads the token on the box with echo off and
// hands it to gh on its input: never in an argument.
func TestGHTokenSignIn(t *testing.T) {
	cfg, state := fakeVendors(t)
	var buf strings.Builder
	// claude: skip; codex: skip; gh: Enter, the token.
	p := prompt.New(strings.NewReader("2\n2\n\n"), &buf, true)
	res := signInAll(context.Background(), cfg, options{}, "", p, &buf, &buf)
	if res.err != nil {
		t.Fatalf("%+v\n%s", res, buf.String())
	}
	flat := strings.Join(strings.Fields(buf.String()), " ")
	for _, want := range []string{"running `gh auth login --with-token` on lab as kbtest.", "https://github.com/settings/personal-access-tokens/new"} {
		if !strings.Contains(flat, want) {
			t.Errorf("output lacks %q:\n%s", want, buf.String())
		}
	}
	_ = state
	// The token goes to gh on its input, from a variable the shell read
	// with echo off: never an argument.
	if !strings.Contains(ghTokenLogin, "stty -echo") || !strings.HasSuffix(ghTokenLogin, `printf %s "$k" | gh auth login --hostname github.com --git-protocol https --with-token`) || strings.Count(ghTokenLogin, "$k") != 2 {
		t.Errorf("ghTokenLogin: %s", ghTokenLogin)
	}
}
