# keelbox

[![docs](https://img.shields.io/badge/docs-keelbox.dev-blue)](https://keelbox.dev/docs)
[![release](https://img.shields.io/github/v/release/keelbox/releases?include_prereleases&label=release)](https://github.com/keelbox/releases/releases)
[![licence](https://img.shields.io/badge/licence-Apache--2.0-blue)](LICENSE)

keelbox is a command-line tool that sets up an Ubuntu machine (a machine at home, a VPS or a cloud server) as a workstation for AI coding agents such as Claude Code and Codex. You describe the box in one YAML file; `keelbox apply` installs and configures it from your laptop over SSH, or on the machine itself, and changes nothing on a second run. The agents run as their own unprivileged user, in tmux sessions you can attach to from a laptop or a phone. You sign in to each agent yourself: keelbox never sees, stores or relays the agents' sign-ins (Claude, Codex, GitHub). What it holds is what you give it: the Hetzner API token in your laptop's credential store, and on the box root-only copies of the backup and notification secrets you configure ([credentials](docs/guides/credentials.md)).

Status: pre-1.0. Laptops: macOS and Linux. Boxes: Ubuntu 24.04 and 26.04, on Intel or AMD (amd64) or ARM (arm64) processors; ARM was tested end to end once, on an arm64 Google Cloud VM set up over SSH (keelbox creates only x86 servers on Google Cloud), and creating an ARM server on Hetzner has not been tested. On each cloud keelbox creates servers on (Hetzner, AWS, DigitalOcean, Google Cloud) the end-to-end test has created, set up, checked and destroyed a box on a live account.

## Install

Read the installer, then run it:

```sh
curl -fsSL https://get.keelbox.dev -o install.sh
less install.sh
sh install.sh
```

or `curl -fsSL https://get.keelbox.dev | sh`, or `brew install keelbox/tap/keelbox`. Either way you get the signed release binary in `~/.local/bin` (no sudo) with shell completion; the installer checks the release key signature on `SHA256SUMS` and the binary against it and the channel index, offers to put `~/.local/bin` on your `PATH`, and lists what to delete to remove it. `keelbox self-update` updates. Details, options and checking the script itself: [installing keelbox](docs/guides/installing.md).

## Quick start

On your laptop:

```sh
keelbox up
```

`keelbox up` asks where the box runs: a server you already reach over SSH (a home server, a mini PC, a VPS), this machine, a new Hetzner, AWS, DigitalOcean or Google Cloud server, or a server you create on any provider from a cloud-config it prints. On a machine you already have, it checks it first (Ubuntu, sudo, what is listening) and says what it will change: key-only SSH, a firewall (with the ports you choose kept open to your LAN), two new accounts. It then sets the machine up, runs each vendor's sign-in on your terminal (keelbox never sees those credentials) and prints how to attach, reach the box from a phone, and open sessions from the Claude app. Ctrl-C stops between steps; run it again and it continues where it stopped.

On the Ubuntu machine you want to set up, install and start `up` in one line:

```sh
curl -fsSL https://get.keelbox.dev | sh -s -- up
```

```sh
keelbox attach acme      # the agent's tmux session
keelbox ls               # your boxes
```

[Your first box](docs/getting-started.md) follows the steps. The flags for agents and CI, and the commands `up` runs, are in [without the wizard](docs/guides/automation.md).

## What a box gets

| Area | What keelbox sets up |
|---|---|
| Accounts | `admin` (you: key-only SSH, passwordless sudo) and `agent` (no sudo, not in the `docker` group; it gets Ubuntu packages by asking a root service that checks each request, `keelbox install`, or only with your approval if you choose so). The image's default account (`ubuntu`) is locked once the admin works. |
| SSH | Keys only, no root login, no agent forwarding to the agent. keelbox pins host keys in its own `known_hosts` and never accepts one silently; on a cloud server it creates (Hetzner, AWS, DigitalOcean, Google Cloud) it injects a host key it generated, then replaces it over the first connection. |
| Firewall | UFW denying inbound except SSH, the tailnet and the LAN ports you keep open (`host.firewall.allow`). A `DOCKER-USER` guard for the system Docker daemon's published ports. An nftables table refusing the agent's connections to SSH on the box and to cloud metadata. |
| Docker | Docker Engine from Docker's signed repository, with bounded logs. The agent gets its own rootless daemon; the system socket is closed to it. |
| Tools | Base build tools, the GitHub CLI, mise with Node and pnpm. Claude Code and Codex from their vendors' own installers, under the vendors' terms, updating themselves as on any computer ([the agent CLIs](docs/guides/upgrades.md#the-agent-clis)). |
| Sessions | tmux with a managed config, sessions saved and restored across reboots, a server started at boot, the session list on SSH login. |
| Access | Tailscale from its signed repository, joined with an auth key file if you give one; reach the box and its sessions from a phone over the tailnet. |
| Primer | `/etc/keelbox/AGENTS.md`, read by Claude Code and Codex: the box's rules and where things are. |
| Upgrades | Unattended security updates. Tool versions other than the agent CLIs come from a tested release manifest; apply refuses to move a box to an older release, and `keelbox rollback` returns to the previous one. keelbox on your computer checks for a newer release once a day; `KEELBOX_NO_UPDATE_CHECK=1` turns that off ([what it sends](docs/security-model.md#what-keelboxs-hosts-learn)). |
| Logbook | `/var/lib/keelbox/logbook.jsonl`: one record of every apply, without secrets. |

Commands: `keelbox help` lists them, `keelbox COMMAND --help` shows one with examples ([reference](docs/reference/cli.md)). On a cloud server, `keelbox resize`, `keelbox park` and `keelbox resume` change its size, stop its bill while it is not needed and bring it back ([guide](docs/guides/resize-park-resume.md)). Machines already in use: [a machine already in use](docs/guides/machine-in-use.md).

## What it does not protect against

keelbox reduces what code an agent runs can do to the box. It does not stop an agent sending what it can read, its own tokens and your source code included, anywhere on the internet: outbound traffic is open and there is no egress allowlist. It does not inspect prompts or agent actions, does not protect against kernel bugs or a compromised laptop or admin key, and does not govern your tailnet. Read the [security model](docs/security-model.md) before giving an agent credentials.

## Documentation

[docs/](docs/README.md): getting started, guides (sessions, signing in, upgrades, adopting, troubleshooting, removing a box), reference (commands, config, releases, files on the box), and how keelbox works.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md), including the rules for AI-assisted contributions, then the [architecture tour](docs/contributing/architecture.md) and [testing guide](docs/contributing/testing.md). Walkthroughs: [add a module](docs/contributing/add-a-module.md), [add a provider](docs/contributing/add-a-provider.md). Coding agents start at [AGENTS.md](AGENTS.md).

- [Security policy](SECURITY.md): report vulnerabilities privately.
- [Code of conduct](CODE_OF_CONDUCT.md)
- [Changelog](CHANGELOG.md)

## Licence

Apache-2.0. See [LICENSE](LICENSE). Copyright 2026 Nick Martin-Kruck; see [NOTICE](NOTICE), which also lists third-party material.
