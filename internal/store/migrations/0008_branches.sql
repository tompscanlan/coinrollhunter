-- 0008_branches: promote the free-text bank string to a first-class branch entity
-- (ADR-010, om-35r3). A bank was a TEXT column on roll_txns and on trips, stored
-- twice for the same real-world place and never joined, so a typo forked a branch
-- across the "which banks pay off" yield table and the branch_count KPI. This
-- migration makes a branch a row with a stable id + an opaque uid (ADR-009), seeds
-- one per distinct trimmed non-empty bank string across roll_txns ∪ trips, keeps
-- the raw string as an alias (so an old export still resolves and a later merge can
-- repoint forks), and backfills a branch_id logical link on both tables.
--
-- Hard cutover (om-fmy4 review decision): the bank column is DROPPED in this same
-- migration. Provenance now lives in branch_aliases; reversibility is a database
-- backup, not a dual-read window. branch_id is a LOGICAL link, not a foreign key --
-- FKs are enforced on this connection (store.Open), so a real FK would make a
-- branch un-deletable while history points at it. Mirrors lots.roll_txn_id (0004)
-- and keepers.roll_txn_id (0007).

CREATE TABLE branches (
  id            INTEGER PRIMARY KEY,
  uid           TEXT NOT NULL UNIQUE,        -- ADR-009: opaque, never recycled
  name          TEXT NOT NULL,               -- canonical display name
  institution   TEXT,                        -- brand ("Chase", "Riverbend CU"), for grouping
  address       TEXT,
  phone         TEXT,                        -- call to order boxes ahead; address-book core
  lat           REAL,                        -- 0 until geocoded or entered by hand (ADR-011)
  lon           REAL,
  hours         TEXT,                        -- open vocabulary; structured windows → ADR-011
  buys          INTEGER NOT NULL DEFAULT 1,  -- sells boxes?    → eligible as a pickup stop
  dumps         INTEGER NOT NULL DEFAULT 1,  -- accepts returns? → eligible as a dropoff stop
  denoms        TEXT,                        -- denominations stocked ("halves,dimes")
  box_limit     INTEGER,                     -- max boxes they'll order per run; NULL = unknown
  box_lead_days INTEGER,                     -- order lead time; NULL = walk-in / unknown
  coin_fee_usd  REAL,                        -- per-order coin fee if any; a P&L line + a reason to skip
  cooldown_days INTEGER NOT NULL DEFAULT 0,  -- don't revisit inside this window
  notes         TEXT,                        -- teller names ("ask for Diane, Tues"), quirks
  active        INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE branch_aliases (
  branch_id INTEGER NOT NULL,
  alias     TEXT NOT NULL PRIMARY KEY
);

ALTER TABLE roll_txns ADD COLUMN branch_id INTEGER;
ALTER TABLE trips     ADD COLUMN branch_id INTEGER;

-- Seed one branch per distinct trimmed non-empty bank string across both tables.
-- The uid is generated in SQLite (the migration runner has no Go step) as a
-- lowercase UUIDv4 via randomblob (ADR-010 (c); the recipe also unblocks ADR-009's
-- om-hdk5 lots/roll_txns uid backfill, which faces the same no-Go-step constraint).
INSERT INTO branches (uid, name)
SELECT
  lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
  substr(lower(hex(randomblob(2))), 2) || '-' ||
  substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
  lower(hex(randomblob(6))),
  bank
FROM (
  SELECT DISTINCT trim(bank) AS bank FROM roll_txns WHERE trim(bank) <> ''
  UNION
  SELECT DISTINCT trim(bank) AS bank FROM trips     WHERE trim(bank) <> ''
);

-- The trimmed raw string is the branch's first alias, so a merge can repoint forks
-- and an old export/re-import still resolves through it.
INSERT INTO branch_aliases (branch_id, alias) SELECT id, name FROM branches;

UPDATE roll_txns SET branch_id = (
  SELECT b.id FROM branches b JOIN branch_aliases a ON a.branch_id = b.id
  WHERE a.alias = trim(roll_txns.bank)
) WHERE trim(bank) <> '';

UPDATE trips SET branch_id = (
  SELECT b.id FROM branches b JOIN branch_aliases a ON a.branch_id = b.id
  WHERE a.alias = trim(trips.bank)
) WHERE trim(bank) <> '';

-- Hard cutover: drop the free-text column. branch_id + branches.name is now the
-- single source of truth for a bank's identity and display name.
ALTER TABLE roll_txns DROP COLUMN bank;
ALTER TABLE trips     DROP COLUMN bank;
