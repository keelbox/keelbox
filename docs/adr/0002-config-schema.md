# 0002: Config schema, version 1

Date: 2026-11-06. Status: accepted.

## Decision
One YAML document per box, `version: 1`, decoded with unknown fields rejected. Top-level keys: `box`, `channel`, `target` (`provider: local|ssh|hetzner` with `ssh` and `hetzner` blocks), `host`, `users` (`admin`, `agent`), `runtime`, `agents`, `sessions`, `access` (`tailscale`), `primer`.

- Defaults live in code (`ApplyDefaults`); toggles are tri-state so "unset" means "default".
- Validation collects every problem with its path and reports them together; values derived from `box` are not double-reported.
- Lock-out guards are validation errors: the admin account needs at least one SSH key; `public_ssh: false` needs Tailscale.
- Key files (`ssh_authorized_key_files`) and `~` are resolved where keelbox runs (under sudo: `SUDO_USER`'s home). Remote providers inline keys before shipping the config, so the box never needs the laptop's files.
- The shipped config has `target.provider: local`; the box keeps a copy at `/etc/keelbox/keelbox.yaml` (0600) so `keelbox doctor` and `status` run with no arguments.
- Secrets are referenced by path only (`access.tailscale.auth_key_file`, `target.hetzner.token_file`), and refused if group- or world-readable.

## Consequences
Schema changes that break old files bump `version`; additive fields do not.
