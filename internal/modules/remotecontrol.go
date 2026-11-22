package modules

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/watch"
)

// Claude Code Remote Control (docs/adr/0023-remote-control.md): with
// sessions.remote_control.enabled, `claude remote-control` runs in server
// mode as a user service for the agent, so the Claude app and
// claude.ai/code start and steer sessions on the box. Claude Code makes
// outbound HTTPS requests only; nothing listens on the box.
//
// keelbox never signs in. Until the user's own sign-in (keelbox login) makes
// `claude auth status` succeed, the unit's ExecCondition skips it: it is
// inactive, not failed, and doctor says it waits for the sign-in.

// RemoteControlUnit is the agent's user service.
const RemoteControlUnit = watch.RemoteControlUnit

// RemoteControlCommand is the server's command line, run from the directory.
func RemoteControlCommand(spawn, box string) string {
	return "claude remote-control --spawn " + spawn + " --remote-control-session-name-prefix " + box
}

func remoteControlUnit(home, dir, spawn, box string) string {
	return fmt.Sprintf(`# Managed by keelbox (sessions.remote_control). Claude Code Remote Control
# in server mode: open claude.ai/code or the Claude app (Code) to start or
# steer sessions on this box. Sessions are named %[3]s-<words>.
[Unit]
Description=Claude Code Remote Control (keelbox)
Documentation=https://code.claude.com/docs/en/remote-control
Wants=network-online.target
After=network-online.target
# A server that keeps failing (no Remote Control on the account, the first
# run's questions unanswered) stops after six tries an hour; the keelbox
# watchdog tries again after 15 minutes, a few times a day, and keelbox
# login or keelbox apply starts it again.
StartLimitIntervalSec=1h
StartLimitBurst=6

[Service]
Type=exec
WorkingDirectory=%[1]s/%[2]s
Environment=PATH=%[1]s/.local/share/mise/shims:%[1]s/.local/bin:/usr/local/bin:/usr/bin:/bin
# Skipped, not failed, until you sign in with keelbox login. Only the exit
# code is used.
ExecCondition=/bin/sh -c 'exec claude auth status >/dev/null 2>&1'
ExecStart=/bin/sh -c 'exec %[4]s'
# Without a terminal the server redraws its status line every second:
# hundreds of thousands of journal lines a day. Its output goes nowhere;
# errors still reach the journal.
StandardOutput=null
StandardError=journal
# Server mode exits after about ten minutes without network, with an exit
# code not yet known: restart on any exit, backing off from 30 s to 10 min.
Restart=always
RestartSec=30
RestartSteps=5
RestartMaxDelaySec=10min
# A stop is Ctrl+C to the server, which leaves its sessions to come back
# when it starts again; the keelbox watchdog starts it when it stays down.
KillSignal=SIGINT

[Install]
WantedBy=default.target
`, home, dir, box, RemoteControlCommand(spawn, box))
}

// claudeSignedIn reports whether the agent's Claude Code is signed in, from
// the exit code of the vendor's own status command only.
func claudeSignedIn(ctx context.Context, e *engine.Env) bool {
	return system.Succeeds(ctx, e.X, system.As(e.Cfg.Users.Agent.Name, e.AgentTool("claude"), "auth", "status"))
}

// RemoteControlState is the unit as systemd reports it.
type RemoteControlState struct {
	Active, Sub, Result string
	Restarts            string
}

func remoteControlState(ctx context.Context, e *engine.Env) RemoteControlState {
	out, _ := system.Output(ctx, e.X, userctl(e.Cfg.Users.Agent.Name, "show", "-p", "ActiveState,SubState,Result,NRestarts", RemoteControlUnit))
	kv := engine.ParseEnvFile(out)
	return RemoteControlState{Active: kv["ActiveState"], Sub: kv["SubState"], Result: kv["Result"], Restarts: kv["NRestarts"]}
}

func remoteControlSteps(e *engine.Env) []engine.Step {
	u := e.AgentUser()
	c := e.Cfg
	dir, spawn := c.RemoteControlDir(), c.RemoteControlSpawn()
	unitPath := u.Home + "/.config/systemd/user/" + RemoteControlUnit
	link := u.Home + "/.config/systemd/user/default.target.wants/" + RemoteControlUnit
	return []engine.Step{
		engine.DirStep("claude-remote-control-dir", u.Home+"/"+dir, 0, u.Name, ""),
		engine.FileStep("claude-remote-control-unit", engine.File{
			Path: unitPath, Content: []byte(remoteControlUnit(u.Home, dir, spawn, c.Box)), Mode: 0o644, Owner: u.Name,
			OnChange: func(ctx context.Context, e *engine.Env) error {
				return restartUserUnit(ctx, e, u, RemoteControlUnit, true)
			},
		}),
		{
			Name: "claude-remote-control-service",
			Soft: true,
			User: u.Name,
			Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
				if st, err := e.FS(u.Name).Stat(link); err != nil || !st.Exists {
					return engine.Pending("enable %s for %s", RemoteControlUnit, u.Name), err
				}
				if claudeSignedIn(ctx, e) && !system.Succeeds(ctx, e.X, userctl(u.Name, "is-active", "--quiet", RemoteControlUnit)) {
					return engine.Pending("start %s (Claude Code is signed in)", RemoteControlUnit), nil
				}
				return engine.Converged(), nil
			},
			Apply: func(ctx context.Context, e *engine.Env) error {
				if err := startUserManager(ctx, e, u); err != nil {
					return err
				}
				signedIn := claudeSignedIn(ctx, e)
				cmds := []system.Cmd{userctl(u.Name, "daemon-reload"), userctl(u.Name, "enable", RemoteControlUnit)}
				if signedIn {
					cmds = append(cmds, userctl(u.Name, "reset-failed", RemoteControlUnit), userctl(u.Name, "restart", RemoteControlUnit))
				}
				for _, cmd := range cmds {
					if _, err := e.X.Run(ctx, cmd); err != nil {
						return err
					}
				}
				if signedIn && !remoteControlComesUp(ctx, e) {
					// Its first run asks the user two questions on a
					// terminal; until they answer, it cannot run. That is
					// the user's step, not a failure of the box.
					return engine.Defer("Claude Code Remote Control needs your answers on a terminal once (enable Remote Control, trust ~/%s): keelbox login %s claude-code", c.RemoteControlDir(), c.Box)
				}
				return nil
			},
		},
	}
}

// rcSettle is how long apply gives the restarted server to run. rcSteady is
// how long it must then stay running, without a restart, to count as up: a
// server whose first-run questions are unanswered starts, exits within a
// second or two and is started again by systemd, so one "running" reading
// proves nothing.
var rcSettle, rcSteady, rcPoll = 10 * time.Second, 6 * time.Second, time.Second

// remoteControlComesUp waits briefly for the service to run, and then for
// it to stay running: still up after rcSteady, with no restart in between.
func remoteControlComesUp(ctx context.Context, e *engine.Env) bool {
	wait := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	running := func(st RemoteControlState) bool { return st.Active == "active" && st.Sub == "running" }
	deadline := time.Now().Add(rcSettle)
	for {
		if st := remoteControlState(ctx, e); running(st) {
			if !wait(rcSteady) {
				return false
			}
			again := remoteControlState(ctx, e)
			return running(again) && again.Restarts == st.Restarts
		}
		if time.Now().After(deadline) || !wait(rcPoll) {
			return false
		}
	}
}

// verifyRemoteControl says plainly whether Remote Control runs, and when it
// cannot, what the user has to do.
func verifyRemoteControl(ctx context.Context, e *engine.Env) engine.Finding {
	dir := "~/" + e.Cfg.RemoteControlDir()
	if !claudeSignedIn(ctx, e) {
		return warn("agents", "claude-remote-control", "waiting for your Claude Code sign-in: it cannot start until you run keelbox login (keelbox never signs in for you)")
	}
	st := remoteControlState(ctx, e)
	if st.Active == "active" && st.Sub == "running" {
		return ok("agents", "claude-remote-control", "running in "+dir+" ("+e.Cfg.RemoteControlSpawn()+"); open claude.ai/code or the Claude app, Code")
	}
	detail := strings.TrimSpace(fmt.Sprintf("%s/%s, result %s, %s restarts", st.Active, st.Sub, st.Result, st.Restarts))
	// Usually the first run's questions, unanswered or answered no: the
	// user's choice about an optional feature, so a warning that needs
	// them, not a failure of the box.
	return warn("agents", "claude-remote-control", "needs you: signed in but not running ("+detail+"). Answer Claude Code's two first-run questions: keelbox login "+e.Cfg.Box+
		" claude-code. Logs, as "+e.Cfg.Users.Agent.Name+": journalctl --user -u "+RemoteControlUnit)
}

// RemoteControlSummary is a one-line state for keelbox status.
func RemoteControlSummary(ctx context.Context, e *engine.Env) string {
	if !claudeSignedIn(ctx, e) {
		return "waiting for Claude Code sign-in (keelbox login)"
	}
	if st := remoteControlState(ctx, e); st.Active == "active" && st.Sub == "running" {
		return "running in ~/" + e.Cfg.RemoteControlDir()
	}
	return "not running (keelbox doctor)"
}
