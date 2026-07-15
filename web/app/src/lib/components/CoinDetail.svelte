<script lang="ts">
  // The per-coin DETAIL DRAWER (om-6hlp) — the net-new surface for looking at ONE lot
  // up close: its photos at display size, links to the originals, and the full gallery
  // (add / re-order / re-role / caption / trash). It reuses SettingsPanel's modal shell
  // (overlay + centered card + Escape/backdrop close) and delegates every photo affordance
  // to the shared PhotoGallery, so this file is mostly chrome + wiring.
  import PhotoGallery from '$lib/components/PhotoGallery.svelte'
  import Button from '$lib/components/ui/Button.svelte'
  import { X } from 'lucide-svelte'

  let {
    ownerUid,
    title,
    subtitle = '',
    onClose,
    onChanged,
  }: {
    /** The lot's stable uid — what photos hang off (owner_uid). */
    ownerUid: string
    title: string
    subtitle?: string
    onClose: () => void
    /** Called after a photo change, so the parent can refresh a thumbnail/feed. */
    onChanged?: () => void
  } = $props()

  // Escape closes, matching the app's other modals (Sell, Merge, Settings).
  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') onClose()
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- Backdrop closes on a click outside the card. -->
<div
  class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
  role="presentation"
  onclick={(e) => {
    if (e.target === e.currentTarget) onClose()
  }}
>
  <div class="max-h-[92svh] w-full max-w-2xl space-y-4 overflow-y-auto rounded-xl border bg-card p-5 shadow-lg" role="dialog" aria-modal="true" aria-label="Coin photos">
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0">
        <h3 class="truncate text-lg font-semibold text-foreground">{title || 'Photos'}</h3>
        {#if subtitle}<p class="truncate text-sm text-muted-foreground">{subtitle}</p>{/if}
      </div>
      <Button variant="ghost" size="icon" title="Close" onclick={onClose}>
        <X class="size-4" />
      </Button>
    </div>

    {#if ownerUid}
      <PhotoGallery ownerKind="lot" {ownerUid} {onChanged} />
    {:else}
      <p class="text-sm text-muted-foreground">
        This lot has no stable id yet — save the row once and reopen to add photos.
      </p>
    {/if}
  </div>
</div>
