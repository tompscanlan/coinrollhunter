# CoinRollHunter — context for Claude Code / CLI sessions

You are continuing work on **CoinRollHunter**, a local-first tracker for coin roll
hunting (CRH) and precious-metals bullion. Read this first, then `docs/ADR-001` and
`docs/ADR-002` for the full decisions.

## Goal
Turn a working prototype into a shippable, share-ready app: super easy to run,
cross-platform (Windows/Linux/macOS), with spreadsheet-style data entry/editing,
flexible purchase units, and live spot prices. Sold pay-what-you-want (suggested $5).

## Decisions (locked — see docs/ for rationale)
- **Architecture:** single Go binary that embeds the web UI (`go:embed`) and serves
  localhost; SQLite datastore via pure-Go `modernc.org/sqlite` (no CGO). Same code can
  run hosted later (phase 2).
- **Frontend:** Svelte 5 + Vite + Tailwind + shadcn-svelte; TanStack Table (Svelte) for
  editable grids; LayerCake or uPlot for the spot-history chart. Build static, embed in Go.
- **Spot prices:** free-tier metals API called behind a `SpotProvider` interface; cache via
  a central proxy only if scale demands. Manual entry is the permanent offline fallback.
- **Monetization:** pay-what-you-want via merchant-of-record (Lemon Squeezy or Polar). No
  license enforcement.

## Conventions
- Backend/app code: **Go**. Tooling/scripts: **Python**. UI: TypeScript/Svelte.
- **Never commit personal data.** Real `pm_holdings.json` / `crh_ledger.json` / `*.db` are
  git-ignored; ship only fictional `*.sample.json`.
- Secrets in `.env` (git-ignored); document in `.env.example`.

## Data model (target SQLite — see ADR-001)
Tables: lots (bullion + CRH finds, `activity` column), roll_txns (with denom + flexible
`unit`: box/roll/face/coin normalized to `face_usd`), trips, supplies, keepers, spot, settings.
Every row has an id so the UI can edit/delete.

## Profitability math (port from prototype/portfolio.py; pin these in tests)
Using the owner's real data the prototype produced: **CRH net (cash, realizable) = $103.81**,
**bullion unrealized = -$1,546.43**, **to-redeposit = $163.50**, boxes = 3.1 (2.1 halves +
1 quarters). Buyback haircuts: 40% silver 0.80, 90% silver 0.90. CRH net = finds realizable
- face cost - gas - supplies. Cash-in: to_redeposit = bought - returned - kept(finds+clad).

## Next steps (Phase 0–1 from ADR-001)
1. `go mod init`; layout `/cmd`, `/internal/store`, `/internal/calc`, `/web`.
2. SQLite schema + migrations; `migrate` command to import a user's JSON.
3. Port portfolio.py math to `internal/calc` with unit tests pinned to the numbers above.
4. REST API (CRUD for all tables; spot get/set; summary) + `go:embed` the Svelte build.
5. Svelte UI: dashboard (cards/verdict/reconciliation/chart) + editable grids.
6. goreleaser + GitHub Actions for per-OS binaries.

The `prototype/` reference is the source of truth for behavior and exact formulas.
