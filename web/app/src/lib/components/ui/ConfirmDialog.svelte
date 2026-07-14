<script lang="ts">
  // A modal "are you sure?" for the destructive gestures — the app's own dialog
  // idiom (SettingsPanel / Sell / Merge: fixed inset-0, role="dialog", a card on a
  // dimmed backdrop), factored out so the answer to "is this recoverable?" is asked
  // in one place and worded the same way every time.
  //
  // Deliberately NOT window.confirm: a browser confirm cannot show the row's
  // identity in the app's own voice, is unstyled, and is un-testable.
  import type { Snippet } from 'svelte'
  import { onMount } from 'svelte'
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

  // The control the dialog opened from — the trash button — captured NOW, before we
  // move focus off it. Restored on teardown so the keyboard user lands back where they
  // were, not on <body>. Read at script-init (synchronous with the mount), while the
  // opener still holds focus.
  const opener = document.activeElement as HTMLElement | null

  /** The dialog's own focusable controls, in DOM order. */
  function focusables(): HTMLElement[] {
    if (!card) return []
    return [
      ...card.querySelectorAll<HTMLElement>(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
      ),
    ].filter((el) => !el.hasAttribute('disabled'))
  }

  onMount(() => {
    // Cancel takes focus on open, and is deliberately FIRST in the card's DOM order.
    // The dialog interrupts a grid you commit cells with by pressing Enter, so Enter is
    // the key most likely already under the user's finger: the safe control is the one
    // it lands on.
    focusables()[0]?.focus()
    // Every close path unmounts us (the parent nulls its state), so this one cleanup
    // covers Cancel / Escape / backdrop / a completed delete alike.
    return () => opener?.focus?.()
  })

  // A real focus trap — aria-modal is a promise the keyboard must keep. Without this,
  // Tab walks straight out the back of the dialog into the live grid: Shift+Tab could
  // land on ANOTHER row's trash button (re-arming the dialog on a different row) or on
  // a hidden grid input, where typing then fires saveRow on blur — a blind write behind
  // a "modal". So Tab and Shift+Tab wrap first↔last within the card, and nothing else
  // is reachable. Scoped to the card, not the window, so it only governs an open dialog.
  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      onCancel()
      return
    }
    if (e.key !== 'Tab') return
    const items = focusables()
    if (items.length === 0) return
    const first = items[0]
    const last = items[items.length - 1]
    const active = document.activeElement
    if (e.shiftKey) {
      if (active === first || !card?.contains(active)) {
        e.preventDefault()
        last.focus()
      }
    } else if (active === last || !card?.contains(active)) {
      e.preventDefault()
      first.focus()
    }
  }
</script>

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

  <!-- tabindex=-1: the dialog can receive programmatic focus but is never a Tab stop
       itself (and focusables() excludes it), so the trap cycles only the buttons. -->
  <div
    bind:this={card}
    role="dialog"
    aria-modal="true"
    aria-label={heading}
    tabindex="-1"
    onkeydown={onKeydown}
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
