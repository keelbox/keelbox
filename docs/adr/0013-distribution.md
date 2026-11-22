# 0013: Distribution: signed release binaries, get.keelbox.dev and self-update

Date: 2026-11-06. Status: superseded by 0016. Amends the URL layout in 0006.

## Context
Users install with `curl -fsSL https://get.keelbox.dev | sh` on macOS or Linux, and keep keelbox current without a package manager. The signed binaries are published from a separate releases repository, and the binary host must be able to change without an installer or CLI change.

## Decision

### Build
goreleaser (pinned in `mise.toml`) builds linux and darwin, amd64 and arm64: static (`CGO_ENABLED=0`), `-trimpath`, `-buildid=`, file times from the commit, version, commit and commit date in `internal/version`. Two builds of a commit are byte-identical. Assets are raw binaries named `keelbox-<os>-<arch>` (the names 0006 fixed), no archives: the installer and self-update need nothing to unpack.

### Integrity
- `SHA256SUMS` lists every binary and SBOM.
- `SHA256SUMS.sigstore.json`: a keyless cosign bundle. The signer is the GitHub OIDC identity `https://github.com/keelbox/keelbox/.github/workflows/release.yml@refs/heads/main`; the bundle carries the Rekor inclusion proof. No signing key exists to leak or rotate.
- One SPDX SBOM per binary (syft). Cheap, and the dependency list is short.

### Hosting
- Binaries: GitHub releases on the public `keelbox/releases` repository, which holds only release assets and a README.
- `get.keelbox.dev`: a static Vercel project serving `install.sh` at `/` as text/plain (readable in a browser before piping to `sh`) and the channel index `/stable.json`. DNS: `get.keelbox.dev A 76.76.21.21`, DNS only. The index and the binaries are on separate origins.
- Channel index, written by `scripts/channel-index.sh`:

```json
{
  "channel": "stable",
  "version": "v0.1.0",
  "releases": "https://github.com/keelbox/releases/releases/download",
  "binaries": { "darwin-arm64": { "url": ".../v0.1.0/keelbox-darwin-arm64", "sha256": "..." } }
}
```

Every download URL is derived from `releases`, so the binary host is a field of the index.

### Release flow
release-please keeps a release PR on `main` (Conventional Commits; before 1.0 a feature bumps the minor version, and a release may be a prerelease); merging it tags `vX.Y.Z` and creates the release, and the same workflow runs goreleaser, signs and attaches the assets and `stable.json` to it. `scripts/publish.sh vX.Y.Z` promotes a release: it verifies the signature and checksums, copies the assets to `keelbox/releases` and deploys `get.keelbox.dev`, using the maintainer's existing `gh` and `vercel` logins.

Promotion is a maintainer step, not CI, because `GITHUB_TOKEN` cannot write to another repository and deploying Vercel from CI needs a token: either would be a new long-lived credential (a GitHub App or fine-grained token, and a Vercel token) held by the source repository. It also keeps promotion to the stable channel deliberate.

The CLI version (`v0.1.0`) is separate from the manifest's `release` id (`2026.11.4`): one names the binary, the other the tested tool set it embeds.

### Installer
POSIX `sh`, shellcheck clean. Detects OS and architecture (native arm64 under Rosetta), reads the index, downloads `SHA256SUMS` and the binary, verifies the checksum, and when `cosign` is on `PATH` verifies `SHA256SUMS` against the signer identity first. Installs to `~/.local/bin` without sudo (`--dir`), pins with `--version`, does nothing when the version is already installed, and replaces the binary by rename.

### Self-update
`keelbox self-update`, not `upgrade`: it replaces only this binary, and moving a box to a new release is `keelbox apply` (ADR 0010). The channel is authoritative (a different version, newer or older, is installed). The download must match both `SHA256SUMS` from the release host and the index's checksum from `get.keelbox.dev`, and must run `version`, before it is renamed over the executable. It shares `internal/release` with the box-binary download from 0006, so there is one verification path.

Self-update does not verify the cosign signature itself: that needs sigstore-go and its dependency tree, against 0001's single-dependency rule. Two independent origins plus TLS cover a compromise of either host; the installer and `scripts/publish.sh` verify signatures.

## Consequences
- Changing the binary host needs no client change.
- Every release needs a maintainer to run `scripts/publish.sh`.
- Installs with cosign get signature verification; without it, checksum only.
- `brew install keelbox` comes from a tap (ADR 0029).
