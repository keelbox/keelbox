# A machine already in use, at home or this one

```sh
keelbox up                    # lists what it would change on the machine, then asks
keelbox plan acme --impact    # the same list for a config you wrote; changes nothing
```

Setting up a box turns on a firewall, limits SSH to keys and can restart Docker. On a machine that already runs other software, keelbox first lists what that would cut off, replace or restart, and asks.

## This machine

To set up the Ubuntu machine you are on:

```sh
curl -fsSL https://get.keelbox.dev | sh -s -- up
```

Choose option 2, "This machine". Your own account keeps its password and sudo. `admin` and `agent` get the keys in your `~/.ssh/authorized_keys`, or a public key file keelbox asks for. SSH takes keys only, so keep your terminal open until `admin` has logged in with your key. SSH from the internet stays open until a `keelbox apply` from another computer reaches the box over your tailnet.

## What keelbox up asks

At step 4 keelbox reads the machine as root, changes nothing, and lists what listens on the network, with the process or container behind each port. The firewall would block all of it, so it asks:

| Question | Preselected | Written to the config |
|---|---|---|
| `Keep SERVICE reachable on your LAN?` | Yes | a `service` entry in `host.firewall.allow` |
| `Other ports to keep open on the LAN` | `all` (or `none`, or port numbers) | a `port` entry each in `host.firewall.allow` |
| `Keep SSH open to your LAN as well?` | Yes | `host.firewall.public_ssh: lan` |
| `Limit the agent to 75% of CPU and memory, so the other services keep the rest?` | Yes | `users.agent.limits` |

The service question comes once per [known service](../reference/services.md) and covers all its ports. With Tailscale, everything stays reachable over the tailnet. Ports your UFW rules already block or admit are not asked about, and your rules stay. The limit covers everything the agent runs, its containers included.

## What the Review step lists

A third Review card lists what keelbox found in use:

| Group | What it lists |
|---|---|
| Cuts off | Services and published container ports blocked from the LAN; accounts that log in over SSH with a password; the image's default account; processes connected to a link-local address; a resolver only root would reach |
| Replaces | Ubuntu's `docker.io`; an agent CLI the agent account already has |
| Restarts | the system Docker daemon, and the containers that stay stopped |
| Changes | root's SSH login, the agent's limits, doctor's scheduled check, the network limits, nightly reboots |
| Keeps | your UFW rules, the ports you kept, the system Docker containers, your accounts |

The network limits stop every account but root from reaching link-local addresses (`169.254.0.0/16`, `fe80::/10`, a cloud's metadata service among them) and the machine's own SSH port, so a metadata query or `ssh localhost` needs sudo. `host.firewall.link_local_accounts` keeps an account's access.

The first apply stops for items marked `*`. With any marked, the question reads `Go ahead, accepting the 2 items marked *?`, with No preselected, and a no changes nothing. Each marked item says how to keep what it would cut off.

## plan --impact and --adopt

For a config you wrote by hand:

```sh
keelbox plan acme --impact    # the same groups, plus the YAML that keeps the marked ports
keelbox apply acme --adopt    # accept everything still marked
```

`plan --impact` reads reachability from the firewall's state; it does not test from your LAN. A first `keelbox apply` with an item still marked changes nothing and exits 1. After `--adopt`, later applies do not stop for those items.

Replacing Ubuntu's Docker packages with Docker Engine always needs `--adopt`. Images and volumes under `/var/lib/docker` are kept, and the containers are left stopped. `runtime.docker: false` leaves Docker alone, and the agent then has none.

Every file keelbox writes is in [files on the box](../reference/box-files.md).

## A machine at home

To stop the machine sleeping, set this and apply:

```yaml
host:
  power:
    sleep: never
```

The machine then refuses every suspend, a closed lid included; shutdowns still work, and doctor fails if it can still suspend. Nightly reboots for updates wait until the agent and logged-in users are idle; `host.auto_reboot: never` stops them.

## After a power cut or crash

Three minutes after a boot that followed a power cut, crash or hard reset, the box sends a `power-back` message to your [notification channels](notifications.md). The `power` row of `keelbox status acme` shows it until the next boot.

The agent's tmux sessions come back as last saved, Remote Control starts again, and a due backup runs. Programs that ran in tmux do not restart: [after a reboot](sessions.md#after-a-reboot) says how to continue an agent.
