<script lang="ts">
  // "Returned culls to the bank" — the redeposit step of the hunt loop. Instead
  // of asking the user to hand-write a roll-txn row, it *tells* them how much is
  // outstanding (the engine's to_redeposit = bought − returned − kept) and records
  // an action='return' txn for the amount they actually took back. The Edit tab
  // (roll-txns grid) remains the place to correct one of these afterward.
  import { onMount } from 'svelte'
  import type { Report, RollTxn, Trip } from '$lib/types'
  import { api } from '$lib/api'
  import { money, today } from '$lib/format'
  import { DENOMS } from '$lib/presets'
  import Card from '$lib/components/ui/Card.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { ArrowLeft, Check, Landmark, TriangleAlert } from 'lucide-svelte'
  import { cn } from '$lib/utils'

  let {
    report,
    onChanged,
    onClose,
  }: { report: Report; onChanged: () => void; onClose: () => void } = $props()

  // Outstanding float to hand back = the engine's reconciliation number.
  const outstanding = $derived(Math.max(0, Math.round(report.to_redeposit * 100) / 100))

  // form state
  let bank = $state('')
  let date = $state(today())
  // A redeposit is a lump of face going back; the denom is optional. Default to
  // "" ("Mixed") so a mixed pile (dollars + halves + dimes + quarters) records as
  // one sum — the reconciliation math nets returns globally and never reads their
  // denom (ADR-001/005 single-pool float). Pick a specific denom only if you know it.
  let denom = $state<string>('')
  let amount = $state(0)
  let notes = $state('')
  let banks = $state<string[]>([])

  let busy = $state(false)
  let err = $state('')
  let done = $state<{ amount: number } | null>(null)

  const distinct = (xs: (string | undefined)[]) =>
    [...new Set(xs.map((s) => (s ?? '').trim()).filter(Boolean))].sort((a, b) => a.localeCompare(b))

  onMount(async () => {
    amount = outstanding // pre-fill with everything still owed back
    try {
      const [rolls, trips, branches] = await Promise.all([api.rollTxns.list(), api.trips.list(), api.branches.list()])
      banks = distinct([
        ...branches.map((b) => b.name), // every known branch (ADR-010), reuse over fork
        ...rolls.map((r: RollTxn) => r.bank),
        ...trips.map((t: Trip) => t.bank),
      ])
    } catch {
      /* suggestions are optional; the field still takes free text */
    }
  })

  const amt = $derived(Math.max(0, Number(amount) || 0))

  async function submit() {
    if (amt <= 0) {
      err = 'Enter an amount greater than $0.'
      return
    }
    busy = true
    err = ''
    try {
      // face_usd is the source of truth; unit='face' means amount IS the dollars.
      await api.rollTxns.create({
        date: date || today(),
        bank: bank.trim(),
        action: 'return',
        denom,
        unit: 'face',
        amount: amt,
        face_usd: amt,
        notes: notes.trim(),
      })
      onChanged() // recompute the report → outstanding drops by this return
      done = { amount: amt }
    } catch (e) {
      err = (e as Error).message
    } finally {
      busy = false
    }
  }

  function again() {
    done = null
    amount = outstanding // the freshly-recomputed remainder
    notes = ''
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
      <Landmark class="size-5" />
    </span>
    <div>
      <h2 class="text-lg font-bold leading-tight">Return culls to the bank</h2>
      <p class="text-xs text-muted-foreground">Record a redeposit of searched coin back to a bank.</p>
    </div>
  </div>

  {#if done}
    <!-- success: show what was recorded + the new remaining float -->
    <Card class="space-y-3 p-5">
      <div class="flex items-start gap-2 text-positive">
        <Check class="mt-0.5 size-5 shrink-0" />
        <div>
          <p class="font-semibold text-foreground">Recorded a {money(done.amount)} return.</p>
          <p class="text-sm text-muted-foreground">
            {#if outstanding > 0}
              Still outstanding: <b class="text-warning">{money(outstanding)}</b> in culls awaiting a
              bank run.
            {:else}
              <b class="text-positive">All cashed in</b> — nothing left to redeposit. 🎉
            {/if}
          </p>
        </div>
      </div>
      <div class="flex gap-2">
        {#if outstanding > 0}
          <Button variant="secondary" onclick={again}>Return more</Button>
        {/if}
        <Button onclick={onClose}>Done</Button>
      </div>
    </Card>
  {:else}
    <!-- the outstanding-float headline (mirrors the Dashboard reconciliation banner) -->
    {#if outstanding > 0}
      <div
        class="flex items-start gap-2 rounded-lg border border-warning/30 bg-warning/10 px-4 py-3 text-sm text-warning"
      >
        <TriangleAlert class="mt-0.5 size-4 shrink-0" />
        <span>
          <b>{money(outstanding)} still to redeposit.</b> Bought {money(report.buys)} − returned
          {money(report.returns)} − kept {money(report.kept_face)}. Hand back what you've finished
          searching.
        </span>
      </div>
    {:else}
      <div
        class="flex items-start gap-2 rounded-lg border border-positive/30 bg-positive/10 px-4 py-3 text-sm text-positive"
      >
        <Check class="mt-0.5 size-4 shrink-0" />
        <span>
          <b>You're all cashed in.</b> Nothing is outstanding — but you can still record a return
          below if you're squaring up.
        </span>
      </div>
    {/if}

    <Card class="space-y-4 p-5">
      <div class="grid grid-cols-2 gap-3">
        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          Bank
          <input
            type="text"
            list="rtb-banks"
            placeholder="Stock Yards"
            bind:value={bank}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
          <datalist id="rtb-banks">
            {#each banks as b (b)}<option value={b}></option>{/each}
          </datalist>
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Amount returned $
          <input
            type="number"
            step="0.01"
            min="0"
            bind:value={amount}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
          {#if outstanding > 0 && amt !== outstanding}
            <button
              type="button"
              class="self-start text-[11px] text-primary underline-offset-2 hover:underline"
              onclick={() => (amount = outstanding)}
            >
              return the full {money(outstanding)}
            </button>
          {/if}
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Denomination (optional)
          <select
            bind:value={denom}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          >
            <option value="">Mixed / whole deposit</option>
            {#each DENOMS as d (d)}<option value={d}>{d}</option>{/each}
          </select>
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Date
          <input
            type="date"
            bind:value={date}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Notes (optional)
          <input
            type="text"
            placeholder="searched halves, returned culls"
            bind:value={notes}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
      </div>

      {#if err}<p class="text-sm text-destructive">{err}</p>{/if}

      <div class="flex justify-end gap-2">
        <Button variant="ghost" onclick={onClose}>Cancel</Button>
        <Button onclick={submit} disabled={busy || amt <= 0} class={cn(amt > 0 && 'tnum')}>
          {amt > 0 ? `Return ${money(amt)} to bank` : 'Return to bank'}
        </Button>
      </div>
    </Card>
  {/if}
</div>
