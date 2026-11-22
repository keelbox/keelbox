# Check the box and fix problems

```sh
keelbox status acme    # a short summary
keelbox doctor acme    # every check; exit 1 on any failure
keelbox ls             # every box this computer manages
```

None of the three changes the box. Doctor is keelbox's set of checks: it reads what is in effect on the running box, and compares the box with its config.

## Reading doctor

```text
sign-in
  ✓  tailscale                signed in
  !  gh                       not signed in: keelbox login devbox gh

✓ 5 ok · ! 4 warnings · 0 failures
```

Each line is one check, grouped by the module that owns it. `✓` passed, `!` is a warning and `✗` a failure; without a terminal they read `ok`, `warn` and `FAIL`. A line that did not pass says why, and most end with the command that clears it.

A warning that starts `drift, apply would change:` names a setting that no longer matches the config. Apply is the command that makes the box match its config, and it can be repeated at any time:

```sh
keelbox apply acme
```

Doctor also runs on the box every hour. `keelbox status` shows the result of that run, the agent CLIs' versions and the projects that hold work found only on the box.

## Several boxes

```text
BOX     PROVIDER          ADDRESS       STATUS
beta    ssh               203.0.113.8   ! needs sign-in: keelbox up beta
delta   hetzner           -             • parked since 2026-10-01: keelbox resume delta
devbox  ssh               203.0.113.7   ✓ ready
```

`keelbox ls` reads this computer only. `keelbox status --all` asks every box, and exits 1 unless each one answers and has no failure.

A command that names no box uses the only box there is. With several it asks "Which box?", with the box a command last used highlighted. Without a terminal it lists the boxes and exits.

## The logbook

The logbook is the box's record of every apply: one JSON line each, with the result and every step changed or failed. Read it as the admin, the account with sudo:

```sh
keelbox ssh acme --admin
sudo tail -n 5 /var/lib/keelbox/logbook.jsonl | jq .
```

`result` is `ok`, `partial`, `failed` or `interrupted`. The fields are in [files on the box](../reference/box-files.md).

## Problems

After fixing a cause, run `keelbox apply acme` again. It checks every step and changes only what still differs.

### Public SSH stays open

`public SSH stays open: ...` after an apply, or a `public-ssh` warning in doctor. keelbox closes SSH from the internet only once it has reached the box over the tailnet, your private Tailscale network. The message says which part is missing.

The box is not on the tailnet:

```sh
keelbox login acme tailscale
keelbox apply acme
```

This computer cannot reach the box there: join it to the same tailnet and apply again. More under [closing public SSH](public-ssh.md).

### Locked out after public SSH closed

SSH at the public address is refused and the tailnet does not reach the box. Follow [getting back in](public-ssh.md#getting-back-in).

### The host key changed

`Host key verification failed`, or ssh warns that the host identification has changed. keelbox pinned another key for that address, usually because the server was rebuilt and kept its address. Compare the new key with the provider's console, then remove the old one and connect again:

```sh
ssh-keygen -R 203.0.113.10 -f ~/.config/keelbox/known_hosts
```

`does not offer the pinned host key` means the key differs from `target.ssh.host_key` in the config. Set that key to the server's new one.

### Permission denied (publickey)

Before the first apply, connect as the account the server's image has, `ubuntu` or `root`:

```sh
keelbox apply acme --ssh-user ubuntu
```

After the first apply keelbox connects as the admin, with the keys under `users.admin`. If your private key is not the default one, set `target.ssh.identity_file`.

### sudo: a password is required

The account keelbox connects as has sudo with a password, and nothing can type it. `keelbox up acme` in a terminal lets sudo ask you. Without a terminal, connect as root:

```sh
keelbox apply acme --ssh-user root
```

### The machine is in use

`acme is in use, and this first apply would cut off or replace what it runs. Nothing has changed.` The first apply found a listening service, a published container port, a password login, a conflicting Docker package or a resolver only root would reach. The message says what keeps each one, with the YAML for the ports. To accept every change instead:

```sh
keelbox apply acme --adopt
```

[A machine already in use](machine-in-use.md) lists what `--adopt` replaces.

### Another keelbox apply is running

An apply started on the box holds the lock. Wait for it, or look for one that is stuck:

```sh
keelbox ssh acme --admin
pgrep -a keelbox
```

An apply started from another computer does not give this error: keelbox follows the one that is running.

### The box is on a newer release

`the box is on release ...; this keelbox carries the older release ...`. Another computer applied the box with a newer keelbox. Update this one:

```sh
keelbox self-update
```

[Update keelbox](upgrades.md) has the flag that moves a box back.

### An agent CLI did not install

`agents/claude-code failed`, exit 1, and `partial` in the logbook. The rest of the box is applied. keelbox runs each vendor's installer as the agent, and the logbook has the installer's error. A failed download usually passes on a second try:

```sh
keelbox apply acme
```

### Steps skipped for one account

`skip ...: agent's step ... failed`, exit 1, and `partial` in the logbook. A path in that account's home stopped a step: usually a symlink, a file root owns or a missing permission. The first failure names the path. The account's later steps were skipped; every other step ran.

Fix or move the path as that account, then apply:

```sh
keelbox ssh acme
```

### Doctor fails an SSH setting

`ssh-password-auth`, `ssh-kbd-interactive`, `ssh-root-login` or `ssh-agent-forwarding` fails. sshd's effective setting is not the one keelbox wrote in `01-keelbox.conf`. A file that sorts before it in the same directory, or a `Match` block in `sshd_config`, overrides it. As the admin:

```sh
sudo sshd -T | grep -i password
ls /etc/ssh/sshd_config.d/
```

Remove the override and apply.

### Doctor fails a Docker check

| Check | Cause | Look, on the box |
|---|---|---|
| `docker-rootless` | the agent's Docker daemon does not answer | as the agent: `systemctl --user status docker` |
| `docker-system-socket` | the agent can use root's Docker daemon | as the admin: `ls -l /var/run/docker.sock` |
| `docker-inbound-guard` | the firewall chain for containers' published ports is missing | as the admin: `sudo systemctl restart keelbox-docker-guard` |

Apply removes the agent from the `docker` group and installs the guard again.

### Doctor fails the network limits

keelbox refuses link-local addresses and the box's own SSH port to every account but root.

| Check | Cause | Fix |
|---|---|---|
| `agent-net` | the loaded table is not the one apply would load now | `keelbox apply acme` |
| `resolver` | every nameserver in `/etc/resolv.conf` is link-local, so only root resolves names | the command below, as the admin |
| `link-local-accounts`, a warning | `host.firewall.link_local_accounts` names an account that gets no rule: not on the box, root, or the agent's | remove the name and apply |

```sh
sudo systemctl enable --now systemd-resolved && sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
```

### Remote Control needs you

`claude-remote-control` warns `needs you: signed in but not running`. Claude Code's two first-run questions have no answer yet. Answer both in a terminal:

```sh
keelbox login acme claude-code
```

Its log, as the agent on the box:

```sh
journalctl --user -u keelbox-claude-remote-control.service
```

### Agent sandboxes

Doctor fails `sandbox` or `codex-sandbox`, or Codex stops with `bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted`. Codex and Claude Code run commands in their sandbox under bwrap, which Ubuntu allows only with an AppArmor profile. On 24.04 keelbox loads Ubuntu's own profile as `/etc/apparmor.d/keelbox-bwrap`.

Apply first. If the step `bwrap-userns` fails, the box's apt sources lack `noble-updates`, where Ubuntu ships the profile. Otherwise, as the admin:

```sh
sudo aa-status | grep bwrap
sudo apparmor_parser -r /etc/apparmor.d/keelbox-bwrap
sudo dmesg | grep -i apparmor | tail
```

Setting `kernel.apparmor_restrict_unprivileged_userns = 0` instead gives every program the agent runs a user namespace with capabilities.

### Claude Code's own sandbox

`apply-seccomp: write /proc/self/setgroups ... Permission denied` after `/sandbox` in Claude Code. Its Unix socket filter needs capabilities inside bwrap, which Ubuntu's profile denies on 24.04 and 26.04.

The sandbox runs with this in the agent's `~/.claude/settings.json`:

```json
{"sandbox": {"network": {"allowAllUnixSockets": true}}}
```

Commands in the sandbox can then open every Unix socket the agent can, its Docker socket included. keelbox leaves the setting to you.

### Closing your laptop during setup

On a server, apply runs on the box as a systemd unit, and this computer follows its log. A closed lid or lost Wi-Fi does not stop it.

| Event | keelbox |
|---|---|
| Ctrl-C | asks whether to leave it running, which is preselected, or stop after the current step |
| The connection drops | tries again for 3 minutes, then exits 4 |

To come back, run the same command again:

```sh
keelbox up acme
```

It follows an apply that still runs, or says how a finished one went, then does the steps left. Every line is on the box:

```sh
sudo journalctl -u 'keelbox-apply-*'
```

Setup of the machine you are on runs in your terminal. Closing it ends the apply where it is; `keelbox up` carries on.

### Setup stopped without finishing

`setup on acme stopped without finishing`, a failing `setup-run` in doctor, or `interrupted` in `keelbox status`. The box restarted during an apply, or the apply was killed. Every step before that point is in place. Carry on:

```sh
keelbox up acme
```

### The box binary cannot be downloaded

`fetch keelbox ... for linux/amd64`. This computer downloads the keelbox that runs on the box once for each version, over HTTPS from `get.keelbox.dev` and `github.com`, and keeps it. Check both hosts are reachable from here.

### Report a bug

Open an issue with the output of these, after removing addresses and names you want kept private:

```sh
keelbox version
keelbox doctor acme
```

Add the provider, the Ubuntu version and the failing command run with `--verbose`. A security problem goes to [SECURITY.md](../../SECURITY.md) instead.
