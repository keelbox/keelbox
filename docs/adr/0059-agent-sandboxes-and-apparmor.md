# 0059: The agent CLIs' sandboxes under AppArmor's user namespace restriction

Date: 2026-11-09. Status: accepted. Builds on ADR 0005 (security defaults).

## Context
Without this decision, Codex on Ubuntu 24.04 (`ubuntu-24.04`, keelbox's default Hetzner image) could not run a command or write a file in workspace-write mode: `bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted`. As an unprivileged account:

- Codex 0.166 runs `/usr/bin/bwrap` when it is installed (it probes `bwrap --help`), else the copy it bundles in `codex-resources/bwrap`. keelbox installs `bubblewrap`, so Codex uses the system one. Claude Code's sandbox also runs bwrap from PATH, and needs `socat` for its network bridge.
- Ubuntu sets `kernel.apparmor_restrict_unprivileged_userns = 1` from 24.04. An unconfined program then gets a user namespace without capabilities in it: bwrap cannot write its uid map, and with `--unshare-net` cannot bring up loopback.
- Ubuntu 26.04 (apparmor 5.0) ships `/etc/apparmor.d/bwrap-userns-restrict`, a profile for `/usr/bin/bwrap` that allows userns and stacks everything bwrap runs with `unpriv_bwrap`, which denies all capabilities. There the system bwrap works and Codex's sandbox runs. Ubuntu 24.04 (apparmor 4.0.1) does not ship it, and Codex's bundled bwrap fails on both releases, having no profile.

Options: set the sysctl to 0 (every program the agent runs gets a capable user namespace, the attack surface the restriction exists to close); a profile with `flags=(unconfined)` and `userns,` for bwrap (what Ubuntu does for Chrome and others: bwrap and everything it starts keep a capable user namespace); or Ubuntu's own profile from 26.04.

## Decision
- **Ubuntu's own profile, only where it is missing.** Where `kernel.apparmor_restrict_unprivileged_userns` is 1 and `/etc/apparmor.d/bwrap-userns-restrict` is absent, the host module writes Ubuntu 26.04's profile, for the 4.0 ABI, as `/etc/apparmor.d/keelbox-bwrap` and loads it with `apparmor_parser --replace --write-cache`; a file the parser refuses is rolled back. It attaches to `/usr/bin/bwrap` alone, not to Codex's bundled copy or any path the agent can write. The profile names are Ubuntu's (`bwrap`, `unpriv_bwrap`), so after a release upgrade brings Ubuntu's file, apply removes keelbox's and reloads Ubuntu's, never unloading bwrap's profile in between.
- **The restriction stays on.** keelbox never changes the sysctl.
- **socat** joins the base packages for Claude Code's sandbox.
- **Checked as the agent.** Doctor runs `bwrap --ro-bind / / --dev /dev --unshare-net -- /usr/bin/true` as the agent when any agent CLI is configured, and Codex's own `codex sandbox -- /usr/bin/true` when Codex is; each fails with bwrap's reason and the fix. The end-to-end test runs both on 24.04 and 26.04, has Codex's sandbox run a command and write a file in workspace-write mode, checks a program bwrap starts has no capabilities and cannot map a user namespace, and runs Anthropic's sandbox runtime, the library behind Claude Code's sandbox, with its network bridge (see below for its Unix socket filter).

## Consequences
- Codex's sandbox works on 24.04 boxes as on 26.04, and Claude Code's as far as it does on 26.04, under the AppArmor policy Ubuntu itself chose for 26.04.
- The agent can run bwrap itself and so reach the kernel code that bwrap's namespace, mount and network setup uses, with arguments it chooses; programs inside get no capabilities. The security model says so.
- Claude Code's sandbox, which is off by default, does not run with its defaults under this profile, on 24.04 as on stock 26.04: its Unix socket filter (`apply-seccomp`) makes a nested user namespace inside bwrap and needs capabilities there, which `unpriv_bwrap` denies. With `sandbox.network.allowAllUnixSockets` it runs. keelbox leaves that setting to the user: it lets sandboxed commands open the agent's Unix sockets, including its rootless Docker socket. A profile that let programs inside bwrap keep capabilities would undo the point of `unpriv_bwrap` for every bwrap user.
- If Codex stops using the system bwrap (for example when it is missing), its sandbox fails again; doctor's `codex-sandbox` check shows it.
- A live Codex turn and a live Claude Code sandboxed command need a signed-in account and are not tested; the e2e drives the same sandboxes through `codex sandbox` and the sandbox runtime.
