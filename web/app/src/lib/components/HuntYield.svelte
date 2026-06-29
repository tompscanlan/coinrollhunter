<script lang="ts">
  // Which banks/boxes actually pay off: silver found (realizable, after haircut)
  // vs face searched, attributed to the box each find came from. Aggregated by
  // bank (the headline question) and listed box-by-box, newest first (the
  // over-time view). All $derived from the report.
  import type { Report } from '$lib/types'
  import { money, oz, num } from '$lib/format'
  import Card from '$lib/components/ui/Card.svelte'
  import { cn } from '$lib/utils'

  let { report }: { report: Report } = $props()
  const r = $derived(report)

  const byBank = $derived.by(() => {
    const m = new Map<string, { bank: string; face: number; value: number; finds: number; boxes: number }>()
    for (const b of r.box_yields) {
      const key = b.bank || '(unknown)'
      let g = m.get(key)
      if (!g) {
        g = { bank: key, face: 0, value: 0, finds: 0, boxes: 0 }
        m.set(key, g)
      }
      g.face += b.face_usd
      g.value += b.find_value_usd
      g.finds += b.find_count
      g.boxes += 1
    }
    return [...m.values()]
      .map((g) => ({ ...g, yield: g.face ? (g.value / g.face) * 100 : 0 }))
      .sort((a, b) => b.yield - a.yield)
  })

  // box-by-box, newest first — the time view
  const boxes = $derived(
    [...r.box_yields].sort((a, b) =>
      a.date < b.date ? 1 : a.date > b.date ? -1 : b.roll_txn_id - a.roll_txn_id,
    ),
  )
  const tone = (y: number) => (y > 0 ? 'text-positive' : 'text-muted-foreground')
</script>

<section class="space-y-2">
  <h2 class="text-lg font-semibold">Hunt yield by bank &amp; box</h2>
  <p class="text-sm text-muted-foreground">
    Silver found (realizable) vs face searched, attributed to the box it came from. Set a find's
    <span class="font-medium">From box</span> in Entry → Holdings to populate this.
  </p>

  <Card class="overflow-x-auto">
    <table class="w-full text-sm tnum">
      <thead>
        <tr class="border-b bg-muted/40 text-left text-muted-foreground">
          <th class="px-3 py-2 font-medium">Bank</th>
          <th class="px-3 py-2 text-right font-medium">Boxes</th>
          <th class="px-3 py-2 text-right font-medium">Face searched</th>
          <th class="px-3 py-2 text-right font-medium">Silver found</th>
          <th class="px-3 py-2 text-right font-medium">Yield</th>
        </tr>
      </thead>
      <tbody>
        {#each byBank as g (g.bank)}
          <tr class="border-b last:border-0">
            <td class="px-3 py-2">{g.bank}</td>
            <td class="px-3 py-2 text-right">{num(g.boxes)}</td>
            <td class="px-3 py-2 text-right">{money(g.face)}</td>
            <td class="px-3 py-2 text-right">
              {money(g.value)} <span class="text-muted-foreground">({g.finds})</span>
            </td>
            <td class={cn('px-3 py-2 text-right font-medium', tone(g.yield))}>{g.yield.toFixed(1)}%</td>
          </tr>
        {:else}
          <tr><td colspan="5" class="px-3 py-6 text-center text-muted-foreground">No boxes logged yet.</td></tr>
        {/each}
      </tbody>
    </table>
  </Card>

  {#if boxes.length}
    <details class="rounded-xl border bg-card shadow-sm">
      <summary class="cursor-pointer px-3 py-2 text-sm font-medium text-muted-foreground">
        Box-by-box ({boxes.length})
      </summary>
      <div class="overflow-x-auto border-t">
        <table class="w-full text-sm tnum">
          <thead>
            <tr class="border-b bg-muted/40 text-left text-muted-foreground">
              <th class="px-3 py-2 font-medium">Date</th>
              <th class="px-3 py-2 font-medium">Bank</th>
              <th class="px-3 py-2 font-medium">Denom</th>
              <th class="px-3 py-2 text-right font-medium">Face</th>
              <th class="px-3 py-2 text-right font-medium">Found</th>
              <th class="px-3 py-2 text-right font-medium">Oz</th>
              <th class="px-3 py-2 text-right font-medium">Yield</th>
            </tr>
          </thead>
          <tbody>
            {#each boxes as b (b.roll_txn_id)}
              <tr class="border-b last:border-0">
                <td class="px-3 py-2">{b.date}</td>
                <td class="px-3 py-2">{b.bank}</td>
                <td class="px-3 py-2">{b.denom}</td>
                <td class="px-3 py-2 text-right">{money(b.face_usd)}</td>
                <td class="px-3 py-2 text-right">
                  {money(b.find_value_usd)} <span class="text-muted-foreground">({b.find_count})</span>
                </td>
                <td class="px-3 py-2 text-right">{oz(b.find_oz)}</td>
                <td class={cn('px-3 py-2 text-right', tone(b.yield_pct))}>{b.yield_pct.toFixed(1)}%</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </details>
  {/if}
</section>
