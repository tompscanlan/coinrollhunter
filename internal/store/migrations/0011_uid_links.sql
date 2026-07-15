-- 0011_uid_links: move the four durable "which box / which branch" links off the
-- recyclable integer rowid and onto the never-recycled uid (om-c8ei, ADR-009).
--
-- THE BUG. roll_txns.id and branches.id are bare rowid aliases (INTEGER PRIMARY KEY
-- with no auto-increment), so SQLite hands out max(rowid)+1: delete the newest box
-- and the next box SILENTLY ADOPTS the dead box's finds and keepers, because those
-- children still store the OLD integer and a new row took it. Same for branches: an
-- old trip re-parents onto the replacement bank. The uid columns already on
-- roll_txns/branches (0008/0009) are never recycled, so a link that stores the uid
-- cannot be re-adopted -- a dangling one is merely blank. blank beats wrong.
--
-- WHY THE uid AND NOT A foreign-key. Foreign keys ARE enforced on this connection
-- (store.Open opens with _pragma=foreign_keys(1); lots.item_type_id is a live one),
-- so a key on these new columns WOULD fire -- and it would make every user database
-- that ALREADY holds an orphan fail to open, aborting inside this migration's own
-- transaction. That is the same mechanism om-1czp proved for a column check. So the
-- new uid columns are deliberately plain: no key, no not-null, no column check, no
-- auto-increment. The uid model needs none of them; a never-recycled key that
-- dangles just resolves to blank. (The absence of keys on these links has been
-- deliberate since 0008_branches.)
--
-- THE UNRECOVERABLE CLASS -- STATED PLAINLY, AND NOT "FIXED" HERE. The repoint below
-- resolves each stored integer through whatever row holds it TODAY:
--   * a correctly-linked row     -> freezes the RIGHT uid. Correct, forever.
--   * a true orphan (box deleted, rowid not reused) -> the subquery yields nothing,
--     so the uid ends null and the link resolves to blank. This is the accepted
--     outcome (blank beats wrong), and it is the ONLY class this migration can even
--     detect (integer set, resolved uid null).
--   * a SILENTLY RE-ADOPTED row (box deleted AND a later row took its rowid) ->
--     resolves to the WRONG box and is frozen there permanently. You CANNOT tell it
--     from a correct link: the child never recorded a uid, and the deleted box left
--     no tombstone. This migration therefore LAUNDERS any pre-existing re-adoption
--     into permanent, invisible corruption. There is no honest repair, and inventing
--     a matching heuristic (a find dated before its box, a 90%-silver dime under a
--     halves box) throws false positives on legitimate data and would silently unlink
--     a user's correct rows -- worse than the bug. The fix stops NEW re-adoption; it
--     cannot undo the old. A doctor command that reports the blanked class is a
--     separate follow-up bead, not this one.
--
-- No Go step runs inside a migration, so uids are generated in pure SQL with the
-- lowercase-UUIDv4 randomblob recipe 0008/0009/0010 proved (non-deterministic,
-- re-evaluates per row). Statement ORDER is load-bearing: backfill, then add, then
-- repoint (which reads the integer), then drop.

-- STEP 1 -- DEFENSIVE uid BACKFILL, BEFORE ANY REPOINT. roll_txns.uid was ALTERed in
-- by 0009 and carries no schema not-null (a UNIQUE index does not imply one -- SQLite
-- treats nulls as mutually distinct), and the store reads it as a nullable string on
-- exactly those grounds. A box with a null uid would blank every child that pointed
-- at it, so fill any null uid FIRST. branches.uid is already not-null, so its update
-- matches nothing -- kept for symmetry.
UPDATE roll_txns SET uid =
  lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
  substr(lower(hex(randomblob(2))), 2) || '-' ||
  substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
  lower(hex(randomblob(6)))
WHERE uid IS NULL;

UPDATE branches SET uid =
  lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
  substr(lower(hex(randomblob(2))), 2) || '-' ||
  substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
  lower(hex(randomblob(6)))
WHERE uid IS NULL;

-- STEP 2 -- ADD THE NEW TEXT COLUMNS (nullable, plain -- see the header). Index each:
-- they are the LEFT JOIN keys the read path now resolves through, once per load.
ALTER TABLE lots      ADD COLUMN roll_txn_uid TEXT;
ALTER TABLE keepers   ADD COLUMN roll_txn_uid TEXT;
ALTER TABLE roll_txns ADD COLUMN branch_uid   TEXT;
ALTER TABLE trips     ADD COLUMN branch_uid   TEXT;

CREATE INDEX idx_lots_roll_txn_uid    ON lots(roll_txn_uid);
CREATE INDEX idx_keepers_roll_txn_uid ON keepers(roll_txn_uid);
CREATE INDEX idx_roll_txns_branch_uid ON roll_txns(branch_uid);
CREATE INDEX idx_trips_branch_uid     ON trips(branch_uid);

-- STEP 3 -- REPOINT: resolve each stored integer through the row that holds it TODAY.
-- A dangling or absent integer resolves to null (a scalar subquery with no match is
-- null), landing as a blank link -- see the header on the three data states. The
-- update touches every row unconditionally: a row with no integer link resolves to
-- null, which is the value the freshly-added column already carries, so no guard is
-- needed (and none that would spell out a null predicate is wanted here).
UPDATE lots      SET roll_txn_uid = (SELECT rt.uid FROM roll_txns rt WHERE rt.id = lots.roll_txn_id);
UPDATE keepers   SET roll_txn_uid = (SELECT rt.uid FROM roll_txns rt WHERE rt.id = keepers.roll_txn_id);
UPDATE roll_txns SET branch_uid   = (SELECT b.uid  FROM branches  b  WHERE b.id  = roll_txns.branch_id);
UPDATE trips     SET branch_uid   = (SELECT b.uid  FROM branches  b  WHERE b.id  = trips.branch_id);

-- STEP 4 -- DROP THE OLD INTEGER LINKS (hard cutover, 0008's precedent: reversibility
-- is a database backup, not a dual-read window -- two links that can disagree is the
-- bug wearing a new hat). branch_aliases.branch_id STAYS an integer: it cannot orphan,
-- because DeleteBranch removes the aliases and the branch in one transaction and
-- MergeBranches repoints them before deleting the loser.
ALTER TABLE lots      DROP COLUMN roll_txn_id;
ALTER TABLE keepers   DROP COLUMN roll_txn_id;
ALTER TABLE roll_txns DROP COLUMN branch_id;
ALTER TABLE trips     DROP COLUMN branch_id;
