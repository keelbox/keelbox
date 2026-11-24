# 0091: Root's path check accepts /proc and /sys owned by nobody, for unprivileged containers

Date: 2026-11-24. Status: accepted. Amends ADR 0015 (root's file calls).

## Context
ADR 0015 has root refuse a file call unless every directory on the way is owned by root and not writable by group or others, so that no other account can redirect it. In an unprivileged container (LXC, LXD, Proxmox's default), `/proc` and `/sys` belong to the host's root, which is not mapped into the container: the container sees them as owned by the overflow user, `nobody`. Apply refused `/proc/sys/kernel/apparmor_restrict_unprivileged_userns` and then `/proc/self/exe`, and stopped, on every such container, with nesting on or off.

## Decision
- **A directory owned by `nobody` passes the check when it is `/proc`, `/sys` or under them**, except the cgroup tree `/sys/fs/cgroup`, where systemd delegates subtrees to accounts and they can make directories. The kernel lays out these trees; no account can create, rename or replace a directory in them, and the container's `nobody` account cannot own one.
- **The other conditions stay**: a directory that is writable by group or others, owned by any other account, or owned by `nobody` anywhere else is refused as before.

## Consequences
- keelbox sets up an unprivileged container. With nesting on, a test on LXD found every limit in force and working: the firewall, the agent's network rule, rootless Docker and bwrap.
- With nesting off, rootless Docker and bwrap cannot mount `/proc` in a new user namespace and fail, and doctor does not report it.
- A privileged container or a virtual machine is unchanged: there `/proc` and `/sys` are root's.
