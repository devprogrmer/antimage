#!/usr/bin/env bash
# Production release build - builds frontend, embeds it, and creates release artifacts.
#
# This script ensures the production installation requires ZERO npm/Node.js.
# The frontend is built during release preparation and embedded into the Go binary.
#
# Usage:
#   ./scripts/build-release.sh [version]
#
# If version is not provided, uses git describe.

set -euo pipefail

VERSION="${1:-$(git describe --tags --always --dirty)}"
OUT="dist"

echo "==> Building Antimage ${VERSION}"
echo

# Step 1: Build frontend
echo "==> Step 1/4: Building frontend..."
cd web
npm install --silent
npm run build
cd ..
echo "✓ Frontend built and embedded into internal/panel/webui/dist"
echo

# Step 2: Sync install script
echo "==> Step 2/4: Syncing install script..."
cp scripts/install.sh internal/panel/httpapi/install.sh
echo "✓ Install script synced"
echo

# Step 3: Build binaries
echo "==> Step 3/4: Building release binaries..."
rm -rf "$OUT"
mkdir -p "$OUT"

for target in linux/amd64 linux/arm64; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  for cmd in antimage-panel antimage-node antimage-ctl; do
    echo "  Building ${cmd} ${GOOS}/${GOARCH}..."
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath \
      -ldflags "-s -w -X github.com/amyrm/antimage/internal/shared/version.Version=${VERSION}" \
      -o "${OUT}/${cmd}-${GOOS}-${GOARCH}" "./cmd/${cmd}"
  done
done
echo "✓ Binaries built"
echo

# Step 4: Generate checksums
echo "==> Step 4/4: Generating checksums..."
( cd "$OUT" && sha256sum ./* > SHA256SUMS )
echo "✓ Checksums generated"
echo

echo "==> Release build complete!"
echo
echo "Artifacts in ${OUT}/ for ${VERSION}:"
ls -lh "${OUT}"
echo
echo "Verifying checksums..."
( cd "$OUT" && sha256sum -c SHA256SUMS )
echo
echo "✓ All checksums verified"
echo
echo "==> Production binaries ready for release"
echo "   These binaries contain the embedded frontend."
echo "   End users need ZERO npm/Node.js dependencies."
