# 0028: A cloud-config snippet for servers created by hand

Date: 2026-11-06. Status: accepted.

## Context
Many providers have no keelbox provider, and one that does needs an API token or a signed-in cloud CLI, the step users find hardest (ADR 0026, ADR 0027). Every mainstream provider's console has a "Cloud config" or "User data" field when creating a server, and Ubuntu cloud images run cloud-init on it. A server created that way, with the user's key already on an admin account, is an SSH target keelbox can apply to.

## Decision
- `keelbox up` offers "A server I create myself". It prints a `#cloud-config` document and keeps it, mode 600, in `~/.config/keelbox/boxes/<box>.cloud-config` until it is no longer needed. The document:
  - creates `admin` with the user's SSH public key, `/bin/bash`, a locked password, `primary_group: admin` (Ubuntu images already have an empty `admin` group, and `useradd admin` fails without `-g`) and `sudo: ALL=(ALL) NOPASSWD:ALL`, so keelbox's first apply can use `sudo -n` as the admin it would create anyway. The list has no `default` entry, so the image's own account (`ubuntu`) is not created;
  - sets `ssh_pwauth: false` and `disable_root: true`;
  - by default, includes a host key pair keelbox generated with `ssh-keygen` (`ssh_keys` and `ssh_deletekeys: true`; with `ssh_keys` set cloud-init generates no others), as `keelbox create` does on Hetzner (ADR 0007). `--no-host-key`, or answering no, leaves it out; the first connection then shows the fingerprints to compare, or uses `--host-key`.
- It holds no credentials: no API token, no passwords. The host key is single-use: providers serve user data through their metadata service, so keelbox pins the public half before the first packet, connects, and replaces the keys with ones generated on the server, then deletes its copy of the snippet.
- The user creates the server and gives keelbox its IP address (interactively, or `keelbox up <box> --host IP`). keelbox waits for SSH as `admin` (up to ten minutes for the port, five for login) and for `cloud-init status --wait`, then applies as the admin. The config written is an ordinary `provider: ssh` config.
- Meant for any provider whose Ubuntu 24.04 or newer images run cloud-init with user data. Tested end to end on LXD (`cloud-init.user-data`); the host key part is the same document `keelbox create` sends to Hetzner.

## Consequences
- Providers without a keelbox provider (Vultr, OVHcloud, others) work without any token in keelbox's hands; keelbox does not create, find or destroy those servers, and adds no provider firewall.
- If the user also selects an SSH key in the provider's form, the provider may add it for root or the default account; keelbox's apply turns off root login and locks a cloud default account as usual (ADR 0009).
- Anyone who can read the server's user data before keelbox's first connection holds that connection's host key. The window ends at first contact.
