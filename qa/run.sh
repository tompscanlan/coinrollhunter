#!/usr/bin/env bash
# One-command end-to-end regression guard. Builds the binary (embedding the
# current UI), serves it against a throwaway DB, seeds a spot price, drives every
# "Do" tab workflow in a headless browser, then tears everything down. Exits
# nonzero if any check fails — wire it into CI or run before a release.
#
#   ./run.sh                 # build, serve, test, clean up
#   SKIP_BUILD=1 ./run.sh    # reuse an existing ../coinrollhunter binary
#   PORT=8901 ./run.sh       # use a different port
#
# Prereqs: Node 22+, a Chromium for Playwright. In this dev container ~/.cache is
# root-owned, so install browsers to a writable path and point Playwright at it:
#   export PLAYWRIGHT_BROWSERS_PATH=$PWD/ms-playwright
#   npx playwright install chromium
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd .. && pwd)"
PORT="${PORT:-8799}"
BASE="http://127.0.0.1:${PORT}"
BIN="${ROOT}/coinrollhunter"
DB="$(mktemp -u "${TMPDIR:-/tmp}/crh-qa-XXXXXX.db")"

if [[ "${SKIP_BUILD:-}" != "1" ]]; then
  echo "▸ building binary (embeds current UI)…"
  ( cd "$ROOT" && make build >/dev/null )
fi
[[ -x "$BIN" ]] || { echo "no binary at $BIN — run 'make build' or unset SKIP_BUILD" >&2; exit 1; }

echo "▸ installing qa deps…"
npm install --silent
npx playwright install chromium >/dev/null 2>&1 || echo "  (playwright install skipped — relying on PLAYWRIGHT_BROWSERS_PATH)"

echo "▸ serving on ${BASE} (db: ${DB})…"
# --spot-provider=none keeps the live gold-api.com poller out of the test path. Left on,
# it appends a spot row at as_of=now; LatestSpot is ORDER BY as_of DESC, so the live market
# price would outrank the 2026-01-01 fixture seeded below and the suite would value against
# whatever gold did today (a network race that can shift valuations mid-run). Manual seed only.
"$BIN" serve --db "$DB" --addr "127.0.0.1:${PORT}" --spot-provider=none &
SRV=$!
cleanup() { kill "$SRV" 2>/dev/null || true; rm -f "$DB"; }
trap cleanup EXIT

for _ in $(seq 1 40); do curl -sf "$BASE/api/health" >/dev/null 2>&1 && break || sleep 0.25; done

echo "▸ seeding spot price…"
curl -sf -X POST "$BASE/api/spot" -H 'Content-Type: application/json' \
  -d '{"as_of":"2026-01-01","gold_usd":4000,"silver_usd":60,"platinum_usd":1000,"palladium_usd":1100,"source":"qa"}' >/dev/null

echo "▸ running do-tab.e2e.mjs…"
BASE_URL="$BASE" node do-tab.e2e.mjs
