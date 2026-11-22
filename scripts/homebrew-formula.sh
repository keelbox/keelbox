#!/usr/bin/env bash
# Write the Homebrew formula for a release to stdout.
#   scripts/homebrew-formula.sh VERSION CHECKSUMS_FILE [RELEASES_URL]
# The formula installs the signed release binary for macOS and Linux,
# arm64 and amd64, at the SHA-256s in the release's SHA256SUMS, and the
# completion the binary prints. publish.sh writes it to keelbox/homebrew-tap
# (docs/adr/0029-homebrew-tap.md); RELEASES_URL other than the default is
# for CI's local tap.
set -euo pipefail
version="$1" sums="$2"
releases="${3:-https://github.com/keelbox/releases/releases/download}"
[[ "${version}" == v* ]] || version="v${version}"

sum() {
  local s
  s="$(awk -v f="keelbox-$1" '$2 == f { print $1 }' "${sums}")"
  [[ "${s}" =~ ^[0-9a-f]{64}$ ]] || { echo "no keelbox-$1 in ${sums}" >&2; exit 1; }
  echo "${s}"
}
platform() { # os arch
  printf '    url "%s/%s/keelbox-%s-%s"\n    sha256 "%s"\n' "${releases}" "${version}" "$1" "$2" "$(sum "$1-$2")"
}

cat <<RUBY
# Written by scripts/homebrew-formula.sh in keelbox/keelbox from the
# release's signed SHA256SUMS; publish.sh replaces it on every release.
class Keelbox < Formula
  desc "Set up an Ubuntu machine as a box for AI coding agents"
  homepage "https://keelbox.dev"
  version "${version#v}"
  license "Apache-2.0"

  on_macos do
    on_arm do
$(platform darwin arm64 | sed 's/^/  /')
    end
    on_intel do
$(platform darwin amd64 | sed 's/^/  /')
    end
  end

  on_linux do
    on_arm do
$(platform linux arm64 | sed 's/^/  /')
    end
    on_intel do
$(platform linux amd64 | sed 's/^/  /')
    end
  end

  def install
    # The download is the bare binary, the one file staged; its name
    # depends on the URL.
    bin.install Dir["*"].first => "keelbox"
    chmod 0755, bin/"keelbox"
    generate_completions_from_executable(bin/"keelbox", "completion")
  end

  test do
    assert_match "keelbox v#{version} ", shell_output("#{bin}/keelbox version")
  end
end
RUBY
