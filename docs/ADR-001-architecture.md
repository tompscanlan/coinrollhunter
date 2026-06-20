# ADR-001: Local-first single-binary architecture for the coins & bullion tracker

**Status:** Proposed
**Date:** 2026-06-20
**Deciders:** Tom (owner)

---

## Context

We have a working prototype: two JSON files (`pm_holdings.json`, `crh_ledger.json`),
a Python engine (`portfolio.py`) that computes P&L and renders an Excel workbook, and an
interactive `dashboard.html` that reads/writes the JSON via the browser File System Access API.

It answers the core questions well (is bullion profitable? is coin-roll-hunting paying for
itself? have we cashed in everything not kept?). But it cannot be handed to other people.

### Requirements for a releasable version

| # | Requirement | Current gap |
|---|-------------|-------------|
| R1 | Super easy to run | "Open an HTML file" breaks once you need saving, spot fetch, updates |
| R2 | Runs on Windows, Linux, macOS | File System Access API is Chromium-only; fails Firefox/Safari |
| R3 | Data entry needs lots of help | Forms are add-only; no presets beyond silver type, no validation depth |
| R4 | Edit existing entries | No edit or delete in the UI at all |
| R5 | Spreadsheet-style management | No grid; data lives in raw JSON |
| R6 | Host it OR run locally | Two different codebases would be needed today |
| R7 | Buy volumes smaller than a box | Model assumes boxes; partial/odd amounts are awkward |
| R8 | Private by default | A net-worth tracker storing others' wealth is sensitive |

### Decisions already made (this session)

- **Deployment:** local single binary first, designed so a hosted multi-user mode is a clean phase 2.
- **Release scope:** personal tool, share-ready (good enough for a friend to run; not full OSS scaffolding yet).
- **Stack bias:** Go preferred (Rust acceptable); Python reserved for one-off tooling/migration.

---

## Decision

Build a **single self-contained Go binary** that embeds the web UI (Go 1.16+ `embed`) and
serves it on `localhost`, with **SQLite** as the datastore (pure-Go driver `modernc.org/sqlite`,
no CGO, so cross-compilation stays trivial). The user downloads one file for their OS,
double-clicks it, and a browser tab opens to the app. All data stays on their machine in a
single `.db` file they can back up or move.

The HTTP layer is a small JSON REST API over the SQLite store. The **same binary** can run in
a hosted mode later (bind to a public port, add auth + per-user data) without re-architecting —
that is the phase-2 path for R6.

This directly satisfies R1 (one file, no runtime), R2 (Go cross-compiles to all three OSes;
UI runs in any browser, no FS API needed), R6 (local now, host later from one codebase), and
R8 (local-first, data never leaves the machine).

---

## Options Considered

### Option A: Go single binary + embedded web UI + SQLite  **(chosen)**

| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium |
| Cost | Free to run; only CI build minutes |
| Cross-platform | Excellent — static binaries for win/mac/linux |
| Ease of running | Excellent — download one file, double-click |
| Hosted path | Excellent — same code serves localhost or a server |
| Team familiarity | High — Go is the owner's preferred language |

**Pros:** No runtime/install; real DB enables edit/delete + grid; browser-agnostic UI; one
codebase for local and hosted; private by default; easy backups (copy the `.db`).
**Cons:** Must produce + (ideally) sign per-OS binaries; first run may hit OS "unknown developer"
warnings; we own a small HTTP server.

### Option B: Native desktop app (Rust + Tauri)

| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium-High |
| Cost | Free; signing certs cost money |
| Cross-platform | Good, but three platform builds + signing |
| Ease of running | Excellent once installed; installer per OS |
| Hosted path | Poor — desktop-only; hosted would be a separate build |
| Team familiarity | Medium — Rust acceptable, more ceremony |

**Pros:** Native feel, system tray, built-in auto-update, smaller memory than Electron.
**Cons:** Per-OS packaging + code-signing overhead; no shared path to a hosted version;
heavier distribution for a "share-ready personal tool."

### Option C: Hosted SaaS from day one

| Dimension | Assessment |
|-----------|------------|
| Complexity | High |
| Cost | Ongoing hosting + DB |
| Cross-platform | Excellent (just a URL) |
| Ease of running | Excellent for users; high for maintainer |
| Hosted path | N/A (already hosted) |
| Team familiarity | High backend, but adds auth/ops |

**Pros:** Zero install for users; multi-device sync; central updates.
**Cons:** You store strangers' wealth data (privacy + liability); accounts/auth/ops burden;
hosting cost; overkill for current goal. Better as opt-in phase 2.

### Option D: Keep single HTML file + File System Access API

| Dimension | Assessment |
|-----------|------------|
| Complexity | Low |
| Cost | Free |
| Cross-platform | Poor — Chromium-only saving |
| Ease of running | Medium — "open file," but save quirks |
| Hosted path | Poor |
| Team familiarity | High |

**Pros:** Already built; nothing to install.
**Cons:** Fails R2 (Firefox/Safari can't save in place); no real DB for grid/edit at scale;
clunky for non-technical users. This is the prototype we are graduating from.

---

## Trade-off Analysis

The decision hinges on R2 + R6 + R8 together. Option D already fails R2. Option C satisfies the
"easy for users" goal but trades it for privacy/liability and ops cost we don't want yet.
Option B gives the nicest native UX but strands us from a hosted future and adds packaging
overhead disproportionate to a "share-ready personal tool."

Option A is the only one that is simultaneously trivial to run (one binary), browser-agnostic,
private by default, and re-usable as the basis for an eventual hosted version. The cost we accept
is owning a tiny HTTP server and a release pipeline — both well-trodden in Go (`embed`,
`net/http`, goreleaser, GitHub Actions). SQLite via `modernc.org/sqlite` keeps builds CGO-free so
a single `go build` cross-compiles everywhere.

---

## Data Model (unified store)

Collapse the two JSON files into one SQLite schema. Bullion lots and CRH finds become one
`lots` table distinguished by `activity` ('bullion' | 'crh'); everything has a stable `id` so
the UI can edit/delete (R4).

```sql
-- holdings: gold, purchased junk silver, and CRH finds
CREATE TABLE lots (
  id            INTEGER PRIMARY KEY,
  activity      TEXT NOT NULL,      -- 'bullion' | 'crh'
  product       TEXT NOT NULL,
  metal         TEXT NOT NULL,      -- 'gold' | 'silver'
  fineness      TEXT,
  qty           REAL NOT NULL,
  fine_oz_each  REAL NOT NULL,      -- troy oz pure metal per unit
  basis_usd     REAL NOT NULL,      -- total paid (face for CRH finds)
  face_value_usd REAL DEFAULT 0,
  acquired      TEXT NOT NULL,      -- ISO date
  source        TEXT,
  notes         TEXT,
  disposed      TEXT,               -- ISO date if sold (for tax/realized gains)
  disposed_usd  REAL
);

-- CRH roll flow + box/roll/volume throughput
CREATE TABLE roll_txns (
  id        INTEGER PRIMARY KEY,
  date      TEXT NOT NULL,
  bank      TEXT,
  action    TEXT NOT NULL,          -- 'buy' | 'return'
  denom     TEXT,                   -- halves|quarters|dimes|nickels|cents
  unit      TEXT,                   -- 'box' | 'roll' | 'face' | 'coin'  (entry unit)
  amount    REAL,                   -- quantity in that unit
  face_usd  REAL NOT NULL,          -- normalized $ face (the source of truth)
  notes     TEXT
);

CREATE TABLE trips    ( id INTEGER PRIMARY KEY, date TEXT, bank TEXT, miles REAL, hours REAL );
CREATE TABLE supplies ( id INTEGER PRIMARY KEY, date TEXT, item TEXT, cost_usd REAL );
CREATE TABLE keepers  ( id INTEGER PRIMARY KEY, denom TEXT, count INTEGER, face_usd REAL );
CREATE TABLE spot     ( as_of TEXT PRIMARY KEY, gold_usd REAL, silver_usd REAL, source TEXT );
CREATE TABLE settings ( key TEXT PRIMARY KEY, value TEXT );
```

### Flexible purchase units (R7)

Entry accepts **box, roll, dollars-of-face, or coin count** and normalizes to `face_usd`:

```
box_face   = { halves:500, quarters:500, dimes:250, nickels:100, cents:25 }
roll_face  = { halves:10,  quarters:10,  dimes:5,   nickels:2,   cents:0.50 }
coin_face  = { halves:0.50, quarters:0.25, dimes:0.10, nickels:0.05, cents:0.01 }
face_usd   = amount * unit_face[denom]        # box/roll/coin
face_usd   = amount                           # unit == 'face'
boxes_equiv = face_usd / box_face[denom]      # derived, for throughput display
```

So "$120 of halves," "3 rolls of dimes," "0.4 box of quarters," and "240 half-dollars" all land
as the same normalized face value, and box-throughput is computed, never required as input.

### Data-entry quality (R3) and grid (R5)

- An **editable grid** per table (add/edit/delete inline), backed by REST `GET/POST/PUT/DELETE`.
- Presets: silver ASW factors, denomination unit-faces, bank autocomplete from prior rows.
- "Duplicate last entry," inline validation, CSV import/export, keyboard-first navigation.
- Spot prices: server fetches from a metals source on demand (manual override always available);
  every fetch is appended to `spot` so we keep history for charts.

---

## Consequences

**Easier:** cross-platform distribution (one `go build`); editing/deleting any record; growing
the dataset without JSON hand-editing; adding charts (spot history is now stored); a future
hosted mode (swap the storage layer for per-user, add auth — the API stays the same).

**Harder / new work:** we now own an HTTP server and a release pipeline; first-run OS security
prompts need a short "how to open" note (or later, code signing); we must write a one-time
migration from the current JSON into SQLite.

**Revisit later:** authentication + multi-user (only if we go hosted); code-signing certificates
(only if first-run warnings become a real adoption barrier); whether to keep `portfolio.py` as a
reporting/export tool or port its math into Go (recommended: port the math to Go for the app,
keep Python only for ad-hoc analysis and the xlsx export).

---

## Action Items — phased build plan

### Phase 0 — Foundations (½ day)
1. [ ] Pick a name (working title: **StackLedger**; alternatives: HuntLedger, BullionBook, StackKeeper).
2. [ ] Init Go module + repo layout (`/cmd`, `/internal/store`, `/internal/calc`, `/web`).
3. [ ] Define the SQLite schema above as migrations.
4. [ ] Write a one-time `migrate` command: current JSON -> SQLite.

### Phase 1 — Core engine + API (1-2 days)
5. [ ] Port the P&L math from `portfolio.py` into `internal/calc` (bullion MTM, CRH net, cash-in
       reconciliation, box/face normalization) with unit tests against the known prototype numbers
       (CRH net $103.81, bullion -$1,546.43, to-redeposit $163.50).
6. [ ] REST API: CRUD for lots, roll_txns, trips, supplies, keepers; spot get/set; summary endpoint.
7. [ ] Embed static UI with `embed`; serve on `localhost:<port>`; auto-open browser on launch.

### Phase 2 — UI: dashboard + grids (2-3 days)
8. [ ] Dashboard view (port current cards/verdict/reconciliation; add spot-history chart).
9. [ ] Editable grids per table with add/edit/delete, flexible unit entry, validation, bank autocomplete.
10. [ ] CSV import/export; xlsx export (reuse Python or a Go xlsx lib).

### Phase 3 — Share-ready packaging (1 day)
11. [ ] goreleaser + GitHub Actions: build win/mac/linux binaries on tag.
12. [ ] README with screenshots + a 3-step "download, run, open" + first-run security note.
13. [ ] Sample/demo dataset and a `--demo` flag so a new user sees it working immediately.

### Phase 4 (optional, later) — Hosted mode
14. [ ] Storage interface -> per-user backend (SQLite-per-user or Postgres).
15. [ ] Auth, and a clear privacy stance (local-first remains the default).

### Live spot prices — open question to resolve in Phase 1
Most metals APIs need a key. Options: a free/keyless source if reliable; a user-supplied API key
in settings; or keep manual entry as the always-available fallback (recommended baseline).
