<script lang="ts">
  // "New coin / bullion" — a purchase for the long-term stack (not a CRH find).
  // Writes the ADR-003 catalog/specimen split via the Holdings grid's
  // find-or-create logic, so typing a known product (or a silver preset)
  // auto-fills metal/fineness/fine-oz. The Edit tab's Holdings grid corrects one.
  import { onMount } from 'svelte'
  import type { Report, ItemType } from '$lib/types'
  import { api } from '$lib/api'
  import { holdingsGrid, productAutofillFrom, productSuggestionsFrom } from '$lib/grids'
  import { money, oz, today } from '$lib/format'
  import { METALS } from '$lib/presets'
  import Card from '$lib/components/ui/Card.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { ArrowLeft, Check, Coins } from 'lucide-svelte'

  let {
    report,
    onChanged,
    onClose,
  }: { report: Report; onChanged: () => void; onClose: () => void } = $props()

  // form state
  let product = $state('')
  let metal = $state<string>('gold')
  let fineness = $state('')
  let fineOz = $state(0)
  let qty = $state(1)
  let basis = $state(0)
  let acquired = $state(today())
  let source = $state('')

  let catalog = $state<ItemType[]>([])
  let busy = $state(false)
  let err = $state('')
  let done = $state<{ qty: number; product: string } | null>(null)

  const suggestions = $derived(productSuggestionsFrom(catalog))

  onMount(async () => {
    try {
      catalog = await api.itemTypes.list()
    } catch {
      /* autofill optional */
    }
  })

  // When the product matches a known type/preset, fill the metal/fineness/fine-oz.
  function onProduct() {
    const fill = productAutofillFrom(product, catalog)
    if (fill) {
      metal = fill.metal
      fineness = fill.fineness
      fineOz = fill.fine_oz_each
    }
  }

  const spot = $derived(
    metal === 'gold'
      ? report.spot.gold_usd
      : metal === 'silver'
        ? report.spot.silver_usd
        : metal === 'platinum'
          ? report.spot.platinum_usd
          : metal === 'palladium'
            ? report.spot.palladium_usd
            : 0,
  )
  const estMelt = $derived((Number(qty) || 0) * (Number(fineOz) || 0) * spot)

  async function submit() {
    if (!product.trim()) {
      err = 'Name the product.'
      return
    }
    if ((Number(qty) || 0) <= 0) {
      err = 'Quantity must be greater than 0.'
      return
    }
    busy = true
    err = ''
    try {
      await holdingsGrid.create({
        activity: 'bullion',
        product: product.trim(),
        metal,
        fineness: fineness.trim(),
        fine_oz_each: Number(fineOz) || 0,
        qty: Number(qty) || 0,
        basis_usd: Number(basis) || 0,
        premium_usd: 0, // not collected in the quick-entry form; editable in the Holdings grid
        face_value_usd: 0,
        acquired: acquired || today(),
        source: source.trim(),
        from_box: '',
      })
      onChanged()
      done = { qty: Number(qty) || 0, product: product.trim() }
    } catch (e) {
      err = (e as Error).message
    } finally {
      busy = false
    }
  }

  function again() {
    done = null
    product = ''
    fineness = ''
    fineOz = 0
    qty = 1
    basis = 0
    source = ''
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
      <Coins class="size-5" />
    </span>
    <div>
      <h2 class="text-lg font-bold leading-tight">New coin / bullion</h2>
      <p class="text-xs text-muted-foreground">A purchase for the long-term stack.</p>
    </div>
  </div>

  {#if done}
    <Card class="space-y-3 p-5">
      <div class="flex items-start gap-2 text-positive">
        <Check class="mt-0.5 size-5 shrink-0" />
        <div>
          <p class="font-semibold text-foreground">Added {done.qty} × {done.product} to the stack.</p>
          <p class="text-sm text-muted-foreground">
            Bullion now totals {money(report.bullion_market)} at market.
          </p>
        </div>
      </div>
      <div class="flex gap-2">
        <Button variant="secondary" onclick={again}>Add another</Button>
        <Button onclick={onClose}>Done</Button>
      </div>
    </Card>
  {:else}
    <Card class="space-y-4 p-5">
      <div class="grid grid-cols-2 gap-3">
        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          Product
          <input
            type="text"
            list="nb-products"
            placeholder="1 oz American Gold Eagle"
            bind:value={product}
            oninput={onProduct}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
          <datalist id="nb-products">
            {#each suggestions as s (s)}<option value={s}></option>{/each}
          </datalist>
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Metal
          <select
            bind:value={metal}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          >
            {#each METALS as m (m)}<option value={m}>{m}</option>{/each}
          </select>
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Fineness
          <input
            type="text"
            placeholder=".9999"
            bind:value={fineness}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Fine oz / unit
          <input
            type="number"
            step="0.0001"
            min="0"
            bind:value={fineOz}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Quantity
          <input
            type="number"
            step="1"
            min="0"
            bind:value={qty}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Total paid (basis) $
          <input
            type="number"
            step="0.01"
            min="0"
            bind:value={basis}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Acquired
          <input
            type="date"
            bind:value={acquired}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Source (optional)
          <input
            type="text"
            placeholder="APMEX"
            bind:value={source}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
      </div>

      {#if estMelt > 0}
        <p class="text-xs text-muted-foreground">
          ≈ {oz((Number(qty) || 0) * (Number(fineOz) || 0))} fine oz · {money(estMelt)} melt at current spot.
        </p>
      {/if}

      {#if err}<p class="text-sm text-destructive">{err}</p>{/if}

      <div class="flex justify-end gap-2">
        <Button variant="ghost" onclick={onClose}>Cancel</Button>
        <Button onclick={submit} disabled={busy}>Add to stack</Button>
      </div>
    </Card>
  {/if}
</div>
