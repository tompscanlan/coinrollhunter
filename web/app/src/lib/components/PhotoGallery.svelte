<script lang="ts" module>
  // Roles the picker suggests, scoped by owner kind (open vocabulary — you can type
  // anything, these are just nudges; the server does not enforce them, ADR-009/ADR-006).
  // 'receipt' is a first-class suggestion for BOTH kinds: a purchase receipt hangs off the
  // holding (lot) it documents just as naturally as off the box buy (roll_txn) — om-9o4n.1.
  const ROLE_SUGGESTIONS: Record<string, string[]> = {
    lot: ['obverse', 'reverse', 'detail', 'edge', 'slab-label', 'receipt'],
    roll_txn: ['box-end', 'receipt', 'detail'],
  }

  // prettyRole renders a role slug ('slab-label') as a human label ('Slab label') for the
  // upload-time <select>. The stored value stays the slug — this is display only.
  function prettyRole(r: string): string {
    const s = r.replace(/-/g, ' ')
    return s.charAt(0).toUpperCase() + s.slice(1)
  }
</script>

<script lang="ts">
  // The reusable photo gallery (om-6hlp): a big display view of the selected photo, a
  // thumbnail strip ordered (seq, uid), per-photo controls (role/caption/reorder/delete),
  // and an upload affordance. The originals are the source of truth; the <img> tags load
  // the regenerable thumb/display derivatives, and "open original" links the raw file.
  // Owner-generic (lot or roll_txn), though v1 only wires it for lots.
  import { api } from '$lib/api'
  import { isDocumentExt } from '$lib/photos'
  import type { Photo } from '$lib/types'
  import Button from '$lib/components/ui/Button.svelte'
  import ConfirmDialog from '$lib/components/ui/ConfirmDialog.svelte'
  import { Upload, Trash2, ArrowLeft, ArrowRight, ExternalLink, ImageOff, FileText } from 'lucide-svelte'

  let {
    ownerKind,
    ownerUid,
    onChanged,
  }: {
    ownerKind: 'lot' | 'roll_txn'
    ownerUid: string
    onChanged?: () => void
  } = $props()

  let photos = $state<Photo[]>([])
  let selectedUid = $state<string>('')
  let error = $state('')
  let busy = $state(false)
  let pendingDelete = $state<Photo | null>(null)
  let fileInput = $state<HTMLInputElement | null>(null)
  // The role the NEXT upload is tagged with (om-9o4n.1). Defaults to 'detail' (the server's
  // own default) so nothing changes for a plain coin photo; pick 'Receipt' to file a receipt
  // scan without the old upload-then-re-role two-step. Sticky across uploads, so several
  // receipts in a row stay one click each. Post-upload re-role (the free-text box) is unchanged.
  let uploadRole = $state('detail')

  const selected = $derived(photos.find((p) => p.uid === selectedUid) ?? photos[0] ?? null)
  const roleOptions = $derived(ROLE_SUGGESTIONS[ownerKind] ?? ROLE_SUGGESTIONS.lot)

  // A document attachment (PDF, om-9o4n.2) has no image derivative, so it renders as a
  // document card, never an <img>. Uses the shared predicate ($lib/photos) so the doc ext
  // set lives in ONE place across the app (this drawer + the TrophyFeed hero).
  const isDoc = (p: { ext: string }): boolean => isDocumentExt(p.ext)

  async function reload() {
    try {
      photos = await api.photos.list(ownerKind, ownerUid)
      if (!photos.some((p) => p.uid === selectedUid)) selectedUid = photos[0]?.uid ?? ''
      error = ''
    } catch (e) {
      error = (e as Error).message
    }
  }

  // Reload whenever the owner changes (the drawer reuses one instance across coins).
  $effect(() => {
    ownerUid // dep
    ownerKind
    reload()
  })

  async function onPick(e: Event) {
    const input = e.currentTarget as HTMLInputElement
    const file = input.files?.[0]
    if (!file) return
    busy = true
    error = ''
    try {
      // Tag the upload with the picked role (default 'detail') so a receipt is filed as one
      // at ingest; the user can still re-role from the strip after (om-9o4n.1).
      const p = await api.photos.upload(ownerKind, ownerUid, file, uploadRole)
      await reload()
      selectedUid = p.uid
      onChanged?.()
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
      input.value = '' // let the same file be re-picked after an error
    }
  }

  async function saveMeta(p: Photo, patch: Partial<Pick<Photo, 'role' | 'seq' | 'caption'>>) {
    busy = true
    error = ''
    try {
      await api.photos.update(p.id, patch)
      await reload()
      onChanged?.()
    } catch (e) {
      error = (e as Error).message
      await reload()
    } finally {
      busy = false
    }
  }

  // Reorder by swapping this photo's seq with its neighbour's, then persisting both. The
  // list is sorted (seq, uid), so swapping the seq values is what moves a photo one slot.
  async function move(p: Photo, dir: -1 | 1) {
    const i = photos.findIndex((x) => x.uid === p.uid)
    const j = i + dir
    if (j < 0 || j >= photos.length) return
    const other = photos[j]
    busy = true
    error = ''
    try {
      await api.photos.update(p.id, { seq: other.seq })
      await api.photos.update(other.id, { seq: p.seq })
      await reload()
      onChanged?.()
    } catch (e) {
      error = (e as Error).message
      await reload()
    } finally {
      busy = false
    }
  }

  async function confirmDelete() {
    const p = pendingDelete
    pendingDelete = null
    if (!p) return
    busy = true
    error = ''
    try {
      await api.photos.remove(p.id)
      await reload()
      onChanged?.()
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<div class="space-y-3">
  {#if error}
    <p class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</p>
  {/if}

  <!-- Big view of the selected photo (the display derivative), with a link to the original. -->
  {#if selected}
    <div class="space-y-2">
      <div class="flex items-center justify-center overflow-hidden rounded-lg border bg-muted/30">
        {#if isDoc(selected)}
          <!-- A document attachment (PDF) has no image derivative — render a document card
               with an open/download link, never an <img> that would show broken (om-9o4n.2). -->
          <a
            href={api.photos.fileUrl(selected.uid, 'original')}
            target="_blank"
            rel="noopener"
            class="flex max-h-[46vh] flex-col items-center justify-center gap-2 px-6 py-12 text-center"
          >
            <FileText class="size-16 text-muted-foreground" />
            <span class="text-sm font-medium text-foreground">{selected.caption || prettyRole(selected.role)}</span>
            <span class="text-xs uppercase tracking-wide text-muted-foreground">{selected.ext} document</span>
            <span class="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline">
              <ExternalLink class="size-3" /> Open / download
            </span>
          </a>
        {:else}
          <img
            src={api.photos.fileUrl(selected.uid, 'display')}
            alt={selected.caption || selected.role}
            class="max-h-[46vh] w-auto object-contain"
          />
        {/if}
      </div>
      <div class="flex flex-wrap items-center justify-between gap-2">
        <a
          href={api.photos.fileUrl(selected.uid, 'original')}
          target="_blank"
          rel="noopener"
          class="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
        >
          <ExternalLink class="size-3" /> Open the original
        </a>
        <Button variant="ghost" size="sm" onclick={() => (pendingDelete = selected)} disabled={busy}>
          <Trash2 class="size-4 text-muted-foreground" /> Delete
        </Button>
      </div>

      <!-- Per-photo metadata: role (open autocomplete) + caption. Re-roling/re-captioning
           never touches the file on disk. -->
      <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Role
          <input
            type="text"
            list="photo-roles"
            value={selected.role}
            onchange={(e) => saveMeta(selected, { role: (e.currentTarget as HTMLInputElement).value })}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          Caption
          <input
            type="text"
            value={selected.caption ?? ''}
            placeholder="e.g. doubled die close-up"
            onchange={(e) => saveMeta(selected, { caption: (e.currentTarget as HTMLInputElement).value })}
            class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
          />
        </label>
      </div>
      <datalist id="photo-roles">
        {#each roleOptions as r (r)}<option value={r}></option>{/each}
      </datalist>
    </div>
  {:else}
    <div class="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed bg-muted/20 py-10 text-center">
      <ImageOff class="size-6 text-muted-foreground" />
      <p class="text-sm text-muted-foreground">No photos yet. Add one below.</p>
    </div>
  {/if}

  <!-- Thumbnail strip (seq, uid order). Clicking selects; the arrows reorder. -->
  {#if photos.length}
    <div class="flex gap-2 overflow-x-auto pb-1">
      {#each photos as p (p.uid)}
        <div class="flex shrink-0 flex-col items-center gap-1">
          <button
            type="button"
            onclick={() => (selectedUid = p.uid)}
            class="overflow-hidden rounded-md border-2 transition-colors {p.uid === selected?.uid
              ? 'border-primary'
              : 'border-transparent hover:border-input'}"
            title={p.role}
          >
            {#if isDoc(p)}
              <!-- A doc has no thumbnail derivative — a generic document tile stands in for it. -->
              <span class="flex size-16 flex-col items-center justify-center gap-0.5 bg-muted/40 text-muted-foreground">
                <FileText class="size-6" />
                <span class="text-[9px] uppercase leading-none">{p.ext}</span>
              </span>
            {:else}
              <img src={api.photos.fileUrl(p.uid, 'thumb')} alt={p.role} class="size-16 object-cover" />
            {/if}
          </button>
          <div class="flex items-center gap-0.5">
            <Button variant="ghost" size="icon" title="Move earlier" disabled={busy} onclick={() => move(p, -1)}>
              <ArrowLeft class="size-3.5" />
            </Button>
            <Button variant="ghost" size="icon" title="Move later" disabled={busy} onclick={() => move(p, 1)}>
              <ArrowRight class="size-3.5" />
            </Button>
          </div>
        </div>
      {/each}
    </div>
  {/if}

  <!-- Upload affordance. A plain file input the browser wraps in multipart. The role picker
       tags the next upload (e.g. as a Receipt) at ingest — om-9o4n.1. -->
  <div>
    <input
      bind:this={fileInput}
      type="file"
      accept="image/jpeg,image/png,image/webp,application/pdf"
      class="hidden"
      onchange={onPick}
    />
    <div class="flex flex-wrap items-center gap-2">
      <label class="flex items-center gap-1.5 text-xs text-muted-foreground">
        Add as
        <select
          bind:value={uploadRole}
          disabled={busy}
          title="What kind of photo this is"
          class="rounded-md border border-input bg-card px-2 py-1.5 text-sm text-foreground focus:border-ring focus:outline-none"
        >
          {#each roleOptions as r (r)}<option value={r}>{prettyRole(r)}</option>{/each}
        </select>
      </label>
      <Button variant="secondary" onclick={() => fileInput?.click()} disabled={busy}>
        <Upload class="size-4" /> {busy ? 'Uploading…' : 'Add a photo'}
      </Button>
    </div>
    <p class="mt-1 text-xs text-muted-foreground">JPEG, PNG, WebP or PDF, up to 10 MB — a photo, scan, or PDF of a receipt works. The original is always kept; images also get a smaller copy for quick viewing.</p>
  </div>
</div>

{#if pendingDelete}
  <ConfirmDialog
    heading="Delete this photo?"
    confirmLabel="Delete"
    onCancel={() => (pendingDelete = null)}
    onConfirm={confirmDelete}
  >
    <p>
      The photo is moved to the trash — it stops showing here, but the original file is
      <b class="text-foreground">kept on disk</b> and still travels with a backup or export.
    </p>
  </ConfirmDialog>
{/if}
