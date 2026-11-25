# 0097: The doctor's sandbox check mounts /proc, as the agents' sandboxes do

Date: 2026-11-25. Status: accepted. Amends ADR 0059 (agent sandboxes and AppArmor).

## Context
ADR 0059 has doctor run `bwrap --ro-bind / / --dev /dev --unshare-net -- /usr/bin/true` as the agent. Claude Code's and Codex's sandboxes also mount a fresh `/proc` in a PID namespace of their own (`--proc /proc --unshare-pid`). An unprivileged LXC or LXD container refuses that mount: in one on Ubuntu 24.04 the check above passed, and with `--proc /proc --unshare-pid` bwrap failed with "Can't mount proc on /newroot/proc: Operation not permitted". Doctor said the sandbox worked where the agents' sandboxes could not start.

## Decision
Doctor's sandbox check runs `bwrap --ro-bind / / --dev /dev --proc /proc --unshare-pid --unshare-net -- /usr/bin/true` as the agent. It fails with bwrap's reason and the same fix as before.

## Consequences
- On a box where the agents' sandboxes cannot mount `/proc`, doctor fails the sandbox check instead of passing it.
- On a VM or a bare machine, nothing changes: the mount works there.
