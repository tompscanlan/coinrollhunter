<script lang="ts">
  // A dependency-free, live snapshot of the *current* portfolio — no time series
  // yet (that's deferred to the spot-history work). Everything is $derived from
  // the report, so editing a grid recomputes these bars in place.
  import type { Report } from '$lib/types'
  import { money } from '$lib/format'
  import Card from '$lib/components/ui/Card.svelte'

  let { report }: { report: Report } = $props()
  const r = $derived(report)

  type Seg = { label: string; value: number; color: string }
  // Where your value sits: gold bullion / silver bullion / CRH silver finds.
  const segs = $derived(
    (
      [
        { label: 'Gold bullion', value: r.gold_market, color: '#f59e0b' },
        { label: 'Silver bullion', value: Math.max(0, r.bullion_market - r.gold_market), color: '#94a3b8' },
        { label: 'CRH silver finds', value: r.find_realizable, color: '#10b981' },
      ] as Seg[]
    ).filter((s) => s.value > 0),
  )
  const total = $derived(segs.reduce((t, s) => t + s.value, 0))
  const share = (v: number) => (total > 0 ? (v / total) * 100 : 0)

  // basis vs liquidation — the live unrealized P&L, drawn to a shared scale.
  const scale = $derived(Math.max(r.total_basis, r.total_market, 1))
</script>

<Card class="space-y-4 p-4">
  <div class="flex items-center justify-between">
    <h2 class="text-lg font-semibold">Portfolio composition</h2>
    <span class="text-sm text-muted-foreground tnum">{money(total)} total value</span>
  </div>

  {#if total > 0}
    <!-- allocation bar -->
    <div class="flex h-5 w-full overflow-hidden rounded-md">
      {#each segs as s (s.label)}
        <div style={`width:${share(s.value)}%;background:${s.color}`} title={`${s.label}: ${money(s.value)}`}></div>
      {/each}
    </div>
    <div class="flex flex-wrap gap-x-5 gap-y-1 text-xs">
      {#each segs as s (s.label)}
        <span class="inline-flex items-center gap-1.5">
          <span class="size-2.5 rounded-sm" style={`background:${s.color}`}></span>
          {s.label}
          <span class="text-muted-foreground tnum">{money(s.value)} · {share(s.value).toFixed(0)}%</span>
        </span>
      {/each}
    </div>

    <!-- invested vs liquidation -->
    <div class="space-y-1.5 pt-1">
      <div class="flex items-center gap-2 text-xs">
        <span class="w-20 shrink-0 text-muted-foreground">Invested</span>
        <div class="h-3 flex-1 rounded bg-muted">
          <div class="h-full rounded bg-foreground/30" style={`width:${(r.total_basis / scale) * 100}%`}></div>
        </div>
        <span class="w-24 shrink-0 text-right tnum">{money(r.total_basis)}</span>
      </div>
      <div class="flex items-center gap-2 text-xs">
        <span class="w-20 shrink-0 text-muted-foreground">Liquidation</span>
        <div class="h-3 flex-1 rounded bg-muted">
          <div
            class={`h-full rounded ${r.total_unreal >= 0 ? 'bg-positive' : 'bg-negative'}`}
            style={`width:${(r.total_market / scale) * 100}%`}
          ></div>
        </div>
        <span class={`w-24 shrink-0 text-right tnum ${r.total_unreal >= 0 ? 'text-positive' : 'text-negative'}`}>
          {money(r.total_market)}
        </span>
      </div>
    </div>
  {:else}
    <p class="text-sm text-muted-foreground">Add holdings to see your allocation.</p>
  {/if}
</Card>
