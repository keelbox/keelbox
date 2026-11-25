# Check the box and fix problems

```sh
keelbox status acme    # a short summary
keelbox doctor acme    # every check; exit 1 on any failure
keelbox ls             # every box this computer manages
```

None of these changes the box. `keelbox status --all` asks every box at once. After you fix a cause, run `keelbox apply acme`: it changes only what still differs from the config.

## Reading doctor

```text
sign-in
  ✓  tailscale                signed in
  !  gh                       not signed in: keelbox login acme gh
```

`✓` passed, `!` is a warning, `✗` a failure (`ok`, `warn`, `FAIL` without a terminal). A line that did not pass says why, and most end with the command that clears it. `drift, apply would change:` means a setting no longer matches the config: run `keelbox apply acme`. Doctor also runs on the box hourly by default, and `keelbox status` shows that result.

The box records every apply in its logbook. To read the last five, as the admin:

```sh
keelbox ssh acme --admin
sudo tail -n 5 /var/lib/keelbox/logbook.jsonl | jq .
```

`result` is `ok`, `partial`, `failed` or `interrupted`. The fields are in [files on the box](../reference/box-files.md).

## Connecting

### Public SSH stays open

`public SSH stays open: ...` after an apply, or a `public-ssh` warning in doctor. keelbox closes SSH from the internet only once it has reached the box over the tailnet. If the box is not on the tailnet:

```sh
keelbox login acme tailscale
keelbox apply acme
```

If this computer is not on the tailnet, join it and apply again. See [closing public SSH](public-ssh.md).

### Locked out after public SSH closed

SSH at the public address is refused and the tailnet does not reach the box. Follow [getting back in](public-ssh.md#getting-back-in).

### The host key changed

`Host key verification failed`, or ssh warns that the host identification has changed, usually because the server was rebuilt at the same address. Check the new key in the provider's console, then:

```sh
ssh-keygen -R 203.0.113.10 -f ~/.config/keelbox/known_hosts
```

`does not offer the host key ... that target.ssh.host_key pins` means the server's key differs from the one in the box's config. If the server was reinstalled, compare its new key, then set `host_key` under `target.ssh` in the file the message names.

### Permission denied (publickey)

Before the first apply, connect as the image's account:

```sh
keelbox apply acme --ssh-user ubuntu    # or root
```

After it, keelbox connects as the admin with the keys under `users.admin`. If your private key is not the default one, set `target.ssh.identity_file`.

### sudo: a password is required

The account keelbox connects as needs a password for sudo. Run `keelbox up acme` in a terminal so sudo can ask you, or connect as root:

```sh
keelbox apply acme --ssh-user root
```

### The box binary cannot be downloaded

`fetch keelbox ... for linux/amd64`. This computer downloads the box's keelbox once per version from `get.keelbox.dev` and `github.com`. Check that both are reachable from here.

## Applying

### The machine is in use

`acme is in use, and this first apply would cut off or replace what it runs. Nothing has changed.` The message says how to keep each item. To accept every change instead:

```sh
keelbox apply acme --adopt
```

See [a machine already in use](machine-in-use.md) for what that accepts.

### An LXC container

`acme is an LXC container, and nothing has changed`. Docker and the agents' sandboxes need nesting on, and Tailscale needs `/dev/net/tun`. Only the container's host can change either, so the message prints the commands for it. Run them there, then `keelbox apply acme`.

On LXD or Incus the commands carry the container's name, or `NAME` when keelbox cannot read it; add `--project` or a `remote:` prefix as you would for that container. `Device already exists` means a `tun` device is there but does not work: `lxc config device remove NAME tun`, then add it again.

### A Proxmox container

`CTID` is the container's number. `pct set` replaces the whole features list and the whole `devN` entry, so read them first:

```sh
pct config CTID | grep -E '^(features|dev[0-9])'
pct set CTID --features nesting=1,keyctl=1   # keep the features listed above, comma-separated
pct set CTID --dev1 /dev/net/tun             # a devN not listed above
pct reboot CTID
```

In the web interface: Options, Features for nesting; Resources, Add, Device Passthrough for the device, from Proxmox 8.2. `--devN` needs Proxmox 8.1 or later. On older releases, add these lines to `/etc/pve/lxc/CTID.conf`, then reboot the container:

```text
lxc.cgroup2.devices.allow: c 10:200 rwm
lxc.mount.entry: /dev/net/tun dev/net/tun none bind,create=file
```

### The box cannot reach GitHub

`acme reaches the internet over IPv6 only`, or `acme cannot reach github.com`. Caddy, mise, restic and the tmux plugins come from GitHub, which has no IPv6 address. Until a box is set up, apply checks it reaches github.com and stops before any change when it does not.

Give the server IPv4 internet access (in your provider's console, or on its network), or route an IPv6-only server through NAT64 with DNS64. A private IPv4 address needs NAT. With IPv4, the reason in brackets says what failed: allow github.com on port 443, check the clock and DNS, or wait out a GitHub outage. Then apply again:

```sh
keelbox apply acme
```

### Apply would take SSH keys off

`this apply would take 1 SSH key off acme that an apply put there` means the config lists fewer keys than applies have put on the box. It is usually the same config applied from a second computer: `ssh_authorized_key_files` is read on the computer that runs keelbox, so `~/.ssh/id_ed25519.pub` is another key there. Nothing has changed. To keep the key, add each line the message prints under the account's `ssh_authorized_keys` and apply again. To take it off, for a lost laptop say, run `keelbox apply acme --remove-keys`. Setting up a second computer: [from a second computer](sessions.md#from-a-second-computer).

### Another keelbox apply is running

An apply started on the box holds the lock. Wait, or look for a stuck one:

```sh
keelbox ssh acme --admin
pgrep -a keelbox
```

### The box is on a newer release

`the box is on release ...; this keelbox carries the older release ...`. Another computer applied a newer keelbox. Update this one:

```sh
keelbox self-update
```

To move the box back instead, see [the downgrade guard](upgrades.md#the-downgrade-guard).

### An agent CLI did not install

`agents/claude-code failed`, exit 1, `partial` in the logbook. The rest of the box is applied, and the logbook has the vendor installer's error. A failed download usually passes on a second try:

```sh
keelbox apply acme
```

### Steps skipped for one account

`skip ...: agent's step ... failed`, exit 1, `partial` in the logbook. A path in that account's home stopped a step, usually a symlink, a file root owns or a missing permission; the first failure names it. Only that account's later steps were skipped. Fix or move the path as that account (`keelbox ssh acme` for the agent), then apply.

### Closing your laptop during setup

On a server, apply runs on the box as a systemd unit, so a closed lid or lost Wi-Fi does not stop it. Ctrl-C asks whether to leave it running (preselected) or stop after the current step. A dropped connection is retried for 3 minutes, then keelbox exits 4. To come back:

```sh
keelbox up acme
```

It follows an apply that is still running, or reports how a finished one went, then does the steps left. The full log is on the box: `sudo journalctl -u 'keelbox-apply-*'`.

Setting up the machine you are on runs in your terminal, so closing it ends the apply. Run `keelbox up` again to carry on.

### Setup stopped without finishing

`setup on acme stopped without finishing`, a failing `setup-run` in doctor, or `interrupted` in `keelbox status`. The box restarted during an apply, or the apply was killed. Steps before that point are in place:

```sh
keelbox up acme
```

## Doctor failures

### A container setting

`container-nesting` or `container-tun` fails: the container's host must change a setting. `keelbox apply acme` prints the commands; see [an LXC container](#an-lxc-container).

### An SSH setting

`ssh-password-auth`, `ssh-kbd-interactive`, `ssh-root-login` or `ssh-agent-forwarding` fails. A file that sorts before `01-keelbox.conf` in `/etc/ssh/sshd_config.d/`, or a `Match` block in `sshd_config`, overrides keelbox's setting. Find it as the admin, remove it, and apply:

```sh
sudo sshd -T | grep -i password
ls /etc/ssh/sshd_config.d/
```

### A Docker check

| Check | Cause | Look, on the box |
|---|---|---|
| `docker-rootless` | the agent's Docker daemon does not answer | as the agent: `systemctl --user status docker` |
| `docker-system-socket` | the agent can use root's Docker daemon | as the admin: `ls -l /var/run/docker.sock` |
| `docker-inbound-guard` | the firewall chain for containers' published ports is missing | as the admin: `sudo systemctl restart keelbox-docker-guard` |

`keelbox apply acme` removes the agent from the `docker` group and installs the guard again.

### The network limits

keelbox stops every account but root from reaching link-local addresses and the box's own SSH port.

| Check | Cause | Fix |
|---|---|---|
| `agent-net` | the loaded rules are not the ones apply would load | `keelbox apply acme` |
| `resolver` | every nameserver in `/etc/resolv.conf` is link-local, so only root resolves names | the command below, as the admin |
| `link-local-accounts` (warning) | `host.firewall.link_local_accounts` names an account that gets no rule: not on the box, root, or the agent's | remove the name and apply |

```sh
sudo systemctl enable --now systemd-resolved && sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
```

### Remote Control needs you

`claude-remote-control` warns `needs you: signed in but not running`: Claude Code's two first-run questions are unanswered. Answer them:

```sh
keelbox login acme claude-code
```

Its log, as the agent: `journalctl --user -u keelbox-claude-remote-control.service`.

### Agent sandboxes

Doctor fails `sandbox` or `codex-sandbox`, or Codex stops with `bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted`. Claude Code and Codex run commands under bwrap, which Ubuntu allows only with an AppArmor profile; on 24.04 keelbox loads Ubuntu's as `/etc/apparmor.d/keelbox-bwrap`.

In an LXC container, `bwrap: Can't mount proc on /newroot/proc: Operation not permitted` means nesting is off: see [an LXC container](#an-lxc-container).

Run `keelbox apply acme` first. If the step `bwrap-userns` fails, the box's apt sources lack `noble-updates`, where Ubuntu ships the profile. Otherwise, as the admin:

```sh
sudo aa-status | grep bwrap
sudo apparmor_parser -r /etc/apparmor.d/keelbox-bwrap
sudo dmesg | grep -i apparmor | tail
```

Setting `kernel.apparmor_restrict_unprivileged_userns = 0` instead gives every program the agent runs a user namespace with capabilities.

### Claude Code's own sandbox

`apply-seccomp: write /proc/self/setgroups ... Permission denied` after `/sandbox` in Claude Code. Its Unix socket filter needs capabilities inside bwrap, which Ubuntu's profile denies on 24.04 and 26.04. The sandbox runs with this in the agent's `~/.claude/settings.json`:

```json
{"sandbox": {"network": {"allowAllUnixSockets": true}}}
```

Commands in the sandbox can then open every Unix socket the agent can, its Docker socket included. keelbox leaves this setting to you.

## Report a bug

Open an issue with the output of these, minus any addresses and names you want kept private:

```sh
keelbox version
keelbox doctor acme
```

Add the provider, the Ubuntu version and the failing command run with `--verbose`. Report a security problem as [SECURITY.md](../../SECURITY.md) says instead.
