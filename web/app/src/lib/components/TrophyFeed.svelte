<script lang="ts">
  // Greatest hits (ADR-006): the finds you flagged as trophies, surfaced as a
  // feed. A trophy is a normal editable column on Holdings (Edit → Holdings),
  // so this list is a filter, not manual curation. Sourced from the live lots in
  // the summary, joined with the find's category for context.
  //
  // om-6hlp: the trophies that HAVE photos rotate through a hero image at the top —
  // most-prized to most-general (the list's own order). No new ranking and no new
  // Dashboard section: the rotation lives here in Insights, keyed off the existing
  // lots.trophy flag, and it simply does not appear when no trophy has a photo yet.
  import type { Report } from '$lib/types'
  import { api } from '$lib/api'
  import { money, oz } from '$lib/format'
  import Card from '$lib/components/ui/Card.svelte'
  import Badge from '$lib/components/ui/Badge.svelte'
  import { Trophy } from 'lucide-svelte'

  let { report }: { report: Report } = $props()

  const trophies = $derived(report.lots.filter((l) => l.trophy))

  // Covers for the rotation: each trophy that has at least one photo contributes its cover
  // (the first photo, seq/uid order). Fetched per trophy — few trophies, and a feed image is
  // strictly optional, so a failure is swallowed.
  let covers = $state<{ uid: string; label: string; sub: string }[]>([])
  let idx = $state(0)

  $effect(() => {
    const list = trophies
    let cancelled = false
    void (async () => {
      const found: { uid: string; label: string; sub: string }[] = []
      for (const l of list) {
        if (!l.uid) continue
        try {
          const ps = await api.photos.list('lot', l.uid)
          if (ps.length) {
            const sub = [l.category, l.subcategory].filter(Boolean).join(' · ')
            found.push({ uid: ps[0].uid, label: l.product || 'Find', sub })
          }
        } catch {
          /* a missing feed image is not an error */
        }
      }
      if (!cancelled) {
        covers = found
        idx = 0
      }
    })()
    return () => {
      cancelled = true
    }
  })

  // Advance the rotation while there is more than one image to show.
  $effect(() => {
    if (covers.length < 2) return
    const t = setInterval(() => {
      idx = (idx + 1) % covers.length
    }, 4500)
    return () => clearInterval(t)
  })

  const current = $derived(covers.length ? covers[idx % covers.length] : null)
</script>

{#if trophies.length}
  <section class="space-y-2">
    <div class="flex items-center justify-between">
      <h2 class="flex items-center gap-1.5 text-lg font-semibold">
        <Trophy class="size-4 text-warning" /> Greatest hits
      </h2>
      <Badge variant="secondary">{trophies.length} troph{trophies.length === 1 ? 'y' : 'ies'}</Badge>
    </div>

    <!-- Rotating hero image of your prized finds (only when a trophy has a photo). -->
    {#if current}
      <div class="relative overflow-hidden rounded-xl border bg-muted/30">
        {#key current.uid}
          <img
            src={api.photos.fileUrl(current.uid, 'display')}
            alt={current.label}
            class="max-h-72 w-full object-contain"
          />
        {/key}
        <div class="absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/60 to-transparent px-4 py-2">
          <p class="truncate text-sm font-medium text-white">{current.label}</p>
          {#if current.sub}<p class="truncate text-xs text-white/80">{current.sub}</p>{/if}
        </div>
      </div>
    {/if}

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
