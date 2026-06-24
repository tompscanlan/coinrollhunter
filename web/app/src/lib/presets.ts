// Coin-roll-hunting reference data — mirrors the prototype's SILVER_PRESETS /
// BOX_FACE. Picking a silver preset auto-fills metal, fineness and fine-oz so a
// find takes two clicks, not a research session.

export interface SilverPreset {
  label: string
  fineness: string // "40%" | "90%" | "35%"
  asw_oz: number // actual silver weight per coin
}

export const SILVER_PRESETS: SilverPreset[] = [
  { label: '40% Kennedy half (1965–70)', fineness: '40%', asw_oz: 0.1479 },
  { label: '90% half (1964 & earlier)', fineness: '90%', asw_oz: 0.36169 },
  { label: '90% quarter (pre-1965)', fineness: '90%', asw_oz: 0.18084 },
  { label: '90% dime (pre-1965)', fineness: '90%', asw_oz: 0.07234 },
  { label: '35% war nickel (1942–45)', fineness: '35%', asw_oz: 0.05626 },
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
