# qa — end-to-end regression guard

A headless-browser ([Playwright](https://playwright.dev)) smoke + workflow test
for the CoinRollHunter UI. It drives every **Do**-tab workflow against a running
`coinrollhunter serve` and asserts both the on-screen flow and the rows it
records (via the REST API). It fails on **any** browser console/page error — the
kind of crash that once shipped silently (a Data-tab `columnPinning` throw; an
empty-DB `box_yields` null) is caught here.

## Suite layout

`do-tab.e2e.mjs` is the thin runner. It executes focused, stateful scenarios in
`e2e/` against one throwaway database:

- `workflows.mjs` — the six Do-tab actions and their accounting effects.
- `reporting-photos.mjs` — reports, photos/documents, export, and settings.
- `grid-persistence.mjs` — grid round-trips, merge safety, and autocompletes.
- `grid-behavior.mjs` — sold-row cues, virtualization, and delete confirmation.
- `support.mjs` — shared API, navigation, polling, and assertion helpers.

## What it covers

- **Bought a box** → `roll_txn(buy)` + optional Trip; denom×unit face auto-fill.
- **Logged finds** → CRH Holdings + clad Keepers attributed to their box.
- **New coin / bullion** → bullion Holding (find-or-create item_type), including
  acquisition-date premium suggestion and a persisted manual override.
- **Returned to bank** → `roll_txn(return)` against the float.
- **Reconcile / close out** → record forgotten inventory, then book a loss;
  float → \$0 and CRH net drops by the loss (ADR-005).
- **Sold** → `POST /lots/{id}/sell` with realized-gain check.
- Edit-layer **Losses** grid round-trips; Overview reconciliation banner.

## Run it

```sh
# from repo root or this dir:
cd qa
./run.sh          # builds the binary, serves a throwaway DB, runs, tears down
```

`run.sh` knobs: `SKIP_BUILD=1` (reuse `../coinrollhunter`), `PORT=8901`.

### Browser prereq (dev container)

`~/.cache` is root-owned in the dev container, so install Chromium to a writable
path and point Playwright at it before running:

```sh
sudo npx playwright install-deps chromium
export PLAYWRIGHT_BROWSERS_PATH="$PWD/ms-playwright"
npx playwright install chromium
```

`node_modules/` and `ms-playwright/` are git-ignored.

## Run against an already-running server

```sh
BASE_URL=http://127.0.0.1:8787 node do-tab.e2e.mjs
```

The script assumes a freshly-migrated DB with a spot price seeded (that's what
`run.sh` sets up). Pointing it at a populated DB may trip the row-count
assertions.
