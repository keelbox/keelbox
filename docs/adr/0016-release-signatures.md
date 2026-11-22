# 0016: Release integrity: a release key, allowlisted hosts, two checksum origins

Date: 2026-11-06. Status: accepted. Supersedes 0013 (distribution); its build, hosting and release-flow sections still describe what happens, amended as below.

## Context
ADR 0013 claimed that self-update's check against "two independent origins" covered a compromise of either host. It did not: the channel index on get.keelbox.dev named the host that served `SHA256SUMS`, so whoever controlled get.keelbox.dev controlled both checksums. The installer checked only the release host's `SHA256SUMS` (the same origin as the binary) unless cosign happened to be installed, which on a fresh machine it rarely is. The laptop's download of the box binary used `SHA256SUMS` alone. Self-update installed whatever version the index named, so a tampered index could roll every keelbox back to an older, genuinely signed release. The cosign bundle cannot be checked in the binary without sigstore-go (ADR 0001's single dependency).

## Decision

### One trust root: the release key
- Every release's `SHA256SUMS` is signed with an Ed25519 release key: `SHA256SUMS.sig` (the raw 64-byte signature over the file, what `openssl pkeyutl -verify -rawin` and Go's `crypto/ed25519` check) and `SHA256SUMS.sshsig` (the same key's `ssh-keygen -Y sign` signature, namespace `keelbox-release`, for machines whose openssl lacks Ed25519: macOS ships LibreSSL).
- The public keys are embedded in the binary (`internal/release/keys/*.pem`) and written into `get/install.sh`; a unit test keeps the two lists identical. Verification is stdlib only.
- The private key is the release workflow's `KEELBOX_RELEASE_SIGNING_KEY` secret (PKCS#8 PEM), passed only to the signing step as an environment variable. `internal/tools/releasesign` signs, refuses a key the tree does not trust (a wrong secret fails the release instead of publishing signatures no client accepts), and never prints the key. `scripts/publish.sh` verifies the signature again before promoting.
- The keyless cosign bundle stays, checked by the installer when cosign is installed and by `publish.sh`. It is an extra, not the requirement.

### Every download goes through one check
`release.Fetcher.Fetch` is the only path for self-update and for the laptop's download of the box binary. `get/install.sh` does the same steps in shell. A binary is used only if all hold:
1. `SHA256SUMS` carries a valid signature by a trusted release key;
2. the binary's SHA-256 matches `SHA256SUMS`;
3. the channel index lists the same checksum for that version and platform.

Downloads come only from allowlisted release hosts, in code and in the installer: `https://github.com/keelbox/releases/releases/download` and `https://github.com/keelbox/keelbox/releases/download`. The index chooses between them and cannot name another. HTTPS downloads may not redirect to plain HTTP. `KEELBOX_INDEX_URL` and `KEELBOX_RELEASE_URL` replace the origins for mirrors and tests; the installer's `KEELBOX_TEST_RELEASE_KEYS` replaces the trusted keys and says so loudly, for tests only.

### The index keeps every release's checksums
`stable.json` gains `checksums`, `"vX.Y.Z/<os>-<arch>": "<sha256>"` for every published release, so a laptop on an older release can still check its own version's box binary against the index. `scripts/publish.sh` builds it from the release's verified `SHA256SUMS` plus the live index's history (`scripts/channel-index.sh VERSION SUMS "" PREVIOUS`) and refuses to deploy an index that drops an entry. `binaries` lists the current version's checksums.

### Downgrade floor
`keelbox self-update` refuses a release older than the running one (semantic version order, prereleases before their release) unless `--allow-downgrade` is passed. Older releases are genuinely signed, so the signature alone does not stop a rollback.

### Installer
The installer needs OpenSSL 3 or OpenSSH 8.1+ (`ssh-keygen -Y`). With neither it stops and says what to install. It reports which tool checked the release key signature, and states plainly when cosign is absent or does not run that the Sigstore signature was not checked.

## What each compromise can do

| Who is compromised | Can | Cannot |
|---|---|---|
| get.keelbox.dev (Vercel) | Serve a different `install.sh` to new `curl \| sh` installs: the installer is the trust root of a fresh install, as with any piped installer. Deny service. Point the channel at an older release, which fresh installs take and self-update refuses. | Make an existing keelbox (self-update, box binary) accept a binary: it needs the release key's signature and the release host's agreement. Redirect downloads to another host. |
| The release host (GitHub releases) | Deny service. Serve old, genuinely signed assets under a version they belong to. | Make anything install a changed binary or `SHA256SUMS`: the signature and the index checksum both fail. |
| Both hosts | The above together; fresh `curl \| sh` installs as in the first row. | Make an existing keelbox accept a binary the release key did not sign. |
| The release key alone | Sign a `SHA256SUMS`, which still has to agree with the index from get.keelbox.dev. | Ship a binary without also controlling a host, and for existing keelbox both hosts. |
| The release key and both hosts | Ship a binary every client accepts. | (Nothing stops this; rotate the key.) |

## Rotation
Trust is a list, and any one key's signature is accepted. To rotate: generate the new key and its secret; release N adds the new public key to `internal/release/keys` and `install.sh` and is still signed by the old key; release N+1 is signed by the new key (clients from release N trust it); once the oldest supported keelbox carries the new key, a later release removes the old one. A compromised key is removed in the next release, signed with a key that is not; keelbox binaries that still trust the bad key must update, so the security advisory names the first release without it. The signing tool refuses a key not in the tree's list, so the order of these steps cannot be got wrong silently.

## Consequences
- A fresh install with no openssl 3 and no ssh-keygen (some minimal containers) stops instead of installing unverified.
- `--allow-downgrade` also governs self-update.
