# Files keelbox manages on a box

```sh
keelbox plan acme
```

Everything `keelbox apply` creates or changes on a box. `keelbox plan` shows the same steps with what would change.

A file keelbox owns outright starts with a "Managed by keelbox" comment and is rewritten on every apply: change the config instead. A file you also edit gets a marked block, from `# >>> keelbox ID >>>` to `# <<< keelbox ID <<<`, and text outside it is left alone.

Paths use the default account names, `admin` and `agent`. The root services that run between applies are called [unattended runs](../concepts/how-it-works.md#what-a-box-does-unattended) here.

## keelbox's own state

| Path | Mode and owner | What |
|---|---|---|
| `/usr/local/bin/keelbox` | 0755 root | The keelbox that last applied the box. Only `apply` installs it. |
| `/var/lib/keelbox/keelbox.previous` | 0755 root | The keelbox it replaced, for `keelbox rollback`. |
| `/var/lib/keelbox/logbook.jsonl` | 0644 root | The [logbook](#logbook). |
| `/var/lib/keelbox/stage/keelbox` | 0755 root, dir 0700 | The binary uploaded for the current or last remote command (`/run` is `noexec`). |
| `/var/lib/keelbox/stage/rollback.yaml` | 0600 root | The config a rollback applies when the one applied before lacks SSH keys a later apply added: that config with those keys kept. Kept until the next rollback. |
| `/etc/keelbox/keelbox.yaml` | 0600 root | The applied config, keys inlined, provider `local`. Commands on the box read it when no config is named. |
| `/etc/keelbox/ssh-keys.yaml` | 0600 root | The SSH keys applies put on the admin and the agent. apply stops before taking one off unless given `--remove-keys`. |
| `/etc/keelbox/AGENTS.md` | 0644 root | The primer: what keelbox tells the agent CLIs about the box. |
| `/run/keelbox/` | tmpfs, dir 0700, files 0600 | During a remote run only: the shipped config (`run-<id>.yaml` for a detached apply) and the secrets the run needs: Tailscale auth key, backup credentials and password, and channel secrets as `notify.<channel>.<key>`. Deleted when the run ends (secrets once no apply runs), and gone at reboot. |
| `/tmp/tmp.XXXXXXXXXX/` | dir 0700, the login account | At the start of a remote command only: scp uploads the binary, config and secrets here, and root moves them to the two places above before the command runs. Left behind if the upload or install fails. When sudo asks for a password, the output waits beside it in `tmp.XXXXXXXXXX.out`, 0600. Where `/tmp` is on disk, these files touch the disk. |
| `/var/lib/keelbox/runs/` | 0640 root:sudo, dir 0750 | One log per apply started from another computer, `<id>.jsonl`, and `current`, the newest run's id. At most 4 MiB each; the ten newest are kept. Readable by the `sudo` group, not by the agent. |
| `keelbox-apply-<id>.service` | transient | An apply started over SSH, run with `systemd-run` so it outlives the connection. Output: `journalctl -u 'keelbox-apply-*'`. |
| `/run/keelbox-start.lock` | 0600 root | Held while an apply starts, so two commands cannot start two runs. |
| `/var/lib/keelbox/previews/caddy.json` | 0640 root:keelbox-preview, dir 0755 root | The rendered Caddy config for previews. |
| `/var/lib/keelbox/previews/status.json` | 0644 root | What is published and any problems, read by `keelbox expose`. |
| `/var/lib/keelbox/previews/tls/` | 0750 root:keelbox-preview | The box's Tailscale certificate, `cert.pem` 0644, and key, `key.pem` 0640. |
| `/var/lib/keelbox-preview/` | 0700 keelbox-preview | Caddy's state directory. |
| `/etc/keelbox/outside-addresses` | 0644 root | Addresses the box's sshd is reached at from outside that none of its interfaces carries (a cloud's public address, a forwarded port), one `ADDRESS:PORT` or `[IPV6]:PORT` a line. apply adds those keelbox commands report, keeping the 16 newest. You may edit it: remove an address the box is no longer reached at, or add one, then run `keelbox apply`. |
| `/etc/keelbox/backup/` | 0700 root | `s3.credentials` and `restic.password`, 0600 copies of `backup.credentials_file` and `backup.password_file`. |
| `/var/lib/keelbox/public-ssh.json` | 0644 root | Whether public SSH is open or closed, why, and the tailnet proof. Your computer reads it to set the cloud firewall's SSH rule. |
| `/var/lib/keelbox/backup/status.json` | 0600 root, dir 0700 | The last backup attempt and success, for `doctor` and `status`. |
| `/var/cache/keelbox/restic/` | 0700 root | restic's cache. Deleting it only slows the next run. |
| `/var/lib/keelbox/restore/` | root | The default restore target for paths outside the agent's and admin's homes. |
| `/run/keelbox.lock` | 0600 root | Held exclusively by `apply`. Unattended runs hold it shared and skip while an apply holds it. |
| `/run/keelbox-daytwo.lock` | 0600 root | Held by one unattended run at a time. |
| `/run/keelbox-installs.lock` | 0600 root | Held by the install service, `keelbox approve` and `keelbox deny`, the only writers of `requests.json`. |
| `/var/lib/keelbox/watch/state.json` | 0644 root, dir 0755 | The watch run's heartbeat and state: watchdog restarts and give-ups, disk alerts sent, last session save, pending update count. Doctor fails when the heartbeat is over 10 minutes old. |
| `/var/lib/keelbox/watch/resume.json` | 0600 root | Written before keelbox reboots the box and at every orderly shutdown: each tmux pane that ran Claude Code or Codex and the command that reopens its conversation. Removed once replayed after boot, or 20 minutes after the boot if a pane did not come back. |
| `/var/lib/keelbox/power/state.json` | 0644 root, dir 0755 | The current boot and the last unclean shutdown found, and when it was reported. |
| `/var/lib/keelbox/power/clean-shutdown` | 0644 root | Touched at every orderly shutdown and removed at boot. A boot without it followed a power loss, crash or hard reset. |
| `/var/lib/keelbox/reports/<id>.json` | 0600 root, dir 0700 | A report the agent filed with `keelbox report`, plus box facts root added: keelbox version and release, Ubuntu version, architecture, size and failing doctor checks. Read and sent only by you, with [`keelbox reports`](../guides/reports.md). |
| `/var/lib/keelbox/status.json` | 0644 root | The [status snapshot](#the-status-snapshot), rewritten by every watch run. |
| `/var/lib/keelbox/notify/queue/` | 0700 root, files 0600 | One file per event waiting to be sent, dropped after 24 hours. |
| `/var/lib/keelbox/notify/cursor.json` | 0600 root | How far the agent's events file has been read. |
| `/var/lib/keelbox/notify/ratelimit.json` | 0600 root | Agent events let through, for the budgets. |
| `/var/lib/keelbox/notify/last.json` | 0600 root | Each channel's last send and error, without secrets. |
| `/var/lib/keelbox/notify/events-run` | 0600 root | When the events service last started (at most every 5 seconds). |
| `/etc/keelbox/notify/` | 0700 root | With `notify.channels`: 0600 copies of each channel's secret files, `<channel>.<key>`. Unused copies are removed, and the directory with the last channel. |
| `/var/lib/keelbox/doctor/last.json` | 0644 root, dir 0755 | The last scheduled doctor run: time, counts, findings that are not ok, and alerts out. |
| `/var/lib/keelbox/installs/mode.json` | 0644 root, dir 0755 | The box name, agent account and `users.agent.permissions`, read by the agent's `keelbox install`. The install service reads the applied config instead. |
| `/var/lib/keelbox/installs/requests.json` | 0644 root | Root's record of package requests: at most 20 waiting and the last 50 decided. Each has root's ID (which `keelbox approve` takes), the agent's ID, time, account, checked package names, the agent's stated reason, and a state with root's reason: `pending`, `approved`, `installing`, `installed`, `refused`, `failed` or `denied`. |
| `/var/lib/keelbox/installs/installed.json` | 0644 root | Packages installed for the agent: name, when, and which request. `keelbox status` lists them and flags those `host.packages` does not name. `apply` never removes them. |
| `/var/lib/keelbox/installs/told.json` | 0644 root | Packages apply last reported missing from `host.packages`, so it reports them once. |
| `/var/lib/keelbox/installs/run`, `apt-update` | 0600 root | When the install service last started (at most every 2 seconds) and last refreshed the package lists (at most hourly). |

## The status snapshot

`/var/lib/keelbox/status.json` holds, as of the last watch run:

- the release, the keelbox version, the agent account's name and whether keelbox reports are off;
- CPUs, memory, and disk use per mount;
- pending security updates and a needed reboot, counted hourly;
- the last apply, backup and boot, and whether that boot followed an unclean shutdown;
- the last scheduled doctor run's counts and failing checks;
- the agent CLIs' active, previous and blocked versions;
- the agent's CPU and memory use against its limits;
- the agent's tmux sessions and Remote Control's state;
- each directory under the agent's projects directory, with its git state: not a repository, not checked, no remote, or counts of uncommitted changes and unpushed commits;
- how many package requests wait for `keelbox approve`.

It holds no secrets, and every account reads it without sudo: that is how `keelbox status --all` and the agent's `keelbox status` read it.

## Logbook

`/var/lib/keelbox/logbook.jsonl` gets one record per apply, failed ones included, and one per action an unattended run takes. It holds what keelbox changed, never command output or secrets. Records are only appended.

```json
{"time":"2026-11-21T15:05:25.490349411Z","box":"acme","keelbox":"v0.1.0","release":"2026.11.4","config_hash":"a52f0a6337de","result":"ok","checked":74,"changes":[{"module":"logbook","step":"keelbox-binary","detail":"install this keelbox (v0.1.0) as /usr/local/bin/keelbox"}],"seconds":0.275171381}
```

Read it on the box with `sudo tail -n 3 /var/lib/keelbox/logbook.jsonl | jq .`.

| Field | Meaning |
|---|---|
| `time`, `box`, `keelbox`, `release` | When, which box, which binary and which release manifest. An unattended run's record carries the release of the last converged apply. |
| `kind` | Absent for an apply. `watch`, `doctor` or `power` for an unattended run's action, with `result` `ok` or `failed` and `changes` saying what it did. `install` for a package request. |
| `from_release` | Set when the apply moved the box to another release. |
| `config_hash` | The first 12 hex characters of the SHA-256 of the effective config. |
| `result` | `ok`, `partial`, `failed` or `interrupted`, as below. |
| `checked`, `changes`, `deferred`, `failed` | The number of steps checked, and the steps changed, deferred and failed, each with module, step and detail. |
| `seconds` | The run time. |

| `result` | Meaning |
|---|---|
| `ok` | Every step applied. |
| `partial` | A soft step or one account's step failed. That account's later steps are in `failed` as skipped; the rest applied. |
| `failed` | A box step failed and the run stopped; `error` says where. |
| `interrupted` | The run was stopped between two steps, by Ctrl-C or from the computer that started it. `error` names the next step. |

An `install` record is written when a request is installed, refused or denied, or starts waiting for approval. Its step is the request ID, such as `request-3f2a9c1e`, and it carries the account, the checked package names and, for a refusal, the reason in fixed words. Its `result` is `failed` only when apt-get did not install the packages. It never holds the agent's stated reason or apt's output.

The downgrade guard reads the newest apply record whose result is `ok` or `partial`.

## System files

| Path | Module | Notes |
|---|---|---|
| `/etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf` | host | Only while public SSH is closed: `DenyUsers *` on non-tailnet, non-loopback addresses. |
| `/etc/ssh/sshd_config.d/01-keelbox.conf` | host | Key-only SSH, no root login, no X11 forwarding, no agent forwarding for the agent. Numbered `01-` because sshd keeps the first value it reads. Validated with `sshd -t` and rolled back on failure; sshd is reloaded, not restarted. |
| `/etc/cloud/cloud.cfg.d/99-keelbox.cfg` | host | Only where cloud-init identifies AWS: `ssh_deletekeys: false`, `preserve_hostname: true`, `users: []`, so a disk booted on a new instance (after `resize` or `resume`) keeps its host keys, hostname and accounts. Also written for `provider: ssh` machines on AWS. See [AWS](clouds.md#aws-lightsail). |
| `/etc/sudoers.d/90-keelbox-admin` | host | `admin ALL=(ALL:ALL) NOPASSWD:ALL`, validated with `visudo`. |
| `/etc/sudoers.d/91-keelbox-agent` | host | Only with `users.agent.permissions: full` (or `users.agent.sudo: true`). Removed otherwise. |
| `/etc/sudoers.d/90-keelbox-bootstrap` | host | Never written. apply removes it, and doctor fails while it is there. |
| `/etc/sysctl.d/60-keelbox.conf` | host | inotify limits for file watchers. |
| `/etc/terminfo/x/xterm-ghostty` | host | Compiled with `tic` as a copy of the release's `ghostty` entry, so a shell reached over `ssh` from Ghostty or cmux edits lines and draws `vim`, `less` and tmux correctly; kitty's `xterm-kitty` comes from the `kitty-terminfo` package. Where the release has no `ghostty` entry (Ubuntu 24.04), it copies `xterm-256color`. apply copies it again when the release's entry changes, makes it readable by every account (0644, its directory 0755), and removes it once a package installs `xterm-ghostty`, such as Ubuntu's `ghostty`. Its description reads `keelbox alias of` the entry copied; an entry of that name keelbox did not compile is left alone. |
| `/etc/apparmor.d/keelbox-bwrap` | host | Only where `kernel.apparmor_restrict_unprivileged_userns` is 1 and Ubuntu ships no `bwrap-userns-restrict` (Ubuntu 24.04). Ubuntu's own profile for `/usr/bin/bwrap`, taken from its `apparmor-profiles` package, so the agent CLIs' sandboxes can use a user namespace. Loaded with `apparmor_parser`, rolled back if refused, and removed once Ubuntu's file is in `/etc/apparmor.d`. |
| `/etc/cron.deny`, `/etc/at.deny` | host | A line naming the agent, when cron or at is installed. apply also removes the agent's crontab and at jobs. An `/etc/cron.allow` or `/etc/at.allow` naming the agent is left to you, and that step fails with the fix. |
| `/etc/apt/apt.conf.d/21keelbox-auto-upgrades` | host | Unattended upgrades on or off. |
| `/etc/apt/apt.conf.d/52keelbox-unattended-upgrades` | host | unattended-upgrades reboots at the time in `host.auto_reboot`, or never; with `nightly` or `weekly` the watch run reboots instead. |
| `/etc/hostname`, `/etc/hosts` | host | Set with `hostnamectl`. A `127.0.1.1 NAME # keelbox` line is added only if the name does not resolve. |
| `/etc/default/locale`, timezone | host | Through `update-locale` and `timedatectl`. |
| `/etc/systemd/system/user-UID.slice.d/50-keelbox-limits.conf` | host | `CPUWeight`, `IOWeight`, `MemoryMax`, `MemorySwapMax` and, with `users.agent.limits.cpu`, `CPUQuota` for the agent account. Removed with `limits: {memory: none}` and no other limit. |
| `/etc/systemd/system/user@UID.service.d/50-keelbox-limits.conf` | host | Only with `users.agent.limits.memory` set: points the agent's user manager at `/etc/keelbox/agent-user`. |
| `/etc/keelbox/agent-user/systemd/user/{app,background,user}.slice.d/50-keelbox-limits.conf` | host | Only with `users.agent.limits.memory` set: `MemoryHigh` at 90% of the ceiling on the agent's work slices. A running user manager gets it at runtime until it restarts. |
| `/etc/systemd/system/user.slice.d/50-keelbox-limits.conf` | host | `CPUWeight` and `IOWeight` for logins against system services. Removed with `limits: {memory: none}` and no other limit. |
| `/etc/systemd/user.conf.d/50-keelbox-oom.conf` | sessions | `DefaultOOMPolicy=continue`: when the kernel stops a process for memory, only that process ends, not the whole tmux pane with the agent CLI in it. |
| `/etc/systemd/user/keelbox-tmux.service.d/50-keelbox-oom.conf`, `/etc/systemd/user/keelbox-claude-remote-control.service.d/50-keelbox-oom.conf` | host | Unless there are no limits: `OOMPolicy=continue`, so the unit keeps running when one of its processes is stopped at the memory ceiling. |
| `/swapfile`, a line in `/etc/fstab` | host | Only with a size in `host.swap`, on a machine without swap. |
| `/etc/subuid`, `/etc/subgid` | runtime | A 65536-ID range for the agent, appended if missing. |
| UFW rules | host | Default deny in, allow out. With Tailscale, `in on tailscale0` (`keelbox tailnet`). `22/tcp` (`keelbox ssh`), removed while public SSH is closed. One rule per `host.firewall.allow` port and source (`keelbox allow NAME`). With `public_ssh: lan` and Tailscale, `22/tcp` from the private ranges (`keelbox allow ssh-lan`). |
| `/etc/keelbox/agent-net.nft`, `/etc/systemd/system/keelbox-agent-net.service` | host | The `inet keelbox_agent` nftables table, matched on the socket owner's uid. To every account but root it refuses port 22 on the box's own addresses, each address in `/etc/keelbox/outside-addresses`, and link-local and metadata addresses. At link-local addresses the agent's uids are refused first; then `systemd-resolve` (port 53), `systemd-timesync` and `_chrony` (UDP 123), `systemd-network` (UDP 67 and 547, ICMPv6) and the accounts in `host.firewall.link_local_accounts` pass. Loaded before the network comes up. Doctor's `host resolver` flags an `/etc/resolv.conf` naming only link-local resolvers, and `host link-local-accounts` a named account without a rule. |
| `/usr/share/keyrings/tailscale-archive-keyring.gpg`, `/etc/apt/sources.list.d/tailscale.list` | access | Key fingerprint checked against the release manifest. |
| `/etc/apt/keyrings/docker.asc`, `/etc/apt/sources.list.d/docker.sources` | runtime | Fingerprint checked. |
| `/etc/docker/daemon.json` | runtime | Only `log-driver` and `log-opts` (json-file, 10m x 3); other keys kept. A change restarts Docker. |
| `/usr/local/lib/keelbox/docker-user-guard.sh`, `/etc/systemd/system/keelbox-docker-guard.service` | runtime | The `KEELBOX-DOCKER` chain, first in `DOCKER-USER`: drops new forwarded connections to system containers unless they arrive on `tailscale0` or a Docker bridge. A published port `host.firewall.allow` opens is admitted from its network. |
| `/etc/apt/keyrings/githubcli-archive-keyring.gpg`, `/etc/apt/sources.list.d/github-cli.list` | runtime | Fingerprint checked. |
| `/usr/local/bin/mise` | runtime | From the GitHub release tarball, checked against the manifest's checksum. |
| `/usr/local/bin/caddy` | previews | From Caddy's GitHub release, checked against the manifest's SHA-512. |
| `/etc/systemd/system/keelbox-preview-caddy.service` | previews | Caddy as `keelbox-preview`: admin API off, no capabilities, IP traffic only to loopback and the tailnet ranges. Started only while there is something to serve. |
| `/etc/systemd/system/keelbox-previews.service`, `.path`, `.timer` | previews | `keelbox previews-sync` as root: when the agent's previews file changes, a minute after boot and every 10 minutes. |
| `/usr/local/bin/restic` | backup | From restic's GitHub release, checked against the manifest's SHA-256. |
| `/etc/systemd/system/keelbox-backup.service`, `keelbox-backup.timer` | backup | `keelbox backup` as root on `backup.schedule`, persistent, with up to 15 minutes' random delay. |
| `/etc/claude-code/managed-settings.d/50-keelbox.json` | agent-policy | Only when a feature needs a Claude Code setting the agent cannot change. |
| `/etc/claude-code/managed-settings.d/51-keelbox-hooks.json` | agent-policy | With `notify.channels`: hooks running `keelbox event --from claude-code` on `Stop`, `StopFailure` and `Notification`, async, 10-second limit. Other files there are left alone; doctor fails if one is not valid JSON. |
| `/etc/codex/requirements.toml` | agent-policy | Only when a feature needs a Codex requirement, and the file is absent or starts with `# Managed by keelbox.`; another file is left alone and doctor warns. With `notify.channels`: `[[hooks.Stop]]` and `[[hooks.PermissionRequest]]` running `keelbox event --from codex`. |
| `/etc/systemd/system/keelbox-watch.service`, `keelbox-watch.timer` | watch | `keelbox watch-run` as root, 90 seconds after boot and every 2 minutes: heartbeat, status snapshot, session watchdog, tmux session saves every 15 minutes, disk alerts, the power-back report and queued events. |
| `/etc/systemd/system/keelbox-boot.service` | watch | Active from boot to shutdown, to detect an unclean shutdown. |
| `/etc/systemd/system/keelbox-conversations.service`, `/etc/systemd/system/user@.service.d/50-keelbox-conversations.conf` | watch | Active from boot to shutdown. Its stop runs `keelbox note-conversations`, which, when the box is shutting down, writes `resume.json` and saves the tmux sessions while every pane still runs. The drop-in has every user manager stop after it, so the agent's tmux still runs then. |
| `/run/keelbox-tmux-saved` | watch | Root's, 0644. Written at shutdown once the sessions are saved, so the tmux unit's own save at its stop is skipped; removed at each boot. |
| `/etc/systemd/logind.conf.d/60-keelbox.conf` | host | Only with `host.power.sleep: never`: lid switch and idle action ignored, and `sleep.target`, `suspend.target`, `hibernate.target` and `hybrid-sleep.target` masked in `/etc/systemd/system/`. |
| `/etc/systemd/system/keelbox-events.service`, `keelbox-events.path` | watch | `keelbox events-sync` as root when the agent's events file changes: reads it as the agent, queues valid agent events within budget, and sends what is due. |
| `/etc/systemd/system/keelbox-install.service`, `keelbox-install.path` | permissions | `keelbox install-run` as root when the agent's request file changes or `keelbox approve` starts it: [the install service](#the-install-service). |
| `/etc/systemd/system/keelbox-doctor.service`, `keelbox-doctor.timer` | doctor | `keelbox doctor-run` as root on `doctor.schedule` and five minutes after boot, raising an event when a check starts or stops failing. Removed with `doctor.schedule: off`. |
| `/var/lib/systemd/linger/agent` | host | Lingering, so the agent's user services run without a login. |

### The install service

`keelbox-install.service` reads the agent's package request as the agent and checks it. Depending on `users.agent.permissions` it then runs `apt-get install -y --no-remove` or keeps the request for approval. The units exist in every mode, `full` included.

One run at a time, at most an hour each, at CPU and I/O weight 50 and nice 10. `NoNewPrivileges` is not set, because packages' maintainer scripts need what apt normally has. The modes: [what the agent may change](../guides/work-on-the-box.md#what-the-agent-may-change).

### Packages and services

| Set | Packages |
|---|---|
| Base | `ca-certificates curl gnupg jq git openssh-server build-essential pkg-config unzip zip xz-utils zstd tmux ncurses-term kitty-terminfo htop ripgrep fd-find ncdu bubblewrap socat file python3-pil unattended-upgrades ufw nftables locales acl` |
| Yours | `host.packages` |
| Tailscale | `tailscale` |
| Docker | `docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker-ce-rootless-extras uidmap slirp4netns dbus-user-session` |
| GitHub CLI | `gh` |
| Backups | `bzip2` |

With `--adopt`, conflicting packages are removed: `docker.io`, `docker-compose`, `docker-compose-v2`, `podman-docker`, `containerd` and `runc`. This stops their containers.

Services enabled: `unattended-upgrades`, `tailscaled`, `docker`, `keelbox-docker-guard` and `keelbox-agent-net`. `ssh.socket` is left as Ubuntu configures it.

## Accounts

| Account | Created as | Changes keelbox makes |
|---|---|---|
| `admin` | `useradd --create-home --shell /bin/bash`, no password | In group `sudo`. A managed block `keys` in `~/.ssh/authorized_keys`, 0600. |
| `agent` | the same | Removed from `docker` with `runtime.docker`, and from `sudo` unless `users.agent.permissions` is `full`. A managed block in `~/.ssh/authorized_keys`, and the files below. |
| the image's default account, such as `ubuntu` | by the image | [Locked](../guides/adopting-a-box.md#the-images-default-account) with `usermod --lock --expiredate 1`. |
| `keelbox-preview` | `useradd --system --user-group --no-create-home`, shell `nologin` | Runs Caddy for previews. |

## The agent's home

| Path | Notes |
|---|---|
| `~/.bashrc`, block `path` | At the top, before Ubuntu's interactive guard, so `ssh box cmd` sees it: mise shims, `~/.local/bin` and pnpm first on the PATH, and `DOCKER_HOST`. |
| `~/.bashrc`, block `sessions` | At the end, for interactive shells: lists the tmux sessions on an SSH login outside tmux, and prints [the welcome](#the-welcome). |
| `~/.config/environment.d/99-keelbox.conf` | The same PATH and `DOCKER_HOST` for systemd user services, after Ubuntu's `99-environment.conf`. apply refreshes the user manager and a running tmux server without ending any session. |
| `user.name`, `user.email`, `init.defaultBranch` in `~/.gitconfig` | Only with `users.agent.git`, set with `git config --global` so other settings stay. `init.defaultBranch` becomes `main` only when unset. |
| `~/.config/docker/daemon.json` | The rootless daemon's log limits; other keys kept. |
| `~/.config/systemd/user/docker.service` | The agent's rootless Docker daemon, enabled. |
| `~/.config/mise/config.toml` | The release's Node and pnpm, installed under `~/.local/share/mise`. A repository's own `mise.toml` pins versions per project. |
| `~/.config/tmux/keelbox.conf`, a `source-file` line in `~/.tmux.conf` | The tmux config, with the status line: the box name on the left, `Ctrl-b d` to leave on the right. Your overrides go in `~/.tmux.conf` after that line. |
| `~/.tmux/plugins/tmux-resurrect`, `~/.tmux/plugins/tmux-continuum` | Git checkouts at pinned commits. |
| `~/.config/systemd/user/keelbox-tmux.service` | Starts the tmux server with session `main` at boot; the watchdog starts it again when the server is gone. Its stop saves the sessions. A change takes effect at the next start: apply never restarts it. |
| `~/.config/systemd/user/keelbox-claude-remote-control.service` | With `sessions.remote_control.enabled`: Claude Code Remote Control in server mode, in `~/projects` or the configured directory. Skipped until Claude Code is signed in. |
| `~/.claude/CLAUDE.md` | An `@/etc/keelbox/AGENTS.md` line, appended if missing. |
| `~/.codex/AGENTS.md` | A symlink to the primer, unless you have your own file there; doctor then warns. |
| `~/.local/state/keelbox/events.jsonl` | Lines the agent CLIs' hooks append through `keelbox event`. Only lines naming an agent event kind with checked fields become events; a `keelbox report` line becomes a report draft. Directory 0700, file 0600, both the agent's. |
| `~/.local/state/keelbox/install-request.json` | The agent's newest package request from `keelbox install`, 0600. The install service reads it as the agent, follows no link and reads nothing over 16 KiB. `install.lock` beside it keeps two `keelbox install` commands apart. |
| `~/.config/keelbox/previews.json` | The agent's previews, written by `keelbox expose`. |
| `~/keelbox-restore/` | Where `keelbox restore` puts snapshots by default. Left out of backups. |
| `~/.claude/skills/`, `~/.agents/skills/`, `~/.codex/skills/` | Only the skills you copy with `keelbox skills`. keelbox removes none. |
| `~/.local/bin/claude`, `codex` and their data | Whatever the vendors' installers write. keelbox checks only that each command is on the agent's PATH. |

The CLIs write the agent's sign-ins when you sign in: `~/.claude`, `~/.codex/auth.json` and `~/.config/gh`. keelbox writes none of them.

### The welcome

Unless `sessions.welcome` is `false`, a new session's first pane prints [the welcome](../guides/sessions.md#the-welcome). The tmux hook `session-created[90]` marks the pane in the session option `@keelbox-welcome`, and the `sessions` block runs `keelbox welcome` there and clears the mark. If the command could not run, `client-attached[90]` restarts the pane's shell, but only while it is bash at its first prompt. Every other interactive shell in tmux pays one `tmux display-message` for it.

## Not managed

- Your projects, and anything in the agent's home not listed above, including agent CLIs the agent installs itself.
- Containers and images.
- Tailscale's state and ACLs.
- The agent CLIs' versions after install: they update themselves.

What stays behind when you remove a box: [remove a box](../guides/removing-a-box.md).
