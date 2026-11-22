# 0048: Limits on the agent's CPU and memory

Date: 2026-11-07. Status: accepted. Extends ADR 0038.

## Context
On the home server of ADR 0046, Jellyfin transcodes in a container of the system Docker while an agent runs a build and a test suite in its own rootless Docker. Without limits the agent takes every CPU and as much memory as it likes: the film stutters, and when memory runs out the kernel's OOM killer picks the largest process on the machine, which may be Jellyfin or the UniFi controller. The owner wants the agent to slow down first, and to be the one stopped if something has to be.

## Decision
- **One slice.** Every process of the agent account runs in its systemd slice, `user-UID.slice`: its SSH sessions, and its user manager (`user@UID.service`, kept running by lingering) with `keelbox-tmux.service`, the Remote Control unit and rootless Docker, whose containers run in the user manager's cgroup. A drop-in, `/etc/systemd/system/user-UID.slice.d/50-keelbox-limits.conf`, limits them together; the agent cannot change it, and nothing it starts leaves the slice.
- **Config.** `users.agent.limits: {cpu: 75%, memory: 70%}`, with `cpu_weight` and `io_weight` (default 50). `cpu` is a share of all CPUs, written as `CPUQuota` for the CPUs the box has when apply runs (systemd counts one CPU as 100%). `memory` is a share of RAM or a size, written as `MemoryMax`, with `MemoryHigh` at 90% of it: past `MemoryHigh` the kernel reclaims the agent's memory and holds back its allocations, so it slows down; at `MemoryMax` the kernel's OOM killer stops the largest process in the slice and nowhere else. `MemorySwapMax` gives the agent the same share of the swap there is when apply runs, so it cannot fill the swap other services fall back on.
- **Weights.** CPU and IO weights compete between sibling cgroups, so the agent's slice against `system.slice` is decided one level up: a second drop-in gives `user.slice` the same weights (50 against system services' 100), and the agent's slice gets them inside it. Under contention Jellyfin, in `system.slice`, gets two thirds of the CPU; idle CPU is still the agent's up to its quota.
- **Sessions survive an OOM kill.** systemd's default `OOMPolicy=stop` would stop `keelbox-tmux.service` when the kernel kills one of its processes, ending every session. Root-owned drop-ins in `/etc/systemd/user/` set `OOMPolicy=continue` for it and for the Remote Control unit, read with a daemon-reload of the agent's user manager; the units are not restarted.
- **Defaults.** No limits unless configured. `keelbox up` on a machine where it found other services or running containers proposes 75% of CPU and memory, a quarter left for them, default yes; `--limits PCT|none` answers without a terminal. On a fresh machine of its own, nothing is proposed.
- **Seen.** `plan --impact` lists the limits under Changes (they never gate). Doctor reads the slice's cgroup: the limits in force (and fails when the drop-in is there but systemd has not applied it), how often CPU was held back, how often memory went past `MemoryHigh`, and warns once the kernel stopped a process at `MemoryMax`; it says which disks honour the IO weight. The watch run samples the slice into the status snapshot every two minutes, and `keelbox status` shows the agent's CPU share and memory against its limits.

## Consequences
- An agent that reaches its ceiling loses a process (the largest: usually a build, a test run or a container), not the box's other services. tmux and its other sessions keep running.
- `CPUQuota` and `MemorySwapMax` are fixed at apply time: a box that gains CPUs or swap keeps the old caps until the next apply.
- IO weight needs the `bfq` scheduler; Ubuntu uses `none` for NVMe and `mq-deadline` for most others, where `io_weight` has no effect. keelbox does not change the scheduler.
- Ubuntu Server does not run systemd-oomd; where it runs, it may act on the slice's memory pressure before the kernel does. keelbox does not configure it.
- Limits apply to the agent account only, not to the admin or to accounts of the owner's, and not to the system Docker's containers.
