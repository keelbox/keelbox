#!/usr/bin/env bash
# The last check of a release: keelbox.dev says what the installer delivers.
#   scripts/site-check.sh vX.Y.Z
# Exits 0 only when the site's home page names the version and its changelog
# page has the release's entry, so the docs it serves are that release's.
# The site is updated by hand after scripts/publish.sh (docs/maintaining.md,
# "Release").
# KEELBOX_SITE overrides the address, for a preview.
set -euo pipefail
tag="${1:?usage: scripts/site-check.sh vX.Y.Z}"
site="${KEELBOX_SITE:-https://keelbox.dev}"
version="${tag#v}"
# A version followed by anything but another digit: rc.1 is not rc.10.
pattern() { printf '%s([^0-9]|$)' "${1//./\\.}"; }
stale=0

home="$(curl -fsSL "${site}/")" || {
  echo "${site} does not answer" >&2
  exit 1
}
if ! grep -qE "$(pattern "${tag}")" <<<"${home}"; then
  names="$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?' <<<"${home}" | sort -u | paste -sd' ')"
  echo "${site} names ${names:-no version}, not ${tag}" >&2
  stale=1
fi
changelog="$(curl -fsSL "${site}/docs/changelog")" || {
  echo "${site}/docs/changelog does not answer" >&2
  exit 1
}
# The entry's heading: "0.1.0 (2026-11-21)".
if ! grep -qE "${version//./\\.} \(" <<<"${changelog}"; then
  echo "${site}/docs/changelog has no entry for ${version}: its docs are an earlier release's" >&2
  stale=1
fi
if [[ "${stale}" -ne 0 ]]; then
  echo "The release is not finished. Update the site (docs/maintaining.md, \"Release\"), then: scripts/site-check.sh ${tag}" >&2
  exit 1
fi
echo "== site: ${site} names ${tag} and serves its docs"
