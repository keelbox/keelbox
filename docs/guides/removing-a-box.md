# Remove a box

```sh
keelbox destroy acme    # a cloud server keelbox created: delete it and its disk
keelbox forget acme     # any other machine: stop managing it from this computer
```

`destroy` cannot be undone: the server, its disk and everything on them are gone. It takes no backup first, so work that was not pushed or copied off is lost. `forget` changes this computer only. To stop the server's bill and keep the disk, [park the box](resize-park-resume.md#park) instead.

## Delete a cloud server

```text
  • Checking devbox for work that exists only there
~/projects on devbox holds work that exists nowhere else:
  shop   not a git repository
  api    3 uncommitted changes, 2 commits not pushed
  notes  no remote
Deleting the server deletes it. To keep it, commit and push as the agent (keelbox ssh
devbox), or copy it off, before you delete.
? Delete this work with the server? [y/N]
```

destroy first reads the projects of the agent, the account the AI tools run as. It looks at the directories directly under `~/projects`, and at git only. It does not see other directories, Docker volumes, databases, stashes, or files a `.gitignore` leaves out.

With work found, or a box that does not answer, it asks, and No is preselected. Then it asks you to type the box name.

| Flag | Effect |
|---|---|
| `--yes` | Skips typing the box name. The question about work is still asked. |
| `--discard-work` | Skips the look at the projects. |

Without a terminal destroy exits in place of asking, unless both flags are given.

| Cloud | What destroy deletes |
|---|---|
| Hetzner | the server and the firewall `keelbox-acme` |
| AWS | the Lightsail instance, the static IP `keelbox-acme` and every snapshot keelbox made of the box |
| DigitalOcean | the Droplet, the firewall `keelbox-acme` and the tags keelbox made |
| Google Cloud | the VM with its disk, the firewall rules `keelbox-acme-allow` and `keelbox-acme-deny`, and the reserved address `keelbox-acme` |

Each of these is deleted only when it carries keelbox's label, tag or description for this box. A resource of the same name that keelbox did not make stops the command and stays.

On a parked box, destroy deletes the snapshot and what park kept beside it. There is no server to read projects from, so it asks `Delete it without knowing?` first. `keelbox resume acme` brings the box back to look.

If `keelbox up` stopped after creating a firewall or an address and before the server, destroy removes those and then reports that there is no server.

## After destroy

The box's config stays in `~/.config/keelbox/boxes/acme.yaml`, and `keelbox ls` shows the box as `server destroyed`, with the date.

| Next | Command |
|---|---|
| A new, empty server from the same config | `keelbox up acme` |
| Remove the box from this computer | `keelbox forget acme` |

## Forget a box

`keelbox forget acme` removes the box's config, what keelbox noted beside it, and the host keys pinned for it. The machine is not contacted. It asks first, with No preselected; `--yes` skips the question.

For a machine keelbox did not create, delete or wipe it at its provider; forget only ends keelbox's part.

On a cloud box whose server may still exist, forget shows what stays billed, and the menu preselects keeping the box:

```text
╭─ Forget gamma ───────────────────────────────────────────────────────────────────╮
│ removed here  ~/.config/keelbox/boxes/gamma.yaml, what keelbox noted about gamma │
│               beside it, and the host keys pinned for it                         │
│ still billed  The Hetzner server keeps running and is billed until it is deleted │
│ to delete it  keelbox destroy gamma first: forget removes what destroy needs to  │
│               find it. After forget, only the Hetzner Console deletes it         │
╰──────────────────────────────────────────────────────────────────────────────────╯
? Forget gamma and leave what is billed in the cloud?
  › 1  Keep gamma     nothing changes
    2  Forget gamma   what is billed stays until you delete it in the Hetzner Console
```

## What stays elsewhere

Neither command removes these. `destroy` lists them when it ends, and first signs a box it can reach out of Tailscale.

- The machine in the Tailscale admin console, and any auth key made for it.
- The agent's sign-ins. Revoke its GitHub token, and its Claude and OpenAI sessions, in those accounts' settings.
- Deploy keys and API tokens you gave the agent.
- The Hetzner token in this computer's credential store. `keelbox forget-token` removes it there; it works until you delete it in the Hetzner Console.
- The files `hetzner.token` and `tailscale.authkey` in `~/.config/keelbox`, if you made them.
- The keelbox binary and its completions: [Install keelbox](installing.md) lists them.

## Keep the machine, undo keelbox

A box that is forgotten keeps working as it was set up, and keelbox's timers on it keep running: [what a box does unattended](../concepts/how-it-works.md#what-a-box-does-unattended).

keelbox has no command that undoes its changes. These do most of it, as the admin on the box:

```sh
# the agent's services: its tmux server and Remote Control stop
sudo loginctl disable-linger agent

# keelbox's timers, path units and services, before the binary they run
cd /etc/systemd/system
units=$(ls keelbox-*.timer keelbox-*.path keelbox-*.service 2>/dev/null)
sudo systemctl disable --now $units
sudo rm -f $units
sudo systemctl daemon-reload

# keelbox's files and state
sudo rm -rf /etc/keelbox /var/lib/keelbox /var/lib/keelbox-preview /var/cache/keelbox \
  /usr/local/lib/keelbox/docker-user-guard.sh /usr/local/bin/keelbox

# the image's default account, if keelbox locked it
sudo usermod --unlock ubuntu && sudo chage -E -1 ubuntu
```

`usermod` warns that the account has no password. [The image's default account](adopting-a-box.md#the-images-default-account) says why that is expected.

The commands leave these in place:

| What | Removing it |
|---|---|
| `/etc/ssh/sshd_config.d/01-keelbox.conf` | lets sshd take password and root logins again, as far as its other settings allow |
| UFW | removes the inbound firewall |
| `/etc/sudoers.d/90-keelbox-admin` | removes the admin's sudo; do it from another account with sudo |
| `~/.local/bin/claude`, `~/.local/share/claude`, `~/.local/bin/codex` in the agent's home | removes the agent's `claude` and `codex`, which the vendors' installers put there |
| `/etc/claude-code/managed-settings.d/50-keelbox.json` and `/etc/codex/requirements.toml` | removes keelbox's notification hooks |
| Docker, Tailscale, gh and the base packages | `apt` removes them; mise is the one file `/usr/local/bin/mise` |
| The accounts | `sudo userdel -r agent`, and `sudo userdel -r admin` from another account with sudo |

Every path keelbox writes is in [files on the box](../reference/box-files.md).
