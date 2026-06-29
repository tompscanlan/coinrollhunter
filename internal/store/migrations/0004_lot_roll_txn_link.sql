-- 0004: link a holding to the roll transaction (box) it came from.
-- For CRH finds this attributes the silver to the exact box/bank/date it was
-- pulled from, enabling per-box and per-bank yield. NULL for bullion and any
-- unattributed find. Kept as a plain nullable column (logical link); FK
-- enforcement isn't needed for a local single-writer store.
ALTER TABLE lots ADD COLUMN roll_txn_id INTEGER;
