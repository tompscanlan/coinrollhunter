// Grid configurations — one per data table. Each bundles the TanStack columns
// (with edit metadata) plus the load/create/update/remove/blank wiring the
// EditableGrid needs. Most map straight to a CRUD resource; Holdings is special:
// it presents a *flat* spreadsheet row but writes the ADR-003 catalog/specimen
// split (find-or-create an item_type, then attach the holding).
import { api } from './api'
import { today } from './format'
import {
  DENOMS,
  ROLL_UNITS,
  METALS,
  SILVER_PRESETS,
  SOURCE_TYPES,
  FIND_CATEGORIES,
  FIND_SUBCATEGORIES,
} from './presets'
import type { GridColumn } from './components/EditableGrid.svelte'
import type { ItemType, Holding, RollTxn, Trip, Branch, Supply, Keeper, Loss } from './types'

// --- Autocomplete caches -----------------------------------------------------
// Refreshed by each grid's load(); the column suggestion/autofill closures read
// these so typing offers your own existing entries (plus the built-in presets).
let catalog: ItemType[] = [] // item_types — powers Product autocomplete + autofill
let holdingSources: string[] = [] // distinct dealer/source names from holdings
let banks: string[] = [] // distinct bank names, unioned across roll txns + trips
// Find-taxonomy autocomplete: the documented ADR-006 vocab unioned with whatever
// category/subcategory strings already exist in your holdings (open vocabulary).
let findCategories: string[] = [...FIND_CATEGORIES]
let findSubcategories: string[] = [...FIND_SUBCATEGORIES]

const distinct = (xs: (string | undefined)[]) =>
  [...new Set(xs.map((s) => (s ?? '').trim()).filter(Boolean))].sort((a, b) => a.localeCompare(b))

const grossWeightToFineOz = (gross: number, purity: number, unit?: string): number => {
  if (!gross || !purity) return 0
  switch ((unit ?? 'ozt').trim().toLowerCase()) {
    case 'g':
    case 'gram':
    case 'grams':
      return (gross / 31.1034768) * purity
    case 'kg':
    case 'kilogram':
    case 'kilograms':
      return (gross * 1000.0 / 31.1034768) * purity
    default:
      return gross * purity
  }
}

/** Map a Product value (existing item_type name OR a silver-preset label) to the
    metal/fineness/fine-oz it implies, given a catalog. Catalog entries win over
    presets on name. Pure — reused by the Do-tab workflows, which pass their own
    freshly-fetched catalog. */
export function productAutofillFrom(
  value: string,
  cat: ItemType[],
): Pick<FlatHolding, 'metal' | 'fineness' | 'fine_oz_each'> | undefined {
  const v = norm(value)
  const t = cat.find((c) => norm(c.name) === v)
  if (t) return { metal: t.metal, fineness: t.fineness, fine_oz_each: t.fine_oz_each }
  const p = SILVER_PRESETS.find((p) => norm(p.label) === v)
  if (p) return { metal: 'silver', fineness: p.fineness, fine_oz_each: p.fine_oz_each }
  return undefined
}

/** Suggestion list for the Product field: catalog names + preset labels. */
export function productSuggestionsFrom(cat: ItemType[]): string[] {
  return distinct([...cat.map((c) => c.name), ...SILVER_PRESETS.map((p) => p.label)])
}

// Grid-column closures read the module-level `catalog` cache (populated by the
// Holdings grid's load()); the exported pure variants above take an explicit one.
const productAutofill = (value: string) => productAutofillFrom(value, catalog)
const productSuggestions = () => productSuggestionsFrom(catalog)

/** Load a bank-bearing table and fold its bank names into the shared cache, so
    the Bank field on roll txns and trips suggests every branch you've used — which
    nudges reuse of an existing branch instead of forking a new one on a typo. Also
    pulls the full branch list so branches with no transactions yet still suggest. */
async function loadCachingBanks<T extends { bank: string }>(list: () => Promise<T[]>): Promise<T[]> {
  const [rows, branchList] = await Promise.all([list(), api.branches.list()])
  banks = distinct([...banks, ...rows.map((r) => r.bank), ...branchList.map((b) => b.name)])
  return rows
}

// Box picker for CRH finds: the buy roll-txns you can attribute a find to.
let boxOpts: { value: string; label: string }[] = [{ value: '', label: '— (none)' }]
const fmtBox = (t: RollTxn) =>
  [`#${t.id}`, t.bank, t.denom, (t.date || '').slice(5)].filter(Boolean).join(' · ')
const boxOptions = () => boxOpts

export interface GridConfig<T extends { id: number }> {
  title: string
  description: string
  columns: GridColumn<T>[]
  load: () => Promise<T[]>
  create: (row: Omit<T, 'id'>) => Promise<number>
  update: (id: number, row: Omit<T, 'id'>) => Promise<void>
  remove: (id: number) => Promise<void>
  blank: () => Omit<T, 'id'>
}

// --- Holdings (flat view over item_type + holding) ---------------------------

/** The flat row the spreadsheet edits — joins a holding to its item type.
    The CRH find taxonomy (category/subcategory/trophy, ADR-006) is optional so
    bullion callers (NewBullion) can omit it; toHolding coerces the defaults. */
export interface FlatHolding {
  id: number
  activity: 'bullion' | 'crh'
  product: string
  metal: string
  fineness: string
  fine_oz_each: number
  gross_weight?: number
  purity?: number
  weight_unit?: string
  qty: number
  basis_usd: number
  premium_usd: number
  face_value_usd: number
  acquired: string
  source: string
  from_box: string // roll_txn id (as string) this CRH find came from; '' = none
  category?: string // CRH find taxonomy (ADR-006) — only meaningful for activity='crh'
  subcategory?: string
  trophy?: boolean
}

const norm = (s: string) => (s ?? '').trim().toLowerCase()

/** Find an item_type matching name+metal+fineness, else create one. Updates the
    catalog's fine_oz_each if it drifted. Returns the type id. */
async function ensureItemType(row: Omit<FlatHolding, 'id'>): Promise<number> {
  const types = await api.itemTypes.list()
  const match = types.find(
    (t) =>
      norm(t.name) === norm(row.product) &&
      norm(t.metal) === norm(row.metal) &&
      norm(t.fineness) === norm(row.fineness),
  )
  if (match) {
    if (Math.abs((match.fine_oz_each ?? 0) - (row.fine_oz_each ?? 0)) > 1e-9) {
      await api.itemTypes.update(match.id, { ...match, fine_oz_each: row.fine_oz_each } as Omit<ItemType, 'id'>)
    }
    return match.id
  }
  const kind = row.activity === 'crh' ? 'junk' : 'coin'
  return api.itemTypes.create({
    kind,
    name: row.product || 'Unnamed',
    metal: row.metal,
    fine_oz_each: row.fine_oz_each,
    fineness: row.fineness,
  } as Omit<ItemType, 'id'>)
}

function toHolding(row: Omit<FlatHolding, 'id'>, item_type_id: number): Omit<Holding, 'id'> {
  return {
    item_type_id,
    roll_txn_id: Number(row.from_box) || 0,
    activity: row.activity,
    qty: Number(row.qty) || 0,
    gross_weight: row.gross_weight,
    purity: row.purity,
    weight_unit: row.weight_unit,
    basis_usd: Number(row.basis_usd) || 0,
    premium_usd: Number(row.premium_usd) || 0,
    face_value_usd: Number(row.face_value_usd) || 0,
    acquired: row.acquired,
    source: row.source,
    category: row.category ?? '',
    subcategory: row.subcategory ?? '',
    trophy: Boolean(row.trophy),
    notes: '',
  }
}

export const holdingsGrid: GridConfig<FlatHolding> = {
  title: 'Holdings',
  description:
    'Bullion lots and CRH finds. Type the product, metal, fineness and metal-oz; we keep the catalog tidy for you.',
  columns: [
    { accessorKey: 'activity', header: 'Activity', meta: { editor: 'select', options: ['bullion', 'crh'], width: '110px' } },
    {
      accessorKey: 'product',
      header: 'Product',
      meta: {
        editor: 'autocomplete',
        width: '430px',
        placeholder: '1 oz Gold Eagle',
        suggestions: productSuggestions,
        autofill: productAutofill,
      },
    },
    { accessorKey: 'metal', header: 'Metal', meta: { editor: 'select', options: METALS, width: '110px' } },
    { accessorKey: 'fineness', header: 'Fineness', meta: { editor: 'text', width: '100px', placeholder: '.9999' } },
    { accessorKey: 'fine_oz_each', header: 'Fine oz / unit', meta: { editor: 'number', step: 0.0001, align: 'right', width: '120px' } },
    { accessorKey: 'gross_weight', header: 'Gross wt', meta: { editor: 'number', step: 0.01, align: 'right', width: '100px', placeholder: '0' } },
    { accessorKey: 'purity', header: 'Purity', meta: { editor: 'number', step: 0.0001, align: 'right', width: '100px', placeholder: '.999' } },
    { accessorKey: 'weight_unit', header: 'Unit', meta: { editor: 'select', options: ['ozt', 'g', 'kg'], width: '90px' } },
    { accessorKey: 'qty', header: 'Qty', meta: { editor: 'number', step: 1, align: 'right', width: '80px' } },
    { accessorKey: 'basis_usd', header: 'Basis $', meta: { editor: 'number', step: 0.01, align: 'right', width: '110px' } },
    { accessorKey: 'premium_usd', header: 'Premium $', meta: { editor: 'number', step: 0.01, align: 'right', width: '110px' } },
    { accessorKey: 'face_value_usd', header: 'Face $', meta: { editor: 'number', step: 0.01, align: 'right', width: '100px' } },
    { accessorKey: 'acquired', header: 'Acquired', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'source', header: 'Source', meta: { editor: 'autocomplete', placeholder: 'APMEX', suggestions: () => holdingSources } },
    { accessorKey: 'from_box', header: 'From box (CRH)', meta: { editor: 'select', optionsFn: boxOptions, width: '190px' } },
    // CRH find taxonomy (ADR-006) — denom-scoped open vocabulary; the dropdowns
    // suggest the documented buckets plus whatever you've already used.
    { accessorKey: 'category', header: 'Category (CRH)', meta: { editor: 'autocomplete', width: '150px', placeholder: 'Silver', suggestions: () => findCategories } },
    { accessorKey: 'subcategory', header: 'Subcat (CRH)', meta: { editor: 'autocomplete', width: '150px', placeholder: 'Mercury', suggestions: () => findSubcategories } },
    { accessorKey: 'trophy', header: 'Trophy', meta: { editor: 'checkbox', width: '90px' } },
  ],
  load: async () => {
    const [types, holdings, rolls] = await Promise.all([
      api.itemTypes.list(),
      api.holdings.list(),
      api.rollTxns.list(),
    ])
    catalog = types
    holdingSources = distinct(holdings.map((h) => h.source))
    // Seed the taxonomy dropdowns from the documented vocab + your own entries.
    findCategories = distinct([...FIND_CATEGORIES, ...holdings.map((h) => h.category)])
    findSubcategories = distinct([...FIND_SUBCATEGORIES, ...holdings.map((h) => h.subcategory)])
    boxOpts = [
      { value: '', label: '— (none)' },
      ...rolls.filter((r) => r.action === 'buy').map((r) => ({ value: String(r.id), label: fmtBox(r) })),
    ]
    const byId = new Map<number, ItemType>(types.map((t) => [t.id, t]))
    return holdings.map((h) => {
      const t = byId.get(h.item_type_id)
      return {
        id: h.id,
        activity: h.activity,
        product: t?.name ?? '',
        metal: t?.metal ?? '',
        fineness: t?.fineness ?? '',
        fine_oz_each:
          (t?.fine_oz_each ?? 0) || grossWeightToFineOz(h.gross_weight ?? 0, h.purity ?? 0, h.weight_unit),
        gross_weight: h.gross_weight,
        purity: h.purity,
        weight_unit: h.weight_unit,
        qty: h.qty,
        basis_usd: h.basis_usd,
        premium_usd: h.premium_usd ?? 0,
        face_value_usd: h.face_value_usd,
        acquired: h.acquired,
        source: h.source,
        from_box: h.roll_txn_id ? String(h.roll_txn_id) : '',
        category: h.category ?? '',
        subcategory: h.subcategory ?? '',
        trophy: Boolean(h.trophy),
      } satisfies FlatHolding
    })
  },
  create: async (row) => {
    const tid = await ensureItemType(row)
    return api.holdings.create(toHolding(row, tid))
  },
  update: async (id, row) => {
    const tid = await ensureItemType(row)
    await api.holdings.update(id, toHolding(row, tid))
  },
  remove: (id) => api.holdings.remove(id),
  blank: () => ({
    activity: 'bullion',
    product: '',
    metal: 'silver',
    fineness: '',
    fine_oz_each: 0,
    gross_weight: 0,
    purity: 0,
    weight_unit: 'ozt',
    qty: 1,
    basis_usd: 0,
    premium_usd: 0,
    face_value_usd: 0,
    acquired: today(),
    source: '',
    from_box: '',
    category: '',
    subcategory: '',
    trophy: false,
  }),
}

// --- Plain CRUD tables -------------------------------------------------------

/** source_type is a buy-only attribute — blank it when the row is (or becomes)
    a return, so flipping a buy to a return can't leave a stale wrap class behind. */
const dropReturnSourceType = (row: Omit<RollTxn, 'id'>): Omit<RollTxn, 'id'> =>
  row.action === 'return' ? { ...row, source_type: '' } : row

export const rollTxnsGrid: GridConfig<RollTxn> = {
  title: 'Roll transactions',
  description:
    'Boxes/rolls bought and culls returned. face_usd is the source of truth; box throughput is derived from it. Source-type is the wrapping/yield class (ADR-006), orthogonal to unit.',
  columns: [
    { accessorKey: 'date', header: 'Date', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'bank', header: 'Bank', meta: { editor: 'autocomplete', placeholder: 'Stock Yards', suggestions: () => banks } },
    { accessorKey: 'action', header: 'Action', meta: { editor: 'select', options: ['buy', 'return'], width: '100px' } },
    { accessorKey: 'denom', header: 'Denom', meta: { editor: 'select', options: DENOMS, width: '110px' } },
    { accessorKey: 'unit', header: 'Unit', meta: { editor: 'select', options: ROLL_UNITS, width: '90px' } },
    // Buy-only attribute: a 'return' is just face going back to the bank, so the
    // cell renders inert ("—") on return rows (om-kn0f).
    { accessorKey: 'source_type', header: 'Source', meta: { editor: 'select', optionsFn: () => SOURCE_TYPES, width: '160px', enabled: (r) => r.action !== 'return' } },
    { accessorKey: 'amount', header: 'Amount', meta: { editor: 'number', step: 0.1, align: 'right', width: '90px' } },
    { accessorKey: 'face_usd', header: 'Face $', meta: { editor: 'number', step: 0.01, align: 'right', width: '110px' } },
    { accessorKey: 'notes', header: 'Notes', meta: { editor: 'text' } },
  ],
  // Normalize source_type to '' (the Go side omits it when empty) so the select binds cleanly.
  load: async () => (await loadCachingBanks(api.rollTxns.list)).map((r) => ({ ...r, source_type: r.source_type ?? '' })),
  create: (row) => api.rollTxns.create(dropReturnSourceType(row)),
  update: (id, row) => api.rollTxns.update(id, dropReturnSourceType(row)),
  remove: api.rollTxns.remove,
  blank: () => ({ date: today(), bank: '', action: 'buy', denom: 'halves', unit: 'box', source_type: '', amount: 1, face_usd: 500, notes: '' }),
}

export const tripsGrid: GridConfig<Trip> = {
  title: 'Bank trips',
  description: 'Sourcing runs. Miles drive the mileage-based gas cost — the real CRH expense.',
  columns: [
    { accessorKey: 'date', header: 'Date', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'bank', header: 'Bank', meta: { editor: 'autocomplete', placeholder: 'Commonwealth', suggestions: () => banks } },
    { accessorKey: 'miles', header: 'Round-trip miles', meta: { editor: 'number', step: 0.1, align: 'right', width: '150px' } },
    { accessorKey: 'hours', header: 'Hours', meta: { editor: 'number', step: 0.25, align: 'right', width: '110px' } },
  ],
  load: () => loadCachingBanks(api.trips.list),
  create: api.trips.create,
  update: api.trips.update,
  remove: api.trips.remove,
  blank: () => ({ date: today(), bank: '', miles: 0, hours: 0 }),
}

// --- Branches (the address book, ADR-010) ------------------------------------
// The searchable notebook of your branches: phone/hours/fees/denoms/box limits and
// — the highest-value field — teller notes. buys/dumps say whether a branch is a
// pickup and/or dropoff stop (they feed routing later). uid is server-managed and
// lat/lon are geocoded by a later slice (om-w2tm), so neither is edited here. Merge
// duplicate branches with the per-row "Merge into…" action.
export const branchesGrid: GridConfig<Branch> = {
  title: 'Branches',
  description:
    'Your bank address book — phone, hours, coin fee, which denoms they stock, box limit/lead time, cooldown, and teller notes. Duplicate spellings from before? Use the row’s “Merge into…” action to fold them together.',
  columns: [
    { accessorKey: 'name', header: 'Name', meta: { editor: 'autocomplete', width: '200px', placeholder: 'Chase — Main St', suggestions: () => banks } },
    { accessorKey: 'institution', header: 'Institution', meta: { editor: 'text', width: '150px', placeholder: 'Chase' } },
    { accessorKey: 'phone', header: 'Phone', meta: { editor: 'text', width: '140px', placeholder: '(502) 555-0134' } },
    { accessorKey: 'address', header: 'Address', meta: { editor: 'text', width: '220px' } },
    { accessorKey: 'hours', header: 'Hours', meta: { editor: 'text', width: '160px', placeholder: 'M–F 9–5, Sat 9–12' } },
    { accessorKey: 'denoms', header: 'Stocks', meta: { editor: 'text', width: '140px', placeholder: 'halves,dimes' } },
    { accessorKey: 'buys', header: 'Buys', meta: { editor: 'checkbox', width: '80px' } },
    { accessorKey: 'dumps', header: 'Dumps', meta: { editor: 'checkbox', width: '80px' } },
    { accessorKey: 'box_limit', header: 'Box limit', meta: { editor: 'number', step: 1, align: 'right', width: '100px', placeholder: '0' } },
    { accessorKey: 'box_lead_days', header: 'Lead days', meta: { editor: 'number', step: 1, align: 'right', width: '100px', placeholder: '0' } },
    { accessorKey: 'coin_fee_usd', header: 'Coin fee $', meta: { editor: 'number', step: 0.01, align: 'right', width: '110px', placeholder: '0' } },
    { accessorKey: 'cooldown_days', header: 'Cooldown', meta: { editor: 'number', step: 1, align: 'right', width: '100px', placeholder: '0' } },
    { accessorKey: 'notes', header: 'Teller notes', meta: { editor: 'text', width: '240px', placeholder: 'ask for Diane, Tuesdays' } },
    { accessorKey: 'active', header: 'Active', meta: { editor: 'checkbox', width: '80px' } },
  ],
  load: async () => {
    const rows = await api.branches.list()
    banks = distinct([...banks, ...rows.map((b) => b.name)])
    return rows
  },
  create: api.branches.create,
  update: api.branches.update,
  remove: api.branches.remove,
  // A new branch defaults to buys+dumps+active on (uncheck to narrow); uid/lat/lon
  // are server/geocoder-owned and carried as inert zeros.
  blank: () => ({
    uid: '', name: '', institution: '', address: '', phone: '', lat: 0, lon: 0, hours: '',
    buys: true, dumps: true, denoms: '', box_limit: 0, box_lead_days: 0, coin_fee_usd: 0,
    cooldown_days: 0, notes: '', active: true,
  }),
}

export const suppliesGrid: GridConfig<Supply> = {
  title: 'Supplies',
  description: 'Consumables: tubes, flips, coin wrappers.',
  columns: [
    { accessorKey: 'date', header: 'Date', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'item', header: 'Item', meta: { editor: 'text', placeholder: 'Coin tubes' } },
    { accessorKey: 'cost_usd', header: 'Cost $', meta: { editor: 'number', step: 0.01, align: 'right', width: '120px' } },
  ],
  load: api.supplies.list,
  create: api.supplies.create,
  update: api.supplies.update,
  remove: api.supplies.remove,
  blank: () => ({ date: today(), item: '', cost_usd: 0 }),
}

export const keepersGrid: GridConfig<Keeper> = {
  title: 'Keepers',
  description:
    'Bulk / uncategorized clad pulled at face. Recoverable, not a loss — kept out of the redeposit float. Individually-notable coins (silver OR clad) belong in Holdings as a taxonomy find, not here (ADR-008). Date/box are optional audit context.',
  columns: [
    { accessorKey: 'denom', header: 'Denom', meta: { editor: 'select', options: DENOMS, width: '120px' } },
    { accessorKey: 'count', header: 'Count', meta: { editor: 'number', step: 1, align: 'right', width: '110px' } },
    { accessorKey: 'face_usd', header: 'Face $', meta: { editor: 'number', step: 0.01, align: 'right', width: '120px' } },
    { accessorKey: 'date', header: 'Date', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'roll_txn_id', header: 'Box', meta: { editor: 'number', step: 1, align: 'right', width: '90px' } },
  ],
  load: api.keepers.list,
  create: api.keepers.create,
  update: api.keepers.update,
  remove: api.keepers.remove,
  blank: () => ({ denom: 'halves', count: 0, face_usd: 0, date: today(), roll_txn_id: 0 }),
}

export const lossesGrid: GridConfig<Loss> = {
  title: 'Losses (shrinkage)',
  description:
    'Face written off at reconcile — machine miscounts, lost coins, short deposits. Honest, auditable, and correctable: delete a row if the coins resurface and the float reopens (ADR-005).',
  columns: [
    { accessorKey: 'date', header: 'Date', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'amount_usd', header: 'Lost $', meta: { editor: 'number', step: 0.01, align: 'right', width: '120px' } },
    { accessorKey: 'reason', header: 'Reason', meta: { editor: 'text', placeholder: 'machine miscount' } },
    { accessorKey: 'scope', header: 'Scope', meta: { editor: 'text', placeholder: 'June halves run' } },
  ],
  load: api.losses.list,
  create: api.losses.create,
  update: api.losses.update,
  remove: api.losses.remove,
  blank: () => ({ date: today(), amount_usd: 0, reason: '', scope: '' }),
}
