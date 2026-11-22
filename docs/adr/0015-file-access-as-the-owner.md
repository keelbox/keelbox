# 0015: Files a user can change are touched as that user, never as root

Date: 2026-11-06. Status: accepted.

## Context
apply runs as root and manages files in the agent's and the admin's homes (`~/.ssh/authorized_keys`, `~/.bashrc`, `~/.config/...`, `~/.claude`, `~/.codex`). Creating, chmodding, chowning and writing them as root, with a path walk that follows symlinks in every component, would leave them open to the agent, which owns its home and can change it between applies. For example, the agent could replace `~/.config` with a symlink to `/etc`, and the next apply's `MkdirAll(~/.config/docker, 0700, agent)` would chown `/etc/docker` to the agent. The agent could then replace the system Docker daemon's `daemon.json`, which the root daemon reads at its next start: agent to root. The same walk would let root read a file the agent linked (`~/.bashrc -> /etc/shadow`) and write the content back into a file the agent owns.

Refusing symlinked components with `openat2(RESOLVE_NO_SYMLINKS)` would close the hole for these steps, but it breaks a common, legitimate setup (dotfiles kept in a repository and linked into the home), and every operation and every new step has to get the descriptor handling right.

## Decision
One rule, enforced in the executor (`internal/system`), not per step:

- **A path a non-root account can change is read and written by a process running as that account.** `Executor.UserFS(user)` runs each operation (stat, read, atomic write, mkdir, symlink, remove) as a child process whose UID, primary GID and supplementary groups the kernel sets before exec (`SysProcAttr.Credential`), using fixed scripts over coreutils that take the path only as an argument. Whatever a symlink points at, the operation has exactly that account's rights, so the account gains nothing it could not already do. Reads are capped at 16 MiB and refuse anything but a regular file, so a link to `/dev/zero` or a FIFO cannot exhaust root's keelbox.
- **The engine picks the account from the file's owner.** `Env.FS(owner)` is root's file access for root and `UserFS(owner)` otherwise. `File`, `Block`, `LineStep`, `DirStep` and every module step that touches a home go through it, so a step cannot forget.
- **Root's own file access refuses paths another account could redirect.** Before every root file operation, `CheckRootPath` walks the path, following symlinks as the kernel would, and refuses unless every directory on the way is owned by root and writable by neither group nor others. Within such directories only root can create, rename or replace an entry, so the path checked is the path used. A step that sends a home path through root's access therefore fails closed instead of being exploitable. Paths must be clean (no `..`): `..` resolves against the real directory, not the lexical one.
- The running keelbox is read from `/proc/self/exe`, not from the path it was started from, which may be in the admin's home (`~/.local/bin`).
- Temporary directories made for a user (agent installers) are removed by that user.

## Consequences
- An agent that plants a symlink in its home can make apply fail on its own files (the error names the path and the account). It cannot make apply change anything outside what it can already change. Repairing the home, or removing the link, lets the next apply converge.
- A file in a home that root owns (left by hand) cannot be fixed by apply: it reports the permission error. keelbox never creates such files.
- `access.tailscale.auth_key_file` must be in a directory only root can write; the staged key in `/run/keelbox` is.
- `system.Fake` models symlink resolution and per-account permissions, so sim tests (`TestAgentSymlinksGainNothing`) replay the attacks; `test/e2e` replays the attack on a real box.
