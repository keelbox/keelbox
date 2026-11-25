# Security model: what keelbox enforces and what it does not stop

```sh
keelbox doctor acme
```

keelbox limits what code an agent runs can do to the box. It treats that code as possibly hostile to the box, but not to the agent's own files: the agent reads its code and tokens to work, and anything the agent can read can leave the box. `keelbox doctor` reads the running box and reports each limit it checks that is not in force.

## Who holds what

| Who | Holds | Can do |
|---|---|---|
| Your computer | the admin's SSH key, keelbox's `known_hosts`, the box config, the cloud sign-in | everything: it applies any config to the box |
| `admin`, your account on the box | SSH login by key, passwordless sudo | everything on the box |
| `agent`, which runs the agent CLIs | its home and projects, its sign-ins, its own rootless Docker | read all of that and send it anywhere; no sudo, not in the `docker` group |
| Agent containers | the agent's subordinate user IDs | less than the agent |
| The tailnet, your private Tailscale network | a way past the box's firewall, to every port and preview | what your Tailscale ACLs allow |
| The cloud account | the server, its firewall and console, a parked box's snapshot | read the disk and replace the server, outside every limit here |
| Your Claude account, with Remote Control on | sessions on the box, as the agent | what the agent can do |
| Anthropic and OpenAI | Claude Code and Codex: their installers and every later update | run as the agent, with the agent's rights |
| keelbox's release hosts | the installer, the channel index, the binaries | withhold updates, change the installer; a binary also needs the release key's signature |
| Notification channels | each message: box name, project name, event kind and time | deliver it, and whatever their operator does with it |

## What is enforced and how it is checked

| Control | Enforced by | Shown by |
|---|---|---|
| Root in an agent container is not root on the box | the agent's own rootless Docker daemon; the system daemon's socket is closed to the agent | doctor: `docker-rootless`, `docker-system-socket` |
| The agent has no sudo | no sudoers rule, no membership of `sudo` or `docker` | doctor: the agent's sudoers file, or `sudo` membership |
| Connections to the box's SSH port are refused to every account but root. So are link-local and cloud metadata addresses, bar four service accounts and those you name | an nftables table that matches the user ID owning a socket | doctor: `agent-net` |
| SSH takes keys only, refuses root and forwards no SSH agent to the agent | sshd; tmux also drops `SSH_AUTH_SOCK` | doctor, from `sshd -T`: `ssh-password-auth`, `ssh-root-login`, `ssh-agent-forwarding` |
| Inbound traffic is denied, except SSH, the tailnet and the ports you allow | UFW, a guard on system Docker's forwarding, and the cloud's firewall in front | doctor: `firewall-active`, `docker-inbound-guard` |
| SSH from the internet closes once keelbox has logged in over the tailnet | UFW, sshd and the cloud's firewall | doctor: `public-ssh`, `public-ssh-firewall`, `public-ssh-sshd` |
| Previews are served on the tailnet only | Caddy's listening addresses, its unit's IP policy, UFW | doctor: `binding`, `caddy-sandbox`, `firewall` |
| Root reads the agent's files as the agent | a child process with the agent's UID, in apply, previews, events, package requests, backups, the watch run's reboot note and the note at shutdown | the end-to-end test; unit tests for package requests |
| A package request is names from the box's apt sources, never a removal | root's install service | doctor: `request-service`; the logbook's `install` records |
| The agent's memory is capped: 90% of RAM by default | a systemd slice with root-owned drop-ins | doctor: `agent-limits` |
| The first SSH connection reaches the server keelbox set up | a pinned host key in keelbox's own `known_hosts` | unit tests; doctor has no check |
| Downloads other than the agent CLIs are the ones the release names | checksums and key fingerprints in the release manifest; the release key's signature on keelbox's binaries | unit tests and the installer's test |
| Secrets stay out of arguments, environment, output and the logbook | root-only files, passed by path | the end-to-end test, for backup and channel secrets |

## What is not stopped

- Outbound traffic is open. The agent, or code it runs, can send source code and anything else in its home anywhere. There is no egress allowlist.
- The agent reads every token it is signed in with: Claude Code, Codex, GitHub, and any key or skill you give it.
- keelbox does not inspect what agents read or do. A repository or web page can instruct an agent to misuse whatever it reaches.
- A kernel or privilege-escalation bug that gets past Linux users, user namespaces and netfilter gets past keelbox.
- With `permissions: auto`, the default, the agent chooses which Ubuntu packages are added. A package can bring a listening service or a flawed privileged program.
- With `permissions: full` the agent has sudo, and none of the limits bind.
- Whoever holds the admin key holds the box. Whoever administers the cloud account can read its disk, and a parked box's snapshot is a copy keelbox does not encrypt.
- Every device your Tailscale ACLs let reach the box can open its previews and ports, without a sign-in.
- With Remote Control, anyone signed in to your Claude account runs commands as the agent.
- The agent's network rule covers only addresses keelbox knows and sockets not root's: [what it leaves open](#the-agents-network-rule).
- A root attacker can make doctor report anything. Doctor reports configuration drift; it is not intrusion detection.

The full list is under [what keelbox does not defend against](#what-keelbox-does-not-defend-against).

## In detail

Each control ends with how it is checked: `keelbox doctor` on the running box, the end-to-end test on a real VM, unit tests, or tests against a simulated box. A missing check is named.

### The agent's network rule

An nftables table refuses two kinds of connection to every account but root. It matches the user ID that owns the socket, so it also covers the agent's containers and subordinate UIDs, services under their own accounts, and the admin, who needs `sudo` for `ssh localhost`.

**SSH to the box.** Port 22 is refused on the box's own addresses and at each outside address on record, such as a cloud's translated public address or a router's forwarded port. keelbox commands from your computer report these, and apply keeps the 16 most recent in `/etc/keelbox/outside-addresses`. sshd also forwards no SSH agent to the agent, and tmux drops `SSH_AUTH_SOCK`. Checked by doctor, unit tests and the end-to-end test; the tmux change only by a unit test of the generated config.

**Cloud metadata.** Link-local addresses (`169.254.0.0/16`, `fe80::/10`) and `fd00:ec2::254` are refused, so instance data and cloud-init user data stay root's. The agent's UID and `/etc/subuid` ranges are always refused; doctor fails on a range added since the last apply. `systemd-resolve`, `systemd-timesync`, `_chrony` and `systemd-network` pass for DNS, time and DHCP only. Accounts in `host.firewall.link_local_accounts` pass on every port, and each is a relay if the agent can drive its service. Checked by doctor, unit tests, tests against a simulated box and the end-to-end test, whose check that root still reaches each cloud's metadata service has run only on a local VM.

Because the rules match every packet, a service that is not root's cannot answer a client on a link-local address, and a box whose `/etc/resolv.conf` names only link-local resolvers (no systemd-resolved stub) resolves names for root only. Doctor and `keelbox plan --impact` report that with the fix. On a machine in use the report also names services connected to link-local addresses at that moment, and can miss one that connects only now and then. A packet the kernel sends for no process has no owner and passes; this follows from the kernel's source and has not been seen on a box.

What the rule leaves open:

- **Addresses keelbox was never told**: a forwarded port or load balancer in no config, a router's WAN address or a translated cloud address when `keelbox up` ran on the machine itself, and a new public address until the next `keelbox apply` from your computer. To cover one, add `ADDRESS:PORT` to `/etc/keelbox/outside-addresses` and run `keelbox apply`.
- **SSH through another machine**: a connection forwarded back to the box arrives as an outside client's.
- **The first minute after a resume on AWS and Google Cloud**: until the apply at the end of `keelbox resume`, the file has the old public address while the agent's services and containers already run. An AWS `resize` keeps its static IP.
- **tailscaled before Tailscale 1.98**: its local API dials for any caller as root, so `tailscale nc` reaches port 22 and the metadata address. From 1.98 the client dials non-tailnet addresses itself. This is read from Tailscale's source. keelbox does not upgrade a Tailscale already installed, and unattended upgrades skip it: run `sudo apt-get update && sudo apt-get install --only-upgrade tailscale`.
- **Root services that connect for a caller**: a package's service under its own account is refused, one running as root is not. The package deny-list names known relays, not every one.

In the first three cases the agent meets sshd as an internet client does: keys only, and once public SSH is closed only at a tailnet or loopback address (or the LAN, with `public_ssh: lan`). Give the agent its own key, never the admin's.

## What keelbox defends against

### Agent to root through Docker

The agent runs its own rootless Docker daemon, so root in its containers is an unprivileged subordinate UID on the box. The system daemon's socket, which is root-equivalent, is closed to it. Checked by doctor and the end-to-end test.

### Agent to root through its own files

apply, previews, events, package requests, backups and the watch run read and write the agent's files through a child process with the agent's UID, so a symlink the agent plants gets root no further than the agent could go. Root's own file operations refuse any path whose directories a non-root account owns or can write. Files are opened without blocking, and anything but a regular file is refused. Backups work the same way: restic runs as root for the credentials, and tar archives and restores each path as its owner. Checked by tests against a simulated box and the end-to-end test, which plants links to `/etc` and `/etc/shadow`, hostile entries and FIFOs.

### Previews on a public address

Caddy listens only on the box's tailnet addresses, its unit allows IP traffic only to loopback and the tailnet ranges, and UFW admits preview ports only on `tailscale0`. With nothing to serve, or the box off the tailnet, Caddy is stopped. Root reads the agent's preview file as the agent and keeps only valid names and ports, and Caddy's admin API is off. Checked by doctor (`binding`, `caddy-sandbox`, `firewall`), tests against a simulated box and the end-to-end test.

### Forged alerts and the notification channels

The agent's CLIs report events by appending to a file in its home. Root takes only known agent event kinds with checked fields, and sets the source, level and box, so the agent cannot raise keelbox's own alerts. Agent events are capped at one per session and kind every two minutes, 30 an hour and 100 a day, then one notice an hour. They are titled with the agent CLI and carry the agent's text only with `notify.agent_text`, quoted and at most 200 characters. The queue is root-only, and reading a flood costs root at most 2 MiB every 5 seconds.

The hooks are managed settings in `/etc/claude-code` and `/etc/codex`, which the agent's settings cannot turn off; a CLI the agent replaced can skip them. Channel secrets are root-only, in `/etc/keelbox/notify/`, and never in arguments, environment, output, the logbook or an error. Plain `http://` goes only to loopback or the tailnet, and keelbox sends to a port on the box only when root, a system account or the admin owns the socket.

Checked by unit tests and the end-to-end test, which forges lines and searches the box's state, journal and process list for the secrets.

### Agent to root through package requests

With `users.agent.permissions: manual` or `auto` the agent has no sudo, only `keelbox install NAME`. Root's service reads the request as the agent, at most 16 KiB and following no link, and:

- accepts at most 20 plain package names, with no versions, paths, URLs or options, each with an install candidate from the box's existing apt sources;
- refuses a request whose simulated run would remove any package, install or upgrade more than 300, or touch a deny-listed package, even as a dependency;
- installs with `apt-get install -y --no-remove`, and never removes, purges, holds or upgrades on request;
- handles one request at a time, at most 30 an hour and 20 waiting for approval, under IDs root issues;
- records each in the logbook as an `install` record of checked names, never the agent's text or apt's output.

The deny-list applies in every mode. It covers remote login servers, firewall tools, sudo and other tools that run commands as another account; Docker, containerd, Tailscale, AppArmor and other security modules; apt, dpkg, unattended-upgrades, systemd, the system bus, PAM and NSS; cloud guest agents, network managers, kernels, boot loaders and kernel modules; schedulers other than cron and at, and proxies that take requests from the box.

With `manual` nothing is installed until you run `keelbox approve` from your computer, and the checks run again first. On the box, `keelbox approve` needs root. Checked by doctor (`request-service`), unit tests, tests against a simulated box for each mode, and the end-to-end test. What a package brings once installed is [below](#what-an-installed-package-brings).

### Unintended public ports

UFW denies inbound traffic except SSH, the tailnet and the ports in `host.firewall.allow`, which it admits only from private ranges or the network the entry names. A cloud box also sits behind the cloud's firewall, which admits only SSH and Tailscale's UDP 41641: a Cloud Firewall on Hetzner and DigitalOcean, the instance firewall on AWS Lightsail, and two rules on the box's network tag on Google Cloud. System Docker's published ports bypass UFW, so a `DOCKER-USER` guard drops forwarded connections except from `tailscale0`, a Docker bridge, or an allowed network to an allowed port. Rootless ports are ordinary sockets behind UFW. Checked by doctor, including the cloud firewall when run from your computer, and the end-to-end test.

An allowed port is open to everyone on that network. A range or a [service preset](reference/services.md) opens several ports, never SSH. On Google Cloud, a rule with a priority number below 901, or an organisation or network firewall policy, can admit more; doctor warns about rules, not policies. On DigitalOcean and AWS the cloud firewall arrives seconds after a new server starts ([below](#the-clouds-firewall-on-a-new-server)).

### SSH

sshd takes keys only and refuses root. Doctor reads `sshd -T`, so a drop-in that overrides keelbox's is caught.

Public SSH closes once keelbox has logged in over the tailnet as the admin, which the box confirms from the session's own addresses. keelbox then removes the public `22/tcp` UFW rule and the cloud firewall's SSH rule, and sshd refuses logins at any address that is not the tailnet's or loopback. Without that proof SSH stays open, so a broken tailnet does not lock you out. `host.firewall.public_ssh: open` keeps it open, and `lan` keeps it for private ranges. Checked by doctor and the end-to-end test on a local VM and Hetzner; on AWS, DigitalOcean and Google Cloud the firewall step is tested against a fake CLI only.

### Machine-in-the-middle on first contact

keelbox connects with `StrictHostKeyChecking=yes` against its own `known_hosts`. It trusts a host key only when the config pins it, you confirm it, or keelbox generated it and passed it in the server's user data. The metadata service may serve user data to anything on the server, so that key is single-use: keelbox replaces it at the first connection with keys made on the server. On AWS the instance's keys before the launch script runs are refused, and on Google Cloud the guest agent is told not to replace the keys. Checked by unit tests; doctor has no check.

### The image's default account

Cloud and LXD images create a default account, usually `ubuntu`, with your SSH key and passwordless sudo. keelbox locks and expires the accounts in `/etc/sudoers.d/90-cloud-init-users`, other than `admin`, `agent` and root, once the admin has logged in with your key and used sudo. SSH and sudo then refuse them; nothing is deleted. `keelbox up` on the machine itself leaves alone the account it runs from. [The image's default account](guides/adopting-a-box.md#the-images-default-account) says how to keep or unlock it. Checked by doctor and the end-to-end test.

### Your sudo password

When the first login's sudo asks for a password, `keelbox up` runs over `ssh -t` and sudo reads it from your terminal; keelbox never sees it. Programs run as the agent start without the terminal, so they cannot read what you type. apply removes `/etc/sudoers.d/90-keelbox-bootstrap`, and doctor fails while it exists; a passwordless rule under another name is yours and stays. Checked by the end-to-end test, which searches the box for the password and runs an agent program that tries to open `/dev/tty`.

### Commands root runs as the agent

Root runs every command by absolute path with a fixed `PATH` and none of the caller's environment. Commands as the agent go through `runuser` and `env -i`, without a terminal, in the agent's slice, under a time limit that kills their whole process group. At shutdown, when systemd refuses a new scope, the note of the agent's conversations runs `tmux list-panes` and `tmux has-session` as the agent without one, and has the agent's tmux server run the session save, so the save runs in the agent's slice. Their programs are found in root-only directories, except the agent's own `claude` and `codex`: whatever the agent puts there runs with its rights, and its output counts as agent text. Checked by unit tests and the end-to-end test.

The watch run acts on the agent's sessions only through fixed `systemctl --user` verbs and programs run as the agent. It never stops a running tmux or Remote Control server; a tmux server the agent keeps killing is restarted 3 times an hour, then hourly, with one alert. After any orderly reboot it types `claude --resume ID` or `codex resume --last` into each restored pane from notes the agent controls, so the agent decides what is typed, as itself. A process that says it is busy postpones the reboot for as long as it looks busy; the weekly `updates-pending` event says one waits, and `keelbox reboot BOX` runs it.

### Agent text on your terminal

Everything the agent can influence, from session names to notification text, passes one sanitiser before keelbox prints, logs or sends it. Control characters and bidirectional overrides become `\xNN` or `\uXXXX`, so the agent cannot clear your screen, set its title or write to your clipboard through keelbox. Checked by unit tests and the end-to-end test.

### Faked or hidden power events

The clean-shutdown marker and power state are in `/var/lib/keelbox/power/`, which only root can write, so the agent can neither hide nor invent an unclean shutdown. Doctor does not check it; the end-to-end test checks that a hard reset is recorded and a clean restart is not.

### The agent starving the box's other services

Every agent process, rootless Docker included, runs in one systemd slice with a memory ceiling (90% of RAM unless `users.agent.limits.memory` says otherwise), lower CPU and IO weight than system services, and a CPU quota with `users.agent.limits.cpu`, in root-owned drop-ins. At the ceiling the kernel stops one agent process and the watch run raises `out-of-memory`. apply denies the agent cron and at, which would run jobs outside the slice. Checked by doctor (`agent-limits`), tests against a simulated box and the end-to-end test.

The agent can raise the `MemoryHigh` slow-down mark below a configured ceiling, but not the ceiling, quota or weights. With `limits: {memory: none}` and no other limit, the agent can use the whole machine. IO weight needs the `bfq` scheduler. The limits do not cover system Docker's containers, other accounts or package installs.

### Tampered downloads

Docker, Tailscale and GitHub CLI come from apt repositories whose signing keys are pinned by fingerprint in the release manifest. mise, Caddy and restic are checked against checksums in the manifest, taken from Caddy's Sigstore-signed checksums, restic's GPG-signed `SHA256SUMS` and mise's `SHASUMS256.txt`, whose signature was not checked. Claude Code and Codex come from their vendors' installers over HTTPS, checked only against checksums from the same vendor host; keelbox does not pin them.

keelbox's own binaries are used only when `SHA256SUMS` carries the release key's signature, the binary matches it, and the channel index lists the same checksum. They come only from keelbox's GitHub release hosts, whatever the index says. `KEELBOX_INDEX_URL` can name an HTTPS mirror of the index, but not another release host or key. Checked by unit tests and the installer's test. [Installing and updating keelbox](reference/release.md#installing-and-updating-keelbox) lists each check.

### Silent downgrades

`keelbox apply` and `keelbox self-update` refuse an older release unless you pass `--allow-downgrade`. Removing an existing `docker.io` needs `--adopt`. On a machine in use, the first apply stops before any change if it would cut off a listening service, a Docker-published port or a password SSH login, or leave software stopped; `keelbox plan --impact` lists these, and the config or `--adopt` resolves them. Checked by the end-to-end test and unit tests.

## Compromised download hosts

get.keelbox.dev, on Vercel, serves the installer and the channel index. GitHub releases serve the binaries and `SHA256SUMS`. The release key lives only in the release workflow's secrets.

| Compromised | Can | Cannot |
|---|---|---|
| get.keelbox.dev alone | Serve a different `install.sh`, which controls any new `curl \| sh` install. Stop updates, or point the channel at an older release, which a new install takes and self-update refuses. | Make an installed keelbox accept a binary, or send downloads to another host. |
| The release host alone | Stop downloads. | Get a changed binary or `SHA256SUMS` installed: the signature and index checksum both fail. |
| Both hosts | Both of the above. | Make an installed keelbox accept a binary the release key did not sign. |
| The release key and both hosts | Ship a binary every client accepts. | Nothing: that is the limit. |

To install without trusting get.keelbox.dev, [check the download by hand](reference/release.md#verifying-a-download-by-hand).

## What keelbox does not defend against

The network rule's gaps are listed [with the rule](#the-agents-network-rule).

### The agent's reach

- **Leaks.** Outbound traffic is open: the agent, or code it runs, can send anything it can read anywhere. There is no egress allowlist.
- **Prompt injection.** keelbox does not inspect what agents read or do. A repository, issue or web page can instruct an agent to misuse its tokens, GitHub access, the tailnet and, with `auto`, package requests.
- **Kernel bugs.** A kernel bug that gets past Linux users, user namespaces or netfilter gets past keelbox. Rootless Docker relies on unprivileged user namespaces, which have had such bugs.
- **bwrap.** On Ubuntu 24.04 keelbox loads Ubuntu's `bwrap-userns-restrict` AppArmor profile so the Codex and Claude Code sandboxes work. Programs bwrap runs get no capabilities, and `kernel.apparmor_restrict_unprivileged_userns` stays 1. But the agent can run bwrap itself, which widens its reach into the kernel's namespace, mount and network setup to what 26.04 allows every account. Claude Code's socket filter needs `sandbox.network.allowAllUnixSockets`, which keelbox leaves to you; it lets commands in the sandbox reach the agent's Unix sockets, its rootless Docker socket included. Doctor and the end-to-end test run bwrap as the agent.
- **The agent CLIs.** The agent can replace them, and a compromised vendor release reaches a box as it reaches a laptop.
- **Unsaved work.** `keelbox status`, doctor and `keelbox destroy` check the agent's projects with git, as the agent, to catch accidents. An agent can make a project look pushed, and files outside the projects, ignored files and volumes are not checked.
- **Its own setup.** An agent can break files in its home so apply fails there. apply skips that account's remaining steps, still applies the rest of the box, security steps included, and exits 1. Doctor fails until the home is fixed.
- **Reports.** A report the agent writes stays in root's inbox until you run `keelbox reports BOX --send N`, which redacts common secret shapes and shows you the text first. Redaction misses other secrets: your review is the control. The agent can fill the inbox, 5 drafts a day and 30 in all.
- **ntfy.sh's limit.** The agent shares the box's IP address and can use up its ntfy.sh allowance, delaying keelbox's messages. Add a second channel for urgent events, or use an ntfy token.

### What an installed package brings

With `auto`, the default, the agent has any package installed that passes the [checks on a request](#agent-to-root-through-package-requests), without asking you. The checks decide what a request may be, not that what it installs is harmless:

- each package adds programs, libraries and services, and a listening service is open to the tailnet like any port;
- a setuid program or root service with a privilege-escalation bug is a new route to root;
- maintainer scripts run as root, from the box's apt sources, including any source you added;
- a package not on the deny-list can still put another program behind a common command, a service on a port, or a new default;
- apt may upgrade a dependency, never a deny-listed one;
- an install runs as root, outside `users.agent.limits`, and can fill the disk.

Unattended security updates patch a package once Ubuntu ships a fix, and nothing before that or from other sources; `universe` packages get fixes from the community or Ubuntu Pro. `manual` limits installs to the ones you approve, and the agent's stated reason you see then is its own text, not a fact about the package.

### `users.agent.permissions: full`

With `full`, or `users.agent.sudo: true`, the agent has sudo, and none of the agent limits bind, their checks included. Doctor warns while a box is in this mode.

### Your keys, sign-ins and accounts

- **The admin key or your computer.** Whoever holds the admin key holds the box.
- **The Hetzner token** is in the macOS Keychain or a Secret Service, else a mode 600 file (or hcloud's own, with `target.hetzner.hcloud_context`). Any program running as you can usually read it. It reaches every server in the project and never expires, so use a project only for keelbox. `keelbox forget-token` removes keelbox's copy; revoke it in the Hetzner Console.
- **Cloud sign-ins.** keelbox runs your AWS, DigitalOcean or Google Cloud CLI, which can do anything its sign-in allows. keelbox changes only resources labelled or tagged for the box (on Google Cloud, also matched by name and description), but the cloud enforces that only if you narrow the sign-in: [cloud sign-ins and permissions](guides/credentials.md). keelbox never reads the CLIs' credential files.
- **Whoever administers the cloud account** can snapshot the disk, change the firewall, use the console and, on AWS, replace the instance. Lightsail's key pair and browser SSH log in as `ubuntu`, which keelbox locks. DigitalOcean's console works with a root password the cloud may set and email; SSH refuses passwords and root. On Google Cloud, metadata can add sudo accounts or a root `startup-script`. keelbox sets `block-project-ssh-keys=true`, `enable-oslogin=false`, no SSH keys in the VM's metadata and no service account, and doctor checks them from your computer.
- **The cloud-config snippet** from `keelbox up` holds your public key and, unless left out, the server's first host key. Whoever reads it from the provider before keelbox's first connection could impersonate the server at that connection.
- **The tailnet.** Your Tailscale ACLs decide what reaches the box. Previews have no sign-in, so any device that reaches the box can open them. The preview certificate puts the box's MagicDNS name, with the tailnet's, in Certificate Transparency logs.
- **Remote Control.** With `sessions.remote_control.enabled`, anyone signed in to your claude.ai account can run commands as the agent from anywhere, and session content passes through Anthropic. At `keelbox login`, keelbox reads only two yes/no flags from the agent's `~/.claude.json`, by exit code, and answers neither of Claude Code's first-run questions.

### A parked box's snapshot

`keelbox park`, and an AWS `keelbox resize` for its few minutes, keep the whole disk as a snapshot in the cloud account: the agent's sign-ins, your keys and tokens, the host keys, the Tailscale identity, and the backup and notification secrets. keelbox does not encrypt it, and anyone who can read snapshots in the account can make a server from it. It stays until `keelbox resume` has applied the box or `keelbox destroy` deletes it. Parking revokes nothing.

keelbox deletes the server only once the snapshot is complete, and touches only servers and snapshots labelled for the box, snapshots only with the `keelbox-parked` marker. A resume adds no usable login: a key the cloud requires goes to root, which sshd refuses, or on AWS to the locked `ubuntu`. The resumed server keeps its host keys, hostname and accounts, and gets the box's firewall before `resume` goes on. On AWS that relies on `/etc/cloud/cloud.cfg.d/99-keelbox.cfg` on the disk, and `resize` and `park` stop before any change without it. Checked by tests against a fake of each cloud and the end-to-end test on each cloud.

### The cloud's firewall on a new server

Neither DigitalOcean nor AWS takes a firewall when a server is created. DigitalOcean applies one seconds after the Droplet is active; at `keelbox resume` keelbox adds the Droplet by ID and waits until it is applied. A new Lightsail instance has ports 22 and 80 open until Lightsail accepts the box's rules, at `keelbox create`, `resume` and `resize`; keelbox sends them straight away, but how long that takes is not measured. Until then, UFW and sshd on the disk stand in front of the box.

### Backups

Whoever holds the bucket, or its key, can delete, withhold or roll back snapshots and see their sizes and times; restic cannot tell a missing snapshot from one never made. Without the repository password they cannot read or change the contents. Object lock or versioning at the provider protects against deletion.

### After root compromise

A root attacker can change any file, rule or log, the logbook included, and make doctor report anything. Doctor reports configuration drift; it is not intrusion detection. Root holds the backup credentials and password, so it can read and delete every backup.

### Losing the tailnet with public SSH closed

If Tailscale stops working, the box is reachable only from the provider's console or rescue system, where there is one; nothing reopens SSH. [Getting back in](guides/public-ssh.md#getting-back-in) has the steps. On AWS the only route is a new instance from a snapshot.

### A bug in Caddy

Caddy runs as `keelbox-preview` without capabilities, with a read-only filesystem and homes hidden. A compromise reaches the TLS key, the agent's loopback ports and the tailnet.

### What every account on the box can read

- `/var/lib/keelbox/status.json`: versions, times, unit states, the agent's tmux session names and the names and git state of its projects. No URL, nothing from a credential file, and nothing the agent cannot already see.
- `/var/lib/keelbox/installs/`: the permissions mode, the agent's requests and reasons, what was installed, and root's reason for a refusal, with apt's error lines for a failed install.
- The status line and welcome in the agent's tmux, drawn from files the agent can change. "An account without sudo" there repeats the config; it is not a check.

Run logs in `/var/lib/keelbox/runs/` hold no secrets; root and the `sudo` group can read them, the agent cannot.

### Secrets in transit

A command from your computer uploads secrets (the Tailscale auth key, backup and channel secrets) over scp into a mode 0700 directory under `/tmp` owned by the login account, normally the admin. Root moves them to `/run/keelbox`, a root-only tmpfs, and removes the directory; if the upload fails, it is left behind. Where `/tmp` is on disk, as on Ubuntu 24.04, the secrets touch the disk. The agent cannot read the directory.

### What keelbox's hosts learn

keelbox collects no usage data, and a box makes no unattended request to keelbox's hosts. Your computer reads `https://get.keelbox.dev/stable.json` at `keelbox self-update`, when it downloads a box binary, and at most once a day to check for updates, after a command you type at a terminal. `KEELBOX_NO_UPDATE_CHECK=1` or `DO_NOT_TRACK=1` turns the check off.

Each request sends a user agent such as `keelbox/0.1.0 (darwin/arm64)`, and nothing about you or your boxes. Vercel and GitHub see the address and time; the maintainers count requests by version and platform and use the logs for nothing else. The index is not signed: a forged security notice could show false text, but not make keelbox run anything, and the check never installs a release.

### What channels learn

Every notification tells the channel's operator the box name, a project's directory name and when your agents work. A self-hosted ntfy or your own webhook keeps that with you.

## Your responsibilities

| What | keelbox does | You do |
|---|---|---|
| Patching | Enables unattended security updates. With `host.auto_reboot` nightly (the default) or weekly, reboots an idle box at night for an update. | Leave both on. With `never`, run `keelbox reboot` when status says so. Apply new keelbox releases. |
| What the agent may change | `auto` is the default. `keelbox status` lists what was installed. keelbox never removes a package. | Use `manual` on a machine that also runs your own services or data. Keep `full` for a box you would hand to the agent whole. Remove a package with `sudo apt-get remove NAME`. |
| Parked boxes | `keelbox destroy` deletes the snapshot. | Guard access to the cloud account. Revoke the agent's sign-ins when you park or destroy. |
| SSH keys | sshd forwards no SSH agent to the agent. | Give the agent its own key. Keep the admin key off the box, behind a passphrase or hardware key. Rotate a key you suspect. |
| What agents can reach | The agent reads whatever you give it. | Use fine-grained GitHub tokens or deploy keys and API keys with spend limits. Set Tailscale ACLs for the box's tag. |
| Review | keelbox does not read what agents write. | Read what agents commit before it ships. |
| Backups | restic encrypts them; `keelbox restore` reads one back. | Keep a copy of the repository password off the box. Use a bucket key for that bucket only. Test a restore into a new directory. |

To end a cloud sign-in:

| Cloud | Ending the sign-in |
|---|---|
| Hetzner | Delete the token in the Hetzner Console. `keelbox forget-token` removes only keelbox's copy. |
| AWS | Sign out with the AWS CLI. `aws login` and IAM Identity Center sign-ins expire; access keys do not. |
| DigitalOcean | `doctl auth remove --context NAME`, and delete the token in the control panel. |
| Google Cloud | `gcloud auth revoke`. |
