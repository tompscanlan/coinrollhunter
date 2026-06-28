<script lang="ts">
  // Inventory lens: roll every holding up by the coin it *is* (product+metal+
  // fineness — the catalog identity), valued at full melt regardless of how it
  // was acquired. So a bought roll and a found half-roll of the same half-dollar
  // appear as ONE stack line, while the hunt cash-flow below still credits the
  // free finds. All $derived from the report — recomputes live on any edit.
  import type { Report } from '$lib/types'
  import { money, oz, num } from '$lib/format'
  import Card from '$lib/components/ui/Card.svelte'

  let { report }: { report: Report } = $props()
  const r = $derived(report)

  type Src = { qty: number; basis: number }
  type Group = {
    key: string
    product: string
    metal: string
    fineness: string
    qty: number
    fineOz: number
    melt: number
    bought: Src
    found: Src
  }

  const groups = $derived.by(() => {
    const m = new Map<string, Group>()
    for (const l of r.lots) {
      const key = `${l.product}|${l.metal}|${l.fineness}`
      let g = m.get(key)
      if (!g) {
        g = {
          key, product: l.product, metal: l.metal, fineness: l.fineness,
          qty: 0, fineOz: 0, melt: 0,
          bought: { qty: 0, basis: 0 }, found: { qty: 0, basis: 0 },
        }
        m.set(key, g)
      }
      g.qty += l.qty
      g.fineOz += l.fine_oz
      g.melt += l.market_usd // full melt — the "value as bullion" figure
      const src = l.activity === 'crh' ? g.found : g.bought
      src.qty += l.qty
      src.basis += l.basis_usd
    }
    return [...m.values()].sort((a, b) => b.melt - a.melt)
  })

  const totals = $derived({
    qty: groups.reduce((t, g) => t + g.qty, 0),
    fineOz: groups.reduce((t, g) => t + g.fineOz, 0),
    melt: groups.reduce((t, g) => t + g.melt, 0),
  })

  // sourced from both bought + found — the case that "felt wrong", now unified
  const mixed = (g: Group) => g.bought.qty > 0 && g.found.qty > 0
</script>

<section class="space-y-2">
  <div class="flex items-center justify-between">
    <h2 class="text-lg font-semibold">Stack by coin type</h2>
    <span class="text-sm text-muted-foreground tnum">{money(totals.melt)} melt</span>
  </div>
  <p class="text-sm text-muted-foreground">
    Every coin you hold, valued at full melt regardless of how you got it. Bought + found of the same
    type combine into one line; the hunt P&amp;L below still credits the free finds.
  </p>
  <Card class="overflow-x-auto">
    <table class="w-full text-sm tnum">
      <thead>
        <tr class="border-b bg-muted/40 text-left text-muted-foreground">
          <th class="px-3 py-2 font-medium">Coin type</th>
          <th class="px-3 py-2 text-right font-medium">Qty</th>
          <th class="px-3 py-2 text-right font-medium">Fine oz</th>
          <th class="px-3 py-2 text-right font-medium">Melt value</th>
        </tr>
      </thead>
      <tbody>
        {#each groups as g (g.key)}
          <tr class="border-b align-top last:border-0">
            <td class="px-3 py-2">
              <div>{g.product || '—'}</div>
              <div class="text-xs text-muted-foreground">
                {g.metal}{g.fineness ? ` · ${g.fineness}` : ''}
                {#if g.bought.qty > 0 || g.found.qty > 0}
                  <span class={mixed(g) ? 'font-medium text-foreground' : ''}>
                    &nbsp;·&nbsp;{#if g.bought.qty > 0}bought {num(g.bought.qty)} ({money(g.bought.basis)}){/if}{#if mixed(g)} · {/if}{#if g.found.qty > 0}found {num(g.found.qty)} ({money(g.found.basis)}){/if}
                  </span>
                {/if}
              </div>
            </td>
            <td class="px-3 py-2 text-right">{num(g.qty)}</td>
            <td class="px-3 py-2 text-right">{oz(g.fineOz)}</td>
            <td class="px-3 py-2 text-right">{money(g.melt)}</td>
          </tr>
        {:else}
          <tr><td colspan="4" class="px-3 py-6 text-center text-muted-foreground">No holdings yet.</td></tr>
        {/each}
        {#if groups.length}
          <tr class="border-t-2 bg-muted/30 font-semibold">
            <td class="px-3 py-2">Total stack</td>
            <td class="px-3 py-2 text-right">{num(totals.qty)}</td>
            <td class="px-3 py-2 text-right">{oz(totals.fineOz)}</td>
            <td class="px-3 py-2 text-right">{money(totals.melt)}</td>
          </tr>
        {/if}
      </tbody>
    </table>
  </Card>
</section>
