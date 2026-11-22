# 0019: Preview URLs on the tailnet: `keelbox expose`, Caddy, Tailscale certificates

Date: 2026-11-06. Status: accepted.

## Context
An agent's dev server listens on `127.0.0.1` (the primer requires it). The human wants to open it from a laptop or a phone without SSH port forwarding, over HTTPS (browser APIs limited to HTTPS pages, service workers and OAuth callbacks need it), and never on a public address. The agent has no sudo, so publishing has to work without it, and nothing the agent writes may reach a privileged config unchecked.

## Decision

### Addressing: one name, one HTTPS port per preview
Tailscale issues certificates only for a node's own MagicDNS name (`<box>.<tailnet>.ts.net`); there are no per-node subdomains, and Tailscale Services (virtual names) need tags and admin-console set-up per service. Path prefixes (`/web/`) break dev servers that serve assets from `/`. So every preview is `https://<box>.<tailnet>.ts.net:<port>/`, with the port taken from `previews.ports` (default `8401-8499`). A name keeps its port while it exists, so URLs are stable across restarts of the dev server and of the box.

### Commands
`keelbox expose <port> [--name NAME]`, `keelbox expose --list`, `keelbox expose --remove NAME`. One verb with flags, like `keelbox backup --list` (ADR 0020). It runs on the box as the agent. From a laptop, `keelbox ssh` first: the laptop has no business editing the agent's previews.

### No sudo: a file in the agent's home, rendered by root
- `keelbox expose` edits `~/.config/keelbox/previews.json` (`{"version":1,"previews":[{"name","port","https_port"}]}`), then waits for the status file to show that exact content (by SHA-256) processed.
- `keelbox-previews.path` (root) watches the file and starts `keelbox-previews.service`, which runs `keelbox previews-sync` against the applied config. `keelbox-previews.timer` runs it at boot and every 10 minutes, to follow tailnet and certificate changes. Apply runs it once.
- The service reads the file through `Env.FS(agent)` (ADR 0015): a link to a root-only file gets a permission error, not the file. It is capped at 64 KiB. Each entry must have a name of lowercase letters, digits and hyphens (at most 32), a port 1-65535 and an HTTPS port in `previews.ports`, all unique, at most 100. Refused entries are listed in the status and by doctor; the rest are served.
- The Caddy config is JSON generated from Go values. The only agent input in it is the validated name (a map key), the local port (in `localhost:<port>`) and the HTTPS port. Listen addresses come from `tailscale status`, and only addresses in `100.64.0.0/10` and `fd7a:115c:a1e0::/48` are rendered; anything else is an error.
- Rejected: a sudoers rule for a helper (the agent "has no sudo" would stop being plainly true), a setuid helper, and Caddy's admin API (any local process could rewrite the config).

### Caddy
- Pinned in the release manifest: the official release tarball, checked against the SHA-512 in `caddy_<v>_checksums.txt`, whose Sigstore signature was verified for Caddy's release workflow identity when the pin was set. Installed root-owned at `/usr/local/bin/caddy`. No apt package, whose unit would start on `:80` on install.
- `keelbox-preview-caddy.service` runs as the system user `keelbox-preview`, with the admin API disabled, automatic HTTPS off (no `:80`, no ACME), and systemd hardening: no capabilities, `ProtectSystem=strict`, `ProtectHome`, and `IPAddressDeny=any` with `IPAddressAllow=localhost 100.64.0.0/10 fd7a:115c:a1e0::/48`, so even a wrong config could not serve or reach anything but loopback and the tailnet.
- With nothing to serve, or the box off the tailnet, the service stops Caddy: no listener at all.
- The upstream is `localhost:<port>`, so a dev server on `::1` works too.

### TLS
The service, as root, runs `tailscale cert` for the box's name into `/var/lib/keelbox/previews/tls/` (directory `0750 root:keelbox-preview`, key `0640`). tailscaled returns its cached certificate until renewal is due, so the 10-minute timer renews it; Caddy restarts only when the certificate or config changed. keelbox does not set `TS_PERMIT_CERT_UID` or touch tailscaled's config. The tailnet must have MagicDNS and HTTPS certificates on; without them the status says how to turn them on.

### Firewall
UFW already admits everything on `tailscale0` and denies other inbound traffic (ADR 0005), so no rule is added. Caddy binds only tailnet addresses, which is what keeps previews off other interfaces even with `host.firewall.enabled: false`. Doctor checks that Caddy's listening sockets are all on tailnet addresses, that `IPAddressDeny` is set on its unit, and that no UFW rule admits a preview port off `tailscale0`.

### Off the tailnet
`keelbox expose PORT` refuses and says why: previews are served only on the tailnet, at the box's MagicDNS name, never on a public address; the admin joins with `keelbox login`. Nothing is recorded, so the agent is not left believing a URL exists.

## Consequences
- Anyone the tailnet ACLs let reach the box can open every preview, without signing in. That is no new reach: UFW already admits all tailnet traffic, so any port the agent binds on all addresses is open to the tailnet too. Previews add TLS, a stable name and loopback-only dev servers.
- Requesting a certificate puts the box's MagicDNS name, which includes the tailnet name, in public Certificate Transparency logs.
- A preview port that something else already holds on the tailnet address makes Caddy fail to start, and with it every preview; doctor and `expose` report it.
- Previews are on by default wherever Tailscale is. `previews.enabled: false` skips the module but does not remove an earlier install.
