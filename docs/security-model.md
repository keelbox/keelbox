# Security model: what keelbox enforces and what it does not stop

```sh
keelbox doctor acme
```

keelbox limits what code run by an agent can do to a box, the Ubuntu machine it sets up. `keelbox doctor` reads the running box and reports each limit it checks that is not in force.

keelbox treats code an agent runs as possibly hostile to the box. It does not treat it as hostile to the agent's own files: the agent reads its code and tokens to work. Anything the agent can read can leave the box.

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

apply sets a box up from its config, and the logbook records what changed.

| Control | Enforced by | Shown by |
|---|---|---|
| Root in an agent container is not root on the box | the agent's own rootless Docker daemon; the system daemon's socket is closed to the agent | doctor: `docker-rootless`, `docker-system-socket` |
| The agent has no sudo | no sudoers rule, no membership of `sudo` or `docker` | doctor: the agent's sudoers file, or `sudo` membership |
| Connections to the box's SSH port are refused to every account but root. So are link-local and cloud metadata addresses, bar four service accounts and those you name | an nftables table that matches the user ID owning a socket | doctor: `agent-net` |
| SSH takes keys only, refuses root and forwards no SSH agent to the agent | sshd; tmux also drops `SSH_AUTH_SOCK` | doctor, from `sshd -T`: `ssh-password-auth`, `ssh-root-login`, `ssh-agent-forwarding` |
| Inbound traffic is denied, except SSH, the tailnet and the ports you allow | UFW, a guard on system Docker's forwarding, and the cloud's firewall in front | doctor: `firewall-active`, `docker-inbound-guard` |
| SSH from the internet closes once keelbox has logged in over the tailnet | UFW, sshd and the cloud's firewall | doctor: `public-ssh`, `public-ssh-firewall`, `public-ssh-sshd` |
| Previews are served on the tailnet only | Caddy's listening addresses, its unit's IP policy, UFW | doctor: `binding`, `caddy-sandbox`, `firewall` |
| Root reads the agent's files as the agent | a child process with the agent's UID, in apply, previews, events, package requests, backups and the watch run's reboot note | the end-to-end test, package requests excepted |
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

These and more: [what keelbox does not defend against](#what-keelbox-does-not-defend-against).

## Why the image's default account is locked

Cloud and LXD images create a default account, usually `ubuntu`, with your SSH key and passwordless sudo. Beside `admin`, it is a second way to root that keelbox does not manage. keelbox locks it once the admin has logged in with your key and used sudo, so a wrong key does not lock you out. [The image's default account](guides/adopting-a-box.md#the-images-default-account) says how to keep or unlock it.

## In detail

Each control says how it is checked. The checks are `keelbox doctor` on the running box, the end-to-end test on a real VM, unit tests, and tests against a simulated box. Where a check of one kind is missing, the control says so.

### The agent's network rule

An nftables table on the box refuses some connections to every account but root, the agent among them. The two items under "What it refuses" are controls, each with how it is checked. The five under "What it does not cover" are not stopped.

What it refuses:

#### Agent to admin through SSH

sshd disables agent forwarding for the agent user. tmux drops `SSH_AUTH_SOCK` from sessions.

An nftables table refuses connections to port 22 on any address of the box (`fib daddr type local`) from every account but root. It refuses the same to each outside address on record. An outside address is an address and port at which keelbox reaches the box's sshd and which no interface of the box carries. One is the public address of a server on AWS or Google Cloud, which is translated before it arrives. Another is a router's forwarded port. Without the second rule a connection to such an address leaves the box and comes back to sshd.

The box cannot see these addresses itself. Every keelbox command over SSH therefore tells it two addresses, resolved on the laptop: the one its ssh connects to, and the box's address outside the tailnet. The second is `target.ssh.host`, and for a cloud box the public address its provider reports. apply keeps those that are not the box's own in `/etc/keelbox/outside-addresses`, the 16 added last. The table has one reject rule for each. An apply over the tailnet or on the box itself removes none.

The table matches the uid that owns the socket, and these rules refuse every uid but 0. That covers:

- the agent;
- its rootless containers, whose traffic leaves through processes the agent owns;
- a process of the agent's that takes a subordinate uid in a user namespace (through `newuidmap`, as `rootlesskit --net=host` does) while staying on the box's own network;
- a service under an account of its own, such as one a package installed for the agent brings, which could otherwise make the connection for the agent;
- the admin account: `ssh localhost` as the admin needs `sudo`.

Without these limits, an agent whose key is also the admin's, or that inherited a forwarded agent socket, could run `ssh admin@localhost` and get sudo.

How it is checked:

- Doctor checks that the loaded table refuses port 22 at the box's own addresses, and at every address on record, to every uid but root's.
- Unit tests run the rendered rules for the agent's uid, a subordinate uid, another login account, a service account and root. They also run them for a packet the kernel sends for no process, which has no owner and passes.
- The end-to-end test connects as the agent and from a container of the agent's, to loopback and to the address the test reaches the box at. It also connects from a subordinate uid of the agent's (`rootlesskit --net=host`, then `setpriv --reuid=1`) to loopback and to the metadata address.
- As another account (`nobody`), the end-to-end test expects no connection to loopback or to that address, and as root a connection to loopback.
- Doctor and the end-to-end test read the forwarding setting from sshd's effective config for the agent.
- That tmux drops `SSH_AUTH_SOCK` is checked only in the text of the generated tmux config, by a unit test.

What this does not cover starts at [an address of the box keelbox was never told](#an-address-of-the-box-keelbox-was-never-told).

#### Agent to cloud metadata

The same table refuses connections to link-local addresses (`169.254.0.0/16`, `fe80::/10`) and to `fd00:ec2::254`, so to `169.254.169.254` among them, from every account but root. These services hand instance data to any local process that asks, and that data can include cloud-init user data. Root keeps them: cloud-init and the clouds' guest agents run as root. The rules for these addresses are, in order:

1. The agent's uid and every subordinate uid `/etc/subuid` gives the agent, read at each apply, are refused, with no exception.
2. Four of Ubuntu's service accounts pass, each for the one kind of traffic it needs from a link-local service:
   - `systemd-resolve` to UDP and TCP port 53: the resolver on Google Cloud is `169.254.169.254`.
   - `systemd-timesync` and `_chrony` to UDP port 123: the time service on AWS is `169.254.169.123`.
   - `systemd-network` to UDP ports 67 and 547 and with ICMPv6. That is DHCP, whose server on Google Cloud is the metadata address, DHCPv6, and IPv6 router and neighbour messages.

   These names are fixed in keelbox's code. apply writes each account's uid, and leaves out an account the box does not have and one whose uid is root's or one of the agent's.
3. Accounts named in `host.firewall.link_local_accounts` pass, on every port. The list is empty unless you fill it. It is for a machine where a service under its own account has to read a link-local address, such as a cloud's monitoring agent on a machine that ran it before keelbox. Each account named there can be used as a relay if the agent can drive it. When its service connects where a local caller says, the agent reaches the metadata service through it. The config is root's on the box and comes from your computer, so the agent cannot add a name. keelbox refuses `root` and the agent account in the list. apply writes no rule for a name that is not an account on the box, or whose uid is root's or one of the agent's, and doctor warns about each.
4. Every other uid but 0 is refused. A subordinate range added to `/etc/subuid` later, or one from another source (an NSS `subid` module), is refused here. Doctor fails until the next apply names an added range as the agent's.

A service under an account of its own, such as one a package installed for the agent brings, is therefore refused as the agent is, unless you named its account. So is the admin account: a metadata query as the admin needs `sudo`.

The rules look at each packet, not at who opened the connection. Two things follow:

- A service that is not root's cannot answer a client that reaches it from a link-local address.
- A box whose `/etc/resolv.conf` names only link-local resolvers, without systemd-resolved's stub, has name resolution for root only. Doctor fails on that file and gives both ways out: the stub, or the accounts that must resolve named in `host.firewall.link_local_accounts`, which never covers the agent. Before a first apply, `keelbox plan --impact` lists it as an item the apply stops for.

On a machine in use that report also states these limits. It names what is connected to a link-local address under another account at that moment. It is one look at the socket table, so a service that asks only now and then can be missed.

A packet the kernel sends for no process, such as neighbour discovery to a link-local IPv6 router, has no owner, and no rule here applies to it. That is how nftables' owner match reads in the kernel's source and how the unit tests model it. It has not been observed on a box.

How it is checked:

- Doctor checks that the loaded table sends both ranges and `fd00:ec2::254` to these rules. It checks that the uids refused first are exactly the agent's uid and its ranges in `/etc/subuid`. It checks that the exceptions are exactly those of the accounts the box has now and of the names in `host.firewall.link_local_accounts`, with no other accept in the table. It checks that every other uid but root's is refused.
- Unit tests run the rendered rules over IPv4 and IPv6. They cover the agent's uid, a subordinate uid, another login account, a service account, each of the four accounts on its port and off it, an account the config names, and root.
- Tests against a simulated box cover a named account that exists, one that does not, and one that appears later.
- The end-to-end test tries `169.254.169.254` from a subordinate uid of the agent's. On the local test VM nothing listens at that address, so there the test shows little.
- With the table loaded, the end-to-end test also checks:
  - as `nobody`, no connection to `169.254.169.254`;
  - name resolution as the agent and as root (`getent hosts`, and `resolvectl query` where systemd-resolved runs);
  - a time source that answers systemd-timesyncd after a restart, or that chrony is synchronised to;
  - `apt-get update` as root.
- On Hetzner, AWS, DigitalOcean and Google Cloud it also expects root to reach `169.254.169.254`. Those checks have run on the local test VM, not on the four clouds, so each cloud's resolver and time source are from the vendors' documentation.
- The doctor check of `/etc/resolv.conf` and the impact report's lines are covered by tests against a simulated box, not on a real one.

What it does not cover:

#### An address of the box keelbox was never told

The agent's SSH to the box is refused at the box's own addresses and at the outside addresses on record, and nowhere else. keelbox learns an outside address only from a laptop's keelbox command over SSH.

`keelbox up` on the machine itself has no laptop. There the machine's own addresses (LAN, tailnet, loopback) are covered. The WAN address of a router that forwards a port to it is not, nor is the public address of a cloud server whose address is translated.

The same holds for an address or name that reaches the box and is in no config, such as a second forwarded port or a load balancer. It also holds for a new public address until the next `keelbox apply` from a laptop that knows it. keelbox does not ask a cloud's metadata service or any outside service what the box's address is.

At such an address the agent reaches sshd as any machine on the internet that can reach it does. sshd takes keys only and forwards no agent to the agent account. Once public SSH is closed (not with `public_ssh: lan`), sshd refuses every login that does not arrive at a tailnet or loopback address, and UFW admits SSH only on `tailscale0`.

To cover one, add it to `/etc/keelbox/outside-addresses` as a line `ADDRESS:PORT` and run `keelbox apply`. Delete a line there when the box is no longer reached at it.

#### The agent's SSH to the box through another machine

The table refuses connections the agent makes to the box. It cannot refuse one the agent makes to a machine elsewhere that forwards it back to the box: that connection arrives at sshd as any outside client's does.

While public SSH is open, sshd answers it from anywhere. Once it is closed, sshd answers it only when the forwarding machine is on the tailnet, or with `public_ssh: lan` on the LAN.

What holds there is sshd, which takes keys only and forwards no agent to the agent account, so the agent gets in as the admin only with the admin's key. Give the agent a key of its own ([your responsibilities](#your-responsibilities)).

#### The agent and tailscaled's local API, on Tailscale older than 1.98

The agent's network table matches the uid that owns a socket. tailscaled runs as root and serves a local API on a Unix socket that every account on the box can call.

Before Tailscale 1.98 its dial endpoint, which `tailscale nc HOST PORT` uses, has no permission check and opens the connection itself, to any address. The socket is then root's and the table does not match it, so the agent can reach port 22 on the box and the metadata address that way.

From 1.98 the client dials addresses outside the tailnet itself, so the socket is the agent's and the table applies. Addresses on the tailnet are still dialled by tailscaled.

This is read from Tailscale's source (`ipn/localapi/localapi.go` at v1.96.4 and v1.98.0) and from keelbox's rules. keelbox has not tried it on a box, on either side of 1.98.

keelbox installs Tailscale's current package on a machine that has none. It does not upgrade a Tailscale that is already there, and does not check its version. It leaves unattended upgrades at Ubuntu's own origins, which do not include Tailscale's repository. A box's Tailscale therefore stays at the version it was installed with until you upgrade it.

`tailscale version` shows what runs. `sudo apt-get update && sudo apt-get install --only-upgrade tailscale` moves it to the current one.

More generally, the table refuses sockets of every account but root. A service whose sockets are root's, and that connects for the agent, is outside it.

#### The agent's SSH to the box in the first minute after a resume, on AWS and Google Cloud

There the public address is translated before it reaches the machine. The rule that refuses the agent's connections to the box's own SSH port therefore lists it from `/etc/keelbox/outside-addresses` (the item on an address keelbox was never told, above).

A resumed box has a new public address. From its boot until the apply at the end of `keelbox resume`, about a minute, the file still lists the old address. The agent's lingering services and containers with a restart policy start at boot.

In that time a process of the agent's reaches sshd at the new public address as any machine on the internet that can reach it does. sshd asks for the admin's key, which the agent does not have, and forwards no agent to the agent account. The apply records the new address.

An AWS `resize` keeps the static IP, so it has no such window.

#### A service that an installed package starts

A service an installed package starts runs under its own account. The table that refuses connections to the box's SSH port and to link-local addresses applies to every account but root. A forwarding proxy, a cache that fetches, or anything else that connects for whoever asks from loopback is therefore refused there as the agent is. Such a service still reaches the internet and the tailnet, as any process on the box does, the agent included.

What the table does not cover is a service whose sockets are root's. One that runs as root and connects where a local caller tells it reaches the box's SSH port and a cloud metadata address for the agent.

The deny-list names the proxies and relays known to start with a configuration that takes requests from loopback, as a second line. It cannot name every package that relays traffic. keelbox has not tried a relay on a box: this follows from the table's rules and the packages' default configuration.

## What keelbox defends against

Two more controls, the agent's SSH to the box and its reach to cloud metadata, are under [the agent's network rule](#the-agents-network-rule).

### Agent to root through Docker

The agent runs its own rootless Docker daemon. Root in an agent container maps onto an unprivileged subordinate UID on the box. The system daemon's socket, which is root-equivalent, is closed to the agent.

Doctor checks that the agent's own daemon answers and that the agent cannot use the system daemon. As the agent, the end-to-end test checks that its daemon reports rootless and that the system socket answers permission denied. It also checks that the agent is not in the `docker` group.

### Agent to root through apply

`keelbox apply` runs as root. It reads and writes files in a user's home as that user, through a child process with the user's UID and groups. A symlink the agent plants in its home gets apply no further than the agent could go itself. Root's own file operations refuse any path whose directories a non-root account owns or can write.

Checked by tests against a simulated box and by the end-to-end test. That test points `~/.config` at `/etc` and `~/.bashrc` at `/etc/shadow`, and confirms that apply refuses there with `/etc` unchanged.

### Previews on a public address

Caddy listens only on the box's tailnet addresses, in `100.64.0.0/10` and `fd7a:115c:a1e0::/48`. The config renderer refuses any other address. Caddy's unit allows IP traffic only to loopback and those ranges, with `IPAddressDeny=any`, so a wrong config still could not serve elsewhere. UFW admits the preview ports only on `tailscale0`.

With nothing to serve, or with the box off the tailnet, Caddy is stopped.

Doctor checks Caddy's listening sockets, the unit's IP policy and the UFW rules. The end-to-end test also checks that a preview is unreachable on the box's LAN address.

### Agent to root through previews

The agent publishes a preview without sudo, by editing a file in its own home. The root service reads that file as the agent, so a link to a root-only file gets a permission error. It keeps only entries with a validated name and port numbers in range, and generates Caddy's JSON from them. Caddy's admin API is off, so no local process can change its config.

Checked by tests against a simulated box and by the end-to-end test, which writes hostile entries and a link to `/etc/shadow`.

### Agent to root, or to other accounts' files, through backups

restic runs as root to reach the credentials, and never reads or writes a user's files. Each path is archived by tar running as its owner. It is restored by tar running as its owner, from the stream restic returns. A symlink or a race in the agent's home gets the agent nothing it could not already read or write.

The bucket's credentials and the repository password are root-only: mode `0600`, in the `0700` directory `/etc/keelbox/backup/`. They reach restic by path. They are never in arguments, environment values, output, the plan or the logbook.

Checked by tests against a simulated box and by the end-to-end test, which also searches the logbook, the applied config and the journal for them.

### Agent to root, or forged alerts, through events

The agent's CLIs report events by appending to a file in its home. The root service reads it as the agent, so a link to a root-only file gets a permission error. It takes only lines that name an agent event kind, with fields that pass fixed checks. It sets the source, the level and the box itself.

The agent therefore cannot raise one of keelbox's own alerts, choose how loud its events are, or add text beyond what the checks allow. The queue, `/var/lib/keelbox/notify/`, is root-only. A flood is capped per run and in the queue, separately from root's own events.

Writing to the file in a loop costs root little and does not keep the watch run from its lock. The service starts at most every 5 seconds and reads at most 2 MiB a run. When the file has not grown, it returns before taking the lock the unattended runs share.

Checked by unit tests and the end-to-end test, which writes forged lines, a link to `/etc/shadow` and a FIFO.

### Agent to root through package requests

With `users.agent.permissions: manual` or `auto` the agent has no sudo. It has `keelbox install NAME`, which writes one request to a file in its home.

A root service reads that file as the agent. It follows no link and reads no file over 16 KiB, and a FIFO does not hold the read. It takes nothing in the file on trust.

| Check | What it does |
|---|---|
| Names only | A request is at most 20 package names: lower-case letters, digits, `+`, `.` and `-`, none starting with `-`. A version, a release, an architecture, a path, a URL and an option all fail that pattern before apt sees them. The names reach apt after `--`. |
| Known packages | Each name must be exactly a package `apt-cache policy` knows, with an install candidate from the apt sources the box already has. A name apt would read as a pattern is refused. No source can be added, and no local `.deb` is taken. |
| A simulated run first | apt-get's own simulated run is read before anything is installed. A request that would remove any package is refused, as is one that would install or upgrade more than 300 packages. So is one that would install or upgrade a package on the deny-list, also as a dependency. |
| The install | `apt-get install -y --no-remove` with keelbox's apt options: apt fails if a package would be removed after all. A named package that is already installed is left as it is. No verb removes, purges, holds or upgrades a package. |
| Root's own IDs | Root names each request: the ID you approve is one root made, not one the agent chose. A later request cannot pass as one you approved. |
| Rate | One request is handled at a time. At most 30 are taken in an hour, and the rest are refused. At most 20 wait for approval. |
| Record | Every request and its outcome is a logbook record of kind `install`. It holds checked names and fixed words, never the agent's stated reason or what apt printed. Root keeps the list of what was installed for the agent, and `keelbox status` shows it. |

The deny-list is families of package names. It applies in every mode, and each family has its reason in `internal/installs/names.go`:

- remote login servers, firewall tools, and sudo and other tools that run commands as another account;
- Docker's engine and containerd, Tailscale, AppArmor and other security modules;
- apt, dpkg and unattended-upgrades, systemd and the system bus, PAM and NSS modules;
- cloud guest agents, network managers, kernels, boot loaders and kernel modules;
- schedulers other than cron and at, and proxies that take requests from the box itself.

With `auto` or `full` a request that passes is installed straight away. With `manual` the same service records the request and installs nothing until `keelbox approve`, which you run from your computer. An approved request passes the same checks again before it is installed. The agent cannot approve: on the box `keelbox approve` needs root.

The other limits on the agent are set up the same in both modes. It has no sudo rule and is in neither the `sudo` nor the `docker` group. [The agent's network rule](#the-agents-network-rule) is in force. keelbox's own files are writable by root only.

Doctor states the mode and fails when the service's path unit is not active.

Unit tests cover hostile names, a request file replaced by a link, a FIFO or a file over the limit, a removal, a deny-listed dependency, and an approved request the box no longer allows. Tests against a simulated box cover each mode and a change of mode.

The end-to-end check runs on a local test VM. As the agent, it installs a package and has a deny-listed name, a version and an option refused. It then looks for sudo and for the logbook record. In `manual` mode it records a request, then approves one and denies another from the laptop.

What a package brings once it is installed is under [what an installed package brings](#what-an-installed-package-brings-with-usersagentpermissions-auto).

### The agent changing the agent CLIs

Claude Code and Codex are the vendors' own installs in the agent's home, as on a laptop, and update themselves from their vendors. The agent can change them, replace them, or put another binary first on its PATH, as it can change their settings and hooks in `~/.claude` and `~/.codex`.

A setting keelbox needs the agent not to change, such as the notification hooks, goes in the managed settings under `/etc/claude-code` and `/etc/codex`, which are root's. The vendors' CLIs read them and rank them above the agent's own settings. A binary the agent replaced need not read them, so they bind the vendors' CLIs, not the agent, and nothing checks which binary runs.

A compromised vendor release reaches a box as it reaches a laptop. keelbox does not test or hold back the versions.

### The agent and the notification channels

The hooks that report the agent's events are managed settings: root files that the vendors' CLIs rank above the agent's own, so the agent's settings cannot turn them off. A CLI the agent replaced can skip them. They run `keelbox event` as the agent, which gains nothing it did not have.

Root budgets what the agent's file can raise before it is queued. Root's own events are outside that budget.

| Per | The agent's events |
|---|---|
| session and kind | one every two minutes |
| hour | 30 |
| day | 100, then one notice an hour |

Messages use fixed templates. An agent event is always titled with the agent CLI, never `keelbox`. It carries the agent's own text only with `notify.agent_text`, up to 200 characters. That text is quoted after `claude-code wrote:` or `codex wrote:`, so it does not read as keelbox's.

Channel secrets are an ntfy topic or token, a Pushover token and user key, a Telegram bot token, and a webhook URL or header. Whoever holds one can post to your phone or chat. They are root-only copies in `/etc/keelbox/notify/`, mode `0600` in a `0700` directory, read by the root senders. They are never in arguments, environment values, output, the plan, the logbook or an error. An error carries the channel and an HTTP status or a fixed network phrase, never a URL, header or response body. Plain `http://` is allowed only to loopback and the tailnet.

For an address on the box itself, keelbox finds the socket that accepted its connection in `/proc/net/tcp` and `tcp6`. It sends nothing unless root, a system account or the admin owns that socket. An agent that binds a self-hosted ntfy's port while ntfy is down gets a connection and no byte of the request. An address keelbox does not find there is sent to as configured: another machine, or a port root forwards elsewhere.

Unit tests cover a Telegram token in the URL path, a webhook URL and a refused request. The end-to-end test fires a real managed hook and forges lines. It then searches the box's state, the policy files, the journal and every process command line for the secrets.

### Unintended public ports

UFW denies inbound traffic except SSH, the tailnet and the ports `host.firewall.allow` names. It admits those ports only from the private address ranges or from a network the entry names, never from `0.0.0.0/0`.

A firewall at the cloud stands in front of a cloud box and admits only SSH and Tailscale's UDP port 41641. A port opened on the box by mistake, or UFW turned off, still does not reach it from the internet.

| Cloud | The firewall in front |
|---|---|
| Hetzner, DigitalOcean | a Cloud Firewall |
| AWS | the Lightsail instance firewall |
| Google Cloud | two firewall rules on the box's network tag |

On DigitalOcean and AWS that firewall is in place some seconds after a new server starts, not from its first second. [The cloud's firewall on a new server](#the-clouds-firewall-on-a-new-server-on-digitalocean-and-aws) says what applies until then.

On Google Cloud this holds for the VPC rules keelbox writes. A rule someone adds for the box's tag, or for every VM, with a priority number below 901 can admit more. So can a firewall policy of the organisation or the network. `keelbox doctor` warns about such rules but not about policies.

Rootless published ports are ordinary sockets, so UFW filters them. The system daemon's port forwarding bypasses UFW. For it, a `DOCKER-USER` guard drops new forwarded connections unless they arrive on `tailscale0` or a Docker bridge. The guard also admits a connection from an allowed network to a published port that `host.firewall.allow` names.

An allowed port is reachable by every device on that network: keelbox does not check who is on your LAN. A range such as `port: 8000-8010`, or a service preset such as `service: unifi`, opens more than one port with one entry. Neither form can include SSH. [Known services](reference/services.md) lists the presets and their ports.

Doctor checks that UFW is active with incoming connections denied by default, and that `DOCKER-USER` jumps to keelbox's guard. Run from your computer, it also checks that the cloud firewall exists, is attached and admits SSH only while public SSH is open. The end-to-end test publishes a port from a rootless container and from a system container. It checks that each answers on the box and not from outside it.

### Agent to root through the watchdog

The watch run acts on the agent's sessions with fixed `systemctl --user` verbs on keelbox's two units, through the agent's own user manager. It runs `tmux`, tmux-resurrect's save script, `claude auth status`, and `find` and `grep` over Claude Code's process notes in `~/.claude/sessions` as the agent, never as root. The agent can change those scripts and files, and gains only what it could run itself.

Before a reboot for an update, root lists the agent's processes with `ps` and reads Claude Code's note on each, as the agent, to decide what to resume. After the boot it types a quoted `claude --resume ID` or `codex resume --last` line into each restored pane, with `tmux send-keys` as the agent. The agent decides what those inputs say, so it decides what is typed, and the line runs as the agent. A process that says it is busy postpones the reboot, so the agent can hold off an automatic reboot for as long as it looks busy; the weekly `updates-pending` says a reboot still waits, and `keelbox reboot BOX` runs one. Unit tests cover the note and the typed line; the end-to-end test reboots a box and checks that the conversation comes back.

The watch run never stops or restarts a running tmux or Remote Control server. An agent that kills its own tmux server repeatedly costs 3 restarts an hour, then one an hour, and one alert.

Unit tests cover the restart, the budget and a running server left alone. The end-to-end test kills the agent's tmux server. It checks that the server comes back, that the restart is in the logbook, and that the next run restarts nothing.

### Agent to root through commands root runs as the agent

Every command root runs starts by absolute path with a fixed `PATH` of root's directories. That covers apply, doctor and the unattended runs. The caller's environment is not passed on, and a command whose own environment sets `PATH` is refused.

A command root runs as the agent goes through `runuser` and `env -i`, and gets a fixed `PATH` too. keelbox resolves its program before the switch, in directories that root owns and no one else can write, with one exception: `claude` and `codex` are the agent's own installs, so keelbox finds them on the agent's PATH and starts them as the agent: for doctor, the watchdog, Remote Control and `keelbox status`. Whatever the agent put there runs with the agent's rights and a time limit, and its output counts as agent text. A `tmux` in `~/.local/bin` is never what root's runs start. The agent's own programs, such as tmux-resurrect's save or a mise shim, still run as the agent, which can change them.

Each such command runs in its own session with no controlling terminal, in the agent's `user-UID.slice`, so under `users.agent.limits`. It has a time limit. The whole process group is killed when it ends or times out, so a child that keeps the output open cannot hold up the run.

Files root reads from the agent's home are read as the agent and opened without blocking. A final link is not followed where that is the rule. Anything but a regular file is refused, so a FIFO or a device in place of the events file stops nothing.

Unit tests cover a `true` shim on the caller's PATH, a group-writable directory, a FIFO and a child holding the output. The end-to-end test plants a `tmux` shim and a save script that leaves a child behind.

### Agent text on the owner's terminal

Whatever the agent can influence passes through one sanitiser before keelbox prints, logs or sends it. That covers session names, CLI versions, sign-in output, doctor details, status rows, notification text and the status snapshot. Control and other non-printing characters, bidirectional overrides included, become `\xNN` or `\uXXXX`. The agent therefore cannot clear the owner's screen, set its title, or write to its clipboard through keelbox's output.

The snapshot is cleaned when it is written and again when it is read. Checked by unit tests with escape sequences in tmux output, and by the end-to-end test.

### Faked or hidden power events

The clean-shutdown marker and the power state are in `/var/lib/keelbox/power/`, a root directory the agent cannot write. The agent can neither hide an unclean shutdown nor invent one. `keelbox apply` creates the directory as root's, with mode 0755.

Doctor does not check it, and no test tries to write there as the agent. The end-to-end test checks that a clean restart records nothing and that a hard reset is recorded.

### The agent starving the box's other services

Every process of the agent account runs in one systemd slice: its SSH sessions, tmux, Remote Control, and rootless Docker with its containers. The slice has a memory ceiling, 90% of RAM unless `users.agent.limits.memory` sets another, and a lower CPU and IO weight than system services. With `users.agent.limits.cpu` it has a CPU quota too. They are set in root-owned drop-ins the agent cannot change. At the ceiling the kernel stops a process of the agent's, not of another service.

A ceiling the config sets has a slow-down mark below it, `MemoryHigh`, on the agent's user manager's slices. The agent could raise it with a drop-in of its own in `~/.config/systemd/user`. It cannot raise the ceiling, the CPU quota or the weights.

cron and at would run the agent's jobs from their own services, outside that slice. `keelbox apply` therefore names the agent in `/etc/cron.deny` and `/etc/at.deny`, and removes any crontab or at jobs it has. Doctor fails while either is there.

The kernel stops that one process: keelbox sets the user managers' `DefaultOOMPolicy=continue`, so systemd does not end the rest of its tmux pane with it. The watch run then raises `out-of-memory`.

With `limits: {memory: none}` and no other limit, the agent can use the whole machine. IO weight has no effect on disks that do not use the `bfq` scheduler. The limits do not cover the system Docker's containers or other accounts.

Doctor reads the limits in force from the slice's cgroup and fails when they differ from the config. Checked by tests against a simulated box and by the end-to-end test, which runs CPU and memory stress as the agent beside a root workload.

### SSH password guessing and root login

sshd accepts public keys only, and refuses root. Doctor reads sshd's effective config with `sshd -T`, not keelbox's file, so another drop-in that overrides it is caught. The end-to-end test tries a password login and a root login and expects both refused.

### SSH exposed to the internet

keelbox closes public SSH once it has logged in to the box over the tailnet, as the admin, from the machine running it. The box checks that from the session's own addresses, and does not take your computer's word for it. keelbox then removes the public `22/tcp` UFW rule and the SSH rule of the cloud's firewall. sshd refuses logins that arrive at any non-tailnet, non-loopback address.

Without that proof SSH stays open, so a broken tailnet does not lock the admin out. `host.firewall.public_ssh: open` keeps it open.

With `public_ssh: lan`, which `keelbox up` offers for a box on a LAN, UFW keeps 22/tcp from the private address ranges. sshd keeps taking logins from them. Anything that can reach the box from a private address can reach sshd, keys only.

Doctor checks the recorded state, and that UFW no longer admits 22/tcp from anywhere. It checks that sshd's effective config refuses a login arriving at a public address and takes one at the tailnet address. Run from your computer, it also checks the cloud firewall's SSH rule.

The end-to-end test closes SSH on a local VM with a stand-in for the tailnet: an interface named `tailscale0` with a forwarded address. It does the same on a live Hetzner account, with a tunnel standing in. On AWS, DigitalOcean and Google Cloud, closing the firewall's SSH rule is tested against a fake of each cloud's CLI, not on a live account.

### The image's default account

The cloud image's default account, `ubuntu`, keeps the provider's key and passwordless sudo. keelbox locks and expires it after confirming that the admin can log in and use sudo.

The accounts locked are those named in `/etc/sudoers.d/90-cloud-init-users`, except `admin`, `agent` and root. A machine cloud-init never set up has no such file, so an account you made by hand is left alone. The lock is `usermod --lock --expiredate 1`: PAM's account check then refuses SSH with any key, and sudo. Nothing is deleted.

`keelbox up` on the machine itself leaves the account unlocked, with `users.lock_cloud_user: false`, when it is the account `up` runs from. There it is the owner's own login, not an image default.

Doctor lists an unlocked default account as drift, since the lock is a step apply would run. The end-to-end test logs in as that account and expects to be refused.

### The owner's password and a passwordless bootstrap rule

The account keelbox first logs in as may have sudo that asks for a password. `keelbox up` then runs the read-only look and the start of the first apply over `ssh -t`. sudo reads the password from the terminal itself. keelbox never sees it, and it is in no argument, environment, file, log or output of keelbox's; the end-to-end test searches the box for it.

The apply itself then runs as a systemd unit with no terminal. Programs keelbox runs as the agent start in a session of their own, without the terminal, so they cannot read what is typed there. The end-to-end test checks this: `sudo keelbox doctor` on a terminal runs an agent shim that tries to open `/dev/tty`.

keelbox leaves no passwordless sudo for that account. `keelbox apply` removes `/etc/sudoers.d/90-keelbox-bootstrap` if it exists, and doctor fails while it is there. A passwordless rule under another name is the owner's and stays.

### Machine-in-the-middle on first contact

keelbox connects with `StrictHostKeyChecking=yes` against its own `known_hosts`. A host key is trusted in three cases only:

- the config pins its fingerprint;
- you confirm it interactively;
- keelbox generated the key itself and it arrived through the server's user data.

The third case is the four clouds and `keelbox up`'s cloud-config snippet. The user data is cloud-init on Hetzner, DigitalOcean and Google Cloud, and a launch script on AWS. While that script runs, the instance still presents keys keelbox has not pinned, and is refused.

On Google Cloud the same user data tells Google's guest agent not to replace the host keys on first boot. Were it to replace them anyway, the VM would present a key keelbox has not pinned and be refused.

That key is single-use. The provider's metadata service may serve user data to anything on the server. keelbox therefore replaces the key over the first connection with keys generated on the server, and pins those.

This happens on your computer, so doctor has no check of it. The pinning is checked by unit tests.

### Tampered downloads

Docker, Tailscale and GitHub CLI packages come from apt repositories whose signing keys are pinned by fingerprint in the release manifest. mise, Caddy and restic are checked against checksums in the manifest. Claude Code and Codex come from their vendors' installers over HTTPS. The checksums they check come from the same vendor host, so they catch a corrupted download, not a vendor host that serves another binary; keelbox does not pin them. When the pins were set, the checksums came from these files:

| Tool | The checksum comes from |
|---|---|
| mise | its release's `SHASUMS256.txt`, which the pin matches; the file's signature was not checked |
| Caddy | its checksums file, which carries a Sigstore signature |
| restic | its `SHA256SUMS`, signed with restic's GPG key |

keelbox's own binaries are the installer's download, the one `keelbox self-update` takes, and the box binary your computer downloads. One is used only when three things hold:

- `SHA256SUMS` carries a signature by a release key embedded in keelbox and in the installer;
- the binary matches `SHA256SUMS`;
- the channel index on get.keelbox.dev lists the same checksum.

Downloads come only from keelbox's release hosts on GitHub, named in the code, whatever the index says. The environment can name another HTTPS index, a mirror, but not another release host or trusted key. The installer honours those only with `--test`, for its own tests, and the binary refuses them.

Checked by unit tests and `make test-install`. [Releases and the release manifest](reference/release.md#installing-and-updating-keelbox) has each check the installer makes.

### Silent downgrades

`keelbox apply` refuses a release older than the box's last one unless `--allow-downgrade` is passed. `keelbox self-update` refuses a release older than the running one on the same terms. Removing an existing `docker.io` install needs `--adopt`.

On a machine in use, the first apply stops before any change in three cases. It would cut off a listening service or a Docker-published port. It would cut off a password SSH login. Or it would replace or restart software in a way that leaves something stopped.

It goes on once the config keeps the thing or `--adopt` accepts the loss. `keelbox plan --impact` lists these cases. Reachability there is read from the firewall state, not probed from the LAN.

These are refusals at apply and self-update, so doctor has no check of them. The end-to-end test applies an older release and expects the refusal, and runs the first-apply gate on a machine in use. Self-update's refusal is checked by a unit test.

## Compromised download hosts

keelbox is distributed from two places. get.keelbox.dev, on Vercel, serves the installer and the channel index. GitHub releases serve the binaries and `SHA256SUMS`. The release key lives only in the release workflow's secrets.

| Compromised | Can | Cannot |
|---|---|---|
| get.keelbox.dev alone | Serve a different `install.sh`, which then controls any new install made with `curl \| sh`. Stop updates, or point the channel at an older release, which a new install takes and self-update refuses. | Make an installed keelbox accept a binary: that needs the release key's signature and agreement with the release host. Send downloads to another host. |
| The release host alone | Stop downloads. | Get a changed binary or `SHA256SUMS` installed anywhere: the signature and the index checksum both fail. |
| Both hosts | Both of the above. | Make an installed keelbox accept a binary the release key did not sign. |
| The release key and both hosts | Ship a binary every client accepts. | Nothing: that is the limit. |

A piped installer is trusted by whoever runs it. To install without trusting get.keelbox.dev, download the binary, `SHA256SUMS` and `SHA256SUMS.sig` from the release and [check them by hand](reference/release.md#verifying-a-download-by-hand).

## What keelbox does not defend against

Five more items are under [the agent's network rule](#the-agents-network-rule). They are an address of the box keelbox was never told, the agent's SSH to the box through another machine, and tailscaled's local API. The other two are the first minute after a resume, and a service that an installed package starts.

### Kernel and local privilege-escalation bugs

The limits on this page are Linux users, user namespaces and netfilter. A kernel bug that escapes them escapes keelbox. Rootless Docker depends on unprivileged user namespaces, which have had such bugs.

### User namespaces through bwrap

The Codex and Claude Code sandboxes need `/usr/bin/bwrap` to create a user namespace with capabilities in it. Ubuntu 24.04 and later allow that only to programs with an AppArmor profile that says so. Ubuntu 24.04 loads none for bwrap. There keelbox loads Ubuntu's own `bwrap-userns-restrict` profile, taken on the box from Ubuntu's `apparmor-profiles` package.

With the profile, bwrap may use a user namespace and capabilities inside it to build the sandbox. Every program it runs is stacked with a profile that denies all capabilities, so the agent's code inside gets none.

The agent can run bwrap itself, so this does widen what the agent can reach in the kernel. That is the namespace, mount and network setup bwrap performs, with arguments the agent chooses. It is what 26.04 allows every account by default, with small differences in the profile's text between the two releases.

keelbox does not turn the restriction off: `kernel.apparmor_restrict_unprivileged_userns` stays 1. Turning it off would give every program a capable user namespace.

Claude Code's own sandbox filters Unix sockets with a helper that needs capabilities inside bwrap. With Ubuntu's profile it runs only with `sandbox.network.allowAllUnixSockets`, which keelbox leaves to you. That setting lets sandboxed commands reach the agent's Unix sockets, its rootless Docker socket included.

Doctor runs bwrap with a new network namespace as the agent, and Codex's own `codex sandbox`. The end-to-end test also checks that a program bwrap runs has no capabilities and cannot map a user namespace of its own.

### What the agent writes in a keelbox report

The agent's text is untrusted. A repository's instructions can make an agent file a report that carries a secret, hoping the owner posts it as a public issue.

Reports stay in root's inbox on the box, `/var/lib/keelbox/reports/` with mode 0700, with a fixed list of box facts. No notification goes out. Only `keelbox reports BOX --send N`, run by the owner, takes one off the box.

That command replaces common token, key, address and path shapes, and shows the whole text and what it replaced. It allows an edit, and asks before it opens a prefilled GitHub form that the owner submits. Redaction matches patterns and misses other secrets: the preview and the owner's yes are the control.

The agent can fill the inbox, up to 5 drafts a day and 30 in all.

### An agent hiding unsaved work

`keelbox status`, doctor and the question `keelbox destroy` asks read the agent's projects with git, as the agent, on the agent's own repositories. That check is for accidents. An agent can make a project look committed and pushed. Work outside the projects directory, ignored files and container volumes are not looked at.

Project names are the agent's text. keelbox prints them escaped and keeps them out of notifications.

### A compromised admin key or computer

Whoever holds the admin key holds the box.

### A parked box's snapshot

`keelbox park` keeps the box's disk as a snapshot in the cloud account. On AWS, `keelbox resize` makes such a snapshot too, for the minutes the resize takes.

The snapshot is the whole disk. It holds the agent's sign-ins, any deploy keys and API tokens you gave it, the SSH host keys and the Tailscale identity. It also holds the backup and notification secrets in `/etc/keelbox`. keelbox does not encrypt it, it is not encrypted with a key of yours, and none of the box's limits apply to it.

Whoever can read snapshots in the account, with its console or its API, can create a server from it and read everything. The account is the Hetzner project, the AWS account, the DigitalOcean team or the Google Cloud project. The snapshot stays until `keelbox resume` has applied the box again or `keelbox destroy` deletes it. Parking a box revokes nothing.

What keelbox enforces is on its own side:

| What | How |
|---|---|
| The server goes only once the snapshot exists | `park` deletes the server only after the cloud reports the snapshot complete. An AWS `resize` deletes the instance only after Lightsail reports its snapshot available and the snapshot carries the parked marker. |
| The resumed server has the box's firewall | On Hetzner it is named in the create call, and on Google Cloud it applies by network tag. On DigitalOcean and AWS it is put on straight after the create call, and `resume` goes on only once the cloud reports it in place. |
| Host keys, hostname and accounts stay | User data that carries no key tells cloud-init to keep the SSH host keys and the hostname, and to create no account. On AWS, where user data cannot say it, `/etc/cloud/cloud.cfg.d/99-keelbox.cfg` on the disk says it. `resize` and `park` stop before any change when the box does not have that file. |
| Only keelbox's resources change | `resize`, `park`, `resume` and `destroy` change only a server or a snapshot that carries keelbox's labels for the box: `keelbox=true` and `keelbox-box=<box>`. On DigitalOcean those are the tags `keelbox` and `keelbox-box-<box>`. A snapshot must also carry the parked marker `keelbox-parked`. |

No login is added at a resume:

| Cloud | Logins on the server a resume creates |
|---|---|
| Hetzner | The create call names the project SSH keys of the config, as at creation, because the cloud otherwise sets a root password. They are root's keys, and sshd refuses root. |
| DigitalOcean | The create call names one of the admin's keys when the account holds it, as at creation and for the same reason. It is root's key too. |
| AWS | No key pair is named, so Lightsail uses the region's default key pair for the image account `ubuntu`, as at creation. That account is locked on the disk. |
| Google Cloud | The VM gets the login settings of a created box: project SSH keys blocked, OS Login off, no key in the metadata, no service account. |

Tests against a fake of each cloud's API or CLI check each of those. On a live account of each of the four clouds, the end-to-end test checks a resumed box. Its SSH host key, accounts, password states, sudo rules and authorised SSH keys are the same, and the isolation checks pass again.

### The cloud's firewall on a new server, on DigitalOcean and AWS

Neither cloud takes a firewall in the call that creates a server.

DigitalOcean applies a Cloud Firewall to a Droplet some seconds after the Droplet is active. At `keelbox create` a new Droplet listens on SSH only, which a new box's firewall admits as well.

At `keelbox resume` the Droplet boots with everything on its disk. keelbox therefore adds it to the firewall by its id, as well as by tag, straight after the create call. It goes on only when DigitalOcean reports the firewall applied. Over 8 runs each, a Droplet added by id never answered on port 22 before the firewall took effect. One that carried only the tag did in 2 runs, for about 2 and about 32 seconds.

On AWS a new Lightsail instance has Lightsail's default ports open, 22 and 80, until Lightsail accepts the box's rules. That is so at `keelbox create`, where nothing listens on port 80 on the Ubuntu image. It is also so at `keelbox resume` and at every `keelbox resize`, which creates a new instance from the box's disk. At a resume and a resize keelbox sends the rules from straight after the create call until they are accepted, then reads them back.

How long that takes on Lightsail has not been measured. During those seconds, what stands in front of the box is on its disk. UFW denies incoming connections by default and starts before the network. sshd takes keys only and refuses root.

### The hcloud CLI's copy of the Hetzner token

With `target.hetzner.hcloud_context` keelbox keeps no token. It asks the hcloud CLI for it at each use, on that command's standard output. The token then lives where hcloud keeps it: a plain mode 600 file, readable by any program running as you. keelbox adds no copy and no protection to it.

### Programs on your computer reading the Hetzner token

keelbox keeps the token in the macOS Keychain or a Secret Service when there is one. It passes the token to `security` or `secret-tool` on stdin, never in arguments or the environment. Where neither works, the token is a mode 600 file, and keelbox says so. Any program running as you can read that file, and can usually ask the Keychain or Secret Service for it too.

The token reaches every server in its Hetzner project, not only keelbox's. keelbox's own destructive calls touch only servers labelled `keelbox=true`, but a stolen token is not limited that way. Hetzner tokens have only Read or Read & Write scopes and no expiry, so `keelbox up` recommends a project used only for keelbox.

`keelbox forget-token` removes the local copy. Revoking the token is done in the Hetzner Console.

### An agent leaking what it can read

Outbound traffic is open apart from [the agent's network rule](#the-agents-network-rule). An agent, or code it runs, can send source code, its own tokens and anything else in its home to anywhere on the internet. keelbox has no egress allowlist.

### Prompt injection

keelbox does not inspect what agents read or do. A repository, issue or web page can instruct an agent to misuse whatever the agent can reach. That is its tokens, its GitHub access, the tailnet, and with `users.agent.permissions: auto` its package requests.

### What an installed package brings, with `users.agent.permissions: auto`

`auto` is the default. The agent has any package installed that passes the [checks on a request](#agent-to-root-through-package-requests), without asking you. The checks decide what a request may be. They do not make what it installs harmless, and `auto` gives up what a box without it has:

| What | Detail |
|---|---|
| More on the box | Every installed package widens what is on the box: more programs and libraries, and services that start when their package is installed. A service that listens is behind UFW from outside like any other port, and open to the tailnet like any other port. |
| A way up | Packages install setuid programs and root services. One with a local privilege-escalation bug gives a process of the agent's a route to root that was not on the box before. With `auto` the agent, or whatever instructed it, chooses which packages are there. |
| Maintainer scripts | A package's install scripts run as root when it is installed. They come from the apt sources the box already has. Those are Ubuntu's signed archive, the Docker, Tailscale and GitHub CLI repositories keelbox adds, and any source you added yourself. A request can name packages from a source you added too. |
| The deny-list | It is a list of names, not a property of packages, and keeps a request from replacing or reconfiguring what keelbox manages. A package that is not on it can still change how the box behaves. It can put another program behind a common command name, a service on a port, or a new default. |
| Proxies and relays | A service that an installed package starts runs under its own account. The last item under [the agent's network rule](#the-agents-network-rule) says what that opens. |
| Upgrades | apt may upgrade a package the new one depends on, when the new one needs a newer version of it. It never upgrades one on the deny-list. |
| Resources | An install runs as root, outside the agent's slice, so `users.agent.limits` does not count it. The service has a lower CPU and I/O weight than system services and handles one request at a time. A large install still fills the disk and takes the time it takes. |

Unattended security updates are the mitigation, not a guarantee. They patch an installed package once Ubuntu ships a fix for it. They do nothing before that, and nothing for a package from a source they do not cover. keelbox leaves them at Ubuntu's own origins, and packages from Ubuntu's `universe` get their fixes from the community or through Ubuntu Pro.

`users.agent.permissions: manual` narrows all of this to the packages you approve. When you decide, you see the checked package names and the agent's stated reason. The reason is the agent's own text, shown with everything that does not print removed. A reason is not a fact about the package.

### What a cloud sign-in can do

For a box on AWS, DigitalOcean or Google Cloud, keelbox runs your cloud CLI. The CLI acts with everything its sign-in is allowed: in the whole AWS account, or in the whole DigitalOcean team. On Google Cloud that is the project, and any other project the account reaches.

keelbox's own code reads and changes less:

| Cloud | Reads | Changes only |
|---|---|---|
| AWS | `sts get-caller-identity` and Lightsail operations | The instance tagged `keelbox=true` and `keelbox-box=<box>`, its static IP, and the snapshots it makes of that instance, tagged the same. |
| DigitalOcean | the account, the catalogue, the SSH key list and the list of the account's own images | The Droplet named after the box and tagged `keelbox` and `keelbox-box-<box>`. The firewall `keelbox-<box>` that applies to that tag. The snapshot `keelbox park` makes of that Droplet, tagged the same and `keelbox-parked`. Those three tags. |
| Google Cloud | gcloud's configuration, the project, and the zone, machine type, network, firewall rule, address, snapshot and VM lists | The VM named after the box and labelled `keelbox=true` and `keelbox-box=<box>`. The firewall rules `keelbox-<box>-allow` and `keelbox-<box>-deny`. The address `keelbox-<box>`. The snapshot `keelbox park` makes of the VM's boot disk, labelled the same and `keelbox-parked=true`. |

On Google Cloud, rules and addresses carry no labels. keelbox touches one only when it also has the description keelbox writes, and a rule only when the box's tag is its sole target.

Tests against a fake of each CLI check that keelbox refuses anything else. For DigitalOcean and Google Cloud, a contract test also runs every command through the real CLI against a stub API.

The cloud enforces none of that unless the sign-in is narrowed:

| Cloud | What narrows the sign-in |
|---|---|
| AWS | The profile's policy: [AWS](guides/credentials.md#aws-nothing-kept). |
| DigitalOcean | A token with custom scopes. Scopes limit kinds of resource, not which Droplet: [DigitalOcean](guides/credentials.md#digitalocean-nothing-kept). |
| Google Cloud | Nothing by label, since IAM roles do not limit by label. A project used only for boxes is the limit that holds: [Google Cloud](guides/credentials.md#google-cloud-nothing-kept). |

keelbox never reads the AWS CLI's or gcloud's credential files, or doctl's config file. It passes the single-use host key to the CLI in a mode 600 file, not in an argument.

### Whoever administers the cloud account

Anyone who can administer the AWS account, the DigitalOcean team or the Google Cloud project can reach the server outside keelbox. They can snapshot its disk and change its firewall. On AWS they can replace the instance.

| Cloud | What else the account can do | What stands against it |
|---|---|---|
| AWS | Lightsail installs the region's default key pair for the image account `ubuntu`. It also has a browser SSH client that logs in as `ubuntu`. | Both stop working once keelbox has locked that account, in the `cloud-user-locked` step, after the admin is proven. |
| DigitalOcean | Reset the Droplet's root password and use the console in the control panel. When the account holds none of your SSH keys, DigitalOcean sets a root password at creation, and again on the Droplet `keelbox resume` creates. It may email the password to the account. | keelbox turns off SSH passwords and root login from the first boot. The console does not go through SSH, and the root password is usable only there. |
| Google Cloud | Change the VM's metadata. Google's guest agent creates an account with sudo for an SSH key added to the VM's `ssh-keys` metadata. It runs a `startup-script` as root at the next boot. keelbox does not turn that off. | The four settings below. |

On Google Cloud keelbox sets four things at creation, and `keelbox doctor` checks them from your computer:

- `block-project-ssh-keys=true`, so keys in the project's metadata create no account on the box;
- `enable-oslogin=false`, so the project's IAM does not decide who logs in;
- no SSH keys in the VM's own metadata;
- no service account, so no process on the box, the agent included, can get a token for the project from the metadata server.

### The cloud-config snippet in the provider's hands

The snippet `keelbox up` prints holds your SSH public key and, unless left out, the private half of the server's first host key. It holds no credentials.

The provider stores user data and serves it from its metadata service. Whoever can read it there, before keelbox's first connection replaces the host key, could impersonate the new server to keelbox at that first connection. That is the same exposure as the Hetzner path.

The snippet also gives `admin` passwordless sudo from first boot, as keelbox's own setup does. The account has no password and logs in only with your key.

### The tailnet

Your Tailscale ACLs, not keelbox, govern which machines the box can reach over Tailscale and which machines can reach it. Previews have no sign-in of their own: any device the ACLs let reach the box can open them.

That is no new reach, since UFW admits all tailnet traffic and an agent can bind any port on all addresses. But a preview of an app with test credentials or real data is open to the whole tailnet. Requesting the certificate also publishes the box's MagicDNS name, with the tailnet's name, in Certificate Transparency logs.

### A compromised backup bucket

Whoever holds the bucket, or its key alone, can delete, withhold or roll back snapshots, and learn their sizes and times. restic cannot tell a missing snapshot from one never made. Without the repository password they can neither read nor change the contents: restic encrypts and authenticates every object. Object lock or versioning at the provider protects against deletion.

### ntfy.sh's shared limit

ntfy.sh limits requests per visitor IP address, and the agent shares the box's address. The agent can use up the box's ntfy.sh allowance with its own requests, which delays keelbox's messages. keelbox cannot prevent that. A second channel for urgent events (Pushover or Telegram), or an ntfy account token, is the way round it.

### What keelbox's hosts learn

keelbox collects no usage data: it reports nothing about what you do with it, and a report leaves the box only when you send it. A box makes no unattended request to keelbox's hosts.

Your computer reads `https://get.keelbox.dev/stable.json` at `keelbox self-update`, when it downloads a box binary, and at most once a day for the update check. The check runs after a command you type at a terminal, not on a box, not in CI and not for a development build, and `KEELBOX_NO_UPDATE_CHECK=1` turns it off. `DO_NOT_TRACK=1` does too, and the first check says so. Each request sends a user agent such as `keelbox/0.1.0 (darwin/arm64)`: keelbox's version and platform, and nothing about you or your boxes. Vercel and GitHub, which serve it, see the address it comes from and when. The maintainers count those requests by version and platform, and use the logs for nothing else.

The index is not signed, as the binaries are. A security notice in it is shown as one line of plain text with a link to keelbox.dev or github.com/keelbox, and the command it gives is keelbox's own: whoever could change the index could show a false notice, but not make keelbox run anything. The update check never downloads or installs a release.

The agent CLIs on a box contact their vendors to update, as they do on any computer.

### What channels learn

Every message tells the channel's operator the box name, a project's directory name and when your agents work. A self-hosted ntfy or your own webhook keeps that with you.

### After root compromise

A root attacker can change any file, rule or log on the box. That includes the logbook and whatever `keelbox doctor` reads. Doctor, on a schedule or not, reports what root sees: a root attacker can make it report anything, or nothing. Doctor reports configuration drift; it is not intrusion detection.

Root holds the bucket credentials and the repository password, so a root attacker can read and delete every backup. The agent cannot read either. It does control what its own tar emits: it can fill the bucket or spoil its own backups.

### Remote Control

With `sessions.remote_control.enabled`, anyone signed in to your claude.ai account can start sessions that run commands as the agent. They can do so from anywhere, without SSH or the tailnet. Session content passes through Anthropic. keelbox only runs Claude Code's own server as the agent, and does not limit what those sessions do beyond the agent's own limits.

At the first run, during `keelbox login`, keelbox reads two yes/no flags from the agent's `~/.claude.json`. They say whether you answered yes to Claude Code's two first-run questions. keelbox reads them through a command that runs as the agent and returns an exit code only. It reads nothing Claude Code prints and not its credentials, and it answers neither question.

Unit tests run that command against sample files and check that it prints nothing. The file is the agent's own: the command reports what the file says, not who wrote it.

### Losing the tailnet with public SSH closed

If Tailscale stops working for the box, the box is reachable only from the provider's console or rescue system, where the provider has one. keelbox does not reopen SSH from outside, and nothing on the box reopens it.

[Getting back in](guides/public-ssh.md#getting-back-in) has the recovery steps for each cloud. Each has been run on a live box locked out with public SSH closed. On AWS the only route is a new instance from a snapshot of the box's disk.

### A bug in Caddy

Caddy faces the tailnet. It runs as `keelbox-preview` without capabilities, with the filesystem read-only and homes hidden. A compromise there reaches the TLS key, the agent's loopback ports and the tailnet.

### What the status snapshot shows

`/var/lib/keelbox/status.json` is world-readable, so `keelbox status --all` reads it without sudo. It holds versions, times, counts, unit states, the agent account's name and the machine's size. It also holds the agent's tmux session names, and the names and git state of the directories under its projects directory. It holds nothing from a credential file and no URL, and the agent learns nothing from it that it cannot already see.

`/var/lib/keelbox/installs/` is world-readable for the same reason: the agent's `keelbox install` sees what became of its requests. It holds the permissions mode, the agent's package requests with the reasons the agent gave, and the list of packages installed for the agent. It also holds root's reason for a refusal or a failed install, which for a failed install is the error lines apt printed.

### Secrets in the upload's temporary directory

A keelbox command run from your computer copies the box binary, the config and the secret files it needs to the box over scp. The secrets are the Tailscale auth key, the backup credentials and password, and each notification channel's secrets. They go into a new directory made by `mktemp -d` under `/tmp`, with mode 0700. It belongs to the account keelbox logs in as, normally the admin.

Root then installs the secrets with mode 0600 in `/run/keelbox`, a root-only tmpfs, and the temporary directory is removed before the command runs. If the upload or that install step fails, the directory is left behind.

So for a moment the secrets are readable by the login account, which has sudo anyway. Where `/tmp` is on disk, which it is on Ubuntu 24.04, they are written to the disk. The agent cannot read the directory.

### What the run logs show

An apply started from your computer runs on the box as a transient root unit. It writes its output to `/var/lib/keelbox/runs/`, mode 0640 `root:sudo` in a 0750 directory, and your computer follows it over SSH.

The log is the apply's own output, which holds no secrets, escaped by the sanitiser and bounded in size. The admin and other accounts with sudo can read it without sudo, and the agent cannot. Starting the unit needs root, as the apply did, and so does stopping it.

### What a session shows

The agent's own tmux and shell draw the status line and the welcome in its tmux sessions, on a terminal it writes to. They come from files in its home that it can change: `~/.config/tmux/keelbox.conf`, `~/.tmux.conf` and `~/.bashrc`.

They say where you are and how to leave. "An account without sudo" there repeats the applied config and the account's groups, and is not a check. `keelbox doctor`, run from your computer, reads the box as root. It reports when keelbox's sudoers file for the agent is there, or the agent is in the `sudo` group.

### An agent breaking apply on its own files

An agent can replace files in its home so that apply fails there, and apply reports the path and the account. It then skips that account's later steps and still applies the rest of the box. That includes sshd, the firewall, the agent's network table and the default account's lock. It exits 1, and doctor fails until the home is repaired.

So the agent can stop its own setup converging, but cannot hold back the box's security changes. Nothing outside the agent's reach changes.

### `users.agent.permissions: full`

With `full`, or its alias `users.agent.sudo: true`, the agent has sudo. None of the agent limits bind: the agent can change anything on the box, the limits and their checks included. Doctor warns while a box is in this mode.

## Your responsibilities

keelbox does part of each of these, and the rest is yours.

| What | What keelbox does | What is left to you |
|---|---|---|
| Patching | It enables unattended-upgrades for security updates. With `host.auto_reboot` nightly, the default, or weekly, the watch run reboots the box for an update at night once it is idle. | Leaving both on. With `never`, running `keelbox reboot` when status says a reboot is needed. A new keelbox release reaches a box when you apply it. |
| What the agent may change | `users.agent.permissions: auto` is the default, and a config without the key gets it at the next apply. `keelbox status` lists what was installed for the agent. keelbox never removes a package. | `manual` makes every request wait for your approval: choose it on a machine that also runs your own services, or holds data the agent's work does not need. `full` gives the agent sudo: keep it for a box you would hand to the agent whole. The admin removes a package with `sudo apt-get remove NAME`. |
| Parked boxes | `keelbox destroy` deletes a parked box's snapshot. | Access to the cloud account the snapshot is in. Revoking the agent's sign-ins, parked or destroyed. |
| SSH keys | sshd forwards no SSH agent to the agent account. | A key of the agent's own, not the admin's. The admin key stays off the box, behind a passphrase or a hardware key. Rotate a key you suspect has been exposed. |
| What agents can reach | The agent reads whatever you give it. | Scoped credentials: fine-grained GitHub tokens or deploy keys for the repositories in use, and API keys with spend limits. Your Tailscale ACLs for the box's tag. |
| Review | keelbox does not read what agents write. | Reading what agents commit before it ships. |
| Backups | restic encrypts them with the repository password, and `keelbox restore` reads one back. | A copy of the repository password that is not on the box, and a bucket key with access to that bucket only. A restore into a new directory shows that the backups work. |

The cloud sign-ins stay on your computer, and [cloud sign-ins and permissions](guides/credentials.md) has the narrowest one for each cloud. Ending one is done at the cloud:

| Cloud | Ending the sign-in |
|---|---|
| Hetzner | Delete the token in the Hetzner Console when you stop using keelbox or suspect your computer. `keelbox forget-token` removes only keelbox's copy. |
| AWS | Sign out with the AWS CLI. A sign-in from `aws login` or IAM Identity Center expires, and access keys do not. |
| DigitalOcean | `doctl auth remove --context NAME`, and delete the token in the control panel. |
| Google Cloud | `gcloud auth revoke`. |

keelbox holds nothing of the AWS, DigitalOcean and Google Cloud sign-ins, so it has nothing to forget.
