#!/usr/bin/env bash
# Cross-compile + package CoinRollHunter for every supported platform.
# Pure-Go SQLite (modernc) means CGO_ENABLED=0 cross-compiles cleanly with no
# per-OS toolchain. Output lands in dist/ as per-target archives + checksums.
#
#   ./scripts/release.sh                          # version from `git describe`
#   VERSION=v1.2.3 ./scripts/release.sh
#   PLATFORMS="windows/amd64" ./scripts/release.sh  # one target, for a test build
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
DIST="dist"
LDFLAGS="-s -w -X main.version=${VERSION}"
# Overridable so a test build can package a single target instead of waiting on
# all six — the archive it produces is byte-for-byte what a release would ship.
read -r -a PLATFORMS <<< "${PLATFORMS:-linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64}"

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

  if [ "$GOOS" = windows ]; then
    # Windows gets two binaries from the same source, because the subsystem is
    # baked into the executable and the two audiences need opposite things.
    #
    #   CoinRollHunter.exe  -H=windowsgui — no console window. Double-click and
    #                       the app appears. A console binary would flash a black
    #                       window that the user has to keep open, and closing it
    #                       kills their app. This is the one people click.
    #   cli/coinrollhunter.exe  console subsystem, for the subcommands. The GUI
    #                       binary cannot print to a terminal at all (no valid
    #                       std handles), so `serve`/`migrate`/`demo` need a real
    #                       console build or they run blind.
    #
    # The CLI copy lives in a subfolder so the top level of the archive has
    # exactly one obvious thing to click.
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$stage/CoinRollHunter.exe" ./cmd/coinrollhunter
    mkdir -p "$stage/cli"
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$stage/cli/coinrollhunter.exe" ./cmd/coinrollhunter
    printf '%s\r\n' \
      'Double-click CoinRollHunter.exe — it opens the app in your browser.' \
      '' \
      'Windows may warn that the publisher is unknown (the binaries are not' \
      'code-signed yet). Click "More info" then "Run anyway".' \
      '' \
      'Your data is saved to %LOCALAPPDATA%\CoinRollHunter\crh.db.' \
      '' \
      'cli\coinrollhunter.exe is the same app with a command line, for the' \
      'serve / migrate / demo subcommands.' \
      > "$stage/READ ME FIRST.txt"
  else
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$stage/coinrollhunter${ext}" ./cmd/coinrollhunter
  fi

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
