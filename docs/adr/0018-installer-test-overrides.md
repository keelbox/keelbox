# 0018: Release overrides only for the installer's tests

Date: 2026-11-06. Status: accepted. Amends ADR 0016.

## Context
ADR 0016 let `KEELBOX_RELEASE_URL` replace the release host in the installer and the binary, and `KEELBOX_TEST_RELEASE_KEYS` replace the installer's trusted keys, for mirrors and tests. An environment variable left set, or set by something else on the machine, then silently changed what an install trusts. The binary never needs either: it refuses development builds, and its tests set `Fetcher.BaseURL` directly. The installer's wget fallback also followed redirects anywhere, and wget's `--https-only` binds only recursive downloads, not redirects.

## Decision
- `install.sh --test` is the only way to honour `KEELBOX_RELEASE_URL`, `KEELBOX_TEST_RELEASE_KEYS` and a plain HTTP `KEELBOX_INDEX_URL`, and it warns that it does. Without `--test`, setting either variable stops the install, and the index URL must be HTTPS. `scripts/test-install.sh` passes `--test`, and checks each refusal without it.
- The binary refuses to start a download when `KEELBOX_RELEASE_URL` is set, and refuses a non-HTTPS `KEELBOX_INDEX_URL`. Another HTTPS index stays allowed for mirrors; the release host it names must still be one of keelbox's.
- With wget, the installer follows redirects itself, one hop at a time with `--max-redirect=0`, and refuses one from HTTPS to anything else, as curl's `--proto =https` does. A wget without `--max-redirect` (BusyBox) is not used.

### Test builds
A keelbox built with `-tags keelbox_testkeys` (package `internal/testbuild`) also trusts the keys in the PEM file `KEELBOX_TEST_RELEASE_KEYS` names, so a test can sign an index with a throwaway key. In every other build the variable is ignored: a release binary cannot be pointed at another key by its environment. The release workflow refuses such a binary: it checks `.goreleaser.yaml` for the tag before the build, and each binary's build settings before signing.

## Consequences
- A mirror of the binaries themselves is not configurable; installing from one means checking the files by hand, as `docs/reference/release.md` shows.
