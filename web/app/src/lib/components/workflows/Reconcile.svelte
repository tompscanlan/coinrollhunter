<script lang="ts">
  // "Reconcile / close the books" — drives a perpetually-nonzero float to $0
  // honestly (ADR-005). The float (to_redeposit = bought − returned − kept) sits
  // nonzero because of machine miscounts, lost coins, and short deposits. But
  // some of it may just be inventory you forgot to record. So this is split:
  //   1. record any forgotten keepers/finds (reduces the float, NOT a loss), then
  //   2. book only the genuine remainder as a loss (an auditable expense).
  // A loss is correctable later — delete the row in Edit and the float reopens.
  import { onMount } from 'svelte'
  import type { Report, RollTxn, ItemType } from '$lib/types'
  import { api } from '$lib/api'
  import { holdingsGrid, productAutofillFrom, productSuggestionsFrom } from '$lib/grids'
  import { money, today } from '$lib/format'
  import { DENOMS, SILVER_PRESETS, COIN_FACE } from '$lib/presets'
  import Card from '$lib/components/ui/Card.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { ArrowLeft, Check, Scale, TriangleAlert } from 'lucide-svelte'

  let {
    report,
    onChanged,
    onClose,
  }: { report: Report; onChanged: () => void; onClose: () => void } = $props()

  // The live unaccounted float (recomputes as inventory is recorded — `report`
  // is a prop the parent refreshes via onChanged).
  const outstanding = $derived(Math.max(0, Math.round(report.to_redeposit * 100) / 100))
  const square = $derived(outstanding < 0.01)

  // --- step 1: record forgotten inventory -----------------------------------
  let buys = $state<RollTxn[]>([])
  let catalog = $state<ItemType[]>([])
  let recorded = $state(0) // how many inventory rows we've added so far
  let note = $state('') // transient "added X" confirmation

  // quick-add keeper (bulk/uncategorized clad only — ADR-008)
  let kDenom = $state<string>('halves')
  let kCount = $state(0)
  let kFace = $state(0)
  let kManual = $state(false)
  let kBox = $state<string>('') // box this batch is logged against (audit + double-count guard)
  $effect(() => {
    if (!kManual) kFace = Math.round((Number(kCount) || 0) * (COIN_FACE[kDenom] ?? 0) * 100) / 100
  })
  // Warn when a keeper is added against a box that ALREADY has crh find lots — a
  // notable coin logged as a taxonomy find must not be re-counted as a bulk keeper.
  const crhFindsForBox = $derived(
    kBox ? report.lots.filter((l) => l.activity === 'crh' && String(l.roll_txn_id ?? 0) === kBox) : []
  )
  const keeperDoubleCount = $derived(crhFindsForBox.length > 0)
  const kBoxDate = $derived(kBox ? (buys.find((b) => String(b.id) === kBox)?.date ?? '') : '')

  // quick-add notable find
  let fProduct = $state('')
  let fMetal = $state('silver')
  let fFineness = $state('')
  let fFineOz = $state(0)
  let fFaceEach = $state(0)
  let fQty = $state(1)
  let fFace = $state(0)
  let fManual = $state(false)
  let fBox = $state<string>('')
  const suggestions = $derived(productSuggestionsFrom(catalog))
  $effect(() => {
    if (!fManual && fFaceEach > 0) fFace = Math.round((Number(fQty) || 0) * fFaceEach * 100) / 100
  })

  // --- step 2: book the remainder as a loss ---------------------------------
  let lossAmount = $state(0)
  let lossManual = $state(false)
  let reason = $state('')
  let scope = $state('')
  let lossDate = $state(today())
  $effect(() => {
    if (!lossManual) lossAmount = outstanding
  })

  let busy = $state(false)
  let err = $state('')
  let done = $state<{ amount: number } | null>(null)

  onMount(async () => {
    try {
      const [rolls, types] = await Promise.all([api.rollTxns.list(), api.itemTypes.list()])
      buys = rolls.filter((r: RollTxn) => r.action === 'buy')
      catalog = types
    } catch {
      /* optional */
    }
  })

  const fmtBuy = (b: RollTxn) =>
    ['#' + b.id, b.bank, b.denom, (b.date || '').slice(5)].filter(Boolean).join(' · ')

  function fillFind() {
    const fill = productAutofillFrom(fProduct, catalog)
    if (fill) {
      fMetal = fill.metal
      fFineness = fill.fineness
      fFineOz = fill.fine_oz_each
    }
    const norm = (s: string) => (s ?? '').trim().toLowerCase()
    const p = SILVER_PRESETS.find((p) => norm(p.label) === norm(fProduct))
    if (p) fFaceEach = p.face_each
  }

  async function addKeeper() {
    if ((Number(kCount) || 0) <= 0 && (Number(kFace) || 0) <= 0) {
      err = 'Enter a keeper count or face.'
      return
    }
    busy = true
    err = ''
    try {
      await api.keepers.create({
        denom: kDenom,
        count: Number(kCount) || 0,
        face_usd: Number(kFace) || 0,
        date: kBoxDate || today(),
        roll_txn_id: Number(kBox) || 0,
      })
      note = `Recorded ${money(Number(kFace) || 0)} of ${kDenom} keepers.`
      recorded++
      kCount = 0
      kManual = false
      onChanged()
    } catch (e) {
      err = (e as Error).message
    } finally {
      busy = false
    }
  }

  async function addFind() {
    if (!fProduct.trim() || (Number(fQty) || 0) <= 0) {
      err = 'Name the find and its quantity.'
      return
    }
    busy = true
    err = ''
    try {
      const faceTotal = Number(fFace) || 0
      await holdingsGrid.create({
        activity: 'crh',
        product: fProduct.trim(),
        metal: fMetal,
        fineness: fFineness.trim(),
        fine_oz_each: Number(fFineOz) || 0,
        qty: Number(fQty) || 0,
        basis_usd: faceTotal,
        premium_usd: 0, // CRH finds carry no premium over melt (acquired at face)
        face_value_usd: faceTotal,
        acquired: today(),
        source: fBox ? (buys.find((b) => String(b.id) === fBox)?.bank ?? '') : '',
        from_box: fBox,
      })
      note = `Recorded ${fQty} × ${fProduct.trim()}.`
      recorded++
      fProduct = ''
      fQty = 1
      fFace = 0
      fManual = false
      fFaceEach = 0
      onChanged()
    } catch (e) {
      err = (e as Error).message
    } finally {
      busy = false
    }
  }

  async function bookLoss() {
    const amt = Math.round((Number(lossAmount) || 0) * 100) / 100
    if (amt <= 0) {
      err = 'Nothing left to write off.'
      return
    }
    busy = true
    err = ''
    try {
      await api.losses.create({
        date: lossDate || today(),
        amount_usd: amt,
        reason: reason.trim(),
        scope: scope.trim(),
      })
      onChanged()
      done = { amount: amt }
    } catch (e) {
      err = (e as Error).message
    } finally {
      busy = false
    }
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
      <Scale class="size-5" />
    </span>
    <div>
      <h2 class="text-lg font-bold leading-tight">Reconcile / close the books</h2>
      <p class="text-xs text-muted-foreground">Square the float honestly — record inventory, write off the rest.</p>
    </div>
  </div>

  {#if done}
    <Card class="space-y-3 p-5">
      <div class="flex items-start gap-2 text-positive">
        <Check class="mt-0.5 size-5 shrink-0" />
        <div>
          <p class="font-semibold text-foreground">Booked a {money(done.amount)} loss — books closed.</p>
          <p class="text-sm text-muted-foreground">
            {#if outstanding < 0.01}
              The float is reconciled to $0. CRH net dropped by the write-off.
            {:else}
              {money(outstanding)} still unaccounted.
            {/if}
            Found the coins later? Delete the loss in Edit → Losses and the float reopens.
          </p>
        </div>
      </div>
      <Button onclick={onClose}>Done</Button>
    </Card>
  {:else if square}
    <Card class="flex items-start gap-2 p-5 text-sm text-positive">
      <Check class="mt-0.5 size-4 shrink-0" />
      <span><b>Nothing to reconcile.</b> Your float is already squared — bought, returned, and kept all balance.</span>
    </Card>
  {:else}
    <!-- the unaccounted headline -->
    <div class="flex items-start gap-2 rounded-lg border border-warning/30 bg-warning/10 px-4 py-3 text-sm text-warning">
      <TriangleAlert class="mt-0.5 size-4 shrink-0" />
      <span>
        <b>{money(outstanding)} unaccounted.</b> Bought {money(report.buys)} − returned {money(report.returns)}
        − kept {money(report.kept_face)}{#if report.losses > 0} − lost {money(report.losses)}{/if}. Some may be
        inventory you forgot — record it first, then write off what's truly gone.
      </span>
    </div>

    {#if note}
      <p class="rounded-md bg-positive/10 px-3 py-2 text-xs text-positive">{note} {money(outstanding)} left.</p>
    {/if}

    <!-- step 1: record forgotten inventory -->
    <Card class="space-y-4 p-5">
      <div>
        <h3 class="text-sm font-semibold text-foreground">1 · Forgot to record anything?</h3>
        <p class="text-xs text-muted-foreground">Adding inventory shrinks the float — it isn't a loss.</p>
      </div>

      <!-- keeper quick-add -->
      <div class="space-y-1.5">
        <p class="text-xs font-medium text-muted-foreground">Bulk clad keepers</p>
        <p class="text-xs text-muted-foreground">
          Bulk / uncategorized clad only. Already logged this coin as a taxonomy find? Don't add it here too —
          you'd double-count it (ADR-008).
        </p>
        <div class="grid grid-cols-[1fr_auto_auto_auto] items-end gap-2">
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            Denom
            <select
              bind:value={kDenom}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
            >
              {#each DENOMS as d (d)}<option value={d}>{d}</option>{/each}
            </select>
          </label>
          <label class="flex w-16 flex-col gap-1 text-xs text-muted-foreground">
            Count
            <input
              type="number" step="1" min="0" bind:value={kCount}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <label class="flex w-24 flex-col gap-1 text-xs text-muted-foreground">
            Face $
            <input
              type="number" step="0.01" min="0" bind:value={kFace} oninput={() => (kManual = true)}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <Button variant="secondary" size="sm" onclick={addKeeper} disabled={busy} class="mb-0.5">Add</Button>
        </div>
        {#if buys.length}
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            Against box (optional — for audit)
            <select
              bind:value={kBox}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
            >
              <option value="">— (none)</option>
              {#each buys as b (b.id)}<option value={String(b.id)}>{fmtBuy(b)}</option>{/each}
            </select>
          </label>
        {/if}
        {#if keeperDoubleCount}
          <p class="flex items-start gap-1.5 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
            <TriangleAlert class="mt-0.5 size-3.5 shrink-0" />
            <span>
              This box already has {crhFindsForBox.length} taxonomy find{crhFindsForBox.length === 1 ? '' : 's'} recorded
              against it. If any of those coins are in this keeper batch, you'd double-count them — keepers are for
              bulk / uncategorized clad only.
            </span>
          </p>
        {/if}
      </div>

      <!-- find quick-add -->
      <div class="space-y-1.5">
        <p class="text-xs font-medium text-muted-foreground">Notable find</p>
        <div class="grid grid-cols-[1fr_auto_auto_auto] items-end gap-2">
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            Product
            <input
              type="text" list="rc-products" placeholder="90% half" bind:value={fProduct} oninput={fillFind}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
            />
          </label>
          <label class="flex w-16 flex-col gap-1 text-xs text-muted-foreground">
            Qty
            <input
              type="number" step="1" min="0" bind:value={fQty}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <label class="flex w-24 flex-col gap-1 text-xs text-muted-foreground">
            Face $
            <input
              type="number" step="0.01" min="0" bind:value={fFace} oninput={() => (fManual = true)}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <Button variant="secondary" size="sm" onclick={addFind} disabled={busy} class="mb-0.5">Add</Button>
        </div>
        {#if buys.length}
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            From box (optional)
            <select
              bind:value={fBox}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
            >
              <option value="">— (none)</option>
              {#each buys as b (b.id)}<option value={String(b.id)}>{fmtBuy(b)}</option>{/each}
            </select>
          </label>
        {/if}
        <datalist id="rc-products">
          {#each suggestions as s (s)}<option value={s}></option>{/each}
        </datalist>
      </div>
    </Card>

    <!-- step 2: book the rest as a loss -->
    <Card class="space-y-4 p-5">
      <div>
        <h3 class="text-sm font-semibold text-foreground">2 · Write off the rest</h3>
        <p class="text-xs text-muted-foreground">
          Booked as shrinkage — a real cost that reduces CRH net and closes the float. Honest and reversible.
        </p>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Lost $
          <input
            type="number" step="0.01" min="0" bind:value={lossAmount} oninput={() => (lossManual = true)}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
          {#if lossManual && Math.abs((Number(lossAmount) || 0) - outstanding) > 0.005}
            <button
              type="button"
              class="self-start text-[11px] text-primary underline-offset-2 hover:underline"
              onclick={() => (lossManual = false)}
            >
              the full {money(outstanding)}
            </button>
          {/if}
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Date
          <input
            type="date" bind:value={lossDate}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Reason
          <input
            type="text" placeholder="machine miscount" bind:value={reason}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Scope (optional)
          <input
            type="text" placeholder="June halves run" bind:value={scope}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
      </div>

      {#if err}<p class="text-sm text-destructive">{err}</p>{/if}

      <div class="flex justify-end gap-2">
        <Button variant="ghost" onclick={onClose}>Cancel</Button>
        <Button onclick={bookLoss} disabled={busy || (Number(lossAmount) || 0) <= 0} class="tnum">
          {(Number(lossAmount) || 0) > 0 ? `Book ${money(Number(lossAmount) || 0)} loss → $0` : 'Book loss'}
        </Button>
      </div>
    </Card>
  {/if}
</div>
