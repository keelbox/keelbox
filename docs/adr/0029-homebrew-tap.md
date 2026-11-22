# 0029: A Homebrew tap updated by publish.sh

Date: 2026-11-07. Status: accepted.

## Context
Many macOS users, and some Linux users, install command-line tools with Homebrew and distrust `curl | sh`. keelbox ships signed static binaries (ADR 0013, ADR 0016); a formula can install those as they are. homebrew-core takes only notable, stable open-source projects, and keelbox is new and pre-1.0, so a third-party tap is the way in. Releases are promoted by `scripts/publish.sh` on the maintainer's machine with the existing `gh` login; there is no release bot and no Actions secret for other repositories.

## Decision
- A public repository `keelbox/homebrew-tap` holds `README.md` and `Formula/keelbox.rb`, nothing else: `brew install keelbox/tap/keelbox`.
- The formula installs the release binary, not a source build: `on_macos`/`on_linux` and `on_arm`/`on_intel` blocks name `keelbox-<os>-<arch>` on keelbox's release host, each with its SHA-256 from that release's signed `SHA256SUMS`. It writes completion with `generate_completions_from_executable(bin/"keelbox", "completion")`, and its test checks `keelbox version`.
- `scripts/homebrew-formula.sh VERSION SHA256SUMS` writes the formula. `publish.sh` runs it after it has verified the release key signature, the cosign bundle and every checksum, and puts the result in the tap through the GitHub contents API with the same `gh` login: no new token, no deploy key, no Actions secret. Unchanged, it is not rewritten.
- CI tests the formula on macOS: `scripts/test-homebrew.sh` serves the snapshot binaries from `file://` URLs through a local tap, then runs `brew style`, `brew install`, `brew test`, and checks the completion files. The same script runs on Linux in the `homebrew/brew` image.
- `keelbox self-update` refuses a binary under Homebrew's `Cellar` and says `brew upgrade keelbox`, so the two never fight over the file.

## Consequences
- Homebrew checks only the SHA-256 in the formula. The formula's trust is the tap's: the maintainer's GitHub account, as for the release host. The release key signature and Sigstore bundle are checked once, by `publish.sh`, before the formula names the binary; users who want to check them install with `get.keelbox.dev` or by hand (ADR 0016).
- The tap updates at the same moment as the stable channel, and a rollback with `publish.sh <previous tag>` rolls the formula back too.
- The tap's README points at get.keelbox.dev.
