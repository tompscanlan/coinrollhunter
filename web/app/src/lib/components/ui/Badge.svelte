<script lang="ts" module>
  import { tv, type VariantProps } from 'tailwind-variants'

  export const badgeVariants = tv({
    base: 'inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-semibold transition-colors',
    variants: {
      variant: {
        default: 'border-transparent bg-primary text-primary-foreground',
        secondary: 'border-transparent bg-secondary text-secondary-foreground',
        positive: 'border-transparent bg-positive/15 text-positive',
        negative: 'border-transparent bg-negative/15 text-negative',
        warning: 'border-transparent bg-warning/15 text-warning',
        outline: 'text-foreground',
      },
    },
    defaultVariants: { variant: 'default' },
  })
  export type BadgeVariant = VariantProps<typeof badgeVariants>
</script>

<script lang="ts">
  import { cn } from '$lib/utils'
  import type { Snippet } from 'svelte'
  import type { HTMLAttributes } from 'svelte/elements'

  let {
    variant = 'default',
    class: className = '',
    children,
    ...rest
  }: HTMLAttributes<HTMLSpanElement> & {
    variant?: BadgeVariant['variant']
    children?: Snippet
  } = $props()
</script>

<span class={cn(badgeVariants({ variant }), className)} {...rest}>
  {@render children?.()}
</span>
