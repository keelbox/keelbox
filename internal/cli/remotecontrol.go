package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// Claude Code Remote Control from the laptop (docs/adr/0023-remote-control.md).
// keelbox only starts and stops the agent's user service and runs Claude
// Code's own commands on the user's terminal; it reads nothing they print.
// Of what Claude Code keeps on the box it asks one yes/no question, answered
// by an exit code (rcAnswered, docs/adr/0073-remote-control-first-run-ends-by-itself.md).

// userctl reaches the agent's own service manager, also from a sudo shell
// on the box, which has no XDG_RUNTIME_DIR.
const userctl = `XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}" systemctl --user `

const (
	rcSignedIn = "claude auth status"
	rcActive   = userctl + "is-active --quiet " + modules.RemoteControlUnit
	rcRestart  = userctl + "reset-failed " + modules.RemoteControlUnit + " 2>/dev/null; " + userctl + "restart " + modules.RemoteControlUnit
	rcStop     = userctl + "stop " + modules.RemoteControlUnit
	// rcInterrupt ends the copy of the server on the user's terminal the way
	// Ctrl-C there would: SIGINT, as the agent, to the agent's own
	// "claude remote-control" processes. The pattern starts at the start of
	// the command line, so neither the shell that runs the server nor this
	// command's own shell matches. The service is stopped while that copy
	// runs and is started only after this has returned. Had the watchdog
	// started it meanwhile, that copy gets the signal too, the one a stop
	// of the unit sends, and the unit is restarted next in any case.
	rcInterrupt = `pkill -INT -u "$(id -u)" -f '^([^ ]*/)?claude remote-control '`
)

// rcAnsweredPy looks up Claude Code's record of its two first-run answers in
// the agent's ~/.claude.json: remoteDialogSeen, and hasTrustDialogAccepted
// under projects[the directory's absolute path]. Claude Code writes each as
// true when the answer is yes, and neither when the question is only shown.
// It uses those two keys and no other, prints nothing and exits 0 (both
// yes), 1 (not both, or no file yet) or 2 (a file it cannot read or parse).
// The keys are not documented by the vendor: should they change, this
// answers 1 and the user ends the run with Ctrl-C, as before (ADR 0073).
// One line: sudo -i, on a box that is this machine, drops a command's
// newlines.
const rcAnsweredPy = `import json,os,sys; sys.excepthook=lambda *a: os._exit(2); h=os.path.expanduser("~"); f=os.path.join(h,".claude.json"); os.path.exists(f) or sys.exit(1); c=json.load(open(f)); d=os.path.join(h,sys.argv[1]); p=c.get("projects",{}); sys.exit(0 if c.get("remoteDialogSeen") is True and any(p.get(k,{}).get("hasTrustDialogAccepted") is True for k in (d,os.path.realpath(d))) else 1)`

// rcAnswered is that check as a command for the agent's shell; dir is the
// Remote Control directory, relative to the agent's home, which the box
// resolves. Without python3 the shell exits 127: not an answer either.
func rcAnswered(dir string) string {
	return "python3 -I -c '" + rcAnsweredPy + "' " + quoteSh(dir)
}

// rcAnswer is what the box says about the two first-run answers.
type rcAnswer int

const (
	rcCannotTell rcAnswer = iota // the check did not run, or could not read the record
	rcNotBoth
	rcBoth
)

// rcAnswers runs the check as the agent and uses its exit code only.
func rcAnswers(ctx context.Context, cfg *config.Config, sh *shell) rcAnswer {
	switch sh.exitCode(ctx, rcAnswered(cfg.RemoteControlDir())) {
	case 0:
		return rcBoth
	case 1:
		return rcNotBoth
	}
	return rcCannotTell
}

// rcState is the unit's ActiveState, SubState and NRestarts.
func rcState(ctx context.Context, sh *shell) (active, sub, restarts string) {
	out, _ := sh.output(ctx, userctl+"show -p ActiveState,SubState,NRestarts "+modules.RemoteControlUnit)
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		switch k {
		case "ActiveState":
			active = v
		case "SubState":
			sub = v
		case "NRestarts":
			restarts = v
		}
	}
	return active, sub, restarts
}

// How long rcRunning waits for the service to run, and for how long it must
// then keep running without a restart; tests shorten them.
var rcWait, rcSettle, rcPoll = 45 * time.Second, 6 * time.Second, time.Second

// rcRunning waits for the service to be active and running, then checks it
// is still running, with no restart in between, a few seconds later. A
// look in the moment of a restart (activating, auto-restart) is not a
// failure: the service gets the whole wait.
func rcRunning(ctx context.Context, sh *shell) bool {
	return rcStaysUp(ctx, func() (string, string, string) { return rcState(ctx, sh) })
}

func rcStaysUp(ctx context.Context, state func() (active, sub, restarts string)) bool {
	sleep := func(d time.Duration) bool { return rcSleep(ctx, d) }
	deadline := time.Now().Add(rcWait)
	for {
		if a, sub, n := state(); a == "active" && sub == "running" {
			if !sleep(rcSettle) {
				return false
			}
			if a2, sub2, n2 := state(); a2 == "active" && sub2 == "running" && n2 == n {
				return true
			}
		}
		if time.Now().After(deadline) || !sleep(rcPoll) {
			return false
		}
	}
}

// remoteControlLogin starts the service once Claude Code is signed in. If it
// does not stay up, it runs the same server on the user's terminal, so they
// answer Claude Code's one-time questions (enable Remote Control, trust the
// directory) themselves, then hands over to the service. With both answers
// already recorded there is nothing to ask, and no terminal run. A run that
// still does not stay up is offered again in place; declining it is the
// user's choice about an optional feature, never a failure of the rest.
func remoteControlLogin(ctx context.Context, cfg *config.Config, sh *shell, p *prompt.Prompter, v signInView, stdout, stderr io.Writer) bool {
	const name = "remote"
	running := "Claude Code Remote Control running as a service on " + cfg.Box + ", without this terminal; open claude.ai/code or the Claude app (Code)"
	ok, err := sh.succeeds(ctx, rcSignedIn)
	switch {
	case err != nil:
		// Not known to be signed out: the check did not run.
		v.line(stdout, name, v.st.Bang(), fmt.Sprintf("Claude Code Remote Control not checked: %v. Then: keelbox login %s claude-code", sh.why(ctx, cfg, err), cfg.Box))
		return false
	case !ok:
		v.line(stdout, name, v.st.Dim("○"), "Claude Code Remote Control waits for the claude-code sign-in")
		return false
	}
	if ok, _ := sh.succeeds(ctx, rcActive); ok && !rcRestartedSince(ctx, sh) {
		v.line(stdout, name, v.st.Tick(), "Claude Code Remote Control running")
		return true
	}
	// A server whose first-run questions are known to be unanswered cannot
	// stay up: go straight to the questions, without starting it and
	// waiting most of a minute to see it fail.
	answers := rcAnswers(ctx, cfg, sh)
	if answers != rcNotBoth {
		_, _ = sh.succeeds(ctx, rcRestart)
		if rcRunning(ctx, sh) {
			v.line(stdout, name, v.st.Tick(), running)
			return true
		}
	}
	dir := "~/" + cfg.RemoteControlDir()
	cmd := modules.RemoteControlCommand(cfg.RemoteControlSpawn(), cfg.Box)
	for first := true; ; first = false {
		starting := "starting Claude Code Remote Control as a service"
		if !first {
			answers = rcAnswers(ctx, cfg, sh)
		}
		if answers == rcBoth || rcSeed(ctx, cfg, sh, v, dir, cmd, answers, stdout) {
			starting = "both answers are in: " + starting
		}
		v.line(stdout, name, v.st.Dim("…"), starting)
		_, _ = sh.succeeds(ctx, rcRestart)
		if rcRunning(ctx, sh) {
			v.line(stdout, name, v.st.Tick(), running)
			return true
		}
		msg := "Claude Code Remote Control is not running: Claude Code still needs both answers (enable Remote Control: y; trust " + dir + ": y)."
		if rcAnswers(ctx, cfg, sh) == rcBoth {
			// Not the answers, then: Claude Code's own reason is in the journal.
			msg = "Claude Code Remote Control is not running, though both first-run answers are recorded. Its errors: keelbox ssh " + cfg.Box + ", then journalctl --user -u " + modules.RemoteControlUnit + "."
		}
		if v.st.Live {
			fmt.Fprint(stderr, v.st.Problem(ui.NeedsYou, msg+" Later: keelbox login "+cfg.Box+" claude-code"))
		} else {
			fmt.Fprintf(stderr, "keelbox: %s Later: keelbox login %s claude-code\n", msg, cfg.Box)
		}
		if p == nil || !p.Interactive() {
			return false
		}
		again, err := p.Confirm("Try again now?", true)
		if err != nil || !again {
			return false
		}
	}
}

// rcSleep waits for d, or returns false when ctx ends first.
func rcSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// How often rcSeed asks the box whether both answers are in, and how long it
// then leaves Claude Code to show "Connected" before it ends the run; tests
// shorten them.
var rcAsk, rcConnect = 2 * time.Second, 6 * time.Second

// rcSeed runs the server once on the user's terminal, after saying what
// Claude Code will ask and what to answer. Once the box records both
// answers, keelbox ends that run from the box side, and rcSeed reports
// true; a run the user ended, or one Claude Code ended after a no, is false.
// With answers that cannot be told (rcCannotTell) it promises nothing, and
// only the user's Ctrl-C ends the run.
func rcSeed(ctx context.Context, cfg *config.Config, sh *shell, v signInView, dir, cmd string, answers rcAnswer, stdout io.Writer) bool {
	lead := "Claude Code asks two questions the first time. Answer y to both:"
	// Claude Code has asked these in both orders, so they are not numbered.
	q1 := fmt.Sprintf("- \"Trust %s?\" keelbox made this folder on %s for Remote Control sessions.", dir, cfg.Box)
	q2 := "- \"Enable Remote Control?\""
	// What happens next is the last thing keelbox can say: Claude Code then
	// has the terminal, and its own "Press Ctrl+C to stop" reads as a
	// warning not to. So this goes under the heading, where it stays in
	// view.
	next := "A few seconds after both answers, keelbox ends this copy by itself. If it does not, press Ctrl-C when it says Connected."
	if answers == rcCannotTell {
		next = "When it says Connected, press Ctrl-C: keelbox is waiting for that, and nothing more happens until you do."
	}
	then := "keelbox then runs Remote Control as a service, so it keeps working after you close this terminal. Claude Code's own \"Press Ctrl+C to stop\" means only the copy on this terminal."
	if v.st.Live {
		fmt.Fprintln(stdout)
		for _, l := range []string{lead, q1, q2} {
			v.hint(stdout, l)
		}
		v.open(stdout, "Claude Code Remote Control", fmt.Sprintf("cd %s && %s, as %s", dir, cmd, sh.user))
		fmt.Fprintln(stdout, "  "+v.st.BoldAccent(ui.Hang(next, max(20, min(v.st.Cols(), 72)-2), 2)))
		v.hint(stdout, then)
		fmt.Fprintln(stdout)
	} else {
		fmt.Fprintf(stdout, "\n%-12s %s\n", "remote", strings.Join([]string{lead, q1, q2, next, then}, " "))
	}
	_, _ = sh.succeeds(ctx, rcStop)
	// The server has the terminal: ssh gets its file descriptors, and
	// keelbox reads nothing that passes through (ADR 0014).
	run := func() { _ = sh.interactive(ctx, "cd "+dir+" && "+cmd) }
	ended := false
	if answers == rcCannotTell {
		run()
	} else {
		ended = rcRunUntilAnswered(ctx, cfg, sh, run)
	}
	v.close(stdout)
	return ended
}

// rcRunUntilAnswered is run with a watch on the two answers beside it, over
// ssh connections of its own. It returns when run has returned and the
// watch has ended, so no check and no signal is under way when the service
// is started; true means keelbox ended the run.
func rcRunUntilAnswered(ctx context.Context, cfg *config.Config, sh *shell, run func()) bool {
	running, stop := context.WithCancel(ctx)
	ended := make(chan bool, 1)
	go func() { ended <- rcEndWhenAnswered(ctx, running, cfg, sh) }()
	run()
	stop()
	return <-ended
}

// rcEndWhenAnswered asks the box every rcAsk whether both answers are in,
// for as long as the terminal run goes on (running). When they are, it
// leaves Claude Code rcConnect to connect, then interrupts it once and
// reports whether a process took the signal. It gives up, leaving the run
// to the user's Ctrl-C, as soon as the check cannot tell. It never touches
// the local ssh that holds the terminal: that ends by itself when the
// server does, and restores the terminal as it always does.
func rcEndWhenAnswered(ctx, running context.Context, cfg *config.Config, sh *shell) bool {
	for rcSleep(running, rcAsk) {
		switch rcAnswers(running, cfg, sh) {
		case rcNotBoth:
			continue
		case rcCannotTell:
			return false
		}
		if !rcSleep(running, rcConnect) {
			return false
		}
		// Under ctx, not running: the run ends because of this signal, and
		// that must not cut short the command that sends it.
		ok, _ := sh.succeeds(ctx, rcInterrupt)
		return ok
	}
	return false
}

// rcRestartedSince reports a unit that systemd is restarting after failures.
func rcRestartedSince(ctx context.Context, sh *shell) bool {
	out, err := sh.output(ctx, userctl+"show -p NRestarts --value "+modules.RemoteControlUnit)
	return err != nil || strings.TrimSpace(out) != "0"
}

// remoteControlLine is the state for keelbox sessions, from exit codes.
func remoteControlLine(ctx context.Context, cfg *config.Config, sh *shell) string {
	ok, err := sh.succeeds(ctx, rcSignedIn)
	switch {
	case err != nil:
		return fmt.Sprintf("not checked: %v", sh.why(ctx, cfg, err))
	case !ok:
		return "waiting for your Claude Code sign-in (keelbox login)"
	}
	if ok, _ := sh.succeeds(ctx, rcActive); ok {
		return "running; sessions are in claude.ai/code and the Claude app (Code)"
	}
	return "not running (keelbox doctor)"
}

// askRemoteControl offers Remote Control once Claude Code is signed in, as
// one more way to reach the agents, saying in three lines what it is for
// and what it lets a Claude account do; a no is told how to turn it on
// later (ADR 0061). Yes turns it on in the box's
// config; the last apply installs the service and up then starts it.
func (w *wizard) askRemoteControl(cfg *config.Config, st *upState) (*config.Config, error) {
	box, path := cfg.Box, boxConfigPath(cfg.Box)
	later := fmt.Sprintf("To turn it on later: set sessions.remote_control.enabled: true in %s, then keelbox apply %s and keelbox login %s claude-code.",
		tildePath(path), box, box)
	fmt.Fprintln(w.out)
	w.hint(fmt.Sprintf("Claude Code Remote Control: start and continue Claude Code sessions on %s from the Claude app or claude.ai/code, with no SSH app or terminal. "+
		"Anyone signed in to your Claude account can then run commands on %s as the agent. It needs a claude.ai plan, not an API key.", box, box))
	ok, err := w.p.Confirm("Turn on Remote Control, so the Claude app can start sessions on "+box+"?", false)
	if err != nil {
		return cfg, err
	}
	if !ok {
		w.hint(later)
		return cfg, nil
	}
	c, err := enableRemoteControl(path)
	if err != nil {
		fmt.Fprintf(w.errOut, "keelbox up: could not turn on Remote Control: %v. %s\n", err, later)
		return cfg, nil
	}
	st.RemoteControlNext = true
	return c, nil
}

// rcOff is the starter config's Remote Control switch, off. The trailing
// space it keeps holds the comment's column.
var rcOff = regexp.MustCompile(`(?m)^([ \t]*remote_control:[ \t]*(?:#.*)?\n[ \t]+enabled:[ \t]*)false( ?)`)

// enableRemoteControl sets sessions.remote_control.enabled to true in the
// box config at path, keeping the rest of the file as it is.
func enableRemoteControl(path string) (*config.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !rcOff.Match(b) {
		return nil, fmt.Errorf("%s has no sessions.remote_control.enabled: false to change", tildePath(path))
	}
	if err := os.WriteFile(path, rcOff.ReplaceAll(b, []byte("${1}true${2}${2}")), 0o644); err != nil { //nolint:gosec // G306: readable, as writeBoxConfig writes it
		return nil, err
	}
	c, err := config.Load(path)
	if err != nil {
		_ = os.WriteFile(path, b, 0o644) //nolint:gosec // G306: the file as it was
		return nil, err
	}
	return c, nil
}
