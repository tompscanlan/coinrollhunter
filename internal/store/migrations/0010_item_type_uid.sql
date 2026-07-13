-- 0010_item_type_uid: give the catalog the same opaque, never-recycled identity the
-- specimens already have (ADR-009, om-9cua).
--
-- ADR-009 deferred this one deliberately and named the exact condition that would
-- undefer it: "it becomes a live problem only if export starts emitting
-- item_type_id as a foreign key". Export does. lots.item_type_id is the single most
-- important link in the schema -- it is what makes a lot a *1943-S cent* rather than
-- a number -- and item_type.id is a bare rowid alias: delete a catalog entry, insert
-- the next, and SQLite hands back the same integer. Every lot in an already-exported
-- spreadsheet would then point at the wrong coin TYPE, with no error and nothing to
-- detect it. That is strictly worse than the photo case ADR-009 was written for: it
-- changes what the coin IS.
--
-- Same recipe as 0008/0009 for the same reason: the migration runner has no Go step,
-- and randomblob() is non-deterministic and re-evaluates PER ROW, so one UPDATE
-- backfills every existing row with a distinct v4. Lowercase hex -- these are join
-- keys in files users open on Windows and macOS, where case-insensitive filesystems
-- and spreadsheet lookups make an uppercase variant a collision waiting to happen.
--
-- As in 0009, the ALTERed column gets no schema NOT NULL (SQLite's ALTER grammar
-- cannot add one portably, and a UNIQUE index does not imply it -- NULLs are
-- mutually distinct). The guarantee is store.InsertItemType plus uid_test.go.
-- Existing ids are untouched: this only fills a new column.

ALTER TABLE item_type ADD COLUMN uid TEXT;

UPDATE item_type SET uid =
  lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
  substr(lower(hex(randomblob(2))), 2) || '-' ||
  substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
  lower(hex(randomblob(6)))
WHERE uid IS NULL;

CREATE UNIQUE INDEX idx_item_type_uid ON item_type(uid);
