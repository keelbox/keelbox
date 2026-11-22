package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/ui"
)

// The end of keelbox up: a headline that says the set-up is finished, two
// sentences on what the box now is, a card with the ways to start working,
// and under it what is left to do and how to check the box.

// readyKeyWidth is the key column of the card and of the rows under it:
// "Phone over SSH".
const readyKeyWidth = 14

// readyMinValue is the narrowest the values beside the keys may get: the
// width of the guide's URL, which no line breaks. A terminal with less
// room gets the keys over their values.
const readyMinValue = len(docsURL + "/guides/sessions")

// ready prints the end of keelbox up for a box that is set up.
func (w *wizard) ready(cfg *config.Config, st upState) {
	took := ""
	if w.progressed {
		took = "in " + time.Since(w.start).Round(time.Second).String()
	}
	_, reached := loadRecord(cfg)
	fmt.Fprint(w.out, readyScreen(w.st, cfg, st, reached, took))
}

// readyScreen is what ready prints. reached says this computer has applied
// to the box over the tailnet (the record reach.go keeps), which is what
// closes public SSH; took is the time the run took, or empty.
func readyScreen(ss ui.Style, cfg *config.Config, st upState, reached bool, took string) string {
	card := ss.Card(ui.Card{Title: "Start working", Lines: startRows(ss, cfg, st), KeyWidth: readyKeyWidth, MinValue: readyMinValue})
	// The text around the card keeps to its columns: width is the card's on
	// a terminal that draws one, and cols where a line of text ends (0 is
	// no wrapping, off a terminal).
	width, lay := 0, readyLayout{}
	switch {
	case ss.Live && ss.UTF8:
		width = ui.Width(card[:strings.IndexByte(card, '\n')])
		lay.cols = width - 2
	case ss.Width > 0:
		lay.cols = ss.Cols()
	}
	lay.stack = ui.Stacks(lay.cols-2, readyKeyWidth, readyMinValue)
	indent := ""
	if ss.Live {
		indent = "  "
	}
	summary := agentsSentence(cfg) + " " + reachSentence(ss, cfg, st, reached)
	if lay.cols > 0 {
		summary = strings.Join(ui.Wrap(summary, lay.cols-len(indent)), "\n"+indent)
	}
	var b strings.Builder
	b.WriteString("\n" + readyHeadline(ss, cfg.Box, took, width) + "\n\n")
	b.WriteString(indent + summary + "\n\n")
	b.WriteString(card + "\n")
	for i, item := range leftItems(ss, cfg, st) {
		key := ""
		if i == 0 {
			key = ss.Heading("Left to do") // in brass: the row that is missed
		}
		b.WriteString(lay.item(key, item))
	}
	for _, r := range checkRows(ss, cfg) {
		b.WriteString(lay.row(ss.Dim(r[0]), r[1], r[2]))
	}
	return b.String()
}

// readyLayout is how the rows under the card are laid out, in the card's
// own columns.
type readyLayout struct {
	cols  int  // where a line of text ends; 0 is no wrapping
	stack bool // a narrow terminal: each key over its value, as in the card
}

// start is how a row begins: the key in its column, or over the value on
// a narrow terminal.
func (l readyLayout) start(key string) string {
	switch {
	case !l.stack:
		return "  " + ui.Pad(key, readyKeyWidth) + "  "
	case key == "":
		return strings.Repeat(" ", l.at())
	}
	return "  " + key + "\n" + strings.Repeat(" ", l.at())
}

// at is the column the values start in.
func (l readyLayout) at() int {
	if l.stack {
		return 2 + ui.StackIndent
	}
	return 2 + readyKeyWidth + 2
}

// hang wraps s from the value column, with the lines after the first
// indented by more.
func (l readyLayout) hang(s string, more int) string {
	if l.cols == 0 {
		return s
	}
	lines := ui.Wrap(s, l.cols-l.at())
	if more > 0 && len(lines) > 1 {
		lines = append(lines[:1], ui.Wrap(strings.Join(lines[1:], " "), l.cols-l.at()-more)...)
	}
	return strings.Join(lines, "\n"+strings.Repeat(" ", l.at()+more))
}

// item is one thing left to do, on a line of its own. What does not fit
// goes under it, indented, so that the next item stands apart.
func (l readyLayout) item(key, text string) string {
	return l.start(key) + l.hang(text, 2) + "\n"
}

// row is a key, a command and a note: after the command when that fits on
// the line, under it otherwise.
func (l readyLayout) row(key, command, note string) string {
	const gap = "        "
	if l.cols == 0 || l.at()+ui.Width(command)+len(gap)+ui.Width(note) <= l.cols {
		return l.start(key) + command + gap + note + "\n"
	}
	return l.start(key) + l.hang(command, 0) + "\n" + strings.Repeat(" ", l.at()) + l.hang(note, 0) + "\n"
}

// readyHeadline says the set-up is finished, with the time it took at the
// card's right edge. Off a terminal it is one plain sentence.
func readyHeadline(ss ui.Style, box, took string, width int) string {
	if !ss.Live {
		if took != "" {
			took = " " + took
		}
		return ss.Bold(box + " is set up" + took + ".")
	}
	head := "  " + ss.Tick() + " " + ss.Bold(box+" is set up")
	if took == "" {
		return head
	}
	gap := max(width-ui.Width(head)-ui.Width(took), 2)
	if ui.Width(head)+gap+ui.Width(took) > ss.Cols() {
		return head + "\n    " + ss.Dim(took)
	}
	return head + strings.Repeat(" ", gap) + ss.Dim(took)
}

// agentsSentence says which agent CLIs the box has and what account runs
// them. The last apply stops keelbox up when one fails to install, so at
// the end of a run the config's list is what is there.
func agentsSentence(cfg *config.Config) string {
	account := "an account without sudo that can install Ubuntu packages (keelbox install)"
	switch cfg.AgentPermissions() {
	case config.PermissionsManual:
		account = "an account without sudo"
	case config.PermissionsFull:
		account = "an account with sudo"
	}
	var names []string
	for _, a := range cfg.Agents {
		names = append(names, signInTitle(a))
	}
	switch n := len(names); n {
	case 0:
		return "No agent CLI is installed; the agent is " + account + "."
	case 1:
		return names[0] + " is installed and runs as the agent, " + account + "."
	default:
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1] + " are installed and run as the agent, " + account + "."
	}
}

// reachSentence says how the box is reached and where its SSH stands, from
// what decides it (ADR 0021): the config's Tailscale and public_ssh
// choices, whether the Tailscale sign-in is still left, and whether this
// computer has applied to the box over the tailnet. Without that last
// proof it says what closes SSH and how to see whether it has, not that it
// is closed.
func reachSentence(ss ui.Style, cfg *config.Config, st upState, reached bool) string {
	box := cfg.Box
	local := cfg.Target.Provider == config.ProviderLocal
	login, apply := ss.Cmd("keelbox login "+box+" tailscale"), ss.Cmd("keelbox apply "+box)
	lead, it, itLower := "", "The box", "the box"
	if local {
		lead, it, itLower = "The box is this machine. ", "It", "it"
	}
	const open = "SSH stays open to the LAN and the internet"
	lan := cfg.PublicSSHMode() == config.PublicSSHLAN
	joined := !contains(st.Left, "tailscale")
	switch {
	case !cfg.Tailscale() && local:
		return lead + "Without Tailscale its " + open + "."
	case !cfg.Tailscale():
		return "Without Tailscale you reach the box over SSH, which stays open to the LAN and the internet."
	case cfg.PublicSSHMode() == config.PublicSSHOpen && joined:
		return lead + it + " is on your tailnet; " + open + " (host.firewall.public_ssh: open)."
	case cfg.PublicSSHMode() == config.PublicSSHOpen:
		return lead + it + " is not on your tailnet yet; " + open + " (host.firewall.public_ssh: open)."
	case !joined:
		closes := "It closes after " + login + ", then " + apply
		if local {
			closes = "It closes at the next keelbox apply from another machine on the tailnet, after " + login
		}
		if lan {
			closes += "; your LAN keeps SSH"
		}
		return lead + it + " is not on your tailnet yet, so SSH from the internet is still open. " + closes + "."
	case reached && !local && lan:
		return "SSH from the internet is closed and your LAN keeps it; the box is on your tailnet."
	case reached && !local:
		return "SSH from the internet is closed; the box is on your tailnet."
	}
	from := ""
	if local {
		from = " from another computer"
	}
	s := lead + it + " is on your tailnet. SSH from the internet closes once " + apply + " reaches " + itLower + " over the tailnet" + from +
		"; " + ss.Cmd("keelbox doctor "+box) + " says whether it has."
	if lan {
		s += " Your LAN keeps SSH."
	}
	return s
}

// startRows are the card's rows: the ways to start working on the box.
func startRows(ss ui.Style, cfg *config.Config, st upState) []ui.Line {
	box := cfg.Box
	where := "on the box"
	if cfg.Target.Provider == config.ProviderLocal {
		where = "on this machine"
	}
	var rows []ui.Line
	if cfg.Tmux() {
		rows = append(rows, ui.Row("This computer", ss.Cmd("keelbox attach "+box)),
			ui.Row("", "opens the agent's terminal "+where+". Close it and the work carries on; run it again to come back."))
	} else {
		rows = append(rows, ui.Row("This computer", ss.Cmd("keelbox ssh "+box)),
			ui.Row("", "opens a shell as the agent "+where+". sessions.tmux is off, so there is no session to come back to."))
	}
	if remoteControlRuns(cfg, st) {
		rows = append(rows, ui.Blank(), ui.Row("Claude app", "Remote Control: the Code tab in the Claude app, or "+
			ss.Link(ss.Info("claude.ai/code"), "https://claude.ai/code")+". Sessions run on "+box+"."))
	}
	return append(append(rows, ui.Blank()), phoneRows(ss, cfg, st)...)
}

// remoteControlRuns reports whether Claude Code Remote Control is on and
// started: enabled, Claude Code signed in, its first-run questions
// answered. While it waits on either, Left to do says so.
func remoteControlRuns(cfg *config.Config, st upState) bool {
	return cfg.RemoteControl() && !contains(st.Left, config.AgentClaudeCode) && !contains(st.Left, "remote-control")
}

// phoneRows say how a phone's SSH app reaches the box: over the tailnet
// once the box is on it, at its address until then. Either way the
// phone's key goes on the box first, and the guide has the steps.
func phoneRows(ss ui.Style, cfg *config.Config, st upState) []ui.Line {
	guide := ui.Row("", ss.URL(docsURL+"/guides/sessions"))
	if cfg.Tailscale() && !contains(st.Left, "tailscale") {
		return []ui.Line{ui.Row("Phone over SSH", "add your phone's key to the box first:"), guide}
	}
	ssh := ss.Cmd(fmt.Sprintf("ssh %s@%s", cfg.Users.Agent.Name, firstNonEmpty(st.Address, cfg.Target.SSH.Host, cfg.Box)))
	return []ui.Line{ui.Row("Phone over SSH", ssh+" from an SSH app, once your phone's key is on the box:"), guide}
}

// leftItems is what is left to do, one item a line: each sign-in skipped
// or not finished, and the git identity for as long as the config names
// none (not a sign-in, so not in st.Left).
func leftItems(ss ui.Style, cfg *config.Config, st upState) []string {
	var items []string
	for _, name := range st.Left {
		items = append(items, leftToDo(ss, cfg, name))
	}
	if _, _, ok := cfg.AgentGit(); !ok {
		items = append(items, fmt.Sprintf("git identity: none in the config, and without one git on %s refuses to commit. Add users.agent.git: {name: Your Name, email: you@example.com} to %s, then %s",
			cfg.Box, tildePath(boxConfigPath(cfg.Box)), ss.Cmd("keelbox apply "+cfg.Box)))
	}
	return items
}

// checkRows are the rows under the card that are always there: a key, a
// command and a note. The admin shell is left out on this machine, where
// the account that ran keelbox up already has sudo.
func checkRows(ss ui.Style, cfg *config.Config) [][3]string {
	box := cfg.Box
	rows := [][3]string{{"Check the box", ss.Cmd("keelbox doctor " + box), ss.Dim("all boxes: ") + ss.Cmd("keelbox ls")}}
	if home, err := skillHome(); err == nil && agentSkillMissing(home) {
		rows = append(rows, [3]string{"Agents here", ss.Cmd("keelbox agent-skill install"), ss.Dim("so Claude Code and Codex on this computer can use keelbox")})
	}
	if home, err := skillHome(); err == nil && len(localSkills(home)) > 0 {
		rows = append(rows, [3]string{"Your skills", ss.Cmd("keelbox skills " + box), ss.Dim("copy skills from this computer to the agent")})
	}
	if cfg.Target.Provider == config.ProviderLocal {
		return rows
	}
	note := "for anything that needs root"
	if !cfg.AgentSudo() {
		note += "; the agent has no sudo"
	}
	return append(rows, [3]string{"Admin shell", ss.Cmd("keelbox ssh " + box + " --admin"), ss.Dim(note)})
}

// leftToDo is the command that finishes a sign-in left for later.
func leftToDo(ss ui.Style, cfg *config.Config, name string) string {
	box := cfg.Box
	login := func(what string) string { return ss.Cmd("keelbox login " + box + " " + what) }
	switch name {
	case "tailscale":
		if cfg.Target.Provider == config.ProviderLocal {
			return "join the tailnet: " + login("tailscale") + "; public SSH closes at the next keelbox apply from a machine on the tailnet"
		}
		return "join the tailnet: " + login("tailscale") + ", then " + ss.Cmd("keelbox apply "+box) + " (closes public SSH)"
	case "remote-control":
		return "Claude Code Remote Control: " + login("claude-code") + ", and answer y to both of Claude Code's first-run questions"
	case config.AgentClaudeCode:
		if cfg.RemoteControl() {
			return "sign in to Claude Code (Remote Control starts after): " + login("claude-code")
		}
	case "gh":
		// What skipping it means, in the same line (ADR 0064).
		return "sign in to GitHub: " + login("gh") + " (until then the agent's work stays on " + box + ", unless it is pushed another way or copied off)"
	}
	return "sign in to " + signInTitle(name) + ": " + login(name)
}
