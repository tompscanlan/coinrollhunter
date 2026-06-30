<script lang="ts">
  // "Bought a box / rolls" — the pick-up step of the hunt loop. Records a
  // roll_txn(action='buy'); face_usd is the source of truth (box throughput is
  // derived from it), so we auto-fill it from denom × unit (1 box of halves =
  // $500) while letting you override. Optionally logs the bank trip in the same
  // step (miles drive the gas cost). The Edit tab's roll-txns grid corrects one.
  import { onMount } from 'svelte'
  import type { Report, RollTxn, Trip } from '$lib/types'
  import { api } from '$lib/api'
  import { money, today } from '$lib/format'
  import { DENOMS, ROLL_UNITS, SOURCE_TYPES, faceFor } from '$lib/presets'
  import Card from '$lib/components/ui/Card.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { ArrowLeft, Check, Boxes } from 'lucide-svelte'
  import { cn } from '$lib/utils'

  let {
    report,
    onChanged,
    onClose,
  }: { report: Report; onChanged: () => void; onClose: () => void } = $props()

  // form state
  let bank = $state('')
  let date = $state(today())
  let denom = $state<string>('halves')
  let unit = $state<string>('box')
  let sourceType = $state<string>('') // how it was wrapped (ADR-006) — the yield-class axis
  let amount = $state(1)
  let face = $state(500)
  let manualFace = $state(false) // true once the user overrides the auto-fill
  let notes = $state('')
  let logTrip = $state(false)
  let miles = $state(0)
  let hours = $state(0)
  let banks = $state<string[]>([])

  let busy = $state(false)
  let err = $state('')
  let done = $state<{ face: number; denom: string; unit: string } | null>(null)

  const distinct = (xs: (string | undefined)[]) =>
    [...new Set(xs.map((s) => (s ?? '').trim()).filter(Boolean))].sort((a, b) => a.localeCompare(b))

  // face = denom × unit, unless the user has taken manual control.
  const autoFace = $derived(Math.round(faceFor(unit, denom, amount) * 100) / 100)
  $effect(() => {
    if (!manualFace) face = autoFace
  })

  onMount(async () => {
    try {
      const [rolls, trips] = await Promise.all([api.rollTxns.list(), api.trips.list()])
      banks = distinct([...rolls.map((r: RollTxn) => r.bank), ...trips.map((t: Trip) => t.bank)])
    } catch {
      /* suggestions optional */
    }
  })

  const faceAmt = $derived(Math.max(0, Number(face) || 0))

  async function submit() {
    if (faceAmt <= 0) {
      err = 'Face value must be greater than $0.'
      return
    }
    busy = true
    err = ''
    try {
      await api.rollTxns.create({
        date: date || today(),
        bank: bank.trim(),
        action: 'buy',
        denom,
        unit,
        source_type: sourceType,
        amount: Number(amount) || 0,
        face_usd: faceAmt,
        notes: notes.trim(),
      })
      if (logTrip && (Number(miles) > 0 || Number(hours) > 0)) {
        await api.trips.create({
          date: date || today(),
          bank: bank.trim(),
          miles: Number(miles) || 0,
          hours: Number(hours) || 0,
        })
      }
      onChanged()
      done = { face: faceAmt, denom, unit }
    } catch (e) {
      err = (e as Error).message
    } finally {
      busy = false
    }
  }

  function again() {
    done = null
    manualFace = false
    amount = 1
    notes = ''
    logTrip = false
    miles = 0
    hours = 0
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
      <Boxes class="size-5" />
    </span>
    <div>
      <h2 class="text-lg font-bold leading-tight">Bought a box / rolls</h2>
      <p class="text-xs text-muted-foreground">Log coin picked up from a bank to search.</p>
    </div>
  </div>

  {#if done}
    <Card class="space-y-3 p-5">
      <div class="flex items-start gap-2 text-positive">
        <Check class="mt-0.5 size-5 shrink-0" />
        <div>
          <p class="font-semibold text-foreground">
            Logged {money(done.face)} of {done.denom} ({done.unit}).
          </p>
          <p class="text-sm text-muted-foreground">
            Now {money(report.to_redeposit > 0 ? report.to_redeposit : 0)} is out to be searched +
            returned. Log your finds next, then return the culls.
          </p>
        </div>
      </div>
      <div class="flex gap-2">
        <Button variant="secondary" onclick={again}>Log another</Button>
        <Button onclick={onClose}>Done</Button>
      </div>
    </Card>
  {:else}
    <Card class="space-y-4 p-5">
      <div class="grid grid-cols-2 gap-3">
        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          Bank
          <input
            type="text"
            list="bab-banks"
            placeholder="Stock Yards"
            bind:value={bank}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
          <datalist id="bab-banks">
            {#each banks as b (b)}<option value={b}></option>{/each}
          </datalist>
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Denomination
          <select
            bind:value={denom}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          >
            {#each DENOMS as d (d)}<option value={d}>{d}</option>{/each}
          </select>
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Unit
          <select
            bind:value={unit}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          >
            {#each ROLL_UNITS as u (u)}<option value={u}>{u}</option>{/each}
          </select>
        </label>

        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          Source type <span class="text-muted-foreground/70">— how it was wrapped (drives yield)</span>
          <select
            bind:value={sourceType}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          >
            {#each SOURCE_TYPES as s (s.value)}<option value={s.value}>{s.label}</option>{/each}
          </select>
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          How many ({unit})
          <input
            type="number"
            step="0.1"
            min="0"
            bind:value={amount}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
        </label>

        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Face $
          <input
            type="number"
            step="0.01"
            min="0"
            bind:value={face}
            oninput={() => (manualFace = true)}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
          />
          {#if manualFace && faceAmt !== autoFace}
            <button
              type="button"
              class="self-start text-[11px] text-primary underline-offset-2 hover:underline"
              onclick={() => (manualFace = false)}
            >
              auto: {money(autoFace)}
            </button>
          {/if}
        </label>

        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          Date
          <input
            type="date"
            bind:value={date}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>

        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          Notes (optional)
          <input
            type="text"
            placeholder="first box from this branch"
            bind:value={notes}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
      </div>

      <!-- optional trip in the same step -->
      <label class="flex items-center gap-2 text-sm text-foreground">
        <input type="checkbox" bind:checked={logTrip} class="size-4 rounded border-input" />
        Also log the bank trip (gas + time)
      </label>
      {#if logTrip}
        <div class="grid grid-cols-2 gap-3">
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            Round-trip miles
            <input
              type="number"
              step="0.1"
              min="0"
              bind:value={miles}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            Hours
            <input
              type="number"
              step="0.25"
              min="0"
              bind:value={hours}
              class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none"
            />
          </label>
        </div>
      {/if}

      {#if err}<p class="text-sm text-destructive">{err}</p>{/if}

      <div class="flex justify-end gap-2">
        <Button variant="ghost" onclick={onClose}>Cancel</Button>
        <Button onclick={submit} disabled={busy || faceAmt <= 0} class={cn(faceAmt > 0 && 'tnum')}>
          {faceAmt > 0 ? `Log ${money(faceAmt)} bought` : 'Log purchase'}
        </Button>
      </div>
    </Card>
  {/if}
</div>
