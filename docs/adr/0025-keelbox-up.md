# 0025: keelbox up and the box registry

Date: 2026-11-06. Status: accepted.

## Context
Setting a box up command by command is easy to get wrong on a first run. Without a first command, a config from `keelbox init` with `provider: local` would make `create` fail with an error that names the problem but not the fix. The Hetzner path would need four console steps (upload an SSH key, note its name, create a token, save it with `cat >` to a mode 600 file), every command would need the config path, and the user would have to know the order create, login, apply, attach.

## Decision
- **`keelbox up [box]`** is the first command. It asks where the box runs (a new Hetzner server, an existing Ubuntu server over SSH, a server the user creates from a cloud-config keelbox prints (ADR 0028), or this machine), the name, the size (Hetzner: the types the API says can be ordered in the chosen location, with its monthly and hourly prices; default the cheapest with 2 vCPU and 4 GB), the agents, Claude Code Remote Control, and Tailscale (default on). It finds the SSH key or offers `ssh-keygen` on the user's terminal, uploads it to Hetzner as `keelbox-<laptop hostname>` or reuses the project key with the same fingerprint, shows what it will do with the cost, and asks once.
- It then runs the commands a user would, in order, through the same code: `create` or the bootstrap `apply`; `login`, each vendor's own flow on the user's terminal (ADR 0014; keelbox never sees agent credentials); `apply` again, which closes public SSH once this machine reaches the box over the tailnet (ADR 0021); then a short "Ready" summary.
- **Resumable.** Progress is a stage in `~/.config/keelbox/boxes/<box>.up.json` (configured, waiting, server, applied, signed-in, ready), plus the server's address and the bootstrap account; no secrets. `keelbox up <box>` again starts at the first stage not done. A Hetzner create that fails after the server exists records the address; the next run applies as `root` if root can still log in, else as the admin. `destroy` removes the record.
- **Non-interactive.** Every question has a flag (`--provider`, `--name`, `--size`, `--location`, `--host`, `--ssh-user`, `--host-key`, `--ssh-key`, `--token-file`, `--agents`, `--remote-control`, `--tailscale=false`, `--no-host-key`, `--yes`). Without a terminal, a missing required answer stops `up` before any change, with exit 2 and an example command. Steps only the user can do (creating a server from the cloud-config, signing in) stop with exit 3 and the command that continues, so agents and CI can tell "stopped for the user" from "failed".
- **Prompts** use a small stdlib helper (`internal/prompt`): numbered choices, yes or no, a line with a default, a secret with echo off through termios. No TUI dependency (ADR 0001: one dependency).
- **Box registry.** `up` writes the commented config, from the same template as `init`, to `~/.config/keelbox/boxes/<box>.yaml`. Every command takes a box name or a config path, or nothing: `/etc/keelbox/keelbox.yaml` on a box, else `./keelbox.yaml`, else the only box in the registry. `keelbox ls` lists the registry with provider, address and status from local files only. `init --provider local|ssh|hetzner` writes that provider's target block.
- **Errors end with the fix**: the command or YAML that resolves them.

## Consequences
- The documented path is one command; `init`, `create`, `apply` and `login` stay for configs written by hand and are documented as what `up` runs.
- The registry lives beside `known_hosts` and the tailnet records in `~/.config/keelbox`, so a laptop's boxes are in one place; configs can still be kept in a repository and passed by path.
- `up` asks for no Tailscale auth key: `login` runs `tailscale up` on the user's terminal. An auth key still works through `access.tailscale.auth_key_file`.
- Tested with scripted input against a fake Hetzner API and a fake runner (decisions, resume, the stops), and end to end on LXD (existing server and cloud-config) and Hetzner, up to the sign-in, which needs real vendor accounts.
