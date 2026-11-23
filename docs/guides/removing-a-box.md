# Remove a box

```sh
keelbox destroy acme    # a cloud server keelbox created: delete it and its disk
keelbox forget acme     # any other machine: stop managing it from this computer
```

`destroy` cannot be undone: the server, its disk and everything on them are gone, and no backup is taken first. `forget` changes only this computer. To stop paying for a server and keep its disk, [park the box](resize-park-resume.md#park) instead.

## Delete a cloud server

destroy first looks for work that exists only on the box:

```text
~/projects on devbox holds work that exists nowhere else:
  shop   not a git repository
  api    3 uncommitted changes, 2 commits not pushed
  notes  no remote
? Delete this work with the server? [y/N]
```

It checks git in each directory directly under the agent's `~/projects`, nothing else: not other directories, Docker volumes, databases, stashes or ignored files. To keep work, push it as the agent (`keelbox ssh acme`) or copy it off first. If it finds work, or the box does not answer, it asks, with No preselected. Then you type the box name.

`--yes` skips typing the name; the question about work is still asked. `--discard-work` skips the look at the projects. Without a terminal, destroy refuses unless you give both.

| Cloud | What destroy deletes |
|---|---|
| Hetzner | the server and the firewall `keelbox-acme` |
| AWS | the Lightsail instance, the static IP `keelbox-acme` and every snapshot keelbox made of the box |
| DigitalOcean | the Droplet, the firewall `keelbox-acme` and the tags keelbox made |
| Google Cloud | the VM and its disk, the firewall rules `keelbox-acme-allow` and `keelbox-acme-deny`, and the address `keelbox-acme` |

destroy deletes only resources that carry keelbox's label, tag or description for this box. A resource of the same name that keelbox did not make stops the command and is left alone. On a parked box, it deletes the snapshot, after asking `Delete it without knowing?`.

Afterwards the config stays, and `keelbox ls` shows `server destroyed`. `keelbox up acme` creates a new, empty server from it; `keelbox forget acme` removes it.

## Forget a box

`keelbox forget acme` removes the box's config, what keelbox noted beside it, and its pinned host keys, after asking (`--yes` skips the question). It does not contact the machine. For a machine keelbox did not create, delete or wipe it at its provider yourself.

For a cloud box, run `keelbox destroy acme` first if the server is to go. forget removes what destroy needs to find it, and afterwards only the cloud's console can delete it. forget says what stays billed and preselects keeping the box.

## What stays elsewhere

Neither command removes these. destroy lists them when it ends, after signing a box it can reach out of Tailscale.

- The machine in the Tailscale admin console, and any auth key made for it.
- The agent's sign-ins: revoke its GitHub token and its Claude and OpenAI sessions in those accounts.
- Deploy keys and API tokens you gave the agent.
- The Hetzner token. `keelbox forget-token` removes keelbox's copy; delete the token itself in the Hetzner Console.
- `hetzner.token` and `tailscale.authkey` in `~/.config/keelbox`, if you made them.
- The keelbox binary and completions: [install keelbox](installing.md) lists them.

## Keep the machine, undo keelbox

A forgotten box keeps working as it was set up, and keelbox's timers on it keep running ([what a box does unattended](../concepts/how-it-works.md#what-a-box-does-unattended)). keelbox has no undo command. As the admin on the box, these do most of it:

```sh
# stop the agent's services: its tmux server and Remote Control
sudo loginctl disable-linger agent

# remove keelbox's timers, path units and services, before the binary they run
cd /etc/systemd/system
units=$(ls keelbox-*.timer keelbox-*.path keelbox-*.service 2>/dev/null)
sudo systemctl disable --now $units
sudo rm -f $units
sudo systemctl daemon-reload

# remove keelbox's files and state
sudo rm -rf /etc/keelbox /var/lib/keelbox /var/lib/keelbox-preview /var/cache/keelbox \
  /usr/local/lib/keelbox/docker-user-guard.sh /usr/local/bin/keelbox

# unlock the image's default account, if keelbox locked it
sudo usermod --unlock ubuntu && sudo chage -E -1 ubuntu
```

`usermod` warns that the account has no password: see [the image's default account](adopting-a-box.md#the-images-default-account).

These stay; remove each only if you want the effect listed:

| What | Removing it |
|---|---|
| `/etc/ssh/sshd_config.d/01-keelbox.conf` | lets sshd take password and root logins again, as far as its other settings allow |
| UFW | removes the inbound firewall |
| `/etc/sudoers.d/90-keelbox-admin` | removes the admin's sudo; do it from another account with sudo |
| `~/.local/bin/claude`, `~/.local/share/claude`, `~/.local/bin/codex` in the agent's home | removes the agent's `claude` and `codex` |
| `/etc/claude-code/managed-settings.d/50-keelbox.json`, `/etc/codex/requirements.toml` | removes keelbox's notification hooks |
| Docker, Tailscale, gh and the base packages | remove them with `apt`; mise is the single file `/usr/local/bin/mise` |
| The accounts | `sudo userdel -r agent`, and `sudo userdel -r admin` from another account with sudo |

Every path keelbox writes is in [files on the box](../reference/box-files.md).
