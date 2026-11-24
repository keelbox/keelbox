# 0092: Apply refuses an LXC container without nesting or the TUN device

Date: 2026-11-24. Status: accepted. Follows ADR 0091.

## Context
Since ADR 0091 keelbox sets up an unprivileged LXC container. Two of the container's own settings decide whether parts of the box work, and only its host can change them.

- **Nesting.** Without it, a new user namespace in the container may not mount `/proc`. Rootless Docker and the agent CLIs' sandboxes (bwrap with `--proc`) do that first. On LXD with `security.nesting=false`, apply completed and doctor passed while both failed; bwrap reported `Can't mount proc on /newroot/proc: Operation not permitted`. Whether the refusal comes from the container's AppArmor profile or from the procfs view LXC sets up (lxcfs over parts of `/proc`) was not settled. The probe below tests the behaviour, so it holds either way. LXD and Incus leave nesting off by default.
- **`/dev/net/tun`.** tailscaled cannot start without it. Proxmox does not pass the device to an unprivileged container by default.

## Decision
- **Apply checks both before it changes anything**, on every run, and only in an LXC container (`systemd-detect-virt --container` prints `lxc`).
  - Nesting: `unshare --user --map-root-user --mount --pid --fork --mount-proc true`, the same step rootless Docker and bwrap take. Skipped when nothing on the box needs it: no agents and `runtime.docker: false`.
  - TUN: `[ -c /dev/net/tun ] && : <>/dev/net/tun`, only when Tailscale is on.
  - Neither leaves anything behind. The checks run only read-only commands, which a sim test pins.
- **Either failing stops apply** with exit 1, before the lock and the detached run, and the message ends with the commands for the container's host.
  - A guest API socket (`/dev/lxd` or `/dev/incus`) says LXD or Incus.
  - Without one, the host may be Proxmox, LXD or Incus with that API off, or plain LXC, so both sets are printed. Proxmox's begin with `pct config` because `pct set` replaces the whole features list and a whole `devN` entry.
- **The container's name**, which the LXD and Incus commands need, comes from cloud-init's `local_hostname`, else the guest API's meta-data, else the hostname unless it is already the box's (apply renames the host), else `NAME`, with a note saying what it stands for. Only a plain label is printed into a command.
- **Doctor runs the same checks** as `container-nesting` and `container-tun` under host. With a known LXD or Incus name it shows the commands; otherwise it says that `keelbox apply` prints them.

## Alternatives
- **Warn and carry on, or an override flag.** Rejected. The box would come up without the agents' sandboxes and rootless Docker that keelbox sets up, and nothing would say so until doctor ran, which is what the LXD test found. The fix is a host setting the user can usually change in a minute. Where they cannot, as on some container VPS plans, a VM is the way.

## Consequences
- A box in an LXC container whose host the user cannot change is refused. A box already set up in such a container is refused at its next apply, with the commands.
- On an image without cloud-init and with the guest API off, after the first apply renames the host, the LXD and Incus commands say `NAME`. A container that was named after the box from the start also gets `NAME`, since its hostname cannot be told apart from the renamed one.
- The Proxmox commands come from Proxmox's `pct` options and Tailscale's LXC guide; they were not run on a Proxmox host.
- Other container kinds (Docker, systemd-nspawn, WSL) are not checked; keelbox does not support them.
