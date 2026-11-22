---
name: release
description: Cut, verify and promote a keelbox release (release-please PR, signed build, keelbox/releases, get.keelbox.dev). Use when asked to release, ship, publish, promote or roll back a keelbox version.
---

# Release keelbox

Follow the Release runbook in `docs/maintaining.md`; this is the checklist and the stop points.

1. Confirm `main` is green: `gh run list --branch main --limit 5`. Red: stop and report.
2. `gh pr list --label "autorelease: pending"`. Read the version and the CHANGELOG diff. Report them and **stop for the maintainer's go-ahead** before merging.
3. After approval: `gh pr merge <n> --squash`, then watch `release.yml` to completion.
4. `gh release view vX.Y.Z` must list four `keelbox-<os>-<arch>` binaries, their `.spdx.json`, `SHA256SUMS`, `SHA256SUMS.sigstore.json`, `stable.json`. Missing: `gh workflow run release.yml -f tag=vX.Y.Z` once; still missing, stop.
5. `scripts/publish.sh vX.Y.Z` with `KEELBOX_VERCEL_SCOPE` set (the maintainer's Vercel team; if unset, ask the maintainer). It verifies before copying; never work around a verification failure, report it.
6. Smoke test as a user in a scratch `HOME` (runbook step 6) and report the `keelbox version` line.
7. Update keelbox.dev for the release (runbook step 7). **Stop for the maintainer's go-ahead** before publishing it. Then `scripts/site-check.sh vX.Y.Z` must exit 0. A release is not done, and is not reported as done, before it does.

`CHANGELOG.md`: release-please writes final-release entries from the commits; release candidates are cut by the maintainer by hand (`docs/maintaining.md`, "Without the release PR").

Never: edit `CHANGELOG.md` outside that runbook, push or move a `v*` tag, create tokens or secrets, or run `keelbox apply` outside a `kb-*` VM.
