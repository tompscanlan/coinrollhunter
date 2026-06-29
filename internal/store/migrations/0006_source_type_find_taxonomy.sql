-- 0006_source_type_find_taxonomy: ADR-006.
-- Acquisition source-type on buys, a find taxonomy on holdings, and a trophy flag.
-- All additive with safe defaults so existing rows read back as unknown/uncategorized.

-- roll_txns: how the coin was wrapped / acquired (MR/CR/box/bag/loose) — orthogonal to
-- `unit` (the quantity unit). Open vocabulary, no CHECK (matches action/denom/unit).
ALTER TABLE roll_txns ADD COLUMN source_type TEXT;

-- lots: denom-scoped find taxonomy for CRH finds (e.g. Proof / Silver / 2009 / PMD), plus a
-- trophy flag for the highlights feed. Vocabulary is documented (ADR-006), not enforced.
ALTER TABLE lots ADD COLUMN category    TEXT;
ALTER TABLE lots ADD COLUMN subcategory TEXT;
ALTER TABLE lots ADD COLUMN trophy      INTEGER NOT NULL DEFAULT 0;
