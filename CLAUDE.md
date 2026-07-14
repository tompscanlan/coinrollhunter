# CoinRollHunter — context for Claude Code / CLI sessions

You are continuing work on **CoinRollHunter**, a local-first tracker for coin roll
hunting (CRH) and precious-metals bullion. Read this first, then `docs/ADR-*` for the
full decisions.

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
to_redeposit = bought - returned - kept(finds+clad). Buyback haircuts: 40% (and 35% war
nickels) silver 0.80, 90% silver 0.90 — prefix-matched on the fineness string. Box throughput
is *derived* from normalized face (face / box_face[denom]), not an input (ADR-001 R7).
**Realized P&L:** sold lots (`disposed`/`disposed_usd`) are excluded from live valuation;
realized gain = proceeds − basis. **Per-box yield:** CRH finds link to their buy txn
(`lots.roll_txn_id`) → find realizable vs face searched, per box/bank.

**Tests use the committed fictional `sample-data/`, not personal numbers.** `internal/calc`
has two layers: change-proof *invariant* tests (accounting identities that hold for any
dataset) and a *worked-example* test whose expected values are derived inline from the sample
fixture. The math is free to evolve — update the worked example deliberately; nothing pins us
to an external oracle. (The owner's real holdings stay out of the repo entirely.)

## Data model — catalog/specimen split (ADR-003)
Storage splits into an `item_type` catalog (reference data: kind/name/metal/`fine_oz_each`/
fineness/year/mint/refs, entered once) and `lots` holdings (specimens that point at a type:
qty/gross_weight/purity/basis/premium/location/insured_value/attributes JSON/disposed, plus
`roll_txn_id` linking a CRH find to its box). Fine oz is *derived* (`fine_oz_each`, else
gross_weight×purity). (The catalog column was renamed `asw_oz`→`fine_oz_each` — metal-neutral;
"ASW" is silver-specific. Migrations run 0001→0006.) `calc` reads a flat resolved view
(`model.Lot` via `model.Resolve`), so the math is blind to the split. Other tables per ADR-001:
roll_txns, trips, supplies, keepers, spot, settings. **ADR-006 (migration 0006)** adds a find
taxonomy + an acquisition dimension: `roll_txns.source_type` (machine_roll/customer_roll/box/bag/
loose — orthogonal to `unit`), and `lots.category`/`subcategory`/`trophy` for CRH finds. These
feed `calc.ComputeFindsReport` (the "1 per face $" hit-rate view, `GET /api/finds-report`) and
the summary KPIs (`buy_count`/`branch_count`/`avg_buy_usd`).

## Next steps
1. [done] `go mod init`; layout `/cmd`, `/internal/{model,store,calc}`, `/web`.
2. [done] Port portfolio.py math to `internal/calc` (invariant + worked-example tests).
3. [done] SQLite schema + migrations (item_type + lots + the ADR-001 tables).
4. [done] `migrate` command: prototype JSON → SQLite (synthesize item_types; FIND*→activity=crh).
5. [done] REST API (CRUD for all tables; spot get/set; summary) + `go:embed` the Svelte build.
6. [done] Svelte UI: dashboard (verdict/cards/tables/reconciliation/throughput + inline spot) and
   spreadsheet-style **TanStack editable grids** for all data tables (Holdings flat-edits the
   ADR-003 split via find-or-create item_type). Stack: Svelte 5 + Vite + Tailwind v4 +
   shadcn-style components in `web/app`; built to `web/dist` (committed) and embedded.
7. [done] Cross-platform binaries: `scripts/release.sh` + `Makefile release` cross-compile
   linux/windows/darwin × amd64/arm64 (pure-Go, CGO_ENABLED=0); `.github/workflows/release.yml`
   publishes on tag; `.goreleaser.yaml` as an alternative.

Added 2026-06-29 (UX + record-keeping pass): catalog/preset **autofill** on the Holdings grid;
segmented **Overview/Entry** toggle; **Stack by coin type** (unified inventory); **Pt/Pd** spot
(migration 0003); **Sell / realized P&L** (partial sales split the lot; `POST /api/lots/{id}/sell`);
**per-find/per-bank/per-box yield** via `lots.roll_txn_id` (migration 0004) — the brief's "finds
per coins searched / which banks pay off" idea.

Added 2026-06-29 (ADR-006 + ADR-007, backend pass): a real hunter's CRH dataset (OKF
`projects/coinrollhunter/crh-field-data-dimes.md`) motivated **ADR-006** — acquisition
**source-type** on buys, a denom-scoped **find taxonomy** (`category`/`subcategory`), a **trophy**
flag (migration 0006), the **"1 per face $" hit-rate report** (`calc.ComputeFindsReport` →
`GET /api/finds-report`) with a low-confidence/sample-size signal and disposed-find survivorship,
and summary **KPIs**. **ADR-007** implements ADR-002's `SpotProvider` (`internal/spot`): a
keyless gold-api.com provider + a staleness-gated background **poller** started by `serve`
(`--spot-provider`/`--spot-interval`, opt-out with `none`; failures log + skip). UI surfacing of
these is the next pass (data model + calc + API + tests landed; grids/dashboards not yet wired).

Added 2026-07-01 (launch-polish pass): the **UI for ADR-006/007** shipped earlier (628b6e5);
this pass added the **settings editor UI** (SettingsPanel modal over GET/PUT /api/settings —
buyback factors, mileage, value-time+hourly, box face), the **`demo` command** (`internal/demo`
seeds a separate demo.db with ~15 months / ~$44k face / ~500 buys of deterministic fictional
data, then serves it; `--reset` regenerates; spot polling off), source-type rendered inert on
'return' roll-txn rows (EditableGrid `enabled` meta, om-kn0f), and web/dist un-committed
(gitignored build artifact, om-qbmm).

Remaining polish (not yet done): **junk-by-face** entry, **premium** in the Holdings grid,
**bars by gross-weight×purity** in the UI (incl. `weight_unit` in ResolveDataset),
**numismatic/collectible value**, the **keepers-vs-find-face double-count** seam, and
merchant-of-record monetization wiring. **ADR-004** — stack-over-time vs indexes
(gold:silver, S&P, CPI) — is deferred; the box-link + appended spot history are the data foundation.

Added 2026-07-12 (zero-friction launch, om-9p0l): the app is for people who do not open
terminals, and `coinrollhunter` with no args used to print usage and exit 2 — a double-click
was a console flash and nothing else. Now **no-arg = run the app**: it picks a database, serves,
and opens the UI (Chromium `--app=` window if Edge/Chrome is there, else the default browser;
a browser that won't start is logged, never fatal). The DB moved to a per-user data dir
(`%LOCALAPPDATA%`/`Application Support`/`XDG_DATA_HOME`) — **compat rule: an existing `crh.db`
in cwd still wins**, so upgrading never looks like data loss. `serveStore` now takes `serveOpts`
and binds its own listener, which buys single-instance (bind fails → probe `/api/health` → if
it's us, reopen the browser and exit 0; if it's a stranger, fall back to an ephemeral port) and
a race-free `onReady` for the browser. **Windows ships two binaries** from one source, because
the subsystem is baked into the PE: `CoinRollHunter.exe` (`-H=windowsgui`, no console — the one
you click) and `cli/coinrollhunter.exe` (console, for the subcommands; the GUI build has no
valid std handles and cannot print). No console ⇒ stdout/stderr go to a log file in the data
dir, fatal startup errors go to a `MessageBoxW` (pure `syscall`, no CGO), and the UI gets a
**Quit** button (`POST /api/quit`) since there is no Ctrl-C. All still `CGO_ENABLED=0`, all six
targets still cross-compile from Linux — which is exactly why Wails/webview_go were rejected:
they need CGO + a Windows toolchain and would cost us single-box releases. Still open:
code-signing (SmartScreen/Gatekeeper still warn).

Added 2026-07-12 (ADR-009 stable uids + backup, om-hdk5): `lots.id` / `roll_txns.id` are
bare rowid aliases — SQLite hands out `max(rowid)+1`, so a delete+insert **recycles** the
integer and a photo filed under it is silently adopted by a different coin. **Migration
0009** (`0009_stable_uids_photos.sql` — ADR-009 says "0008", but ADR-010/branches took that
number first) adds `uid` to `lots` + `roll_txns`, backfills them in pure SQL with the
`randomblob` UUIDv4 recipe 0008 proved (non-deterministic, re-evaluates **per row**), and
creates the **`photos`** table (arbitrary N per owner, `role`+`seq`, owned by a lot *or* a
roll_txn, path `photos/<owner_uid>/<photo_uid>.<ext>` — nothing mutable in the path).
**A UNIQUE index does not imply NOT NULL in SQLite**, so on the two ALTERed columns the
guarantee lives in Go (`newUID()` on all three insert paths — including the easy-to-miss
one where a *partial sale* carves out a new lot) plus the invariant tests in
`uid_test.go`. Don't "fix" this with `ALTER COLUMN … SET NOT NULL`: modernc accepts it but
it isn't SQLite grammar and would bind `crh.db` to one driver. `model.Holding.UID` /
`model.RollTxn.UID` are scanned as `NullString` and exposed on the API — export (om-9cua)
and photos (om-6hlp) are now unblocked. Also **`coinrollhunter backup DEST.db`** via
`VACUUM INTO`: one consistent self-contained file, safe on a *live* database (a plain `cp`
of `crh.db` can miss commits still in the `-wal` sidecar). It uses `store.BackupFile`, not
`Open`+`Backup`, because `Open` applies pending migrations — a backup must not upgrade the
thing it is preserving.

Build notes: `make build` (UI then Go). In this container, Go needs a writable cache —
`go env -w GOCACHE=/go/cache`. The UI build needs Node 22 + npm registry access. `web/dist`
is a git-ignored build artifact (only its `.gitkeep` is committed, so `go:embed all:dist`
always resolves) — a bare `go build` without `make ui` first serves an empty UI.

Added 2026-07-13 (PUT is a merge, om-kyq7): **the generic `PUT /api/<table>/{id}` no longer
replaces a row — it merges onto it.** This closes shipped data loss. The Holdings grid models
only some of a lot's columns, and `toHolding()` rebuilt the whole row from that flat view, so
editing *any* cell (a quantity!) wrote back an empty `notes`, a zero `insured_value`/`attributes`,
and blew away the `disposed`/`disposed_usd` of a lot that had been sold — resurrecting the sale.
`notes` has a real producer (`internal/legacy/import.go` — the spreadsheet on-ramp), so this hit
new users on their first correction, silently, with the destroyed field not even on screen.
The fix is structural rather than "carry every column in the client", which would re-break the
moment anyone adds a column: `api.register`'s PUT now decodes the body **onto the stored row**
(`decodeOnto`), so a client can only overwrite what it *names* — clearing a field still works,
you just have to say `"notes": ""`. `T` is constrained to **`model.Entity`** (an `EntityID()`
accessor) so the merge can fetch the row it addresses, which also means a new resource *cannot*
be registered back into full-replace semantics by accident. A merge is a read-modify-write, so it
runs under a new **store write lock** (`Store.WithWrite`; `SellHolding` and `MergeBranches` take
it too) — `SetMaxOpenConns(1)` serializes statements but not a SELECT-then-UPDATE pair, and a sale
committing in that gap would be undone by the merge's stale write-back. The invariant this all
rests on is pinned schema-driven in `internal/store/merge_invariants_test.go`: **the read path must
return every column the write path writes**, or a merge silently zeroes the difference.

Added 2026-07-13 (autocomplete actually suggests, om-rubx): **`web/app/src/lib/grids.ts` is now
`grids.svelte.ts`, and the suggestion caches are `$state`.** No autocomplete in the app had ever
offered a value the user typed — only the built-in presets. The caches (catalog / holdingSources /
holdingLocations / findCategories / banks / boxOpts) are filled by each grid's `load()`, i.e.
*after* mount; they were plain module-level `let`s, invisible to the renderer, and EditableGrid's
shared `<datalist>` sits under `{#each columns}` — a list that never changes — so the block was
evaluated exactly once, **before** `load()` resolved, and never again. Whatever was already
non-empty at module scope (the 7 `SILVER_PRESETS`, the 10 `FIND_CATEGORIES`) rendered; everything
else was empty forever. A probe of the old build: source `[]`, location `[]`, product = the presets
and nothing else. So ADR-006's "open vocabulary over your own entries" was, in practice,
presets-only, in **six grids at once** — and the Bank field's whole purpose (nudge reuse of a
branch you have instead of forking a new one on a typo) was silently not happening. Reactive state
is the fix rather than having the template touch an unrelated reactive var to fake a dependency:
that line reads like dead code and dies in the first cleanup, taking every autocomplete with it.
**Note for new grid work:** a module that holds state the UI must react to has to be `.svelte.ts`,
or the renderer cannot see it change. The `boxOpts` select survived only by accident — it renders
inside the row loop, which re-runs when the rows land.

Added 2026-07-14 (the loopback guard, om-6ex5): **binding to 127.0.0.1 is not an
authentication boundary** — it only means the attacker has to be a webpage instead of a
stranger on the internet. The API has no auth, and the reason CORS did not save us is
`decode()` (`internal/api/api.go`): it never inspects Content-Type, so a hostile page can
send a JSON body as `Content-Type: text/plain` — CORS-safelisted, therefore a *simple*
request that never preflights — and Go parses it happily. That put **every POST route** in
reach of any tab the user had open: `/api/quit`, `/api/lots/{id}/sell`,
`/api/branches/{id}/merge`, `/api/spot`, and the eight generic creates. The response is
unreadable cross-origin, but the *write lands*, and ids are dense integer rowids, so a
blind `for id in 1..500: sell` loop is practical: silent, irreversible corruption of a
financial ledger. (PUT/DELETE were only ever safe by accident — they always preflight.)
The fix is **`api.Guard`** (`internal/api/guard.go`), and **it wraps the OUTER mux in
`cmd/` (`appHandler`), not `api.Handler`** — `POST /api/quit` is registered in the command,
so a guard inside the API package would miss the one route that kills the process. Three
rules, each load-bearing: a **missing Origin is ALLOWED** (curl, `instanceAt`'s Go probe,
and the Node-side `qa/` fetches send none — and a browser *cannot* suppress Origin on a
cross-origin request, so this costs nothing against the actual adversary); the
**Origin/Host hostname must be loopback**; the **port is never pinned** (the ephemeral-port
fallback moves it, `qa/run.sh` takes a `PORT`, and the Vite dev proxy forwards
`Origin: http://localhost:5173`). The **Host** half is the DNS-rebinding defense — the only
path to actually *reading* the ledger — which is why it applies to GET too. **No per-launch
token**, deliberately: a token only defends against a local non-browser process, which can
already read `crh.db` off disk, and it would have cost an index.html injection seam and
broken the e2e gate. Also: a **non-loopback `--addr` is now refused** unless
`--unsafe-network` is passed (`checkAddr`), which binds and prints a loud warning and
relaxes the *Host* check to the bound address — but still refuses cross-origin requests.
**If a guard change makes you want to edit `qa/`, the guard is wrong.**

Added 2026-07-14 (server-side validation, om-1czp): the API used to decode a JSON body
**straight into the store with nothing between decode and insert** — so a typo, an editable
grid, or a direct curl could land a record that *can't be true* (a negative basis, an unknown
metal, an unparseable date) and then silently poison every downstream number. Now
**`internal/model/validate.go`** gives every mutable type a field-level `Validate()`, and the
**store is the single chokepoint** that calls it: all 8 `Insert*`, all 8 `Update*` (the PUT
merge was unvalidated too, not just create), `SellHolding`, `PutSpot`, `PutSettings` — so
every writer (API, demo seeder, legacy import, spot poller) is covered by construction. A
`model.ErrInvalid` maps to **HTTP 400** with the field-named message the grids already render
and revert on; nothing else in `writeErr` changed. **Scope is money-corruption only, tied to a
consequence read off `calc`**: the four enums whose bad value silently corrupts money — `metal`
(→ $0 via `spotFor`), `roll_txns.action` (→ the txn vanishes from the buy/return switch),
`lots.weight_unit` (→ ~31× fine-oz error), `lots.activity` (→ invisible to every report) —
plus non-negative money/counts, `purity` 0..1, and ISO dates on the required/optional date
fields. The **open vocabularies stay open** (ADR-006: category/subcategory/source/location,
losses.reason/scope, supplies.item, item_type.kind, denom/unit/source_type), **blank metal /
blank weight_unit / 0 purity are legal** (clad junk types, derived fine oz), and **`spot.as_of`
is left alone** (the poller writes RFC3339, not ISO). **No migration, no CHECK constraint, no
schema change** — deliberately: it was *proven* that adding a CHECK to an existing table makes
any user database holding one pre-existing bad row **fail to open** (the rebuild's
`INSERT...SELECT` aborts inside the migration's transaction), which is strictly worse than the
bug. `internal/store/validate_ast_test.go` walks the package AST and **fails if a future
mutation reaches the DB without a `Validate*` call** (so the next writer can't quietly reopen
the hole), and the **brick test** (`TestExistingBadDataStillOpens`) pins that a DB carrying raw
bad rows still opens, lists, and lets you delete them. The DB-level backstop (TRIGGERs, not
CHECK) and a `doctor` command to report pre-existing bad rows are deliberately **separate
follow-up beads**. No frontend change (`api.ts` + `EditableGrid.svelte` already render `{error}`).

The `prototype/` reference is the source of truth for behavior and exact formulas.
