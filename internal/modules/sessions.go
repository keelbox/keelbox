package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/ui"
	"github.com/keelbox/keelbox/internal/watch"
)

// Sessions makes interactive work survive disconnects and reboots: a
// managed tmux config, Resurrect + Continuum at pinned commits, user
// lingering, and (optionally) a user service that starts the tmux server at
// boot so Continuum restores the saved sessions.
//
// No tmux auto-attach is added to shell startup files: it breaks scp, rsync,
// editors' remote SSH and `ssh box command`. Attach explicitly:
// ssh -t agent@box tmux new -A -s main
type Sessions struct{}

func (Sessions) Name() string                  { return "sessions" }
func (Sessions) Enabled(c *config.Config) bool { return c.Tmux() }

const tmuxService = watch.TmuxUnit

// WelcomeCommand is the keelbox command that prints the welcome a new
// session starts with.
const WelcomeCommand = "welcome"

// SessionHint is a .bashrc block, after Ubuntu's interactive guard, that
// lists the agent's tmux sessions on an interactive SSH login outside tmux:
// the first thing a phone user needs. It never attaches by itself.
const SessionHint = `# On an interactive SSH login outside tmux, list the running sessions.
if [ -n "$SSH_TTY" ] && [ -z "$TMUX" ] && command -v tmux >/dev/null; then
  keelbox_sessions=$(tmux list-sessions -F '#S' 2>/dev/null | tr '\n' ' ')
  if [ -n "$keelbox_sessions" ]; then
    echo "tmux sessions: $keelbox_sessions  (attach: tmux new -A -s main)"
  fi
  unset keelbox_sessions
fi`

// welcomeMark is the tmux session option that carries a new session's
// welcome from tmux to the shell. Its value is the first pane's id while
// that pane's shell has still to print the welcome, "retry" and the id when
// the shell could not, and "again" and the id when tmux has started the
// shell a second time for it.
const welcomeMark = "@keelbox-welcome"

// welcomeMarkAge is how old, in seconds, a new session's mark may be for
// its first shell to act on it. tmux runs hooks late while its config is
// still loading, which is when tmux-continuum restores sessions at boot:
// their marks arrive after their shells have started. A late mark is not
// for whatever shell starts in that pane next.
const welcomeMarkAge = 30

// tmuxWelcome marks a new session's first pane, so the shell in it, and no
// other, prints the welcome (SessionWelcome). On a box's first apply the
// tmux server starts session main before keelbox and the agent CLIs are
// installed, so that shell cannot print it and leaves "retry": the first
// attach then starts the shell again, only while it is bash at a first
// prompt that has printed nothing else (cursor on the first row, no
// history): no command has run in it.
const tmuxWelcome = `
# The welcome (sessions.welcome in the box's config). tmux marks a new
# session's first pane; the shell in it prints keelbox's welcome once and
# clears the mark (the "sessions" block in ~/.bashrc). A shell that could
# not print it leaves "retry": when someone attaches and that shell is
# still at its first prompt, with nothing else on its screen, tmux starts it
# again so it can. Add hooks of your own with "set-hook -ga"; "set-hook -g"
# replaces these.
set-hook -g session-created[90] 'set -F ` + welcomeMark + ` "#{pane_id}"'
set-hook -g client-attached[90] {
    if -F "#{&&:#{==:#{` + welcomeMark + `},retry#{pane_id}},#{&&:#{==:#{pane_current_command},bash},#{&&:#{==:#{history_size},0},#{==:#{cursor_y},0}}}}" {
        set -F ` + welcomeMark + ` "again#{pane_id}"
        respawn-pane -k
    }
}
`

// WelcomeOptions are what the welcome says that only the config knows.
type WelcomeOptions struct {
	Box, Agent string
	Projects   string // the agent's projects directory, in its home
	Sudo       bool   // users.agent.sudo
}

// SessionWelcome is the part of the .bashrc block that prints keelbox's
// welcome: in an interactive shell (the block sits after Ubuntu's
// interactive guard) on a terminal, inside tmux, in the one pane tmux
// marked when it created the session, while the mark is fresh. It costs
// every other shell in tmux one tmux call. A shell that cannot print it (no
// keelbox yet, or one that does not know the command) says so in the mark
// and prints nothing.
func SessionWelcome(o WelcomeOptions) string {
	cmd := BinaryPath + " " + WelcomeCommand + " --box " + shellQuote(o.Box) + " --agent " + shellQuote(o.Agent) + " --projects " + shellQuote(o.Projects)
	if o.Sudo {
		cmd += " --sudo"
	}
	return strings.NewReplacer("{mark}", welcomeMark, "{age}", fmt.Sprint(welcomeMarkAge), "{welcome}", cmd).Replace(`# In a new tmux session's first pane, print keelbox's welcome once
# (sessions.welcome in the box's config turns it off). tmux marks that pane
# when it creates the session; a mark over {age} seconds old was set too
# late for the session's first shell and is dropped.
if [ -n "$TMUX" ] && [ -n "$TMUX_PANE" ] && [ -t 1 ]; then
  keelbox_welcome=$(tmux display-message -p '#{?{mark},#{{mark}},-} #{session_created} #{session_name}' 2>/dev/null)
  keelbox_mark=${keelbox_welcome%% *}
  keelbox_welcome=${keelbox_welcome#* }
  if [ "$keelbox_mark" = "$TMUX_PANE" ] && [ $(( ${EPOCHSECONDS:-0} - ${keelbox_welcome%% *} )) -ge {age} ]; then
    tmux set-option -qu {mark} 2>/dev/null
  elif [ "$keelbox_mark" = "$TMUX_PANE" ] || [ "$keelbox_mark" = "again$TMUX_PANE" ]; then
    if {welcome} --session "${keelbox_welcome#* }" 2>/dev/null; then
      tmux set-option -qu {mark} 2>/dev/null
    else
      tmux set-option -q {mark} "retry$TMUX_PANE" 2>/dev/null
    fi
  fi
  unset keelbox_welcome keelbox_mark
fi`)
}

// shellQuote quotes s as one word for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// TmuxOptions shape the managed tmux configuration.
type TmuxOptions struct {
	Box, Home string
	Recovery  bool // load tmux-resurrect and tmux-continuum
	// CSIu selects CSI u for extended keys; the option exists from tmux 3.5
	// (Ubuntu 26.04), and 3.4 (Ubuntu 24.04) rejects it.
	CSIu bool
	// Welcome marks a new session's first pane for the welcome.
	Welcome bool
}

// TmuxConfig renders the managed tmux configuration.
func TmuxConfig(o TmuxOptions) string {
	box, home := o.Box, o.Home
	var b strings.Builder
	fmt.Fprintf(&b, `# Managed by keelbox: regenerated on every apply.
# Personal overrides go in ~/.tmux.conf after the source-file line: what is
# set there replaces what is set here.

set -g mouse on
set -g history-limit 50000
set -g default-terminal "tmux-256color"
set -ga terminal-overrides ",*256col*:Tc"
set -as terminal-features ",xterm*:clipboard:extkeys"
set -g escape-time 10
set -g focus-events on
set -g extended-keys on
set -g set-clipboard on

# Refresh connection variables on reattach. SSH_AUTH_SOCK is deliberately
# absent: agents running in these sessions must never inherit a human's
# forwarded SSH agent.
set -g update-environment "SSH_CONNECTION SSH_CLIENT SSH_TTY DISPLAY"
set-environment -gu SSH_AUTH_SOCK

set -g base-index 1
setw -g pane-base-index 1
set -g renumber-windows on
set -g set-titles on
set -g set-titles-string "%[1]s:#S:#I:#W"
setw -g automatic-rename on

%[3]s
# Click a tab to select it; the + opens a new window. New windows and splits
# inherit the active pane's directory. Clicking the box name on the left opens
# every session and window full screen, as Ctrl-b w does: the status line
# shows only this session's windows, and on a narrow screen not all of them.
bind -Troot MouseDown1StatusLeft choose-tree -Zw
bind -Troot MouseDown1Status if -F '#{==:#{mouse_status_range},window}' {
    select-window
} {
    if -F '#{==:#{mouse_status_range},new}' {
        new-window -c "#{pane_current_path}"
    }
}
bind c new-window -c "#{pane_current_path}"
bind '"' split-window -v -c "#{pane_current_path}"
bind %% split-window -h -c "#{pane_current_path}"
bind r source-file "%[2]s/.tmux.conf" \; display-message "tmux config reloaded"
`, box, home, statusLine(box))
	if o.CSIu {
		b.WriteString("set -g extended-keys-format csi-u\n")
	}
	if o.Welcome {
		b.WriteString(tmuxWelcome)
	}
	if o.Recovery {
		fmt.Fprintf(&b, `
# Resurrect: Prefix Ctrl-s saves, Prefix Ctrl-r restores. Continuum saves
# every 15 minutes and restores when a new tmux server starts.
set -g @continuum-save-interval 15
set -g @continuum-restore on
run-shell "%[1]s/.tmux/plugins/tmux-resurrect/resurrect.tmux"
run-shell "%[1]s/.tmux/plugins/tmux-continuum/continuum.tmux"
`, home)
	}
	return b.String()
}

// LeaveKeys and LeaveHint are the status line's right side: how to leave a
// session, and that leaving stops nothing.
const (
	LeaveKeys = "Ctrl-b d"
	LeaveHint = "leave, everything keeps running"
)

// statusLine is the status line of the agent's sessions: the box name and
// "keelbox" on the left (and the session, when it is not main), how to
// leave on the right. Terminals under 80 columns get the keys and "leave"
// only, and the + that opens a window is drawn from 100 columns, so the
// window list keeps its room. Commas inside #{?...} are written "#,".
func statusLine(box string) string {
	c := ui.NightStatusBar()
	return strings.NewReplacer(
		"{box}", box, "{ground}", c.Ground, "{name}", c.Name, "{text}", c.Text, "{here}", c.Here, "{rule}", c.Rule,
		"{keys}", LeaveKeys, "{hint}", strings.ReplaceAll(LeaveHint, ",", "#,"), "{short}", strings.SplitN(LeaveHint, ",", 2)[0],
	).Replace(`# The status line says where you are and how to leave. The colours are
# keelbox's (Night Chart) on the bar's own dark ground; a terminal with 256
# colours gets the nearest of them. To change it, set status-left or
# status-right in ~/.tmux.conf after the source-file line; "set -g status off"
# there hides the line.
set -g status-position bottom
set -g status-style "bg={ground},fg={text}"
set -g status-left "#[fg={name},bold] {box} #[fg={text},nobold]keelbox#{?#{==:#{session_name},main},, #[fg={rule}]· #[fg={text}]#{session_name}} "
set -g status-left-length 60
set -g status-right "#{?#{e|>=:#{client_width},100},#[range=user|new]#[fg={here}]#[bold] + #[norange]#[nobold],}#[fg={text}] #{?#{e|>=:#{client_width},80},{keys}  {hint},{keys}  {short}} "
set -g status-right-length 60
set -g window-status-separator ""
set -g window-status-format "#[fg={text}] #I:#W#F "
set -g window-status-current-format "#[fg={here},bold] #I:#W#F #[default]"
set -g pane-border-style "fg={rule}"
set -g pane-active-border-style "fg={name}"
set -g message-style "fg={ground},bg={name},bold"
setw -g mode-style "fg={ground},bg={here},bold"
`)
}

// PaneOOMFile sets the default of every user manager on the box for what
// happens to a unit when the kernel's OOM killer stops one of its
// processes. tmux starts each pane in a scope of its own
// (tmux-spawn-*.scope), and systemd's default for a scope is to stop it:
// one test runner stopped for memory would take the agent CLI that started
// it, and everything else in the pane, with it. With continue the process
// the kernel chose is the only one that ends.
const PaneOOMFile = "/etc/systemd/user.conf.d/50-keelbox-oom.conf"

const paneOOMConfig = `# Managed by keelbox. When the kernel stops a process because memory ran
# out, the rest of its unit keeps running: tmux starts each pane as a unit,
# and systemd's default would end everything else in the pane too.
[Manager]
DefaultOOMPolicy=continue
`

func tmuxUnit(home string, recovery bool) string {
	stop := ""
	if recovery {
		// Save only while a server runs: a save without one would replace
		// the last save with an empty one, and the restore would bring
		// back nothing. The watchdog restarts this unit after the server
		// is gone (docs/adr/0039-session-watchdog.md). At a shutdown
		// where keelbox-conversations.service has saved while every pane
		// ran, this save is skipped: it would miss panes systemd has
		// stopped, and could replace that save (docs/adr/0096).
		stop = "ExecStop=-/bin/sh -c '[ -e " + watch.ShutdownSavedPath + " ] && exit 0; tmux has-session 2>/dev/null || exit 0; exec " + home + "/.tmux/plugins/tmux-resurrect/scripts/save.sh'\n"
	}
	return fmt.Sprintf(`# Managed by keelbox. Starts the tmux server at boot (user lingering is on)
# so tmux-continuum restores the last saved sessions.
[Unit]
Description=keelbox tmux server

[Service]
Type=oneshot
RemainAfterExit=yes
# At boot, tmux 3.4 (Ubuntu 24.04) can report failure while Continuum
# restores sessions, though the server is up: accept an existing session,
# and retry rather than leave the box without one.
ExecStart=/bin/sh -c 'tmux has-session -t main 2>/dev/null || tmux new-session -d -s main || { sleep 1; tmux has-session -t main; }'
Restart=on-failure
RestartSec=3
%sExecStop=-/usr/bin/tmux kill-server

[Install]
WantedBy=default.target
`, stop)
}

func (s Sessions) Steps(e *engine.Env) []engine.Step {
	u := e.AgentUser()
	conf := u.Home + "/.config/tmux/keelbox.conf"
	recovery := e.Cfg.TmuxRecovery()
	// The host module has installed tmux by now; Steps has no context of its own.
	opts := TmuxOptions{Box: e.Cfg.Box, Home: u.Home, Recovery: recovery, CSIu: tmuxAtLeast(context.Background(), e, 3, 5), Welcome: e.Cfg.SessionWelcome()}
	// One block in ~/.bashrc: the login hint, and the welcome unless it is
	// turned off, so turning it off takes the lines out again.
	shell := SessionHint
	if opts.Welcome {
		shell += "\n\n" + SessionWelcome(WelcomeOptions{Box: e.Cfg.Box, Agent: u.Name, Projects: e.Cfg.ProjectsDir(), Sudo: e.Cfg.AgentSudo()})
	}
	base := opts
	base.Recovery = false
	var steps []engine.Step
	if recovery {
		steps = append(steps,
			pluginStep(u.Name, "tmux-resurrect", "https://github.com/tmux-plugins/tmux-resurrect.git", u.Home+"/.tmux/plugins/tmux-resurrect", e.M.Tmux.Resurrect),
			pluginStep(u.Name, "tmux-continuum", "https://github.com/tmux-plugins/tmux-continuum.git", u.Home+"/.tmux/plugins/tmux-continuum", e.M.Tmux.Continuum),
		)
	}
	steps = append(steps,
		// The running manager reads it at a reload, which restarts nothing,
		// and the panes that are open take the new default with it.
		engine.FileStep("pane-oom-policy", engine.File{Path: PaneOOMFile, Content: []byte(paneOOMConfig), Mode: 0o644, OnChange: agentDaemonReload}),
		engine.FileStep("tmux-config", engine.File{
			Path: conf, Content: []byte(TmuxConfig(opts)), Mode: 0o644, Owner: u.Name,
			Validate: validateTmux(u, TmuxConfig(base)),
			// A server that is already running keeps the old status line
			// and hooks until it reads the config again. Reading it ends no
			// session and restarts nothing; with no server there is nothing
			// to do, and a failure here is not a reason to fail the apply.
			OnChange: func(ctx context.Context, e *engine.Env) error {
				if system.Succeeds(ctx, e.X, system.As(u.Name, "tmux", "has-session")) {
					_, _ = e.X.Run(ctx, system.As(u.Name, "tmux", "source-file", conf))
				}
				return nil
			},
		}),
		engine.LineStep("tmux-source", u.Home+"/.tmux.conf", "source-file -q "+conf, 0o644, u.Name),
		engine.BlockStep("tmux-bashrc", engine.Block{Path: u.Home + "/.bashrc", ID: "sessions", Content: shell, Mode: 0o644, Owner: u.Name}),
	)
	if e.Cfg.TmuxStartOnBoot() {
		steps = append(steps,
			// A change only reloads the unit: restarting it would end every
			// session the agent has open. The new text applies at the next
			// start (boot, or the watchdog after the server is gone).
			engine.FileStep("tmux-boot-unit", engine.File{
				Path: u.Home + "/.config/systemd/user/" + tmuxService, Content: []byte(tmuxUnit(u.Home, recovery)), Mode: 0o644, Owner: u.Name,
				OnChange: func(ctx context.Context, e *engine.Env) error { return reloadUserManager(ctx, e, u) },
			}),
			userUnitStep("tmux-boot-enabled", u, tmuxService),
		)
	}
	return steps
}

// validateTmux loads the config (without plugins, so Continuum cannot
// restore real sessions) into a throwaway server on its own socket.
// source-file exits non-zero on any line tmux rejects, which a plain
// new-session -f does not.
func validateTmux(u system.User, base string) func(context.Context, *engine.Env) error {
	return func(ctx context.Context, e *engine.Env) error {
		tmp := u.Home + "/.config/tmux/.keelbox-check.conf"
		if err := e.FS(u.Name).WriteFile(tmp, []byte(base), 0o600, u.Name, ""); err != nil {
			return err
		}
		defer func() { _ = e.FS(u.Name).Remove(tmp) }()
		sock := "keelbox-check"
		_, err := e.X.Run(ctx, system.As(u.Name, "tmux", "-L", sock, "-f", "/dev/null", "start-server", ";", "source-file", tmp))
		_, _ = e.X.Run(ctx, system.As(u.Name, "tmux", "-L", sock, "kill-server"))
		if err != nil {
			return fmt.Errorf("tmux rejected the generated config: %w", err)
		}
		return nil
	}
}

// tmuxAtLeast reports whether the installed tmux is at least major.minor.
// tmux -V prints "tmux 3.4" or "tmux 3.5a"; an unreadable version is false,
// which only leaves out options newer tmux understands.
func tmuxAtLeast(ctx context.Context, e *engine.Env, major, minor int) bool {
	out, err := system.Output(ctx, e.X, system.C("tmux", "-V"))
	if err != nil {
		return false
	}
	var maj, min int
	if _, err := fmt.Sscanf(strings.TrimPrefix(out, "tmux "), "%d.%d", &maj, &min); err != nil {
		return false
	}
	return maj > major || (maj == major && min >= minor)
}

func pluginStep(user, name, repo, dir, commit string) engine.Step {
	return engine.Step{
		Name: name,
		User: user,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			head, err := system.Output(ctx, e.X, system.As(e.Cfg.Users.Agent.Name, "git", "-C", dir, "rev-parse", "HEAD"))
			if err != nil {
				return engine.Pending("clone %s at %s", name, commit[:7]), nil
			}
			if head != commit {
				return engine.Pending("move %s to %s", name, commit[:7]), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			user := e.Cfg.Users.Agent.Name
			if st, _ := e.FS(user).Stat(dir + "/.git"); !st.Exists {
				if st, _ := e.FS(user).Stat(dir); st.Exists {
					return fmt.Errorf("%s exists but is not a git checkout; move it aside", dir)
				}
				if _, err := e.X.Run(ctx, system.As(user, "git", "clone", "--quiet", repo, dir)); err != nil {
					return err
				}
			} else if _, err := e.X.Run(ctx, system.As(user, "git", "-C", dir, "fetch", "--quiet", "origin")); err != nil {
				return err
			}
			_, err := e.X.Run(ctx, system.As(user, "git", "-C", dir, "-c", "advice.detachedHead=false", "checkout", "--quiet", commit))
			return err
		},
	}
}

func (Sessions) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	var out []engine.Finding
	u := e.AgentUser()
	if v, err := system.Output(ctx, e.X, system.C("tmux", "-V")); err != nil {
		out = append(out, fail("sessions", "tmux", "tmux not installed"))
	} else {
		out = append(out, ok("sessions", "tmux", v))
	}
	if st, _ := e.FS(u.Name).Stat(u.Home + "/.config/tmux/keelbox.conf"); st.Exists {
		out = append(out, ok("sessions", "tmux-config", "managed config present"))
	} else {
		out = append(out, fail("sessions", "tmux-config", "managed tmux config missing"))
	}
	if e.Cfg.TmuxStartOnBoot() {
		if system.Succeeds(ctx, e.X, userctl(u.Name, "is-active", "--quiet", tmuxService)) {
			out = append(out, ok("sessions", "tmux-server", tmuxService+" active"))
		} else {
			out = append(out, fail("sessions", "tmux-server", tmuxService+" not active for "+u.Name))
		}
	}
	return out
}
