# keelbox

[![docs](https://img.shields.io/badge/docs-keelbox.dev-blue)](https://keelbox.dev/docs)
[![release](https://img.shields.io/github/v/release/keelbox/releases?include_prereleases&label=release)](https://github.com/keelbox/releases/releases)
[![licence](https://img.shields.io/badge/licence-Apache--2.0-blue)](LICENSE)

keelbox is a command-line tool that turns an Ubuntu machine into a workstation for AI coding agents such as Claude Code and Codex. It can be a new cloud server, a VPS or a machine at home. One command asks a few questions, sets the machine up from your laptop over SSH, and leaves you with an agent you can attach to from your laptop, your phone or the Claude app.

The agents run as their own account, without sudo by default, in tmux sessions that keep running when you disconnect. You sign in to each agent yourself: keelbox never sees, stores or relays those sign-ins. It holds only what you give it: a Hetzner token in your OS credential store, and root-only copies on the box of the backup and notification secrets you configure ([credentials](docs/guides/credentials.md)).

keelbox is pre-1.0. It runs on macOS and Linux, and sets up Ubuntu 24.04 and 26.04 on amd64 or arm64. It creates servers on Hetzner, AWS (Lightsail), DigitalOcean and Google Cloud, or sets up one you already have.

## Install

```sh
curl -fsSL https://get.keelbox.dev | sh
```

This puts a signed release binary in `~/.local/bin`, without sudo. You can also read the script first, or use `brew install keelbox/tap/keelbox`: [install keelbox](docs/guides/installing.md).

## Quick start

1. Run `keelbox up` on your laptop, and answer its questions. Before it changes anything, it says what it will create and, on most clouds, the monthly price.
2. Sign in to Claude Code, Codex and GitHub when it asks. Each vendor's own sign-in runs on your terminal.
3. Attach to the box, here named `acme`, and start an agent:

   ```sh
   keelbox attach acme    # then type claude or codex
   ```

To set up the Ubuntu machine you are on, install and start in one line: `curl -fsSL https://get.keelbox.dev | sh -s -- up`.

[Your first box](docs/getting-started.md) walks through every question.

## What a box gets

- An `admin` account for you, with key-only SSH and sudo, and an `agent` account without sudo (unless you choose to give it sudo) or access to the system Docker socket.
- A UFW firewall and Tailscale. SSH from the internet closes once keelbox reaches the box over your tailnet.
- Docker, with a rootless daemon of the agent's own, plus the GitHub CLI, Node and pnpm.
- Claude Code and Codex from their vendors' installers, in tmux sessions restored after a reboot.
- Packages the agent can install without sudo, through `keelbox install`, or only with your approval.
- Unattended security updates, tested tool versions from a release manifest, and `keelbox rollback` to the previous release.

The whole box is described in one YAML file, and `keelbox apply` changes nothing on a second run: see [how keelbox works](docs/concepts/how-it-works.md).

## What it does not protect against

keelbox limits what code an agent runs can do to the box. It does not stop an agent sending what it can read, its own tokens and your source code included, anywhere on the internet: outbound traffic is open. It does not inspect prompts or agent actions, does not protect against kernel bugs or a compromised laptop or admin key, and does not govern your tailnet. Read the [security model](docs/security-model.md) before you give an agent credentials.

## Documentation

Start with [the docs index](docs/README.md), also at [keelbox.dev/docs](https://keelbox.dev/docs). `keelbox help` lists every command, and `keelbox COMMAND --help` shows one with examples. keelbox on your computer checks for a newer release once a day; `KEELBOX_NO_UPDATE_CHECK=1` turns that off ([updates](docs/guides/upgrades.md)).

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md), including the rules for AI-assisted contributions, then the [architecture tour](docs/contributing/architecture.md). Coding agents start at [AGENTS.md](AGENTS.md).

- [Security policy](SECURITY.md): report vulnerabilities privately.
- [Code of conduct](CODE_OF_CONDUCT.md)
- [Changelog](CHANGELOG.md)

## Licence

Apache-2.0. See [LICENSE](LICENSE). Copyright 2026 Nick Martin-Kruck; see [NOTICE](NOTICE), which also lists third-party material.
