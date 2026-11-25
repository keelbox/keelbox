# Config file

```sh
keelbox init acme.yaml --box acme --provider ssh
```

One YAML file describes one box. `keelbox init` writes a commented file; `keelbox up` writes the same file, with your answers, to `~/.config/keelbox/boxes/<box>.yaml`. After you edit it, run `keelbox apply acme`.

## Rules for the whole file

- `version: 1` is required. Only a change that breaks old files bumps it.
- An unknown key is an error, such as `unknown field "hostnme" in Host`.
- Every problem is reported at once, each with its key path, and the command exits 2.
- A toggle left out takes its default, not `false`.
- `~/` resolves where keelbox runs. Under `sudo`, that is the home of the user who ran sudo.
- Key files are read where keelbox runs and inlined before the config goes to the box.
- A secret is named by file path only. A file that group or others can read is refused: `chmod 600`.
- On the box the applied config is `/etc/keelbox/keelbox.yaml`, mode 600, root's, with keys inlined, `target.provider: local` and secret paths pointing at the copies on the box.
- An older keelbox rejects keys it does not know. Remove them before `keelbox rollback` to it.

## Top level

| Key | Type | Default | Rules |
|---|---|---|---|
| `version` | int | none | Required. Must be `1`. |
| `box` | string | none | Required. Lowercase letters, digits and hyphens, starting and ending with a letter or digit, at most 63 characters. Names the cloud server, the tmux status label and, unless `host.hostname` is set, the host. On AWS at least 2 characters; on Google Cloud it starts with a letter and has at most 49. |
| `channel` | string | `stable` | Only `stable` exists. |
| `agents` | list | `[claude-code, codex]` | Each of `claude-code`, `codex`, at most once. `[]` installs no agent CLI. Each is installed by its vendor's installer into the agent's home and updates itself. |

## target

Where the box runs and how keelbox reaches it.

| Key | Type | Default | Rules |
|---|---|---|---|
| `target.provider` | string | `local` | `local`: keelbox runs on the box. `ssh`: a machine you have. `hetzner`, `aws`, `digitalocean` or `gcp`: keelbox creates the server. |
| `target.ssh.host` | string | none | Required for `ssh`. An address or DNS name, not an `~/.ssh/config` alias. Optional for a cloud provider: commands after `create` connect to this name, trusting only the host keys already pinned for the server's IP. |
| `target.ssh.port` | int | `22` | 1 to 65535. |
| `target.ssh.user` | string | `users.admin.name` | The account keelbox connects as. `--ssh-user` overrides it for one command. |
| `target.ssh.identity_file` | path | none | The private key for ssh. Unset: your ssh agent and default keys. |
| `target.ssh.known_hosts` | path | `~/.config/keelbox/known_hosts` | keelbox's own known_hosts, checked strictly. `~/.ssh/known_hosts` is not read. |
| `target.ssh.tailnet_host` | string | none | Where to reach the box on the tailnet: `host` or `host:port`. Unset: the Tailscale IPv4 the box reports. Commands try it first, then the public address, and it inherits the public address's pinned host keys. Needs `access.tailscale`. |
| `target.ssh.host_key` | string | none | `SHA256:` and 43 base64 characters, as `ssh-keygen -l` prints. keelbox pins only the offered key with this fingerprint. Unset: keelbox shows the fingerprints and asks, and refuses without a terminal. |
| `target.hetzner.server_type` | string | none | Required for `hetzner`. A server type such as `cpx22`; the architecture follows it. Editing it does not change an existing server: [resize](../guides/resize-park-resume.md) does, and rewrites this value. |
| `target.hetzner.location` | string | none | Required for `hetzner`. A location such as `fsn1`. |
| `target.hetzner.image` | string | `ubuntu-24.04` | A Hetzner system image for the type's architecture. `ubuntu-26.04` also works. |
| `target.hetzner.ssh_keys` | list | none | Names of SSH keys in the Hetzner project. `create` and `resume` need one. `create` installs them for `root` for the first connection, and `resume` names the same keys. |
| `target.hetzner.credential` | string | `hetzner` | The token's name in your OS credential store. Lowercase letters, digits, `.`, `_` and `-`. More in [the Hetzner token](../guides/credentials.md#hetzner-an-api-token). |
| `target.hetzner.token_file` | path | none | A mode 600 file holding the API token, instead of the credential store. Lookup order: `HCLOUD_TOKEN`, this file, `hcloud_context`, the credential store, then `~/.config/keelbox/hetzner.token`. |
| `target.hetzner.hcloud_context` | string | none | A context of Hetzner's `hcloud` CLI, 1.44.0 or newer. keelbox asks hcloud for the token each time and keeps no copy. Not with `token_file`. More in [the hcloud CLI's token](../guides/credentials.md#using-the-hcloud-clis-token). |
| `target.hetzner.labels` | map | none | Extra server labels, in Hetzner's syntax. keelbox always adds `keelbox=true` and `keelbox-box=<box>`, and deletes only servers with both. |
| `target.aws.region` | string | none | Required for `aws`. A region with Lightsail, such as `eu-central-1`. |
| `target.aws.profile` | string | none | The AWS CLI profile whose sign-in keelbox uses. Unset: the CLI's default. More in [AWS sign-in](../guides/credentials.md#aws-nothing-kept). |
| `target.aws.bundle` | string | none | Required for `aws`. An active Linux Lightsail bundle id with a public IPv4 address, such as `medium_3_0`. Editing it does not change an existing instance. `resize` moves to a larger plan only, and rewrites this value. `resume` needs a plan at least as large as the one the box was parked on. |
| `target.aws.blueprint` | string | `ubuntu_24_04` | An active Lightsail OS blueprint id: Ubuntu 24.04 or newer. |
| `target.aws.availability_zone` | string | none | Such as `eu-central-1a`. Unset: the region's first available zone. |
| `target.aws.tags` | map | none | Extra instance tags. Letters, digits and `+ - = . _ : / @`; no key starting `aws:`. keelbox always adds `keelbox=true` and `keelbox-box=<box>`, and deletes only instances with both. |
| `target.digitalocean.region` | string | none | Required for `digitalocean`. A region slug such as `fra1`. A parked box resumes only in its snapshot's region. |
| `target.digitalocean.size` | string | none | Required for `digitalocean`. A Droplet size slug such as `s-2vcpu-4gb`. Editing it does not change an existing Droplet; `resize` does, and rewrites this value. At `resume` its disk must be at least the box's. |
| `target.digitalocean.image` | string | `ubuntu-24-04-x64` | An image slug or id: Ubuntu 24.04 or newer. |
| `target.digitalocean.context` | string | none | The doctl context whose sign-in keelbox uses. Unset: doctl's current one. Letters, digits, `.`, `_` and `-`. More in [DigitalOcean sign-in](../guides/credentials.md#digitalocean-nothing-kept). |
| `target.digitalocean.tags` | list | none | Extra Droplet tags. Letters, digits, `:`, `_` and `-`. keelbox always adds `keelbox` and `keelbox-box-<box>`, which this list cannot name, and deletes only a Droplet with both. |
| `target.gcp.project` | string | none | The Google Cloud project id. Unset: gcloud's configured project. `keelbox up` always writes it. More in [Google Cloud sign-in](../guides/credentials.md#google-cloud-nothing-kept). |
| `target.gcp.zone` | string | none | Required for `gcp`. A Compute Engine zone such as `europe-west3-a`. |
| `target.gcp.machine_type` | string | none | Required for `gcp`. An x86 machine type of the zone, such as `e2-medium`; the image is Ubuntu 24.04. Editing it does not change an existing VM; `resize` does, and rewrites this value. |
| `target.gcp.disk_gb` | int | `50` | Boot disk size in GB, 10 to 65536, deleted with the VM. Used only at create: `resize` keeps the disk, and `resume` restores the size it had. |
| `target.gcp.labels` | map | none | Extra VM labels. Lowercase letters, digits, `_` and `-`, the key starting with a letter. keelbox always adds `keelbox=true` and `keelbox-box=<box>`, which this map cannot name, and deletes only a VM with both. |

`create` checks the region, size and image with the cloud before creating anything, and lists the values it accepts.

## host

| Key | Type | Default | Rules |
|---|---|---|---|
| `host.hostname` | string | `box` | Same rule as `box`. Added to `/etc/hosts` when it does not resolve. `keelbox up` writes the machine's own hostname here when it differs from the box name. |
| `host.timezone` | string | `UTC` | A zone name in `/usr/share/zoneinfo`, such as `Europe/London`. |
| `host.locale` | string | `C.UTF-8` | A UTF-8 locale such as `en_GB.UTF-8`, generated if needed and set as `LANG`. |
| `host.swap` | size | `2G` | Such as `512M` or `2G`; `0`, `off` or `none` leaves swap alone. `/swapfile` is created only on a machine without swap. |
| `host.packages` | list | none | Extra apt packages. Removing a name leaves the package installed. |
| `host.unattended_upgrades` | bool | `true` | Unattended security updates. `false` turns them off. |
| `host.auto_reboot` | string | `nightly` | When the box reboots for an installed update: [values below](#auto_reboot). |
| `host.firewall.enabled` | bool | `true` | UFW: deny inbound, allow outbound, allow 22/tcp, allow everything on `tailscale0`. `false` leaves UFW as it is. |
| `host.firewall.public_ssh` | string | `auto` | When 22/tcp is open to the internet: [values below](#public_ssh). `closed` with Tailscale off is an error; `auto` and `lan` then leave it open. `true` and `false` read as `open` and `closed`. |
| `host.firewall.allow` | list | `[]` | Ports open to the LAN or a network you name, besides SSH. The tailnet reaches every port regardless. [Fields below](#allow). `keelbox up` offers the services it finds listening. |
| `host.firewall.link_local_accounts` | list | `[]` | Accounts besides root that keep link-local addresses, a cloud's metadata service among them, on every port: [below](#link_local_accounts). |
| `host.power.sleep` | string | `allow` | `never` masks the sleep, suspend and hibernate targets and has logind ignore the lid and idle; with a display manager running, the logind part takes effect at the next boot. Reboots and shutdowns still work. `allow` undoes only what `never` did. For [a laptop or desktop used as a box](../guides/machine-in-use.md). |
| `host.disk.warn_percent` | int | `85` | 1 to 99, below `fail_percent`. Raises `disk-warn` when `/`, `/home` or `/var/lib/docker` is this full, and daily while it stays over. |
| `host.disk.fail_percent` | int | `95` | At most 99. Raises `disk-full`. |

### auto_reboot

| Value | Effect |
|---|---|
| `auto_reboot: nightly` | Reboots between 01:00 and 06:00 in `host.timezone`, once idle for 30 minutes, and raises `rebooting`. |
| `auto_reboot: weekly` | The same on Sunday night. If the box is busy all night, it reboots on the next idle night. |
| `auto_reboot: never` | Only `keelbox reboot` reboots it. |
| `auto_reboot: "04:00"` | unattended-upgrades reboots at that time, whatever is running. |

Idle means all of these: no one typed in a login for a few minutes and no desktop session is logged in; the agent's tmux sessions had no output and no Claude Code or Codex conversation was written for 5 minutes; no Claude Code process says it is at work; the agent uses under 10% of the CPUs; no backup runs. A connection without a terminal, such as VS Code's, also holds off the reboot. The box reboots at most once in 20 hours.

After any orderly reboot, each tmux pane that ran Claude Code or Codex reopens its conversation: keelbox types `claude --resume ID` or `codex resume --last`, keeping flags such as `--model`. That holds for a reboot by the box, by `keelbox reboot`, by unattended-upgrades at a set time, by `sudo reboot` or from a cloud's console, and after the box was shut down. It needs `sessions.recovery`. A crash or a power loss gives no chance to note the conversations, and they do not reopen.

### public_ssh

| Value | Effect |
|---|---|
| `public_ssh: auto` | Closes 22/tcp once an apply has reached sshd over the tailnet as the admin. Opens again if that stops holding. |
| `public_ssh: lan` | As `auto`, but the private ranges keep SSH: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7`. `keelbox up` sets it on a LAN when you keep SSH there. |
| `public_ssh: open` | Never closes it. |
| `public_ssh: closed` | As `auto`, and stays closed while the tailnet is down. doctor fails until it is closed. |

Closed means the UFW rule is removed, sshd refuses logins on addresses outside the tailnet, and the cloud firewall of a server keelbox created drops 22. More in [closing public SSH](../guides/public-ssh.md).

### allow

| Entry | Field | Rules |
|---|---|---|
| port | `port` | One port, 1 to 65535, or a range such as `8000-8010`. Never SSH or a range that includes it. |
| port | `proto` | `tcp` (default) or `udp`. |
| service | `service` | A preset from [known services](services.md). Opens every port the vendor lists, TCP and UDP; takes no `port` or `proto`. |
| both | `from` | `lan` (default): the private ranges under `public_ssh`. Or a CIDR such as `192.168.1.0/24`, never `0.0.0.0/0`. |
| both | `name` | Optional rule name: lowercase letters, digits and hyphens, at most 32. A service's rules are named after it. |

```yaml
host:
  firewall:
    allow:
      - {service: unifi}
      - {name: games, port: 27015-27030, proto: udp}
```

Each port and source gets a UFW rule commented `keelbox allow NAME`, and the same exception in the guard for system Docker containers' published ports. Removing an entry removes its rules. An entry with `host.firewall.enabled: false` is an error.

### link_local_accounts

keelbox refuses link-local addresses (`169.254.0.0/16`, `fe80::/10`) to every account but root. Ubuntu's own service accounts keep only their DNS, time and DHCP traffic there. The full rule: [the agent's network rule](../security-model.md#the-agents-network-rule).

Name here the account of a service that must reach those addresses, such as a cloud's monitoring agent:

```yaml
host:
  firewall:
    link_local_accounts: [do-agent]
```

| Rule | Detail |
|---|---|
| Names | As in `/etc/passwd`, at most 16. `root` is refused, as no rule limits it; so is the agent account, as naming it would undo the limit. |
| What a name opens | Each account named can be used as a relay to those addresses, if the agent can make its service connect where the agent says. |
| An account the box lacks | Gets no rule. `keelbox doctor` warns until the account exists or the name is removed. |
| What stays refused | The box's own SSH port. |
| UFW | Not a UFW setting: it applies with `host.firewall.enabled: false` too. |

## users

| Key | Type | Default | Rules |
|---|---|---|---|
| `users.lock_cloud_user` | bool | `true` | Lock [the image's default account](../guides/adopting-a-box.md#the-images-default-account), such as `ubuntu`, once the admin is proven. `keelbox up` on this machine writes `false` when run from that account. |
| `users.admin.name` | string | `admin` | A Linux user name: lowercase, starting with a letter or `_`, at most 31 characters. Not `root`, `daemon`, `bin`, `sys`, `nobody`, `syslog`, `docker` or the agent's name. |
| `users.admin.ssh_authorized_keys` | list | none | OpenSSH public key lines: `ssh-ed25519`, `ssh-rsa`, `ecdsa-sha2-nistp*`, `sk-ssh-ed25519@openssh.com` or `sk-ecdsa-sha2-nistp256@openssh.com`. |
| `users.admin.ssh_authorized_key_files` | list | none | Paths to `.pub` files, one or more keys each, read on the computer that runs keelbox, so the same path can be another key on another computer. `keelbox up` writes the key itself under `ssh_authorized_keys` instead. The admin needs at least one key from either list, because apply turns off SSH passwords and root login. |
| `users.admin.sudo` | bool | `true` | The admin always has passwordless sudo. `false` is an error; leave the key out. |
| `users.agent.name` | string | `agent` | As `users.admin.name`. |
| `users.agent.ssh_authorized_keys` | list | none | As for the admin. `login`, `attach`, `sessions` and `ssh` log in as the agent with one of these. |
| `users.agent.ssh_authorized_key_files` | list | none | As for the admin. |
| `users.agent.permissions` | string | `auto` | `auto`: `keelbox install`, run by the agent, installs Ubuntu packages through a root service that checks each request. `manual`: the same command records a request for `keelbox approve`. `full`: passwordless sudo, and the security model's limits on the agent do not hold. More in [what the agent may change](../guides/work-on-the-box.md#what-the-agent-may-change). |
| `users.agent.sudo` | bool | `false` | Alias of `users.agent.permissions: full`. A value that disagrees with `permissions` is an error naming the key to remove. |
| `users.agent.limits.cpu` | string | none | A share of all CPUs for the agent's processes together, `5%` to `100%`, as `CPUQuota` on the agent's `user-UID.slice` (75% of 4 CPUs is `CPUQuota=300%`). `keelbox up` proposes 75% for CPU and memory on [a machine that runs other services](../guides/machine-in-use.md). |
| `users.agent.limits.memory` | string | `90%` | A share of RAM, `10%` to `95%`; a size of at least 256M such as `8G`; or `none`. At this ceiling (`MemoryMax`) the kernel stops the agent's largest process and the rest keep running; the same share of swap applies. The remaining RAM keeps SSH, Tailscale and the admin responsive. A ceiling you set also slows the agent's services from 90% of it (`MemoryHigh`); the default does not. `none` lifts the ceiling; `limits: {memory: none}` alone removes every limit. |
| `users.agent.limits.cpu_weight` | int | `50` | `CPUWeight` on `user.slice` and the agent's slice, 1 to 10000. System services have 100, so they get two thirds of contended CPU. |
| `users.agent.limits.io_weight` | int | `50` | As `cpu_weight`, for disk (`IOWeight`). Works only on disks using the `bfq` scheduler; doctor says which do. |
| `users.agent.git.name` | string | none | The name on the agent's commits: one line, at most 100 characters, no `<` or `>`. With `email`, apply sets `user.name`, `user.email` and, when unset, `init.defaultBranch main` in the agent's global git config. Without both, git on the box refuses to commit and doctor warns. |
| `users.agent.git.email` | string | none | The email on the agent's commits. Needs `name`. |

Keys in keelbox's managed block of `authorized_keys` are replaced on each apply. Keys you add outside the block are kept.

## runtime

| Key | Type | Default | Rules |
|---|---|---|---|
| `runtime.docker` | bool | `true` | Docker Engine, Compose and Buildx from Docker's apt repository: the system daemon for root, and a rootless daemon for the agent. |
| `runtime.node` | bool | `true` | mise in `/usr/local/bin`, with the release's Node and pnpm for the agent. |
| `runtime.gh` | bool | `true` | GitHub CLI from GitHub's apt repository. |

Setting a toggle to `false` leaves what is installed; keelbox stops managing it.

## sessions

| Key | Type | Default | Rules |
|---|---|---|---|
| `sessions.tmux` | bool | `true` | The managed tmux config and the sessions module. `false` also turns off `recovery`, `start_on_boot` and `welcome`. |
| `sessions.recovery` | bool | `true` | tmux-resurrect and tmux-continuum: sessions are saved every 15 minutes and restored when tmux starts. `true` with `tmux: false` is an error. |
| `sessions.start_on_boot` | bool | `true` | `keelbox-tmux.service` starts the agent's tmux server at boot, so saved sessions come back without a login. |
| `sessions.watchdog` | bool | `true` | Every two minutes, restarts the agent's tmux server if it is gone and a stopped Remote Control. At most 3 restarts an hour per unit, then one alert and hourly tries. Needs `start_on_boot`. More in [after a reboot](../guides/sessions.md#after-a-reboot). |
| `sessions.welcome` | bool | `true` | A new tmux session opens with [the welcome](../guides/sessions.md#the-welcome), once. `false` removes it; the status line stays. |
| `sessions.remote_control.enabled` | bool | `false` | Runs `claude remote-control` as the agent's user service `keelbox-claude-remote-control.service`, for [the Claude app](../guides/remote-control.md). Needs `claude-code` in `agents`; `directory` or `spawn` without it is an error. Starts once you have signed in with `keelbox login`. |
| `sessions.remote_control.directory` | path | `projects` | Where sessions start, relative to the agent's home, not the home itself. Letters, digits, `.`, `_`, `-` and `/`. Created if missing. `keelbox status`, doctor and `keelbox destroy` read the projects in it. |
| `sessions.remote_control.spawn` | string | `same-dir` | `same-dir`: every session in the directory. `worktree`: each in its own git worktree; the directory must be a git repository. |

## access

| Key | Type | Default | Rules |
|---|---|---|---|
| `access.tailscale.enabled` | bool | `true` | Install Tailscale from its signed repository and run `tailscaled`. `false` skips the access module. |
| `access.tailscale.auth_key_file` | path | none | A mode 600 file holding a Tailscale auth key. apply joins the tailnet with it and deletes the box's copy after the run. Unset: `keelbox login` joins. An error with Tailscale off. More in [an auth key file](../guides/automation.md#a-tailscale-auth-key-file). |
| `access.tailscale.hostname` | string | `host.hostname` | The name on the tailnet. Same rule as `box`. |

## primer

| Key | Type | Default | Rules |
|---|---|---|---|
| `primer.enabled` | bool | `true` | Write the primer, `/etc/keelbox/AGENTS.md`, which tells the agents what the box is. Claude Code reads it through `~/.claude/CLAUDE.md`, Codex through `~/.codex/AGENTS.md`. |
| `primer.notes` | string | none | Text appended to the primer under "Box notes". Use a YAML block scalar. |

## previews

Previews publish the agent's dev servers on the tailnet with `keelbox expose`: [preview a dev server](../guides/previews.md).

| Key | Type | Default | Rules |
|---|---|---|---|
| `previews.enabled` | bool | `true` when Tailscale is enabled | Install Caddy, its system user `keelbox-preview` and the previews service. `true` with Tailscale off is an error. `false` skips the module and leaves what an earlier apply installed. |
| `previews.ports` | range | `8401-8499` | The HTTPS ports previews use, on the box's tailnet addresses only. At most 1000 ports, between 1024 and 65535. TCP ports `host.firewall.allow` opens are skipped; a range left with none is an error. |

## backup

Backups are off until `backup.repository` is set; any other backup key without it is an error. Setting one up: [back up and restore](../guides/backups.md).

| Key | Type | Default | Rules |
|---|---|---|---|
| `backup.repository` | string | none | A restic S3 repository on Backblaze B2, Cloudflare R2, AWS S3 or MinIO: `s3:https://HOST[:PORT]/BUCKET[/PREFIX]`, such as `s3:https://s3.eu-central-003.backblazeb2.com/my-bucket/acme` on B2. `s3:http://` is accepted. No credentials in the URL. The first backup creates the bucket if it can. |
| `backup.credentials_file` | path | none | Required. A mode 600 AWS-style credentials file with a `[default]` profile holding `aws_access_key_id` and `aws_secret_access_key`. Copied root-only to `/etc/keelbox/backup/s3.credentials`. With provider `local` it must be in a directory only root can change. |
| `backup.password_file` | path | none | Required. The restic repository password, mode 600, copied to `/etc/keelbox/backup/restic.password`. Without it no snapshot can be read: keep a copy off the box. Must differ from `credentials_file`; the same directory rule holds with provider `local`. |
| `backup.schedule` | string | `daily` | A systemd `OnCalendar` expression for `keelbox-backup.timer`, such as `hourly` or `*-*-* 03:00`. A run missed while the box was off happens at the next boot. |
| `backup.keep.daily` | int | `7` | `restic forget --keep-daily`, per path. `0` leaves the rule out. |
| `backup.keep.weekly` | int | `4` | `--keep-weekly`. |
| `backup.keep.monthly` | int | `6` | `--keep-monthly`. At least one of the three must be above 0. Every run forgets what the policy does not keep, and prunes. |
| `backup.paths` | list | the agent's home | Absolute, clean paths, not `/`, no commas. A path in the agent's or admin's home is read and restored as that account; any other as root. |
| `backup.exclude` | list | `node_modules`, `.cache`, `.npm`, `.pnpm-store`, `.local/share/pnpm`, `.local/share/mise`, `.local/share/claude/versions`, `.codex/packages`, `.local/share/docker`, `.local/share/containers`, `.rustup`, `.cargo/registry`, `go/pkg/mod`, `keelbox-restore` | tar `--exclude` patterns, matched against any part of a path. The defaults leave out what apply reinstalls. Setting the list replaces the defaults: to back up the agent CLIs too, repeat it without `.local/share/claude/versions` and `.codex/packages`. A pattern cannot start with `-`. Directories with a `CACHEDIR.TAG` are always left out. |
| `backup.stale_after` | duration | `48h` | doctor fails when the last successful backup is older. At least `1h`. |

## Notifications and scheduled doctor

Nothing is sent until a channel is listed. Setting one up: [notifications](../guides/notifications.md).

### notify

| Key | Type | Default | Rules |
|---|---|---|---|
| `notify.channels` | list | `[]` | At most 8. Fields under [channels](#channels). |
| `notify.mute` | list | `[]` | Event kinds never sent, such as `agent-done`. Kinds under [events](#events). |
| `notify.quiet_hours` | string | none | `HH:MM-HH:MM` in the box's timezone; may cross midnight. Events below urgent wait and arrive as one digest when quiet hours end. |
| `notify.quiet_hours_urgent` | bool | `true` | Urgent events go through quiet hours. |
| `notify.agent_text` | bool | `false` | Add the agent CLI's own notification text, at most 200 characters, which can name commands and files. Without it an agent event carries the box name, the CLI, the kind, the reason, the project directory's name and the time. |

### Channels

| Type | Field | Required | Rules |
|---|---|---|---|
| every type | `name` | yes | Unique. Lowercase letters, digits and hyphens, at most 32 characters. |
| every type | `type` | yes | `ntfy`, `pushover`, `telegram` or `webhook`. |
| every type | `level` | no | The lowest level sent: `info` (default), `warn` or `urgent`. |
| ntfy | `topic_file` | yes | A file holding the topic. On ntfy.sh anyone who knows the topic can read and post to it. |
| ntfy | `server` | no | Default `https://ntfy.sh`. A server URL without credentials or a path. |
| ntfy | `token_file` | no | A file holding an access token, sent as `Authorization: Bearer`. |
| Pushover | `token_file` | yes | A file holding the application's API token. |
| Pushover | `user_file` | yes | A file holding your user key or a group key. |
| Telegram | `chat_id` | yes | A numeric chat id, or `@name` for a channel the bot posts to. Not a secret, so it goes in the config. |
| Telegram | `bot_token_file` | yes | A file holding the bot's token. |
| webhook | `format` | yes | `json` or `text`. |
| webhook | `url_file` | yes | A file holding the URL, without credentials. |
| webhook | `header_file` | no | A file holding one `Name: value` line, sent as a request header. |

A field of another type is an error.

- Each `*_file` is a mode 600 file with the value on one line. apply copies it root-only to `/etc/keelbox/notify/` on the box.
- The ntfy server and webhook URL must be `https://`, except `http://` to loopback, a tailnet address or a `*.ts.net` name. To an address on the box itself, keelbox sends only when root, a system account or the admin owns the listening socket.
- `format: json` posts an object with `text`, `title`, `message`, `level`, `kind`, `box`, `source`, `time`, `key` and `fields`; Slack and Mattermost show `text`. `format: text` posts one line as `text/plain`.
- A failed send is retried after 2, 4, 8 minutes and so on, at most an hour apart, and dropped after 24 hours. Errors give the HTTP status or network problem, never the URL, header or token.
- `keelbox notify test acme` tests every channel, reading the files the config names, so it works before an apply.

| Level | ntfy priority | Pushover priority | Telegram |
|---|---|---|---|
| info | 3 | -1 | silent |
| warn | 4 | 0 | with a notification |
| urgent | 5 | 1 | with a notification |

Pushover's priority 2, which repeats until acknowledged, is not used.

### Events

| From | Kind | Level | Raised when |
|---|---|---|---|
| agent CLIs | `agent-done` | info | Claude Code or Codex finished a turn. |
| agent CLIs | `agent-needs-input` | warn | Claude Code asks permission, asks a question or has waited about a minute for you. Codex asks for approval. |
| agent CLIs | `agent-limit` | warn | Claude Code's turn ended on a rate limit, or a billing, sign-in or account error. |
| agent CLIs | `agent-error` | info | Claude Code's turn ended on another API error. |
| doctor | `doctor-failed` | urgent | A scheduled doctor check started failing. Raised once, however long it fails. |
| doctor | `doctor-warning` | warn | A check started warning, with `doctor.notify_warnings`. |
| doctor | `doctor-cleared` | info | A failing check passes again. |
| watchdog | `watch-recovered` | info | The watchdog brought back the tmux server or Remote Control. |
| watchdog | `watch-gave-up` | urgent | The watchdog used up its restarts for the hour. |
| disk | `disk-warn` | warn | A disk reached `host.disk.warn_percent`. |
| disk | `disk-full` | urgent | A disk reached `host.disk.fail_percent`. |
| memory | `out-of-memory` | warn | The kernel stopped agent processes for lack of memory. Names them, with what each held, where the kernel log has them. |
| power | `power-back` | warn | The box is back after [an unclean shutdown](../guides/machine-in-use.md#after-a-power-cut-or-crash), three minutes after boot. |
| updates | `updates-pending` | warn | Security updates wait, or a reboot is needed. Again weekly while either stays. |
| updates | `rebooting` | info | The box reboots for an update, with `host.auto_reboot` nightly or weekly. |
| apply | `apply-finished` | info | An apply ended while no computer followed it. |
| apply | `apply-failed` | urgent | Such an apply failed. |
| installs | `install-request` | warn | With `users.agent.permissions: manual`, a package request waits for `keelbox approve`. Names the packages, not the agent's reason. |
| keelbox | `test` | info | `keelbox notify test`. |
| keelbox | `notify-throttled` | warn | Agent events went over their budget and were dropped. |
| backup | `backup-failed` | warn | Accepted in `notify.mute`; never raised. |
| backup | `backup-stale` | urgent | Accepted in `notify.mute`; never raised. |

Codex has no hook for usage limits, so a Codex limit raises no event.

Agent events have a budget: one per session and kind every 2 minutes, 30 an hour, 100 a day. An `agent-done` within 30 seconds of an `agent-needs-input` for the same session is dropped. Over budget, one `notify-throttled` an hour says how many were dropped. keelbox's own events have no budget.

### doctor

| Key | Type | Default | Rules |
|---|---|---|---|
| `doctor.schedule` | string | `hourly` | When doctor runs on the box, besides five minutes after boot: a systemd `OnCalendar` expression, or `off`. A network-dependent check must fail twice in a row to count. |
| `doctor.notify_warnings` | bool | `false` | Also raise `doctor-warning` when a check starts warning. |

Only this scheduled run records its result and raises events; `keelbox doctor` from your computer does neither. doctor warns when a channel's last send failed, and when an event has waited over an hour.

## reports

Problems with keelbox that the agent writes up, after asking you, for you to review and send: [reports](../guides/reports.md).

| Key | Type | Default | Rules |
|---|---|---|---|
| `reports.enabled` | bool | `true` | The agent may save keelbox reports to the box's inbox with `keelbox report`, and the primer tells it how. `false` drops that from the primer, and the box discards reports the agent files. |

## Minimal configs

A machine you have, over SSH:

```yaml
version: 1
box: acme
target:
  provider: ssh
  ssh:
    host: 203.0.113.10
users:
  admin:
    ssh_authorized_key_files: [~/.ssh/id_ed25519.pub]
  agent:
    ssh_authorized_key_files: [~/.ssh/id_ed25519.pub]
```

A new cloud server differs in the `target` block only. Hetzner:

```yaml
target:
  provider: hetzner
  hetzner:
    server_type: cpx22
    location: fsn1
    ssh_keys: [laptop]
```

AWS, a Lightsail instance:

```yaml
target:
  provider: aws
  aws:
    region: eu-central-1
    bundle: medium_3_0
    profile: keelbox
```

DigitalOcean, a Droplet:

```yaml
target:
  provider: digitalocean
  digitalocean:
    region: fra1
    size: s-2vcpu-4gb
    context: keelbox
```

Google Cloud, a Compute Engine VM:

```yaml
target:
  provider: gcp
  gcp:
    project: my-project
    zone: europe-west3-a
    machine_type: e2-medium
```
