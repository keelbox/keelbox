# Change size or pause billing: resize, park and resume

```sh
keelbox resize acme    # another size, the same box
keelbox park acme      # delete the server, keep its disk as a snapshot
keelbox resume acme    # a new server from that snapshot
```

They work on a box whose server keelbox created. Resize and park show a card with what the change does, costs and takes, then ask; No is preselected.

Resize and park stop what runs on the box: sessions, agents, dev servers and containers. Containers with a restart policy start again, and tmux sessions return as after a reboot.

## Resize

```text
╭─ Resize acme from cx23 to cpx22 ────────────────────────────────────────────────────╮
│ from          cx23: 2 vCPU (shared), 4 GB, EUR 3.79/month                           │
│ to            cpx22: 2 vCPU (shared), 4 GB, EUR 7.49/month                          │
│ costs         about EUR 3.70 a month more than now                                  │
│ what happens  acme restarts: running sessions, agents, dev servers and containers   │
│               stop. It is back in 1 to 2 minutes, typically                         │
│ what stays    its 40 GB disk, its address and everything on it                      │
│ change back   keelbox resize acme --size cx23                                       │
╰─────────────────────────────────────────────────────────────────────────────────────╯
```

keelbox shuts the server down, changes its size, starts it and applies the config.

The menu lists the sizes the cloud can deliver in the server's location now, with a disk at least as large as the box's. The disk keeps its size, so the box can change back from any size listed.

AWS differs: the instance is replaced, the disk grows with the plan, and the box cannot be made smaller again.

If the cloud refuses the change, keelbox starts the server again at its old size.

## Park

A server that is switched off is still billed on all four clouds. Park deletes the server and keeps its disk.

```text
╭─ While acme is parked ──────────────────────────────────────────────────────────────╮
│ still billed   snapshot 1001, which holds its disk (2.5 GB, about EUR 0.04 a month) │
│ not billed     the server, about EUR 7.49 a month. Its address 203.0.113.7 is given │
│                up                                                                   │
│ bring it back  keelbox resume acme                                                  │
│                takes 1 to 3 minutes, typically. It comes back at a new address,     │
│                with its files and sign-ins as they were                             │
╰─────────────────────────────────────────────────────────────────────────────────────╯
```

keelbox shuts the server down, takes the snapshot, waits until the cloud reports it complete, and then deletes the server. If the shutdown or the snapshot fails, it starts the server again and deletes nothing.

The snapshot holds everything the disk held, the agent's sign-ins and tokens included. Whoever can read snapshots in the cloud account can create a server from it and read them. Parking revokes nothing.

A box with public SSH closed comes back reachable over the tailnet only. If its Tailscale key expires while it is parked, it does not rejoin, and the way in is [the cloud's console](public-ssh.md#getting-back-in).

## While a box is parked

`keelbox ls` shows the box as parked, with the date. A command that needs the server stops and names `keelbox resume acme`, and `keelbox up acme` creates no second server.

The parked state is in the cloud, as labels on the snapshot. Any computer with the box's config and access to the cloud account can resume it.

| Command | On a parked box |
|---|---|
| `keelbox destroy acme` | Deletes the snapshot. It cannot look for unpushed work without a server, and asks `Delete it without knowing?` first. |
| `keelbox forget acme` | Removes the box from this computer. The snapshot stays in the cloud account and is billed until you delete it there. |

## Resume

Resume asks nothing. It says the server is billed again from the moment it is created, then creates it from the snapshot, at the size in the config. It applies the config, and deletes the snapshot once that apply has passed. A resume that fails keeps the snapshot; run it again.

The box has a new public address, and anything that names the old one needs the new one. It rejoins the tailnet under its name, unless its key expired: then run `keelbox login acme tailscale`.

For another size, change the size key in the config first.

## Without a terminal

```sh
keelbox resize acme --size cpx22 --yes
keelbox park acme --yes
keelbox resume acme
```

Without `--yes`, resize and park change nothing and exit 2. Resize without `--size` lists the sizes the box can change to.

## What differs by cloud

| | Hetzner | AWS (Lightsail) | DigitalOcean | Google Cloud |
|---|---|---|---|---|
| Size key in the config | `target.hetzner.server_type` | `target.aws.bundle` | `target.digitalocean.size` | `target.gcp.machine_type` |
| Prices on the cards | from the Hetzner API | from the Lightsail API; none for the snapshot | from the DigitalOcean API; published for the snapshot | none |
| The snapshot is billed | per GB stored | per GB of the disk's size | per GB stored | per GB stored |

Timings, the firewall at a resume, the permissions each command needs and each cloud's limits are in the [clouds reference](../reference/clouds.md).
