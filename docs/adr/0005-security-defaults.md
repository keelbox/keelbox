# 0005: Hardened defaults

Date: 2026-11-06. Status: accepted.

A setup for a single trusted user on a trusted LAN can leave the firewall off, allow SSH passwords and give the only user passwordless sudo. keelbox boxes face the internet, so each of those defaults is reversed.

| Area | Default | Notes |
|---|---|---|
| SSH | Key-only (`PasswordAuthentication no`, `KbdInteractiveAuthentication no`, `AuthenticationMethods publickey`), `PermitRootLogin no` | Drop-in `sshd_config.d/01-keelbox.conf`: sshd keeps the **first** value per keyword, so it must sort before cloud-init's `50-cloud-init.conf`. Refuses to apply until the admin has a key; validated with `sshd -t` and rolled back on failure; reload, never `enable --now ssh` (Ubuntu 24.04+ socket activation). Doctor checks effective `sshd -T`. |
| Accounts | `admin`: key-only, no password, sudo NOPASSWD (it has no password to ask for). `agent`: no sudo unless `users.agent.sudo: true`. | Docker group is root-equivalent: agent without sudo is defence in depth, not a boundary. |
| Firewall | UFW on: deny inbound, allow outbound, allow 22/tcp (unless `public_ssh: false`), allow all on `tailscale0` | Docker's iptables rules bypass UFW for published ports. Containers must bind 127.0.0.1; the primer tells agents. Closing public SSH is refused while the tailnet is down. `allow`, not `limit`: key-only SSH makes rate limiting low value and it locks out tooling. |
| Updates | unattended-upgrades on, never auto-reboot unless `host.auto_reboot: "HH:MM"` | Agents may be mid-task. |
| Supply chain | apt keys pinned by fingerprint; mise checksum-pinned; installers downloaded to a file before running | |
| Secrets | Tailscale auth key and Hetzner token by file path, mode 600 enforced, never on a command line or in output; staged auth key on tmpfs `/run/keelbox`, deleted after apply | Cloud-init user data carries nothing secret (Hetzner's metadata service serves it to every process). |
| Agent credentials | never touched | Users sign in to each CLI themselves. |
| Tailscale | installed; `tailscale up` left to the user unless an auth key file is given | Never Funnel, Serve or Tailscale SSH. |
| Remote access | keelbox's own `known_hosts`, accept-new (TOFU), entries forgotten on create/destroy | |
