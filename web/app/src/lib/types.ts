// Types mirror the Go JSON (internal/model + internal/calc). Field names match
// the Go `json:` tags exactly — snake_case — so responses map straight through.

export interface Spot {
  as_of: string
  gold_usd: number
  silver_usd: number
  platinum_usd: number
  palladium_usd: number
  source: string
}

export interface ItemType {
  id: number
  kind: string // coin|round|bar|junk|jewelry|other
  name: string
  metal: string // gold|silver|platinum|palladium
  fine_oz_each: number // fine metal oz per unit, troy (0 => derived from gross*purity)
  fineness: string
  year?: string
  mint?: string
  mintmark?: string
  references?: string
}

export interface Holding {
  id: number
  item_type_id: number
  roll_txn_id?: number // box this find came from (0/absent = none)
  activity: 'bullion' | 'crh'
  qty: number
  gross_weight?: number
  purity?: number
  weight_unit?: string
  basis_usd: number
  premium_usd?: number
  face_value_usd: number
  acquired: string
  source: string
  location?: string
  insured_value?: number
  attributes?: string
  notes: string
  // CRH find taxonomy (ADR-006) — meaningful for activity='crh' rows.
  category?: string // e.g. "Silver" | "PMD" | "Error" | "Key Date"
  subcategory?: string // e.g. "Mercury" | "parking lot" | "major"
  trophy?: boolean // flags a notable find for the highlights feed
  disposed?: string
  disposed_usd?: number
}

export interface RollTxn {
  id: number
  date: string
  bank: string // resolved branch name; typing it find-or-creates a branch (ADR-010)
  branch_id?: number // logical link to a branches row (0/absent = none)
  action: 'buy' | 'return'
  denom: string // dollars|halves|quarters|dimes|nickels|cents
  unit: string // box|roll|bag|face|coin
  amount: number
  face_usd: number
  // How the coin was wrapped/acquired — the high-signal yield axis (ADR-006),
  // orthogonal to unit: machine_roll|customer_roll|box|bag|loose ('' = unknown).
  source_type?: string
  notes: string
}

export interface Trip {
  id: number
  date: string
  bank: string // resolved branch name (see RollTxn.bank)
  branch_id?: number
  miles: number
  hours: number
}

/** A bank branch as a first-class entity (ADR-010): the address book plus the
    pickup/dropoff eligibility and cooldown that later feed routing. uid is opaque
    and server-managed; lat/lon stay 0 until geocoded (ADR-011). */
export interface Branch {
  id: number
  uid: string
  name: string
  institution: string
  address: string
  phone: string
  lat: number
  lon: number
  hours: string
  buys: boolean
  dumps: boolean
  denoms: string
  box_limit: number
  box_lead_days: number
  coin_fee_usd: number
  cooldown_days: number
  notes: string
  active: boolean
}

export interface Supply {
  id: number
  date: string
  item: string
  cost_usd: number
}

export interface Keeper {
  id: number
  denom: string
  count: number
  face_usd: number
  // Auditable dimension (migration 0007, ADR-008) — which session/box a bulk-clad
  // batch was logged against. Both nullable; legacy keepers omit them.
  date?: string // ISO date the batch was logged
  roll_txn_id?: number // box (buy) this batch is attributed to (0/absent = none)
}

/** A shrinkage write-off booked at reconcile — face declared lost (ADR-005). */
export interface Loss {
  id: number
  date: string
  amount_usd: number
  reason: string
  scope: string
}

export interface Settings {
  value_time: boolean
  hourly_rate_usd: number
  irs_mileage_rate_usd_per_mile: number
  silver_buyback_factor_40pct: number
  silver_buyback_factor_90pct: number
  box_face_usd: Record<string, number>
}

export interface EnrichedLot {
  id: number
  roll_txn_id?: number // box this find came from (0/absent = none) — used to flag keeper double-counts
  activity: 'bullion' | 'crh'
  product: string
  metal: string
  fineness: string
  qty: number
  fine_oz_each: number
  basis_usd: number
  face_value_usd: number
  acquired: string
  source: string
  premium_usd?: number // paid over melt at acquisition; a component of basis, display-only (omitted when 0)
  // CRH find taxonomy (ADR-006) — present on activity='crh' lots (omitted when empty/false).
  category?: string
  subcategory?: string
  trophy?: boolean
  fine_oz: number
  market_usd: number
  unreal_usd: number
  unreal_pct: number | null // null when basis is 0 (undefined %; rendered "n/a")
}

/** A sold holding with realized gain (proceeds - basis). */
export interface RealizedLot {
  id: number
  activity: 'bullion' | 'crh'
  product: string
  metal: string
  qty: number
  basis_usd: number
  proceeds_usd: number
  disposed: string
  gain_usd: number
}

/** Per-box find attribution — which banks/boxes actually produced silver. */
export interface BoxYield {
  roll_txn_id: number
  date: string
  bank: string // resolved branch name (ADR-010)
  branch_id?: number // stable grouping key; survives a rename/merge
  denom: string
  face_usd: number
  find_count: number
  find_oz: number
  find_value_usd: number
  yield_pct: number
}

/** The computed summary from GET /api/summary (calc.Report). */
export interface Report {
  spot: Spot
  lots: EnrichedLot[]

  bullion_basis: number
  bullion_market: number
  bullion_unreal: number
  bullion_pct: number
  gold_oz: number
  gold_basis: number
  gold_market: number

  find_oz: number
  find_cost: number
  find_melt: number
  find_realizable: number

  gas: number
  hours: number
  supplies: number
  op_cost: number
  losses: number // shrinkage write-offs (ADR-005)

  buys: number
  returns: number
  clad_face: number
  kept_face: number
  // Face (basis) of SOLD CRH finds that stays on the kept side of the float
  // (ADR-008 (c)). A component of kept_face only — NOT part of CRH net or total
  // basis (those are live-only): kept_face folds it in alongside clad_face and find_cost.
  disposed_find_face: number
  to_redeposit: number
  reconciled: boolean

  // Activity KPIs (ADR-006): coarse "how much hunting" stats over buy txns.
  buy_count: number
  branch_count: number
  avg_buy_usd: number

  boxes_by_denom: Record<string, number>
  total_boxes: number
  face_searched: number
  box_yields: BoxYield[]

  crh_net_melt: number
  crh_net_real: number
  crh_net_time: number
  hourly_rate: number

  realized: RealizedLot[]
  realized_proceeds: number
  realized_basis: number
  realized_gain: number

  total_basis: number
  total_market: number
  total_unreal: number
}

// --- Hit-rate report (GET /api/finds-report, calc.FindsReport, ADR-006) -------
// The "1 per face $" view: per denom × find category × acquisition source, how
// many face dollars you must search to find one. Every cell carries its sample
// size (count) and a low_confidence flag — a point estimate is misleading at small N.

/** One (category|subcategory) × source hit-rate cell. */
export interface SourceCell {
  source: string
  count: number
  hit_per_face: number // 0 when count is 0 (treat as N/A)
  low_confidence: boolean
}

export interface SubcategoryReport {
  subcategory: string
  count: number
  hit_per_face: number
  low_confidence: boolean
  by_source: SourceCell[]
}

export interface CategoryReport {
  category: string
  count: number
  hit_per_face: number
  low_confidence: boolean
  by_source: SourceCell[]
  subcategories?: SubcategoryReport[]
}

/** The hit-rate grid for one denomination. */
export interface DenomReport {
  denom: string
  face_searched: number
  coins_searched: number
  face_by_source: Record<string, number>
  categories: CategoryReport[]
}

/** The full hit-rate view (GET /api/finds-report). */
export interface FindsReport {
  total_face_searched: number
  low_confidence_n: number
  sources: string[] // source_types present, canonical order (high-yield first)
  unattributed: number // finds (coins) with no linked buy
  denoms: DenomReport[]
}

/** Mirrors calc.Report.Verdict() — derived client-side (not serialized). */
export function verdict(r: Report): string {
  if (r.crh_net_real > 0) return 'PROFITABLE (cash basis)'
  if (r.crh_net_real === 0) return 'BREAK-EVEN'
  return 'COSTING MONEY'
}
