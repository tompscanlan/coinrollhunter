-- 0007_keeper_audit: make keepers auditable / box-attributable (om-co69, ADR-008).
-- Two NULLABLE, additive columns so existing keeper rows survive untouched (they
-- read back as NULL date / NULL roll_txn_id and compute exactly as before — the
-- clad-at-face float contribution is unchanged). No CHECK / FK enforcement:
-- roll_txn_id logically references roll_txns.id (a buy/box), mirroring the
-- lots.roll_txn_id link from migration 0004, but this is a local single-writer
-- store so a plain nullable column is enough.
--
-- Why: a keeper batch had no when/where, so a later Reconcile pass couldn't tell
-- if it was already counted (the date-independent double-count vector). With a
-- date + box, the UI can warn when keepers are added against a box/date that
-- already has activity=crh find lots recorded ("already recorded this session").
ALTER TABLE keepers ADD COLUMN date        TEXT;
ALTER TABLE keepers ADD COLUMN roll_txn_id INTEGER;
