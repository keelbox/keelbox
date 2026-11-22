# Files keelbox manages on a box

```sh
keelbox plan acme
```

Everything `keelbox apply` creates or changes on a box, by owner. `keelbox plan` shows the same steps with what would change.

A file keelbox owns outright starts with a "Managed by keelbox" comment and is rewritten on every apply; change the config instead. A file you also edit gets a marked block, from `# >>> keelbox ID >>>` to `# <<< keelbox ID <<<`. Text outside the block is left as it is.

Paths use the default account names, `admin` and `agent`. The root services that run between applies are called [unattended runs](../concepts/how-it-works.md#what-a-box-does-unattended) here.

## keelbox's own state

| Path | Mode and owner | What |
|---|---|---|
| `/usr/local/bin/keelbox` | 0755 root | The keelbox that last applied the box. Only `apply` installs it. |
| `/var/lib/keelbox/keelbox.previous` | 0755 root | The keelbox it replaced, for `keelbox rollback`. |
| `/var/lib/keelbox/logbook.jsonl` | 0644 root | The [logbook](#logbook): one JSON line per apply and per action of an unattended run. |
| `/var/lib/keelbox/stage/keelbox` | 0755 root, dir 0700 | The binary uploaded for the current or last remote command. `/run` is mounted `noexec`, so it is staged here. |
| `/etc/keelbox/keelbox.yaml` | 0600 root | The applied config, with keys inlined and provider `local`. Commands run on the box read it when no config is named. |
| `/etc/keelbox/AGENTS.md` | 0644 root | The primer: what keelbox tells the agent CLIs about the box. |
| `/run/keelbox/` | tmpfs, dir 0700, files 0600 | During a remote run only: the shipped config and the secret files the run needs. Those are the Tailscale auth key, the backup credentials and password, and each channel's secrets as `notify.<channel>.<key>`. A detached apply's own copy of the config is `run-<id>.yaml`. The files are deleted when the run ends, the secrets once no apply is running, and all are gone at reboot. |
| `/tmp/tmp.XXXXXXXXXX/` | dir 0700, the account keelbox logs in as | Only at the start of a remote command: scp puts the binary, the config and the secret files here. Root then moves them to the two places above. Made with `mktemp -d`, removed before the command runs, and left behind if the upload or the install fails. When sudo asks for a password, the command's output waits beside it in `tmp.XXXXXXXXXX.out`, 0600. Where `/tmp` is on disk, the files are written to disk here. |
| `/var/lib/keelbox/runs/` | 0640 root:sudo, dir 0750 | One log per apply started from another computer, `<id>.jsonl`, and `current`, the newest run's id. A log is the run's output, one JSON line per step and line, with text escaped. It is bounded at 4 MiB, and the ten newest are kept. Accounts in the `sudo` group read it and the agent does not; `status` and `doctor` read it too. |
| `keelbox-apply-<id>.service` | transient | An apply started over SSH, run by `systemd-run` as root, so it finishes when the connection is gone. Its output is also in the journal: `journalctl -u 'keelbox-apply-*'`. |
| `/run/keelbox-start.lock` | 0600 root | Held while an apply starts, so that two commands do not start two runs. |
| `/var/lib/keelbox/previews/caddy.json` | 0640 root:keelbox-preview, dir 0755 root | The rendered Caddy config for previews. |
| `/var/lib/keelbox/previews/status.json` | 0644 root | What is published and any problems, read by `keelbox expose`. |
| `/var/lib/keelbox/previews/tls/` | 0750 root:keelbox-preview | The box's Tailscale certificate, `cert.pem`, 0644, and its key, `key.pem`, 0640. |
| `/var/lib/keelbox-preview/` | 0700 keelbox-preview | Caddy's state directory, made by systemd. |
| `/etc/keelbox/outside-addresses` | 0644 root | Where the box's sshd is reached from outside at an address none of its interfaces carries (a cloud's translated public address, a router's forwarded port), one `ADDRESS:PORT` or `[IPV6-ADDRESS]:PORT` a line. Each keelbox command over SSH tells the box where the laptop reaches it; apply adds the addresses that are not the box's own and keeps the 16 added last. Unlike other managed files this one may be edited: delete a line the box is no longer reached at, or add an address keelbox has not been told about, then run `keelbox apply`. Lines that are not addresses are not kept. |
| `/etc/keelbox/backup/` | 0700 root | `s3.credentials` and `restic.password`, copies of `backup.credentials_file` and `backup.password_file`, each 0600 root. |
| `/var/lib/keelbox/public-ssh.json` | 0644 root | Whether public SSH is open or closed, why, and the tailnet proof: address, client address, account and time. Your computer reads it to set the SSH rule of the cloud's firewall in front of the box. |
| `/var/lib/keelbox/backup/status.json` | 0600 root, dir 0700 | The last backup attempt and the last success, for `doctor` and `status`. |
| `/var/cache/keelbox/restic/` | 0700 root | restic's cache. Deleting it costs a slower next run. |
| `/var/lib/keelbox/restore/` | root | The default target when restoring a backup path outside the agent's and the admin's homes. |
| `/run/keelbox.lock` | 0600 root | Held exclusively while `apply` runs. Unattended runs hold it shared, and skip while an apply holds it. |
| `/run/keelbox-daytwo.lock` | 0600 root | Held by one unattended run at a time. |
| `/run/keelbox-installs.lock` | 0600 root | Held by the install service, `keelbox approve` and `keelbox deny`, one at a time. They alone write `requests.json`, where the agent's `keelbox install` reads the outcome. |
| `/var/lib/keelbox/watch/state.json` | 0644 root, dir 0755 | The watch run's heartbeat and the boot it last saw. For each of the checks `tmux` and `remote-control`: the watchdog's restarts, give-ups and last action. Also when Remote Control's service stopped answering, the disk alerts sent per mount, the last session save and the last count of pending updates. Doctor fails when the heartbeat is over 10 minutes old. |
| `/var/lib/keelbox/watch/resume.json` | 0600 root | Written before keelbox reboots the box: each tmux pane that ran Claude Code or Codex, and the command that opens its conversation again. The watch runs after the boot type it into each pane, then remove the file. |
| `/var/lib/keelbox/power/state.json` | 0644 root, dir 0755 | The boot `keelbox-boot.service` last started in, and the last unclean shutdown it found: when, the boot before, its last heartbeat and when it was reported. |
| `/var/lib/keelbox/power/clean-shutdown` | 0644 root | Touched at every orderly shutdown or reboot, and removed at the next boot. A boot that finds none followed a power loss, crash or hard reset. |
| `/var/lib/keelbox/reports/<id>.json` | 0600 root, dir 0700 | A report the agent filed with `keelbox report`: its title, area and detail, cleaned of what does not print, and the box facts root added. Those are the keelbox version and release, the Ubuntu version, the architecture, the size and the failing doctor checks. Only the owner reads and sends them, with [`keelbox reports`](../guides/reports.md). |
| `/var/lib/keelbox/status.json` | 0644 root | The [status snapshot](#the-status-snapshot), rewritten by every watch run. |
| `/var/lib/keelbox/notify/queue/` | 0700 root, files 0600, in a 0700 root dir | One file per event waiting for its channels, dropped after 24 hours. |
| `/var/lib/keelbox/notify/cursor.json` | 0600 root | How far the agent's events file has been read. |
| `/var/lib/keelbox/notify/ratelimit.json` | 0600 root | The agent events let through, for the budgets. |
| `/var/lib/keelbox/notify/last.json` | 0600 root | Each channel's last send and its error, without secrets. |
| `/var/lib/keelbox/notify/events-run` | 0600 root | When the events service last started. It starts at most every 5 seconds. |
| `/etc/keelbox/notify/` | 0700 root | With `notify.channels`: copies of each channel's secret files, named `<channel>.<key>`, each 0600 root. A copy no channel names is removed, and the directory goes with the last channel. |
| `/var/lib/keelbox/doctor/last.json` | 0644 root, dir 0755 | The last scheduled doctor run: time, counts, the findings that are not ok, and which alerts are out. |
| `/var/lib/keelbox/installs/mode.json` | 0644 root, dir 0755 | The box name, the agent account and `users.agent.permissions`, written by apply. The agent's `keelbox install` reads it to know the box's mode. The install service reads the applied config instead. |
| `/var/lib/keelbox/installs/requests.json` | 0644 root | Root's record of the agent's package requests: at most 20 waiting for `keelbox approve`, and the last 50 decided. Each has root's ID for it, which `keelbox approve` takes, the agent's own ID, the time, the account and the checked package names. Each has the agent's stated reason, cleaned of what does not print, and a state with root's reason for it. The states are `pending`, `approved`, `installing`, `installed`, `refused`, `failed` and `denied`. |
| `/var/lib/keelbox/installs/installed.json` | 0644 root | The packages installed for the agent: name, when, and the request that asked. `keelbox status` lists them and says which `host.packages` does not name. `keelbox apply` never removes the record or the packages. |
| `/var/lib/keelbox/installs/told.json` | 0644 root | The packages apply last said `host.packages` lacks, so that it says so once. |
| `/var/lib/keelbox/installs/run`, `apt-update` | 0600 root | When the install service last started, which is at most every 2 seconds. When it last refreshed the package lists, which is at most hourly. |

## The status snapshot

`/var/lib/keelbox/status.json` holds, as of the last watch run:

- the release, the keelbox version, the agent account's name and whether keelbox reports are off;
- the machine's CPUs and memory, and disk use per mount;
- pending security updates and a needed reboot, counted hourly;
- the last apply, the last backup, the last boot and whether it followed an unclean shutdown;
- the last scheduled doctor run's counts and failing checks;
- the agent CLIs' active, previous and blocked versions;
- the agent's CPU and memory use against its limits;
- the agent's tmux sessions and Remote Control's state;
- the name of each directory under the agent's projects directory, with its git state;
- how many package requests of the agent's wait for `keelbox approve`.

A project's git state: not a repository, not checked, no remote, and the counts of uncommitted changes and commits not pushed.

The snapshot holds no secrets. Every account reads it without sudo: that is how `keelbox status --all` reads it, and `keelbox status` run by the agent.

## Logbook

`/var/lib/keelbox/logbook.jsonl` gets one record per apply, failed ones included, and one per action an unattended run takes. It holds what keelbox changed, never command output or secrets. Each record is appended with a single write, and the file is never rewritten.

```json
{"time":"2026-11-21T15:05:25.490349411Z","box":"acme","keelbox":"v0.1.0","release":"2026.11.4","config_hash":"a52f0a6337de","result":"ok","checked":74,"changes":[{"module":"logbook","step":"keelbox-binary","detail":"install this keelbox (v0.1.0) as /usr/local/bin/keelbox"}],"seconds":0.275171381}
```

| Field | Meaning |
|---|---|
| `time`, `box`, `keelbox`, `release` | When, which box, which keelbox binary and which release manifest. A record of an unattended run carries the release of the box's last converged apply. |
| `kind` | Absent for an apply. `watch`, `doctor` or `power` for an action an unattended run took: `result` is `ok` or `failed`, and `changes` says what it did. `install` for a package request of the agent's. `agents`, for an agent CLI upgrade by a keelbox agent store, still reads; nothing writes it. |
| `from_release` | Set when the apply moved the box to another release. |
| `config_hash` | The first 12 hex characters of the SHA-256 of the effective config. |
| `result` | `ok`, `partial`, `failed` or `interrupted`, as below. |
| `checked`, `changes`, `deferred`, `failed` | The number of steps checked, and the steps changed, deferred and failed, each with module, step and detail. |
| `seconds` | The run time. |

| `result` | Meaning |
|---|---|
| `ok` | Every step applied. |
| `partial` | A soft step or an account's step failed. That account's later steps are in `failed` as skipped, and the rest applied. |
| `failed` | A box step failed and the run stopped; `error` says where. |
| `interrupted` | The run was stopped between two steps, by Ctrl-C or from the computer that started it. `error` names the next step. |

An `install` record is written when a request is installed, refused or denied, or starts waiting for approval. Its step is the request ID, such as `request-3f2a9c1e`. It carries the account, the checked package names and, for a refusal, the reason in fixed words.

Its `result` is `failed` only when apt-get did not install the packages. It never holds the agent's stated reason or what apt printed.

Read the logbook on the box with `sudo tail -n 3 /var/lib/keelbox/logbook.jsonl | jq .`. The downgrade guard reads the newest apply record whose result is `ok` or `partial`.

## System files

| Path | Module | Notes |
|---|---|---|
| `/etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf` | host | Only while public SSH is closed: `Match LocalAddress` on non-tailnet, non-loopback addresses with `DenyUsers *`. Removed when public SSH opens again. |
| `/etc/ssh/sshd_config.d/01-keelbox.conf` | host | Key-only SSH, no root login, no X11 forwarding, no agent forwarding for the agent. Numbered `01-` because sshd keeps the first value it reads. Validated with `sshd -t` and rolled back on failure. sshd is reloaded, not restarted. |
| `/etc/cloud/cloud.cfg.d/99-keelbox.cfg` | host | Only on a machine cloud-init identifies as AWS, where `/run/cloud-init/cloud-id` says `aws`: `ssh_deletekeys: false`, `preserve_hostname: true` and `users: []`. A disk that boots on a new instance, as after `keelbox resize` and `keelbox resume`, then keeps its SSH host keys, hostname and accounts. A machine of your own on AWS, set up with `provider: ssh`, gets the file too. [AWS](clouds.md#aws-lightsail) has what resize and park do without it. |
| `/etc/sudoers.d/90-keelbox-admin` | host | `admin ALL=(ALL:ALL) NOPASSWD:ALL`, validated with `visudo`. |
| `/etc/sudoers.d/91-keelbox-agent` | host | Only with `users.agent.permissions: full` or its alias `users.agent.sudo: true`. Removed otherwise. |
| `/etc/sudoers.d/90-keelbox-bootstrap` | host | Never written. `keelbox apply` removes it, and doctor fails while it is there. |
| `/etc/sysctl.d/60-keelbox.conf` | host | inotify limits for file watchers. |
| `/etc/apparmor.d/keelbox-bwrap` | host | Only where `kernel.apparmor_restrict_unprivileged_userns` is 1 and Ubuntu loads no `bwrap-userns-restrict`, which is Ubuntu 24.04. It is Ubuntu's own profile for `/usr/bin/bwrap`, under a keelbox header that names its source. It lets bwrap use a user namespace, which the agent CLIs' sandboxes need. `keelbox apply` reads the text from Ubuntu's `apparmor-profiles` package, downloaded with apt and not installed, or from the installed file. It is loaded with `apparmor_parser`, rolled back if that refuses it, and removed once Ubuntu's file is in `/etc/apparmor.d`. |
| `/etc/cron.deny`, `/etc/at.deny` | host | A line naming the agent, when cron or at is installed. `keelbox apply` also removes a crontab or at jobs the agent has. An `/etc/cron.allow` or `/etc/at.allow` that names the agent is left to you, and that step fails with the fix. |
| `/etc/apt/apt.conf.d/21keelbox-auto-upgrades` | host | Unattended upgrades on or off. |
| `/etc/apt/apt.conf.d/52keelbox-unattended-upgrades` | host | unattended-upgrades reboots at the time in `host.auto_reboot`, or never: with `nightly` or `weekly` the watch run reboots instead. |
| `/etc/hostname`, `/etc/hosts` | host | Set with `hostnamectl`. A `127.0.1.1 NAME # keelbox` line is added only if the name does not resolve. |
| `/etc/default/locale`, timezone | host | Through `update-locale` and `timedatectl`. |
| `/etc/systemd/system/user-UID.slice.d/50-keelbox-limits.conf` | host | `CPUWeight`, `IOWeight`, `MemoryMax` and `MemorySwapMax` for every process of the agent account, and `CPUQuota` with `users.agent.limits.cpu`. Removed with `limits: {memory: none}` and no other limit. |
| `/etc/systemd/system/user@UID.service.d/50-keelbox-limits.conf` | host | Only with `users.agent.limits.memory` set: `XDG_CONFIG_DIRS=/etc/keelbox/agent-user:/etc/xdg` for the agent's user manager alone, read when it starts. |
| `/etc/keelbox/agent-user/systemd/user/{app,background,user}.slice.d/50-keelbox-limits.conf` | host | Only with `users.agent.limits.memory` set: `MemoryHigh` at 90% of the ceiling on the agent's user manager's work slices. Its `session.slice` and the manager itself have none. A manager already running gets the same with `systemctl --user set-property --runtime` until it restarts. |
| `/etc/systemd/system/user.slice.d/50-keelbox-limits.conf` | host | `CPUWeight` and `IOWeight` for logins against system services. Removed with `limits: {memory: none}` and no other limit. |
| `/etc/systemd/user.conf.d/50-keelbox-oom.conf` | sessions | `DefaultOOMPolicy=continue` for the user managers on the box. tmux starts each pane as a unit of its own, and systemd by default ends a whole unit when the kernel stops one of its processes for memory. With this only that process ends: the agent CLI and everything else in the pane keep running. |
| `/etc/systemd/user/keelbox-tmux.service.d/50-keelbox-oom.conf`, `/etc/systemd/user/keelbox-claude-remote-control.service.d/50-keelbox-oom.conf` | host | Unless there are no limits: `OOMPolicy=continue`, so the unit keeps running when the kernel stops one of its processes at the agent's memory ceiling. |
| `/swapfile`, a line in `/etc/fstab` | host | Only with a size in `host.swap`, on a machine that has no swap. |
| `/etc/subuid`, `/etc/subgid` | runtime | A 65536-ID range for the agent, appended if missing. |
| UFW rules | host | Default deny in and allow out. With Tailscale, `in on tailscale0` with comment `keelbox tailnet`. `22/tcp` with comment `keelbox ssh`, removed while public SSH is closed. One rule per `host.firewall.allow` port and source network, with comment `keelbox allow NAME`. With `public_ssh: lan` and Tailscale, `22/tcp` from the private ranges, with comment `keelbox allow ssh-lan`. |
| `/etc/keelbox/agent-net.nft`, `/etc/systemd/system/keelbox-agent-net.service` | host | The `inet keelbox_agent` nftables table, which matches the uid that owns a socket: connections to port 22 on the box's own addresses, to each address and port in `/etc/keelbox/outside-addresses`, and to link-local and metadata addresses are refused to every account but root. At link-local addresses the agent's uid and its subordinate uids in `/etc/subuid` are refused first; then `systemd-resolve` (port 53), `systemd-timesync` and `_chrony` (UDP port 123) and `systemd-network` (UDP ports 67 and 547, ICMPv6) pass, where the box has the account, and so do the accounts `host.firewall.link_local_accounts` names, on every port; then every other account but root is refused. Loaded before the network comes up, and again when the rules change. Doctor's `host resolver` reports an `/etc/resolv.conf` that names only link-local resolvers, and `host link-local-accounts` a named account that gets no rule. |
| `/usr/share/keyrings/tailscale-archive-keyring.gpg`, `/etc/apt/sources.list.d/tailscale.list` | access | The key's fingerprint is checked against the release manifest. |
| `/etc/apt/keyrings/docker.asc`, `/etc/apt/sources.list.d/docker.sources` | runtime | Fingerprint checked. |
| `/etc/docker/daemon.json` | runtime | Only `log-driver` and `log-opts` are set, to json-file and 10m x 3. Other keys are kept. A change restarts Docker. |
| `/usr/local/lib/keelbox/docker-user-guard.sh`, `/etc/systemd/system/keelbox-docker-guard.service` | runtime | The `KEELBOX-DOCKER` chain, jumped to first from `DOCKER-USER`. It drops new forwarded connections to system containers unless they arrive on `tailscale0` or a Docker bridge. A published port that `host.firewall.allow` keeps open is admitted from its network. |
| `/etc/apt/keyrings/githubcli-archive-keyring.gpg`, `/etc/apt/sources.list.d/github-cli.list` | runtime | Fingerprint checked. |
| `/usr/local/bin/mise` | runtime | From the GitHub release tarball, checked against the release manifest's checksum. |
| `/usr/local/bin/caddy` | previews | From Caddy's GitHub release tarball, checked against the release manifest's SHA-512. |
| `/etc/systemd/system/keelbox-preview-caddy.service` | previews | Caddy as `keelbox-preview`: config `/var/lib/keelbox/previews/caddy.json`, admin API off, no capabilities, IP traffic only to loopback and the tailnet ranges. Enabled, and started only while there is something to serve. |
| `/etc/systemd/system/keelbox-previews.service`, `.path`, `.timer` | previews | `keelbox previews-sync` as root: when the agent's previews file changes, a minute after boot and every 10 minutes. |
| `/usr/local/bin/restic` | backup | From restic's GitHub release, a `.bz2`, checked against the release manifest's SHA-256. |
| `/etc/systemd/system/keelbox-backup.service`, `keelbox-backup.timer` | backup | `keelbox backup` as root on `backup.schedule`, persistent, with up to 15 minutes' random delay. |
| `/etc/claude-code/managed-settings.d/50-keelbox.json` | agent-policy | Only when a feature needs a Claude Code setting the agent cannot change: managed env and settings. |
| `/etc/claude-code/managed-settings.d/51-keelbox-hooks.json` | agent-policy | Managed hooks. With `notify.channels` they run `keelbox event --from claude-code` on `Stop`, `StopFailure` and `Notification`, async, with a 10 second limit. Other files in the directory are left alone, and doctor fails if any of them is not valid JSON. |
| `/etc/codex/requirements.toml` | agent-policy | Only when a feature needs a Codex requirement, and the file is absent or starts with `# Managed by keelbox.`. Another file is left alone, and doctor warns. With `notify.channels`: `[[hooks.Stop]]` and `[[hooks.PermissionRequest]]`, running `keelbox event --from codex`. |
| `/etc/systemd/system/keelbox-watch.service`, `keelbox-watch.timer` | watch | `keelbox watch-run` as root, 90 seconds after boot and every 2 minutes. It writes the heartbeat and the status snapshot, runs the session watchdog, and saves the agent's tmux sessions every 15 minutes. It also sends disk alerts, the power-back report and queued events. |
| `/etc/systemd/system/keelbox-boot.service` | watch | Active from boot to shutdown. Its start, `keelbox boot-mark`, tells whether the last boot ended cleanly; its stop touches the marker. |
| `/etc/systemd/logind.conf.d/60-keelbox.conf` | host | Only with `host.power.sleep: never`: lid switch and idle action ignored. With it, `sleep.target`, `suspend.target`, `hibernate.target` and `hybrid-sleep.target` are masked, as links to `/dev/null` in `/etc/systemd/system/`. |
| `/etc/systemd/system/keelbox-events.service`, `keelbox-events.path` | watch | `keelbox events-sync` as root whenever the agent's events file changes. It reads the file as the agent, queues the valid agent events within their budget and sends what is due. |
| `/etc/systemd/system/keelbox-install.service`, `keelbox-install.path` | permissions | `keelbox install-run` as root whenever the agent's request file changes, and when `keelbox approve` starts it. [The install service](#the-install-service) has the detail. |
| `/etc/systemd/system/keelbox-doctor.service`, `keelbox-doctor.timer` | doctor | `keelbox doctor-run` as root on `doctor.schedule`, hourly unless set, and five minutes after boot. It runs doctor's checks and raises an event when one starts or stops failing. Removed with `doctor.schedule: off`. |
| `/var/lib/systemd/linger/agent` | host | Lingering, so the agent's user services run without a login. |

### The install service

`keelbox-install.service` reads the agent's package request as the agent and checks it. By `users.agent.permissions` in the applied config, it then installs with `apt-get install -y --no-remove` or keeps the request for approval. The units are there in every mode, `full` included.

One run at a time, with a time limit of an hour. Its CPU and I/O weight is 50, below system services, and its nice value 10. `NoNewPrivileges` is not set, as packages' maintainer scripts expect of apt. [What the agent may change](../guides/work-on-the-box.md#what-the-agent-may-change) covers the three modes.

### Packages and services

| Set | Packages |
|---|---|
| Base | `ca-certificates curl gnupg jq git openssh-server build-essential pkg-config unzip zip xz-utils zstd tmux ncurses-term htop ripgrep fd-find ncdu bubblewrap socat file python3-pil unattended-upgrades ufw nftables locales acl` |
| Yours | `host.packages` |
| Tailscale | `tailscale` |
| Docker | `docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker-ce-rootless-extras uidmap slirp4netns dbus-user-session` |
| GitHub CLI | `gh` |
| Backups | `bzip2` |

With `--adopt`, the conflicting packages are removed: `docker.io`, `docker-compose`, `docker-compose-v2`, `podman-docker`, `containerd` and `runc`. On a first apply `--adopt` also accepts what `plan --impact` marks.

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
| `~/.bashrc`, block `path` | At the top, before Ubuntu's interactive guard, so `ssh box cmd` sees it. It puts mise shims, `~/.local/bin` and pnpm first on the PATH, and sets `DOCKER_HOST`. |
| `~/.bashrc`, block `sessions` | At the end, so for interactive shells only. On an SSH login outside tmux it lists the tmux sessions. It also prints [the welcome](#the-welcome). |
| `~/.config/environment.d/99-keelbox.conf` | The same PATH and `DOCKER_HOST` for systemd user services. It sorts after Ubuntu's `99-environment.conf`, which would replace its PATH. `keelbox apply` reloads the agent's user manager when its PATH lacks it, and gives a running tmux server the same with `tmux set-environment -g`. No session ends, and an older `10-keelbox.conf` is removed. |
| `user.name`, `user.email` and `init.defaultBranch` in `~/.gitconfig` | Only with `users.agent.git`: set with `git config --global`, run as the agent, so every other setting stays. `init.defaultBranch` becomes `main` only when nothing sets one. |
| `~/.config/docker/daemon.json` | The rootless daemon's log limits. Other keys are kept. |
| `~/.config/systemd/user/docker.service` | The agent's rootless Docker daemon, enabled. |
| `~/.config/mise/config.toml` | The release's Node and pnpm, which mise installs under `~/.local/share/mise`. A repository's own `mise.toml` pins versions per project. |
| `~/.config/tmux/keelbox.conf`, a `source-file` line in `~/.tmux.conf` | The tmux config, validated with a throwaway server. It includes the status line: the box's name on the left, and how to leave, `Ctrl-b d`, on the right. Your overrides go in `~/.tmux.conf` after that line and replace keelbox's. |
| `~/.tmux/plugins/tmux-resurrect`, `~/.tmux/plugins/tmux-continuum` | Git checkouts at pinned commits. |
| `~/.config/systemd/user/keelbox-tmux.service` | Starts the tmux server with session `main` at boot; the watchdog restarts it when the server is gone. Its stop saves the sessions while a server runs. A change is picked up at the next start, never by restarting it. |
| `~/.config/systemd/user/keelbox-claude-remote-control.service` | With `sessions.remote_control.enabled`: Claude Code Remote Control in server mode, in `~/projects` or the configured directory, created if missing. Enabled, and skipped until Claude Code is signed in. |
| `~/.claude/CLAUDE.md` | An `@/etc/keelbox/AGENTS.md` line, appended if missing. |
| `~/.codex/AGENTS.md` | A symlink to the primer, unless you have your own file there; doctor then warns. |
| `~/.local/state/keelbox/events.jsonl` | The agent's events: lines its CLIs' hooks append through `keelbox event`, read as the agent by the events service. Only a line that names an agent event kind with checked fields becomes an event. A `keelbox report` line becomes a draft in `/var/lib/keelbox/reports/`, never an event. The directory is 0700 and the file 0600, both the agent's; keelbox creates only the directory. |
| `~/.local/state/keelbox/install-request.json` | The agent's newest package request, written by `keelbox install`: an ID, a time, package names and its stated reason. It is 0600 and the agent's. The install service reads it as the agent, follows no link and reads no file over 16 KiB. `install.lock` beside it keeps two `keelbox install` commands from replacing each other's request; root does not rely on it. |
| `~/.config/keelbox/previews.json` | The agent's previews, written by `keelbox expose` and read as the agent by the previews service. keelbox creates only the directory. |
| `~/keelbox-restore/` | Where `keelbox restore` puts snapshots unless told otherwise. Left out of backups. |
| `~/.claude/skills/`, `~/.agents/skills/`, `~/.codex/skills/` | Only the skills you copy with `keelbox skills`, written as the agent over its own SSH login. keelbox removes none of them. |
| `~/.local/bin/claude`, `codex` and their data | Whatever the vendors' installers write, as the agent. keelbox only checks each command is on the agent's PATH, and leaves its version and updates to the CLI. |

The CLIs write the agent's sign-ins when you sign in: `~/.claude`, `~/.codex/auth.json` and `~/.config/gh`. keelbox writes none of them.

### The welcome

Unless `sessions.welcome` is `false`, tmux and bash print [the welcome](../guides/sessions.md#the-welcome) in a new session's first pane:

- The tmux hook `session-created[90]` puts the pane's id in the session option `@keelbox-welcome`.
- The `sessions` block runs `/usr/local/bin/keelbox welcome` in that pane and clears the mark. `keelbox help` does not list that command. A mark over 30 seconds older than its session is dropped.
- When the command could not run, the block sets the mark to `retry`. The tmux hook `client-attached[90]` then starts the pane's shell again, only while it is bash at its first prompt.

Every other interactive shell in tmux pays one `tmux display-message` for it.

## Not managed

- Your projects, and anything in the agent's home not listed above, any agent CLI the agent installs for itself included.
- Containers and images.
- Tailscale's state and ACLs.
- The agent CLIs' versions after install: they update themselves.

[Remove a box](../guides/removing-a-box.md) says what stays behind.
