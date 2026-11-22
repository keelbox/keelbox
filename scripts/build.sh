#!/usr/bin/env bash
# Build static keelbox binaries for both supported box architectures into dist/.
# A keelbox run from dist/ uploads dist/keelbox-linux-<arch> when the box's
# architecture differs from the machine running keelbox. Release builds for
# every platform come from .goreleaser.yaml; the flags here match it.
set -euo pipefail
cd "$(dirname "$0")/.."
version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
commit="$(git rev-parse HEAD 2>/dev/null || true)"
date="$(git log -1 --format=%cI 2>/dev/null || true)"
pkg=github.com/keelbox/keelbox/internal/version
mkdir -p dist
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" mise exec -- go build -trimpath \
    -ldflags "-s -w -X ${pkg}.Version=${version} -X ${pkg}.Commit=${commit} -X ${pkg}.Date=${date}" \
    -o "dist/keelbox-linux-${arch}" ./cmd/keelbox
done
ln -sf "keelbox-linux-$(mise exec -- go env GOARCH)" dist/keelbox
ls -l dist/
