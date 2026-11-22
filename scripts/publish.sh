#!/usr/bin/env bash
# Promote a built release to the public download host and the stable channel.
#   scripts/publish.sh vX.Y.Z
# Downloads the assets the release workflow attached to keelbox/keelbox's
# release, verifies the release key signature, the cosign signature and
# every checksum, copies them to the public keelbox/releases repository,
# then deploys get.keelbox.dev with that release's install.sh, its signed
# SHA256SUMS and a stable.json that keeps every earlier release's checksums
# (from the live index), and points the Homebrew formula in
# keelbox/homebrew-tap at it. Uses the maintainer's existing gh and vercel
# logins; idempotent.
#   KEELBOX_FRESH_INDEX=1 scripts/publish.sh vX.Y.Z
# starts a new stable.json, without reading a live index, for the first
# publish to a new index.
# See docs/maintaining.md.
set -euo pipefail
cd "$(dirname "$0")/.."
tag="${1:?usage: scripts/publish.sh vX.Y.Z}"
source_repo=keelbox/keelbox public_repo=keelbox/releases tap_repo=keelbox/homebrew-tap
vercel_scope="${KEELBOX_VERCEL_SCOPE:?set KEELBOX_VERCEL_SCOPE to the Vercel team that owns get.keelbox.dev}"
vercel_project="${KEELBOX_VERCEL_PROJECT:-keelbox-get}"
signer="https://github.com/${source_repo}/.github/workflows/release.yml@refs/heads/main"

for tool in gh vercel; do command -v "${tool}" >/dev/null || { echo "need ${tool}" >&2; exit 1; }; done
cosign="$(mise which cosign)" # pinned in mise.toml; resolved here, inside the repo
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
assets="${work}/assets" site="${work}/site"
mkdir -p "${assets}" "${site}"

echo "== download ${tag} from ${source_repo}"
gh release download "${tag}" --repo "${source_repo}" --dir "${assets}"
cd "${assets}"
"${cosign}" verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity "${signer}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
sha256sum --check --strict SHA256SUMS
cd - >/dev/null
mise exec -- go run ./internal/tools/releasesign verify "${tag}" "${assets}/SHA256SUMS"
if [[ -n "${KEELBOX_FRESH_INDEX:-}" ]]; then
  echo "== KEELBOX_FRESH_INDEX: a new stable.json"
  echo '{}' >"${work}/live.json"
else
  curl -fsS --proto =https https://get.keelbox.dev/stable.json >"${work}/live.json"
fi
scripts/channel-index.sh "${tag}" "${assets}/SHA256SUMS" "" "${work}/live.json" >"${work}/stable.json"
mise exec -- go run ./internal/tools/indexcheck "${work}/stable.json"
# An index with no checksums has no entries to keep.
{ grep -oE '"v[0-9][0-9A-Za-z.-]*/' "${work}/live.json" || true; } | sort -u | while read -r old; do
  grep -qF "${old}" "${work}/stable.json" || { echo "stable.json would drop ${old}" >&2; exit 1; }
done

echo "== publish to ${public_repo}"
prerelease=()
[[ "${tag}" == *-* ]] && prerelease=(--prerelease)
notes="${work}/notes.md"
cat >"${notes}" <<NOTES
Install or update:

    curl -fsSL https://get.keelbox.dev | sh -s -- --version ${tag}

Verify by hand: \`sha256sum --check SHA256SUMS\` after checking its release key signature
(the public key is in get/install.sh and internal/release/keys/ in the source) with
\`{ printf 'keelbox-release ${tag}\\n'; cat SHA256SUMS; } >signed && openssl pkeyutl -verify -pubin -inkey keelbox-release.pem -rawin -in signed -sigfile SHA256SUMS.sig\`,
and its Sigstore signature with
\`cosign verify-blob --bundle SHA256SUMS.sigstore.json --certificate-identity ${signer} --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS\`.
SPDX SBOMs are attached per binary.

$(gh release view "${tag}" --repo "${source_repo}" --json body --jq .body)
NOTES
if gh release view "${tag}" --repo "${public_repo}" >/dev/null 2>&1; then
  gh release upload "${tag}" "${assets}"/* --repo "${public_repo}" --clobber
else
  gh release create "${tag}" "${assets}"/* --repo "${public_repo}" --title "keelbox ${tag}" \
    --notes-file "${notes}" "${prerelease[@]}"
fi

echo "== deploy get.keelbox.dev"
# The installer served is the release asset, checked against the signed
# SHA256SUMS above, and must be the tagged source's. SHA256SUMS and its
# signatures are served beside it, so the script can be checked before it
# runs (docs/guides/installing.md).
[[ -f "${assets}/install.sh" ]] || { echo "the release has no install.sh" >&2; exit 1; }
git show "${tag}:get/install.sh" | cmp -s - "${assets}/install.sh" ||
  { echo "the release's install.sh is not ${tag}:get/install.sh" >&2; exit 1; }
cp "${assets}/install.sh" "${assets}/SHA256SUMS" "${assets}/SHA256SUMS.sig" "${assets}/SHA256SUMS.sshsig" \
  "${assets}/SHA256SUMS.sigstore.json" "${site}/"
git show "${tag}:get/vercel.json" >"${site}/vercel.json"
cp "${work}/stable.json" "${site}/"
vercel deploy "${site}" --prod --yes --scope "${vercel_scope}" --project "${vercel_project}" >/dev/null

echo "== update ${tap_repo}"
# The formula goes in through GraphQL createCommitOnBranch with the same gh
# login, so the tap needs no token or Actions secret of its own (ADR 0029),
# and GitHub signs the commit (ADR 0037). expectedHeadOid makes a concurrent
# push fail the call rather than be overwritten.
formula="${work}/keelbox.rb" path=Formula/keelbox.rb branch=main
scripts/homebrew-formula.sh "${tag}" "${assets}/SHA256SUMS" >"${formula}"
head="$(gh api "repos/${tap_repo}/git/ref/heads/${branch}" --jq .object.sha)"
current="$(gh api "repos/${tap_repo}/contents/${path}?ref=${head}" --jq .content 2>/dev/null || true)"
if [[ -n "${current}" ]] && [[ "$(printf '%s' "${current}" | base64 -d)" == "$(cat "${formula}")" ]]; then
  echo "${path} already installs ${tag}"
else
  # shellcheck disable=SC2016 # GraphQL variables, not shell ones
  gh api graphql -f query='
    mutation($repo: String!, $branch: String!, $head: GitObjectID!, $message: String!, $path: String!, $contents: Base64String!) {
      createCommitOnBranch(input: {
        branch: {repositoryNameWithOwner: $repo, branchName: $branch}
        expectedHeadOid: $head
        message: {headline: $message}
        fileChanges: {additions: [{path: $path, contents: $contents}]}
      }) { commit { oid } }
    }' -f "repo=${tap_repo}" -f "branch=${branch}" -f "head=${head}" -f "message=keelbox ${tag}" \
    -f "path=${path}" -f "contents=$(base64 -w0 "${formula}")" >/dev/null
  echo "${path} now installs ${tag}: brew install keelbox/tap/keelbox"
fi

for _ in $(seq 30); do
  if curl -fsS https://get.keelbox.dev/stable.json | grep -q "\"version\": \"${tag}\""; then
    echo "== live: https://get.keelbox.dev/stable.json is ${tag}"
    echo "== next: keelbox.dev must say ${tag} too. Update the site (docs/maintaining.md), then: scripts/site-check.sh ${tag}"
    exit 0
  fi
  sleep 2
done
echo "get.keelbox.dev does not serve ${tag} yet" >&2
exit 1
