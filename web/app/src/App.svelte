<script lang="ts">
  import { api } from '$lib/api'
  import type { Report } from '$lib/types'
  import { today } from '$lib/format'
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
    type FlatHolding,
  } from '$lib/grids'
  import { Moon, Sun, RefreshCw, LayoutDashboard, Table2 } from 'lucide-svelte'

  type View = 'overview' | 'entry'
  type DataTab = 'holdings' | 'rolls' | 'trips' | 'supplies' | 'keepers'

  let view = $state<View>('overview')
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

  // --- sell a holding (full or partial) ---
  let holdingsReload = $state(0) // bump to force the Holdings grid to reload
  let sellRow = $state<FlatHolding | null>(null)
  let sellQty = $state(0)
  let sellProceeds = $state(0)
  let sellDate = $state('')
  let sellBusy = $state(false)
  let sellErr = $state('')

  function openSell(row: FlatHolding) {
    sellRow = row
    sellQty = row.qty
    sellProceeds = 0
    sellDate = today()
    sellErr = ''
  }
  async function confirmSell() {
    if (!sellRow) return
    sellBusy = true
    sellErr = ''
    try {
      await api.sellHolding(sellRow.id, {
        qty: Number(sellQty) || 0,
        proceeds_usd: Number(sellProceeds) || 0,
        date: sellDate,
      })
      sellRow = null
      holdingsReload++ // reload the grid (the split/disposal happened server-side)
      refresh() // recompute the overview (realized section + stack)
    } catch (e) {
      sellErr = (e as Error).message
    } finally {
      sellBusy = false
    }
  }
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

  <!-- view toggle: one place, instant switch between the numbers and entry -->
  {#if !error && !(loading && !report)}
    <div class="mt-5 flex justify-center">
      <div class="inline-flex rounded-lg border bg-muted/40 p-0.5 text-sm font-medium">
        <button
          class={cn(
            'flex items-center gap-1.5 rounded-md px-4 py-1.5 transition-colors',
            view === 'overview' ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
          )}
          onclick={() => (view = 'overview')}
        >
          <LayoutDashboard class="size-4" /> Overview
        </button>
        <button
          class={cn(
            'flex items-center gap-1.5 rounded-md px-4 py-1.5 transition-colors',
            view === 'entry' ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
          )}
          onclick={() => (view = 'entry')}
        >
          <Table2 class="size-4" /> Entry
        </button>
      </div>
    </div>
  {/if}

  <main class="mt-6">
    {#if error}
      <div class="rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm text-destructive">
        Couldn't reach the API: {error}
      </div>
    {:else if loading && !report}
      <p class="text-sm text-muted-foreground">Loading…</p>
    {:else if view === 'overview'}
      {#if report}
        <Dashboard {report} onRefresh={refresh} />
      {/if}
    {:else}
      <section class="space-y-4">
        <div class="flex items-center justify-between gap-3">
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
          <button
            class="shrink-0 text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            onclick={() => (view = 'overview')}
          >
            edits save instantly · see impact in Overview →
          </button>
        </div>

        {#if dataTab === 'holdings'}
          <EditableGrid
            {...holdingsGrid}
            onChanged={refresh}
            rowAction={openSell}
            rowActionTitle="Sell / dispose"
            reloadSignal={holdingsReload}
          />
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

{#if sellRow}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-sm space-y-4 rounded-xl border bg-card p-5 shadow-lg">
      <div>
        <h3 class="text-lg font-semibold text-foreground">Sell {sellRow.product || 'holding'}</h3>
        <p class="text-sm text-muted-foreground">
          You hold {sellRow.qty} unit{sellRow.qty === 1 ? '' : 's'}. Selling fewer splits the lot — the
          rest stays in your stack; the sold portion moves to realized P&amp;L.
        </p>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Qty to sell
          <input
            type="number" step="any" min="0" max={sellRow.qty} bind:value={sellQty}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Proceeds $
          <input
            type="number" step="0.01" min="0" bind:value={sellProceeds}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
        </label>
        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          Date sold
          <input
            type="date" bind:value={sellDate}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
      </div>
      {#if sellErr}<p class="text-sm text-destructive">{sellErr}</p>{/if}
      <div class="flex justify-end gap-2">
        <Button variant="ghost" onclick={() => (sellRow = null)}>Cancel</Button>
        <Button onclick={confirmSell} disabled={sellBusy}>Record sale</Button>
      </div>
    </div>
  </div>
{/if}
