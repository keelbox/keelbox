# 0081: A pane keeps running when the kernel stops one of its processes for memory

Date: 2026-11-16. Status: accepted. Builds on [ADR 0048](0048-agent-limits.md).

## Context
An agent runs three sub-agents, a Docker stack and a dev server in one tmux pane on a box with 16 GB. When memory runs out, the kernel's OOM killer stops one process, and without more the whole session goes: the agent CLI, its sub-agents and the dev server.

The kernel stops one process; systemd ends the rest. tmux starts every pane in a systemd scope of its own, `tmux-spawn-*.scope`, in the agent's user manager: tmux 3.4 on Ubuntu 24.04 and tmux 3.6 on Ubuntu 26.04 both do. systemd's default for a unit, `OOMPolicy=stop`, ends the unit when the OOM killer stops any process in it: every other process in the pane gets SIGTERM, and SIGKILL 90 seconds later. With one pane, the last session closes and the tmux server exits.

ADR 0048 set `OOMPolicy=continue` on `keelbox-tmux.service` and the Remote Control service, and only with `users.agent.limits`. The panes are not in that service, so it does not cover them, with limits or without.

## Decision
- **keelbox writes `/etc/systemd/user.conf.d/50-keelbox-oom.conf` with `DefaultOOMPolicy=continue`**, on every box with tmux, whether or not the agent has limits. The process the kernel chose ends, and the rest of its pane keeps running.
- **The agent's user manager reads it at a `daemon-reload`.** Nothing restarts. Panes that are open take the new default at the reload too.
- **It is the default of every user manager on the box**, the admin's included: systemd reads that directory for all of them. A unit that sets its own `OOMPolicy` keeps it.
- **Nothing else about memory changes.** The agent's memory ceiling is ADR 0082's.

## Consequences
- A process that depended on the one the kernel stopped keeps running without it. The agent CLI sees a command that was killed and carries on.
- Without a ceiling (`memory: none`), a box that runs out of memory stalls before the kernel acts, SSH included, because nothing holds the agent back short of all the RAM. ADR 0082's default ceiling is what does that.
