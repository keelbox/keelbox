package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/keelbox/keelbox/internal/ui"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
)

// shell runs commands on the box as one account: over SSH from a laptop, or
// through sudo on the box itself.
type shell struct {
	ssh  *provider.SSH // nil: this machine is the box
	user string
}

// boxShell reaches the box as user. ssh logs in as user directly: agent
// sessions should look exactly like the user's own SSH from a phone.
func boxShell(ctx context.Context, cfg *config.Config, o options, user string, stdout, stderr io.Writer) (*shell, error) {
	agent := cfg.Users.Agent
	if cfg.Target.Provider != config.ProviderLocal && user == agent.Name && len(agent.SSHAuthorizedKeys)+len(agent.SSHAuthorizedKeyFiles) == 0 {
		return nil, errNoAgentKey
	}
	switch cfg.Target.Provider {
	case config.ProviderLocal:
		return &shell{user: user}, nil
	case config.ProviderSSH:
		a := reach(ctx, cfg, addr{cfg.Target.SSH.Host, cfg.Target.SSH.Port}, stderr)
		return &shell{ssh: sshTarget(cfg, a, o, stdout, stderr).As(user), user: user}, nil
	}
	if isCloud(cfg.Target.Provider) {
		cl, err := openCloudKnown(ctx, cfg, o, terminalPrompt(ctx, stdout, o.style), boxRef(cfg, o.configFile), stdout)
		if err != nil {
			return nil, err
		}
		public, _, err := cloudPublic(ctx, cl, cfg, boxRef(cfg, o.configFile))
		if err != nil {
			return nil, err
		}
		return &shell{ssh: sshTarget(cfg, reach(ctx, cfg, public, stderr), o, stdout, stderr).As(user), user: user}, nil
	}
	return nil, fmt.Errorf("unknown provider %q", cfg.Target.Provider)
}

// local wraps a command to run as the account on this machine.
func (s *shell) local(ctx context.Context, remote string) *exec.Cmd {
	if os.Geteuid() == 0 || os.Getenv("USER") != s.user {
		return exec.CommandContext(ctx, "sudo", "-iu", s.user, "--", "sh", "-c", remote)
	}
	return exec.CommandContext(ctx, "sh", "-c", remote)
}

// interactive runs remote on the user's terminal; keelbox sees none of it.
func (s *shell) interactive(ctx context.Context, remote string) error {
	_, err := s.session(ctx, remote)
	return err
}

// session is interactive, and reports whether the session was opened
// before it ended (provider.SSH.InteractiveOpened): only then is ssh's
// exit code 255 a connection that was lost.
func (s *shell) session(ctx context.Context, remote string) (opened bool, err error) {
	if s.ssh != nil {
		opened, err := s.ssh.InteractiveOpened(ctx, remote, os.Stdin, os.Stdout, os.Stderr)
		if cutOff(err) && prompt.IsTerminal(os.Stderr) {
			_, _ = io.WriteString(os.Stderr, terminalRestore)
		}
		return opened, err
	}
	return true, s.interactiveLocal(ctx, remote)
}

func (s *shell) interactiveLocal(ctx context.Context, remote string) error {
	if remote == "" {
		remote = "exec bash -l"
	}
	c := s.local(ctx, remote)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// terminalRestore turns off what a full-screen program on the box turned on
// in the user's terminal and never turned off, because ssh ended under it:
// tmux and the agent CLIs ask for mouse reports, bracketed paste, focus
// reports and extended keys, hide the cursor and draw on the alternate
// screen. Left on, every movement of the mouse types into the user's shell.
// Each sequence does nothing in a terminal where the mode is off. The cursor
// is saved before the alternate screen is left, since leaving it restores
// the saved cursor: in a terminal that was not on it, the cursor stays.
const terminalRestore = "\x1b7\x1b[?1049l" + // back from the alternate screen
	"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" + // mouse reports
	"\x1b[?1004l\x1b[?2004l" + // focus reports, bracketed paste
	"\x1b[>4m\x1b[<u" + // extended keys: modifyOtherKeys, the kitty protocol
	"\x1b[?1l\x1b>" + // cursor keys and keypad as a shell expects them
	"\x1b[0m\x1b[?25h" // colours, and the cursor shown

// tailnet4 is the range Tailscale gives its IPv4 addresses from.
var tailnet4 = netip.MustParsePrefix("100.64.0.0/10")

// cutOff reports whether a session on the terminal ended the way a lost
// connection ends it: ssh's own exit code, 255, or ssh killed by a signal.
// keelbox read nothing of the session; the exit code is all it has.
func cutOff(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && (ee.ExitCode() == 255 || ee.ExitCode() < 0)
}

// succeeds runs a status check without a terminal, discarding its output:
// only the exit code is used, so nothing a CLI prints about an account is
// read or kept. Over SSH the check exits 0 or 1, so that ssh's own 255 (no
// connection, no login) is an error and not the check's answer: a box that
// did not answer is not one that is signed out.
func (s *shell) succeeds(ctx context.Context, remote string) (bool, error) {
	if s.ssh != nil {
		if err := s.ssh.TrustHost(ctx); err != nil {
			return false, err
		}
		_, err := s.ssh.Output(ctx, remote+" >/dev/null 2>&1 || exit 1")
		// Ctrl-C reaches ssh as well, which then exits 255 too: that is the
		// user stopping, not a box that did not answer.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 255 && ctx.Err() == nil {
			return false, err
		}
		return err == nil, nil
	}
	return s.local(ctx, remote+" >/dev/null 2>&1").Run() == nil, nil
}

// exitCode runs a check that has more than two answers, without a terminal
// and with its output discarded on the box: its exit code is all keelbox
// gets. -1 is a check that did not run to its end. Over SSH, 255 is also
// ssh's own failure, so a check run this way must not use 255 as an answer.
func (s *shell) exitCode(ctx context.Context, remote string) int {
	var err error
	if s.ssh != nil {
		if err = s.ssh.TrustHost(ctx); err != nil {
			return -1
		}
		_, err = s.ssh.Output(ctx, remote+" >/dev/null 2>&1")
	} else {
		err = s.local(ctx, remote+" >/dev/null 2>&1").Run()
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	}
	return -1
}

// why words the reason a check on the box could not run.
func (s *shell) why(ctx context.Context, cfg *config.Config, err error) error {
	return sshFailure(ctx, err, cfg, s.ssh, options{}, "")
}

func (s *shell) output(ctx context.Context, remote string) (string, error) {
	if s.ssh != nil {
		if err := s.ssh.TrustHost(ctx); err != nil {
			return "", err
		}
		return s.ssh.Output(ctx, remote)
	}
	out, err := s.local(ctx, remote).Output()
	return string(out), err
}

// pipe runs remote with in as its input, its output discarded unless it
// fails.
func (s *shell) pipe(ctx context.Context, remote string, in io.Reader) error {
	if s.ssh != nil {
		if err := s.ssh.TrustHost(ctx); err != nil {
			return err
		}
		return s.ssh.Input(ctx, remote, in)
	}
	c := s.local(ctx, remote)
	c.Stdin = in
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// signIn is one account keelbox walks the user through. Status and Login
// are the vendor's own commands; keelbox only runs them on the user's
// terminal.
type signIn struct {
	name, status, login, after string
	admin                      bool // run as the admin account (tailscale up needs root)
	api                        apiSignIn
}

// apiSignIn is a vendor's sign-in for API billing, where it has one: the
// account pays by use, with no plan. An empty login means there is none.
type apiSignIn struct {
	login  string // what runs on the user's terminal
	shown  string // the vendor's command in it, as messages name it
	label  string // the choice in the menu
	detail string
	hint   string // said before it runs, in place of the line about a link
	// first offers it before the vendor's own sign-in, and preselects it:
	// GitHub's browser sign-in reaches every repository the account can.
	first bool
}

// ghTokenLogin is the GitHub CLI's sign-in with a fine-grained token, read
// on the box with the terminal's echo off and handed to gh on its input,
// as codexKeyLogin does: in no command's arguments or environment, and
// never read by keelbox on this computer.
const ghTokenLogin = `printf 'Fine-grained token (not shown), then Enter: '; trap 'stty echo 2>/dev/null; echo' EXIT; stty -echo 2>/dev/null; ` +
	`IFS= read -r k; trap - EXIT; stty echo 2>/dev/null; echo; ` +
	`[ -n "$k" ] || { echo 'No token entered.' >&2; exit 1; }; printf %s "$k" | gh auth login --hostname github.com --git-protocol https --with-token`

// ghTokenHint goes before the token sign-in: where to make one, and what
// it needs for the agent's git and pull requests.
const ghTokenHint = "Make one at https://github.com/settings/personal-access-tokens/new: Only select repositories, then Contents, Pull requests and Workflows read and write. Paste it and press Enter; it is not shown. gh keeps it on the box, where the agent can read it." //nolint:gosec // G101: instructions, no credential

// codexKeyLogin is Codex's sign-in with an OpenAI API key. Codex reads the
// key from its input up to the end, so typed at a terminal it would show on
// the screen and need Ctrl-D. This line asks for it with the terminal's echo
// off and hands it to Codex. The key is read on the box, by the shell of the
// account that signs in: it is in no command's arguments and no environment,
// and keelbox on this computer reads none of it, as with every sign-in.
const codexKeyLogin = `printf 'OpenAI API key (not shown), then Enter: '; trap 'stty echo 2>/dev/null; echo' EXIT; stty -echo 2>/dev/null; ` +
	`IFS= read -r k; trap - EXIT; stty echo 2>/dev/null; echo; ` +
	`[ -n "$k" ] || { echo 'No key entered.' >&2; exit 1; }; printf %s "$k" | codex login --with-api-key`

const codexKeyHint = "Paste the key and press Enter; it is not shown. Codex keeps it on the box in ~/.codex/auth.json, which the agent can read."

func signIns(cfg *config.Config) []signIn {
	var out []signIn
	if cfg.Tailscale() && cfg.Access.Tailscale.AuthKeyFile == "" {
		out = append(out, signIn{
			name:   "tailscale",
			status: `tailscale status --json | grep -q '"BackendState": *"Running"'`,
			login:  "sudo tailscale up --hostname=" + cfg.Access.Tailscale.Hostname,
			admin:  true,
		})
	}
	if cfg.HasAgent(config.AgentClaudeCode) {
		detail := "API billing, in place of a Claude plan"
		if cfg.RemoteControl() {
			detail += "; Remote Control needs a plan"
		}
		out = append(out, signIn{name: config.AgentClaudeCode, status: "claude auth status", login: "claude auth login",
			api: apiSignIn{login: "claude auth login --console", shown: "claude auth login --console",
				label: "Use a Console account", detail: detail}})
	}
	if cfg.HasAgent(config.AgentCodex) {
		out = append(out, signIn{name: config.AgentCodex, status: "codex login status", login: "codex login --device-auth",
			api: apiSignIn{login: codexKeyLogin, shown: "codex login --with-api-key",
				label: "Use an API key", detail: "an OpenAI API key, in place of a ChatGPT account", hint: codexKeyHint}})
	}
	if cfg.GH() {
		out = append(out, signIn{name: "gh", status: "gh auth status",
			login: ghLogin, after: "gh auth setup-git",
			api: apiSignIn{login: ghTokenLogin, shown: "gh auth login --with-token",
				label: "Use a fine-grained token", detail: "only the repositories you choose", hint: ghTokenHint, first: true}})
	}
	return out
}

// signInLinkHint goes before a sign-in that ends in a browser.
const signInLinkHint = "Open the link it shows in a browser on this computer, sign in, and paste any code it asks for back here."

// codexCodeHint goes before Codex's device sign-in. Codex prints a new
// one-time code each attempt, and a code from an earlier attempt (or the
// browser's autofill) fails with "We couldn't authorize this device".
// keelbox does not read the vendor's output (ADR 0014), so it points at the newest code rather than repeating it.
const codexCodeHint = "Enter the code Codex shows below, from this attempt: not one from an earlier attempt or your browser's autofill, which fails with \"We couldn't authorize this device\"."

// ghLogin is the GitHub CLI's sign-in in a browser. At a terminal gh waits
// for Enter before it opens a browser, and starts waiting for the approval
// only after. A box has no browser: someone who opened the link themselves
// and approved saw nothing happen until they pressed Enter, and then a line
// that opening the browser failed. GH_PROMPT_DISABLED is gh's own setting
// for no questions: with it gh prints the code and the link and waits for
// the approval at once. --clipboard=false keeps gh from trying to copy the
// code, which on a box ends in a line that it failed to.
const ghLogin = "GH_PROMPT_DISABLED=1 gh auth login --hostname github.com --git-protocol https --web --clipboard=false"

// ghCodeHint goes before the GitHub CLI's sign-in, in place of the line
// about a link: the code goes into the browser, and nothing is typed back.
const ghCodeHint = "gh shows a one-time code and a link. Open the link in a browser on this computer and type the code there. gh finishes once you approve."

// signInTitle is how messages name a sign-in.
func signInTitle(name string) string {
	switch name {
	case "tailscale":
		return "Tailscale"
	case config.AgentClaudeCode:
		return "Claude Code"
	case config.AgentCodex:
		return "Codex"
	case "gh":
		return "GitHub"
	}
	return name
}

// signInPurpose says what a sign-in is for, before the user chooses.
func signInPurpose(cfg *config.Config, name string) string {
	switch name {
	case "tailscale":
		return "joins " + cfg.Box + " to your tailnet, so you reach it privately; public SSH closes after"
	case config.AgentClaudeCode:
		return "your Claude account, for Claude Code"
	case config.AgentCodex:
		return "your ChatGPT account or an OpenAI API key, for Codex"
	case "gh":
		return "GitHub access for the agent, for git push and pull requests; a token for some repositories keeps the rest out of its reach"
	}
	return name
}

// signInName reads a sign-in named on the command line.
func signInName(s string) (string, bool) {
	switch strings.ToLower(s) {
	case "tailscale":
		return "tailscale", true
	case "claude", config.AgentClaudeCode:
		return config.AgentClaudeCode, true
	case config.AgentCodex:
		return config.AgentCodex, true
	case "gh", "github":
		return "gh", true
	}
	return "", false
}

// signInState is where one sign-in stands, and the shell that checks it.
type signInState struct {
	signIn
	sh   *shell // nil when the account has no shell to check with
	done bool
	// unchecked is why the check could not run: no shell for the account,
	// or ssh itself failing. Such a sign-in is neither done nor known to be
	// missing.
	unchecked error
}

// signInStates checks every sign-in, or only the one named, from exit
// codes only. A check that could not run is not an answer: its state
// carries the reason, and the other sign-ins are still checked, each
// account with its own shell. Once the box itself does not answer, the
// rest are not tried: each would wait out the same timeout.
func signInStates(ctx context.Context, cfg *config.Config, o options, only string, stderr io.Writer) []signInState {
	type account struct {
		sh  *shell
		err error
	}
	accounts := map[bool]*account{}
	var down error
	var out []signInState
	for _, si := range signIns(cfg) {
		if only != "" && si.name != only {
			continue
		}
		a := accounts[si.admin]
		if a == nil {
			user := cfg.Users.Agent.Name
			if si.admin {
				user = cfg.Users.Admin.Name
			}
			a = &account{}
			a.sh, a.err = boxShell(ctx, cfg, o, user, io.Discard, stderr)
			accounts[si.admin] = a
		}
		st := signInState{signIn: si, sh: a.sh}
		switch {
		case a.err != nil:
			st.unchecked = a.err
		case down != nil:
			st.unchecked = down
		default:
			ok, err := a.sh.succeeds(ctx, si.status)
			if err != nil {
				st.unchecked = a.sh.why(ctx, cfg, err)
				var gone *noAnswer
				if errors.As(st.unchecked, &gone) {
					down = st.unchecked
				}
			}
			st.done = ok
		}
		out = append(out, st)
	}
	return out
}

// allDown is why no sign-in could be checked, when that is one reason for
// all of them: the box does not answer. It is nil when any was checked.
func allDown(states []signInState) error {
	var gone *noAnswer
	for _, st := range states {
		if st.unchecked == nil || !errors.As(st.unchecked, &gone) || st.unchecked.Error() != states[0].unchecked.Error() {
			return nil
		}
	}
	if len(states) == 0 {
		return nil
	}
	return states[0].unchecked
}

// loginResult is how the sign-in step ended.
type loginResult struct {
	signed, skipped, failed []string
	unchecked               []string // could not be checked: neither signed in nor known not to be
	interrupted             string   // the sign-in Ctrl-C stopped
	err                     error    // the step could not start
}

// left is what is still to do: skipped, then not finished, then not checked.
func (r loginResult) left() []string {
	return append(append(append([]string(nil), r.skipped...), r.failed...), r.unchecked...)
}

// interruptedExit is a vendor's sign-in ended by Ctrl-C: the wrapper below
// exits 130 then, whatever the vendor's CLI itself exits with.
func interruptedExit(ctx context.Context, err error) bool {
	var ee *exec.ExitError
	return ctx.Err() != nil || errors.As(err, &ee) && ee.ExitCode() == exitInterrupted
}

// onCtrlC makes a remote command exit 130 when the terminal's Ctrl-C reaches
// it. Over ssh -t the keystroke goes to the box, not to keelbox; the shell
// runs the trap once the vendor's CLI has exited, so its own exit code
// (tailscale's 1, codex's 255) does not hide the user's choice.
func onCtrlC(cmd string) string { return "trap 'exit 130' INT; " + cmd }

// signInAll walks through every sign-in that is not done (or only the one
// named), running each vendor's own login on the user's terminal. For each
// the user chooses: sign in now, skip it for later, or say it is done and
// have it checked again. keelbox never sees, stores or relays the links,
// codes or tokens involved. Ctrl-C ends the whole step.
func signInAll(ctx context.Context, cfg *config.Config, o options, only string, p *prompt.Prompter, stdout, stderr io.Writer) loginResult {
	var res loginResult
	// Only the sign-in asked for is checked, with only its account.
	states := signInStates(ctx, cfg, o, only, stderr)
	if only != "" && len(states) == 0 {
		res.err = noSuchSignIn(cfg, o, only)
		return res
	}
	sty := p.Style()
	v := signInView{st: sty, verbose: o.verbose}
	// A box that does not answer is said once, not once a sign-in. keelbox
	// login ends there; keelbox up leaves the sign-ins for later.
	if down := allDown(states); down != nil && len(states) > 1 {
		for _, st := range states {
			res.unchecked = append(res.unchecked, st.name)
		}
		if !o.inUp {
			res.err = down
			return res
		}
		text := fmt.Sprintf("not checked: %v. Then: keelbox login %s", down, cfg.Box)
		if sty.Live {
			sayAsIs(stdout, sty, sty.Bang(), "Sign-ins "+text)
		} else {
			fmt.Fprintf(stdout, "%-12s %s\n", "sign-ins", text)
		}
		return res
	}
	signedIn := func(name string) { v.line(stdout, name, sty.Tick(), signInTitle(name)+" signed in") }
	// A sign-in that could not be checked is said to be so, with why and
	// the command that tries again; the others go on.
	notChecked := func(name string, why error) {
		v.line(stdout, name, sty.Bang(), fmt.Sprintf("%s not checked: %v. Then: keelbox login %s %s", signInTitle(name), why, cfg.Box, name))
		res.unchecked = append(res.unchecked, name)
	}
	// --api names the sign-in to run, so it runs where already signed in too:
	// that is how an account moves from a plan to API billing.
	again := o.force || o.api
	var todo []string
	for _, st := range states {
		if st.unchecked != nil {
			notChecked(st.name, st.unchecked)
			continue
		}
		if st.done && !again {
			if !sty.Live {
				signedIn(st.name) // a styled terminal lists them at the end
			}
			continue
		}
		todo = append(todo, signInTitle(st.name))
	}
	if len(todo) > 0 && sty.Live {
		fmt.Fprintln(stdout, "  "+sty.Dim(ui.Hang(fmt.Sprintf("Next, %s on %s: %s. Each runs the vendor's own sign-in on your terminal; keelbox never sees the credentials. Ctrl-C stops signing in.",
			plural(len(todo), "sign-in", "sign-ins"), cfg.Box, strings.Join(todo, ", ")), sty.Cols()-2, 2)))
	} else if len(todo) > 0 {
		fmt.Fprintf(stdout, "\nNext, %s on %s: %s. Each runs the vendor's own sign-in on your terminal; keelbox never\nsees the credentials. Ctrl-C stops signing in.\n",
			plural(len(todo), "sign-in", "sign-ins"), cfg.Box, strings.Join(todo, ", "))
	}
	joined := false
	for _, st := range states {
		if st.unchecked != nil || st.done && !again {
			continue
		}
		// GitHub's menu is also shown when gh is named: its first choice, a
		// token for some repositories, is what keelbox steers towards.
		ask := only == "" && !o.force || st.name == "gh"
		useAPI := o.api && st.name != "gh"
		for {
			if ask {
				fmt.Fprintln(stdout)
				question := signInTitle(st.name) + ": " + signInPurpose(cfg, st.name)
				if sty.Live {
					// The purpose as a hint, the question short.
					v.hint(stdout, capitalFirst(signInPurpose(cfg, st.name))+".")
					question = signInTitle(st.name) + " sign-in"
				}
				now := prompt.Choice{Key: "now", Label: "Sign in now"}
				if st.name == "gh" {
					now = prompt.Choice{Key: "now", Label: "Sign in with GitHub in a browser", Detail: "every repository your account can reach"}
				}
				choices := []prompt.Choice{
					now,
					{Key: "skip", Label: "Skip for now", Detail: "later: keelbox login " + cfg.Box + " " + st.name},
					{Key: "done", Label: "I have signed in", Detail: "check again"},
				}
				if st.api.login != "" {
					api := prompt.Choice{Key: "api", Label: st.api.label, Detail: st.api.detail}
					if st.api.first {
						choices = append([]prompt.Choice{api}, choices...)
					} else {
						choices = append(choices, api)
					}
				}
				i, err := p.Choose(question, choices, 0)
				key := ""
				if err == nil {
					key = choices[i].Key
				}
				switch {
				case errors.Is(err, prompt.ErrInterrupted) || ctx.Err() != nil:
					res.interrupted = st.name
					return res
				case err != nil:
					res.err = err
					return res
				case key == "skip":
					res.skipped = append(res.skipped, st.name)
				case key == "done":
					ok, cerr := st.sh.succeeds(ctx, st.status)
					if cerr != nil {
						fmt.Fprintf(stdout, "  %s could not be checked on %s: %v\n", signInTitle(st.name), cfg.Box, st.sh.why(ctx, cfg, cerr))
						continue
					}
					if ok {
						signedIn(st.name)
						res.signed = append(res.signed, st.name)
						break
					}
					fmt.Fprintf(stdout, "  %s still reports not signed in on %s.\n", signInTitle(st.name), cfg.Box)
					continue
				}
				if key != "now" && key != "api" {
					break
				}
				useAPI = key == "api"
			}
			// What runs, how messages name it, and what to say first. A
			// sign-in with a line of its own to say has no link to open.
			login, shown, hints := st.login, st.login, []string{signInLinkHint}
			switch {
			case useAPI && st.api.hint != "":
				login, shown, hints = st.api.login, st.api.shown, []string{st.api.hint}
			case useAPI:
				login, shown = st.api.login, st.api.shown
			case st.name == config.AgentCodex:
				hints = append(hints, codexCodeHint)
			case st.name == "gh":
				hints = []string{ghCodeHint}
			}
			if sty.Live {
				for _, h := range hints {
					v.hint(stdout, h)
				}
				v.open(stdout, signInTitle(st.name), fmt.Sprintf("%s, as %s on %s", shown, st.sh.user, cfg.Box))
			} else {
				fmt.Fprintf(stdout, "%-12s running `%s` on %s as %s.\n", st.name, shown, cfg.Box, st.sh.user)
				for _, h := range hints {
					fmt.Fprintf(stdout, "%-12s %s\n", "", ui.Hang(h, 87, 13))
				}
			}
			err := st.sh.interactive(ctx, onCtrlC(login))
			v.close(stdout)
			if err != nil {
				if interruptedExit(ctx, err) {
					res.interrupted = st.name
					return res
				}
				if !sty.Live { // the list at the end says so
					notef(stderr, "the %s sign-in did not finish; try it again later: keelbox login %s %s", signInTitle(st.name), cfg.Box, st.name)
				}
				res.failed = append(res.failed, st.name)
				break
			}
			if st.after != "" {
				if err := st.sh.interactive(ctx, st.after); err != nil {
					notef(stderr, "%s did not finish; run it on the box as %s", st.after, st.sh.user)
				}
			}
			ok, cerr := st.sh.succeeds(ctx, st.status)
			if cerr != nil {
				// The sign-in ran; whether it took is not known.
				notChecked(st.name, st.sh.why(ctx, cfg, cerr))
				break
			}
			if !ok {
				if !sty.Live {
					notef(stderr, "%s still reports not signed in; try again: keelbox login %s %s --force", signInTitle(st.name), cfg.Box, st.name)
				}
				res.failed = append(res.failed, st.name)
				break
			}
			signedIn(st.name)
			res.signed = append(res.signed, st.name)
			if st.name == "tailscale" {
				joined = true
				keyExpiryStep(ctx, cfg, st.sh, p, stdout)
			}
			break
		}
	}
	if cfg.RemoteControl() && (only == "" || only == config.AgentClaudeCode) {
		var agent *shell
		var err error
		for _, st := range states {
			if !st.admin && st.sh != nil {
				agent = st.sh
			}
		}
		if agent == nil {
			agent, err = boxShell(ctx, cfg, o, cfg.Users.Agent.Name, stdout, stderr)
		}
		switch {
		case err != nil:
			// No shell as the agent: said, and left for later like the rest.
			v.line(stdout, "remote", sty.Bang(), fmt.Sprintf("Claude Code Remote Control not checked: %v. Then: keelbox login %s claude-code", err, cfg.Box))
			if !contains(res.left(), config.AgentClaudeCode) {
				res.failed = append(res.failed, "remote-control")
			}
		case remoteControlLogin(ctx, cfg, agent, p, v, stdout, stderr):
			// Takes it off up's list of what is left.
			res.signed = append(res.signed, "remote-control")
		case !contains(res.left(), config.AgentClaudeCode):
			res.failed = append(res.failed, "remote-control")
		}
	}
	if sty.Live && only == "" {
		v.summary(stdout, cfg, states, res)
	}
	if joined && cfg.PublicSSHMode() != config.PublicSSHOpen && !o.inUp {
		fmt.Fprintf(stdout, "\nThe box is on the tailnet. keelbox apply %s, run from a machine on the same tailnet, closes public SSH.\n", cfg.Box)
	}
	return res
}

// noSuchSignIn is the error for a sign-in the box's config leaves out. One
// keelbox can add ends with the command that adds it.
func noSuchSignIn(cfg *config.Config, o options, only string) error {
	ref := boxRef(cfg, o.configFile)
	switch only {
	case config.AgentClaudeCode, config.AgentCodex:
		api := ""
		if o.api {
			api = " --api"
		}
		return fmt.Errorf("%s has no %s sign-in: its config does not list %s under agents. Add it, which installs it and then signs in: keelbox add %s %s%s",
			cfg.Box, only, only, ref, only, api)
	case "gh":
		return fmt.Errorf("%s has no gh sign-in: its config has runtime.gh off. Add it, which installs it and then signs in: keelbox add %s gh", cfg.Box, ref)
	case "tailscale":
		if cfg.Tailscale() {
			return fmt.Errorf("%s has no tailscale sign-in: apply joins the tailnet with the key in access.tailscale.auth_key_file. Its sign-ins: %s", cfg.Box, signInNames(cfg))
		}
		path := o.configFile
		if path == "" {
			path = boxConfigPath(cfg.Box)
		}
		return fmt.Errorf("%s has no tailscale sign-in: its config has Tailscale off. Set `enabled: true` under access.tailscale in %s. Apply it, then run keelbox login %s tailscale again: keelbox apply %s",
			cfg.Box, tildePath(path), ref, ref)
	}
	return fmt.Errorf("%s has no %s sign-in; it has: %s", cfg.Box, only, signInNames(cfg))
}

// apiSignInRefusal is why --api cannot run as given, with the command that
// can, or "" when it can. A sign-in the box does not have is not refused
// here: the sign-in step says how to add it, which is what is missing, and
// not that it has no API route.
func apiSignInRefusal(cfg *config.Config, only, ref string) string {
	var with []string
	has := only == ""
	for _, si := range signIns(cfg) {
		has = has || si.name == only
		if si.api.login == "" || si.name == "gh" { // gh's token is offered in the menu, not by --api
			continue
		}
		if si.name == only {
			return ""
		}
		with = append(with, si.name)
	}
	switch {
	case !has:
		return ""
	case len(with) == 0:
		return fmt.Sprintf("--api is for the Claude Code and Codex sign-ins, and %s has neither agent; its sign-ins: %s", cfg.Box, signInNames(cfg))
	case only == "":
		return fmt.Sprintf("--api takes the sign-in it is for: keelbox login %s %s --api", ref, with[len(with)-1])
	}
	return fmt.Sprintf("the %s sign-in has no API route; --api is for %s: keelbox login %s %s --api", signInTitle(only), strings.Join(with, " and "), ref, with[len(with)-1])
}

// cmdLogin is keelbox login [box] [sign-in].
func cmdLogin(ctx context.Context, cfg *config.Config, o options, only string, stdout, stderr io.Writer) int {
	if !prompt.IsTerminal(os.Stdin) {
		failf(stderr, "login needs a terminal: each sign-in asks you to open a link and paste a code")
		return 2
	}
	ref := boxRef(cfg, o.configFile)
	if o.api {
		if msg := apiSignInRefusal(cfg, only, ref); msg != "" {
			failf(stderr, "%s", msg)
			return 2
		}
	}
	res := signInAll(ctx, cfg, o, only, terminalPrompt(ctx, stdout, o.style), stdout, stderr)
	noteSignedIn(cfg, res.signed)
	// What login did before a Ctrl-C it says itself; one at a question
	// before any sign-in changed nothing, and Main says that.
	stopSaid.Store(res.interrupted != "" || len(res.signed) > 0)
	switch {
	case res.interrupted != "":
		failf(stderr, "stopped at the %s sign-in. Continue: keelbox login %s\n", signInTitle(res.interrupted), ref)
		return exitInterrupted
	case res.err != nil:
		fail(stderr, res.err)
		return 1
	}
	for _, n := range res.skipped {
		if !o.style.Live || o.signIn != "" { // a styled terminal listed them
			fmt.Fprintf(stdout, "%-12s skipped; later: keelbox login %s %s\n", n, ref, n)
		}
	}
	if len(res.failed) > 0 || len(res.unchecked) > 0 {
		return 1
	}
	if cfg.Tmux() && len(res.skipped) == 0 {
		fmt.Fprintf(stdout, "\nStart work: keelbox attach %s (or from a phone: ssh %s@%s, then tmux new -A -s main)\n", ref, cfg.Users.Agent.Name, cfg.Access.Tailscale.Hostname)
	}
	return 0
}

// noteSignedIn takes sign-ins done now off the list keelbox up left.
func noteSignedIn(cfg *config.Config, signed []string) {
	st, ok := loadUpState(cfg)
	if !ok || len(st.Left) == 0 || len(signed) == 0 {
		return
	}
	var left []string
	for _, n := range st.Left {
		if !contains(signed, n) {
			left = append(left, n)
		}
	}
	st.Left = left
	_ = saveUpState(cfg.Box, st)
}

var sessionNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// cmdAttach opens or creates a tmux session as the agent.
func cmdAttach(ctx context.Context, cfg *config.Config, o options, stdout, stderr io.Writer) int {
	name := o.session
	if name == "" {
		name = "main"
	}
	if !sessionNameRe.MatchString(name) {
		failf(stderr, "session name %q: use letters, digits, '.', '_' and '-' (tmux treats ':' as a separator)\n", name)
		return 2
	}
	sh, err := boxShell(ctx, cfg, o, cfg.Users.Agent.Name, stdout, stderr)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	again := attachAgain(boxRef(cfg, o.configFile), o.session)
	open := func() (bool, error) { return sh.session(ctx, attachCommand(name)) }
	var ended error
	if sh.ssh != nil && prompt.IsTerminal(os.Stdin) && prompt.IsTerminal(os.Stderr) {
		// A session that is opened again can give a silent connection up
		// sooner: a wrong guess costs a redraw, not the session.
		quick := *sh.ssh
		quick.LostAfter = attachLostAfter
		kept := *sh
		kept.ssh = &quick
		sh = &kept
		at := addr{sh.ssh.Host, sh.ssh.Port}
		ended = keepSession(ctx, open, func() error { return waitForSSH(ctx, at) }, func(line string) { fmt.Fprintln(stderr, "keelbox: "+line) }, cfg.Box)
	} else {
		_, ended = open()
	}
	if errors.Is(ended, errStoppedWaiting) {
		fail(stderr, fmt.Errorf("stopped waiting for %s. A session left open there keeps running. Open it again: %s", cfg.Box, again))
		return 130
	}
	code := sessionExit(ctx, ended, sh, cfg.Box, again, stderr)
	if code == 0 && stdoutIsTerminal(stdout) {
		fmt.Fprintln(stdout, "keelbox: "+afterAttach(cfg.Box, again))
	}
	return code
}

// errStoppedWaiting is Ctrl-C while keelbox waited for a box to answer.
var errStoppedWaiting = errors.New("stopped waiting")

// How keelbox attach comes back after a lost connection; tests shorten
// the wait.
var (
	// attachLostAfter is how long keelbox attach lets the box go without
	// an answer before it takes the connection for lost.
	attachLostAfter = 10 * time.Second
	// reconnectEvery is the pause between two looks at the box's SSH port.
	reconnectEvery = 3 * time.Second
	// shortSession is a session too short to have been of use: reopened
	// sessions that each end sooner than this, shortSessions times in a
	// row, are a box that takes the connection and drops it, and keelbox
	// stops reopening.
	shortSession  = 10 * time.Second
	shortSessions = 3
)

// keepSession runs a session until it ends by its own doing: the user
// detached, its shell exited, or ssh could not open it. A session that was
// open and that ssh then lost (exit code 255) is opened again once the box
// answers: a laptop that slept or changed network is back in the session
// without a command. open runs one session and reports whether it was
// opened; wait returns when the box's SSH port answers, or
// errStoppedWaiting. The error returned is the last session's.
func keepSession(ctx context.Context, open func() (bool, error), wait func() error, say func(string), box string) error {
	short := 0
	for {
		began := time.Now()
		opened, err := open()
		if !opened || !cutOff(err) || ctx.Err() != nil {
			return err
		}
		if time.Since(began) < shortSession {
			short++
		} else {
			short = 0
		}
		if short >= shortSessions {
			return err
		}
		say("lost the connection to " + box + ". A session left open there keeps running. Waiting for " + box + " to answer, then opening it again; Ctrl-C stops.")
		if werr := wait(); werr != nil {
			return werr
		}
		say(box + " answers again.")
	}
}

// waitForSSH returns once the box's SSH port answers, or errStoppedWaiting
// when the user stops it.
func waitForSSH(ctx context.Context, a addr) error {
	for {
		if ctx.Err() != nil {
			return errStoppedWaiting
		}
		if sshAnswers(ctx, a) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errStoppedWaiting
		case <-time.After(reconnectEvery):
		}
	}
}

// attachAgain is the command that opens the session again.
func attachAgain(ref, session string) string {
	again := "keelbox attach " + ref
	if session != "" {
		again += " --session " + session
	}
	return again
}

// afterAttach is the line after a session that ended with exit status 0:
// the user detached, or its last shell exited. keelbox read nothing of the
// session and cannot tell which, so the line says what holds either way,
// and how to come back.
func afterAttach(box, again string) string {
	return box + " keeps running whatever was left on it. Come back: " + again
}

// sessionExit is the exit code of a session on the user's terminal, with a
// line from keelbox when ssh could not start it or lost it (sessionFailure,
// sessionCutOff). again is the command that opens the session again.
func sessionExit(ctx context.Context, err error, sh *shell, box, again string, stderr io.Writer) int {
	return exitCode(sessionCutOff(ctx, sessionFailure(ctx, err, sh.ssh), err, sh.ssh, box, again), stderr)
}

// sessionCutOff words the end of a session that ssh ended with its own exit
// code. ssh said why on the terminal, and where the session was full
// screen that line went with the screen (terminalRestore), so keelbox's
// line has to stand without it. keelbox cannot tell a connection that was
// lost from one that was never made, nor from a shell that itself exited
// with 255: the line holds for all three, and says that work in a session
// on the box is not lost with the connection.
// failure is what sessionFailure made of err.
func sessionCutOff(ctx context.Context, failure, err error, s *provider.SSH, box, again string) error {
	if s == nil || ctx.Err() != nil || !cutOff(err) {
		return failure
	}
	const kept = "A session left open there keeps running"
	var off *noAnswer
	switch {
	case errors.As(failure, &off) && strings.Contains(failure.Error(), "does not answer"):
		online := "this computer is online"
		if a, perr := netip.ParseAddr(s.Host); perr == nil && tailnet4.Contains(a) {
			online += " and connected to Tailscale"
		}
		return &noAnswer{fmt.Sprintf("%s does not answer at %s on port %d: check %s and that %s is on. %s. Then open it again: %s", box, s.Host, firstPort(s.Port), online, box, kept, again)}
	case failure == err:
		return fmt.Errorf("the session on %s ended with exit code 255, which is ssh's for a connection that failed. %s. Open it again: %s", box, kept, again)
	}
	return failure
}

// attachCommand opens the session, or creates it. What to do first is the
// box's to say, in whatever way the session came to be: its status line,
// and the welcome in a new session's first pane (modules.SessionWelcome).
// ssh hands the box the laptop's TERM. When the box has no terminfo entry
// for it (Ghostty's xterm-ghostty on Ubuntu 24.04, say), tmux stops with
// "missing or unsuitable terminal", so the session gets xterm-256color,
// which those terminals also speak. infocmp is in ncurses-bin, which every
// Ubuntu has.
func attachCommand(session string) string {
	return "infocmp >/dev/null 2>&1 || export TERM=xterm-256color; tmux new-session -A -s " + session
}

// cmdShell opens a login shell as the agent, or the admin with --admin.
func cmdShell(ctx context.Context, cfg *config.Config, o options, stdout, stderr io.Writer) int {
	user := cfg.Users.Agent.Name
	if o.admin {
		user = cfg.Users.Admin.Name
	}
	if o.admin && underAgent() && !stdinIsTerminal() {
		// Root on the box is the owner's to use: an agent here could pipe
		// commands to an admin shell as easily as pass one after --.
		failf(stderr, "ssh --admin gives root on %s, so it needs the owner at a terminal; ask them to run it: keelbox ssh %s --admin", cfg.Box, boxRef(cfg, o.configFile))
		return 2
	}
	if o.hasRemote {
		return runOnBox(ctx, cfg, o, user, stdout, stderr)
	}
	sh, err := boxShell(ctx, cfg, o, user, stdout, stderr)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	again := "keelbox ssh " + boxRef(cfg, o.configFile)
	if o.admin {
		again += " --admin"
	}
	return sessionExit(ctx, sh.interactive(ctx, ""), sh, cfg.Box, again, stderr)
}

const sessionFormat = "#{session_name}\t#{session_windows}\t#{session_created}\t#{session_attached}"

// cmdSessions lists the agent's tmux sessions.
func cmdSessions(ctx context.Context, cfg *config.Config, o options, stdout, stderr io.Writer) int {
	sh, err := boxShell(ctx, cfg, o, cfg.Users.Agent.Name, stdout, stderr)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	out, err := sh.output(ctx, "tmux list-sessions -F '"+sessionFormat+"' 2>/dev/null || true")
	if err != nil {
		fail(stderr, sshFailure(ctx, err, cfg, sh.ssh, o, ""))
		return 1
	}
	sessions := parseSessions(out, time.Now())
	if o.json {
		return sessionsJSON(sessions, stdout)
	}
	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "No tmux sessions. Start one: keelbox attach")
	} else {
		var rows [][]string
		for _, s := range sessions {
			state := "detached"
			if s.attached != "" {
				state = s.attached
			}
			rows = append(rows, []string{s.name, strconv.Itoa(s.windows), s.age, state})
		}
		fmt.Fprint(stdout, o.style.Table([]string{"SESSION", "WINDOWS", "AGE", "STATE"}, rows))
	}
	if cfg.RemoteControl() {
		fmt.Fprintf(stdout, "\nClaude Code Remote Control: %s\n", remoteControlLine(ctx, cfg, sh))
	}
	return 0
}

type session struct {
	name     string
	windows  int
	age      string
	attached string
}

func parseSessions(out string, now time.Time) []session {
	var list []session
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 4 {
			continue
		}
		w, _ := strconv.Atoi(f[1])
		created, err := strconv.ParseInt(f[2], 10, 64)
		age := "?"
		if err == nil {
			age = humanAge(now.Sub(time.Unix(created, 0)))
		}
		att := ""
		if n, _ := strconv.Atoi(f[3]); n > 0 {
			att = "attached"
		}
		// The agent's own shell printed this: its names are plain text here.
		list = append(list, session{name: plain.Line(f[0]), windows: w, age: age, attached: att})
	}
	return list
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// errNoAgentKey explains why agent commands cannot log in.
var errNoAgentKey = errors.New("users.agent has no SSH key, so keelbox cannot log in as the agent; add ssh_authorized_key_files under users.agent and apply")

// signInView draws the sign-in step on a styled terminal: keelbox's lines
// with marks, each vendor's own sign-in between two rules, so what the
// vendor's CLI prints is set apart from what keelbox says, and a list of
// where each sign-in stands at the end. In plain text it keeps the
// one-line-per-sign-in form.
type signInView struct {
	st      ui.Style
	verbose bool
}

func (v signInView) line(out io.Writer, name, mark, text string) {
	if !v.st.Live {
		fmt.Fprintf(out, "%-12s %s\n", name, strings.TrimPrefix(text, signInTitle(name)+" "))
		return
	}
	say(out, v.st, mark, text)
}

func (v signInView) hint(out io.Writer, text string) {
	fmt.Fprintln(out, "  "+v.st.Dim(ui.Hang(text, v.st.Cols()-2, 2)))
}

// open starts a vendor's own output; the command it runs shows only with
// --verbose.
func (v signInView) open(out io.Writer, title, cmd string) {
	if !v.st.Live {
		return
	}
	head := v.st.Faint("──") + " " + v.st.Bold(title) + " " + v.st.Dim("(its own sign-in; keelbox does not read it)") + " "
	fmt.Fprintln(out, head+v.st.Faint(strings.Repeat("─", max(0, min(v.st.Cols(), ui.MaxWidth)-ui.Width(head)))))
	if v.verbose {
		fmt.Fprintln(out, v.st.Dim("$ "+cmd))
	}
}

func (v signInView) close(out io.Writer) {
	if !v.st.Live {
		return
	}
	fmt.Fprintln(out, v.st.Faint(strings.Repeat("─", min(v.st.Cols(), ui.MaxWidth))))
}

// summary lists where each sign-in stands.
func (v signInView) summary(out io.Writer, cfg *config.Config, states []signInState, res loginResult) {
	var lines []ui.Line
	for _, st := range states {
		t := signInTitle(st.name)
		later := v.st.Cmd("keelbox login " + cfg.Box + " " + st.name)
		switch {
		case contains(res.unchecked, st.name):
			lines = append(lines, ui.Line{Mark: v.st.Bang(), Key: t, Text: "not checked; try again: " + later})
		case contains(res.failed, st.name):
			lines = append(lines, ui.Line{Mark: v.st.Cross(), Key: t, Text: "did not finish; try again: " + later})
		case contains(res.skipped, st.name):
			lines = append(lines, ui.Line{Mark: v.st.Dim("○"), Key: t, Text: "skipped; later: " + later})
		case st.done || contains(res.signed, st.name):
			lines = append(lines, ui.Line{Mark: v.st.Tick(), Key: t, Text: "signed in"})
		}
	}
	if contains(res.failed, "remote-control") {
		lines = append(lines, ui.Line{Mark: v.st.Bang(), Key: "Remote Control", Text: "not running yet; later: " + v.st.Cmd("keelbox login "+cfg.Box+" claude-code")})
	}
	if len(lines) > 0 {
		fmt.Fprint(out, "\n"+v.st.Card(ui.Card{Title: "Sign-ins on " + cfg.Box, Lines: lines}))
	}
}

func capitalFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// keyExpiry is true, by its exit code, while the box's Tailscale key has
// an expiry date. --peers=false leaves out the other machines, whose keys
// carry their own.
const keyExpiry = `tailscale status --json --peers=false | grep -q '"KeyExpiry"'`

// keyExpiryStep follows the box joining the tailnet: its key expires in
// 180 days or so, and the box then leaves the tailnet, which may be the
// only way in. It shows where to turn expiry off, waits on a terminal, and
// checks, by exit code only. Doctor warns before the date either way.
func keyExpiryStep(ctx context.Context, cfg *config.Config, sh *shell, p *prompt.Prompter, stdout io.Writer) {
	if on, err := sh.succeeds(ctx, keyExpiry); err != nil || !on {
		return
	}
	st := p.Style()
	text := fmt.Sprintf("%s's Tailscale key expires in about 180 days, and the box then leaves the tailnet until someone signs it in again. "+
		"Turn key expiry off for it: %s, the box, then Disable key expiry. Doctor warns 30 days before it expires.", cfg.Box, modules.TailscaleMachines)
	fmt.Fprintln(stdout)
	sayAsIs(stdout, st, st.Bang(), text)
	if !p.Interactive() {
		return
	}
	if err := p.Wait("Press Enter once it is off, or to leave it on"); err != nil {
		return
	}
	if on, err := sh.succeeds(ctx, keyExpiry); err == nil && !on {
		say(stdout, st, st.Tick(), "key expiry is off for "+cfg.Box)
		return
	}
	say(stdout, st, st.Faint(st.Bullet()), "key expiry is still on for "+cfg.Box+"; doctor warns 30 days before it expires")
}
