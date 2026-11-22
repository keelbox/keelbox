# 0004: Release manifest

Date: 2026-11-06. Status: accepted.

## Decision
`internal/manifest/stable.yaml`, embedded in the binary, is the only place tool versions live. It has a `release` id (recorded in every logbook entry), the supported OS/arch matrix (apply refuses anything else), and three pinning modes:

| Mode | Tools | apply behaviour |
|---|---|---|
| exact | mise (with per-arch SHA-256), Node, pnpm, tmux-resurrect and tmux-continuum (commit SHAs) | converges to the pin, up or down |
| series | Docker Engine (`series: "29"`) | installs the newest package in the series; Engine and CLI pinned to the same version; out-of-series is a change |
| install | Claude Code, Codex | version installed on a fresh box; presence-only check, because both CLIs update themselves and apply must not fight them; doctor shows drift |

Apt signing keys are pinned by primary-key fingerprint (Docker, Tailscale); a downloaded key that does not match is refused. mise is installed from the GitHub release tarball verified against the manifest's checksum (not the release's own SHASUMS file), root-owned in `/usr/local/bin`.

## Consequences
- A bump is one commit to `stable.yaml` plus `release`. The stable channel is that one file.
- The end-to-end workflow installs the vendors' current agent CLIs ([ADR 0088](0088-vendor-agent-clis.md)).
