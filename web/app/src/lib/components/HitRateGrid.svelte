<script lang="ts">
  // The "1 per face $" hit-rate grid (ADR-006): GET /api/finds-report rendered
  // per denomination — rows are find categories (+ indented subcategories),
  // columns are Overall + each acquisition source. Each cell is the rate
  // (face searched per one find) with its sample size; thin cells (low
  // confidence) are greyed and annotated, never hidden — the dataset's central
  // lesson is "never ship a point estimate without its N."
  import type { Report, FindsReport, SourceCell } from '$lib/types'
  import { api } from '$lib/api'
  import { money, num } from '$lib/format'
  import { SOURCE_TYPES } from '$lib/presets'
  import Card from '$lib/components/ui/Card.svelte'
  import { cn } from '$lib/utils'
  import { Info } from 'lucide-svelte'

  // `report` is only a refresh trigger: when the Overview reloads (a new buy or
  // find), re-fetch the hit-rate view so the grid stays in lock-step.
  let { report }: { report: Report } = $props()

  let data = $state<FindsReport | null>(null)
  let err = $state('')

  $effect(() => {
    report // dependency: refetch whenever the summary refreshes
    api
      .findsReport()
      .then((d) => {
        // The Go side returns nil slices (JSON null) for empty groups — an empty
        // dataset, or a denom with buys but no finds yet. Normalize to arrays so
        // the template/derived never deref null.
        data = {
          ...d,
          sources: d.sources ?? [],
          denoms: (d.denoms ?? []).map((dn) => ({ ...dn, categories: dn.categories ?? [] })),
        }
        err = ''
      })
      .catch((e) => (err = (e as Error).message))
  })

  const sourceLabel = (s: string) =>
    SOURCE_TYPES.find((o) => o.value === s)?.label ?? (s || 'Unknown')
  const denomLabel = (d: string) => d || 'Unattributed'

  type Cell = { count: number; hit_per_face: number; low_confidence: boolean }
  const hasAny = $derived(!!data && data.denoms.some((d) => d.categories.length > 0))
</script>

<section class="space-y-2">
  <div class="flex items-center justify-between">
    <h2 class="text-lg font-semibold">Hit rate — 1 per face $</h2>
    {#if data}
      <span class="text-xs text-muted-foreground tnum">{money(data.total_face_searched)} searched</span>
    {/if}
  </div>
  <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
    <Info class="size-3.5 shrink-0" />
    Face dollars you search to find one. Higher = rarer. <span class="tnum">(n)</span> is the sample size;
    greyed cells are low-confidence (n &lt; {data?.low_confidence_n ?? 5}).
  </p>

  {#if err}
    <div class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
      {err}
    </div>
  {:else if !data || !hasAny}
    <Card class="px-4 py-6 text-center text-sm text-muted-foreground">
      No hit-rate data yet. Log buys with a <b>source type</b> and give your finds a <b>category</b>
      (Edit → Holdings) to build the table.
    </Card>
  {:else}
    {@const rep = data}
    {#if rep.unattributed > 0}
      <p class="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
        {num(rep.unattributed)} found coin{rep.unattributed === 1 ? '' : 's'} aren't linked to a buy, so they
        can't be attributed to a source. Link finds to their box (the “From box” column) to sharpen this.
      </p>
    {/if}

    {#each rep.denoms as d (d.denom)}
      {#if d.categories.length > 0}
        <Card class="space-y-2 overflow-x-auto p-4">
          <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <h3 class="font-semibold capitalize text-foreground">{denomLabel(d.denom)}</h3>
            <span class="text-xs text-muted-foreground tnum">
              {money(d.face_searched)} face{#if d.coins_searched > 0} · ~{num(d.coins_searched)} coins searched{/if}
            </span>
          </div>

          <table class="w-full border-collapse text-sm tnum">
            <thead>
              <tr class="border-b text-left text-muted-foreground">
                <th class="px-2 py-1.5 font-medium">Category</th>
                <th class="px-2 py-1.5 text-right font-medium">Overall</th>
                {#each rep.sources as s (s)}
                  <th class="px-2 py-1.5 text-right font-medium">{sourceLabel(s)}</th>
                {/each}
              </tr>
            </thead>
            <tbody>
              {#each d.categories as c (c.category)}
                {@render rateRow(c.category, c, false)}
                {#each c.subcategories ?? [] as sc (sc.subcategory)}
                  {@render rateRow(sc.subcategory, sc, true)}
                {/each}
              {/each}
            </tbody>
          </table>

          <!-- per-source face-searched footnote: the denominators behind the rates -->
          <p class="text-[11px] text-muted-foreground">
            Face searched:
            {#each rep.sources as s, i (s)}{i > 0 ? ' · ' : ''}{sourceLabel(s)} {money(d.face_by_source?.[s] ?? 0)}{/each}
          </p>
        </Card>
      {/if}
    {/each}
  {/if}
</section>

{#snippet cell(c: Cell)}
  {#if c.count <= 0}
    <span class="text-muted-foreground/40">—</span>
  {:else}
    <span
      class={cn(c.low_confidence && 'text-muted-foreground/60')}
      title={c.low_confidence ? `low confidence — only ${num(c.count)} found` : undefined}
    >
      {money(c.hit_per_face)}
      <span class="text-[11px] text-muted-foreground">({num(c.count)})</span>
    </span>
  {/if}
{/snippet}

{#snippet rateRow(
  label: string,
  row: { count: number; hit_per_face: number; low_confidence: boolean; by_source: SourceCell[] },
  indent: boolean,
)}
  <tr class="border-b last:border-0">
    <td class={cn('px-2 py-1.5', indent ? 'pl-6 text-muted-foreground' : 'font-medium text-foreground')}>{label}</td>
    <td class="px-2 py-1.5 text-right">{@render cell(row)}</td>
    {#each row.by_source as sc (sc.source)}
      <td class="px-2 py-1.5 text-right">{@render cell(sc)}</td>
    {/each}
  </tr>
{/snippet}
