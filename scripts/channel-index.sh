#!/usr/bin/env bash
# Write a channel index (get.keelbox.dev/stable.json) for a release to stdout.
#   scripts/channel-index.sh VERSION CHECKSUMS_FILE [RELEASES_URL [PREVIOUS_INDEX]]
# The index names the version, the release host and, per platform, the
# binary's URL and SHA-256 from the release's SHA256SUMS. "checksums" lists
# every published release's binaries ("vX.Y.Z/<os>-<arch>": sha256), carried
# over from PREVIOUS_INDEX, so clients can check any release they fetch
# against this second origin. Clients accept only release hosts they know
# (docs/adr/0016-release-signatures.md); RELEASES_URL other than the default
# is for test servers. "security" is get/security.json, the security notices
# clients show (docs/maintaining.md), left out while that file is [].
set -euo pipefail
version="$1" sums="$2"
security="$(dirname "$0")/../get/security.json"
releases="${3:-https://github.com/keelbox/releases/releases/download}"
previous="${4:-}"
base="${releases}/${version}"
history=""
if [[ -n "${previous}" ]]; then
  # Earlier releases' entries, one per line as this script writes them.
  history="$(grep -E '^ *"v[0-9][0-9A-Za-z.-]*/(linux|darwin)-(amd64|arm64)": "[0-9a-f]{64}",?$' "${previous}" |
    sed -E 's/^ *//; s/,$//' | grep -v "^\"${version}/" || true)"
fi
awk -v version="$version" -v releases="$releases" -v base="$base" -v history="$history" '
  $2 ~ /^keelbox-(linux|darwin)-(amd64|arm64)$/ {
    platform = substr($2, 9)
    entries[++n] = sprintf("    \"%s\": {\n      \"url\": \"%s/%s\",\n      \"sha256\": \"%s\"\n    }", platform, base, $2, $1)
    sums[n] = sprintf("\"%s/%s\": \"%s\"", version, platform, $1)
  }
  END {
    if (n != 4) { print "expected 4 binaries in checksums, found " n > "/dev/stderr"; exit 1 }
    printf "{\n  \"channel\": \"stable\",\n  \"version\": \"%s\",\n  \"releases\": \"%s\",\n  \"binaries\": {\n", version, releases
    for (i = 1; i <= n; i++) printf "%s%s\n", entries[i], (i < n ? "," : "")
    printf "  },\n  \"checksums\": {\n"
    m = 0
    if (history != "") m = split(history, all, "\n")
    for (i = 1; i <= n; i++) all[++m] = sums[i]
    # Sorted, for a stable file.
    for (i = 2; i <= m; i++) { v = all[i]; for (j = i - 1; j >= 1 && all[j] > v; j--) all[j + 1] = all[j]; all[j + 1] = v }
    for (i = 1; i <= m; i++) printf "    %s%s\n", all[i], (i < m ? "," : "")
    printf "  }"
  }' "$sums"
if [[ -s "${security}" && "$(tr -d ' \t\n' <"${security}")" != "[]" ]]; then
  printf ',\n  "security": %s\n}\n' "$(cat "${security}")"
else
  printf '\n}\n'
fi
