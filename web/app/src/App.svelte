<script lang="ts">
  import { api } from '$lib/api'
  import type { Report } from '$lib/types'
  import Dashboard from '$lib/components/Dashboard.svelte'
  import EditableGrid from '$lib/components/EditableGrid.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { cn } from '$lib/utils'
  import {
    holdingsGrid,
    rollTxnsGrid,
    tripsGrid,
    suppliesGrid,
    keepersGrid,
  } from '$lib/grids'
  import { Moon, Sun, RefreshCw } from 'lucide-svelte'

  type DataTab = 'holdings' | 'rolls' | 'trips' | 'supplies' | 'keepers'

  let dataTab = $state<DataTab>('holdings')
  let report = $state<Report | null>(null)
  let loading = $state(true)
  let error = $state('')
  let dark = $state(false)

  async function refresh() {
    try {
      report = await api.summary()
      error = ''
    } catch (e) {
      error = (e as Error).message
    } finally {
      loading = false
    }
  }
  $effect(() => {
    refresh()
  })
  $effect(() => {
    document.documentElement.classList.toggle('dark', dark)
  })

  const dataTabs: { id: DataTab; label: string }[] = [
    { id: 'holdings', label: 'Holdings' },
    { id: 'rolls', label: 'Roll txns' },
    { id: 'trips', label: 'Trips' },
    { id: 'supplies', label: 'Supplies' },
    { id: 'keepers', label: 'Keepers' },
  ]
</script>

<div class="mx-auto min-h-svh max-w-6xl px-4 pb-20 pt-6">
  <!-- header -->
  <header class="flex items-center justify-between gap-3">
    <div class="flex items-center gap-2.5">
      <span class="text-2xl">🪙</span>
      <div>
        <h1 class="text-xl font-bold leading-tight text-primary">CoinRollHunter</h1>
        <p class="text-xs text-muted-foreground">Local-first coins &amp; bullion tracker</p>
      </div>
    </div>
    <div class="flex items-center gap-2">
      <Button variant="ghost" size="icon" title="Refresh" onclick={refresh}>
        <RefreshCw class={cn('size-4', loading && 'animate-spin')} />
      </Button>
      <Button variant="ghost" size="icon" title="Toggle theme" onclick={() => (dark = !dark)}>
        {#if dark}<Sun class="size-4" />{:else}<Moon class="size-4" />{/if}
      </Button>
    </div>
  </header>

  <main class="mt-6 space-y-8">
    {#if error}
      <div class="rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm text-destructive">
        Couldn't reach the API: {error}
      </div>
    {:else if loading && !report}
      <p class="text-sm text-muted-foreground">Loading…</p>
    {:else}
      <!-- overview: live numbers + composition snapshot -->
      {#if report}
        <Dashboard {report} onRefresh={refresh} />
      {/if}

      <!-- data entry: same page, edits flow straight into the overview above -->
      <section class="space-y-4 border-t pt-6">
        <div>
          <h2 class="text-lg font-semibold text-foreground">Data entry</h2>
          <p class="text-sm text-muted-foreground">Edits recompute the overview above instantly.</p>
        </div>

        <div class="flex flex-wrap gap-1.5">
          {#each dataTabs as t (t.id)}
            <button
              class={cn(
                'rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
                dataTab === t.id
                  ? 'bg-primary text-primary-foreground'
                  : 'bg-secondary text-secondary-foreground hover:bg-accent',
              )}
              onclick={() => (dataTab = t.id)}
            >
              {t.label}
            </button>
          {/each}
        </div>

        {#if dataTab === 'holdings'}
          <EditableGrid {...holdingsGrid} onChanged={refresh} />
        {:else if dataTab === 'rolls'}
          <EditableGrid {...rollTxnsGrid} onChanged={refresh} />
        {:else if dataTab === 'trips'}
          <EditableGrid {...tripsGrid} onChanged={refresh} />
        {:else if dataTab === 'supplies'}
          <EditableGrid {...suppliesGrid} onChanged={refresh} />
        {:else if dataTab === 'keepers'}
          <EditableGrid {...keepersGrid} onChanged={refresh} />
        {/if}
      </section>
    {/if}
  </main>
</div>
