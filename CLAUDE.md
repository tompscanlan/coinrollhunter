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

## Profitability math (port from prototype/portfolio.py)
Core formulas: CRH net = finds_realizable - face_cost - gas - supplies. Cash-in:
to_redeposit = bought - returned - kept(finds+clad). Buyback haircuts: 40% silver 0.80,
90% silver 0.90. Box throughput is *derived* from normalized face (face / box_face[denom]),
not an input (ADR-001 R7).

**Tests use the committed fictional `sample-data/`, not personal numbers.** `internal/calc`
has two layers: change-proof *invariant* tests (accounting identities that hold for any
dataset) and a *worked-example* test whose expected values are derived inline from the sample
fixture. The math is free to evolve — update the worked example deliberately; nothing pins us
to an external oracle. (The owner's real holdings stay out of the repo entirely.)

## Data model — catalog/specimen split (ADR-003)
Storage splits into an `item_type` catalog (reference data: kind/name/metal/asw_oz/fineness/
year/mint/refs, entered once) and `lots` holdings (specimens that point at a type:
qty/gross_weight/purity/basis/premium/location/insured_value/attributes JSON/disposed).
Fine oz is *derived* (asw_oz, else gross_weight×purity). `calc` reads a flat resolved view
(`model.Lot` via `model.Resolve`), so the math is blind to the split. Other tables per ADR-001:
roll_txns, trips, supplies, keepers, spot, settings.

## Next steps (Phase 0–1 from ADR-001/003)
1. [done] `go mod init`; layout `/cmd`, `/internal/{model,store,calc}`, `/web`.
2. [done] Port portfolio.py math to `internal/calc` (invariant + worked-example tests).
3. SQLite schema + migrations (item_type + lots + the ADR-001 tables).
4. `migrate` command: prototype JSON → SQLite (synthesize item_types; FIND*→activity=crh).
5. REST API (CRUD for all tables; spot get/set; summary) + `go:embed` the Svelte build.
6. Svelte UI: dashboard (cards/verdict/reconciliation/chart) + editable grids.
7. goreleaser + GitHub Actions for per-OS binaries.

The `prototype/` reference is the source of truth for behavior and exact formulas.
