<script lang="ts">
  // Settings editor (audit gap #8): the tunables that drive the math — buyback
  // factors, mileage, hourly rate, box face — were API-only before this. A
  // modal over GET/PUT /api/settings; saving recomputes the Overview.
  import { api } from '$lib/api'
  import type { Settings, Spot } from '$lib/types'
  import { DENOMS } from '$lib/presets'
  import { today } from '$lib/format'
  import Button from '$lib/components/ui/Button.svelte'

  let {
    onClose,
    onSaved,
    spot,
  }: {
    onClose: () => void
    /** Called after a successful save so the parent can recompute the summary. */
    onSaved?: () => void
    /** Current spot from the report, so the manual editor seeds with live values. */
    spot?: Spot | null
  } = $props()

  let cfg = $state<Settings | null>(null)
  let error = $state('')
  let busy = $state(false)

  $effect(() => {
    api
      .getSettings()
      .then((s) => (cfg = s))
      .catch((e) => (error = (e as Error).message))
  })

  // Manual spot editor (moved off Overview — ADR-012 §5: read views host no
  // editors). Its own update action, kept separate from Save settings so a
  // manual entry only overwrites the background-polled spot (ADR-007) when the
  // user deliberately updates it.
  let spotGold = $state(0)
  let spotSilver = $state(0)
  let spotPlat = $state(0)
  let spotPall = $state(0)
  let spotDate = $state('')
  let spotBusy = $state(false)
  let spotErr = $state('')
  let spotDone = $state(false)
  $effect(() => {
    spotGold = spot?.gold_usd ?? 0
    spotSilver = spot?.silver_usd ?? 0
    spotPlat = spot?.platinum_usd ?? 0
    spotPall = spot?.palladium_usd ?? 0
    spotDate = spot?.as_of || today()
  })

  async function saveSpot() {
    spotBusy = true
    spotErr = ''
    spotDone = false
    try {
      await api.putSpot({
        as_of: spotDate || today(),
        gold_usd: Number(spotGold) || 0,
        silver_usd: Number(spotSilver) || 0,
        platinum_usd: Number(spotPlat) || 0,
        palladium_usd: Number(spotPall) || 0,
        source: 'manual',
      })
      spotDone = true
      onSaved?.()
    } catch (e) {
      spotErr = (e as Error).message
    } finally {
      spotBusy = false
    }
  }

  async function save() {
    if (!cfg) return
    busy = true
    error = ''
    try {
      // Coerce number inputs (an emptied field binds as '' / null).
      await api.putSettings({
        ...cfg,
        hourly_rate_usd: Number(cfg.hourly_rate_usd) || 0,
        irs_mileage_rate_usd_per_mile: Number(cfg.irs_mileage_rate_usd_per_mile) || 0,
        silver_buyback_factor_40pct: Number(cfg.silver_buyback_factor_40pct) || 0,
        silver_buyback_factor_90pct: Number(cfg.silver_buyback_factor_90pct) || 0,
        box_face_usd: Object.fromEntries(
          Object.entries(cfg.box_face_usd).map(([k, v]) => [k, Number(v) || 0]),
        ),
      })
      onSaved?.()
      onClose()
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }

  const inputCls =
    'rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground tnum focus:border-ring focus:outline-none'
</script>

<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true">
  <div class="max-h-[90svh] w-full max-w-md space-y-5 overflow-y-auto rounded-xl border bg-card p-5 shadow-lg">
    <div>
      <h3 class="text-lg font-semibold text-foreground">Settings</h3>
      <p class="text-sm text-muted-foreground">
        The tunables behind the math. Saved to your local database; the Overview recomputes on save.
      </p>
    </div>

    {#if error}
      <p class="text-sm text-destructive">{error}</p>
    {/if}

    {#if cfg}
      <section class="space-y-2">
        <h4 class="text-sm font-medium text-foreground">Silver buyback factors</h4>
        <p class="text-xs text-muted-foreground">
          Fraction of melt a dealer actually pays for junk silver — drives "realizable" find value.
        </p>
        <div class="grid grid-cols-2 gap-3">
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            90% silver
            <input type="number" step="0.01" min="0" max="1" bind:value={cfg.silver_buyback_factor_90pct} class={inputCls} />
          </label>
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            40% &amp; 35% (war nickel)
            <input type="number" step="0.01" min="0" max="1" bind:value={cfg.silver_buyback_factor_40pct} class={inputCls} />
          </label>
        </div>
      </section>

      <section class="space-y-2">
        <h4 class="text-sm font-medium text-foreground">Hunt costs</h4>
        <div class="grid grid-cols-2 gap-3">
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            Mileage rate $/mile
            <input type="number" step="0.005" min="0" bind:value={cfg.irs_mileage_rate_usd_per_mile} class={inputCls} />
          </label>
          <label class="flex flex-col gap-1 text-xs text-muted-foreground">
            Hourly rate $ (if valuing time)
            <input
              type="number" step="1" min="0" bind:value={cfg.hourly_rate_usd}
              disabled={!cfg.value_time} class={inputCls + ' disabled:opacity-50'}
            />
          </label>
        </div>
        <label class="flex items-center gap-2 text-sm text-foreground">
          <input type="checkbox" class="size-4 rounded border-input" bind:checked={cfg.value_time} />
          Value my time (adds an hours-based line to CRH net)
        </label>
      </section>

      <section class="space-y-2">
        <h4 class="text-sm font-medium text-foreground">Face $ per bank box</h4>
        <p class="text-xs text-muted-foreground">
          Box throughput is derived from face ÷ these. Change only if your bank's boxes differ.
        </p>
        <div class="grid grid-cols-3 gap-3">
          {#each DENOMS as denom (denom)}
            <label class="flex flex-col gap-1 text-xs text-muted-foreground">
              {denom}
              <input type="number" step="1" min="0" bind:value={cfg.box_face_usd[denom]} class={inputCls} />
            </label>
          {/each}
        </div>
      </section>
    {:else if !error}
      <p class="text-sm text-muted-foreground">Loading…</p>
    {/if}

    <section class="space-y-2 border-t pt-4">
      <h4 class="text-sm font-medium text-foreground">Spot prices</h4>
      <p class="text-xs text-muted-foreground">
        A background feed refreshes these while the app runs (ADR-007). Manual entry is the offline
        fallback — updating here overwrites the current spot with your values.
      </p>
      <div class="grid grid-cols-2 gap-3">
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Gold $/ozt
          <input type="number" step="0.01" min="0" bind:value={spotGold} class={inputCls} />
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Silver $/ozt
          <input type="number" step="0.01" min="0" bind:value={spotSilver} class={inputCls} />
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Platinum $/ozt
          <input type="number" step="0.01" min="0" bind:value={spotPlat} class={inputCls} />
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Palladium $/ozt
          <input type="number" step="0.01" min="0" bind:value={spotPall} class={inputCls} />
        </label>
        <label class="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground">
          As of
          <input type="date" bind:value={spotDate} class={inputCls} />
        </label>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="secondary" onclick={saveSpot} disabled={spotBusy}>Update spot</Button>
        {#if spotErr}<span class="text-sm text-destructive">{spotErr}</span>
        {:else if spotDone}<span class="text-xs text-positive">Spot updated.</span>{/if}
      </div>
    </section>

    <div class="flex justify-end gap-2">
      <Button variant="ghost" onclick={onClose}>Cancel</Button>
      <Button onclick={save} disabled={busy || !cfg}>Save settings</Button>
    </div>
  </div>
</div>
