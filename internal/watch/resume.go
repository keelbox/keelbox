package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Resuming the agent's conversations after a reboot
// (docs/adr/0090-reboots-for-updates.md, 0096-resume-after-any-reboot.md).
// Before a reboot, each tmux pane where Claude Code or Codex runs is noted,
// with what to type there to open the same conversation again: by Restart,
// and at every orderly shutdown by keelbox-conversations.service, so a
// reboot keelbox did not start (unattended-upgrades at a set time, sudo
// reboot, a cloud console) is covered too. tmux-resurrect brings the panes
// back, each at a shell in its directory, and the first watch runs after
// the boot type the command into each.

// ResumePath holds the panes to resume: root's, as the commands are typed
// into the agent's panes from it.
const ResumePath = Dir + "/resume.json"

// resumeFor is how long after the boot the watch run keeps trying: the
// panes come back within a minute or two of it.
const resumeFor = 20 * time.Minute

// NoteCommand is keelbox-conversations.service's stop: it notes the
// conversations when the box shuts down.
const NoteCommand = "note-conversations"

// Resume is the note Restart writes.
type Resume struct {
	Saved time.Time `json:"saved"`
	// Boot is the boot the note was written in: panes are resumed only in
	// a later one.
	Boot  string       `json:"boot"`
	Panes []ResumePane `json:"panes"`
}

// ResumePane is one pane and the command that opens its conversation.
type ResumePane struct {
	Pane    string   `json:"pane"` // session:window.pane
	Dir     string   `json:"dir"`
	Command []string `json:"command"`
}

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// agentProc is one of the agent's processes.
type agentProc struct {
	pid, ppid int
	args      []string
}

// agentProcs lists the agent's processes. ps shows the arguments joined
// with spaces, so an argument with a space in it reads as two.
func agentProcs(ctx context.Context, e *engine.Env) []agentProc {
	out, err := bounded(ctx, e.X, system.C("ps", "-u", e.AgentUser().Name, "-o", "pid=,ppid=,args="))
	if err != nil {
		return nil
	}
	var procs []agentProc
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			procs = append(procs, agentProc{pid: pid, ppid: ppid, args: f[2:]})
		}
	}
	return procs
}

// cli is the agent CLI a process runs: claude, codex or "".
func (p agentProc) cli() string {
	if len(p.args) == 0 {
		return ""
	}
	switch b := path.Base(p.args[0]); b {
	case "claude", "codex":
		return b
	}
	return ""
}

// claudeSession is what Claude Code keeps about a running process in
// ~/.claude/sessions/PID.json. The file is Claude Code's own, not
// documented: what it lacks is left out.
type claudeSession struct {
	SessionID string `json:"sessionId"`
}

func readClaudeSession(e *engine.Env, pid int) (claudeSession, bool) {
	agent := e.AgentUser()
	var s claudeSession
	b, err := e.FS(agent.Name).ReadFile(agent.Home + "/.claude/sessions/" + strconv.Itoa(pid) + ".json")
	if err != nil || json.Unmarshal(b, &s) != nil {
		return claudeSession{}, false
	}
	return s, true
}

// claudeAtWork says whether a Claude Code process says it is busy: in a
// terminal, or started by Remote Control or another program. A note whose
// process has ended does not count.
func claudeAtWork(ctx context.Context, e *engine.Env) string {
	agent := e.AgentUser()
	if agent.Home == "" {
		return ""
	}
	out, _ := bounded(ctx, e.X, system.As(agent.Name, "find", agent.Home+"/.claude/sessions", "-maxdepth", "1", "-type", "f", "-name", "*.json",
		"-exec", "grep", "-lE", `"status": ?"busy"`, "{}", "+"))
	if strings.TrimSpace(out) == "" {
		return ""
	}
	running := map[string]bool{}
	for _, p := range agentProcs(ctx, e) {
		running[strconv.Itoa(p.pid)] = true
	}
	for _, f := range strings.Fields(out) {
		if running[strings.TrimSuffix(path.Base(f), ".json")] {
			return "a Claude Code conversation is at work"
		}
	}
	return ""
}

// Flags kept when a conversation is resumed: those that say how the agent
// works, not what it was asked. The value says whether one follows.
var (
	claudeFlags = map[string]bool{
		"--dangerously-skip-permissions": false, "--allow-dangerously-skip-permissions": false,
		"--permission-mode": true, "--model": true, "--add-dir": true,
	}
	codexFlags = map[string]bool{
		"--dangerously-bypass-approvals-and-sandbox": false, "--search": false,
		"-m": true, "--model": true, "-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true,
		"-p": true, "--profile": true, "-c": true, "--config": true, "--add-dir": true,
	}
	// Subcommands that are not a conversation: such a pane is left as it is.
	claudeCommands = []string{"agents", "auth", "config", "daemon", "doctor", "install", "mcp", "migrate-installer", "plugin", "remote-control", "setup-token", "update"}
	codexCommands  = []string{"app", "app-server", "apply", "cloud", "completion", "debug", "exec", "features", "fork", "login", "logout", "mcp", "mcp-server", "review", "sandbox", "update"}
)

// keptFlags returns the flags in args that keep, and the first word that
// is not a flag: a subcommand or a prompt.
func keptFlags(args []string, keep map[string]bool) (kept []string, first string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			if first == "" {
				first = a
			}
			continue
		}
		name, _, inline := strings.Cut(a, "=")
		value, known := keep[name]
		switch {
		case known && (inline || !value):
			kept = append(kept, a)
		case known && i+1 < len(args):
			kept = append(kept, a, args[i+1])
			i++
		}
	}
	return kept, first
}

// resumeCommand is what to type to open a process's conversation again, or
// nil when it runs something else.
func resumeCommand(e *engine.Env, p agentProc) []string {
	switch p.cli() {
	case "claude":
		kept, first := keptFlags(p.args[1:], claudeFlags)
		if slices.Contains(claudeCommands, first) {
			return nil
		}
		if s, ok := readClaudeSession(e, p.pid); ok && sessionIDRe.MatchString(s.SessionID) {
			return append([]string{"claude", "--resume", s.SessionID}, kept...)
		}
		return append([]string{"claude", "--continue"}, kept...)
	case "codex":
		kept, first := keptFlags(p.args[1:], codexFlags)
		if slices.Contains(codexCommands, first) {
			return nil
		}
		// The most recent conversation in the pane's directory.
		return append([]string{"codex", "resume", "--last"}, kept...)
	}
	return nil
}

// conversations lists the tmux panes that run an agent CLI, each with the
// command that resumes it. An error means tmux could not be asked, which
// is not the same as no panes. At shutdown the command runs without a
// scope of its own, which systemd would refuse.
func conversations(ctx context.Context, e *engine.Env, atShutdown bool) ([]ResumePane, error) {
	if !e.Cfg.Tmux() {
		return nil, nil
	}
	agent := e.AgentUser()
	list := system.As(agent.Name, "tmux", "list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index}\t#{pane_pid}\t#{pane_current_path}")
	list.NoScope = atShutdown
	out, err := bounded(ctx, e.X, list)
	if err != nil {
		return nil, err
	}
	procs := agentProcs(ctx, e)
	var panes []ResumePane
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 3 {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		// The CLI is the pane's own process, or a child of its shell.
		for _, p := range procs {
			if p.pid != pid && p.ppid != pid {
				continue
			}
			if cmd := resumeCommand(e, p); cmd != nil {
				panes = append(panes, ResumePane{Pane: f[0], Dir: f[2], Command: cmd})
				break
			}
		}
	}
	return panes, nil
}

// noteConversations writes ResumePath before a reboot, or removes an old
// one when no pane runs an agent CLI. It returns how many it noted. When
// tmux cannot be asked, the last note stays. So does a note from an
// earlier boot that no watch run has replayed yet, when no pane runs an
// agent CLI now: the box went down again before the panes were resumed.
func noteConversations(ctx context.Context, e *engine.Env, now time.Time, atShutdown bool) (int, error) {
	panes, err := conversations(ctx, e, atShutdown)
	if err != nil {
		return 0, fmt.Errorf("listing the tmux panes: %s", oneLine(err.Error()))
	}
	if len(panes) == 0 {
		if b, err := e.X.ReadFile(ResumePath); err == nil {
			var r Resume
			if json.Unmarshal(b, &r) == nil && r.Boot != "" && r.Boot != bootID(e.X) {
				return 0, nil
			}
		}
		_ = e.X.Remove(ResumePath)
		return 0, nil
	}
	b, err := json.MarshalIndent(Resume{Saved: now, Boot: bootID(e.X), Panes: panes}, "", "  ")
	if err == nil {
		err = e.X.WriteFile(ResumePath, append(b, '\n'), 0o600, "root", "root")
	}
	if err != nil {
		return 0, err
	}
	return len(panes), nil
}

// NoteAtShutdown is keelbox-conversations.service's stop. systemd stops
// the unit before the user managers, so the agent's tmux still runs, with
// every pane: it notes the conversations and saves the sessions then. It
// notes only while the box is shutting down: a stop within a boot (an
// apply restarting the unit, systemctl stop) leaves the last note alone,
// so a later crash cannot resume conversations from long ago. It returns
// what it did, for the journal.
func NoteAtShutdown(ctx context.Context, e *engine.Env, now time.Time) string {
	if !e.Cfg.TmuxRecovery() {
		return "nothing noted: the tmux sessions are not restored (sessions.recovery is off)"
	}
	// is-system-running exits non-zero for every state but running.
	if state, _ := bounded(ctx, e.X, system.C("systemctl", "is-system-running")); state != "stopping" {
		return "nothing noted: the box is not shutting down (" + state + ")"
	}
	n, err := noteConversations(ctx, e, now, true)
	if err != nil {
		return "nothing noted, the last note kept: " + err.Error()
	}
	msg := strconv.Itoa(n) + " conversation(s) noted to open again after the boot"
	if err := saveSessionsNow(ctx, e, true); err != nil {
		msg += "; saving the tmux sessions failed: " + oneLine(err.Error())
	} else {
		msg += "; tmux sessions saved"
	}
	return msg
}

// shells are what a restored pane runs before anything is typed into it.
var shells = []string{"bash", "sh", "dash", "zsh", "fish"}

// resumeConversations types each noted command into its pane once the box
// has rebooted and tmux-resurrect has brought the pane back at a shell. A
// pane that is not back yet is tried at the next run, until resumeFor has
// passed.
func resumeConversations(ctx context.Context, e *engine.Env, now time.Time) {
	b, err := e.X.ReadFile(ResumePath)
	if err != nil {
		return
	}
	var r Resume
	if json.Unmarshal(b, &r) != nil {
		_ = e.X.Remove(ResumePath)
		return
	}
	// The time runs from the boot, so a box that was off for days still
	// resumes; before the reboot, and when the boot time is unknown, from
	// the note. A soft reboot (systemctl soft-reboot) keeps the boot id
	// and time but starts the agent's tmux server again: a server started
	// after the note has the panes back too.
	rebooted := r.Boot == "" || r.Boot != bootID(e.X)
	since := r.Saved
	if boot, ok := bootTime(e.X); ok && rebooted && boot.After(since) {
		since = boot
	}
	if start, ok := tmuxStarted(ctx, e); ok && start.After(r.Saved) {
		rebooted = true
		if start.After(since) {
			since = start
		}
	}
	if now.Sub(since) > resumeFor {
		_ = e.X.Remove(ResumePath)
		if e.Log != nil && len(r.Panes) > 0 {
			fmt.Fprintf(e.Log, "keelbox watch-run: %d conversation(s) not resumed after the reboot: their tmux panes did not come back\n", len(r.Panes))
		}
		return
	}
	if !rebooted {
		return
	}
	agent := e.AgentUser()
	out, err := bounded(ctx, e.X, system.As(agent.Name, "tmux", "list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index}\t#{pane_current_command}\t#{pane_current_path}"))
	if err != nil {
		return // no server yet
	}
	type paneNow struct{ command, dir string }
	live := map[string]paneNow{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Split(l, "\t"); len(f) == 3 {
			live[f[0]] = paneNow{f[1], f[2]}
		}
	}
	var left []ResumePane
	for _, p := range r.Panes {
		got, ok := live[p.Pane]
		if !ok || !slices.Contains(shells, got.command) {
			left = append(left, p)
			continue
		}
		line := shellLine(p.Command)
		if got.dir != p.Dir {
			line = "cd -- " + shellWord(p.Dir) + " && " + line
		}
		if _, err := bounded(ctx, e.X, system.As(agent.Name, "tmux", "send-keys", "-t", p.Pane, "-l", "--", line)); err != nil {
			left = append(left, p)
			continue
		}
		_, _ = bounded(ctx, e.X, system.As(agent.Name, "tmux", "send-keys", "-t", p.Pane, "Enter"))
	}
	if len(left) == 0 {
		_ = e.X.Remove(ResumePath)
		return
	}
	r.Panes = left
	if b, err := json.MarshalIndent(r, "", "  "); err == nil {
		_ = e.X.WriteFile(ResumePath, append(b, '\n'), 0o600, "root", "root")
	}
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./=:@%+,-]+$`)

// shellWord quotes s for the agent's shell when it needs it.
func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellLine(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellWord(a)
	}
	return strings.Join(q, " ")
}

// tmuxStarted is when the agent's tmux server started.
func tmuxStarted(ctx context.Context, e *engine.Env) (time.Time, bool) {
	out, err := bounded(ctx, e.X, system.As(e.AgentUser().Name, "tmux", "display-message", "-p", "#{start_time}"))
	if err != nil {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
}
