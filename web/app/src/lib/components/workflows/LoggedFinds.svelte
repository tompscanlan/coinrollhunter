<script lang="ts">
  // "Logged finds" — the payoff step of the hunt. Records the silver you pulled
  // (CRH Holdings) and any clad keepers, both attributed to the box they came
  // from so the per-bank/per-box yield works (calc.box_yields). If the box was
  // never logged (the Fifth-Third gap), you can create the buy inline here. The
  // Edit tab's Holdings/Keepers grids correct any of these afterward.
  import { onMount } from 'svelte'
  import type { Report, RollTxn, ItemType } from '$lib/types'
  import { api } from '$lib/api'
  import { holdingsGrid, productAutofillFrom, productSuggestionsFrom } from '$lib/grids.svelte'
  import { money, today } from '$lib/format'
  import { DENOMS, ROLL_UNITS, SOURCE_TYPES, SILVER_PRESETS, COIN_FACE, faceFor } from '$lib/presets'
  import Card from '$lib/components/ui/Card.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { ArrowLeft, Check, Search, Plus, X, TriangleAlert } from 'lucide-svelte'

  let {
    onChanged,
    onClose,
  }: { report: Report; onChanged: () => void; onClose: () => void } = $props()

  type FindRow = {
    product: string
    metal: string
    fineness: string
    fineOz: number
    faceEach: number
    qty: number
    face: number
    faceManual: boolean
  }
  type KeeperRow = { denom: string; count: number; face: number; faceManual: boolean }

  const blankFind = (): FindRow => ({
    product: '',
    metal: 'silver',
    fineness: '',
    fineOz: 0,
    faceEach: 0,
    qty: 1,
    face: 0,
    faceManual: false,
  })
  const blankKeeper = (): KeeperRow => ({ denom: 'halves', count: 0, face: 0, faceManual: false })

  // box attribution
  let buys = $state<RollTxn[]>([])
  let boxChoice = $state<string>('__new__') // '__new__' or a roll_txn id
  const useNewBox = $derived(boxChoice === '__new__')

  // inline new-box fields (only used when useNewBox)
  let nbBank = $state('')
  let nbDenom = $state<string>('halves')
  let nbDate = $state(today())
  let nbUnit = $state<string>('box')
  let nbSourceType = $state<string>('') // wrapping/yield class (ADR-006)
  let nbAmount = $state(1)
  let nbFace = $state(500)
  let nbManualFace = $state(false)
  const nbAutoFace = $derived(Math.round(faceFor(nbUnit, nbDenom, nbAmount) * 100) / 100)
  $effect(() => {
    if (!nbManualFace) nbFace = nbAutoFace
  })

  let finds = $state<FindRow[]>([blankFind()])
  let keepers = $state<KeeperRow[]>([])
  let catalog = $state<ItemType[]>([])

  let busy = $state(false)
  let err = $state('')
  let done = $state<{ findValue: number; bank: string } | null>(null)

  const suggestions = $derived(productSuggestionsFrom(catalog))

  onMount(async () => {
    try {
      const [rolls, types] = await Promise.all([api.rollTxns.list(), api.itemTypes.list()])
      buys = rolls.filter((r: RollTxn) => r.action === 'buy')
      catalog = types
      // Default to an existing box when there is one; else inline-create (the gap).
      if (buys.length) boxChoice = String(buys[buys.length - 1].id)
    } catch {
      /* optional */
    }
  })

  const selectedBuy = $derived(buys.find((b) => String(b.id) === boxChoice) ?? null)
  const chosenBank = $derived(useNewBox ? nbBank : (selectedBuy?.bank ?? ''))
  const chosenDate = $derived(useNewBox ? nbDate : (selectedBuy?.date ?? today()))

  const fmtBuy = (b: RollTxn) =>
    ['#' + b.id, b.bank, b.denom, (b.date || '').slice(5), money(b.face_usd)].filter(Boolean).join(' · ')

  function fillFind(i: number) {
    const row = finds[i]
    const fill = productAutofillFrom(row.product, catalog)
    if (fill) {
      row.metal = fill.metal
      row.fineness = fill.fineness
      row.fineOz = fill.fine_oz_each
    }
    const norm = (s: string) => (s ?? '').trim().toLowerCase()
    const p = SILVER_PRESETS.find((p) => norm(p.label) === norm(row.product))
    if (p) row.faceEach = p.face_each
    recomputeFindFace(i)
  }
  function recomputeFindFace(i: number) {
    const row = finds[i]
    if (!row.faceManual && row.faceEach > 0) {
      row.face = Math.round((Number(row.qty) || 0) * row.faceEach * 100) / 100
    }
  }
  function recomputeKeeperFace(i: number) {
    const row = keepers[i]
    if (!row.faceManual) {
      row.face = Math.round((Number(row.count) || 0) * (COIN_FACE[row.denom] ?? 0) * 100) / 100
    }
  }

  const findValueTotal = $derived(finds.reduce((s, f) => s + (Number(f.face) || 0), 0))

  async function submit() {
    const realFinds = finds.filter((f) => f.product.trim() && (Number(f.qty) || 0) > 0)
    const realKeepers = keepers.filter((k) => (Number(k.count) || 0) > 0 || (Number(k.face) || 0) > 0)
    if (!realFinds.length && !realKeepers.length) {
      err = 'Add at least one find or keeper.'
      return
    }
    busy = true
    err = ''
    try {
      // 1) create the buy inline if needed (and the user actually filled it in)
      let boxId = 0
      if (useNewBox) {
        if (nbBank.trim() && (Number(nbFace) || 0) > 0) {
          boxId = await api.rollTxns.create({
            date: nbDate || today(),
            bank: nbBank.trim(),
            action: 'buy',
            denom: nbDenom,
            unit: nbUnit,
            source_type: nbSourceType,
            amount: Number(nbAmount) || 0,
            face_usd: Number(nbFace) || 0,
            notes: '',
          })
        }
      } else {
        boxId = Number(boxChoice) || 0
      }

      // 2) finds → CRH holdings, linked to the box
      for (const f of realFinds) {
        const faceTotal = Number(f.face) || 0
        await holdingsGrid.create({
          activity: 'crh',
          product: f.product.trim(),
          metal: f.metal,
          fineness: f.fineness.trim(),
          fine_oz_each: Number(f.fineOz) || 0,
          qty: Number(f.qty) || 0,
          basis_usd: faceTotal,
          premium_usd: 0, // CRH finds carry no premium over melt (acquired at face)
          face_value_usd: faceTotal,
          acquired: chosenDate || today(),
          source: chosenBank.trim(),
          from_box: boxId ? String(boxId) : '',
        })
      }

      // 3) bulk clad keepers — attributed to the same box/date as the finds so a
      //    later Reconcile can tell whether a batch was already counted (ADR-008).
      for (const k of realKeepers) {
        await api.keepers.create({
          denom: k.denom,
          count: Number(k.count) || 0,
          face_usd: Number(k.face) || 0,
          date: chosenDate || today(),
          roll_txn_id: boxId || 0,
        })
      }

      onChanged()
      done = { findValue: findValueTotal, bank: chosenBank.trim() }
    } catch (e) {
      err = (e as Error).message
    } finally {
      busy = false
    }
  }

  function reset() {
    done = null
    finds = [blankFind()]
    keepers = []
    nbManualFace = false
  }
</script>

<div class="mx-auto max-w-xl space-y-4">
  <button
    class="flex items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
    onclick={onClose}
  >
    <ArrowLeft class="size-4" /> All actions
  </button>

  <div class="flex items-center gap-2.5">
    <span class="flex size-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
      <Search class="size-5" />
    </span>
    <div>
      <h2 class="text-lg font-bold leading-tight">Logged finds</h2>
      <p class="text-xs text-muted-foreground">Notable finds + bulk clad keepers from a searched box.</p>
    </div>
  </div>

  {#if done}
    <Card class="space-y-3 p-5">
      <div class="flex items-start gap-2 text-positive">
        <Check class="mt-0.5 size-5 shrink-0" />
        <div>
          <p class="font-semibold text-foreground">Logged {money(done.findValue)} in find face.</p>
          <p class="text-sm text-muted-foreground">
            Attributed {done.bank ? `to ${done.bank}` : ''} — see the per-box yield on the Overview. Return
            the culls when you're done searching.
          </p>
        </div>
      </div>
      <div class="flex gap-2">
        <Button variant="secondary" onclick={reset}>Log more</Button>
        <Button onclick={onClose}>Done</Button>
      </div>
    </Card>
  {:else}
    <!-- box attribution -->
    <Card class="space-y-3 p-5">
      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
        Which box did these come from?
        <select
          bind:value={boxChoice}
          class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
        >
          <option value="__new__">＋ Log the box now (not recorded yet)</option>
          {#each buys as b (b.id)}<option value={String(b.id)}>{fmtBuy(b)}</option>{/each}
        </select>
      </label>

      {#if useNewBox}
        <div class="rounded-lg border border-dashed border-input p-3">
          <p class="mb-2 flex items-center gap-1.5 text-xs text-muted-foreground">
            <TriangleAlert class="size-3.5" /> No buy logged — record it so the yield is attributable.
          </p>
          <div class="grid grid-cols-2 gap-2.5">
            <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
              Bank
              <input
                type="text"
                placeholder="Fifth Third"
                bind:value={nbBank}
                class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
              />
            </label>
            <label class="flex flex-col gap-1 text-xs text-muted-foreground">
              Denom
              <select
                bind:value={nbDenom}
                class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
              >
                {#each DENOMS as d (d)}<option value={d}>{d}</option>{/each}
              </select>
            </label>
            <label class="flex flex-col gap-1 text-xs text-muted-foreground">
              Unit
              <select
                bind:value={nbUnit}
                class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
              >
                {#each ROLL_UNITS as u (u)}<option value={u}>{u}</option>{/each}
              </select>
            </label>
            <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
              Source type
              <select
                bind:value={nbSourceType}
                class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
              >
                {#each SOURCE_TYPES as s (s.value)}<option value={s.value}>{s.label}</option>{/each}
              </select>
            </label>
            <label class="flex flex-col gap-1 text-xs text-muted-foreground">
              How many
              <input
                type="number"
                step="0.1"
                min="0"
                bind:value={nbAmount}
                class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
              />
            </label>
            <label class="flex flex-col gap-1 text-xs text-muted-foreground">
              Face $
              <input
                type="number"
                step="0.01"
                min="0"
                bind:value={nbFace}
                oninput={() => (nbManualFace = true)}
                class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
              />
            </label>
            <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
              Date
              <input
                type="date"
                bind:value={nbDate}
                class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
              />
            </label>
          </div>
        </div>
      {/if}
    </Card>

    <!-- finds -->
    <Card class="space-y-3 p-5">
      <div class="flex items-center justify-between">
        <h3 class="text-sm font-semibold text-foreground">Notable finds</h3>
        <span class="text-xs text-muted-foreground tnum">{money(findValueTotal)} face</span>
      </div>
      <p class="text-xs text-muted-foreground">
        Any individually-notable coin — silver <em>or</em> clad (proof, error, key date, PMD…). Logged as a
        taxonomy find in Holdings, not a keeper (ADR-008).
      </p>
      {#each finds as f, i (i)}
        <div class="grid grid-cols-[1fr_auto_auto_auto] items-end gap-2">
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            {i === 0 ? 'Product' : ''}
            <input
              type="text"
              list="lf-products"
              placeholder="90% half (1964 & earlier)"
              bind:value={f.product}
              oninput={() => fillFind(i)}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
            />
          </label>
          <label class="flex w-16 flex-col gap-1 text-xs text-muted-foreground">
            {i === 0 ? 'Qty' : ''}
            <input
              type="number"
              step="1"
              min="0"
              bind:value={f.qty}
              oninput={() => recomputeFindFace(i)}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <label class="flex w-24 flex-col gap-1 text-xs text-muted-foreground">
            {i === 0 ? 'Face $' : ''}
            <input
              type="number"
              step="0.01"
              min="0"
              bind:value={f.face}
              oninput={() => (f.faceManual = true)}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <button
            type="button"
            class="mb-1.5 text-muted-foreground hover:text-destructive disabled:opacity-30"
            disabled={finds.length === 1}
            onclick={() => (finds = finds.filter((_, j) => j !== i))}
            title="Remove"
          >
            <X class="size-4" />
          </button>
        </div>
      {/each}
      <datalist id="lf-products">
        {#each suggestions as s (s)}<option value={s}></option>{/each}
      </datalist>
      <button
        type="button"
        class="flex items-center gap-1 text-xs font-medium text-primary hover:underline"
        onclick={() => (finds = [...finds, blankFind()])}
      >
        <Plus class="size-3.5" /> Add a find
      </button>
    </Card>

    <!-- clad keepers -->
    <Card class="space-y-3 p-5">
      <h3 class="text-sm font-semibold text-foreground">Bulk clad keepers (kept at face)</h3>
      <p class="flex items-start gap-1.5 text-xs text-muted-foreground">
        <TriangleAlert class="mt-0.5 size-3.5 shrink-0" />
        <span>
          <b>Bulk / uncategorized clad only.</b> Already logged this coin as a taxonomy find above? Don't add it
          here too — you'd double-count its face and understate what's left to redeposit.
        </span>
      </p>
      {#if keepers.length === 0}
        <p class="text-xs text-muted-foreground">None — add the leftover bulk clad you're keeping.</p>
      {/if}
      {#each keepers as k, i (i)}
        <div class="grid grid-cols-[1fr_auto_auto_auto] items-end gap-2">
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            {i === 0 ? 'Denom' : ''}
            <select
              bind:value={k.denom}
              onchange={() => recomputeKeeperFace(i)}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
            >
              {#each DENOMS as d (d)}<option value={d}>{d}</option>{/each}
            </select>
          </label>
          <label class="flex w-16 flex-col gap-1 text-xs text-muted-foreground">
            {i === 0 ? 'Count' : ''}
            <input
              type="number"
              step="1"
              min="0"
              bind:value={k.count}
              oninput={() => recomputeKeeperFace(i)}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <label class="flex w-24 flex-col gap-1 text-xs text-muted-foreground">
            {i === 0 ? 'Face $' : ''}
            <input
              type="number"
              step="0.01"
              min="0"
              bind:value={k.face}
              oninput={() => (k.faceManual = true)}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <button
            type="button"
            class="mb-1.5 text-muted-foreground hover:text-destructive"
            onclick={() => (keepers = keepers.filter((_, j) => j !== i))}
            title="Remove"
          >
            <X class="size-4" />
          </button>
        </div>
      {/each}
      <button
        type="button"
        class="flex items-center gap-1 text-xs font-medium text-primary hover:underline"
        onclick={() => (keepers = [...keepers, blankKeeper()])}
      >
        <Plus class="size-3.5" /> Add a keeper
      </button>
    </Card>

    {#if err}<p class="text-sm text-destructive">{err}</p>{/if}

    <div class="flex justify-end gap-2">
      <Button variant="ghost" onclick={onClose}>Cancel</Button>
      <Button onclick={submit} disabled={busy}>Save finds</Button>
    </div>
  {/if}
</div>
