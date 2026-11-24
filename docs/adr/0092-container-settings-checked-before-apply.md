# 0092: Apply refuses an LXC container without nesting or the TUN device

Date: 2026-11-24. Status: accepted. Follows ADR 0091.

## Context
Since ADR 0091 keelbox sets up an unprivileged LXC container. Two of the container's own settings decide whether parts of the box work, and only its host can change them. Without nesting, LXC's AppArmor profile refuses to mount `/proc` in a new user namespace, which rootless Docker and the agent CLIs' sandboxes (bwrap) need; a test on LXD with `security.nesting=false` found apply completing and doctor passing while both failed. Without `/dev/net/tun`, tailscaled cannot start; Proxmox does not pass the device to an unprivileged container by default. LXD and Incus leave nesting off by default.

## Decision
- **Apply checks both before it changes anything**, on every run, in an LXC container only (`systemd-detect-virt --container` prints `lxc`). Nesting: `unshare --user --map-root-user --mount --pid --fork --mount-proc true`, the same step rootless Docker and bwrap take. TUN: open `/dev/net/tun` for reading and writing, only when Tailscale is on. Neither leaves anything behind.
- **Either failing stops apply** with exit 1, before the detached run starts, and the message ends with the commands for the container's host: `lxc` or `incus` with the container's name (from cloud-init's `local_hostname`, else the hostname) when the guest API socket says which, otherwise Proxmox's `pct` with `CTID`, which cannot be read inside the container.
- **Doctor runs the same checks** as `container-nesting` and `container-tun` under host, failing with the same commands.

## Consequences
- A box in an LXC container whose host the user cannot change, such as some low-cost container VPS plans, is refused. Apply has no override: such a box would lack the agents' sandboxes and rootless Docker that keelbox sets up, with nothing to say so until doctor ran.
- A box already set up in such a container is refused at its next apply, with the fix.
- Other container kinds (Docker, systemd-nspawn, WSL) are not checked; keelbox does not support them.
