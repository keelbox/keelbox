#!/usr/bin/env bash
# Everything CI runs: format, vet, lint (Go, shell and workflows), unit tests,
# and compile the e2e harness.
set -euo pipefail
cd "$(dirname "$0")/.."
unformatted="$(mise exec -- gofmt -l .)"
if [[ -n "${unformatted}" ]]; then
  printf 'gofmt needed:\n%s\n' "${unformatted}" >&2
  exit 1
fi
mise exec -- go vet ./...
mise exec -- go vet -tags e2e ./test/e2e/
# The laptop CLI must build for every laptop, not just the box.
for target in darwin/arm64 darwin/amd64 linux/arm64; do
  GOOS="${target%/*}" GOARCH="${target#*/}" mise exec -- go vet ./...
done
mise exec -- golangci-lint run ./...
mise exec -- shellcheck get/install.sh scripts/*.sh
mise exec -- actionlint
mise exec -- go test -race ./...
