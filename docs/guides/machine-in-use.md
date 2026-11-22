# A machine already in use, at home or this one

```sh
keelbox up                    # lists what it would change on the machine, then asks
keelbox plan acme --impact    # the same list for a config you wrote; changes nothing
```

A box is an Ubuntu machine set up for AI coding agents. Setting one up turns on a firewall, limits SSH to keys and can restart Docker. On a machine that runs other software, keelbox first lists what that cuts off, replaces or restarts.

## This machine

Option 2 of `keelbox up`, "This machine", sets up the Ubuntu machine you run it on. It is preselected when `keelbox up` runs as root, or on Ubuntu without a desktop session. One line installs keelbox there and starts it:

```sh
curl -fsSL https://get.keelbox.dev | sh -s -- up
```

At step 2 sudo may ask for your password, and keelbox checks the Ubuntu release. Enter at the name keeps the hostname. The steps Agents and access and Sign in are the same as for [your first box](../getting-started.md).

| On this machine | What happens |
|---|---|
| Your own account | It keeps its password and sudo. |
| The new accounts `admin` and `agent` | They get the SSH keys in your `~/.ssh/authorized_keys`. With none there, keelbox asks for a public key file. |
| SSH | It takes keys only. The Review card says to keep the terminal open until `admin` has logged in with your key. |
| SSH from the internet | It stays open until a `keelbox apply` from another computer reaches the box over your tailnet, the private network Tailscale makes. |

## What keelbox up asks

On the SSH route and on this machine, step 4 reads the machine as root and changes nothing. A card lists what listens on the network, with the process or Docker container behind each port. The firewall would block all of it.

| Question | Preselected | Written to the config |
|---|---|---|
| `Keep SERVICE reachable on your LAN?` | Yes | a `service` entry in `host.firewall.allow` |
| `Other ports to keep open on the LAN` | `all`; `none` or port numbers also answer | a `port` entry each in `host.firewall.allow` |
| `Keep SSH open to your LAN as well?` | Yes | `host.firewall.public_ssh: lan` |
| `Limit the agent to 75% of CPU and memory, so the other services keep the rest?` | Yes | `users.agent.limits` |

The first question comes once for each [known service](../reference/services.md) found, and covers all its ports. With Tailscale, everything listed stays reachable over the tailnet. A port that UFW already blocks, or admits by a rule of yours, is not asked about: your UFW rules stay.

The limit question comes when other services or running containers are found. The limit covers everything the agent account runs, its containers included.

## What the Review step lists

A third card, under the two every Review has, lists what keelbox found in use:

| Group | What it lists |
|---|---|
| Cuts off | Services and published container ports that would be blocked from the LAN; accounts that log in over SSH with a password; the image's default account; processes connected to a link-local address; a resolver only root would reach |
| Replaces | Ubuntu's `docker.io`; an agent CLI the agent account already has |
| Restarts | the system Docker daemon, with the containers that stay stopped |
| Changes | root's SSH login, the agent's limits, doctor's scheduled check, the network limits, nightly reboots |
| Keeps | Your UFW rules, the ports you kept, the system Docker containers, your accounts |

The network limits refuse link-local addresses, a cloud's metadata service among them, and the machine's own SSH port to accounts other than root. `host.firewall.link_local_accounts` keeps an account's access.

An item marked `*` is one the first apply would stop for. With any marked, the question reads `Go ahead, accepting the 2 items marked *?`, and No is preselected. A no changes nothing. Each marked item says how to keep what it would cut off, and `keelbox up acme` then asks only the go-ahead again.

## plan --impact and --adopt

```sh
keelbox plan acme --impact
keelbox apply acme --adopt
```

`keelbox plan acme --impact` prints the same groups for a config you wrote by hand, with sshd, UFW and the hostname among them. It also prints the YAML that keeps the ports it marks. It reads the machine as root. Whether a port is reachable is read from the firewall's state, not tested from your LAN.

The first `keelbox apply` on a machine reads it the same way. With an item still marked it changes nothing and exits 1. `--adopt` accepts everything still marked. Later applies do not stop for them.

One change needs `--adopt` on any apply: replacing Ubuntu's Docker packages with Docker Engine from Docker's repository. Images and volumes under `/var/lib/docker` are kept, and the containers are left stopped. `runtime.docker: false` leaves Docker as it is, and the agent then has no Docker.

Every file keelbox writes or edits is in [files keelbox manages on a box](../reference/box-files.md).

## A machine at home

```yaml
host:
  power:
    sleep: never
```

With this key, `keelbox apply` makes the machine refuse every suspend, a closed lid and an idle session included. Shutdowns work as before. Nightly reboots for updates count only the agent and logged-in users (`host.auto_reboot: never` stops them). `keelbox doctor` fails if the machine can still suspend. `sleep: allow`, the default, leaves the machine as it is.

## After a power cut or crash

keelbox notices a boot that did not follow an orderly shutdown: a power cut, a crash or a hard reset. Three minutes after the boot, a `power-back` message goes to your [notification channels](notifications.md). The `power` row of `keelbox status acme` says so until the next boot.

The agent's tmux sessions come back as last saved. Remote Control starts again, and a due backup runs. Programs that ran in tmux are not restarted: [after a reboot](sessions.md#after-a-reboot) says how to continue an agent.
