#!/usr/bin/env bash
# Create or update the repository's labels; safe to rerun. Needs gh, logged in
# with write access. Issue forms and .github/labeler.yml apply these; the
# triage runbook in docs/maintaining.md says when to use each.
set -euo pipefail
repo="${1:-keelbox/keelbox}"
while IFS='|' read -r name color description; do
  gh label create "${name}" --repo "${repo}" --color "${color}" --description "${description}" --force
done <<'LABELS'
bug|d73a4a|Something keelbox does wrong
enhancement|a2eeef|New behaviour or a better way to do something
provider|5319e7|A new or changed provider
docs|0075ca|Documentation only
dependencies|0366d6|Go modules, actions or manifest pins
ci|ededed|Workflows, checks and the e2e harness
needs-triage|fbca04|Not yet reviewed by a maintainer
needs-info|d4c5f9|Waiting on the reporter
good first issue|7057ff|Small, well-defined, a good start
help wanted|008672|A contribution would be welcome
duplicate|cfd3d7|Already reported
wontfix|ffffff|Out of scope or declined
area: cli|c5def5|cmd, internal/cli, internal/config
area: modules|c5def5|Engine, modules, system, manifest
area: providers|c5def5|internal/provider
area: release|c5def5|Builds, installer, self-update, release pipeline
LABELS
# GitHub defaults these replace.
for name in documentation invalid question; do
  gh label delete "${name}" --repo "${repo}" --yes 2>/dev/null || true
done
