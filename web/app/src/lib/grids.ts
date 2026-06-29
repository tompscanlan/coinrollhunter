// Grid configurations — one per data table. Each bundles the TanStack columns
// (with edit metadata) plus the load/create/update/remove/blank wiring the
// EditableGrid needs. Most map straight to a CRUD resource; Holdings is special:
// it presents a *flat* spreadsheet row but writes the ADR-003 catalog/specimen
// split (find-or-create an item_type, then attach the holding).
import { api } from './api'
import { today } from './format'
import { DENOMS, ROLL_UNITS, METALS, SILVER_PRESETS } from './presets'
import type { GridColumn } from './components/EditableGrid.svelte'
import type { ItemType, Holding, RollTxn, Trip, Supply, Keeper, Loss } from './types'

// --- Autocomplete caches -----------------------------------------------------
// Refreshed by each grid's load(); the column suggestion/autofill closures read
// these so typing offers your own existing entries (plus the built-in presets).
let catalog: ItemType[] = [] // item_types — powers Product autocomplete + autofill
let holdingSources: string[] = [] // distinct dealer/source names from holdings
let banks: string[] = [] // distinct bank names, unioned across roll txns + trips

const distinct = (xs: (string | undefined)[]) =>
  [...new Set(xs.map((s) => (s ?? '').trim()).filter(Boolean))].sort((a, b) => a.localeCompare(b))

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
    the Bank field on roll txns and trips suggests every bank you've used. */
async function loadCachingBanks<T extends { bank: string }>(list: () => Promise<T[]>): Promise<T[]> {
  const rows = await list()
  banks = distinct([...banks, ...rows.map((r) => r.bank)])
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

/** The flat row the spreadsheet edits — joins a holding to its item type. */
export interface FlatHolding {
  id: number
  activity: 'bullion' | 'crh'
  product: string
  metal: string
  fineness: string
  fine_oz_each: number
  qty: number
  basis_usd: number
  face_value_usd: number
  acquired: string
  source: string
  from_box: string // roll_txn id (as string) this CRH find came from; '' = none
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
    basis_usd: Number(row.basis_usd) || 0,
    face_value_usd: Number(row.face_value_usd) || 0,
    acquired: row.acquired,
    source: row.source,
    notes: '',
  }
}

export const holdingsGrid: GridConfig<FlatHolding> = {
  title: 'Holdings',
  description:
    'Bullion lots and CRH silver finds. Type the product, metal, fineness and metal-oz; we keep the catalog tidy for you.',
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
    { accessorKey: 'qty', header: 'Qty', meta: { editor: 'number', step: 1, align: 'right', width: '80px' } },
    { accessorKey: 'basis_usd', header: 'Basis $', meta: { editor: 'number', step: 0.01, align: 'right', width: '110px' } },
    { accessorKey: 'face_value_usd', header: 'Face $', meta: { editor: 'number', step: 0.01, align: 'right', width: '100px' } },
    { accessorKey: 'acquired', header: 'Acquired', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'source', header: 'Source', meta: { editor: 'autocomplete', placeholder: 'APMEX', suggestions: () => holdingSources } },
    { accessorKey: 'from_box', header: 'From box (CRH)', meta: { editor: 'select', optionsFn: boxOptions, width: '190px' } },
  ],
  load: async () => {
    const [types, holdings, rolls] = await Promise.all([
      api.itemTypes.list(),
      api.holdings.list(),
      api.rollTxns.list(),
    ])
    catalog = types
    holdingSources = distinct(holdings.map((h) => h.source))
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
        fine_oz_each: t?.fine_oz_each ?? 0,
        qty: h.qty,
        basis_usd: h.basis_usd,
        face_value_usd: h.face_value_usd,
        acquired: h.acquired,
        source: h.source,
        from_box: h.roll_txn_id ? String(h.roll_txn_id) : '',
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
    qty: 1,
    basis_usd: 0,
    face_value_usd: 0,
    acquired: today(),
    source: '',
    from_box: '',
  }),
}

// --- Plain CRUD tables -------------------------------------------------------

export const rollTxnsGrid: GridConfig<RollTxn> = {
  title: 'Roll transactions',
  description: 'Boxes/rolls bought and culls returned. face_usd is the source of truth; box throughput is derived from it.',
  columns: [
    { accessorKey: 'date', header: 'Date', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'bank', header: 'Bank', meta: { editor: 'autocomplete', placeholder: 'Stock Yards', suggestions: () => banks } },
    { accessorKey: 'action', header: 'Action', meta: { editor: 'select', options: ['buy', 'return'], width: '100px' } },
    { accessorKey: 'denom', header: 'Denom', meta: { editor: 'select', options: DENOMS, width: '110px' } },
    { accessorKey: 'unit', header: 'Unit', meta: { editor: 'select', options: ROLL_UNITS, width: '90px' } },
    { accessorKey: 'amount', header: 'Amount', meta: { editor: 'number', step: 0.1, align: 'right', width: '90px' } },
    { accessorKey: 'face_usd', header: 'Face $', meta: { editor: 'number', step: 0.01, align: 'right', width: '110px' } },
    { accessorKey: 'notes', header: 'Notes', meta: { editor: 'text' } },
  ],
  load: () => loadCachingBanks(api.rollTxns.list),
  create: api.rollTxns.create,
  update: api.rollTxns.update,
  remove: api.rollTxns.remove,
  blank: () => ({ date: today(), bank: '', action: 'buy', denom: 'halves', unit: 'box', amount: 1, face_usd: 500, notes: '' }),
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
  description: 'Non-silver coins pulled at face. Recoverable, not a loss — kept out of the redeposit float.',
  columns: [
    { accessorKey: 'denom', header: 'Denom', meta: { editor: 'select', options: DENOMS, width: '130px' } },
    { accessorKey: 'count', header: 'Count', meta: { editor: 'number', step: 1, align: 'right', width: '120px' } },
    { accessorKey: 'face_usd', header: 'Face $', meta: { editor: 'number', step: 0.01, align: 'right', width: '130px' } },
  ],
  load: api.keepers.list,
  create: api.keepers.create,
  update: api.keepers.update,
  remove: api.keepers.remove,
  blank: () => ({ denom: 'halves', count: 0, face_usd: 0 }),
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
