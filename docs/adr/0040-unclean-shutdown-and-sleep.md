# 0040: An unclean shutdown is recorded and reported; host.power.sleep: never

Date: 2026-11-07. Status: accepted. Builds on ADR 0030 and 0031.

## Context
A box at home is a mini PC or an old laptop. The power goes, the kernel panics, someone holds the power button. The box comes back by itself (BIOS "restore on AC power loss", the tmux unit, Continuum, lingering), but the owner never hears that it went down, nor whether what they left running came back. A laptop used as a box also suspends when its lid closes or a desktop session idles, which takes it off the network until someone opens it.

## Decision
### Unclean shutdown
- `keelbox-boot.service` (root, `Type=oneshot`, `RemainAfterExit=yes`, the default dependencies, `WantedBy=multi-user.target`) starts at boot and stays active. systemd stops it at every orderly shutdown or reboot, before `/var/lib` is unmounted; its `ExecStop` is `touch /var/lib/keelbox/power/clean-shutdown`, which works whatever keelbox is installed by then.
- Its start runs `keelbox boot-mark`, which reads no config (a config that does not load cannot hide a crash), as root:
  - marker present: the last boot ended cleanly; it removes the marker.
  - no state yet (the first start, at the apply that installs it), or the same boot as last time (an apply restarting the unit): nothing to report.
  - otherwise: the last boot ended uncleanly (power loss, a crash or a hard reset; keelbox cannot tell them apart). It writes a logbook record of kind `power` (step `unclean-shutdown`) with when the box went down: within two minutes after the last watch-run heartbeat of that boot.
- `/var/lib/keelbox/power/state.json` (0644 root, directory 0755 root) keeps the boot the unit last started in and the last unclean end found. The agent cannot write either, so it cannot fake or hide one.
- The watch run raises `power-back` (warn) once per unclean end, when the box has been up three minutes, saying what came back: the tmux sessions, Remote Control, Tailscale, and that a missed backup runs (its timer is persistent). The snapshot's `power.unclean` is true for that boot. A clean reboot says nothing.

### Sleep
- `host.power.sleep: never` masks `sleep.target`, `suspend.target`, `hibernate.target` and `hybrid-sleep.target`, which refuses a suspend whoever asks for it, and writes `/etc/systemd/logind.conf.d/60-keelbox.conf` (`HandleLidSwitch`, `HandleLidSwitchExternalPower`, `HandleLidSwitchDocked` and `IdleAction` all `ignore`). Reboots and shutdowns are not affected.
- logind is restarted to read the drop-in only when no display manager runs: restarting it can end graphical sessions. On a desktop the drop-in applies at the next boot; the masked targets refuse a suspend meanwhile.
- `allow`, the default, leaves the machine as it is. Going back from `never` to `allow` unmasks the targets and removes the drop-in only while keelbox's drop-in is there, so masks the owner set themselves are never undone.
- Doctor fails when `never` is set and a target is not masked.

## Consequences
- A crash between the stop and the power going off at the end of a shutdown counts as clean.
