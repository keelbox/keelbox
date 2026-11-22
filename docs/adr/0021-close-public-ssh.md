# 0021: Close public SSH once the admin is proven to reach sshd over the tailnet

Date: 2026-11-06. Status: accepted. Supersedes the `host.firewall.public_ssh` bool of ADR 0002 and the "refuse while the tailnet is down" check of ADR 0005.

## Context
A box on a tailnet does not need 22/tcp open to the internet, and keelbox should close it. Closing it on a guess locks the user out: Tailscale can be up on the box while the laptop is on another tailnet, ACLs can deny SSH, or the box can report addresses the laptop cannot reach. The only trigger that cannot lock the user out is a login that has already worked over the tailnet, from the machine running keelbox, as the account that will need it.

## Decision

### The proof is checked on the box
`keelbox apply` from a laptop runs `keelbox apply --local` over SSH. The remote script passes the session's `SSH_CONNECTION` to the box binary (`KEELBOX_SSH_CONNECTION`). The box counts a run as proof only if all of these hold:
- the connection arrived at one of the box's own Tailscale addresses (`tailscale status --json`, `Self.TailscaleIPs`) that is also on `tailscale0` (`ip -j addr show dev tailscale0`), with the tailnet `Running`;
- the account keelbox ran as is the admin (`target.ssh.user`), not the image's default account.

The box does not take the laptop's word for it, and no extra port, agent or service is involved. The proof (address, client address, user, time) is written to `/var/lib/keelbox/public-ssh.json` (0644, no secrets) with the state.

### What closes
- UFW: the `allow in on tailscale0` rule is added first, then the public `22/tcp` rule is deleted.
- sshd: `/etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf` holds `Match LocalAddress *,!100.64.0.0/10,!fd7a:115c:a1e0::/48,!127.0.0.0/8,!::1` with `DenyUsers *`, so a login arriving at a non-tailnet, non-loopback address is refused even if UFW is off. This is the equivalent of `ListenAddress` without its failure: sshd started before `tailscaled` cannot bind a tailnet address and would not listen at all; a Match on the local address needs no restart ordering and keeps socket activation. `sshd -t` validates it before it is kept, and sshd is reloaded when running. Checked on OpenSSH 9.6 (Ubuntu 24.04) and 10.2 (Ubuntu 26.04): the Match ends at the end of the drop-in, settings in later drop-ins and `sshd_config` are unchanged, and `sshd -T -C` differs between a public and a tailnet local address only in `denyusers *`.
- Hetzner: the Cloud Firewall loses its 22 rule (ADR 0022).

### Modes
`host.firewall.public_ssh`:
- `auto` (default): closes on proof. A run over the public address, a changed tailnet address, or the tailnet stopping opens it again and drops the proof. A run on the box, or over the tailnet as another account, keeps a proof that still holds.
- `open`: never closes.
- `closed`: closes on proof like `auto`; keeps an old proof while the tailnet is down; without proof, apply leaves SSH open, defers the step and doctor fails. It never closes without proof.
- `true` and `false` are also read as `open` and `closed`.
Anything but `open` needs `access.tailscale`.

### The laptop finds the tailnet
After an apply over the public address with the box on the tailnet, keelbox reads `tailscale status --json` over SSH, picks `target.ssh.tailnet_host` or the box's first Tailscale IPv4, copies the host keys pinned for the public address to that name (nothing new is trusted), waits up to about 30 seconds for sshd there and applies again over it: that run is the proof. If the address cannot be reached it says so and leaves SSH open. The address it reached is kept in `boxes/<box>.json` beside keelbox's `known_hosts`; every later command (`apply`, `doctor`, `status`, `ssh`, `attach`, `sessions`, `login`) tries it first and falls back to the public address with a note. `destroy` removes the record and the pins.

### Recovery
Without the tailnet the box is reachable only from the provider console. Hetzner: add TCP 22 back to the box's Cloud Firewall in the console, then boot the rescue system, mount the disk, delete the drop-in and `public-ssh.json`, set `ENABLED=no` in `/etc/ufw/ufw.conf`, and reboot; the console login needs a password the admin does not have, so the rescue system is the way in. With the tailnet working, `public_ssh: open` and `keelbox apply` (with `--ssh-user` if needed) reopen it. docs/guides/public-ssh.md has the steps.

## Consequences
- The first apply on a fresh box leaves SSH open; the one after `keelbox login` joins the tailnet closes it.
- An apply from a laptop that is not on the tailnet, while the public address still works, reopens public SSH in `auto`. That is deliberate: it is the run that would otherwise have been locked out next time.
- Tested by sim tests (decision table, convergence, reopen paths, closed-mode defer), and end to end on LXD with a stand-in tailnet (a macvlan named `tailscale0` and a DNAT to a tailnet address) including the no-proof path, and on Hetzner through a tunnel. A real tailnet is used only when `~/.config/keelbox/tailscale-e2e.key` exists.
