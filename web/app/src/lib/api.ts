// Typed client for the Go REST API (internal/api). Same-origin '/api' works both
// when embedded (Go serves the SPA + API together) and in dev (Vite proxies /api).
import type {
  Report,
  Spot,
  Settings,
  ItemType,
  Holding,
  RollTxn,
  Trip,
  Supply,
  Keeper,
} from './types'

const BASE = '/api'

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(BASE + path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    let msg = `${method} ${path} → ${res.status}`
    try {
      const j = await res.json()
      if (j?.error) msg = j.error
    } catch {
      /* non-JSON error body */
    }
    throw new Error(msg)
  }
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

/** CRUD bundle for one resource table. `T` is the row type; create/update take a
    row sans id. The Go API returns {id} on create. */
export interface Crud<T extends { id: number }> {
  list(): Promise<T[]>
  create(row: Omit<T, 'id'>): Promise<number>
  update(id: number, row: Omit<T, 'id'>): Promise<void>
  remove(id: number): Promise<void>
}

function crud<T extends { id: number }>(name: string): Crud<T> {
  const base = `/${name}`
  return {
    list: () => req<T[]>('GET', base),
    create: async (row) => (await req<{ id: number }>('POST', base, row)).id,
    update: (id, row) => req<{ id: number }>('PUT', `${base}/${id}`, row).then(() => {}),
    remove: (id) => req<void>('DELETE', `${base}/${id}`),
  }
}

export const api = {
  summary: () => req<Report>('GET', '/summary'),
  health: () => req<{ status: string }>('GET', '/health'),

  spotHistory: () => req<Spot[]>('GET', '/spot'),
  spotLatest: () => req<Spot>('GET', '/spot/latest'),
  putSpot: (s: Spot) => req<Spot>('POST', '/spot', s),

  getSettings: () => req<Settings>('GET', '/settings'),
  putSettings: (s: Settings) => req<Settings>('PUT', '/settings', s),

  itemTypes: crud<ItemType>('item-types'),
  holdings: crud<Holding>('lots'),
  // sell a holding (full or partial); records disposal + realized P&L
  sellHolding: (id: number, body: { qty: number; proceeds_usd: number; date: string }) =>
    req<void>('POST', `/lots/${id}/sell`, body),
  rollTxns: crud<RollTxn>('roll-txns'),
  trips: crud<Trip>('trips'),
  supplies: crud<Supply>('supplies'),
  keepers: crud<Keeper>('keepers'),
}
