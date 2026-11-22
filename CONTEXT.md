# keelbox: context

**AI coding agents on an Ubuntu machine you own or rent.** keelbox sets up an Ubuntu machine (a VPS, a home server, a VM) as a workstation for AI coding agents such as Claude Code and Codex. `keelbox up` asks a few questions and takes a box from nothing to signed in: it writes the box's YAML file, creates or converges the machine from the box or a Mac or Linux laptop, runs each vendor's own sign-in on the user's terminal, and agents then work in tmux sessions the user reaches from a laptop or a phone. The steps it runs (`apply`, `login`, ...) stay available on their own. Apache-2.0.

## Glossary
- **Box**: one machine managed by one config file. Its name is the hostname and server name.
- **Config**: the YAML file (`version: 1`). Strict: unknown fields are errors.
- **Box registry**: `~/.config/keelbox/boxes/<box>.yaml` on the laptop, where `keelbox up` writes configs; commands take the box name.
- **keelbox up**: the guided, resumable setup; its progress per box is in `boxes/<box>.up.json` (ADR 0025), its answers before the config exists in `up-draft.json` (ADR 0035).
- **Credential store**: the macOS Keychain or a Secret Service, holding the Hetzner API token on the laptop; a mode 600 file where neither exists (ADR 0026).
- **Cloud-config snippet**: what `keelbox up` prints for a server the user creates in any provider's console (ADR 0028).
- **Target / provider**: where the box runs and how keelbox reaches it: `local` (on the box), `ssh` (copy keelbox over, run there), `hetzner` (create the server via the Hetzner API, then ssh), `aws`, `digitalocean`, `gcp` (create the server through the cloud's own CLI, signed in on the laptop, then ssh).
- **Module**: a unit of box configuration (host, access, runtime, sessions, primer, logbook, previews, backup, notify, doctor, watch, permissions, agent-policy, agents, applied in that order): ordered steps plus verify checks.
- **Step**: one idempotent check + apply pair.
- **Plan / apply / doctor**: check only / converge / verify the running box.
- **Impact report**: `keelbox plan --impact`, what apply would change on a machine already in use that its owner may care about, grouped (cuts off, replaces, restarts, changes, keeps), from an **inventory** of the machine. The **first-apply gate** stops a box's first apply before any change for the items it marks, until the config keeps them or `--adopt` accepts them (ADR 0038).
- **Channel index**: `get.keelbox.dev/stable.json`, the stable channel's pointer to the current keelbox release and where its binaries are downloaded. Read by the installer and `keelbox self-update` (ADR 0013).
- **Release manifest**: versioned file of tested tool versions, embedded in the binary (`internal/manifest/stable.yaml`). Channel `stable`.
- **Admin user**: the human's account. Key-only SSH, passwordless sudo (it has no password).
- **Agent user**: runs agents, its own rootless Docker and projects. No sudo by default, never in the `docker` group; its connections to the box's own SSH port are refused.
- **Agent permissions**: `users.agent.permissions`, how much the agent user may change the box itself: `manual` (it asks, the owner approves), `auto` (the default: Ubuntu packages through the install service) or `full` (sudo). A **package request** is what `keelbox install` writes; the **install service** is the root unit that reads it as the agent, checks it against the **deny-list** and apt, and installs it or keeps it for `keelbox approve` (ADR 0074).
- **Parked box**: a cloud box whose server `keelbox park` deleted after saving its disk as a snapshot, to stop the server's bill while the box is not needed. The snapshot's labels in the cloud are what makes it parked; `keelbox resume` creates the server again from it (ADR 0071).
- **Cloud default user**: the image's account (`ubuntu`), locked once the admin is proven.
- **Soft step / deferred step**: a step whose failure does not stop apply (agent CLI installs) / that waits for a later apply (locking the account keelbox is running as).
- **Security model**: `docs/security-model.md`, what the box defends against and what it does not. Docs and the primer never claim more.
- **Primer**: generated `AGENTS.md` telling agents the box's rules.
- **Tailnet proof**: an apply whose SSH session arrived at the box's own Tailscale address on `tailscale0`, as the admin. The only thing that closes public SSH (ADR 0021).
- **Day-two run**: a short root keelbox command a systemd timer or path unit starts between applies; it never changes the config (ADR 0030). The **watch run** is the one every two minutes. The install service is the one that adds software, at the agent's request (ADR 0074).
- **Watchdog**: the part of the watch run that brings back the agent's tmux server or Remote Control when it is down, within a budget, and never touches one that runs (ADR 0039).
- **Unclean shutdown**: a boot that ended without the clean-shutdown marker (power loss, a crash, a hard reset), recorded in the logbook and reported once the box is back (ADR 0040).
- **Status snapshot**: `/var/lib/keelbox/status.json`, the box's state as the watch run last saw it, readable without sudo.
- **Event**: something to tell the owner, of a fixed kind, from a root source or from the agent's events file; queued on the box until a channel takes it (ADR 0032).
- **Channel**: a place events go that the owner configures (`notify.channels`): ntfy, Pushover, Telegram or a webhook, its secrets in mode 600 files copied to `/etc/keelbox/notify/` (ADR 0043).
- **Managed hooks**: hooks in the agent policy files that run `keelbox event` as the agent when a CLI finishes, waits for input or stops on an error; the agent's own settings cannot turn them off (ADR 0043).
- **Scheduled doctor**: doctor's checks on a timer (`doctor.schedule`), alerting only when a check starts or stops failing (ADR 0042).
- **Agent CLIs**: Claude Code and Codex, installed by their vendors' installers into the agent's home and updating themselves; keelbox does not choose their versions (ADR 0088).
- **Agent policy files**: the agent CLIs' administrator settings (`/etc/claude-code/managed-settings.d/`, `/etc/codex/requirements.toml`), which rank above the agent's own; one builder writes them for every feature (ADR 0033).
- **Logbook**: append-only record of every apply on the box, and of every action keelbox takes between applies, each with its kind (`/var/lib/keelbox/logbook.jsonl`).

## Hard constraints
- keelbox never collects, stores, proxies or automates Claude/OpenAI sign-in or tokens. Login is the user's ceremony: `keelbox login` only starts each vendor's own flow on the user's terminal and reads nothing that passes through.
- Defaults are hardened: no SSH passwords, no root login, firewall on, agent without sudo or Docker-group access, services on loopback.
- Never describe a box or agent as secure, safe, sandboxed or contained in an absolute sense. State what is enforced and what is not, as `docs/security-model.md` does.
- Secrets travel by file path, never on command lines, in output or in the logbook.

## Out of scope
keelbox does not cover: creating servers on providers other than Hetzner, AWS, DigitalOcean and Google Cloud (Azure and the rest; their servers work over SSH or with the cloud-config snippet), AWS services other than Lightsail (EC2; ADR 0065), a DigitalOcean sign-in of keelbox's own (it uses doctl's; ADR 0067), Google Cloud networks other than `default`, Arm machine types and prices (gcloud lists none; ADR 0068), per-agent Unix users, an egress allowlist, release channels other than stable, unattended keelbox upgrades, a command that undoes keelbox on a machine, agent CLIs beyond Claude Code and Codex, non-Ubuntu.
