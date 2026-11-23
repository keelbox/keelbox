# Closing public SSH, and getting back in when locked out

```sh
keelbox login acme tailscale    # the box joins your tailnet
keelbox apply acme              # from a computer on that tailnet
```

`keelbox up` runs both at its last step. Once keelbox has logged in to the box as the admin over your tailnet (your private Tailscale network), the box stops taking SSH from every other address. Until then, port 22 stays open.

Locked out now? Go to [getting back in](#getting-back-in).

## What closes

The box closes SSH only after it sees an apply arrive at its own tailnet address, as the admin. That apply changes three things:

| Where | Change |
|---|---|
| UFW on the box | SSH is admitted on the tailnet interface only. Only the rule that admits 22/tcp from anywhere is removed; your other rules stay. |
| sshd on the box | A login that arrives at an address outside the tailnet is refused. |
| The cloud firewall, on a server keelbox created | TCP 22 is removed. |

If this computer cannot reach the box over the tailnet, apply prints `public SSH stays open:` with the reason and closes nothing. Join this computer to the tailnet, or set `target.ssh.tailnet_host` to an address that reaches the box, and apply again. Once closed, keelbox commands try the tailnet address first, and with MagicDNS `ssh admin@acme` works.

To see the state:

```sh
keelbox status acme    # the ssh row: "closed: tailnet only", or "closed to the internet: tailnet and LAN only"
keelbox doctor acme    # public-ssh, public-ssh-firewall and public-ssh-sshd
```

## The setting

`host.firewall.public_ssh` in the config:

| Value | SSH closes | SSH opens again |
|---|---|---|
| `auto` (default) | once keelbox has reached the box over the tailnet, to every other address, the LAN included | when an apply arrives at the public address; or, at an apply on the box, when the tailnet is down or the box's tailnet address has changed |
| `lan` | as `auto`, but to the internet only: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7` keep SSH | as `auto` |
| `open` | never | |
| `closed` | once keelbox has reached the box over the tailnet; doctor fails until then | never, even while the tailnet is down |

`keelbox up` preselects `lan` for a machine on your LAN. `open` keeps SSH at the public address, with keys only.

## Before public SSH closes

Once SSH is closed, the tailnet is the only way in. Two settings in the Tailscale admin console decide whether the box stays on it, and keelbox sets neither:

- **Key expiry.** The box's Tailscale key expires after 180 days unless the tailnet sets another period. The box then leaves the tailnet until it is signed in again, which needs a way in. To turn expiry off: Machines, the box, Disable key expiry. `keelbox login` reminds you, and doctor's `tailnet-key` warns 30 days ahead. A machine joined with a tagged [auth key](automation.md#a-tailscale-auth-key-file) has expiry off already.
- **ACLs.** A rule that stops this computer reaching the box on port 22 has the same effect as no tailnet.

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
3. sshd on the box refuses logins from outside the tailnet, by `/etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf`.

The last two are on the box's disk, so opening port 22 in the cloud firewall is not enough. These three commands, as root on the box, undo both:

```sh
rm -f /etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf /var/lib/keelbox/public-ssh.json
ufw allow 22/tcp
systemctl try-reload-or-restart ssh.service
```

`public-ssh.json` records that keelbox reached the box over the tailnet. Without it, the next apply leaves SSH open until an apply arrives over the tailnet again.

How you run them depends on the cloud, as below; a cloud can change its console or tools. Each procedure ends the same way: set `host.firewall.public_ssh: open` or repair the tailnet (`keelbox login acme tailscale`), then run `keelbox apply acme`, which also resets the cloud firewall. If the box rejoins the tailnet at a new address, keelbox finds it through the public address.

### Hetzner

The web console needs a password, and keelbox's accounts have none, so use the rescue system.

1. In the Hetzner Console, open the firewall `keelbox-acme` and add an inbound rule for TCP 22, or detach it.
2. Enable the rescue system for the server and reboot into it. Log in with the rescue password: `ssh root@ADDRESS`.
3. Mount the disk and edit it:

   ```sh
   mount /dev/sda1 /mnt
   rm /mnt/etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf /mnt/var/lib/keelbox/public-ssh.json
   sed -i 's/^ENABLED=yes/ENABLED=no/' /mnt/etc/ufw/ufw.conf
   ```

4. Reboot out of rescue. SSH answers at the public address, with UFW off.
5. Apply, as above. The apply turns UFW on again.

### Google Cloud

SSH-in-browser and `gcloud compute ssh` use port 22, and the serial console needs a password. Instead, use a startup script, which the VM's guest agent runs as root at every boot.

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

If SSH still refuses you after step 3, the VM's serial port output in the Google Cloud console shows the script's output.

### DigitalOcean

Use the Recovery Console, a screen and keyboard on the Droplet that does not go over the network. It needs a password, and only root can have one. (The Droplet Console goes over the network, as SSH does.)

1. In the Droplet's settings, choose Reset root password, unless you have one DigitalOcean set. DigitalOcean emails a temporary password; the first login asks for a new one.
2. Open the Recovery Console, log in as root and type the three commands. It does not take pasted text reliably.
3. Under Networking, Firewalls, add an inbound SSH rule to the firewall `keelbox-acme`.
4. Apply, as above.

If the password reset does not take, boot the Droplet from DigitalOcean's recovery ISO (a Droplet setting), mount the disk from the ISO's menu, and make the two file edits of Hetzner's step 3 under `/mnt`. Then power off and switch back to the hard drive.

### AWS

Lightsail has no rescue system and no console outside the network, and its browser SSH logs in as `ubuntu`, which keelbox locked. A launch script runs only on a new instance, so you recreate the instance from a snapshot of its own disk, with the commands as its launch script. The snapshot can take several minutes for a large disk.

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

4. Delete the instance and create it again under the same name, with keelbox's tags and any from `target.aws.tags`. If Lightsail says the name is still in use, run the second command again:

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

6. Apply, as above. The disk keeps its SSH host keys on the new instance, so the keys keelbox pinned still match.
7. Delete the snapshot once the box works. It is a billed copy of the disk with every credential on it, and `keelbox destroy` deletes only snapshots with keelbox's tags:

   ```sh
   aws lightsail delete-instance-snapshot --instance-snapshot-name acme-recover --region REGION
   ```

If the new instance still refuses SSH, the launch script did not run, and Lightsail has no other way to change the disk. There is no fallback, so on AWS set up what [before public SSH closes](#before-public-ssh-closes) describes first.

### Other providers

Make the file edits of Hetzner's step 3 from the provider's rescue system, or run the three commands as root from a console that does not go through SSH.
