# Change size or pause billing: resize, park and resume

```sh
keelbox resize acme    # another size, the same box
keelbox park acme      # delete the server, keep its disk as a snapshot
keelbox resume acme    # a new server from that snapshot
```

These work on a box whose server keelbox created. Resize and park show what the change does, costs and takes, then ask, with No preselected. Both stop everything running on the box: sessions, agents, dev servers and containers. Containers with a restart policy start again, and tmux sessions come back as after a reboot.

## Resize

keelbox shuts the server down, changes its size, starts it, applies the config and writes the new size into it. The disk, the address and everything on the disk stay. If the cloud refuses the change, keelbox starts the server again at its old size.

The menu lists the sizes the cloud can deliver in the server's location now, with a disk at least as large as the box's. The disk keeps its size, so you can change back to any size listed. On AWS the instance is replaced and its disk grows with the plan, so the box cannot be made smaller again.

## Park

A server that is switched off is still billed, on all four clouds. Park shuts the server down, saves its disk as a snapshot, waits until the cloud reports the snapshot complete, and only then deletes the server. If the shutdown or snapshot fails, it starts the server again and deletes nothing. The snapshot stays billed, and the public address is given up.

The snapshot holds everything on the disk, the agent's sign-ins and tokens included. Anyone who can read snapshots in the cloud account can create a server from it and read them. Parking revokes nothing.

While a box is parked, `keelbox ls` shows it as parked, and commands that need the server name `keelbox resume acme`. The parked state is stored on the snapshot, so any computer with the box's config and access to the cloud account can resume it. `keelbox destroy acme` deletes the snapshot. `keelbox forget acme` leaves it in the cloud, billed until you delete it there.

## Resume

Resume asks nothing. It creates the server from the snapshot at the size in the config, billed from that moment, applies the config, and deletes the snapshot once that apply has passed. If resume fails, the snapshot is kept; run it again. For another size, change the size key first.

The box comes back at a new public address, so update anything that names the old one. It rejoins the tailnet under its name. If its Tailscale key expired while parked, it does not: run `keelbox login acme tailscale`. A box with public SSH closed is then reachable only through [the cloud's console](public-ssh.md#getting-back-in).

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
| Prices shown | from the Hetzner API | from the Lightsail API; none for the snapshot | from the DigitalOcean API; published for the snapshot | none |
| The snapshot is billed | per GB stored | per GB of the disk's size | per GB stored | per GB stored |

Timings, permissions and each cloud's limits are in the [clouds reference](../reference/clouds.md).
