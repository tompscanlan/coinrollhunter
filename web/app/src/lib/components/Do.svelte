<script lang="ts">
  // The "Do" tab — a quick-action home built around the verbs of the hobby
  // ("what did you just do?") rather than raw tables. Each action is a focused
  // workflow over the same REST endpoints the Edit grids use; the grids stay as
  // the correction/backstop layer.
  import type { Report } from '$lib/types'
  import { money } from '$lib/format'
  import Card from '$lib/components/ui/Card.svelte'
  import ReturnToBank from './workflows/ReturnToBank.svelte'
  import BoughtABox from './workflows/BoughtABox.svelte'
  import LoggedFinds from './workflows/LoggedFinds.svelte'
  import NewBullion from './workflows/NewBullion.svelte'
  import SellHolding from './workflows/SellHolding.svelte'
  import Reconcile from './workflows/Reconcile.svelte'
  import { Boxes, Search, Landmark, Coins, HandCoins, Scale } from 'lucide-svelte'
  import { cn } from '$lib/utils'

  let { report, onChanged }: { report: Report; onChanged: () => void } = $props()

  type WorkflowId = 'buy' | 'finds' | 'return' | 'reconcile' | 'purchase' | 'sell'
  let active = $state<WorkflowId | null>(null)

  const outstanding = $derived(Math.max(0, report.to_redeposit))

  // Action tiles, ordered as the hunt loop runs then stack management. `hint` is
  // a live, data-aware nudge.
  type Action = {
    id: WorkflowId
    title: string
    sub: string
    icon: typeof Landmark
    enabled: boolean
    hint?: string
  }
  const actions = $derived<Action[]>([
    {
      id: 'buy',
      title: 'Bought a box / rolls',
      sub: 'Log coin picked up from a bank',
      icon: Boxes,
      enabled: true,
    },
    {
      id: 'finds',
      title: 'Logged finds',
      sub: 'Notable finds + keepers from a box',
      icon: Search,
      enabled: true,
    },
    {
      id: 'return',
      title: 'Returned to bank',
      sub: 'Redeposit searched culls',
      icon: Landmark,
      enabled: true,
      hint: outstanding > 0 ? `${money(outstanding)} outstanding` : 'all cashed in',
    },
    {
      id: 'reconcile',
      title: 'Reconcile / close out',
      sub: 'Square the float, write off shrinkage',
      icon: Scale,
      enabled: true,
      hint: outstanding > 0 ? `${money(outstanding)} to square` : 'books square',
    },
    {
      id: 'purchase',
      title: 'New coin / bullion',
      sub: 'A purchase for the stack',
      icon: Coins,
      enabled: true,
    },
    {
      id: 'sell',
      title: 'Sold something',
      sub: 'Record a sale + realized P&L',
      icon: HandCoins,
      enabled: true,
    },
  ])
</script>

{#if active === 'buy'}
  <BoughtABox {report} {onChanged} onClose={() => (active = null)} />
{:else if active === 'finds'}
  <LoggedFinds {report} {onChanged} onClose={() => (active = null)} />
{:else if active === 'return'}
  <ReturnToBank {report} {onChanged} onClose={() => (active = null)} />
{:else if active === 'reconcile'}
  <Reconcile {report} {onChanged} onClose={() => (active = null)} />
{:else if active === 'purchase'}
  <NewBullion {report} {onChanged} onClose={() => (active = null)} />
{:else if active === 'sell'}
  <SellHolding {report} {onChanged} onClose={() => (active = null)} />
{:else}
  <div class="space-y-4">
    <div class="text-center">
      <h2 class="text-lg font-semibold">What did you do?</h2>
      <p class="text-sm text-muted-foreground">
        Pick an action — it records the right rows for you. Need to fix something? Use the Edit tab.
      </p>
    </div>

    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
      {#each actions as a (a.id)}
        <button
          type="button"
          disabled={!a.enabled}
          onclick={() => a.enabled && (active = a.id)}
          class={cn(
            'group text-left',
            a.enabled ? 'cursor-pointer' : 'cursor-not-allowed',
          )}
        >
          <Card
            class={cn(
              'flex h-full items-start gap-3 p-4 transition-colors',
              a.enabled ? 'hover:border-primary/50 hover:bg-accent/40' : 'opacity-60',
            )}
          >
            <span
              class={cn(
                'flex size-10 shrink-0 items-center justify-center rounded-lg',
                a.enabled ? 'bg-primary/10 text-primary' : 'bg-muted text-muted-foreground',
              )}
            >
              <a.icon class="size-5" />
            </span>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="font-semibold text-foreground">{a.title}</span>
                {#if !a.enabled}
                  <span class="rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
                    soon
                  </span>
                {/if}
              </div>
              <p class="text-sm text-muted-foreground">{a.sub}</p>
              {#if a.enabled && a.hint}
                <p class="mt-1 text-xs font-medium text-primary tnum">{a.hint}</p>
              {/if}
            </div>
          </Card>
        </button>
      {/each}
    </div>
  </div>
{/if}
