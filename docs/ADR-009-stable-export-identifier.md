# ADR-009: A stable specimen identifier (`lots.uid`) for photos and export

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

Two planned features both key off a durable per-specimen identity, and neither can
have one today:

- **Photos** need a filename that names one specimen *forever* — the filename is the
  only thing linking an image on disk to a row in a spreadsheet.
- **Export** needs a row key stable enough to survive leaving the app and coming back.

`lots.id` cannot be that key. It is `INTEGER PRIMARY KEY` with no `AUTOINCREMENT`
anywhere in migrations 0001–0007 — i.e. a plain SQLite rowid alias, allocated as
`max(rowid)+1`. Two distinct failure modes follow:

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

**Scope.** A CRH find *is* a `lots` row (`activity='crh'`), so per-holding photos
already cover "photograph my findings" — no separate find table is needed. `keepers`
rows cannot carry a specimen photo: they are aggregate batches (denom + count + face),
not specimens, and a keeper worth photographing is a keeper worth promoting to a lot
(ADR-008's notability rule). The photo belongs on the **specimen** (`lots`), never on
the **catalog entry** (`item_type`) — per ADR-003 it is *your* coin being photographed,
not the reference type it points at.

## Decision

### (a) Add an opaque, never-reused `lots.uid`

A UUIDv4 in lowercase hex, stored as `TEXT`. Generated once at insert, never
renumbered, never recycled, never reused after delete. `id` keeps its existing job as
the internal join key — `item_type_id` and `roll_txn_id` joins are untouched. This ADR
**adds** an identifier; it does not replace one.

Lowercase hex specifically: photo filenames must be safe on case-insensitive
filesystems (Windows, and macOS by default), which rules out case-sensitive base62.

### (b) `uid` is the export row key *and* the photo filename stem

`photos/<uid>-obverse.jpg`, `photos/<uid>-reverse.jpg`. `lots.csv` leads with `uid`.
No index file, no lookup step, no manifest read required: open the CSV, open the
folder, and the join *is* the filename. Export becomes a bundle — per-table CSVs, a
`photos/` directory, and a `manifest.json` carrying schema version and file list —
rather than loose CSVs.

### (c) Migration 0008 is additive; `NOT NULL` is enforced in Go, not in the schema

```sql
ALTER TABLE lots ADD COLUMN uid TEXT;
UPDATE lots SET uid = lower(
  hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' ||
  substr(hex(randomblob(2)),2) || '-' ||
  substr('89ab', abs(random()) % 4 + 1, 1) ||
  substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6))
) WHERE uid IS NULL;
CREATE UNIQUE INDEX idx_lots_uid ON lots(uid);
```

`randomblob()` is non-deterministic and re-evaluates **per row** (verified), so the
single `UPDATE` gives every existing row a distinct, well-formed v4 — no Go-side
backfill loop, no second migration pass.

Two constraints, both verified against the pinned driver (`modernc.org/sqlite`
v1.53.0, SQLite 3.53.2), and both easy to get wrong:

- **A `UNIQUE` index does not imply `NOT NULL`.** SQLite treats NULLs as mutually
  distinct, so any number of rows may carry a NULL `uid` straight through the index.
  The real guard is therefore the Go insert path. Pin it with a store test asserting
  that no lot ever reads back a NULL or empty `uid` — an *invariant* test in the
  ADR-001 spirit (an accounting identity that holds for any dataset), not a mock.
- **Do not reach for `ALTER TABLE lots ALTER COLUMN uid SET NOT NULL`.** The pinned
  driver accepts this and rewrites the stored schema, which makes it look like the
  obvious fix. It is not part of SQLite's `ALTER TABLE` grammar (upstream:
  `RENAME TO` | `RENAME COLUMN` | `ADD COLUMN` | `DROP COLUMN`), and the driver's
  support for it is partial — the sibling `SET DEFAULT` and `TYPE` forms are rejected.
  A migration leaning on it silently binds `crh.db` to one driver, and users will open
  that file with other tools.

The portable way to get a schema-level `NOT NULL` is the standard 12-step table
rebuild. **Rejected**, for a reason specific to this codebase: `store.go` opens the DB
with `foreign_keys(1)`, `migrate()` runs each migration inside its own transaction, and
`PRAGMA foreign_keys = OFF` is a **no-op inside a transaction** (verified — it stays on).
A rebuild would need the migration runner to special-case itself. That is a poor trade
for a local single-writer store, and it cuts against the precedent 0004 and 0007 set
explicitly: *"a plain nullable column is enough."*

### (d) Export reserves the photo path even if it ships first

Export is P2 and photos are P3, so export will likely land first. It emits `uid` in the
CSVs and reserves `photos/` in `manifest.json` from day one, with no photos to put
there. Adding a column or a directory to a format users have already built spreadsheets
against is a breaking change; reserving both now costs nothing.

## Consequences

- **+** A photo can never silently re-attach to the wrong coin. The identifier outlives
  deletes, sales, and reimports.
- **+** The export → edit → reimport round trip has a join key that survives it, which
  is what makes "you can always leave with your data" true rather than merely shipped.
- **+** The estate sheet and insurance schedule inherit a durable per-specimen
  reference for free — both want to point at a coin *and* its picture.
- **+** Backfill is one SQL statement against existing databases.
- **−** One more column and one more index on `lots`.
- **−** The `NOT NULL` guarantee lives in Go plus a test rather than in the schema. This
  is a real weakening; the test is what keeps it honest.
- **−** `uid` is 36 characters, so photo filenames are long. Accepted: they are opaque
  handles, not names for humans to type.

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
- **Give `roll_txns` and `item_type` uids too.** Deferred, not rejected. They carry the
  same rowid-alias exposure, and it becomes a live problem the moment export emits
  their ids as foreign-key columns. Revisit when it does.
