# 0009: Lock the cloud image's default user, from an admin session

Date: 2026-11-06. Status: accepted.

## Context
Cloud and LXD images create a default account (`ubuntu`) with the user's SSH key and `NOPASSWD:ALL` sudo through `/etc/sudoers.d/90-cloud-init-users`. After keelbox creates the admin account, that default account is a second, unmanaged root login. The first apply usually runs as that very account, so locking it carelessly locks the user out if the admin key turns out not to work.

## Decision
- The accounts to lock are exactly those named in `90-cloud-init-users`, minus the admin and agent accounts. A machine cloud-init never set up has no such file, so hand-made accounts on home servers are never touched.
- Lock means `usermod --lock --expiredate 1`: password locked and account expired. PAM's account check then refuses both SSH (with any key) and sudo; verified on Ubuntu 24.04. Nothing is deleted. Undo: `sudo usermod --unlock ubuntu && sudo chage -E -1 ubuntu`.
- The step defers (ADR 0008) while keelbox runs as that account (`SUDO_USER`). After a bootstrap apply (`--ssh-user ubuntu`, or `keelbox create`), the CLI applies again as the admin; that run proves the admin can log in and use sudo, then locks the default account.
- `users.lock_cloud_user: false` keeps the account.

## Consequences
- On Hetzner the bootstrap account is root, already refused by `PermitRootLogin no`; the second apply still verifies admin access.
- Sessions already open as the default account keep running until they end.
