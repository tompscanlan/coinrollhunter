// Grid configurations — one per data table. Each bundles the TanStack columns
// (with edit metadata) plus the load/create/update/remove/blank wiring the
// EditableGrid needs. Most map straight to a CRUD resource; Holdings is special:
// it presents a *flat* spreadsheet row but writes the ADR-003 catalog/specimen
// split (find-or-create an item_type, then attach the holding).
import { api } from './api'
import { today } from './format'
import { DENOMS, ROLL_UNITS, METALS } from './presets'
import type { GridColumn } from './components/EditableGrid.svelte'
import type { ItemType, Holding, RollTxn, Trip, Supply, Keeper } from './types'

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
  asw_oz: number
  qty: number
  basis_usd: number
  face_value_usd: number
  acquired: string
  source: string
}

const norm = (s: string) => (s ?? '').trim().toLowerCase()

/** Find an item_type matching name+metal+fineness, else create one. Updates the
    catalog's asw_oz if it drifted. Returns the type id. */
async function ensureItemType(row: Omit<FlatHolding, 'id'>): Promise<number> {
  const types = await api.itemTypes.list()
  const match = types.find(
    (t) =>
      norm(t.name) === norm(row.product) &&
      norm(t.metal) === norm(row.metal) &&
      norm(t.fineness) === norm(row.fineness),
  )
  if (match) {
    if (Math.abs((match.asw_oz ?? 0) - (row.asw_oz ?? 0)) > 1e-9) {
      await api.itemTypes.update(match.id, { ...match, asw_oz: row.asw_oz } as Omit<ItemType, 'id'>)
    }
    return match.id
  }
  const kind = row.activity === 'crh' ? 'junk' : 'coin'
  return api.itemTypes.create({
    kind,
    name: row.product || 'Unnamed',
    metal: row.metal,
    asw_oz: row.asw_oz,
    fineness: row.fineness,
  } as Omit<ItemType, 'id'>)
}

function toHolding(row: Omit<FlatHolding, 'id'>, item_type_id: number): Omit<Holding, 'id'> {
  return {
    item_type_id,
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
    { accessorKey: 'product', header: 'Product', meta: { editor: 'text', placeholder: '1 oz Gold Eagle' } },
    { accessorKey: 'metal', header: 'Metal', meta: { editor: 'select', options: METALS, width: '110px' } },
    { accessorKey: 'fineness', header: 'Fineness', meta: { editor: 'text', width: '100px', placeholder: '.9999' } },
    { accessorKey: 'asw_oz', header: 'Fine oz / unit', meta: { editor: 'number', step: 0.0001, align: 'right', width: '120px' } },
    { accessorKey: 'qty', header: 'Qty', meta: { editor: 'number', step: 1, align: 'right', width: '80px' } },
    { accessorKey: 'basis_usd', header: 'Basis $', meta: { editor: 'number', step: 0.01, align: 'right', width: '110px' } },
    { accessorKey: 'face_value_usd', header: 'Face $', meta: { editor: 'number', step: 0.01, align: 'right', width: '100px' } },
    { accessorKey: 'acquired', header: 'Acquired', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'source', header: 'Source', meta: { editor: 'text', placeholder: 'APMEX' } },
  ],
  load: async () => {
    const [types, holdings] = await Promise.all([api.itemTypes.list(), api.holdings.list()])
    const byId = new Map<number, ItemType>(types.map((t) => [t.id, t]))
    return holdings.map((h) => {
      const t = byId.get(h.item_type_id)
      return {
        id: h.id,
        activity: h.activity,
        product: t?.name ?? '',
        metal: t?.metal ?? '',
        fineness: t?.fineness ?? '',
        asw_oz: t?.asw_oz ?? 0,
        qty: h.qty,
        basis_usd: h.basis_usd,
        face_value_usd: h.face_value_usd,
        acquired: h.acquired,
        source: h.source,
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
    asw_oz: 0,
    qty: 1,
    basis_usd: 0,
    face_value_usd: 0,
    acquired: today(),
    source: '',
  }),
}

// --- Plain CRUD tables -------------------------------------------------------

export const rollTxnsGrid: GridConfig<RollTxn> = {
  title: 'Roll transactions',
  description: 'Boxes/rolls bought and culls returned. face_usd is the source of truth; box throughput is derived from it.',
  columns: [
    { accessorKey: 'date', header: 'Date', meta: { editor: 'date', width: '150px' } },
    { accessorKey: 'bank', header: 'Bank', meta: { editor: 'text', placeholder: 'Stock Yards' } },
    { accessorKey: 'action', header: 'Action', meta: { editor: 'select', options: ['buy', 'return'], width: '100px' } },
    { accessorKey: 'denom', header: 'Denom', meta: { editor: 'select', options: DENOMS, width: '110px' } },
    { accessorKey: 'unit', header: 'Unit', meta: { editor: 'select', options: ROLL_UNITS, width: '90px' } },
    { accessorKey: 'amount', header: 'Amount', meta: { editor: 'number', step: 0.1, align: 'right', width: '90px' } },
    { accessorKey: 'face_usd', header: 'Face $', meta: { editor: 'number', step: 0.01, align: 'right', width: '110px' } },
    { accessorKey: 'notes', header: 'Notes', meta: { editor: 'text' } },
  ],
  load: api.rollTxns.list,
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
    { accessorKey: 'bank', header: 'Bank', meta: { editor: 'text', placeholder: 'Commonwealth' } },
    { accessorKey: 'miles', header: 'Round-trip miles', meta: { editor: 'number', step: 0.1, align: 'right', width: '150px' } },
    { accessorKey: 'hours', header: 'Hours', meta: { editor: 'number', step: 0.25, align: 'right', width: '110px' } },
  ],
  load: api.trips.list,
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
  title: 'Clad keepers',
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
