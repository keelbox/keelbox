# 0075: A session's welcome is marked by tmux and printed by the shell

Date: 2026-11-15. Status: accepted.

## Context
`keelbox attach` puts a person in the agent's tmux session. Someone who does not know tmux sees a bare prompt: nothing says which machine this is, how to start an agent, how to leave without stopping it, or how to get root when the agent has none. The status line answers "where" and "how to leave" all the time. A few lines of welcome at the start of a session answer the rest.

The welcome has to appear once for each new session, in one place. It must not appear in every window, pane or shell, in a shell that is not interactive (`ssh agent@box command`, `scp`, the commands agent CLIs run), or in Remote Control's sessions. And sessions come to be in several ways:

- `keelbox attach` creates one when it is missing (`tmux new-session -A`).
- A phone user types `tmux new -A -s main` over plain ssh.
- `keelbox-tmux.service` creates `main` at boot, detached. With the defaults this is the session everyone first attaches to.
- tmux-resurrect creates the saved sessions again after a reboot.
- On a box's first apply, the sessions module starts that service, and so `main`, before the logbook module installs `/usr/local/bin/keelbox` and long before the agents module installs the CLIs.

Options considered:

1. **A command in the session's first window, given by `keelbox attach`.** tmux ignores the command when the session exists, and `main` exists from boot, so such a line is almost never shown, and plain ssh never gets it.
2. **`default-command` in the tmux config.** It runs for every pane, changes how every pane's shell starts (no login shell unless the wrapper makes one) and is the first thing a user's own config replaces.
3. **A snippet in the shell start-up with a file of its own as the "shown" record.** A shell cannot tell from a file whether it is its session's first pane, or whether the session is new or restored.
4. **tmux marks the pane, the shell prints.** A `session-created` hook knows the new session and its first pane; the shell in that pane is where text belongs.
5. **Printing at attach by typing into the pane** (`send-keys`) or writing to its terminal. It types into whatever runs there, or leaves the prompt overwritten.

## Decision
- **The mark.** The tmux config sets a `session-created` hook (index 90) that stores the first pane's id in the session option `@keelbox-welcome`.
- **The print.** The `sessions` block in the agent's `~/.bashrc`, which sits after Ubuntu's interactive guard, asks tmux for the mark when it runs inside tmux on a terminal. In the marked pane, and only there, and while the mark is fresh (see the consequences), it runs `keelbox welcome` and clears the mark. Every other interactive shell in tmux pays that one `tmux display-message` and prints nothing.
- **The text** comes from the keelbox binary, in the CLI's styles, so there is one place that knows how keelbox writes to a terminal. The block gives it what only the applied config knows (the box's name, the agent's name, the projects directory, `users.agent.sudo`); it reads the installed agent CLIs, the system's name and the account's groups itself. It uses the 16 colours of the terminal's own theme, because the pane may be light or dark and asking the terminal is a round trip a shell start-up should not make. It prints nothing, and exits 0, without a terminal, with `TERM=dumb` or under 36 columns. It is not in `keelbox help`.
- **The first apply.** A shell in the marked pane that cannot run the command (no binary yet) sets the mark to `retry` and the pane's id. A `client-attached` hook then sets the mark to `again` and the id and starts that pane's shell again (`respawn-pane -k`), so it prints the welcome with the agent CLIs that are installed by then. It does so only when the mark says `retry` for the attaching client's pane, the pane's program is bash, its cursor is on the first row and it has no history: a shell at its first prompt that has printed nothing else.
- **`keelbox attach` adds no line of its own.** It runs `tmux new-session -A -s NAME` and nothing else.
- **`sessions.welcome: false`** takes the hooks out of the tmux config and the lines out of the block. The status line stays.

## Consequences
- The welcome appears however a new session was created: by `keelbox attach`, over plain ssh, or by `keelbox-tmux.service` (`main` on a new box, and at any boot without a save to restore).
- Sessions restored at boot do not show the welcome again. tmux-continuum starts the restore from the config, and tmux runs hooks only when the config has finished loading, which waits for the restore: the marks arrive after the restored sessions' shells have started. Because a late mark would otherwise be acted on by whatever shell started in that pane next, the shell acts on a new session's mark only for 30 seconds after the session began and drops an older one. A slow shell start (over 30 seconds) therefore gets no welcome, and a restore run by hand in a running server may show it once in each session it creates.
- Restarting a shell is something keelbox does nowhere else. Here it is tmux, at an attach, under the conditions above. What can be lost is a line typed but not entered, or background jobs started in a session that nobody had attached to and whose screen was then cleared. A shell that has run anything visible, or a pane running another program, is left alone; the end-to-end test covers the first attach on a new box, and both cases were checked against tmux 3.4 and 3.6.
- A box whose keelbox does not know the command (after `keelbox rollback` to an older one) restarts an untouched first shell at each attach and prints nothing.
- `set-hook -g session-created` or `set-hook -g client-attached` in the user's `~/.tmux.conf` replaces keelbox's hook, and the welcome with it. `set-hook -ga` adds to it. The sessions guide says so.
- A tmux server that was running before the apply that wrote the hooks has none until it reads the config again. Apply has a running server do that (`tmux source-file` as the agent) whenever it changes keelbox's tmux config; it ends no session, and a failure to reload does not fail the apply.
- The status line and the welcome are drawn by the agent's own tmux and shell from files the agent can change. They are help for a person, not a check; the security model says so.
