<script lang="ts">
  // A modal "are you sure?" for the destructive gestures — the app's own dialog
  // idiom (SettingsPanel / Sell / Merge: fixed inset-0, role="dialog", a card on a
  // dimmed backdrop), factored out so the answer to "is this recoverable?" is asked
  // in one place and worded the same way every time.
  //
  // Deliberately NOT window.confirm: a browser confirm cannot show the row's
  // identity in the app's own voice, is unstyled, and is un-testable.
  import type { Snippet } from 'svelte'
  import Button from '$lib/components/ui/Button.svelte'

  let {
    heading,
    confirmLabel = 'Delete',
    cancelLabel = 'Cancel',
    onConfirm,
    onCancel,
    children,
  }: {
    heading: string
    confirmLabel?: string
    cancelLabel?: string
    onConfirm: () => void
    onCancel: () => void
    /** The body — say WHICH thing is about to go, not just "are you sure". */
    children: Snippet
  } = $props()

  let card = $state<HTMLDivElement | null>(null)

  // Cancel takes focus on open, and is deliberately FIRST in the card's DOM order.
  // The dialog interrupts a grid you commit cells with by pressing Enter, so Enter
  // is the key most likely to already be under the user's finger: the safe control
  // is the one it lands on.
  $effect(() => {
    card?.querySelector('button')?.focus()
  })
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onCancel()} />

<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
  <!-- The backdrop is a <button>, not a div with a click handler: click-outside-to-
       dismiss then costs no accessibility warnings and needs no key handler of its
       own. Out of the tab order on purpose — Escape and Cancel are the keyboard paths. -->
  <button
    type="button"
    aria-label="Dismiss"
    tabindex="-1"
    class="absolute inset-0 cursor-default bg-black/40"
    onclick={onCancel}
  ></button>

  <div
    bind:this={card}
    role="dialog"
    aria-modal="true"
    aria-label={heading}
    class="relative w-full max-w-sm space-y-4 rounded-xl border bg-card p-5 shadow-lg"
  >
    <div class="space-y-1">
      <h3 class="text-lg font-semibold text-foreground">{heading}</h3>
      <div class="space-y-1 text-sm text-muted-foreground">{@render children()}</div>
    </div>
    <div class="flex justify-end gap-2">
      <Button variant="ghost" onclick={onCancel}>{cancelLabel}</Button>
      <Button variant="destructive" onclick={onConfirm}>{confirmLabel}</Button>
    </div>
  </div>
</div>
