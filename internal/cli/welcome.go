package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/term"
	"github.com/keelbox/keelbox/internal/ui"
)

// keelbox welcome prints a few lines for a person who has just landed in a
// tmux session on a box: where they are, how to start an agent, how to
// leave without stopping anything, how to come back, how to get a package
// and where root is. The
// agent's shell runs it once, in a new session's first pane (the "sessions"
// block apply writes to ~/.bashrc), and hands it what the applied config
// says: the box's name, the agent's name, its projects directory and
// whether it has sudo. The rest is read from the box as it is now. It is
// not in help: nobody needs to type it.
//
// It reads a few small local files and nothing else: no network, no sudo,
// no sign-in status (that is doctor's). Without a terminal, or on one it
// cannot lay text out on, it prints nothing and exits 0, so a shell that
// starts it where it does not belong shows nothing.

// welcome is what the lines are built from.
type welcome struct {
	Box     string
	Session string // the tmux session; "" and main need no --session to come back
	User    string // the account the shell runs as
	Agent   bool   // that account is the box's agent
	Sudo    bool   // that account has sudo
	// Install is the box's users.agent.permissions as the agent's
	// keelbox install reads it, for the agent without sudo: auto or
	// manual. "" for anyone else and on a box that has no mode yet.
	Install string
	OS      string // "Ubuntu 24.04"; "" when the box does not say
	// CLIs are the agent CLIs installed, as typed: claude, codex.
	CLIs []string
	// Projects is the agent's projects directory, as typed: ~/projects.
	Projects string
}

const (
	// welcomeMinCols is the narrowest terminal the welcome is printed on.
	welcomeMinCols = 36
	// welcomeWideCols is where the labels get a column of their own.
	welcomeWideCols = 72
	// welcomeLabel is that column's width: "Start an agent".
	welcomeLabel = 14
)

// welcomeRow is a label and its text. Each part starts a line and is given
// from its fullest wording to its shortest: the first that fits the
// terminal is printed, and the shortest is wrapped when none does. The
// session a box starts at boot is 80 columns wide whatever attaches later,
// so the short wordings are what keeps the welcome to seven lines there.
type welcomeRow struct {
	label string
	parts [][]string
}

// rows are the welcome's lines after the first, in the order a newcomer
// needs them.
func (w welcome) rows(st ui.Style) []welcomeRow {
	one := func(s string) [][]string { return [][]string{{s}} }
	var rows []welcomeRow
	if len(w.CLIs) > 0 {
		var cmds []string
		for _, c := range w.CLIs {
			cmds = append(cmds, st.Cmd(c))
		}
		rows = append(rows, welcomeRow{"Start an agent", one(strings.Join(cmds, "    "))})
	}
	if w.Projects != "" {
		rows = append(rows, welcomeRow{"Your work", one(w.Projects)})
	}
	back := "keelbox attach " + w.Box
	if w.Session != "" && w.Session != "main" && sessionNameRe.MatchString(w.Session) {
		back += " --session " + w.Session
	}
	// A command to type stays on one line.
	cmd := func(s string) string { return st.Cmd(strings.ReplaceAll(s, " ", welcomeTie)) }
	rows = append(rows,
		welcomeRow{"Leave", one(st.Cmd("Ctrl-b") + " then " + st.Cmd("d") + ": everything here keeps running")},
		welcomeRow{"Come back", [][]string{{cmd(back) + "   " + st.Dim("(from your computer)"), cmd(back)}}},
	)
	if w.Sudo {
		return rows
	}
	admin := []string{"from your computer: " + cmd("keelbox ssh "+w.Box+" --admin")}
	install := cmd("keelbox install NAME")
	switch w.Install {
	case config.PermissionsAuto:
		rows = append(rows,
			welcomeRow{"Packages", [][]string{{install + ": an Ubuntu package, without sudo", install}}},
			welcomeRow{"Need root?", [][]string{admin}})
	case config.PermissionsManual:
		requests := cmd("keelbox requests " + w.Box)
		rows = append(rows,
			welcomeRow{"Packages", [][]string{{install + " asks you; answer from your computer: " + requests, install + " asks you: " + requests}}},
			welcomeRow{"Need root?", [][]string{admin}})
	default:
		apply := cmd("keelbox apply " + w.Box)
		rows = append(rows, welcomeRow{"Need root?", [][]string{
			admin,
			{
				"packages for good: host.packages in the box's config, then " + apply,
				"packages for good: host.packages, then " + apply,
				"packages: host.packages, then " + apply,
			},
		}})
	}
	return rows
}

// welcomeTie stands for a space inside a command until the lines are laid
// out, so wrapping breaks beside a command and not within it.
const welcomeTie = "\x00"

// welcomeFit is the first wording that fits n columns, as written (with
// its wider gaps), or the last one wrapped at its words. A command wider
// than the line is wrapped like any other text.
func welcomeFit(alts []string, n int) []string {
	lines := []string{}
	for _, s := range alts {
		if ui.Width(s) <= n {
			lines = []string{s}
			break
		}
	}
	if len(lines) == 0 {
		words := strings.Split(alts[len(alts)-1], " ")
		for i, word := range words {
			if ui.Width(word) > n {
				words[i] = strings.ReplaceAll(word, welcomeTie, " ")
			}
		}
		lines = ui.Wrap(strings.Join(words, " "), n)
	}
	for i := range lines {
		lines[i] = strings.ReplaceAll(lines[i], welcomeTie, " ")
	}
	return lines
}

// head is the first line's parts: the box, who you are here, the system.
func (w welcome) head(st ui.Style) []string {
	who := "you are " + w.User
	switch {
	case w.Agent && w.Sudo:
		who = "you are the agent, an account with sudo"
	case w.Agent:
		who = "you are the agent, an account without sudo"
	}
	parts := []string{st.Bold(w.Box), who}
	if w.OS != "" {
		parts = append(parts, w.OS)
	}
	return parts
}

// render lays the welcome out for a terminal of width columns (0: not
// known, taken as 80). Wide terminals get a column of labels; narrow ones
// get each label and its text on one line, wrapped under it.
func (w welcome) render(st ui.Style, width int) string {
	if width <= 0 {
		width = 80
	}
	cols := width - 1 // a full line never wraps by itself
	fit := welcomeFit
	dot := st.Faint(strings.TrimSpace(ui.Strip(st.Sep())))
	head := w.head(st)
	var out []string
	if width >= welcomeWideCols {
		out = append(out, "  "+strings.Join(fit([]string{strings.Join(head, "  "+dot+"  "), strings.Join(head, " "+dot+" ")}, cols-2), "\n  "))
		indent := 2 + welcomeLabel + 3
		for _, r := range w.rows(st) {
			label := "  " + st.Dim(ui.Pad(r.label, welcomeLabel)) + "   "
			for _, p := range r.parts {
				for _, l := range fit(p, cols-indent) {
					out = append(out, label+l)
					label = strings.Repeat(" ", indent)
				}
			}
		}
		return strings.Join(out, "\n") + "\n"
	}
	out = append(out, "  "+strings.Join(fit([]string{strings.Join(head, " "+dot+" ")}, cols-2), "\n  "))
	for _, r := range w.rows(st) {
		label := st.Dim(r.label + ":")
		if strings.HasSuffix(r.label, "?") {
			label = st.Dim(r.label)
		}
		for i, p := range r.parts {
			lead := "    "
			if i == 0 {
				lead = "  "
				for j := range p {
					p[j] = label + " " + p[j]
				}
			}
			out = append(out, lead+strings.Join(fit(p, cols-4), "\n    "))
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// welcomeFacts reads what the welcome says about the box as it is now;
// tests replace it. Each is a small local file or a PATH lookup.
var welcomeFacts = func() (osName string, clis []string, sudoGroup bool) {
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		env := engine.ParseEnvFile(string(data))
		osName = strings.TrimSpace(env["NAME"] + " " + env["VERSION_ID"])
	}
	for _, bin := range modules.AgentBins() {
		if _, err := exec.LookPath(bin); err == nil {
			clis = append(clis, bin)
		} else if home, err := os.UserHomeDir(); err == nil {
			if st, err := os.Stat(home + "/.local/bin/" + bin); err == nil && !st.IsDir() {
				clis = append(clis, bin)
			}
		}
	}
	if u, err := user.Current(); err == nil {
		if g, err := user.LookupGroup("sudo"); err == nil {
			ids, _ := u.GroupIds()
			sudoGroup = contains(ids, g.Gid)
		}
	}
	return osName, clis, sudoGroup
}

// welcomeInstall is the box's permissions mode as keelbox install reads
// it, from the mode file apply writes for every account to read; "" when
// the box has none. Tests replace it.
var welcomeInstall = welcomeInstallFromFile

func welcomeInstallFromFile() string {
	m, err := installs.LoadMode(installModePath)
	if err != nil {
		return ""
	}
	return m.Mode
}

// welcomeTerminal is the terminal stdout is, and its width; ok is false
// when it is not one, or TERM says it cannot place text. Tests replace it.
var welcomeTerminal = func(stdout io.Writer) (st ui.Style, width int, ok bool) {
	f, isFile := stdout.(*os.File)
	if !isFile || !term.IsTerminal(f) {
		return ui.Style{}, 0, false
	}
	st = ui.Detect(true, os.Getenv)
	// The 16 colours the terminal's own theme defines: the pane may be
	// light or dark, and asking the terminal costs a round trip a shell
	// start-up should not make.
	st.Depth = ui.Depth16
	return st, term.Width(f), os.Getenv("TERM") != "" && os.Getenv("TERM") != "dumb"
}

// cmdWelcome is keelbox welcome.
func cmdWelcome(args []string, stdout, stderr io.Writer) int {
	var w welcome
	var agent, projects string
	fs := flag.NewFlagSet("keelbox welcome", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&w.Box, "box", "", "")
	fs.StringVar(&agent, "agent", "", "")
	fs.StringVar(&projects, "projects", "", "")
	fs.StringVar(&w.Session, "session", "", "")
	fs.BoolVar(&w.Sudo, "sudo", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || w.Box == "" {
		fmt.Fprintln(stderr, "keelbox: welcome is run by the agent's shell in a new tmux session on a box, which gives it --box, --agent, --projects, --session and --sudo (the sessions block in ~/.bashrc). Open a session from your computer: keelbox attach BOX")
		return 2
	}
	st, width, ok := welcomeTerminal(stdout)
	if !ok || (width > 0 && width < welcomeMinCols) {
		return 0
	}
	w.User = currentUser()
	w.Agent = agent != "" && w.User == agent
	if projects != "" {
		w.Projects = "~/" + strings.Trim(projects, "/")
	}
	osName, clis, sudoGroup := welcomeFacts()
	w.OS, w.CLIs = osName, clis
	// Without sudo only when the applied config says so and the account
	// is not in the sudo group either.
	w.Sudo = w.Sudo || sudoGroup || os.Geteuid() == 0
	// keelbox install is the agent's command and no other account's.
	if w.Agent && !w.Sudo {
		w.Install = welcomeInstall()
	}
	fmt.Fprint(stdout, w.render(st, width))
	return 0
}
