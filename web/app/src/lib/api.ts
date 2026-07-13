// Typed client for the Go REST API (internal/api). Same-origin '/api' works both
// when embedded (Go serves the SPA + API together) and in dev (Vite proxies /api).
import type {
  Report,
  FindsReport,
  Spot,
  Settings,
  ItemType,
  Holding,
  RollTxn,
  Trip,
  Branch,
  Supply,
  Keeper,
  Loss,
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
    row sans id. The Go API returns {id} on create.

    `update` takes a PARTIAL row because PUT is a merge: the server decodes the body
    onto the stored row, so a field you omit is a field you keep. A client that models
    only some columns — the Holdings grid, which never shows notes or insured_value —
    sends only what it knows and cannot blank the rest. To clear a field, name it. */
export interface Crud<T extends { id: number }> {
  list(): Promise<T[]>
  create(row: Omit<T, 'id'>): Promise<number>
  update(id: number, row: Partial<Omit<T, 'id'>>): Promise<void>
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
  // hit-rate report: the "1 per face $" view per denom × category × source (ADR-006)
  findsReport: () => req<FindsReport>('GET', '/finds-report'),
  health: () => req<{ status: string }>('GET', '/health'),

  spotHistory: () => req<Spot[]>('GET', '/spot'),
  spotLatest: () => req<Spot>('GET', '/spot/latest'),
  putSpot: (s: Spot) => req<Spot>('POST', '/spot', s),

  getSettings: () => req<Settings>('GET', '/settings'),
  putSettings: (s: Settings) => req<Settings>('PUT', '/settings', s),

  // The data-export bundle (internal/export): a zip of one CSV per table, a lossless
  // data.json, and the photo originals. A plain link the browser downloads — not a
  // fetch: the app never has to hold a collection's worth of photos in memory.
  exportUrl: `${BASE}/export`,

  itemTypes: crud<ItemType>('item-types'),
  holdings: crud<Holding>('lots'),
  // sell a holding (full or partial); records disposal + realized P&L
  sellHolding: (id: number, body: { qty: number; proceeds_usd: number; date: string }) =>
    req<void>('POST', `/lots/${id}/sell`, body),
  rollTxns: crud<RollTxn>('roll-txns'),
  trips: crud<Trip>('trips'),
  branches: crud<Branch>('branches'),
  // Fold duplicate branches into one survivor (ADR-010 dedup).
  mergeBranches: (survivorId: number, loserIds: number[]) =>
    req<void>('POST', `/branches/${survivorId}/merge`, { loser_ids: loserIds }),
  supplies: crud<Supply>('supplies'),
  keepers: crud<Keeper>('keepers'),
  losses: crud<Loss>('losses'),
  // Stop the local server. The double-clicked app has no console, so this is the
  // only way to quit it short of Task Manager (om-9p0l).
  quit: () => req<void>('POST', '/quit'),
}
