# ADR-009: Stable identifiers (`uid`) and the photo model for export

**Status:** Accepted
**Date:** 2026-07-09
**Deciders:** Tom (owner)
**Builds on:** ADR-001 (*"every row has an id so the UI can edit/delete"* — this ADR
is about what that id cannot be asked to do), ADR-003 (catalog/specimen split — the
specimen is a `lots` row), ADR-006 (find taxonomy lives on `lots`).
**Blocks:** specimen photos + storage location (bead `om-6hlp`), CSV/JSON data export
(bead `om-9cua`).
**Closes:** bead `om-hdk5`.

---

## Context

Two planned features both key off a durable per-row identity, and neither can have one
today:

- **Photos** need a path that names one row *forever* — on disk, that path is the only
  thing tying an image to a row in a spreadsheet.
- **Export** needs a row key stable enough to survive leaving the app and coming back.

`lots.id` cannot be that key, and neither can `roll_txns.id`. Both are
`INTEGER PRIMARY KEY` with no `AUTOINCREMENT` anywhere in migrations 0001–0007 — i.e.
plain SQLite rowid aliases, allocated as `max(rowid)+1`. Two distinct failure modes
follow:

1. **ID reuse after delete.** Delete the highest-id lot, insert the next one, and the
   integer is silently recycled. A photo written as `lot-2-obverse.jpg` for a 1964
   Kennedy half is adopted, with no error and no warning, by the common clad quarter
   entered after the Kennedy was sold. Right filename, wrong coin, nothing to detect
   it. (Verified against `modernc.org/sqlite` with the real PK shape.)
2. **Round-trip reshuffle.** Spreadsheet *import* is the planned follow-up slice of
   the export work, so export → edit → reimport is a flow we intend to support.
   Reimport reassigns ids, so every photo reference *and* every exported foreign key
   (`lots.item_type_id`, `lots.roll_txn_id`) lands on whatever row happens to take
   that integer.

There is a third, quieter gap: photos are not a table, so an export acceptance
criterion phrased as *"no data loss vs the SQLite contents for exported tables"* would
be fully satisfied by an export that drops every image.

**What gets photographed — two kinds of row, not one.**

- **Specimens** (`lots`). A CRH find *is* a `lots` row (`activity='crh'`), so
  per-holding photos already cover "photograph my findings"; no separate find table is
  needed. The photo belongs on the **specimen** (`lots`), never on the **catalog entry**
  (`item_type`) — per ADR-003 it is *your* coin being photographed, not the reference
  type it points at.
- **Box/roll purchase records** (`roll_txns`). The box end with its bank stamps, a
  customer-wrapped roll end, a machine-roll wrapper, the receipt. This is evidence about
  the *hunt* rather than about a specimen, and it has nowhere else to live. It is also
  what forces `roll_txns` to carry a `uid` too, rather than deferring that question.

`keepers` rows are excluded. They are aggregate batches (denom + count + face), not
specimens, and a keeper worth photographing is a keeper worth promoting to a lot
(ADR-008's notability rule).

**How many — not two.** Obverse and reverse are the floor, not the ceiling. A variety
attribution wants a close-up of the mintmark or the doubling; a slabbed coin wants its
label; a box record wants the end *and* the receipt. The model has to take an arbitrary
number of images per row, each with a role and an explicit order, or it gets rebuilt the
first time someone photographs a die crack. That rules out a fixed obverse/reverse pair
of filenames and forces a real `photos` table.

## Decision

### (a) Add an opaque, never-reused `uid` to every photographable row

A UUIDv4 in lowercase hex, stored as `TEXT`, on **`lots` and `roll_txns`** — and on the
`photos` rows themselves. Generated once at insert, never renumbered, never recycled,
never reused after delete. `id` keeps its existing job as the internal join key:
`item_type_id` and `roll_txn_id` joins are untouched. This ADR **adds** an identifier; it
does not replace one.

Lowercase hex specifically: photo paths must be safe on case-insensitive filesystems
(Windows, and macOS by default), which rules out case-sensitive base62.

### (b) A `photos` table — identity in the path, everything mutable in the row

An arbitrary number of photos per owner, each with a role, means photos need their own
table:

- **`owner_kind` + `owner_uid`** point at a `lots` or `roll_txns` row. A logical link, not
  a foreign key — the choice migrations 0004 and 0007 made, for the reason they gave
  (*"a local single-writer store"*). Because uids are globally unique, `owner_uid` alone
  identifies the row; `owner_kind` exists so an exported CSV is self-describing.
- **`role`** (`obverse`, `reverse`, `detail`, `edge`, `slab-label`, `box-end`, `receipt`,
  …) and **`seq`** carry meaning and order. Open vocabulary, documented not enforced —
  the precedent ADR-006 set for `category`/`subcategory`. Lowest `seq` is the cover shot.

**The file on disk is `photos/<owner_uid>/<photo_uid>.<ext>`.** The directory says which
coin or which box; the filename is the photo's own immutable uid.

Nothing mutable is encoded in the path. Re-ordering a photo, correcting a role, or
promoting a detail shot to the cover is one `UPDATE` and never a filesystem rename. That
matters more than it looks: a rename is a second write that can fail *after* the database
has already committed, and a path derived from mutable data is the same mistake as an
identifier derived from a reusable integer, one level up.

The cost is real and worth stating: you can no longer tell an obverse from a reverse by
filename alone. `photos.csv` carries `role`, `seq`, `caption`, and a derived `path`
column, so the spreadsheet-to-image join is a single column — and a file manager still
shows one folder per coin.

**How the app reads it.** `obverse` and `reverse` are *known* roles rendered in fixed
slots; every other role flows into a detail gallery in the user's own order. Nothing about
this is left to chance:

```sql
-- the two faces (same query for 'reverse')
SELECT * FROM photos WHERE owner_kind=? AND owner_uid=? AND role='obverse'
  ORDER BY seq, uid LIMIT 1;

-- everything else, in the user's order
SELECT * FROM photos WHERE owner_kind=? AND owner_uid=? AND role NOT IN ('obverse','reverse')
  ORDER BY seq, uid;
```

The grid thumbnail is the obverse when one exists, else the lowest `(seq, uid)`. Two
details are what make that deterministic rather than merely likely, and both were found by
testing rather than reasoning:

- **`ORDER BY seq` alone is not a total order.** `seq` defaults to 0, so a UI that never
  assigns one leaves every photo tied, and SQLite's order among ties is unspecified —
  verified: the identical query over the identical rows returns `reverse, obverse, detail,
  detail` under one plan and the exact reverse of that under another. So the order is
  always `ORDER BY seq, uid`, with `uid` as the total, stable tiebreaker; the owner index
  carries `uid` as its last column, which turns the ordering into a covering-index scan
  instead of a temp b-tree. The app should still assign `seq = max(seq)+1` per owner at
  insert, so ties are a fallback rather than the norm.
- **`role` is `NOT NULL DEFAULT 'detail'`.** A nullable role *loses photos*: `NULL NOT IN
  ('obverse','reverse')` evaluates to `NULL` rather than true, so an un-roled image drops
  out of the gallery query — present in the database, present on disk, invisible in the
  app. `photos` is a new table, so it can simply forbid the case.

Export therefore becomes a bundle: per-table CSVs (including `photos.csv`), a `photos/`
tree, and a `manifest.json` carrying schema version and file list — rather than loose
CSVs.

### (c) The migration is additive; on the *altered* tables, `NOT NULL` lives in Go

> **Numbering, as shipped.** This section was written as "migration 0008". ADR-010
> (branches) landed first and took 0008, so the uid + photos migration shipped as
> **`0009_stable_uids_photos.sql`**. Same SQL, next free number. ADR-010 (c) already
> anticipated this: it generated `branches.uid` with the same `randomblob` recipe and
> noted it "unblocks ADR-009's om-hdk5 lots/roll_txns uid backfill".

```sql
-- Backfill expression, used once per altered table:
--   lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' ||
--         substr(hex(randomblob(2)),2) || '-' ||
--         substr('89ab', abs(random()) % 4 + 1, 1) ||
--         substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6)))

ALTER TABLE lots      ADD COLUMN uid TEXT;
ALTER TABLE roll_txns ADD COLUMN uid TEXT;

UPDATE lots      SET uid = <expr> WHERE uid IS NULL;
UPDATE roll_txns SET uid = <expr> WHERE uid IS NULL;

CREATE UNIQUE INDEX idx_lots_uid      ON lots(uid);
CREATE UNIQUE INDEX idx_roll_txns_uid ON roll_txns(uid);

CREATE TABLE photos (
  id         INTEGER PRIMARY KEY,
  uid        TEXT NOT NULL UNIQUE,   -- filename stem; a NEW table can declare this
  owner_kind TEXT NOT NULL,          -- 'lot' | 'roll_txn'
  owner_uid  TEXT NOT NULL,          -- lots.uid | roll_txns.uid (logical link, no FK)
  role       TEXT NOT NULL DEFAULT 'detail',
                                     -- obverse|reverse|detail|edge|slab-label|box-end|receipt
                                     -- NOT NULL: a NULL role drops the photo out of the
                                     -- gallery query (NULL NOT IN (...) is NULL, not true)
  seq        INTEGER NOT NULL DEFAULT 0,   -- lowest = cover; app assigns max(seq)+1 per owner
  ext        TEXT NOT NULL,          -- jpg|png|webp
  caption    TEXT,
  created    TEXT
);
-- uid is the last column so ORDER BY seq, uid is served by a covering index, not a sort
CREATE INDEX idx_photos_owner ON photos(owner_kind, owner_uid, seq, uid);
```

Verified end-to-end on top of the real 0001–0007: both backfills produce distinct,
well-formed v4s with no cross-table collisions, because `randomblob()` is
non-deterministic and re-evaluates **per row**. No Go-side backfill loop, no second
migration pass.

**Note the asymmetry.** `photos` is a *new* table, so it declares `uid TEXT NOT NULL
UNIQUE` outright and SQLite enforces both (verified: NULL and duplicate inserts are
rejected). The two *altered* tables cannot, and that is where the care goes. Two
constraints, both verified against the pinned driver (`modernc.org/sqlite` v1.53.0,
SQLite 3.53.2), and both easy to get wrong:

- **A `UNIQUE` index does not imply `NOT NULL`.** SQLite treats NULLs as mutually
  distinct, so any number of rows may carry a NULL `uid` straight through the index.
  The real guard is therefore the Go insert path. Pin it with a store test asserting
  that no lot and no roll txn ever reads back a NULL or empty `uid` — an *invariant*
  test in the ADR-001 spirit (an accounting identity that holds for any dataset), not a
  mock. A photo orphaned by a NULL owner uid is unrecoverable: nothing else on disk says
  which coin it was.
- **Do not reach for `ALTER TABLE lots ALTER COLUMN uid SET NOT NULL`.** The pinned
  driver accepts this and rewrites the stored schema, which makes it look like the
  obvious fix. It is not part of SQLite's `ALTER TABLE` grammar (upstream:
  `RENAME TO` | `RENAME COLUMN` | `ADD COLUMN` | `DROP COLUMN`), and the driver's
  support for it is partial — the sibling `SET DEFAULT` and `TYPE` forms are rejected.
  A migration leaning on it silently binds `crh.db` to one driver, and users will open
  that file with other tools.

The portable way to get a schema-level `NOT NULL` is the standard 12-step table rebuild —
now *two* of them, `lots` and `roll_txns`. **Rejected**, for a reason specific to this
codebase: `store.go` opens the DB with `foreign_keys(1)`, `migrate()` runs each migration
inside its own transaction, and `PRAGMA foreign_keys = OFF` is a **no-op inside a
transaction** (verified — it stays on). A rebuild would need the migration runner to
special-case itself. That is a poor trade for a local single-writer store, and it cuts
against the precedent 0004 and 0007 set explicitly: *"a plain nullable column is enough."*

### (d) Export reserves the photo tree even if it ships first

Export is P2 and photos are P3, so export will likely land first. It emits `uid` in
`lots.csv` and `roll_txns.csv`, ships a `photos.csv` with its columns fixed (empty until
photos exist), and reserves `photos/` in `manifest.json` from day one. Adding a column, a
whole table, or a directory to a format users have already built spreadsheets against is
a breaking change; reserving all three now costs nothing.

### (e) The bundle, as shipped (bead `om-9cua`)

> **Amendment, 2026-07-12.** Export landed first, as (d) expected, and it did better than
> reserve the tree: the photo-copy code is *written and tested*, against the empty-but-live
> `photos` table. A test inserts a row, drops a real byte-file, and asserts the bundle
> carries it — so *"export never silently drops photos"* is a passing test **before photos
> exist**, and `om-6hlp` never has to touch `internal/export`.

The bundle is one CSV per table (all twelve, no filters, no options — an export with
options is an export with a way to silently produce an incomplete file), plus:

- **`data.json`** — the same rows, typed. CSV cannot distinguish `NULL` from the empty
  string, and this schema is full of nullable columns, so *"no data loss vs. the SQLite
  contents"* is only literally true with this file in the bundle. It is also the lossless
  input a future importer wants. Lossless is precise here: it holds for **valid-UTF-8 text**
  (which is all the app itself writes) and for every number and NULL. SQLite `TEXT` can
  technically store *invalid* UTF-8 — only reachable if an external tool writes it — and that
  is best-effort: JSON substitutes the Unicode replacement character `U+FFFD` for such bytes
  rather than preserve them. We accept that rather than base64-encode every text column; a
  *non-finite number* (Inf/NaN), by contrast, is refused loudly (see below), not best-effort.
- **`manifest.json`** — `format_version` (of the bundle) and `db_schema_version` (the
  `PRAGMA user_version`), plus per-file row counts and SHA-256. An importer that meets a
  `format_version` above what it understands must **refuse the bundle whole**, never
  partially import it. It also carries a `missing[]` list (photo files a row referenced but
  that were not in the bundle — absent, unreadable, or refused as unsafe) and an
  `unexpected_settings[]` list (any settings key beyond the known tunables — see below).
  **`format_version` stays 1**: both lists are additive in shape. But the *semantic* contract
  softened — a valid v1 bundle can now legitimately be missing a referenced photo — so an
  importer **must read `missing[]`** rather than assume every referenced file is present. And
  **`missing[]` entries are untrusted strings**: they are built from raw database column
  values (that is how a hostile `owner_uid` lands there), so an importer must not treat one as
  a real filesystem path without re-validating it, on pain of inheriting the traversal the
  exporter refused.
- **`photos/<owner_uid>/<photo_uid>.<ext>`** — the **originals only**. Resized derivatives
  are a regenerable cache, not the user's data.

Properties successive adversarial reviews turned from intention into guarantee:

- **Export is read-only over the user's database, structurally.** The CLI does not open the
  source file as a database — `store.Open` would migrate it (an old archive would be silently
  upgraded before being read). It snapshots the file with `BackupFile` (VACUUM INTO, the same
  call `backup` runs on live databases — a plain byte copy is what `store.Backup`'s own
  docstring calls wrong), migrates and reads the *copy*, and discards it. The source is only
  ever read.
- **The reads are snapshot-consistent, and the transaction is released before file I/O.** The
  export runs in two phases: a read phase that pulls every table (plus the settings keys and the
  photo row list) into memory inside ONE read transaction, then — with the transaction closed — a
  write phase that renders the CSVs/`data.json`/manifest and copies the photo files. On the CLI
  the transaction is redundant (it reads a private snapshot); on the *live* browser path it is the
  guarantee — twelve separate queries could otherwise straddle a concurrent write and emit a lot
  whose `item_type_uid` is not in `item_type.csv`. The store is `MaxOpenConns(1)`, so during the
  read phase the open transaction holds the one connection and any concurrent write serializes
  after it — no interleave window. Crucially the (potentially slow) file writing and photo copying
  happen *after* the transaction is released, so the export does not freeze the UI and the spot
  poller for its whole duration; and the caller's `context` flows into `BeginTx`, so a cancelled
  HTTP download releases the connection instead of holding it to the end.
- **The photo root is passed in, never derived from the store being read, and symlink-resolved.**
  Photos live beside the user's *real* database; the CLI reads a *copy*. Deriving the photo
  directory from the copy's path (an empty temp dir) silently dropped every photo — two
  individually-correct fixes composing into a data-loss bug. The root is now an explicit argument
  computed from the real path by the caller, and that path is run through `EvalSymlinks` first, so
  a DB reached through a link (`~/crh.db` → `/srv/coins/crh.db`) still finds its photos.
- **No single unreadable photo fails the whole export.** The rest of the collection is what the
  user came for; one bad row must not deny it. A file that is absent, permission-denied, or
  corrupt — or a row whose `owner_uid`/`uid`/`ext` carries a separator, `..`, or a
  Windows-reserved token, which is refused to stop it escaping the bundle — is recorded in
  `missing[]` and export carries on. Only a failure to *write* the bundle is fatal.
- **A non-finite number is refused, loudly and precisely.** SQLite `REAL` can hold `Inf`/`NaN`
  (an external tool can write one); a spreadsheet cannot represent it and `json.Marshal` fails
  with a generic, undebuggable error. Export detects it first and errors naming the table, the
  column, and the row (its `uid`, else its identifying column) — so the user fixes one cell
  rather than losing the export to a mystery.
- **The directory export writes in place and cleans up only its own output on failure.** After
  the emptiness check it writes the bundle directly into the destination; on failure it removes
  only what it created (the whole directory if it created it, else just the top-level entries it
  wrote), so a partial never blocks a retry, and on success it touches nothing else. It
  deliberately does NOT stage-and-rename over the destination: a rename would delete anything a
  concurrent process dropped into the directory after the check (a data-loss race), replace a
  destination that is a *symlink* to a synced/removable target with a local directory (the bundle
  silently never reaching the real target), and fail outright for `export .`. Write-in-place has
  none of those failure modes. (The zip download stays as it was — it already builds to a temp
  file and commits only on success, which is correct for a single file.)
- **The settings table is an open key/value bag, so export flags what it does not recognise.**
  Nothing is dropped (that would be data loss), but any key beyond the six known tunables is
  named in `unexpected_settings[]`, so a credential a future feature parks there surfaces in
  the manifest instead of leaking into a file the user shares.

Three consequences worth writing down, because they were not obvious:

- **`item_type` gets a `uid` after all** (migration 0010). The Alternatives section below
  deferred it and named the exact trigger — *"a live problem only if export starts emitting
  `item_type_id` as a foreign key"*. Export does, so it is one. Recycling that particular
  rowid is worse than the photo case this ADR was written for: it changes what the coin
  **is**. `keepers`/`trips`/`supplies`/`losses` still get none — they are leaf rows, nothing
  points at them, and their own outbound FKs already resolve to uid targets.
- **The exporter writes its table and column lists out**, rather than discovering them with
  `SELECT *`. A self-discovering exporter passes every test you can give it while shipping
  whatever it happens to find. Declared lists let the tests compare the bundle against
  `sqlite_schema` and `PRAGMA table_info` and **break** when a migration adds a table or a
  column — which is exactly when someone should have to decide how that data leaves the app.
- **Export is not backup, and neither replaces the other.** `backup` is machine-readable and
  restorable (the database file); the export bundle is human-readable and portable (CSV +
  photos). Two different promises — *"restore my app"* and *"leave with my data"* — so there
  is no `crh.db` inside the bundle.

## Consequences

- **+** A photo can never silently re-attach to the wrong coin. The identifier outlives
  deletes, sales, and reimports.
- **+** Arbitrary photos per row, each with a role and an order — and box/roll records can
  carry evidence of the hunt, not just specimens of it.
- **+** The two faces are a first-class query rather than a naming convention: `role` slots
  the obverse and reverse, and `(seq, uid)` orders everything else deterministically.
- **+** Re-ordering or re-roling a photo never touches the filesystem.
- **+** The export → edit → reimport round trip has a join key that survives it, which
  is what makes "you can always leave with your data" true rather than merely shipped.
- **+** The estate sheet and insurance schedule inherit a durable per-row reference for
  free — both want to point at a coin *and* its picture.
- **+** Backfill is one SQL statement per altered table.
- **−** Three `uid` columns and a new table, up from one column.
- **−** The `NOT NULL` guarantee on `lots.uid` and `roll_txns.uid` lives in Go plus a test
  rather than in the schema (`photos.uid` gets the real thing). A genuine weakening; the
  test is what keeps it honest.
- **−** Paths are long (`photos/<36 chars>/<36 chars>.jpg`) and no filename tells you what
  it shows. Accepted: they are opaque handles, and `photos.csv` is where humans look.

## Alternatives considered

- **Name photos by `lots.id`.** The obvious choice, and the reason this ADR exists.
  rowid reuse rebinds an existing photo to a different specimen with no error; the
  export round trip scrambles the rest. Verified, not theorized.
- **A composite natural key** (year + mint + denom + acquired). Rejected twice over: it
  is not unique (two 1943-S cents out of the same box) and it is mutable (correcting a
  misread year would rename every photo of that coin).
- **ULID or a short base62 id.** Genuinely close. A ULID sorts by creation time, so
  `ls photos/` would group a hunt chronologically — a real ergonomic win. Rejected on
  the filename constraint: case-sensitive encodings are unsafe on Windows and macOS,
  and the sortability is not worth a collision that only appears on someone else's
  filesystem.
- **Blobs in SQLite, no filenames at all.** Rejected in the photos decision: blobs
  bloat the DB, make backup and restore all-or-nothing, and force export to invent an
  unpacking step. Files beside the DB make backup "copy the directory" and let a user
  with a file manager already see their collection.
- **A fixed obverse/reverse pair, `photos/<uid>-obverse.jpg`.** The first sketch of this
  ADR, and wrong. Two photos is the floor: variety attribution needs a mintmark close-up,
  a slab needs its label, a box record needs the end *and* the receipt. A fixed pair would
  have been rebuilt into a table on first contact with a real collection.
- **Encode `role` and `seq` in the filename** (`photos/<uid>/0-obverse.jpg`). Genuinely
  tempting — the folder reads without a CSV. Rejected: every re-order or role correction
  becomes a filesystem rename, a second write that can fail after the database has
  committed, and identity would once again derive from mutable data. The `path` column in
  `photos.csv` buys back the readability at no such cost.
- **Enforce at most one `obverse` and one `reverse` per owner** with a partial unique index
  (`… ON photos(owner_kind, owner_uid, role) WHERE role IN ('obverse','reverse')`).
  Supported on the pinned driver (verified), and tempting because it makes "the obverse"
  literally singular. Not adopted: it contradicts the open-vocabulary precedent ADR-006
  set, and it forbids a legitimate case — two obverse shots under different lighting.
  Taking the lowest `(seq, uid)` among `role='obverse'` is already deterministic without
  spending a constraint on it.
- **Give `item_type` a uid too.** ~~Still deferred, not rejected.~~ **Adopted 2026-07-12
  (migration 0010, bead `om-9cua`)** — on precisely the trigger this bullet named. A catalog
  entry is reference data, not a thing you own or photograph, so it was deferred: *"it
  becomes a live problem only if export starts emitting `item_type_id` as a foreign key."*
  Export does. See (e).
