<script lang="ts">
  // Insights — the *analysis* altitude (ADR-012). Everything that needs a legend
  // or study lives here, lifted out of Overview so the day-to-day glance stays
  // read-without-interpreting. A day-to-day user never has to open this tab.
  import type { Report } from '$lib/types'
  import Composition from './Composition.svelte'
  import StackByType from './StackByType.svelte'
  import HuntYield from './HuntYield.svelte'
  import TrophyFeed from './TrophyFeed.svelte'
  import HitRateGrid from './HitRateGrid.svelte'

  let { report }: { report: Report } = $props()
  const r = $derived(report)

  // Progressive disclosure (ADR-012 §3): analysis is *earned* by data. With
  // nothing logged, don't render empty grids — a simple derived predicate, not a
  // stored flag. "Which banks pay off" stays dormant until finds link to boxes.
  const hasAnything = $derived(r.lots.length > 0 || r.buy_count > 0)
</script>

<div class="space-y-6">
  <div>
    <h2 class="text-lg font-semibold">Insights</h2>
    <p class="text-sm text-muted-foreground">
      The analysis layer — composition, which banks pay off, hit-rate, and your best finds.
      Each view fills in as you log more hunts.
    </p>
  </div>

  {#if !hasAnything}
    <div class="rounded-lg border border-dashed px-4 py-10 text-center text-sm text-muted-foreground">
      Nothing to analyze yet. Log a box and some finds in
      <span class="font-medium text-foreground">Do</span>, and your yield-by-bank, hit-rate, and
      trophies will appear here.
    </div>
  {:else}
    <!-- live composition snapshot -->
    <Composition {report} />

    <!-- unified inventory: stack by coin type (bought + found combined) -->
    <StackByType {report} />

    <!-- hunt yield by bank & box: "which banks pay off" — dormant until finds link to boxes -->
    {#if r.box_yields?.length}
      <HuntYield {report} />
    {/if}

    <!-- greatest hits: finds flagged as trophies (ADR-006) -->
    <TrophyFeed {report} />

    <!-- hit-rate report: 1 per face $, per denom × category × source (ADR-006) -->
    <HitRateGrid {report} />
  {/if}
</div>
