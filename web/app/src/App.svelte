<script lang="ts">
  import { api } from '$lib/api'
  import type { Report } from '$lib/types'
  import { today } from '$lib/format'
  import Dashboard from '$lib/components/Dashboard.svelte'
  import Do from '$lib/components/Do.svelte'
  import Insights from '$lib/components/Insights.svelte'
  import EditableGrid from '$lib/components/EditableGrid.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { cn } from '$lib/utils'
  import {
    holdingsGrid,
    rollTxnsGrid,
    tripsGrid,
    branchesGrid,
    suppliesGrid,
    keepersGrid,
    lossesGrid,
    type FlatHolding,
  } from '$lib/grids'
  import type { Branch } from '$lib/types'
  import SettingsPanel from '$lib/components/SettingsPanel.svelte'
  import { Moon, Sun, RefreshCw, LayoutDashboard, Table2, Zap, BarChart3, Settings as SettingsIcon, Power } from 'lucide-svelte'

  type View = 'overview' | 'do' | 'insights' | 'edit'
  type DataTab = 'holdings' | 'rolls' | 'trips' | 'branches' | 'supplies' | 'keepers' | 'losses'

  let view = $state<View>('overview')
  let dataTab = $state<DataTab>('holdings')
  let report = $state<Report | null>(null)
  let loading = $state(true)
  let error = $state('')
  let dark = $state(false)
  let settingsOpen = $state(false)
  let landed = $state(false)
  let quit = $state(false)

  // Closing the browser window leaves the server running with no console to
  // Ctrl-C, so it would sit in Task Manager forever. Quitting is a real action
  // the app has to offer (om-9p0l). Every write is already committed to SQLite,
  // so there is nothing to save — but it does end the session, hence the confirm.
  async function quitApp() {
    if (!confirm('Quit CoinRollHunter? Your data is already saved.')) return
    quit = true
    try {
      await api.quit()
    } catch {
      // The server closing the connection mid-response is the success case.
    }
  }

  // A fresh database has no holdings and no roll-txn buys. ADR-012 §4: land such
  // a user on an obvious action, not a wall of zeros — Overview also shows a
  // get-started state below.
  const isEmpty = $derived(!!report && report.lots.length === 0 && report.buy_count === 0)

  async function refresh() {
    try {
      report = await api.summary()
      error = ''
      // First load only: send a brand-new user straight to Do (ADR-012 §4).
      // Never override a navigation the user has already made.
      if (!landed) {
        landed = true
        if (report.lots.length === 0 && report.buy_count === 0) view = 'do'
      }
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
    { id: 'branches', label: 'Branches' },
    { id: 'supplies', label: 'Supplies' },
    { id: 'keepers', label: 'Keepers' },
    { id: 'losses', label: 'Losses' },
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

  // --- merge a duplicate branch into another (ADR-010 dedup) ---
  let branchesReload = $state(0)
  let mergeRow = $state<Branch | null>(null) // the branch to retire (the loser)
  let mergeInto = $state('') // survivor branch id (as string)
  let mergeChoices = $state<Branch[]>([])
  let mergeBusy = $state(false)
  let mergeErr = $state('')

  async function openMerge(row: Branch) {
    mergeRow = row
    mergeInto = ''
    mergeErr = ''
    mergeChoices = (await api.branches.list()).filter((b) => b.id !== row.id)
  }
  async function confirmMerge() {
    if (!mergeRow || !mergeInto) return
    mergeBusy = true
    mergeErr = ''
    try {
      await api.mergeBranches(Number(mergeInto), [mergeRow.id])
      mergeRow = null
      branchesReload++ // the loser is gone + history repointed, server-side
      refresh() // branch_count / yield-by-bank change
    } catch (e) {
      mergeErr = (e as Error).message
    } finally {
      mergeBusy = false
    }
  }
</script>

{#if quit}
  <!-- The server is gone, so the page cannot do anything anymore. Say so plainly
       rather than leaving a live-looking dashboard whose every click fails. -->
  <div class="flex min-h-svh flex-col items-center justify-center gap-3 px-4 text-center">
    <span class="text-4xl">🪙</span>
    <h1 class="text-xl font-bold text-primary">CoinRollHunter has closed</h1>
    <p class="text-sm text-muted-foreground">
      Your data is saved. You can close this window — start the app again any time.
    </p>
  </div>
{:else}
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
      <Button variant="ghost" size="icon" title="Settings" onclick={() => (settingsOpen = true)}>
        <SettingsIcon class="size-4" />
      </Button>
      <Button variant="ghost" size="icon" title="Toggle theme" onclick={() => (dark = !dark)}>
        {#if dark}<Sun class="size-4" />{:else}<Moon class="size-4" />{/if}
      </Button>
      <Button variant="ghost" size="icon" title="Quit CoinRollHunter" onclick={quitApp}>
        <Power class="size-4" />
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
            view === 'do' ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
          )}
          onclick={() => (view = 'do')}
        >
          <Zap class="size-4" /> Do
        </button>
        <button
          class={cn(
            'flex items-center gap-1.5 rounded-md px-4 py-1.5 transition-colors',
            view === 'insights' ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
          )}
          onclick={() => (view = 'insights')}
        >
          <BarChart3 class="size-4" /> Insights
        </button>
        <button
          class={cn(
            'flex items-center gap-1.5 rounded-md px-4 py-1.5 transition-colors',
            view === 'edit' ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
          )}
          onclick={() => (view = 'edit')}
        >
          <Table2 class="size-4" /> Edit
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
        {#if isEmpty}
          <!-- get-started state: one obvious action, not a wall of zeros (ADR-012 §4) -->
          <div class="mx-auto mt-6 max-w-md rounded-xl border bg-card p-8 text-center shadow-sm">
            <div class="text-4xl">🪙</div>
            <h2 class="mt-3 text-lg font-semibold text-foreground">Start your first hunt</h2>
            <p class="mt-1.5 text-sm text-muted-foreground">
              Log a box of coins you picked up from the bank — CoinRollHunter tracks the rest:
              finds, costs, and whether it's paying off.
            </p>
            <Button class="mt-4" onclick={() => (view = 'do')}>Log your first box →</Button>
          </div>
        {:else}
          <Dashboard {report} />
        {/if}
      {/if}
    {:else if view === 'do'}
      {#if report}
        <Do {report} onChanged={refresh} />
      {/if}
    {:else if view === 'insights'}
      {#if report}
        <Insights {report} />
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
        {:else if dataTab === 'branches'}
          <EditableGrid
            {...branchesGrid}
            onChanged={refresh}
            rowAction={openMerge}
            rowActionTitle="Merge into…"
            reloadSignal={branchesReload}
          />
        {:else if dataTab === 'supplies'}
          <EditableGrid {...suppliesGrid} onChanged={refresh} />
        {:else if dataTab === 'keepers'}
          <EditableGrid {...keepersGrid} onChanged={refresh} />
        {:else if dataTab === 'losses'}
          <EditableGrid {...lossesGrid} onChanged={refresh} />
        {/if}
      </section>
    {/if}
  </main>
</div>
{/if}

{#if settingsOpen}
  <SettingsPanel spot={report?.spot} onClose={() => (settingsOpen = false)} onSaved={refresh} />
{/if}

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

{#if mergeRow}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-sm space-y-4 rounded-xl border bg-card p-5 shadow-lg">
      <div>
        <h3 class="text-lg font-semibold text-foreground">Merge branch</h3>
        <p class="text-sm text-muted-foreground">
          Fold <b class="text-foreground">{mergeRow.name || 'this branch'}</b> into another. Its
          transactions, trips, and every old spelling move to the survivor; this row is then removed.
          Nothing is lost — the merge is a repoint, not a delete.
        </p>
      </div>
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        Survivor branch
        <select
          bind:value={mergeInto}
          class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
        >
          <option value="" disabled>Choose a branch…</option>
          {#each mergeChoices as b (b.id)}
            <option value={String(b.id)}>{b.name}</option>
          {/each}
        </select>
      </label>
      {#if mergeErr}<p class="text-sm text-destructive">{mergeErr}</p>{/if}
      <div class="flex justify-end gap-2">
        <Button variant="ghost" onclick={() => (mergeRow = null)}>Cancel</Button>
        <Button onclick={confirmMerge} disabled={mergeBusy || !mergeInto}>Merge</Button>
      </div>
    </div>
  </div>
{/if}
