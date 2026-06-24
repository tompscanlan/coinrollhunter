// Display formatters — mirror the prototype's m()/pc()/num() helpers so the
// numbers read identically to dashboard.html.

const usd = new Intl.NumberFormat('en-US', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

/** Money, e.g. 1234.5 -> "$1,234.50". */
export function money(x: number): string {
  if (x == null || Number.isNaN(x)) return '—'
  const sign = x < 0 ? '-' : ''
  return `${sign}$${usd.format(Math.abs(x))}`
}

/** Signed money, e.g. +$12.00 / -$8.00 — for deltas where sign matters. */
export function signedMoney(x: number): string {
  if (x == null || Number.isNaN(x)) return '—'
  return (x >= 0 ? '+' : '') + money(x).replace('+', '')
}

/** Percent, e.g. 8.53 -> "+8.5%"; Infinity -> "n/a" (zero basis). */
export function pct(x: number): string {
  if (x === Infinity || x == null || Number.isNaN(x)) return 'n/a'
  return (x >= 0 ? '+' : '') + x.toFixed(1) + '%'
}

/** Troy ounces to 4 dp. */
export function oz(x: number): string {
  return (x ?? 0).toFixed(4)
}

/** Plain number, trimmed (3.10 -> "3.1"). */
export function num(x: number): string {
  return (Math.round((x ?? 0) * 100) / 100).toString()
}

/** Today as an ISO date (YYYY-MM-DD), for entry defaults. */
export function today(): string {
  return new Date().toISOString().slice(0, 10)
}
