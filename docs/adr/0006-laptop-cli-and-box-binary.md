# 0006: Laptop CLI and the binary it uploads to the box

Date: 2026-11-06. Status: accepted.

## Context
The same keelbox binary is the laptop CLI and the on-box agent (ADR 0001). Laptops are macOS (arm64, amd64) or Linux; boxes are linux/amd64 or linux/arm64. A Mac user has no linux binary beside them, so "upload this executable or a sibling" fails for the most common laptop.

## Decision
Remote providers pick the box binary in this order:

1. `--binary PATH`, when given.
2. This executable, when it is already linux on the box's architecture.
3. `keelbox-linux-<arch>` beside this executable (a local `make build`).
4. The cache: `<user cache dir>/keelbox/<version>/keelbox-linux-<arch>` (macOS `~/Library/Caches`, Linux `$XDG_CACHE_HOME` or `~/.cache`).
5. A download of exactly this keelbox version from the release channel, verified, then cached.

A development build (`version: dev`) never downloads: there is no release to match it, and uploading a different version than the one the user runs would apply a different manifest from the one `plan` showed.

### Release channel interface
The release pipeline publishes, per version `X.Y.Z`, at
`https://github.com/keelbox/keelbox/releases/download/vX.Y.Z/`:

| Asset | Content |
|---|---|
| `keelbox-linux-amd64`, `keelbox-linux-arm64` | raw static binaries, the same names `scripts/build.sh` writes |
| `SHA256SUMS` | `sha256sum` format, one line per asset |

keelbox downloads `SHA256SUMS` and the binary over HTTPS, refuses a binary whose SHA-256 is not listed, and caches it (0755) only after the check. `KEELBOX_RELEASE_URL` replaces the base URL (up to and excluding `/vX.Y.Z/`) for mirrors and tests. Signature verification of `SHA256SUMS` (ADR 0016) is in the same shared code that self-update uses, so there is one trust root.

## Consequences
- `brew install keelbox` or the install script on a Mac is enough to manage a box; nothing is cross-compiled locally.
- The box always runs the laptop's exact version, so the embedded release manifest, the plan and the apply agree.
- Offline laptops need `--binary` or a warm cache.
