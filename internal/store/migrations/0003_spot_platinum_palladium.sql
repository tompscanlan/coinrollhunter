-- 0003: platinum & palladium spot prices.
-- The metal dropdown already offers platinum/palladium, but calc.spotFor only
-- knew gold/silver — so those holdings were valued at $0 (a silent -100%). Add
-- manual price columns; manual entry is the permanent offline fallback (ADR-002).
ALTER TABLE spot ADD COLUMN platinum_usd  REAL NOT NULL DEFAULT 0;
ALTER TABLE spot ADD COLUMN palladium_usd REAL NOT NULL DEFAULT 0;
