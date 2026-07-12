-- 0009_stable_uids_photos: give every photographable/exportable row an opaque,
-- never-recycled identity, and make photos a real table (ADR-009, om-hdk5).
--
-- NOTE ON NUMBERING: ADR-009 and bead om-hdk5 both call this "migration 0008".
-- They were written before ADR-010 landed branches as 0008. Same migration, next
-- free number.
--
-- Why: lots.id and roll_txns.id are INTEGER PRIMARY KEY with no AUTOINCREMENT --
-- plain rowid aliases, allocated max(rowid)+1. Delete the highest-id lot, insert
-- the next, and the integer is silently RECYCLED: a photo filed under the 1964
-- Kennedy half is adopted, with no error, by the clad quarter entered after the
-- Kennedy was sold. Right filename, wrong coin, nothing to detect it. Export ->
-- edit -> reimport reshuffles the same integers across every FK. So identity that
-- outlives a row's rowid has to be its own column.
--
-- The uid is generated in SQL here (the migration runner has no Go step) with the
-- same lowercase-UUIDv4 randomblob recipe migration 0008 proved for branches --
-- randomblob() is non-deterministic and re-evaluates PER ROW, so one UPDATE
-- backfills every row with a distinct v4. Lowercase hex, not base62: these become
-- path segments, and case-sensitive encodings collide on Windows and macOS.

ALTER TABLE lots      ADD COLUMN uid TEXT;
ALTER TABLE roll_txns ADD COLUMN uid TEXT;

UPDATE lots SET uid =
  lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
  substr(lower(hex(randomblob(2))), 2) || '-' ||
  substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
  lower(hex(randomblob(6)))
WHERE uid IS NULL;

UPDATE roll_txns SET uid =
  lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
  substr(lower(hex(randomblob(2))), 2) || '-' ||
  substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
  lower(hex(randomblob(6)))
WHERE uid IS NULL;

-- A UNIQUE index does NOT imply NOT NULL: SQLite treats NULLs as mutually
-- distinct, so any number of rows could still carry a NULL uid straight through
-- this index. The real guard for these two ALTERed columns is the Go insert path
-- (store.newUID) plus the invariant test in store_uid_test.go. Do NOT "fix" this
-- with ALTER TABLE ... ALTER COLUMN uid SET NOT NULL: modernc accepts it and
-- rewrites the stored schema, which makes it look right, but it is not SQLite
-- grammar (ADD/DROP/RENAME COLUMN only) and would bind crh.db to one driver --
-- users open this file with other tools. The portable alternative, a 12-step table
-- rebuild, needs foreign_keys=OFF, which is a no-op inside the transaction the
-- migration runner wraps each file in. See ADR-009 (c).
CREATE UNIQUE INDEX idx_lots_uid      ON lots(uid);
CREATE UNIQUE INDEX idx_roll_txns_uid ON roll_txns(uid);

-- Photos hang off EITHER a specimen (lots) or a box/roll purchase record
-- (roll_txns: the box end with its bank stamps, the wrapper, the receipt). Two per
-- item is a floor, not a ceiling -- a mintmark close-up, doubling, a slab label --
-- so this is a table, not a fixed obverse/reverse filename pair.
CREATE TABLE photos (
  id         INTEGER PRIMARY KEY,
  -- A NEW table can declare what the two ALTERed ones above cannot. SQLite
  -- enforces both halves here.
  uid        TEXT NOT NULL UNIQUE,
  owner_kind TEXT NOT NULL,             -- 'lot' | 'roll_txn'; makes an exported CSV self-describing
  owner_uid  TEXT NOT NULL,             -- lots.uid | roll_txns.uid; logical link, no FK (as 0004/0007/0008)
  -- NOT NULL because a NULL role LOSES PHOTOS: the gallery query filters
  -- `role NOT IN ('obverse','reverse')`, and NULL NOT IN (...) evaluates to NULL,
  -- not true -- so an un-roled image is in the database, on disk, and invisible in
  -- the app. Open vocabulary (obverse|reverse|detail|edge|slab-label|box-end|
  -- receipt|...), documented not enforced, per the ADR-006 precedent.
  role       TEXT NOT NULL DEFAULT 'detail',
  seq        INTEGER NOT NULL DEFAULT 0, -- lowest = cover; app assigns max(seq)+1 per owner
  ext        TEXT NOT NULL,              -- jpg|png|webp
  caption    TEXT,
  created    TEXT
);

-- ORDER BY seq alone is NOT a total order: seq defaults to 0, so a UI that never
-- assigns one leaves every photo tied, and SQLite's order among ties is
-- unspecified (verified: the same query over the same rows returns obverse/reverse
-- in either order under different plans). Every read is ORDER BY seq, uid -- and
-- uid is the last index column so that ordering is a covering-index scan rather
-- than a temp b-tree.
CREATE INDEX idx_photos_owner ON photos(owner_kind, owner_uid, seq, uid);
