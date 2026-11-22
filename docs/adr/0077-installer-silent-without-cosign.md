# 0077: The installer says nothing about cosign when it is not installed

Date: 2026-11-16. Status: accepted. Amends [ADR 0016](0016-release-signatures.md).

## Context
ADR 0016 made the release key signature the check every install requires, and kept the keyless cosign bundle as an extra for a machine that has cosign. It also had the installer state, on every install without cosign, that the Sigstore signature was not checked.

Few machines have cosign, so that line would be on nearly every install: `cosign not found`, between two lines that report checks which passed. A reader takes it for a fault, or for a step they missed. Neither is true: the install it describes passed every check keelbox requires.

## Decision
- **Without cosign the installer prints nothing about it.** The release key signature and the checksum lines are the report.
- **With cosign nothing changes.** The bundle is verified against the release workflow's identity, a mismatch stops the install, and the installer prints that the Sigstore signature was verified.
- **A cosign that is on `PATH` but does not run still gets a warning.** That machine was set up to make the check, and it did not happen.
- **The bundle is still published and still required at promotion.** `scripts/publish.sh` verifies it before a release reaches the public channel. The reference says how to check it by hand.

## Consequences
- An install's output does not tell the reader that a second signature exists. `docs/reference/release.md` and `SECURITY.md` do.
- A reader who wants the second check installs cosign and runs the installer again.
