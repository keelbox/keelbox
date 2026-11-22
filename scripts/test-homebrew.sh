#!/usr/bin/env bash
# Install keelbox with Homebrew from a local release, through the formula
# scripts/homebrew-formula.sh writes for keelbox/homebrew-tap.
#   scripts/test-homebrew.sh [DIST]
# DIST is a goreleaser dist directory (default dist/release, from
# `make snapshot`; the macOS CI job downloads one). The binaries are served
# from file:// URLs through a local tap, then brew installs, runs the
# formula's test, and the completion files must be where shells look.
# Needs brew; changes only the local tap, which it removes.
set -euo pipefail
cd "$(dirname "$0")/.."
dist="${1:-dist/release}"
version="$(sed -n 's/.*"tag":"\([^"]*\)".*/\1/p' "${dist}/metadata.json")"
[[ -n "${version}" ]] || { echo "no tag in ${dist}/metadata.json" >&2; exit 1; }
export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ANALYTICS=1 HOMEBREW_NO_ENV_HINTS=1

work="$(mktemp -d)"
tap=keelbox/local
trap 'brew uninstall --formula "${tap}/keelbox" >/dev/null 2>&1 || true; brew untap "${tap}" >/dev/null 2>&1 || true; rm -rf "${work}"' EXIT
mkdir -p "${work}/${version}"
for b in "${dist}"/keelbox_*/keelbox; do
  d="$(basename "$(dirname "$b")")" # keelbox_<os>_<arch>_<variant>
  IFS=_ read -r _ os arch _ <<<"$d"
  cp "$b" "${work}/${version}/keelbox-${os}-${arch}"
done
scripts/homebrew-formula.sh "${version}" "${dist}/SHA256SUMS" "file://${work}" >"${work}/keelbox.rb"

fail() { echo "FAIL: $*" >&2; exit 1; }
brew tap-new --no-git "${tap}" >/dev/null
cp "${work}/keelbox.rb" "$(brew --repository "${tap}")/Formula/keelbox.rb"
brew style "${tap}/keelbox" || fail "brew style"
brew install --formula "${tap}/keelbox"
brew test "${tap}/keelbox"
prefix="$(brew --prefix)"
"${prefix}/bin/keelbox" version | grep -q "^keelbox ${version} " || fail "wrong version on PATH"
for f in etc/bash_completion.d/keelbox share/zsh/site-functions/_keelbox share/fish/vendor_completions.d/keelbox.fish; do
  grep -q keelbox "${prefix}/${f}" || fail "no completion at ${prefix}/${f}"
done
echo "== ok: brew install ${tap}/keelbox (${version}), completion for bash, zsh and fish"
