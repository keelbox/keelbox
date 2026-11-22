# Closing public SSH, and getting back in when locked out

```sh
keelbox login acme tailscale    # the box joins your tailnet
keelbox apply acme              # from a computer on that tailnet
```

The tailnet is your private Tailscale network. Once keelbox has logged in over it as the admin, the account with sudo, the box stops taking SSH from every other address. Until then port 22 stays open. `keelbox up` runs both commands at its last step.

Locked out now: [getting back in](#getting-back-in).

## What closes

The box checks that the apply arrived at its own tailnet address, as the admin, before it closes anything. That apply changes three things:

| Where | Change |
|---|---|
| UFW, the firewall on the box | SSH is admitted on the tailnet interface only |
| sshd on the box | a login that arrives at an address outside the tailnet is refused |
| The cloud firewall of a server keelbox created | TCP 22 is removed |

Only the UFW rule that admits 22/tcp from anywhere is removed. Other UFW rules stay.

When this computer cannot reach the box over the tailnet, apply prints `public SSH stays open:` with the reason, and closes nothing. Join this computer to the same tailnet, or set `target.ssh.tailnet_host` to an address that reaches the box, then apply again.

Afterwards every keelbox command tries the tailnet address first. With MagicDNS, `ssh admin@acme` works over the tailnet.

## See the state

```sh
keelbox doctor acme
keelbox status acme
```

Doctor has three lines: `public-ssh`, `public-ssh-firewall` and `public-ssh-sshd`. Status has one row, `ssh`, which reads `closed: tailnet only` once it is closed, or `closed to the internet: tailnet and LAN only` with `lan`.

## The setting

`host.firewall.public_ssh` in the config:

| Value | SSH closes | SSH opens again |
|---|---|---|
| `auto`, the default | once keelbox has reached the box over the tailnet, to every other address, the LAN included | when an apply arrives at the public address; or, at an apply on the box, when the tailnet is down or the box's tailnet address has changed |
| `lan` | as `auto`, to the internet only: the private ranges `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7` keep SSH | as `auto` |
| `open` | never | |
| `closed` | once keelbox has reached the box over the tailnet; doctor fails until then | never, also while the tailnet is down |

`keelbox up` preselects `lan` for a machine on your LAN.

## Before public SSH closes

Once SSH is closed, the tailnet is the only way in. Two settings in the Tailscale admin console decide whether the box stays on it, and keelbox sets neither.

| Setting | What it does to the box |
|---|---|
| Key expiry | The box's Tailscale key expires after 180 days, unless the tailnet sets another period. The box then leaves the tailnet until someone signs it in again, which needs a way in. |
| ACLs | A rule that stops this computer reaching the box on port 22 has the same effect as no tailnet. |

To turn key expiry off for the box: Machines, the box, Disable key expiry. `keelbox login` says so, and doctor's `tailnet-key` warns 30 days before expiry. Tailscale also turns it off for a machine joined with a tagged [auth key](automation.md#a-tailscale-auth-key-file).

`host.firewall.public_ssh: open` keeps SSH at the public address, with keys only.

## Getting back in

Try these first:

| Situation | Do |
|---|---|
| This computer is on the tailnet, and keelbox tries an old address | Set `target.ssh.tailnet_host` to the box's MagicDNS name or Tailscale IP. |
| The setting is `lan`, and this computer is on the box's LAN | `keelbox login acme tailscale` signs the box in to Tailscale again. |
| Another account still reaches the box over the tailnet | Set `host.firewall.public_ssh: open`, then `keelbox apply acme --ssh-user NAME`. |

When nothing reaches the box, three things refuse a login at its public address:

1. The cloud firewall no longer admits TCP 22. You change that in the cloud account.
2. UFW on the box admits SSH on `tailscale0` only.
3. sshd on the box refuses a login that arrives outside the tailnet, by `/etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf`.

The last two are on the box's disk, so opening port 22 in the cloud firewall is not enough. These three commands, as root on the box, undo both:

```sh
rm -f /etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf /var/lib/keelbox/public-ssh.json
ufw allow 22/tcp
systemctl try-reload-or-restart ssh.service
```

`public-ssh.json` is the box's record that keelbox reached it over the tailnet. Without it, the next apply leaves SSH open until one arrives over the tailnet again.

How to run the commands depends on the cloud. Each procedure below has been run on a live box locked out this way: public SSH closed, then the box signed out of Tailscale. keelbox's automated tests do not run them, and a cloud can change its console or tools.

Each procedure ends the same way: set `host.firewall.public_ssh: open` or repair the tailnet (`keelbox login acme tailscale`), then run `keelbox apply acme`. That apply sets the cloud firewall back to what the box needs. A box that joins the tailnet again may get a new Tailscale address; keelbox reaches it at the public address first and finds the new one.

### Hetzner

The web console asks for a password and keelbox's accounts have none, so the way in is the rescue system.

1. In the Hetzner Console, open the firewall `keelbox-acme` and add an inbound rule for TCP 22, or detach it.
2. Enable the rescue system for the server and reboot into it. Log in with the rescue password: `ssh root@ADDRESS`.
3. Mount the disk and edit it:

   ```sh
   mount /dev/sda1 /mnt
   rm /mnt/etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf /mnt/var/lib/keelbox/public-ssh.json
   sed -i 's/^ENABLED=yes/ENABLED=no/' /mnt/etc/ufw/ufw.conf
   ```

4. Reboot out of rescue. SSH answers at the public address, and UFW is off.
5. Apply, as above. The apply turns UFW on again.

### Google Cloud

The guest agent on the VM runs the script in its `startup-script` metadata as root at every boot. SSH-in-browser and `gcloud compute ssh` connect to port 22, and the serial console asks for a password.

1. Save the three commands in a file `recover.sh`, with `#!/bin/sh` as its first line.
2. Give the script to the VM and restart it. The reserved address stays:

   ```sh
   gcloud compute instances add-metadata acme --zone ZONE --project PROJECT \
     --metadata-from-file startup-script=recover.sh
   gcloud compute instances stop acme --zone ZONE --project PROJECT
   gcloud compute instances start acme --zone ZONE --project PROJECT
   ```

3. Admit TCP 22 in the box's allow rule:

   ```sh
   gcloud compute firewall-rules update keelbox-acme-allow --rules udp:41641,tcp:22 --project PROJECT
   ```

4. Apply, as above.
5. Remove the script, or it opens SSH at every boot:

   ```sh
   gcloud compute instances remove-metadata acme --zone ZONE --project PROJECT --keys startup-script
   ```

If SSH still refuses you after step 3, the VM's serial port output in the Google Cloud console has the script's output.

### DigitalOcean

The Recovery Console in the control panel is a screen and keyboard on the Droplet, outside the network. It asks for a password, and root is the one account that can have one. The Droplet Console connects over the network, as SSH does.

1. In the Droplet's settings, choose Reset root password, unless you hold one DigitalOcean set. DigitalOcean emails a temporary password, and the first login asks for a new one.
2. Open the Recovery Console, log in as root and type the three commands. The console does not take pasted text reliably.
3. Under Networking, Firewalls, add an inbound SSH rule to the firewall `keelbox-acme`.
4. Apply, as above.

If the password reset does not take, boot the Droplet from DigitalOcean's recovery ISO, a setting of the Droplet. Mount the disk from the ISO's menu and make the two file edits of Hetzner's step 3 under `/mnt`. Then power off and switch back to the hard drive.

### AWS

Lightsail has no rescue system and no console outside the network. Its browser SSH client connects to port 22 as `ubuntu`, the account keelbox locked. A launch script runs only on a new instance. So the instance is created again from a snapshot of its own disk, with the commands as its launch script.

The launch script runs on an instance created from a snapshot, because cloud-init runs user data for a new instance id. Lightsail's documentation does not say so. Saving the snapshot of a 60 GB disk took about six minutes.

1. Save the three commands in a file `recover.sh`, with `#!/bin/sh` as its first line. Note the instance's zone and bundle:

   ```sh
   aws lightsail get-instance --instance-name acme --region REGION \
     --query 'instance.[location.availabilityZone,bundleId]'
   ```

2. Stop the instance and save its disk:

   ```sh
   aws lightsail stop-instance --instance-name acme --region REGION
   aws lightsail create-instance-snapshot --instance-name acme \
     --instance-snapshot-name acme-recover --region REGION
   ```

3. Wait until this prints `"available"`. Only then does the snapshot hold the whole disk:

   ```sh
   aws lightsail get-instance-snapshot --instance-snapshot-name acme-recover --region REGION \
     --query instanceSnapshot.state
   ```

4. Delete the instance and create it again under the same name, with keelbox's tags and any from `target.aws.tags`. If Lightsail answers that the name is still in use, run the second command again:

   ```sh
   aws lightsail delete-instance --instance-name acme --force-delete-add-ons --region REGION
   aws lightsail create-instances-from-snapshot --instance-names acme \
     --instance-snapshot-name acme-recover --availability-zone ZONE --bundle-id BUNDLE \
     --user-data file://recover.sh --tags key=keelbox,value=true key=keelbox-box,value=acme \
     --region REGION
   ```

5. Once the instance runs, set its firewall and attach the box's static IP. A new instance has ports 22 and 80 open until the first command replaces them:

   ```sh
   aws lightsail put-instance-public-ports --instance-name acme --region REGION \
     --port-infos fromPort=22,toPort=22,protocol=tcp fromPort=41641,toPort=41641,protocol=udp
   aws lightsail attach-static-ip --static-ip-name keelbox-acme --instance-name acme --region REGION
   ```

6. Apply, as above. The disk keeps its SSH host keys on the new instance, so the keys keelbox pinned still match: `/etc/cloud/cloud.cfg.d/99-keelbox.cfg` tells cloud-init to leave them.
7. Delete the snapshot once the box works. It is a billed copy of the disk with every credential on it, and `keelbox destroy` deletes only snapshots with keelbox's tags:

   ```sh
   aws lightsail delete-instance-snapshot --instance-snapshot-name acme-recover --region REGION
   ```

If the new instance still refuses SSH, the launch script did not run. The snapshot still holds the disk, and Lightsail has no other way to change what is on it, so this route has no second one behind it. On AWS, set what is under [before public SSH closes](#before-public-ssh-closes) first.

### Other providers

Make the file edits of Hetzner's step 3 from the provider's rescue system. Or run the three commands as root from a console that does not go through SSH.
