<script lang="ts">
  import Card from '$lib/components/ui/Card.svelte'
  import { cn } from '$lib/utils'

  let {
    label,
    value,
    sub = '',
    tone = 'neutral',
  }: {
    label: string
    value: string
    sub?: string
    tone?: 'neutral' | 'positive' | 'negative' | 'warning'
  } = $props()

  const toneClass = {
    neutral: 'text-foreground',
    positive: 'text-positive',
    negative: 'text-negative',
    warning: 'text-warning',
  }
</script>

<Card class="p-4">
  <div class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{label}</div>
  <div class={cn('mt-1 text-2xl font-bold tnum', toneClass[tone])}>{value}</div>
  {#if sub}
    <div class="mt-0.5 text-xs text-muted-foreground">{sub}</div>
  {/if}
</Card>
