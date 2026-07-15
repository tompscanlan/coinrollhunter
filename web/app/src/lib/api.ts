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
  Photo,
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

/** The catalog half of a holdings-with-type write: the fields that find-or-create the
    item_type a holding points at. item_type_id is resolved server-side from these. */
export interface HoldingCatalog {
  product: string
  metal: string
  fineness: string
  fine_oz_each: number
}

/** The holding half of a holdings-with-type write — the columns the Holdings grid
    models, WITHOUT item_type_id (the server resolves it from the catalog). On UPDATE
    this is a MERGE, exactly like the granular PUT /api/lots/{id}: a field not named here
    is preserved (notes/insured_value/attributes/the disposal — om-kyq7). */
export type WorkflowHolding = Partial<Omit<Holding, 'id' | 'uid' | 'item_type_id'>>

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
  // --- Compound workflow endpoints (/api/workflows/*, om-2sl6) -----------------
  // Each Do-tab action is ONE request to ONE endpoint wrapping ONE store transaction,
  // so a mid-sequence failure can no longer half-record the action — and because
  // nothing lands on failure, a human re-press of the still-populated form is safe (no
  // idempotency key). The granular crud() endpoints above stay: the Edit grids, export
  // and the e2e suite still use them.
  workflows: {
    // "Bought a box": a roll_txn buy plus an optional bank trip. trip is null when the
    // user did not log one.
    boughtABox: (body: {
      purchase: Omit<RollTxn, 'id' | 'uid' | 'branch_id'>
      trip: Omit<Trip, 'id' | 'branch_id'> | null
    }) => req<{ roll_txn_id: number; trip_id: number }>('POST', '/workflows/bought-a-box', body),
    // "Logged finds": an optional box (existing or created inline), N CRH finds (each
    // find-or-creating its item_type server-side) and M clad keepers — all attributed to
    // the box, all in one transaction. The box link for every find/keeper is resolved on
    // the server, so the rows carry no from_box of their own.
    loggedFinds: (body: {
      box: { existing_id: number } | { new: Omit<RollTxn, 'id' | 'uid' | 'branch_id'> } | null
      finds: {
        product: string
        metal: string
        fineness: string
        fine_oz_each: number
        qty: number
        basis_usd: number
        premium_usd: number
        face_value_usd: number
        acquired: string
        source: string
        kept: boolean
      }[]
      keepers: { denom: string; count: number; face_usd: number; date: string }[]
    }) => req<void>('POST', '/workflows/logged-finds', body),
  },
  // Holdings grid create/update as ONE atomic request: find-or-create the item_type and
  // write the holding in a single store transaction (replaces the client-side
  // ensureItemType + a separate lots write). create returns the new id; update is a merge.
  holdingWithType: {
    create: async (catalog: HoldingCatalog, holding: WorkflowHolding) =>
      (await req<{ id: number }>('POST', '/workflows/holdings-with-type', { catalog, holding })).id,
    update: (id: number, catalog: HoldingCatalog, holding: WorkflowHolding) =>
      req<{ id: number }>('PUT', `/workflows/holdings-with-type/${id}`, { catalog, holding }).then(() => {}),
  },
  // --- Photos (om-6hlp) --------------------------------------------------------
  // A lot carries N photos. Upload is multipart (cannot reuse req()'s JSON path); the
  // originals are the source of truth and thumb/display are a regenerable cache fetched by
  // variant. Soft delete only — a "deleted" photo is trashed (inactive), its file kept.
  photos: {
    list: (ownerKind: 'lot' | 'roll_txn', ownerUid: string) =>
      req<Photo[]>('GET', `/photos?owner_kind=${ownerKind}&owner_uid=${encodeURIComponent(ownerUid)}`),
    // Multipart POST: FormData, no JSON body, so req()'s Content-Type dance is bypassed and
    // the browser sets the multipart boundary itself.
    upload: async (
      ownerKind: 'lot' | 'roll_txn',
      ownerUid: string,
      file: File,
      role = '',
      caption = '',
    ): Promise<Photo> => {
      const fd = new FormData()
      fd.append('owner_kind', ownerKind)
      fd.append('owner_uid', ownerUid)
      if (role) fd.append('role', role)
      if (caption) fd.append('caption', caption)
      fd.append('file', file)
      const res = await fetch(`${BASE}/photos`, { method: 'POST', body: fd })
      if (!res.ok) {
        let msg = `upload → ${res.status}`
        try {
          const j = await res.json()
          if (j?.error) msg = j.error
        } catch {
          /* non-JSON error body */
        }
        throw new Error(msg)
      }
      return res.json() as Promise<Photo>
    },
    // role / seq / caption only — the server never lets a PUT move the file.
    update: (id: number, patch: Partial<Pick<Photo, 'role' | 'seq' | 'caption'>>) =>
      req<{ id: number }>('PUT', `/photos/${id}`, patch).then(() => {}),
    // Soft delete: flags inactive, keeps the file.
    remove: (id: number) => req<void>('DELETE', `/photos/${id}`),
    // A plain URL an <img> can load — original, or the thumb/display derivative.
    fileUrl: (uid: string, variant: 'original' | 'thumb' | 'display' = 'display') =>
      `${BASE}/photos/${uid}/file?variant=${variant}`,
  },

  // Stop the local server. The double-clicked app has no console, so this is the
  // only way to quit it short of Task Manager (om-9p0l).
  quit: () => req<void>('POST', '/quit'),
}
