<script lang="ts" module>
  import { tv, type VariantProps } from 'tailwind-variants'

  export const buttonVariants = tv({
    base: 'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 disabled:pointer-events-none disabled:opacity-50 cursor-pointer [&_svg]:size-4 [&_svg]:shrink-0',
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground hover:bg-primary/90',
        secondary: 'bg-secondary text-secondary-foreground hover:bg-secondary/80',
        outline: 'border border-input bg-card hover:bg-accent hover:text-accent-foreground',
        ghost: 'hover:bg-accent hover:text-accent-foreground',
        destructive: 'bg-destructive text-destructive-foreground hover:bg-destructive/90',
      },
      size: {
        default: 'h-9 px-4 py-2',
        sm: 'h-8 rounded-md px-3 text-xs',
        lg: 'h-10 rounded-md px-6',
        icon: 'h-8 w-8',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  })
  export type ButtonVariant = VariantProps<typeof buttonVariants>
</script>

<script lang="ts">
  import { cn } from '$lib/utils'
  import type { Snippet } from 'svelte'
  import type { HTMLButtonAttributes } from 'svelte/elements'

  let {
    variant = 'default',
    size = 'default',
    class: className = '',
    children,
    ...rest
  }: HTMLButtonAttributes & {
    variant?: ButtonVariant['variant']
    size?: ButtonVariant['size']
    children?: Snippet
  } = $props()
</script>

<button class={cn(buttonVariants({ variant, size }), className)} {...rest}>
  {@render children?.()}
</button>
