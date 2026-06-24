# Resume prompt — CoinRollHunter (paste into Claude Code)

Pick up development on any machine. Everything you need is in this git repo.

## 0. Get the code + run it
```bash
git clone https://github.com/tompscanlan/coinrollhunter.git
cd coinrollhunter
make build        # builds the Svelte UI (web/app -> web/dist) then the Go binary
# optional: load the fictional sample data so the dashboard isn't empty
./coinrollhunter migrate \
  --holdings prototype/sample-data/pm_holdings.sample.json \
  --crh prototype/sample-data/crh_ledger.sample.json
./coinrollhunter serve     # open http://127.0.0.1:8787
claude            # start Claude Code in the repo root
```
In-container note: Go needs a writable cache — `go env -w GOCACHE=/go/cache`. UI build needs Node 22 + npm.

## 1. Paste this into Claude Code

---

You are resuming **CoinRollHunter**, a local-first tracker for coin roll hunting (CRH) and
precious-metals bullion. The repo is the complete source of truth.

### Where we are (as of 2026-06-24)
**Phases 0–3 are built, tested, and (this session) committed to `main`.** Read `CLAUDE.md`,
then `docs/ADR-001/002/003` for the locked decisions.

- **Backend (done):** single Go binary. `internal/{model,calc,store,legacy,api}` — profitability
  engine (faithful `portfolio.py` port), pure-Go SQLite (`modernc.org/sqlite`, no CGO), prototype
  JSON importer, JSON REST API. `cmd/coinrollhunter` has `migrate`, `serve`, `version`.
  `go vet` + `go test ./...` green.
- **UI (done, NEEDS BROWSER QA):** Svelte 5 + Vite + Tailwind v4 + shadcn-style components +
  **TanStack `table-core`** in `web/app`. Built to `web/dist` (committed) and embedded via
  `web/embed.go` (`go:embed all:dist`); `internal/api` serves it with an SPA fallback.
  - `App.svelte` — two top-level tabs: **Dashboard** and **Data**. Data entry is under the
    **Data** tab (sub-tabs: Holdings / Roll txns / Trips / Supplies / Keepers).
  - `lib/components/Dashboard.svelte` — verdict banner, stat cards, bullion + finds tables,
    reconciliation, box throughput, inline spot updater.
  - `lib/components/EditableGrid.svelte` — generic spreadsheet-style grid: inline-edit cells,
    sortable headers, per-row delete, an always-present add-row; persists to the CRUD API.
  - `lib/grids.ts` — column specs + wiring per table. **Holdings** is a flat sheet that hides the
    ADR-003 catalog/specimen split: create/update find-or-create the `item_type` then attach the
    holding (`ensureItemType`).
  - `svelte-check` is clean; the production bundle builds with no warnings.
- **Binaries (done):** `scripts/release.sh` + `make release` cross-compile linux/windows/darwin ×
  amd64/arm64 (CGO_ENABLED=0) into `dist/` as archives + `checksums.txt`. `.github/workflows/
  release.yml` publishes a draft GitHub Release on a `v*` tag; `.goreleaser.yaml` is an alternative.
  v0.1.0 archives were built and a packaged binary was verified to serve UI + API.

### >>> START HERE: the one open item <<<
The owner ran the app, said the dashboard "looks ok," but **had not seen the data entry**. It was
never confirmed in a real browser (the dev box had no headless browser). **First task: open
http://127.0.0.1:8787, click the "Data" tab, and verify the editable grids render and work**
(add a row, edit a cell, delete a row, watch the Dashboard totals update).

If the grids DON'T render/work, debug in this order:
1. Browser console for runtime errors in `EditableGrid.svelte`.
2. The TanStack integration: `createTable` uses getter-based options + a `$derived` (`view`) that
   calls `table.setOptions(...)` then reads `getRowModel()/getHeaderGroups()`. Confirm rows appear
   and sorting works; this lock-step pattern is the riskiest part.
3. `bind:value={row.original[key]}` (dynamic member bind to a Svelte 5 `$state` proxy element) —
   confirm edits actually mutate state and fire `onchange`.
4. Discoverability: if it works but was just hard to find, consider surfacing data entry better
   (e.g. land on Data when the DB is empty, or merge the dashboard's spot panel cue).

For fast UI iteration: run `./coinrollhunter serve` and `cd web/app && npm run dev` (Vite proxies
`/api` to :8787), so you get hot reload without rebuilding the Go binary.

### After that — remaining polish (see CLAUDE.md "Next steps")
- Live spot-price feed behind the `SpotProvider` interface (manual entry is the offline fallback now).
- Per-find / per-bank success tracking — the brief's "finds per coins searched, which banks/boxes
  pay off" idea (currently only aggregate throughput is tracked).
- A `--demo` seed dataset; spot-history chart (ADR-002 mentions LayerCake/uPlot); merchant-of-record
  monetization wiring.

### Rules
Go for app code, TypeScript/Svelte for UI, Python only for tooling. **Never commit personal data**
(`pm_holdings.json`, `crh_ledger.json`, `*.db` are git-ignored; ship only `*.sample.json`). Tests
use the committed fictional `sample-data/`. Commit signed to `main`. Ask before destructive steps.

Start by running the app and reporting whether the Data-tab grids work, then propose the next step.

---
