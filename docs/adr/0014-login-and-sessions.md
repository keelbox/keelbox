# 0014: Sign-in and sessions from the laptop

Date: 2026-11-06. Status: accepted.

## Context
After apply, the user still has to sign the box in to Tailscale and each agent CLI, then start work in tmux, from a laptop or a phone. The hard constraint: keelbox never captures, reads, stores or proxies a credential.

## Decision
- **`keelbox login`** walks the sign-ins in order: Tailscale (as the admin, `sudo tailscale up`, only when no auth key file is configured), then as the agent Claude Code (`claude auth login`), Codex (`codex login --device-auth`) and the GitHub CLI (`gh auth login --web`, then `gh auth setup-git`). Each is the vendor's own command, run with `ssh -t` and keelbox's own stdin, stdout and stderr passed to ssh as file descriptors, so the bytes go between the terminal and the box without passing through keelbox. Whether a sign-in is needed comes from the vendor's status command (`claude auth status`, `codex login status`, `gh auth status`, Tailscale's backend state); keelbox uses only the exit code and sends the output to `/dev/null` on the box. `--force` signs in again. It needs a terminal and refuses without one.
- **The GitHub CLI is installed** from GitHub's apt repository, pinned by key fingerprint in the release manifest like Docker and Tailscale (`runtime.gh`, default on). It signs the agent in to its own GitHub account or a fine-grained token, because the agent gets no forwarded SSH agent (ADR 0011).
- **Sessions are tmux, reached as the agent over SSH.** `keelbox attach [--session NAME]` runs `tmux new-session -A -s NAME` (default `main`); `keelbox sessions` lists them; `keelbox ssh [--admin]` opens a login shell. Session names are limited to letters, digits, `.`, `_` and `-`, since tmux treats `:` and `.` in targets specially and the name is passed to a remote shell.
- **Phone:** the same account over SSH to the tailnet name, then `tmux new -A -s main`. An interactive SSH login outside tmux prints the running sessions, which is the first thing a phone user needs; it never attaches by itself, so scripts and `scp` are unaffected.
- **Reaching a Hetzner box by name:** `target.ssh.host` is allowed with provider hetzner. Create still uses the API's address; later commands use the name, which inherits the host keys pinned for the address, so switching to the tailnet name trusts nothing new.

## Consequences
- Login needs the agent's own SSH key in `users.agent`; without one, agent commands say so.
- keelbox cannot tell a user which account a CLI signed in to: it does not read the status output. The user sees it on their own terminal.
- A CLI that changes its login or status command breaks `keelbox login` for that CLI; the end-to-end run, which installs the vendors' current CLIs, is where that shows.
