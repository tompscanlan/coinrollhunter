<script lang="ts">
  import type { Report, EnrichedLot } from '$lib/types'
  import { verdict } from '$lib/types'
  import { money, pct, oz, num } from '$lib/format'
  import Card from '$lib/components/ui/Card.svelte'
  import Badge from '$lib/components/ui/Badge.svelte'
  import StatCard from './StatCard.svelte'
  import { cn } from '$lib/utils'
  import { Check, TriangleAlert, RadioTower } from 'lucide-svelte'

  // Overview owns the *glance + ledger* altitude only (ADR-012): what you can
  // read without interpreting — is the hunt costing money, what's it worth, is
  // the float square. Analysis (yield-by-bank, hit-rate, trophies, composition,
  // stack-by-type) lives in Insights; the spot *editor* lives in Settings. This
  // view stays read-only.
  let { report }: { report: Report } = $props()

  const r = $derived(report)
  const bullion = $derived(r.lots.filter((l) => l.activity !== 'crh'))
  const finds = $derived(r.lots.filter((l) => l.activity === 'crh'))
  // Distribute the dealer haircut proportionally so per-row realizable sums to the total.
  const haircut = $derived(r.find_melt > 0 ? r.find_realizable / r.find_melt : 1)
  const realizable = (l: EnrichedLot) => l.market_usd * haircut

  const v = $derived(verdict(r))
  const crhTone = $derived(r.crh_net_real >= 0 ? 'positive' : 'negative')
  const boxList = $derived(
    Object.entries(r.boxes_by_denom)
      .map(([d, n]) => `${num(n)} ${d}`)
      .join(', ') || 'none logged',
  )
</script>

<div class="space-y-6">
  <!-- meta line + spot freshness chip (ADR-007: a background poller refreshes spot).
       Read-only here; the spot editor moved to Settings (ADR-012 §5). -->
  <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
    <p class="text-sm text-muted-foreground">
      As of <span class="font-medium text-foreground">{r.spot.as_of || '—'}</span>
      &nbsp;·&nbsp; Spot: Au {money(r.spot.gold_usd)} · Ag {money(r.spot.silver_usd)}{#if r.spot.platinum_usd}
        · Pt {money(r.spot.platinum_usd)}{/if}{#if r.spot.palladium_usd} · Pd {money(r.spot.palladium_usd)}{/if} / ozt
    </p>
    <span
      class="inline-flex items-center gap-1.5 rounded-full border bg-muted/40 px-2.5 py-0.5 text-xs text-muted-foreground"
      title="Spot prices refresh in the background while the app runs (ADR-007). Manual entry is the offline fallback in Settings."
    >
      <RadioTower class="size-3" />
      <span>Spot: {r.spot.source || 'none'}</span>
      <span class="text-muted-foreground/60">· {r.spot.as_of || 'no data'}</span>
    </span>
  </div>

  <!-- verdict — TWO numbers, side by side (om-nass). The live figure alone was
       answering an ALL-TIME question with a CURRENT-HOLDINGS number: sell a
       winning find and its value leaves crh_net_real while the costs that found
       it remain, flipping a profitable hunt negative. So the all-time question
       now sits over crh_net_lifetime (live + realized on sold finds), and the
       live figure keeps its own, honestly-labelled card. Each card's tone
       follows its own number. -->
  <div class="grid gap-3 md:grid-cols-2">
    <Card
      class={cn(
        'border-none p-6 text-white shadow-md',
        r.crh_net_real >= 0 ? 'bg-positive' : 'bg-negative',
      )}
    >
      <div class="text-xs font-semibold uppercase tracking-wide opacity-80">Current holdings</div>
      <div class="text-sm/relaxed opacity-90">What are the finds you still hold worth, net?</div>
      <div class="mt-1 text-2xl font-bold tnum">{v} &nbsp; {money(r.crh_net_real)}</div>
      <div class="mt-1.5 text-sm opacity-95">
        Near-free finds ({oz(r.find_oz)} oz) still in hand vs logged costs of {money(
          r.op_cost + r.losses,
        )}{#if r.losses > 0}
          (incl. {money(r.losses)} shrinkage){/if}. Bullion is a separate long-term hold.
      </div>
    </Card>

    <Card
      class={cn(
        'border-none p-6 text-white shadow-md',
        r.crh_net_lifetime >= 0 ? 'bg-positive' : 'bg-negative',
      )}
    >
      <div class="text-xs font-semibold uppercase tracking-wide opacity-80">Lifetime (incl. sales)</div>
      <div class="text-sm/relaxed opacity-90">Is coin roll hunting costing you money?</div>
      <div class="mt-1 text-2xl font-bold tnum">{money(r.crh_net_lifetime)}</div>
      <div class="mt-1.5 text-sm opacity-95">
        Live finds {money(r.crh_net_real)} + realized on sold finds {money(r.realized_gain_crh)}.
        {#if r.realized_gain_bullion !== 0}
          Bullion sales ({money(r.realized_gain_bullion)}) are counted separately.
        {/if}
      </div>
    </Card>
  </div>

  <!-- headline stats -->
  <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
    <StatCard label="Total invested" value={money(r.total_basis)} sub="cash basis" />
    <StatCard
      label="Liquidation value"
      value={money(r.total_market)}
      sub="est. realizable"
      tone={r.total_unreal >= 0 ? 'positive' : 'negative'}
    />
    <StatCard
      label="Net position"
      value={money(r.total_unreal)}
      sub={pct(r.total_basis ? (r.total_unreal / r.total_basis) * 100 : 0)}
      tone={r.total_unreal >= 0 ? 'positive' : 'negative'}
    />
    <StatCard label="CRH net (cash)" value={money(r.crh_net_real)} sub="finds minus costs" tone={crhTone} />
  </div>

  <!-- bullion -->
  <section class="space-y-2">
    <div class="flex items-center justify-between">
      <h2 class="text-lg font-semibold">Bullion investment</h2>
      <Badge variant={r.bullion_unreal >= 0 ? 'positive' : 'negative'}>
        {money(r.bullion_unreal)} ({pct(r.bullion_pct)})
      </Badge>
    </div>
    <Card class="overflow-x-auto">
      <table class="w-full text-sm tnum">
        <thead>
          <tr class="border-b bg-muted/40 text-left text-muted-foreground">
            <th class="px-3 py-2 font-medium">Holding</th>
            <th class="px-3 py-2 text-right font-medium">Fine oz</th>
            <th class="px-3 py-2 text-right font-medium">Basis</th>
            <th class="px-3 py-2 text-right font-medium">Market</th>
            <th class="px-3 py-2 text-right font-medium">Unrealized</th>
          </tr>
        </thead>
        <tbody>
          {#each bullion as l (l.id)}
            <tr class="border-b last:border-0">
              <td class="px-3 py-2">{l.product}</td>
              <td class="px-3 py-2 text-right">{oz(l.fine_oz)}</td>
              <td class="px-3 py-2 text-right">{money(l.basis_usd)}</td>
              <td class="px-3 py-2 text-right">{money(l.market_usd)}</td>
              <td class={cn('px-3 py-2 text-right', l.unreal_usd >= 0 ? 'text-positive' : 'text-negative')}>
                {money(l.unreal_usd)} ({pct(l.unreal_pct)})
              </td>
            </tr>
          {:else}
            <tr><td colspan="5" class="px-3 py-6 text-center text-muted-foreground">No bullion lots yet.</td></tr>
          {/each}
          {#if bullion.length}
            <tr class="border-t-2 bg-muted/30 font-semibold">
              <td class="px-3 py-2">Total</td>
              <td class="px-3 py-2 text-right">{oz(bullion.reduce((t, l) => t + l.fine_oz, 0))}</td>
              <td class="px-3 py-2 text-right">{money(r.bullion_basis)}</td>
              <td class="px-3 py-2 text-right">{money(r.bullion_market)}</td>
              <td class={cn('px-3 py-2 text-right', r.bullion_unreal >= 0 ? 'text-positive' : 'text-negative')}>
                {money(r.bullion_unreal)}
              </td>
            </tr>
          {/if}
        </tbody>
      </table>
    </Card>
  </section>

  <!-- CRH finds -->
  <section class="space-y-2">
    <div class="flex items-center justify-between">
      <h2 class="text-lg font-semibold">Coin roll hunting</h2>
      <Badge variant={r.crh_net_real >= 0 ? 'positive' : 'negative'}>net {money(r.crh_net_real)}</Badge>
    </div>
    <Card class="overflow-x-auto">
      <table class="w-full text-sm tnum">
        <thead>
          <tr class="border-b bg-muted/40 text-left text-muted-foreground">
            <th class="px-3 py-2 font-medium">Find</th>
            <th class="px-3 py-2 text-right font-medium">Fine oz</th>
            <th class="px-3 py-2 text-right font-medium">Cost (face)</th>
            <th class="px-3 py-2 text-right font-medium">Realizable</th>
          </tr>
        </thead>
        <tbody>
          {#each finds as l (l.id)}
            <tr class="border-b last:border-0">
              <td class="px-3 py-2">{l.product}</td>
              <td class="px-3 py-2 text-right">{oz(l.fine_oz)}</td>
              <td class="px-3 py-2 text-right">{money(l.basis_usd)}</td>
              <td class="px-3 py-2 text-right">{money(realizable(l))}</td>
            </tr>
          {:else}
            <tr><td colspan="4" class="px-3 py-6 text-center text-muted-foreground">No finds logged yet.</td></tr>
          {/each}
          {#if finds.length}
            <tr class="border-t-2 bg-muted/30 font-semibold">
              <td class="px-3 py-2">Total finds</td>
              <td class="px-3 py-2 text-right">{oz(r.find_oz)}</td>
              <td class="px-3 py-2 text-right">{money(r.find_cost)}</td>
              <td class="px-3 py-2 text-right">{money(r.find_realizable)}</td>
            </tr>
          {/if}
        </tbody>
      </table>
    </Card>

    <!-- reconciliation -->
    <div
      class={cn(
        'flex items-start gap-2 rounded-lg border px-4 py-3 text-sm',
        r.reconciled
          ? 'border-positive/30 bg-positive/10 text-positive'
          : 'border-warning/30 bg-warning/10 text-warning',
      )}
    >
      {#if r.reconciled}
        <Check class="mt-0.5 size-4 shrink-0" />
        <span>
          <b>All cashed in.</b> Bought {money(r.buys)} − returned {money(r.returns)} − kept {money(r.kept_face)}{#if r.losses > 0}
            − lost {money(r.losses)}{/if} = $0.00 outstanding.
        </span>
      {:else}
        <TriangleAlert class="mt-0.5 size-4 shrink-0" />
        <span>
          <b>{money(r.to_redeposit)} still to redeposit.</b> Bought {money(r.buys)} − returned
          {money(r.returns)} − kept {money(r.kept_face)}{#if r.losses > 0} − lost {money(r.losses)}{/if}. That's
          searched culls awaiting a bank run — or close the books with Reconcile.
        </span>
      {/if}
    </div>

    <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
      <StatCard label="Boxes searched" value={num(r.total_boxes)} sub={boxList} />
      <StatCard label="Finds realizable" value={money(r.find_realizable)} sub="after dealer haircut" tone="positive" />
      <StatCard label="Gas + supplies" value={money(r.op_cost)} sub="logged to date" />
      <StatCard
        label="To redeposit"
        value={money(r.to_redeposit)}
        sub={r.reconciled ? 'all cashed in' : 'awaiting bank run'}
        tone={r.reconciled ? 'positive' : 'warning'}
      />
    </div>

    <!-- hunt-activity KPIs (ADR-006): how much hunting, over buy txns -->
    <div class="grid grid-cols-3 gap-3">
      <StatCard label="Buys" value={num(r.buy_count)} sub="roll-txn purchases" />
      <StatCard label="Branches" value={num(r.branch_count)} sub="distinct banks" />
      <StatCard label="Avg buy" value={money(r.avg_buy_usd)} sub="mean face / buy" />
    </div>
  </section>

  <!-- realized (sold) -->
  {#if r.realized?.length}
    <section class="space-y-2">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">Realized (sold)</h2>
        <Badge variant={r.realized_gain >= 0 ? 'positive' : 'negative'}>gain {money(r.realized_gain)}</Badge>
      </div>
      <Card class="overflow-x-auto">
        <table class="w-full text-sm tnum">
          <thead>
            <tr class="border-b bg-muted/40 text-left text-muted-foreground">
              <th class="px-3 py-2 font-medium">Sold</th>
              <th class="px-3 py-2 text-right font-medium">Qty</th>
              <th class="px-3 py-2 text-right font-medium">Basis</th>
              <th class="px-3 py-2 text-right font-medium">Proceeds</th>
              <th class="px-3 py-2 text-right font-medium">Gain</th>
              <th class="px-3 py-2 text-right font-medium">Date</th>
            </tr>
          </thead>
          <tbody>
            {#each r.realized as l (l.id)}
              <tr class="border-b last:border-0">
                <td class="px-3 py-2">{l.product}{l.activity === 'crh' ? ' (find)' : ''}</td>
                <td class="px-3 py-2 text-right">{num(l.qty)}</td>
                <td class="px-3 py-2 text-right">{money(l.basis_usd)}</td>
                <td class="px-3 py-2 text-right">{money(l.proceeds_usd)}</td>
                <td class={cn('px-3 py-2 text-right', l.gain_usd >= 0 ? 'text-positive' : 'text-negative')}>
                  {money(l.gain_usd)}
                </td>
                <td class="px-3 py-2 text-right text-muted-foreground">{l.disposed}</td>
              </tr>
            {/each}
            <tr class="border-t-2 bg-muted/30 font-semibold">
              <td class="px-3 py-2">Total realized</td>
              <td class="px-3 py-2"></td>
              <td class="px-3 py-2 text-right">{money(r.realized_basis)}</td>
              <td class="px-3 py-2 text-right">{money(r.realized_proceeds)}</td>
              <td class={cn('px-3 py-2 text-right', r.realized_gain >= 0 ? 'text-positive' : 'text-negative')}>
                {money(r.realized_gain)}
              </td>
              <td class="px-3 py-2"></td>
            </tr>
          </tbody>
        </table>
      </Card>
    </section>
  {/if}
</div>
