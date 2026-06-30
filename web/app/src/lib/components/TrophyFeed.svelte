<script lang="ts">
  // Greatest hits (ADR-006): the finds you flagged as trophies, surfaced as a
  // feed. A trophy is a normal editable column on Holdings (Edit → Holdings),
  // so this list is a filter, not manual curation. Sourced from the live lots in
  // the summary, joined with the find's category for context.
  import type { Report } from '$lib/types'
  import { money, oz } from '$lib/format'
  import Card from '$lib/components/ui/Card.svelte'
  import Badge from '$lib/components/ui/Badge.svelte'
  import { Trophy } from 'lucide-svelte'

  let { report }: { report: Report } = $props()

  const trophies = $derived(report.lots.filter((l) => l.trophy))
</script>

{#if trophies.length}
  <section class="space-y-2">
    <div class="flex items-center justify-between">
      <h2 class="flex items-center gap-1.5 text-lg font-semibold">
        <Trophy class="size-4 text-warning" /> Greatest hits
      </h2>
      <Badge variant="secondary">{trophies.length} troph{trophies.length === 1 ? 'y' : 'ies'}</Badge>
    </div>
    <Card class="divide-y">
      {#each trophies as l (l.id)}
        <div class="flex items-center justify-between gap-3 px-4 py-2.5">
          <div class="min-w-0">
            <p class="truncate font-medium text-foreground">{l.product || 'Find'}</p>
            <p class="text-xs text-muted-foreground">
              {#if l.category}{l.category}{#if l.subcategory} · {l.subcategory}{/if} · {/if}{l.qty} × · {oz(l.fine_oz)} oz
            </p>
          </div>
          <span class="shrink-0 text-sm text-muted-foreground tnum">{money(l.market_usd)}</span>
        </div>
      {/each}
    </Card>
  </section>
{/if}
