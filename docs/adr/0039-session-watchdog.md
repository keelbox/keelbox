# 0039: The watch run brings back a dead tmux server and a stopped Remote Control

Date: 2026-11-07. Status: accepted. Builds on ADR 0014, 0023 and 0030.

## Context
The agent works in tmux, and optionally behind Claude Code Remote Control. Both die silently. `keelbox-tmux.service` is a oneshot that stays active after the server it started is gone (the agent ran `tmux kill-server`, an out-of-memory kill, a crash), so systemd never notices. Remote Control in server mode exits after about ten minutes without network (its exit code is not documented), and systemd stops restarting a unit after six failed starts an hour. Either way the owner finds out the next time they look.

Two facts shape the fix:
- tmux-resurrect's `save.sh` run without a tmux server writes an empty save and points `last` at it, so the next restore brings back nothing. A stop of `keelbox-tmux.service` that ran it unconditionally would, on a restart after the server was gone, throw away the sessions the restart was meant to restore.
- Restarting `keelbox-tmux.service` when apply rewrites it would end every session the agent has open.

## Decision
- **One watchdog, in the watch run.** Every watch run (ADR 0030, every two minutes) checks the agent's tmux server (`tmux list-sessions` as the agent) when `sessions.start_on_boot` installs the unit, and Remote Control's unit when it is enabled. `sessions.watchdog: false` turns it off. It acts only on a unit that is down. It never stops, restarts or signals a running tmux server or Remote Control server, so it never ends an agent's work.
- **tmux.** Server gone: `systemctl --user -M agent@ restart keelbox-tmux.service` (after starting the agent's user manager if needed), then `tmux has-session` as the agent to check. Continuum restores the last save when the server starts. The unit's stop saves only while a server runs (`tmux has-session || exit 0`), and a change to the unit's text reloads the user manager without restarting the unit: the new text applies at the next start.
- **Remote Control.** The unit gets `Restart=always` (the offline exit code may be 0), `RestartSec=30` with `RestartSteps=5` and `RestartMaxDelaySec=10min` (systemd 254 and later; Ubuntu 24.04 has 255), and `KillSignal=SIGINT`, because a Ctrl+C stop leaves the server's sessions restorable for about four hours. The start limit stays at six an hour. The watchdog, for a unit that is not running:
  - not signed in (`claude auth status` exit code): nothing; that is how it waits for `keelbox login`.
  - no TCP route to `api.anthropic.com:443` within 3 seconds, as root: nothing, and it records `offline since`. When the route is back, it starts the unit.
  - failed (systemd gave up): after 15 minutes, `reset-failed` and `start`, at most 4 times a day. Then it says so once and stops until an apply or a sign-in starts the unit again.
  - stopped without failing: `start`.
- **Budget.** At most 3 restarts an hour per unit. The fourth time it is found down, the watchdog raises one `watch-gave-up` (urgent, with the fix) and then tries once an hour. A unit that has then run for an hour without a restart raises `watch-recovered` and gets its budget back; for Remote Control, a running unit after a give-up means an apply or `keelbox login` started it, so the budget comes back at once.
- **Records.** Every restart is a logbook record of kind `watch` (step `tmux` or `remote-control`, result `ok` or `failed`) and, when it worked, a `watch-recovered` event (info, key `watch:<check>`). The state (restart times, give-up, offline since, the last action) is in `/var/lib/keelbox/watch/state.json`; `keelbox status` shows the last action and the snapshot the sessions.

## Consequences
- A tmux server the agent kills comes back within about two minutes, with the sessions of the last save; programs that were running in it are not restarted. The agent can make it die repeatedly: that costs 3 restarts an hour, then one an hour, and one alert.
- The watchdog runs only `systemctl --user` verbs on keelbox's two units, through the agent's own user manager, and `tmux` and `claude auth status` as the agent. It never runs a command line the agent controls as root.
- keelbox does not detect a Remote Control server that runs but is silently disconnected.
- Remote Control's live behaviour (the offline exit, a restart with SIGINT bringing sessions back) needs a claude.ai sign-in and is not in the e2e; sim tests cover every branch, and the e2e covers the tmux path end to end.
