# 0010: Box upgrades, the downgrade guard and rollback

Date: 2026-11-06. Status: accepted.

## Context
A box's tool versions come from the release manifest embedded in whichever keelbox applies it (ADR 0004), and exact pins converge up or down. If every remote command, `doctor` included, also replaced `/usr/local/bin/keelbox` with the laptop's binary, two laptops on different versions would flip the box between releases, and an old laptop would silently downgrade it.

## Decision
- **Upgrade is apply.** A newer keelbox carries a newer manifest; `keelbox apply` converges the box to it. The logbook record carries `from_release` whenever an apply moves the box, so upgrades are visible in the box's history.
- **Only apply installs keelbox.** Remote commands run the uploaded binary from the stage (`/var/lib/keelbox/stage/keelbox`; `/run` is mounted noexec). The `keelbox-binary` step of an apply installs the running binary as `/usr/local/bin/keelbox` and keeps the one it replaced as `/var/lib/keelbox/keelbox.previous`.
- **Downgrade guard.** Apply refuses when its release is older than the release of the box's last converged apply (result `ok` or `partial`), naming both and the keelbox that applied the newer one. `--allow-downgrade` overrides.
- **Rollback.** `keelbox rollback` runs the previous binary's `apply --allow-downgrade` against the applied config `/etc/keelbox/keelbox.yaml`. That binary installs itself and keeps the newer one as the previous, so rolling back twice returns to the newer release.

## What rollback does not undo
- Agent CLIs (the vendors' installs update themselves, ADR 0088).
- Docker within its series (a downgrade inside the series is not attempted).
- Config fields the older release does not know: its strict parser rejects them. Remove them from the config first.

The laptop is still the source of truth: its next `keelbox apply` moves the box forward again. To stay on the older release, apply from the older keelbox.
