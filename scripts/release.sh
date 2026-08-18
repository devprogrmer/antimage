#!/usr/bin/env bash
# Build release artifacts and their checksums into dist/.
#
# Only Linux amd64 and arm64 are published. The tree cross-compiles for macOS
# and Windows too, but antimage-node reads /proc and install.sh refuses any
# distribution other than Debian and Ubuntu, so publishing those would claim
# support that does not exist.
set -euo pipefail

VERSION="${1:-$(git describe --tags --always --dirty)}"
OUT="dist"

rm -rf "$OUT"
mkdir -p "$OUT"

for target in linux/amd64 linux/arm64; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  for cmd in antimage-panel antimage-node antimage-ctl; do
    echo "building ${cmd} ${GOOS}/${GOARCH}"
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath \
      -ldflags "-s -w -X github.com/amyrm/antimage/internal/shared/version.Version=${VERSION}" \
      -o "${OUT}/${cmd}-${GOOS}-${GOARCH}" "./cmd/${cmd}"
  done
done

( cd "$OUT" && sha256sum ./* > SHA256SUMS && sha256sum -c SHA256SUMS )
echo
echo "artifacts in ${OUT}/ for ${VERSION}"
