-- 0002: rename item_type.asw_oz -> fine_oz_each.
-- "ASW" (Actual Silver Weight) is silver-specific numismatic jargon, but the
-- column holds fine metal ounces per unit for ANY metal (gold's term would be
-- AGW, plus Pt/Pd). Rename to the metal-neutral name that matches the resolved
-- model.Lot.FineOzEach and the "Fine oz / unit" UI label. Troy ounces.
-- SQLite RENAME COLUMN preserves all existing data.
ALTER TABLE item_type RENAME COLUMN asw_oz TO fine_oz_each;
