<script lang="ts">
  // "Sold something" — records a disposal + realized P&L over the existing
  // POST /api/lots/{id}/sell. Pick a holding, say how many and for how much; a
  // partial sale splits the lot server-side (the rest stays in the stack). The
  // Edit tab's per-row Sell button does the same thing.
  import { onMount } from 'svelte'
  import type { Report, Holding, ItemType } from '$lib/types'
  import { api } from '$lib/api'
  import { money, today } from '$lib/format'
  import Card from '$lib/components/ui/Card.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { ArrowLeft, Check, HandCoins } from 'lucide-svelte'

  let {
    report,
    onChanged,
    onClose,
  }: { report: Report; onChanged: () => void; onClose: () => void } = $props()

  type Sellable = { id: number; label: string; activity: string; qty: number; basis: number }

  let items = $state<Sellable[]>([])
  let selectedId = $state<number | null>(null)
  let qty = $state(0)
  let proceeds = $state(0)
  let date = $state(today())

  let loading = $state(true)
  let busy = $state(false)
  let err = $state('')
  let done = $state<{ label: string; gain: number } | null>(null)

  const selected = $derived(items.find((i) => i.id === selectedId) ?? null)
  // proportional basis of the portion being sold → realized gain preview
  const soldBasis = $derived(
    selected && selected.qty > 0 ? (selected.basis * (Number(qty) || 0)) / selected.qty : 0,
  )
  const gain = $derived((Number(proceeds) || 0) - soldBasis)

  onMount(load)
  async function load() {
    loading = true
    try {
      const [holdings, types] = await Promise.all([api.holdings.list(), api.itemTypes.list()])
      const byId = new Map<number, ItemType>(types.map((t) => [t.id, t]))
      items = holdings
        .filter((h: Holding) => !h.disposed)
        .map((h: Holding) => ({
          id: h.id,
          label: byId.get(h.item_type_id)?.name || `holding #${h.id}`,
          activity: h.activity,
          qty: h.qty,
          basis: h.basis_usd,
        }))
      if (items.length && selectedId == null) select(items[0].id)
    } catch (e) {
      err = (e as Error).message
    } finally {
      loading = false
    }
  }

  function select(id: number) {
    selectedId = id
    qty = items.find((i) => i.id === id)?.qty ?? 0
    proceeds = 0
  }

  async function submit() {
    if (!selected) return
    if ((Number(qty) || 0) <= 0) {
      err = 'Quantity to sell must be greater than 0.'
      return
    }
    busy = true
    err = ''
    try {
      await api.sellHolding(selected.id, {
        qty: Number(qty) || 0,
        proceeds_usd: Number(proceeds) || 0,
        date: date || today(),
      })
      onChanged()
      done = { label: selected.label, gain }
      selectedId = null
      await load()
    } catch (e) {
      err = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<div class="mx-auto max-w-lg space-y-4">
  <button
    class="flex items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
    onclick={onClose}
  >
    <ArrowLeft class="size-4" /> All actions
  </button>

  <div class="flex items-center gap-2.5">
    <span class="flex size-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
      <HandCoins class="size-5" />
    </span>
    <div>
      <h2 class="text-lg font-bold leading-tight">Sold something</h2>
      <p class="text-xs text-muted-foreground">Record a sale and its realized profit or loss.</p>
    </div>
  </div>

  {#if done}
    <Card class="space-y-3 p-5">
      <div class="flex items-start gap-2 text-positive">
        <Check class="mt-0.5 size-5 shrink-0" />
        <div>
          <p class="font-semibold text-foreground">Sold {done.label}.</p>
          <p class="text-sm text-muted-foreground">
            Realized
            <b class={done.gain >= 0 ? 'text-positive' : 'text-negative'}>{money(done.gain)}</b>.
            Total realized to date: {money(report.realized_gain)}.
          </p>
        </div>
      </div>
      <div class="flex gap-2">
        {#if items.length}<Button variant="secondary" onclick={() => (done = null)}>Sell another</Button>{/if}
        <Button onclick={onClose}>Done</Button>
      </div>
    </Card>
  {:else if loading}
    <p class="text-sm text-muted-foreground">Loading your holdings…</p>
  {:else if !items.length}
    <Card class="p-5 text-sm text-muted-foreground">
      Nothing to sell yet — add a coin or bullion first, or log some finds.
    </Card>
  {:else}
    <Card class="space-y-4 p-5">
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        Which holding?
        <select
          value={selectedId}
          onchange={(e) => select(Number((e.currentTarget as HTMLSelectElement).value))}
          class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
        >
          {#each items as i (i.id)}
            <option value={i.id}>{i.label} — {i.qty} held ({i.activity})</option>
          {/each}
        </select>
      </label>

      <div class="grid grid-cols-2 gap-3">
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Qty to sell {#if selected}<span class="text-muted-foreground/70">/ {selected.qty}</span>{/if}
          <input
            type="number"
            step="any"
            min="0"
            max={selected?.qty}
            bind:value={qty}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Proceeds $
          <input
            type="number"
            step="0.01"
            min="0"
            bind:value={proceeds}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
        </label>
        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          Date sold
          <input
            type="date"
            bind:value={date}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
      </div>

      <p class="text-xs text-muted-foreground">
        Cost basis of this portion {money(soldBasis)} · realized
        <b class={gain >= 0 ? 'text-positive' : 'text-negative'}>{money(gain)}</b>.
        {#if selected && (Number(qty) || 0) < selected.qty && (Number(qty) || 0) > 0}
          Selling part — the remaining {selected.qty - (Number(qty) || 0)} stay in your stack.
        {/if}
      </p>

      {#if err}<p class="text-sm text-destructive">{err}</p>{/if}

      <div class="flex justify-end gap-2">
        <Button variant="ghost" onclick={onClose}>Cancel</Button>
        <Button onclick={submit} disabled={busy || !selected}>Record sale</Button>
      </div>
    </Card>
  {/if}
</div>
