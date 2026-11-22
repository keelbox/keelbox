# 0088: The agent CLIs are the vendors' own installs

Date: 2026-11-19. Status: accepted. Supersedes the `install` row of [ADR 0004](0004-release-manifest.md).

## Context
keelbox put Claude Code and Codex in a root-owned store at versions it chose, with each CLI's own updater off, and moved those versions between releases through a signed index. Keeping that working meant keelbox ran a version feed: testing each vendor release, signing and publishing an index, and answering for a box that falls behind. A box whose feed stops stays at its release's pins while the vendors move on, and a CLI's own updater cannot help: `codex update` fails with "Could not detect the Codex installation method".

Such a store also protects less than it seems to. The agent owns `~/.claude` and `~/.codex`, their settings and hooks, and its own PATH, so a root-owned `claude` binary does not stop the agent changing what Claude Code does. The limits that matter are root's managed settings in `/etc` (ADR 0033) and the account's own limits, and those do not depend on where the binary lives.

keelbox's aim is to set a machine up quickly with sensible limits, then leave the person to run it. Choosing which agent CLI versions people may use is the vendors' job and the person's, not keelbox's.

## Decision
- **Each CLI is installed with its vendor's installer**, as the agent, into the agent's home (`~/.local/bin`), at the vendor's current release. apply checks that the CLI is on the agent's PATH and runs (`--version`), installs it again at the vendor's current release when it does not, and never moves the version of one that runs.
- **The CLIs update as on any computer.** Claude Code updates itself in the background, and Codex offers an update when it starts. `claude update` and `codex update` work. Turning updates off, or pinning a version, is done with the CLI's own settings.
- **The release manifest carries no Claude Code or Codex pins.**
- **apply removes what the agent store left**: the `keelbox-agents.timer` and `keelbox-agents.service` units, the `keelbox-smoke` account, and `/opt/keelbox/agents`, `/usr/local/lib/keelbox/agents`, `/var/lib/keelbox/agents` and `/var/lib/keelbox/smoke`. It removes the directories only once every configured CLI is on the agent's PATH, so a box whose installer download fails keeps the old binaries. A config with an `agent_updates` key is refused, with the fix: delete the block.
- **Tests:** the end-to-end run installs the vendors' current releases, so it tests whatever a new box would get.

## Consequences
- A vendor release that breaks something reaches boxes as it reaches laptops. The fix is the vendor's: an update, or the CLI's own way to install an older version.
- The agent can change its own CLIs, as it can change their settings. Doctor reports each CLI's version and fails one that does not run.
- A keelbox release does not change the agent CLIs' versions.
