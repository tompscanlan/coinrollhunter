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
nickels) silver 0.80, 90% silver 0.90 — classified by *range* over a fineness string normalized to
a numeric fine fraction, never by prefix-match (om-t0fs). Box throughput
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

Added 2026-07-14 (the import is atomic, om-u3el): **`internal/legacy/import.go` wrote with no
transaction** — and that only became a user-visible wound when om-1czp made every store mutation
validate. The first rejected row aborted the run mid-stream, every row *ahead* of it was already
committed (each `s.Insert*` is its own auto-commit statement), so the user fixed their file, re-ran,
and **every previously-inserted row was DUPLICATED**. `settings` and `spot` are upserts, so a
*rejected* file could still silently rewrite the user's settings; and `resolveBranchID` — the store's
**hidden 7th writer**, which find-or-creates a branch from a typed bank name inside `InsertRollTxn`/
`InsertTrip` — left an **orphan bank branch** behind. This is the spreadsheet on-ramp: it burned the
one population we most want to keep, on their first interaction.
**`Store.WithWrite` is not the fix — it is a MUTEX, not a transaction** (no Begin/Commit/Rollback;
wrapping the import in it buys *zero* atomicity while looking right). And the obvious fix —
`s.db.Begin()`, then keep calling the existing `s.Insert*` — **deadlocks**: `SetMaxOpenConns(1)`
(SQLite tolerates one writer) means the open tx holds the pool's only connection and the next
`s.db.Exec` blocks **forever**. The symptom is a hung test, not an error, and the tempting "fix" is
to delete the transaction again. **There is no way to do this without making the insert path
tx-aware.** So: **`Store.WithTx(fn func(*Tx) error)`** runs the `wmu` + `Begin` + `defer Rollback` +
`Commit` dance (the idiom `SellHolding` and `MergeBranches` already used) and hands `fn` a
transaction-bound **`*Tx`** writer; each insert's SQL now lives in one private helper over a small
**`execer`** interface (`{Exec; QueryRow}`) that **both `*sql.DB` and `*sql.Tx`** satisfy, so the
auto-commit `*Store` method and its `*Tx` twin cannot drift. `resolveBranchID` takes an `execer` too,
so a branch forked inside a tx rolls back with it. **The 19 public `Store` mutations keep their names,
signatures, and their own `x.Validate()` first line** — that repetition is deliberate:
`validate_ast_test.go` reads each mutation's own **body**, so folding the call into the shared helper
would blind the chokepoint guard. **The guard itself is widened**: it used to walk only `*Store`
receivers, which would have made the whole `*Tx` insert path **invisible to it** — a second,
*unvalidated* door into the ledger, precisely the hole om-1czp closed. It now walks **every** receiver,
keys mutations `Receiver.Method`, and fails on an **undeclared** one, so the next writer cannot escape
by inventing a new receiver either. On top of the tx, `Import` gained a **pre-validate pass** (plan →
validate → write): it builds every `model` struct first, runs every validator, and reports **EVERY**
bad row at once (`legacy.ImportErrors`, unwrapping to `model.ErrInvalid`), each naming its file, row
index and field — `migrate` prints the lot. **The two halves are not redundant**: the pre-validate is
the UX (fix your whole file in one pass), the **transaction is the guarantee**, because it also covers
what validation cannot foresee — a missing table, a full disk, an FK, a process kill. Pre-validation
alone would still half-write at row 400 of 900. **`*Tx` is the surface om-2sl6's compound workflows
consume**; note it does not carry `SellHolding` or the `Update*`s yet — add them the same way (a twin
that validates in its own body + an entry in `expectedMutations`), and never inside a `WithTx` callback
call a `*Store` method, or you will meet the deadlock above.

Added 2026-07-14 (classification is money, om-t0fs): `internal/calc` used to read the
**stringly-typed `metal` and `fineness` straight into the money math**, and both halves failed
**silently and in the flattering direction**. `buybackFactor` **prefix-matched** the fineness, so
`".900"` — ordinary numismatic notation — missed `HasPrefix("90")`, fell to the default, and paid
**full melt with no dealer haircut** (~11% overstatement of expected payout); `" 90%"` lost to a
leading space the same way; and `"40 grain"` — a **weight** — matched `HasPrefix("40")` and got
haircut as 40% junk. Meanwhile `spotFor` was an **exact, case-sensitive** switch, so a historical
row with `metal="Silver"` valued at **$0 spot**. Those two compound: a `"Silver"` row also fails
`l.Metal != "silver"` in `buybackFactor`, so it skipped the haircut *as well* — **both halves of
the money math wrong on the same row**. om-1czp's validation does not close this and was never
going to: it guards **new writes only** (historical rows, `internal/legacy` imports and hand-edited
DBs never pass through it) and it **does not validate fineness at all**. The parser is the only
defense these rows ever meet, so the parser is where the fix lives — deliberately **not** a new
CHECK or a new validation rule. Now `normalizeMetal` folds case+whitespace (and the `gold` KPI
guard uses it too, or a `"Gold"` lot would price at gold spot yet vanish from `gold_*`), and
`finenessFraction` normalizes free text to a **numeric fine fraction**, which is then classified by
**RANGE** (`near`, ±0.015 — wide enough for `".900"`/`"90%"`/`"900"`, tight enough that sterling
`.925`, 22k `.9167` and world `80% (CAD)` stay **out** of the junk buckets and keep full melt, as
they did before). **No `strings.HasPrefix` on fineness survives.** The rule that makes `"40 grain"`
work is worth keeping in your head: a **bare** number (no `%`, no decimal point, no karat) is a
fineness only if it **stands alone**; a number carrying any other unit is a weight. A number that
*is* explicitly marked may carry annotation, which is why `"80% (CAD)"` (a real fineness in
`internal/demo`) still parses. An **unparseable** fineness on silver now takes the **conservative**
branch — the worst known dealer factor — because the old `1.0` meant *full melt*, i.e. bad data
silently produced the **most optimistic number available**. What does not resolve is recorded on a
**new additive `Report.Anomalies`** (row + offending value): purely additive, so the API carries it
and existing consumers ignore it — `/api/summary` over `sample-data` is **byte-identical** before
and after except for the new `"anomalies": []`. **Rendering it is om-ay3b, not this bead.** The
trap to not fall into: **a BLANK metal is legal and load-bearing** — clad "junk" types (error
coins, world coins) carry no melt metal, correctly value at $0, and must stay **silent**; flagging
blank would fire on correct data across a huge share of the ledger, which is how a warning becomes
noise. Only a **non-blank, unrecognized** metal is an anomaly. Blank *fineness* is treated the same
way (not stated ≠ corrupt): no haircut, no anomaly.

Added 2026-07-14 (box/branch links ride the stable uid, om-c8ei): `roll_txns.id` and
`branches.id` are bare rowid aliases, so SQLite hands out `max(rowid)+1` and a delete+insert
**recycles** the integer — after which a NEW box **silently adopts the dead box's finds and
keepers**, and an old trip re-parents onto the *replacement* bank. Right rows, wrong parent,
no error; and it is on the **happy path of fixing a mistake** (delete your newest box, enter
another — the exact om-lv4q correction workflow). The four durable links that stored that
recyclable integer — `lots.roll_txn_id`, `keepers.roll_txn_id`, `roll_txns.branch_id`,
`trips.branch_id` — now store the never-recycled **uid** instead (`*_uid` columns, **migration
0011**, hard cutover: the integer columns are dropped, 0008's precedent — reversibility is a
backup, not a dual-read window). This is **store-internal (Shape A)**: the store resolves
id→uid on **write** (the box demonstrably exists in that statement) and uid→id on **read** (a
`LEFT JOIN` back to the row's *current* id), so `model`/`calc`/`api`/`web` keep seeing the same
integer `RollTxnID`/`BranchID` on the wire — now always the **right** box, never a recycled
rowid. A deleted box/branch leaves the child's uid dangling, so it resolves to **blank**;
**blank beats wrong**. Unknown box id on write ⇒ **NULL**, not a 400 (a 400 would give the
legacy import a new failure mode on the new-user on-ramp). Three landmines, each load-bearing:
(1) **NOT a foreign key.** FKs *are* enforced on this connection (`store.Open` opens with
`_pragma=foreign_keys(1)`; `lots.item_type_id` is a live one), so a key on the new columns would
**fire and brick** every user DB that already holds an orphan — the same mechanism om-1czp
proved for a `CHECK`. The uid model needs none: a never-recycled key that dangles is merely
blank. No "box must exist" validator either — that is a FK by another name. (2) **Backfill
before repoint.** `roll_txns.uid` has no schema not-null (0009; a UNIQUE index does not imply
one), so 0011's **first** statement re-runs the `randomblob` uid backfill `WHERE uid IS NULL`,
or a null-uid box would blank every child that pointed at it. (3) **The partial-sale carve-out**
(`SellHolding`) copies the box link onto the NEW lots row it carves out — miss it and every
partially-sold find loses its box (the trap ADR-009 named). **Export** inverts by the same logic
(`internal/export`): the uid becomes the REAL column and the integer is DERIVED by resolving it
back, so `lots.csv`/`keepers.csv`/`roll_txns.csv`/`trips.csv` keep **both** columns and the
bundle shape is unchanged. `branch_aliases.branch_id` **stays an integer** — it cannot orphan
(deleted in the same transaction as its branch, repointed by `MergeBranches` before the loser
is removed). **What this fix CANNOT do, stated plainly:** a database that was **already**
re-adopted before 0011 carries a link that resolves to the WRONG box today, and the repoint
**freezes it there permanently** — nothing can distinguish it from a correct link, because the
child never recorded a uid and the deleted box left no tombstone. The migration **launders
existing corruption into permanent, invisible corruption**; there is no honest repair (a
matching heuristic throws false positives on legitimate data and would silently unlink correct
rows, which is worse). The fix **stops NEW re-adoption; it cannot undo the old.** A doctor
command that reports the blanked (true-orphan) class is a separate follow-up bead.

Added 2026-07-14 (om-c8ei follow-ups F1/F4, Tom-adjudicated). **F1 — a blank/0 box link on
`Update*` PRESERVES the stored `*_uid`.** A find whose box was deleted reads back
`RollTxnID 0` (and a merged-away branch reads back `Bank ""`); before this, editing any
*other* cell re-resolved that 0/blank to NULL and **erased the dead box's uid** — the exact
forensic orphan trace export now keeps. So `UpdateHolding`/`UpdateKeeper`/`UpdateRollTxn`/
`UpdateTrip` now leave the stored uid **unchanged** when the incoming link is blank (integer
`0`, or an empty bank name), mirroring `SellHolding`; a **nonzero** id still resolves (D3:
unknown → NULL). Tradeoff Tom accepted: a genuine **"clear the box link"** also arrives as
`0`, so it is **no longer expressible over the integer wire** and is out of scope (the wire
carries the resolved integer, and there is no distinct "unset" value). Implemented as a
one-param `CASE WHEN ?=1 THEN col ELSE ? END` at each update seam; pinned by
`TestUpdatePreservesOrphaned{Box,Branch}Uid` (fail before, pass after) and D3 is pinned on
the write path by `TestWriteWithUnknownBoxIsStoredAsNull`. **F4 — the export CSV column
ORDER changed for the link columns** (accepted + documented, no refactor): `lots.csv`,
`keepers.csv`, `roll_txns.csv` and `trips.csv` still carry **both** the id and uid link
columns, but the inversion made the **stored uid** take the slot the integer used to hold and
**appended the derived integer** after the other derived columns (e.g. `lots.csv` ends
`… item_type_uid, roll_txn_id` where the uid now sits mid-row). Every other column is
byte-identical. **Consumers must key by header NAME, not column position** — which the bundle
already invites (row 1 is the header, and the whole point of the uid columns is name-based
joins). The set-based coverage guard (`TestBundleCoversEveryColumn`) and the orphan-export
shape (`TestUIDLeadsAndForeignKeysResolveToUIDs`, which now seeds a real orphan) pin this.

Added 2026-07-14 (a `kept` flag on the find, om-5psc): the **keeper/find double-count**
this app self-flagged is now closed **structurally**. One physical coin could be entered as
BOTH a CRH find (a `lots` row, `activity='crh'`) AND a keeper batch — the same LoggedFinds
submission accepted both — and `kept_face` then counted its face **twice** (bought $500,
returned $499.50, one $0.50 half entered twice → `kept_face` $1.00, `to_redeposit` −$0.50
instead of $0.50/$0.00). The fix is **model (a): a `kept` flag on the find.** A find you keep
is **one flagged find row**, never a find plus a duplicate keeper; the keeper table is now
**clad-only by convention**. **Migration 0012** (`0012_kept_flag.sql`) is the trophy pattern
exactly: `ALTER TABLE lots ADD COLUMN kept INTEGER NOT NULL DEFAULT 0`, stored via `b2i()`,
scanned via `kept != 0`, `Kept bool` on `model.Holding`/`model.Lot` (`Resolve` copies it),
plumbed through `InsertHolding`/`UpdateHolding`/`ListHoldings`/`ResolveDataset` and the export
lots columns. **Three things this bead deliberately did NOT do, each load-bearing:**
**(1) `internal/calc` is a ZERO diff.** The bug was the second **row**, not the formula:
`keptFace` already counts a find's face **exactly once** via `fCost`, kept or not — a find is
coin pulled off the search table, its face belongs on the kept side **regardless of intent**.
Gating `keptFace`'s find term on the flag would drop an *unflagged* find out of `kept_face`,
**inflating** `to_redeposit` (telling the user to redeposit coin in their pocket) — a worse bug,
and it would force weakening the ADR-008 identities. So `kept` records **data-entry intent, not
accounting**; it is **math-neutral**, and the headline verdicts (`crh_net_*`, `total_basis`) are
**bit-identical** buggy-vs-correct (delta 0, so om-nass is untouched — `TestKeptFindNoDoubleCount`).
**(2) The migration is ADDITIVE-ONLY and repairs NOTHING** — zero existing keeper rows touched.
Automatic collapse of *existing* duplicates is unsafe and stays out: a keeper is a **batch, not
a coin** (its $0.50 is inside a larger total, no pair to collapse); the match key is **empty** for
the populations that matter (the legacy import + demo seeder fan clad into one row per denom with
NO date, NO box; every pre-0007 row is NULL); and box+date co-location is the **signature of a
correct entry**, so a detector false-positives on good data — and a false positive is **silent,
unrecoverable** money loss (no undo, om-lv4q). Repairing pre-existing duplicates is a
**user-adjudicated** in-app step (a separate tandem bead, om-cqmp), never a migration/boot action.
**(3) The partial-sale carve-out** (`SellHolding`, `internal/store/data.go`) hand-enumerates every
`lots` column into the new disposed row — `kept` rides across it exactly like `trophy`, or a
partially-sold kept find would silently **un-keep** the carve-out (the om-hdk5 trap, no guard).
LoggedFinds gained a **"Keep"** checkbox on the find row (defaults on) and its keeper section is
clad-only; a kept notable find is one flagged find, and the submission can no longer write a keeper
for a coin it just logged as a find (pinned in `qa/do-tab.e2e.mjs`, AC2). ADR-008's §Alternatives
rejection of a keeper↔lot dedupe is **vindicated, not overturned** — we do not dedupe.

Added 2026-07-14 (compound workflows are atomic, om-2sl6): every user-visible "action"
in the Do tab used to be a **sequence of independent POSTs from the browser**, with no
transaction spanning them — "Bought a box" (roll_txn + optional trip), "Logged finds"
(roll_txn + N×(item_type + lot) + M×keeper), and every catalog+holding write (NewBullion,
Reconcile.addFind, AND the Edit-tab Holdings grid on every row create/update). A failure
after the first left the ledger **half-written with no undo**, and the user's natural
response — re-press the still-populated form — **duplicated** the part that succeeded (a box
you paid for once, entered twice). This is a partial-failure bug, not a concurrency one, so
"single-user app" is no defense. The fix **moves the seam**: one endpoint per compound
action, one transaction inside it, copying the `SellHolding`/`MergeBranches` idiom the
codebase already trusted. **New composite store methods** live in `internal/store/compound.go`
— `RecordPurchase`, `RecordFinds`, `RecordHolding`, `ReviseHolding` — each opening ONE
`Store.WithTx` and composing the tx-bound, self-validating mutations om-u3el already built
(`Tx.Insert*`), so a failure at ANY step rolls back everything. They are named Record*/Revise*
(not Insert*/Update*) on purpose: they are **orchestrators, not leaf writers** — every actual
write still funnels through a guarded `Tx` twin that validates in its own body, so the
compound is a lock around existing doors, not a new one, and the AST chokepoint guard
(`validate_ast_test.go`) stays honest. **This USED om-u3el's execer/`WithTx` surface rather
than rebuilding it** — the DECISION the stale contract asked for ("thread an execer through
the Insert\* funcs") was already done; branch resolution (`resolveBranchUID`) already takes an
execer, so it runs **inside** the compound tx and a rolled-back box leaves **no orphan bank
branch** (the 18:47 seam-f pitfall — proven by counting `branches` before/after, not just
asserting a 400). Two new `Tx` twins were added the way om-u3el's note prescribes
(`Tx.UpdateItemType`, `Tx.UpdateHolding`, each validating + declared in `expectedMutations`)
so the holdings-with-type UPDATE can find-or-create-then-refresh a catalog row and
**merge-update** a holding in one tx: the merge is the om-kyq7 guarantee (`decode` the body
ONTO the row read *inside* the tx, so a column the client never names — notes/insured_value/
attributes/the disposal — cannot be blanked). The endpoints are **hand-written under
`/api/workflows/*`** (`internal/api/workflows.go`), added to `Handler()` beside the two other
hand-written handlers; `register()` and every **granular route stay untouched** (the Edit
grids, `/api/export`, om-1czp and the e2e suite still ride them — AC#3). `decode[T]` uses
`DisallowUnknownFields`, so the composite request shape matches the client JSON exactly, with
the holding carried as a nested object so it can be decoded onto a fresh row (create) or the
stored row (merge). The **client stopped chaining**: `BoughtABox`/`LoggedFinds` issue exactly
one `fetch`, and `grids.svelte.ts`'s `holdingsGrid.create/update` (the old client-side
`ensureItemType` + separate lots write, which NewBullion/Reconcile also funnel through) is now
ONE atomic call — killing the second, non-atomic write path into the same tables. **No
idempotency key** (there is no auto-retry in `api.ts`; atomicity alone makes a human re-press
safe), **no migration** (adds no columns). Note the **`*Store` auto-commit inserts stay
single-statement on `s.db`** (unchanged): the demo seeder wraps ~2k inserts in its own ambient
`BEGIN` and the poller writes bare, so wrapping `InsertRollTxn`/`InsertTrip` in their own tx
would fire "cannot start a transaction within a transaction" — seam f is closed where a box is
actually *rolled back*, which is only ever inside a compound workflow. `qa/do-tab.e2e.mjs`
passes **unmodified** — the proof the UX did not change.

Added 2026-07-14 (photos are local files with a regenerable cache, om-6hlp): a lot can carry
**N photos**, keyed by the stable `lots.uid` (never the recyclable rowid — the whole reason
ADR-009 exists). The **original is the immutable source of truth** at
`photos/<owner_uid>/<photo_uid>.<ext>`; **thumb+display derivatives are a regenerable cache** in
a *separate* sibling `photos-cache/` (gitignored, generated at ingest, lazily rebuilt on a
serve-time miss, in **neither** backup nor export). Both trees live **beside the database**,
derived via `export.PhotoRoot(export.ResolveDBPath(dbPath))` — REUSE those helpers, don't
re-derive, or the CLI's relative `crh.db` + symlink traps bite. This is the app's first
**untrusted-bytes** surface, so the server decodes behind a **decompression-bomb guard**
(`image.DecodeConfig` dimensions checked BEFORE any full decode; a magic-byte sniff, never the
filename, picks the `ext`) using `internal/imaging` — pure Go (`image/jpeg`+`image/png` stdlib,
`golang.org/x/image/{webp,draw}`), **no CGO**, all six cross-compile targets intact. Upload is
**multipart** with a 10 MB `MaxBytesReader`; the serve route `GET /api/photos/{uid}/file`
validates the uid against the DB **before** building any path (whitelist → traversal impossible,
404 on a miss, never spaHandler's HTML-200). Ingest is **file-first**: write the temp original →
INSERT the row in a `WithTx` → rename temp→final → generate derivatives (best-effort). Invariant:
once the rename lands **a committed row has its original on disk**, and the tolerable residue of a
failed upload is an orphan FILE with no row (never a row with no original). The one gap — a hard
crash *between* the commit and the rename leaves the bytes under the `.upload-*.part` temp name
(the uid, hence the final name, is minted inside the tx) — is tracked in **om-9occ** (mint the uid
before the insert, or a reaper). And the write path validates owner_kind/owner_uid (a well-formed
v4) **before** it builds any path, so a traversal `owner_uid` can never escape `photosDir` — the
same whitelist-before-filesystem discipline as the serve route. Delete is **SOFT** (`photos.inactive`, migration **0013**,
user_version→13): the row is flagged, the file stays; deleting a **lot** soft-flags its photos
(no FK to `lots`). `coinrollhunter backup` is now a **restorable DIRECTORY bundle** (db +
originals); `backup DEST.db` is a **hard error**. EXIF: one Settings key `strip_exif_on_import`,
default KEEP, future-imports-only. Store side follows the three-form pattern
(`insertPhoto`/`InsertPhoto`/`Tx.InsertPhoto` + `Update*`/soft-delete twins, each validating in
its own body + declared in `expectedMutations`). UI: a camera cell on the Holdings grid → a
**detail drawer** (`CoinDetail.svelte`, SettingsPanel's modal shell) around a shared
`PhotoGallery.svelte`, plus an image rotation in `TrophyFeed.svelte` (Insights, keyed off the
existing `lots.trophy` — nothing added to Dashboard, ADR-012 §2). Full rationale: **ADR-009 (f)**.

Added 2026-07-15 (Sol red-team review — three hardening fixes). An external, different-lineage
reviewer (Sol) red-teamed the intentional-looking-wrong decisions; 6 BREAKS verified, 3 fixed now,
the rest tracked. Each fix was implemented on Opus, then independently re-run + adversarially
reviewed on Fable before merge.

**om-bz89 (branch write-lock, Sol #7):** `UpdateBranch` was TWO auto-commit statements — the
`UPDATE branches` then a separate `INSERT branch_aliases` — so a concurrent `DeleteBranch` could
commit between them and leave an **orphan alias** on a recyclable rowid, reopening the om-c8ei
wrong-parent adoption through `branch_aliases` (the one link that stayed integer-keyed). Fix:
`UpdateBranch` wraps both statements in ONE tx — with `SetMaxOpenConns(1)` the tx pins the pool's
sole connection Begin→Commit, so nothing interleaves; `DeleteBranch`/`MergeBranches` take `s.wmu`.
`UpdateBranch` deliberately does **NOT** self-lock: its only caller is `api.register`'s generic PUT
handler, which already holds the **non-reentrant** `s.wmu` via `WithWrite`, so self-locking would
**deadlock** every `PUT /api/branches/{id}`. The **transaction** closes the orphan race
caller-independently; the caller's lock provides RMW serialization. Lock order is wmu→connection at
every site, so no cycle. Pinned by a real concurrency test (fails pre-fix by round 4, passes under
`-race`), which also folds om-h9bn (MergeBranches errors on a nonexistent survivor and mutates
nothing — already true via its uid-resolve first statement, now pinned). **Follow-up om-61d5:**
`insertBranch` on the AUTO-COMMIT path (`POST /api/branches` + `resolveBranchID`'s find-or-create
inside `InsertRollTxn`/`InsertTrip`) has the SAME two-statement hazard on CREATE — not naively
wrappable because `resolveBranchID` also runs inside compound `WithTx` (seam-f).

**om-9occ + om-hs1v-A (photos hardening, Sol #9/#8):** the upload's **commit→rename crash window**
is gone. The uid is now minted with `store.NewUID()` **BEFORE** the tx, the original is written
straight to its final path with `O_EXCL`, the row is INSERTed carrying that same uid, and the file
is removed on ANY tx failure — **no `os.Rename` anywhere**, so a committed row has its original
across a **process** crash. (Power-loss durability still needs an fsync of file+dir before commit —
tracked in **om-0j33**; the code is honest about this now.) `insertPhoto` honors a well-formed
caller-supplied v4 uid (mints when blank, **rejects** a non-blank malformed uid with `ErrInvalid`)
so a row's uid always matches the file naming it. And the serve route now **404s a soft-deleted
photo** for every variant (om-hs1v Half A — deleted means deleted to a viewer); `PhotoByUID` still
resolves inactive rows on purpose (export/restore rely on it), so the guard lives in `serve()`, not
the lookup. **om-hs1v Half B** (hard-delete/purge, whether export excludes inactive, storage GC)
stays **tandem** — the real deletion story is a design sitting.

**om-65nv (EXIF strip-all, Sol #11):** `StripJPEGMetadata` (name kept for the call site) now strips
embedded metadata from **all three** accepted formats — JPEG APP1, PNG `eXIf`/`tEXt`/`zTXt`/`iTXt`,
WebP RIFF `EXIF`/`XMP ` (recomputing the RIFF size) — dispatching on magic bytes. Non-image or
desynced input is returned **unchanged**, and a metadata-free file is returned **byte-identical**
(normal uploads are never rewritten). This closes the false promise where the "strip camera
metadata" Settings toggle **silently no-op'd** on the very WebP/PNG the upload accepts. **Follow-up
om-tmb0:** JPEG odd-fill-byte desync leaks an APP1, JPEG COM/APP13-IPTC + PNG `tIME`/post-IEND +
WebP VP8X flag-bits are out-of-scope channels, and the WebP pad-byte + clean-file fast-path lack
tests. **om-4h6m** (a read-only `doctor` report — Sol #1/#2/#5, Compute silently sums raw-invalid
basis; deferred by om-1czp/om-c8ei) and **om-cqmp** (now spans keeper clad-only *prevention* per
Sol #4, not only repair) are **tandem**.

Added 2026-07-15 (receipts on holdings, om-9o4n): a purchase receipt can now be attached to a
holding — as a **photo/scan** OR a **PDF** — and tagged `role=receipt`. Split into two fired
halves. **om-9o4n.1 (role picker):** the server (`POST /api/photos`) and the api client already
accepted a `role`; only the upload UI never sent one, so every upload defaulted to `detail` and
`receipt` wasn't even suggested for lots. `PhotoGallery` gained an **"Add as" role `<select>`**
(defaults `detail`, sticky across uploads) wired into the upload, and `receipt` joined the lot
suggestions — UI-only, no model/store/imaging change. **om-9o4n.2 (PDF/document attachments):** a
document rides the **existing `photos` table** (no new "attachments" concept, **no migration**) but
**skips the imaging pipeline** — `imaging.Sniff` recognizes `%PDF`→`pdf` and a new `IsDocument`
predicate gates the branch, so a doc never reaches `CheckConfig`/EXIF-strip/`Derive` (all assume
decodable pixels). The **closed ext whitelist** gained `pdf` in BOTH places that enforce it
(`imaging.docExts` + `model.Photo.Validate`) — ext is a path segment, so it stays closed, never
arbitrary text. Ingest is unchanged: a doc rides the same file-first `O_EXCL`/`WithTx`/
remove-on-failure `writeOriginalAndInsert`, so the crash-window-free guarantee (om-9occ) still
holds. **Serve** (`serveDoc`): a doc's original streams `application/pdf` + `X-Content-Type-Options:
nosniff` + **`Content-Disposition: attachment`** — the attachment is the security crux (om-rix0):
`nosniff` does nothing about the browser's OWN PDF viewer, and a same-origin pdf.js-class viewer
bug (CVE-2024-4367) would script the unauthenticated local API past the inert-same-origin om-6ex5
guard, so a doc **downloads** instead of rendering inline, moving it out of the app's origin;
thumb/display variants **404** (no derivative to decode — never a 500, never HTML). The **frontend**
renders a doc as a **document card** (FileText + open/download), never an `<img>` — including the
Insights **TrophyFeed** hero, which now picks the first **non-document** photo as its cover (a
doc-only trophy contributes no hero, like a photo-less one). The doc-ext predicate lives once in
**`$lib/photos`** (`isDocumentExt`), shared by both consumers — the adversarial review caught
TrophyFeed as a third `fileUrl` consumer the first cut missed (it would have rendered a broken hero
`<img>` for a PDF-first trophy). CGO stays off, **no PDF-render dependency** (a generic doc tile, not
a first-page raster). **Follow-up om-6x06:** the >10 MB→413 upload cap still lacks a test.

Added 2026-07-23 (stale browser writes are rejected after upgrade): cache headers
alone cannot protect a tab whose old JavaScript is already running when the binary restarts.
The embedded SPA now serves `index.html` and deep-link fallbacks with `Cache-Control: no-cache`
while Vite's content-hashed `/assets/` use a one-year immutable cache, and every browser mutation carries
`X-CoinRollHunter-Client-Contract: 1`. The outer `api.Guard` checks that contract after its
Origin/Host checks and returns a non-mutating 409 with a refresh instruction when a same-origin
browser write is missing or stale. The guard deliberately requires it only when `Origin` is
present, so curl, the Go health probe, and Node QA remain valid; GET/HEAD remain available to a
stale tab. Bump the matching constants in `internal/api/guard.go` and `web/app/src/lib/api.ts`
whenever an older embedded UI can no longer safely write to the current API; a Go test reads
the TypeScript constants so either half changing alone fails the suite. Immutable caching is
granted only to an existing `/assets/` filename matching Vite's default `name-[hash].ext` shape;
this is a filename heuristic, not proof of hashing, so a future custom output config that places
long English dash-suffixes under `/assets/` must revisit it. An ordinary unhashed or missing asset
keeps `no-cache`. Accepted limitation: Guard rejects before reading a request body,
so net/http may close the connection rather than deliver the JSON refresh message when a stale
tab is partway through a large photo upload; the write is still safely rejected, but the browser
may show a network error and the user must refresh manually.

The `prototype/` reference is the source of truth for behavior and exact formulas.
