# The four clouds

```sh
keelbox up acme --provider hetzner    # or aws, digitalocean, gcp
```

keelbox creates, resizes, parks and deletes servers on four clouds. This page lists what differs between them. What each needs installed, its sign-in and its permissions: [cloud sign-ins and permissions](../guides/credentials.md).

## What differs

| | Hetzner | AWS (Lightsail) | DigitalOcean | Google Cloud |
|---|---|---|---|---|
| The server | a cloud server | a Lightsail instance | a Droplet | a Compute Engine VM |
| Size key | `target.hetzner.server_type` | `target.aws.bundle` | `target.digitalocean.size` | `target.gcp.machine_type` |
| Location key | `target.hetzner.location` | `target.aws.region` | `target.digitalocean.region` | `target.gcp.zone` |
| Account of the first login | `root` | `ubuntu` | the admin | the admin |
| Public address | the server's own | a static IP, `keelbox-acme` | the Droplet's own | a reserved address, `keelbox-acme` |
| A resize | changes the type in place | replaces the instance, on a larger plan only | changes the size in place | changes the machine type in place |
| The disk at a resize | keeps its size | grows to the plan's size | keeps its size | keeps its size |
| Kept beside the snapshot at park | the firewall | nothing: a tag on the snapshot records whether SSH was admitted | the firewall and the box's tag | the two firewall rules |
| The firewall arrives | in the create call | at create once the instance runs; at resume and resize once Lightsail accepts it | at create by tag, when DigitalOcean applies it; at resume by id, and keelbox waits for it | by network tag, once the VM exists |
| The server is marked with | labels `keelbox=true` and `keelbox-box=acme` | tags of those names | tags `keelbox` and `keelbox-box-acme` | labels of those names |
| The parked snapshot adds | the label `keelbox-parked=true` | a tag of that name | the tag `keelbox-parked` | a label of that name |
| Resize, park and resume take, typically | 1 to 2, 1 to 2 and 1 to 3 minutes | about 9, 2 and 1 to 2 minutes | 2 to 3, 2 and 2 to 3 minutes | 2 to 3, 2 to 3 and 2 minutes |

Times are for a box with little on its disk; resize and resume include their apply.

On all four:

- keelbox resizes, parks and deletes only a server with those marks, together with the firewall and address it created for it.
- The public address stays at a resize and is given up at a park.
- The firewall admits Tailscale's UDP port 41641, and SSH until [public SSH closes](../guides/public-ssh.md).
- The server's first SSH host key is made on your computer and passed in at create. keelbox checks it at the first connection, then replaces it with keys made on the server. The same host keys come back at resume.
- keelbox switches off a server that has not shut down after 5 minutes. DigitalOcean can take up to five minutes more. Google Cloud switches a VM off itself.

## Hetzner

`keelbox up` uploads your SSH public key to the project and names it in `target.hetzner.ssh_keys`. Create and resume refuse without a key there, because Hetzner would set a root password.

The resize menu lists the types of the server's architecture that its location can deliver now, with a disk no smaller than the server's.

| Message | What to do |
|---|---|
| `Hetzner refused to create acme's server, ...` or `Hetzner lists server type ... as not orderable in ... now` at create | On a terminal, `keelbox up` asks for another location, size or cloud. Otherwise run one of the commands the message ends with. After `keelbox create`, set `target.hetzner.location` or `server_type` to one the message lists, then run `keelbox create acme` again. |
| `Hetzner refused to create acme's server again from its snapshot, ...` at resume | The box stays parked. Set `target.hetzner.location` or `server_type` to one the message lists, then run `keelbox resume acme`, or run it later. |
| `a firewall named keelbox-acme exists ... without keelbox's labels` | Rename or delete that firewall in the Hetzner Console. |

## AWS (Lightsail)

Lightsail opens ports 22 and 80 on every new instance and takes other rules only once the instance exists. keelbox sets the box's rules once the instance runs at create, and keeps sending them at resume and resize until Lightsail accepts them.

The static IP keeps the address across stop and start. Without one attached, doctor warns `static-ip`, and a resize changes nothing and starts the instance again. Both print these commands:

```sh
aws lightsail allocate-static-ip --static-ip-name keelbox-acme --region REGION
aws lightsail attach-static-ip --static-ip-name keelbox-acme --instance-name acme --region REGION
```

Lightsail cannot change an instance's plan. To resize, keelbox snapshots the stopped instance, deletes it, and creates one of the same name from the snapshot on the larger plan. The menu lists only plans whose disk, memory and vCPUs are all at least the box's.

| If | Then |
|---|---|
| The instance cannot be created on the larger plan | keelbox creates it again on the old plan. |
| That fails too, or the resize is interrupted after the delete | The box is parked, with its static IP kept. `keelbox resume acme` brings it back at the same address. |
| `target.aws.bundle` is smaller than the plan a parked snapshot was taken on | Resume exits before creating anything and lists the plans that fit. |

Park releases the static IP, which Lightsail bills while unused, and resume allocates a new one.

A new Lightsail instance makes new SSH host keys unless a file on its disk tells cloud-init to keep them. Apply writes that file, `/etc/cloud/cloud.cfg.d/99-keelbox.cfg`, on every AWS box. Resize and park exit with `nothing changed` without it; this puts it there:

```sh
keelbox apply acme
```

## DigitalOcean

Without a terminal `keelbox up` needs `--location`: there is no default region.

DigitalOcean sets a root password on a new Droplet and emails it, unless the Droplet is created with an SSH key from the account. keelbox adds no key to the account; if the account already holds one of the admin's keys, keelbox names it at create and resume, and no password is set. Either way sshd on the box refuses root and passwords.

The resize menu lists the sizes the region can deliver, with a disk at least as large as the Droplet's.

The DigitalOcean API has no snapshot price, so the card prints DigitalOcean's published one, USD 0.06 per GB a month.

| If | Then |
|---|---|
| keelbox is killed between starting the snapshot and tagging it | An untagged snapshot `keelbox-parked-acme` is left, which keelbox does not find. Delete it in the control panel, under Backups & Snapshots. |
| `target.digitalocean.region` is not the snapshot's region, or the size's disk is too small for the snapshot | Resume exits before creating anything, and names the region or the sizes that fit. |
| DigitalOcean does not report the firewall applied at resume | Resume exits with `Droplet ... runs, but ...` and a `doctl compute firewall get` command. UFW on the box is in force meanwhile. |

## Google Cloud

Menus and cards name Google's pricing pages where the other clouds show a price.

At create keelbox makes these in the project:

| What | Detail |
|---|---|
| Firewall rule `keelbox-acme-allow` | admits SSH and Tailscale to the network tag `keelbox-box-acme`, priority 900 |
| Firewall rule `keelbox-acme-deny` | refuses every other inbound packet to that tag, priority 901 |
| Address `keelbox-acme` | reserved, so it stays across stop and start |
| The VM | a 50 GB `pd-balanced` boot disk unless `target.gcp.disk_gb` says otherwise; project SSH keys blocked, OS Login off, no service account |

Doctor checks the login settings as `cloud-login`, and warns `cloud-firewall-others` when another rule of the network admits traffic to the box ahead of the deny rule.

`keelbox up` lists E2 machine types up to 8 vCPUs; `--size` takes any other x86 type. For a VM of another family, the resize menu adds that family up to 8 vCPUs, or twice the VM's if more.

The parked snapshot is named `keelbox-acme-YYYYMMDD-HHMMSS`, in UTC. At resume the boot disk keeps the size it had; `target.gcp.disk_gb` is not applied.

| If | Then |
|---|---|
| The project has no `default` network | Create exits and prints `gcloud compute networks create default --subnet-mode=auto`. |
| The organisation requires OS Login on every VM | Create and resume fail: a box logs in with the admin's own SSH key. |
| The VM has a second disk | Park keeps the boot disk only, so it exits before its question and prints the `gcloud compute instances detach-disk` command. |
| The address cannot be released at park | Park finishes with a warning and the `gcloud compute addresses delete` command. A later resume uses that address. |
| The VM cannot be created at resume | The snapshot is kept, and the address resume reserved is released. |
| A stop takes 2 minutes or more | keelbox notes that Google may have switched the VM off before its system shut down. |

## The first minute after a resume

A resumed server boots with everything on its disk running, before the apply at the end of `keelbox resume`. Two protections are not in place from the first second:

| What | Where | Until |
|---|---|---|
| The cloud firewall | DigitalOcean and AWS | the cloud reports it applied. keelbox waits for that before the apply. |
| The rule that refuses SSH to the box's new public address to every account but root | AWS and Google Cloud | the apply, which records the address in `/etc/keelbox/outside-addresses`. |

Meanwhile sshd on the disk takes keys only and refuses root, and UFW denies incoming connections. Both cases: [security model](../security-model.md#what-keelbox-does-not-defend-against).
