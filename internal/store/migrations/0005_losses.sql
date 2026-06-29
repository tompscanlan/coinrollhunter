-- 0005: shrinkage / loss adjustments (ADR-005).
-- When the float (to_redeposit = bought − returned − kept) sits perpetually
-- nonzero because of machine miscounts, lost coins, or short deposits, the
-- Reconcile/close-the-books action books the genuine remainder as a loss here:
-- an honest, auditable expense line that drives the float to $0 and reduces CRH
-- net. Kept separate from roll_txns (a return is coin recovered; a loss is value
-- gone) — see ADR-005.
CREATE TABLE losses (
  id         INTEGER PRIMARY KEY,
  date       TEXT NOT NULL,           -- ISO date the period was closed
  amount_usd REAL NOT NULL,           -- face declared lost (drives the float to 0)
  reason     TEXT,                    -- "machine miscount", "short deposit", ...
  scope      TEXT                     -- free-text period/session/bank tag (audit gap #10 seam)
);
