# 0038: The impact report, the first-apply gate and what --adopt accepts

Date: 2026-11-07. Status: accepted. Extends ADR 0025 and ADR 0036.

## Context
Most people who try keelbox run it on hardware they already use: a home server with Samba, Plex in Docker, an account they log in to with a password. `keelbox plan` lists every step, about a hundred lines, none of which says "your media server stops being reachable from the TV". `apply` then turns UFW on with default deny, drops LAN connections to the system Docker daemon's published ports in the `KEELBOX-DOCKER` guard, turns off SSH passwords and restarts Docker for its `daemon.json`, which leaves containers without a restart policy stopped. Each is right for an agent box and each, unannounced, breaks something on a machine in use. An `--adopt` that covered only one of these (replacing Ubuntu's `docker.io`) would stop the apply half way, after host and access had changed.

## Decision
- **Inventory.** `internal/inventory` reads the machine as root and changes nothing: `ss -Hltunp`, `ufw status verbose`, the system Docker daemon's containers with their published ports and restart policies (asked at `/var/run/docker.sock` by name, never the agent's rootless daemon), accounts from `getent passwd`, `/etc/shadow` and `getent group`, `sshd -T`, `systemd-detect-virt`, and tmux servers and agent CLIs from `ps`.
- **Impact.** A module may implement the optional `engine.Impacter`; the `Module` interface (ADR 0003) is unchanged. `engine.Impact` joins the plan with the inventory, and each module says in plain words what its pending steps would do to what is there. Items are grouped: cuts off, replaces, restarts, changes, keeps. Host covers the firewall, accounts, sshd, LAN SSH and the hostname; runtime covers Docker and its published ports; sessions covers tmux and agent CLIs already running.
- **Reachability is estimated from firewall state, not probed**: keelbox cannot test from the LAN, and the report says what the firewall would do. A listener is reachable now when UFW is off or lets everything in; when UFW already refuses inbound by default, keelbox keeps its rules and changes nothing for that listener. A container port published beyond loopback is reachable now whatever UFW says, because Docker's forwarding bypasses it.
- **`keelbox plan --impact`** prints the report, the `host.firewall.allow` and `public_ssh` YAML that keeps what would be cut off, and the number of steps; `--json` gives the same for scripts. It runs on the box like `plan`.
- **The first-apply gate.** When the logbook has no apply record at all, apply collects the report before any step. If an item is marked as gating and `--adopt` is not given, it exits 1 having changed nothing and written nothing, printing those items, the YAML that keeps them and `keelbox apply BOX --adopt`. Gating items:
  - a service listening on the network that the firewall or the Docker guard would block, unless `host.firewall.allow` names its port;
  - an account that logs in over SSH with a password now;
  - an existing account named as the agent that would leave `sudo` or `docker`;
  - software replaced: Ubuntu's `docker.io` and its conflicts, or `docker-ce` moved to the release's major series;
  - a Docker restart that leaves a running container without a restart policy stopped.
  Not gating, but listed: the image's default account locked (keelbox proves the admin first, ADR 0009), root's SSH login, LAN SSH closing later under `public_ssh: auto` (only once a tailnet path is proven, ADR 0021; `lan` keeps it, ADR 0036), the hostname, UFW turning on.
- **`--adopt` means "accept what the report marks on a machine in use"**, not only "replace conflicting software". Replacing `docker.io` needs it on any apply.
- Later applies show the report with `plan --impact` but never gate: the box is keelbox's by then.
- **`keelbox up`** on the "this machine" and "existing server" paths runs `plan --impact` on the machine before the go-ahead and lists what it found in use. Ports it asked about (keep open on the LAN or not) count as answered. Anything else that gates needs an explicit yes on the terminal (default no), or `--adopt` without one; the yes is kept in up's state so a resumed first apply runs with `--adopt`. If the report cannot be had, up says so and goes on: the gate still stops the first apply.

## Consequences
- A fresh cloud server has nothing that gates, so `create`, `up --provider hetzner` and the e2e boxes are unaffected. The first apply on any box runs every check twice (once for the report): a few seconds.
- The gate keys on "no apply record", so a machine whose logbook was lost, or a box's config moved to another machine already applied, does not gate.
- An `/etc/shadow` hash says an account has a password; whether its owner uses it over SSH is not known (`last` is not read). The item says the password still works for sudo and at the console.
- The report can be wrong where firewall state misleads: a service bound to one interface behind another firewall, or a container behind a reverse proxy on the host. It names what it read so the owner can judge.
