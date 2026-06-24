-- 0001_init: catalog/specimen store (ADR-001 + ADR-003).
-- Applied when PRAGMA user_version < 1.

-- Catalog: reference data for a kind of thing, entered once, shared by holdings.
CREATE TABLE item_type (
  id        INTEGER PRIMARY KEY,
  kind      TEXT NOT NULL,            -- coin|round|bar|junk|jewelry|other
  name      TEXT NOT NULL,
  metal     TEXT NOT NULL,            -- gold|silver|platinum|palladium
  asw_oz    REAL NOT NULL DEFAULT 0,  -- actual metal weight per unit (0 => derive gross*purity)
  fineness  TEXT,
  year      TEXT,
  mint      TEXT,
  mintmark  TEXT,
  refs      TEXT                      -- JSON: {"pcgs":..., "km":...}
);

-- Holdings (specimens): what you own, pointing at an item_type.
CREATE TABLE lots (
  id             INTEGER PRIMARY KEY,
  item_type_id   INTEGER NOT NULL REFERENCES item_type(id),
  activity       TEXT NOT NULL,           -- 'bullion' | 'crh'
  qty            REAL NOT NULL,
  gross_weight   REAL NOT NULL DEFAULT 0, -- per unit; with purity derives fine oz when asw_oz=0
  purity         REAL NOT NULL DEFAULT 0, -- 0..1
  weight_unit    TEXT,                    -- ozt|g|kg
  basis_usd      REAL NOT NULL,           -- total paid (face for CRH finds)
  premium_usd    REAL NOT NULL DEFAULT 0,
  face_value_usd REAL NOT NULL DEFAULT 0,
  acquired       TEXT NOT NULL,           -- ISO date
  source         TEXT,
  location       TEXT,                    -- custody: home safe, SDB, depository
  insured_value  REAL NOT NULL DEFAULT 0,
  attributes     TEXT,                    -- JSON escape hatch (grade, cert#, gemstone, hallmark)
  notes          TEXT,
  disposed       TEXT,                    -- ISO date if sold
  disposed_usd   REAL NOT NULL DEFAULT 0
);
CREATE INDEX idx_lots_item_type ON lots(item_type_id);
CREATE INDEX idx_lots_activity ON lots(activity);

-- CRH roll flow; face_usd is the normalized source of truth (ADR-001 R7).
CREATE TABLE roll_txns (
  id       INTEGER PRIMARY KEY,
  date     TEXT NOT NULL,
  bank     TEXT,
  action   TEXT NOT NULL,        -- 'buy' | 'return'
  denom    TEXT,                 -- halves|quarters|dimes|nickels|cents
  unit     TEXT,                 -- box|roll|face|coin (entry unit)
  amount   REAL,                 -- quantity in that unit
  face_usd REAL NOT NULL,
  notes    TEXT
);

CREATE TABLE trips    ( id INTEGER PRIMARY KEY, date TEXT, bank TEXT, miles REAL NOT NULL DEFAULT 0, hours REAL NOT NULL DEFAULT 0 );
CREATE TABLE supplies ( id INTEGER PRIMARY KEY, date TEXT, item TEXT, cost_usd REAL NOT NULL DEFAULT 0 );
CREATE TABLE keepers  ( id INTEGER PRIMARY KEY, denom TEXT, count INTEGER NOT NULL DEFAULT 0, face_usd REAL NOT NULL DEFAULT 0 );
CREATE TABLE spot     ( as_of TEXT PRIMARY KEY, gold_usd REAL NOT NULL DEFAULT 0, silver_usd REAL NOT NULL DEFAULT 0, source TEXT );
CREATE TABLE settings ( key TEXT PRIMARY KEY, value TEXT );
