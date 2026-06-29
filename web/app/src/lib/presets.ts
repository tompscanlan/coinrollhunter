// Coin-roll-hunting reference data — mirrors the prototype's SILVER_PRESETS /
// BOX_FACE. Picking a silver preset auto-fills metal, fineness and fine-oz so a
// find takes two clicks, not a research session.

export interface SilverPreset {
  label: string
  fineness: string // "40%" | "90%" | "35%"
  fine_oz_each: number // fine metal oz per coin (troy)
  face_each: number // face value per coin ($) — for the finds workflow's face auto-fill
}

export const SILVER_PRESETS: SilverPreset[] = [
  { label: '40% Kennedy half (1965–70)', fineness: '40%', fine_oz_each: 0.1479, face_each: 0.5 },
  { label: '90% half (1964 & earlier)', fineness: '90%', fine_oz_each: 0.36169, face_each: 0.5 },
  { label: '90% quarter (pre-1965)', fineness: '90%', fine_oz_each: 0.18084, face_each: 0.25 },
  { label: '90% dime (pre-1965)', fineness: '90%', fine_oz_each: 0.07234, face_each: 0.1 },
  { label: '35% war nickel (1942–45)', fineness: '35%', fine_oz_each: 0.05626, face_each: 0.05 },
]

/** Face dollars in one bank box, by denomination — for box→face auto-fill. */
export const BOX_FACE: Record<string, number> = {
  halves: 500,
  quarters: 500,
  dimes: 250,
  nickels: 100,
  cents: 25,
}

export const DENOMS = ['halves', 'quarters', 'dimes', 'nickels', 'cents'] as const
export const ROLL_UNITS = ['box', 'roll', 'face', 'coin'] as const
export const METALS = ['gold', 'silver', 'platinum', 'palladium'] as const
export const KINDS = ['coin', 'round', 'bar', 'junk', 'jewelry', 'other'] as const

/** Face dollars per single coin, by denomination. */
export const COIN_FACE: Record<string, number> = {
  halves: 0.5,
  quarters: 0.25,
  dimes: 0.1,
  nickels: 0.05,
  cents: 0.01,
}

/** Coins in a standard customer-wrapped roll, by denomination. */
export const ROLL_COUNT: Record<string, number> = {
  halves: 20,
  quarters: 40,
  dimes: 50,
  nickels: 40,
  cents: 50,
}

/** Normalize an entry (amount of `unit` for a `denom`) to face dollars — the
    source of truth a roll_txn stores. box→box-face, roll→coins×coin-face,
    coin→coin-face, face→the amount itself. Used by the Do-tab buy workflows so
    "1 box of halves" auto-fills $500. */
export function faceFor(unit: string, denom: string, amount: number): number {
  const n = Number(amount) || 0
  switch (unit) {
    case 'box':
      return n * (BOX_FACE[denom] ?? 0)
    case 'roll':
      return n * (COIN_FACE[denom] ?? 0) * (ROLL_COUNT[denom] ?? 0)
    case 'coin':
      return n * (COIN_FACE[denom] ?? 0)
    case 'face':
    default:
      return n
  }
}
