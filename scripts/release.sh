#!/usr/bin/env bash
# Cross-compile + package CoinRollHunter for every supported platform.
# Pure-Go SQLite (modernc) means CGO_ENABLED=0 cross-compiles cleanly with no
# per-OS toolchain. Output lands in dist/ as per-target archives + checksums.
#
#   ./scripts/release.sh            # version from `git describe`
#   VERSION=v1.2.3 ./scripts/release.sh
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
DIST="dist"
LDFLAGS="-s -w -X main.version=${VERSION}"
PLATFORMS=(linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64)

echo "==> CoinRollHunter ${VERSION}"
rm -rf "$DIST"
mkdir -p "$DIST"

echo "==> Building UI (web/app -> web/dist)"
( cd web/app && npm ci --no-audit --no-fund && npm run build )

for p in "${PLATFORMS[@]}"; do
  GOOS="${p%/*}"; GOARCH="${p#*/}"
  ext=""; [ "$GOOS" = windows ] && ext=".exe"
  stage="$DIST/coinrollhunter_${GOOS}_${GOARCH}"
  mkdir -p "$stage"
  echo "==> Building ${GOOS}/${GOARCH}"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$stage/coinrollhunter${ext}" ./cmd/coinrollhunter
  cp README.md LICENSE "$stage/" 2>/dev/null || true

  archive="coinrollhunter_${VERSION}_${GOOS}_${GOARCH}"
  if [ "$GOOS" = windows ]; then
    ( cd "$DIST" && zip -qr "${archive}.zip" "coinrollhunter_${GOOS}_${GOARCH}" )
  else
    tar -czf "$DIST/${archive}.tar.gz" -C "$DIST" "coinrollhunter_${GOOS}_${GOARCH}"
  fi
done

echo "==> Checksums"
( cd "$DIST" && sha256sum coinrollhunter_*.tar.gz coinrollhunter_*.zip 2>/dev/null > checksums.txt || true )

echo "==> Done. Artifacts in $DIST/:"
ls -1 "$DIST"/*.tar.gz "$DIST"/*.zip "$DIST"/checksums.txt 2>/dev/null
