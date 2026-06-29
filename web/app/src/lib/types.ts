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
  disposed?: string
  disposed_usd?: number
}

export interface RollTxn {
  id: number
  date: string
  bank: string
  action: 'buy' | 'return'
  denom: string // halves|quarters|dimes|nickels|cents
  unit: string // box|roll|face|coin
  amount: number
  face_usd: number
  notes: string
}

export interface Trip {
  id: number
  date: string
  bank: string
  miles: number
  hours: number
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
  fine_oz: number
  market_usd: number
  unreal_usd: number
  unreal_pct: number
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
  bank: string
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
  to_redeposit: number
  reconciled: boolean

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

/** Mirrors calc.Report.Verdict() — derived client-side (not serialized). */
export function verdict(r: Report): string {
  if (r.crh_net_real > 0) return 'PROFITABLE (cash basis)'
  if (r.crh_net_real === 0) return 'BREAK-EVEN'
  return 'COSTING MONEY'
}
