# 0012: Rootless Docker for the agent; guarded system daemon

Date: 2026-11-06. Status: accepted. Supersedes the Docker parts of ADR 0005.

## Context
Access to the system Docker socket is root-equivalent (`docker run -v /:/host`), so with the agent in the `docker` group "the agent has no sudo" would protect nothing, and agents could rewrite their root-owned rules. Separately, Docker publishes container ports with kernel DNAT ahead of UFW's input rules, so `ports: ["5432:5432"]` from any process with daemon access was reachable from the internet.

## Decision
- **The agent runs its own rootless Docker daemon** (`~/.config/systemd/user/docker.service`, the unit `dockerd-rootless-setuptool.sh` writes, managed and converged), with `docker-ce-rootless-extras`, `uidmap`, `slirp4netns` and `dbus-user-session`. `DOCKER_HOST` points at `/run/user/<uid>/docker.sock` in the agent's shell block and `environment.d`. The agent is not in the `docker` group; apply removes it if it is. Ubuntu 24.04+ ships the AppArmor profile rootlesskit needs for unprivileged user namespaces.
- **Rootless published ports are ordinary sockets** owned by the agent's rootlesskit process, so UFW filters them like any listening port: from outside the box they are reachable only where UFW allows (tailnet, SSH). Rootless containers' outbound traffic leaves through the agent's own network helper, so the agent network limits (ADR 0011) apply to them.
- **The system daemon stays, for root only, behind a guard.** `keelbox-docker-guard.service` (bound to `docker.service` with `PartOf` and `WantedBy`) creates the `KEELBOX-DOCKER` chain and jumps to it first from `DOCKER-USER`: new forwarded connections are dropped unless they arrive on `tailscale0` or a Docker bridge. Rules the user adds to `DOCKER-USER` are kept. Doctor checks the jump is in place.
- **Adoption is explicit.** A box with Ubuntu's `docker.io` (or another package Docker lists as conflicting) is not changed silently: plan shows the removal as needing `--adopt`, and apply refuses without it, because removing the package stops its containers.

## Consequences
- Containers the agent ran under the system daemon on such a box stay there, owned by root; the agent starts afresh under its own daemon. Images re-pull.
- Rootless limits apply: no privileged ports below 1024, no `--net=host` onto the real host network, overlayfs in the user namespace. Compose and Buildx work.
- If Docker switches its default firewall backend to nftables, `DOCKER-USER` goes away; doctor's guard check then fails loudly rather than passing.
