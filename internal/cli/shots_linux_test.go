package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/digitalocean"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/ui"
)

// TestShots writes the screens the docs and the website show, in the style of a
// capable terminal, as raw terminal output: KEELBOX_SHOTS=DIR, with
// KEELBOX_SHOTS_WIDTH and KEELBOX_SHOTS_THEME. It checks nothing else.
func TestShots(t *testing.T) {
	dir := os.Getenv("KEELBOX_SHOTS")
	if dir == "" {
		t.Skip("set KEELBOX_SHOTS=DIR to write the screens")
	}
	isolate(t)
	cols, theme := shotsTerminal()
	st := fullStyle(cols, theme == "light")
	save := func(name string, b *bytes.Buffer) {
		// As a terminal shows it: the line discipline turns \n into \r\n.
		raw := strings.ReplaceAll(b.String(), "\n", "\r\n")
		_ = os.WriteFile(filepath.Join(dir, strings.Join([]string{itoa(cols), theme, name}, "-")+".ans"), []byte(raw), 0o600)
	}
	cfg := &config.Config{Box: "devbox"}
	o := options{style: st}

	// A first apply on Hetzner, then the admin check.
	var b bytes.Buffer
	b.WriteString(st.Step(6, 7, "Set up"))
	out, _, done := applyViewModules([]string{"host", "access", "runtime", "sessions", "primer", "logbook", "previews", "backup", "notify", "doctor", "watch", "agent-policy", "agents"}, o, &b, &b)
	_, _ = io.WriteString(out, ownerApply)
	done()
	b.WriteString(st.Section("Access"))
	b.WriteString("  " + st.Tick() + " admin logs in with your key and uses sudo" + st.Sep() + "0 changes\n")
	say(&b, st, st.Bang(), "public SSH stays open: the box is not on the tailnet yet. keelbox up closes it after the Tailscale sign-in.")
	save("owner-apply", &b)

	// The re-apply after sign-in, with Remote Control's first run still to
	// answer: deferred, not failed (ADR 0061).
	b.Reset()
	b.WriteString(st.Step(7, 7, "Sign in") + "\n")
	(&wizard{st: st, out: &b}).again("applying %s again, over the tailnet once it answers there, to close public SSH", "devbox")
	out, errOut, done := applyViewModules([]string{"host", "access", "runtime", "agent-policy", "agents"}, o, &b, &b)
	_, _ = io.WriteString(out, ownerReapply)
	_ = errOut
	done()
	save("owner-reapply", &b)

	// An apply with nothing to change.
	b.Reset()
	out, _, done = applyViewModules([]string{"host", "agents"}, o, &b, &b)
	_, _ = io.WriteString(out, "keelbox apply: devbox (release 2026.11.3, Ubuntu 24.04 amd64)\nhost\n  ok      packages\nagents\n  ok      codex\n\nNo changes. The box matches the config (140 checks, 3s).\n")
	done()
	save("up-to-date", &b)

	// The sign-in step's list.
	b.Reset()
	v := signInView{st: st}
	v.hint(&b, "Open the link it shows in a browser on this computer, sign in, and paste any code it asks for back here.")
	v.hint(&b, codexCodeHint)
	v.open(&b, "Codex", "codex login")
	b.WriteString("Successfully logged in\n")
	v.close(&b)
	v.line(&b, "codex", st.Tick(), "Codex signed in")
	b.WriteString("\n")
	v.hint(&b, "A GitHub account for the agent, for git push and pull requests (one of its own, or a fine-grained token, keeps your other repositories out of reach).")
	b.WriteString(st.Bold("? GitHub sign-in") + "  " + st.Dim("Skip for now") + "\n")
	v.summary(&b, cfg, []signInState{{name: "tailscale", done: true}, {name: config.AgentClaudeCode}, {name: config.AgentCodex}, {name: "gh"}},
		loginResult{signed: []string{config.AgentClaudeCode, config.AgentCodex}, skipped: []string{"gh"}, failed: []string{"remote-control"}})
	save("sign-in", &b)

	// The start screen, ls, help, doctor and status --all.
	register(t, config.Starter{Box: "devbox", Provider: config.ProviderSSH, SSHHost: "203.0.113.7", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}})
	_ = saveUpState("devbox", upState{Via: viaHetzner, Stage: stageReady})
	b.Reset()
	startScreen(&b, st)
	save("start", &b)
	b.Reset()
	cmdList(st, &b, &b)
	save("ls", &b)
	b.Reset()
	b.WriteString(overviewIn(st))
	save("help", &b)
	b.Reset()
	r := ui.NewReport(&b, st, false)
	_, _ = io.WriteString(r, "keelbox doctor: devbox (release 2026.11.3, Ubuntu 24.04 amd64)\n\nhost\n  ok    packages                 installed\n  ok    firewall                 ufw active: 22/tcp from the tailnet\n  warn  disk                     81% of / used\n\nruntime\n  ok    git-identity             commits as the name and email in users.agent.git\n  warn  projects                 2 of 3 in ~/projects hold work that exists only on this box (1 not a git repository, 1 with uncommitted changes). List them: keelbox status devbox; then commit and push as the agent, or copy them off\n\nagents\n  ok    claude-code              2.3.4\n  warn  claude-remote-control    needs you: signed in but not running (activating/auto-restart, result exit-code, 2 restarts). Answer Claude Code's two first-run questions: keelbox login devbox claude-code. Logs, as agent: journalctl --user -u keelbox-claude-remote-control.service\n\nsign-in\n  ok    tailscale                signed in\n  warn  gh                       not signed in: keelbox login devbox gh\n\n5 ok, 4 warnings, 0 failures.\n")
	r.Close()
	save("doctor", &b)
	// keelbox status's projects rows, and what destroy shows before it asks
	// (ADR 0064).
	b.Reset()
	r = ui.NewReport(&b, st, true)
	projects := &snapshot.Projects{Dir: "~/projects", Total: 4, List: []snapshot.Project{
		{Name: "shop"}, {Name: "api", Repo: true, Uncommitted: 3, Unpushed: 2}, {Name: "notes", Repo: true, NoRemote: true}, {Name: "site", Repo: true}}}
	_, _ = io.WriteString(r, "box        devbox (Ubuntu 24.04 amd64)\nkeelbox    v0.1.0, release 2026.11.4\ndoctor     2026-11-10 09:00 (12m ago): 0 failures, 2 warnings\nagents     claude-code 2.3.4, codex 0.166.0\n")
	projectRows(r, projects)
	r.Close()
	save("status-projects", &b)
	b.Reset()
	say(&b, st, st.Faint(st.Bullet()), "checking devbox for work that exists only there")
	b.WriteString(workList(st, "devbox", projects))
	b.WriteString(strings.Join(prompt.QuestionLines(st, st.Ask(), "Delete this work with the server? "+st.Dim("[y/N]"), st.Cols()), "\n") + "\n")
	save("destroy-work", &b)
	// The question at the first connection to a server nobody gave
	// keelbox the host key of.
	b.Reset()
	confirmHostKeys(prompt.New(strings.NewReader("\n"), &b, true).WithStyle(st), &b, "203.0.113.10", provider.ParseHostKeys(offeredHostKeys))
	save("host-key", &b)
	b.Reset()
	now := time.Now()
	printStatusAllStyled(&b, st, []statusAllRow{
		{Box: "devbox", Reach: reachOK, Status: &snapshot.Status{Written: now, Release: "2026.11.3", Doctor: &snapshot.Doctor{Time: now, Warnings: 2}}},
		{Box: "nas", Reach: reachStale, Status: &snapshot.Status{Written: now.Add(-20 * time.Minute), Release: "2026.11.2", Doctor: &snapshot.Doctor{Time: now, Failures: 1}}},
		{Box: "lab", Reach: reachUnreachable, Error: "unreachable: no route to host; keelbox ssh lab"},
	}, now)
	save("status-all", &b)
	// What forget shows for a cloud box whose server may exist, and its
	// question.
	gamma := register(t, config.StarterFor("gamma", "hetzner"))
	_ = saveUpState("gamma", upState{Via: viaHetzner, Stage: stageReady, Address: "203.0.113.9"})
	if gcfg, err := config.Load(gamma); err == nil {
		b.Reset()
		plan := forgetPlanFor("gamma", gcfg)
		b.WriteString(st.Card(ui.Card{Title: "Forget gamma", Lines: plan.rows(st, "gamma")}))
		question := prompt.QuestionLines(st, st.Ask(), "Forget gamma and leave what is billed in the cloud?", st.Cols())
		question = append(question, prompt.MenuLines(st, []prompt.Choice{
			{Label: "Keep gamma", Detail: "nothing changes"},
			{Label: "Forget gamma", Detail: "what is billed stays until you delete it in " + plan.console},
		}, 0, st.Cols())...)
		b.WriteString(strings.Join(question, "\n") + "\n")
		save("forget-cloud", &b)
	}
	// The card keelbox up prints with a cloud-config, and what stopping
	// at its question says.
	_ = os.WriteFile(cloudConfigPath("web"), []byte("#cloud-config\nhostname: web\nusers:\n    - name: admin\n      ssh_authorized_keys:\n        - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... you@laptop\n"), 0o600)
	b.Reset()
	b.WriteString(st.Step(5, 7, "Create the server"))
	_ = (&wizard{st: st, out: &b}).showCloudConfig("web")
	b.WriteString(strings.Join(prompt.QuestionLines(st, st.Dim("-"), "The server's IP address, once it is running", st.Cols()), "\n") + "\n")
	b.WriteString("\n" + st.Trouble(ui.Trouble{Severity: ui.Stopped, Names: []string{"web"},
		Text: "stopped; web is saved and waits for its server. Its cloud-config is in ~/.config/keelbox/boxes/web.cloud-config. Continue: keelbox up web"}))
	save("cloud-config", &b)
	// keelbox ls and the start screen with boxes in every state.
	for box, up := range map[string]upState{
		"beta": {Via: viaSSH, Stage: stageApplied, Address: "203.0.113.8"},
		"zeta": {Via: viaSSH, Stage: stageReady, Address: "203.0.113.10", Left: []string{"codex", "gh"}},
	} {
		starter := config.StarterFor(box, "ssh")
		starter.SSHHost = up.Address
		register(t, starter)
		_ = saveUpState(box, up)
	}
	register(t, config.StarterFor("delta", "hetzner"))
	_ = saveParkMark("delta", parkMark{Since: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)})
	register(t, config.StarterFor("echo", "hetzner"))
	_ = saveDestroyedMark("echo", destroyedMark{At: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)})
	pending := config.StarterFor("web", "ssh")
	_ = saveUpState("web", upState{Via: viaCloudInit, Stage: stageWaiting, Pending: &pending})
	b.Reset()
	cmdList(st, &b, &b)
	save("ls-all", &b)
	b.Reset()
	startScreen(&b, st)
	save("start-all", &b)
	// Errors: a box that does not answer, a sign-in only the user can do
	// with a command longer than the terminal, and a config with a list of
	// problems.
	b.Reset()
	for _, t := range []ui.Trouble{
		{Text: "203.0.113.7 does not answer on port 22; check it is on and reachable from here: ssh agent@203.0.113.7"},
		{Severity: ui.NeedsYou, Note: "Nothing was changed. Your answers so far are kept.",
			Text: "doctl (the DigitalOcean CLI) is not installed or not on PATH; keelbox creates Droplets through it. Install it: https://docs.digitalocean.com/reference/doctl/how-to/install/, then sign in: " +
				`doctl auth login --scopes "` + strings.ReplaceAll(digitalocean.Scopes, ", ", " ") + `". Then: keelbox up devbox`},
		{Text: "~/.config/keelbox/boxes/broken.yaml: 2 problems:\n  - target.provider: \"nowhere\" is not one of local, ssh, hetzner, aws, digitalocean, gcp (keelbox init --provider ssh writes an example)\n" +
			"  - users.admin: at least one SSH key is required: the admin's only login, so a box without one would lock you out\nFix ~/.config/keelbox/boxes/broken.yaml, then: keelbox up broken"},
		{Severity: ui.Stopped, Text: "stopped; nothing was changed. Your answers so far are kept. Continue: keelbox up devbox"},
	} {
		t.Names = []string{"devbox", "gamma", "web", "broken"}
		b.WriteString("\n" + st.Trouble(t))
	}
	save("errors", &b)
	// The end of keelbox up: everything done, the sign-ins skipped, and
	// this machine. Last, because the states come with a HOME of their own.
	for name, r := range readyStates(t) {
		b.Reset()
		b.WriteString(r.render(st))
		save("ready-"+name, &b)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

var ownerApply = `keelbox apply: devbox (release 2026.11.3, Ubuntu 24.04 amd64)
host
` + steps46 + `access
  change  tailscale: install
  change  tailscale-up: join
runtime
  change  docker: install
  change  node: install
sessions
  change  tmux: write
primer
  change  primer: write
logbook
  change  logbook: create
previews
  change  previews: write
backup
  ok      backup
notify
  change  notify: write
doctor
  change  doctor-timer: enable
watch
` + steps13 + `agent-policy
  change  managed-settings: write
  change  hooks: write
  ok      env
agents
  change  claude-code: install
  change  codex: install
  change  gh: install

Applied 106 changes in 1m38s. Recorded in /var/lib/keelbox/logbook.jsonl.
`

var ownerReapply = `keelbox apply: devbox (release 2026.11.3, Ubuntu 24.04 amd64)
host
  ok      packages
access
  ok      tailscale
runtime
  ok      docker
agent-policy
  ok      managed-settings
agents
  ok      claude-code
  ok      codex
  change  gh-auth-helper: write
  defer   claude-remote-control-service: Claude Code Remote Control needs your answers on a terminal once (enable Remote Control, trust ~/projects): keelbox login devbox claude-code

Deferred agents/claude-remote-control-service: Claude Code Remote Control needs your answers on a terminal once (enable Remote Control, trust ~/projects): keelbox login devbox claude-code.
Applied 1 change in 3s. Recorded in /var/lib/keelbox/logbook.jsonl.
`

var steps46 = strings.Repeat("  change  step: x\n", 24) + strings.Repeat("  ok      step\n", 22)
var steps13 = strings.Repeat("  change  watch: x\n", 13)
