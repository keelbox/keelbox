package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/agentlimits"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/previews"
	"github.com/keelbox/keelbox/internal/snapshot"
)

// keelbox on the box, run by the agent account. The agent has no sudo, so
// commands that read the box's root files cannot run for it, and commands
// that reach a box from a laptop make no sense where it already is. Rather
// than ask sudo for a password the agent does not have, keelbox says what
// the agent can run.

// agentCommands are what the agent runs on its box.
var agentCommands = map[string]bool{
	"version": true, "expose": true, "status": true, "init": true, "completion": true, "report": true, "install": true,
}

// laptopCommands reach a box from somewhere else.
var laptopCommands = map[string]bool{
	"ssh": true, "attach": true, "login": true, "up": true, "create": true, "destroy": true,
	"resize": true, "park": true, "resume": true, "add": true, "update": true,
	"forget": true, "forget-token": true, "self-update": true, "reports": true,
	"ls": true, "boxes": true, "sessions": true, "notify": true,
	// The owner's side of the agent's package requests: the agent does
	// not approve its own.
	"requests": true, "approve": true, "deny": true,
}

// onBox is the box this machine is, and its agent account, from the
// world-readable snapshot root writes, or the previews status before the
// first snapshot. ok is false off a box.
var onBox = func() (box, agent string, ok bool) {
	if s, err := readSnapshot(); err == nil && s.Agent != "" {
		return s.Box, s.Agent, true
	}
	if data, err := os.ReadFile(previewsStatusPath); err == nil {
		if st, err := previews.ParseStatus(data); err == nil && st.Agent != "" {
			return "", st.Agent, true
		}
	}
	return "", "", false
}

// The world-readable files the agent's status reads; tests point them
// elsewhere.
var snapshotPath, previewsStatusPath = snapshot.Path, previews.StatusPath

// readSnapshot reads the snapshot as whoever runs keelbox: it is 0644.
func readSnapshot() (snapshot.Status, error) {
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return snapshot.Status{}, err
	}
	return snapshot.Parse(data)
}

// currentUser is the account keelbox runs as; tests replace it.
var currentUser = func() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// asAgentOnBox handles a command the agent cannot run on its box. done is
// false for everyone else, and for what the agent may run.
func asAgentOnBox(cmd string, o options, stdout, stderr io.Writer) (code int, done bool) {
	if os.Geteuid() == 0 {
		return 0, false
	}
	box, agent, ok := onBox()
	if !ok || currentUser() != agent {
		return 0, false
	}
	if box == "" {
		box = "this box"
	}
	switch {
	case cmd == "status" && !o.all:
		return agentStatus(stdout, stderr), true
	case agentCommands[cmd] || internalCommands[cmd]:
		return 0, false
	case laptopCommands[cmd]:
		failf(stderr, "you are already on %s, as %s. keelbox %s runs on the owner's laptop and reaches the box from there.\n%s",
			box, agent, cmd, agentHelp(box))
		return 2, true
	}
	failf(stderr, "keelbox %s needs root on %s, and %s has no sudo; keelbox will not ask for a password. "+
		"Ask the owner to run it from their laptop (keelbox %s %s).\n%s", cmd, box, agent, cmd, box, agentHelp(box))
	return 1, true
}

// agentHelp lists what the agent can run here.
func agentHelp(box string) string {
	return "On " + box + ", as the agent, you can run:\n" +
		"  keelbox status          the box's health as root last saw it, without sudo\n" +
		"  keelbox expose PORT     publish a dev server on the tailnet (--list, --remove NAME)\n" +
		"  keelbox install NAME    ask root's install service for an Ubuntu package (--why TEXT, --status)\n" +
		"  keelbox report TITLE    save a keelbox problem as a draft for the owner, after the user said yes\n" +
		"  keelbox version\n"
}

// agentStatus is keelbox status for the agent: what root's services last
// wrote to world-readable files (the snapshot, the previews status). It
// runs nothing as root and reads no root-only file.
func agentStatus(stdout, stderr io.Writer) int {
	s, err := readSnapshot()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		failf(stderr, "no status snapshot yet (%s); the watch run writes one every two minutes once keelbox apply has run.\n", snapshotPath)
		return 1
	case err != nil:
		fail(stderr, err)
		return 1
	}
	now := time.Now()
	row := func(k, v string) { fmt.Fprintf(stdout, "%-10s %s\n", k, plain.Line(v)) }
	row("box", s.Box)
	row("keelbox", fmt.Sprintf("%s, release %s", s.Keelbox, s.Release))
	age := now.Sub(s.Written).Round(time.Second)
	note := fmt.Sprintf("%s ago, by root's watch run", age)
	if s.Stale(now) {
		note += "; stale: the watch run has not written it for a while"
	}
	row("as of", note)
	if h := s.Host; h != nil {
		v := fmt.Sprintf("%d CPU, %s memory", h.CPUs, agentlimits.Size(h.Memory))
		if h.MemoryAvailable > 0 {
			v += fmt.Sprintf(", %s available", agentlimits.Size(h.MemoryAvailable))
		}
		row("size", v)
	}
	if len(s.Disk) > 0 {
		var ds []string
		for _, d := range s.Disk {
			ds = append(ds, fmt.Sprintf("%s %d%%", d.Mount, d.Percent))
		}
		row("disk", strings.Join(ds, ", ")+" used")
	}
	if u := s.AgentUsage; u != nil && u.MemoryMax > 0 {
		row("agent", fmt.Sprintf("%s of %s memory limit", agentlimits.Size(u.Memory), agentlimits.Size(u.MemoryMax)))
	}
	if s.Updates != nil {
		row("updates", updatesCell(s.Updates))
	}
	if d := s.Doctor; d != nil {
		v := fmt.Sprintf("%d failures, %d warnings (%s ago)", d.Failures, d.Warnings, now.Sub(d.Time).Round(time.Minute))
		if len(d.Failing) > 0 {
			v += ": " + strings.Join(d.Failing, ", ")
		}
		row("doctor", v)
	}
	if data, err := os.ReadFile(previewsStatusPath); err == nil {
		if st, err := previews.ParseStatus(data); err == nil {
			row("tailnet", tailnetCell(st))
			row("previews", previewsCell(st))
		}
	}
	if s.Sessions != nil && s.Sessions.RemoteControl != "" {
		row("remote", "Claude Code Remote Control "+s.Sessions.RemoteControl)
	}
	if agents := agentVersions(context.Background()); len(agents) > 0 {
		row("agents", strings.Join(agents, ", "))
	}
	projectRows(stdout, s.Projects)
	if st, err := installs.LoadState(installRequestsPath); err == nil {
		if n := len(st.Pending()); n > 0 {
			row("requests", fmt.Sprintf("%d waiting for the owner's approval (keelbox install --status)", n))
		}
	}
	fmt.Fprintln(stdout, "\nThe full check (keelbox doctor) needs root: ask the owner to run it from their laptop.")
	return 0
}

func tailnetCell(st previews.Status) string {
	switch {
	case !st.Tailnet.Connected:
		return "not connected"
	case !st.Tailnet.HTTPS:
		return "connected as " + st.Tailnet.Name + "; HTTPS certificates off"
	case !st.Certificate:
		return "connected as " + st.Tailnet.Name + "; no certificate yet (the first keelbox expose fetches it)"
	}
	return "connected as " + st.Tailnet.Name + ", certificate in place"
}

// previewsCell names each preview beside its URL and local port.
func previewsCell(st previews.Status) string {
	if st.Error != "" && len(st.Previews) == 0 {
		return "not served: " + st.Error
	}
	var out []string
	for _, p := range st.Previews {
		out = append(out, fmt.Sprintf("%s %s -> 127.0.0.1:%d", p.Name, p.URL, p.Port))
	}
	if len(out) == 0 {
		return "none (keelbox expose PORT)"
	}
	return strings.Join(out, ", ")
}

// agentNote is what keelbox with no command, or help, adds for the agent on
// its box; "" for everyone else.
func agentNote() string {
	if os.Geteuid() == 0 {
		return ""
	}
	box, agent, ok := onBox()
	if !ok || currentUser() != agent {
		return ""
	}
	if box == "" {
		box = "this box"
	}
	return agentHelp(box)
}

// agentVersions is each agent CLI on this account's PATH with the first
// line of its --version, such as "claude 2.1.311 (Claude Code)"; tests
// replace it.
var agentVersions = func(ctx context.Context) []string {
	var out []string
	for _, bin := range modules.AgentBins() {
		path, err := exec.LookPath(bin)
		if err != nil {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		v, err := exec.CommandContext(c, path, "--version").Output()
		cancel()
		line, _, _ := strings.Cut(strings.TrimSpace(string(v)), "\n")
		if err != nil || line == "" {
			line = "(--version failed)"
		}
		if !strings.HasPrefix(line, bin) {
			line = bin + " " + line
		}
		out = append(out, plain.Line(line))
	}
	return out
}
