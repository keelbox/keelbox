# 0096: Conversations reopen after any orderly reboot, not only keelbox's

Date: 2026-11-25. Status: accepted. Amends ADR 0090 (reboots for updates).

## Context
ADR 0090 had the watch run note each tmux pane running Claude Code or Codex before it rebooted the box, and type the command that reopens the conversation after the boot. Only keelbox's own reboots took the note: `host.auto_reboot: nightly` or `weekly`, and `keelbox reboot`. With `auto_reboot: "04:00"`, unattended-upgrades reboots the box itself, and keelbox never noted anything. The same held for `sudo reboot`, a cloud console's reboot and a shutdown. tmux-resurrect brought the panes back at a shell, and the conversations stayed closed. The commented config `keelbox init` writes promised they reopen after a `"04:00"` reboot too. An owner's box set to `"04:00"` lost its conversation that way.

## Decision
- **A unit takes the note at every orderly shutdown.** `keelbox-conversations.service` does nothing at start and stays active. Its stop runs `keelbox note-conversations`, which writes the same note as before. A drop-in for `user@.service` orders every user manager before the unit, so systemd stops the unit first, while the agent's tmux server still runs. keelbox's own reboots keep taking the note before they reboot, as before.
- **At shutdown, tmux is asked without a scope.** keelbox runs the agent's commands in a scope in the agent's slice, so its limits cover them. systemd refuses to start a scope while the box shuts down, so this one command runs without it; it lists panes and starts nothing.
- **A note is never lost to a failed look.** When tmux cannot be asked, the last note stays: keelbox's own reboots take theirs first. A note from an earlier boot that no watch run has replayed yet also stays when no pane runs an agent CLI: the box went down again before the panes were resumed.
- **The note is taken only while the box shuts down.** `systemctl is-system-running` must say `stopping`. A stop within a boot, such as an apply that restarts the unit, leaves the last note alone, so a later crash cannot reopen conversations from long ago. A keelbox too old to know the command fails the stop, and systemd ignores the failure.
- **The time to resume runs from the boot, not from the note.** The panes come back within minutes of the boot, so the watch runs keep trying for 20 minutes after it. A box that was shut down for days reopens its conversations when it starts again. A soft reboot (`systemctl soft-reboot`) keeps the boot's id and time, so a tmux server started after the note counts as a reboot too, and the 20 minutes run from its start.
- **The unit takes no lock.** At shutdown a watch run may hold the day-two lock, and a skipped note would lose the conversations. It writes only the note, which the watch run reads.

## Consequences
- `auto_reboot: "04:00"`, `sudo reboot`, a soft reboot, a cloud console's reboot and a shutdown reopen the conversations as keelbox's own reboots do. A crash or power loss still does not: nothing runs before it.
- A real-machine test covers it: the full e2e run starts a stand-in for Claude Code in tmux, reboots with `systemctl reboot`, and checks the conversation's command was typed after the boot.
- The `"04:00"` setting still reboots whatever is running. The resume is what changes.
