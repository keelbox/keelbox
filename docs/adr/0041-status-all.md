# 0041: keelbox status --all reads each box's snapshot over plain ssh

Date: 2026-11-07. Status: accepted. An exception to "remote commands run the uploaded binary" (ADR 0006).

## Context
An owner with several boxes wants one table: reachable, healthy, sessions up, updates pending. `keelbox status` per box uploads keelbox (about 10 MB) and runs it with `sudo -n`, one box after another: slow for a handful of boxes and pointless for a box that is off, which costs a full SSH timeout each.

Each box's watch run already writes a status snapshot every two minutes (`/var/lib/keelbox/status.json`, 0644 root, no secrets; ADR 0030).

## Decision
- `keelbox status --all` reads every box in `~/.config/keelbox/boxes/`, ten at a time, each within 12 seconds. For each it runs `cat /var/lib/keelbox/status.json` over ssh as the admin with `BatchMode=yes`, `ConnectTimeout=8` and keelbox's own `known_hosts`, strictly. Nothing is uploaded, no sudo is used and nothing on the box runs as root: this is the one command that reads a box without the uploaded binary, because it only reads a world-readable file.
- Address order: the tailnet address when keelbox knows one and it accepts a TCP connection within 3 seconds, then the public one (`target.ssh.host`, the address `keelbox up` recorded, or for a Hetzner box the API's, with a token found without a prompt). A box on this machine is read directly.
- A box is `ok`, `stale` (reached, snapshot over 10 minutes old: its watch run has stopped), `unreachable` (ssh did not get there, with ssh's reason and `keelbox ssh <box>`) or `error` (a config that does not load, or no snapshot, which `keelbox apply <box>` fixes). Reachability is ssh's exit code; the reason is read from ssh's own message.
- Exit 0 when every box was reached, its snapshot is fresh and its last scheduled doctor found no failure; 1 otherwise. `--json` prints the rows with each snapshot.

## Consequences
- A set of boxes that are all off answers in about one time limit, not one per box.
- The table is as fresh as each snapshot, at most two minutes old on a running box. A column the snapshot has nothing for shows `-`.
- The agent can read the snapshot, which tells it nothing it cannot already see, and cannot change it.
