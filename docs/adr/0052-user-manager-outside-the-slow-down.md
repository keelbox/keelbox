# 0052: The agent's user manager outside the memory slow-down

Date: 2026-11-07. Status: accepted. Amends ADR 0048.

## Context
ADR 0048 put `MemoryHigh` and `MemoryMax` on `user-UID.slice`, the agent's whole account. Past `memory.high` the kernel reclaims and holds back every allocation in that cgroup's subtree, and that subtree included the agent's user manager (`user@UID.service/init.scope`) and its D-Bus (`session.slice`). On a machine with other accounts, with the agent's memory over the mark, `systemctl --user -M agent@ ...` took minutes: the watchdog, doctor and apply all talk to that manager. `MemoryLow` or `MemoryMin` on `user@UID.service` would not help: they protect from reclaim, not from the allocation penalty, and they would cover the agent's services inside it too.

## Decision
- **Hard limits stay on the account.** `CPUQuota`, the weights, `MemoryMax` and `MemorySwapMax` remain on `user-UID.slice`: everything the agent runs (SSH sessions, tmux, Remote Control, rootless Docker and its containers, the agent CLIs) stays under them, and the kernel's OOM killer acts only there.
- **The slow-down mark moves to the work slices.** `MemoryHigh` (90% of the ceiling, in bytes of the RAM at apply time) goes on the agent's user manager's `app.slice` (its services: tmux, Remote Control, rootless Docker), `background.slice`, and `user.slice` (where rootless Docker can put containers). `session.slice` and `init.scope` get none.
- **Only the agent's manager reads them.** A system drop-in for `user@UID.service` sets `XDG_CONFIG_DIRS=/etc/keelbox/agent-user:/etc/xdg`; the drop-ins are root-owned in `/etc/keelbox/agent-user/systemd/user/`. Other accounts' user managers, the owner's included, read nothing new. A manager reads `XDG_CONFIG_DIRS` when it starts, and restarting it would end every session, so a manager that is running already gets the same mark with `systemctl --user set-property --runtime` until its next start (the next boot). Without a memory limit keelbox removes its files and lifts a runtime mark it set; a mark of the agent's own stays.
- **Seen.** Doctor reads the mark from `app.slice` and fails when the agent's services run without it. The high-event count is still read from the account's slice, where the kernel counts its descendants' events.

## Consequences
- The agent can raise its own slow-down mark with a drop-in in `~/.config/systemd/user`, which takes precedence; it cannot raise the ceiling. A higher mark means it reaches the ceiling, and the OOM killer, sooner.
- Processes in the agent's SSH sessions (outside tmux) have the ceiling but no slow-down mark: logind makes their scopes, outside the user manager.
- `XDG_CONFIG_DIRS` is in the environment of every service the agent's manager starts; it adds a directory holding only the drop-ins.
- An end-to-end test checks that `systemctl --user -M agent@ is-active` answers within 5 seconds throughout the memory stress.
