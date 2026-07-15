<script lang="ts" module>
  import type { ColumnDef } from '@tanstack/table-core'

  /** Per-column editing metadata, carried on ColumnDef.meta. */
  export interface EditMeta<T> {
    editor?: 'text' | 'number' | 'date' | 'select' | 'autocomplete' | 'checkbox'
    options?: readonly string[]
    /** For editor:'select' — dynamic value/label options (e.g. pick a box).
        Takes precedence over `options`; a function so it reflects loaded data. */
    optionsFn?: () => readonly (string | { value: string; label: string })[]
    step?: number
    min?: number
    placeholder?: string
    align?: 'left' | 'right'
    width?: string
    /** Freeze this column against the left edge during horizontal scroll, so a
        wide table never loses which row you are on. Only a contiguous run from
        the first column can pin — the sticky offset is the sum of the widths
        before it, which only holds if every earlier column is pinned too. */
    pin?: boolean
    /** Read-only computed display (e.g. a derived/joined column). */
    display?: (row: T) => string
    readOnly?: boolean
    /** Row-conditional applicability: return false to render an inert "—"
        instead of the editor (e.g. source-type only applies to 'buy' roll
        txns). Also consulted for the new-row draft. */
    enabled?: (row: Partial<T>) => boolean
    /** For editor:'autocomplete' — suggestion list (a function so it can reflect
        freshly-loaded data each render). Renders an HTML <datalist>. */
    suggestions?: () => readonly string[]
    /** Given the committed cell value, return sibling fields to merge into the
        row — e.g. picking a Product fills metal/fineness/fine-oz. Returns
        undefined for an unrecognized value (so novel entries aren't clobbered). */
    autofill?: (value: string) => Partial<T> | undefined
  }

  export type GridColumn<T> = ColumnDef<T> & {
    accessorKey?: keyof T & string
    meta?: EditMeta<T>
  }
</script>

<script lang="ts" generics="T extends { id: number }">
  import {
    createTable,
    getCoreRowModel,
    getSortedRowModel,
    type SortingState,
  } from '@tanstack/table-core'
  import { untrack } from 'svelte'
  import { cn } from '$lib/utils'
  import Button from '$lib/components/ui/Button.svelte'
  import ConfirmDialog from '$lib/components/ui/ConfirmDialog.svelte'
  import { Plus, Trash2, ArrowUpDown, ArrowUp, ArrowDown, DollarSign, Camera } from 'lucide-svelte'

  type Draft = Omit<T, 'id'>

  let {
    title,
    description = '',
    columns,
    load,
    create,
    update,
    remove,
    blank,
    onChanged,
    rowAction,
    rowActionTitle,
    rowAction2,
    rowActionTitle2,
    rowClass,
    rowLabel,
    reloadSignal,
  }: {
    title: string
    description?: string
    columns: GridColumn<T>[]
    load: () => Promise<T[]>
    create: (row: Draft) => Promise<number>
    update: (id: number, row: Draft) => Promise<void>
    remove: (id: number) => Promise<void>
    blank: () => Draft
    onChanged?: () => void
    /** Optional extra per-row button (e.g. Sell). */
    rowAction?: (row: T) => void
    rowActionTitle?: string
    /** A SECOND optional per-row button, rendered as a camera (e.g. open a lot's photos).
        Only the grids that opt in get it — keepers, deliberately, do not (om-6hlp). */
    rowAction2?: (row: T) => void
    rowActionTitle2?: string
    /** Row-conditional styling — classes applied to the <tr> (e.g. dim a lot you
        have already sold, so you cannot edit a completed sale without noticing). */
    rowClass?: (row: T) => string | undefined
    /** How this grid NAMES a row in the delete confirmation — "1964 Kennedy Half —
        qty 20", not "#17". A confirm that cannot say what it is about to destroy is
        a speed bump, not a safeguard: you cannot tell the row you meant from the row
        your cursor was actually on. Optional only so a new grid still compiles;
        `describe` falls back to the row's own visible values, never a bare id. */
    rowLabel?: (row: T) => string
    /** Bump to force a reload from the parent (e.g. after an out-of-grid sale). */
    reloadSignal?: number
  } = $props()

  let data = $state<T[]>([])
  let draft = $state<Draft>(untrack(() => blank()))
  let sorting = $state<SortingState>([])
  let error = $state('')
  let busy = $state(false)
  /** The row the trash can was clicked on, held pending an explicit confirm. */
  let pendingDelete = $state<T | null>(null)

  async function reload() {
    try {
      data = await load()
      error = ''
    } catch (e) {
      error = (e as Error).message
    }
  }
  $effect(() => {
    reloadSignal // re-run when the parent bumps it (e.g. after a sale)
    reload()
  })

  // Getter-based initial options avoid capturing stale reactive values at
  // construction; the $derived below keeps the instance in lock-step thereafter.
  const table = createTable<T>({
    get data() {
      return data
    },
    get columns() {
      return columns
    },
    state: {
      get sorting() {
        return sorting
      },
    },
    onSortingChange: (u) => (sorting = typeof u === 'function' ? u(sorting) : u),
    // Required by table-core's resolved options type. We drive state externally
    // (controlled `sorting` + seeded defaults via setOptions below), so internal
    // state pushes are a no-op — this just satisfies the type without changing behavior.
    onStateChange: () => {},
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    renderFallbackValue: undefined,
  })

  // table-core builds a full default state (columnPinning, columnSizing, …) at
  // construction; getHeaderGroups() reads columnPinning.left/right, so the state
  // we feed must carry those defaults — controlling only `sorting` leaves them
  // undefined and getHeaderGroups() throws "reading 'left'".
  const defaultState = table.initialState

  // setOptions inside the derived keeps the instance in lock-step with $state,
  // and reading data/sorting registers them as deps — order-safe.
  const view = $derived.by(() => {
    table.setOptions((prev) => ({
      ...prev,
      data,
      state: { ...defaultState, ...prev.state, sorting },
    }))
    return { rows: table.getRowModel().rows, headerGroups: table.getHeaderGroups() }
  })

  function strip(row: T): Draft {
    const { id: _id, ...rest } = row as T & { id: number }
    return rest as Draft
  }

  async function saveRow(row: T) {
    try {
      busy = true
      await update(row.id, strip(row))
      error = ''
      onChanged?.()
    } catch (e) {
      error = (e as Error).message
      await reload()
    } finally {
      busy = false
    }
  }

  async function addRow() {
    try {
      busy = true
      await create(draft)
      draft = blank()
      await reload()
      error = ''
      onChanged?.()
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }

  /** Name a row for the confirmation. Prefers the grid's own `rowLabel`; failing
      that, describes the row from the first few values it is actually showing, so
      the dialog never degrades to "delete #17" — an id names nothing a user can
      recognize, which is exactly the check a misclick needs to fail. */
  function describe(row: T): string {
    const custom = rowLabel?.(row)?.trim()
    if (custom) return custom
    const parts = columns
      .map((c) => (c.accessorKey ? row[c.accessorKey] : undefined))
      .filter((v) => v !== undefined && v !== null && String(v).trim() !== '')
      .slice(0, 3)
      .map(String)
    return parts.length ? parts.join(' · ') : `${title} row #${row.id}`
  }

  // Deletion is a hard DELETE (store/crud.go): no soft-delete column, no history,
  // no server-side trash. The only way back from a misclick is restoring a backup
  // and losing everything since — so the trash can ARMS this, and only an explicit
  // confirm below fires it. This is the sole call site of `remove`, and `remove` is
  // a required prop, so every grid in the app is covered by this one guard.
  async function confirmDelete() {
    const row = pendingDelete
    if (!row) return
    pendingDelete = null
    try {
      busy = true
      await remove(row.id)
      await reload()
      error = ''
      onChanged?.()
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }

  function meta(col: GridColumn<T>): EditMeta<T> {
    return (col.meta ?? {}) as EditMeta<T>
  }
  const align = (m: EditMeta<T>) => (m.align === 'right' ? 'text-right' : 'text-left')

  // Column sizing. The table uses fixed layout so each column honors its width
  // instead of being squeezed to fit the container; the wrapper then scrolls
  // horizontally when the columns total more than the viewport. Columns without
  // an explicit width fall back to a sensible default (e.g. free-text Source,
  // Notes, Bank), and the table's min-width is the sum so nothing collapses.
  const DEFAULT_COL_PX = 170
  // Wide enough for up to three per-row buttons (sell + camera + delete on Holdings).
  const ACTIONS_COL_PX = 120
  function widthPx(m: EditMeta<T>): number {
    const w = m.width
    if (w && w.endsWith('px')) {
      const n = parseFloat(w)
      if (!Number.isNaN(n)) return n
    }
    return DEFAULT_COL_PX
  }
  const tableMinWidth = $derived(
    columns.reduce((sum, c) => sum + widthPx(meta(c)), 0) + ACTIONS_COL_PX,
  )

  // Left offset of each pinned column: the widths of the pinned columns before
  // it. Only a contiguous leading run pins (see EditMeta.pin) — the first
  // unpinned column ends the run, so a later `pin` is ignored rather than
  // silently landing at the wrong offset.
  const pinnedLeft = $derived.by(() => {
    const offsets = new Map<string, number>()
    let x = 0
    for (const col of columns) {
      if (!meta(col).pin) break
      offsets.set(col.accessorKey as string, x)
      x += widthPx(meta(col))
    }
    return offsets
  })
  const pinOf = (col: GridColumn<T>) => pinnedLeft.get(col.accessorKey as string)

  // --- row virtualization -------------------------------------------------
  // The grid renders an editor per cell, so a full render is not rows-worth of
  // DOM but rows x columns worth of live form controls (522 lots => ~7,800
  // inputs, ~16s to first paint). Below the threshold we render everything and
  // behave exactly as before; above it we render only the visible window plus
  // overscan, padding the scroll height with spacer rows so the scrollbar still
  // reflects the whole table.
  const VIRTUALIZE_ABOVE = 60
  const OVERSCAN = 10
  const EST_ROW_PX = 39

  let viewportEl = $state<HTMLDivElement | null>(null)
  let scrollTop = $state(0)
  let viewportPx = $state(0)
  let rowPx = $state(EST_ROW_PX)

  const virtual = $derived(view.rows.length > VIRTUALIZE_ABOVE)

  function windowAt(top: number, total: number) {
    const h = rowPx || EST_ROW_PX
    const start = Math.max(0, Math.floor(top / h) - OVERSCAN)
    const visible = Math.ceil((viewportPx || 600) / h)
    const end = Math.min(total, start + visible + OVERSCAN * 2)
    return { start, end }
  }

  const slice = $derived.by(() => {
    const total = view.rows.length
    if (!virtual) return { start: 0, end: total, padTop: 0, padBottom: 0 }
    const { start, end } = windowAt(scrollTop, total)
    const h = rowPx || EST_ROW_PX
    return { start, end, padTop: start * h, padBottom: (total - end) * h }
  })

  // A cell commits on `change`, which fires on blur. Virtualization unmounts
  // rows that scroll out of the window — so an edit still focused when its row
  // leaves would be destroyed before it ever committed, which is precisely the
  // silent grid data loss v0.3.0 just fixed. Blur it on the way out: `change`
  // fires, the row saves, and only then is it safe to unmount.
  function commitIfLeaving(nextTop: number) {
    const el = document.activeElement as HTMLElement | null
    if (!el || !viewportEl?.contains(el)) return
    if (el.tagName !== 'INPUT' && el.tagName !== 'SELECT') return
    const idx = Number((el.closest('tr') as HTMLElement | null)?.dataset.rowIndex)
    if (Number.isNaN(idx)) return // the new-row editor is sticky, never unmounted
    const { start, end } = windowAt(nextTop, view.rows.length)
    if (idx < start || idx >= end) el.blur()
  }

  function onScroll(e: Event & { currentTarget: HTMLDivElement }) {
    const top = e.currentTarget.scrollTop
    if (virtual) commitIfLeaving(top)
    scrollTop = top
    viewportPx = e.currentTarget.clientHeight
  }

  // Measure a real row once one exists; the estimate only has to carry the first
  // paint. Guarded against feeding back into its own render loop.
  let tbodyEl = $state<HTMLTableSectionElement | null>(null)
  $effect(() => {
    slice.start // re-measure after the window moves (fonts/zoom can change it)
    const tr = tbodyEl?.querySelector('tr[data-row-index]') as HTMLElement | null
    const h = tr?.offsetHeight ?? 0
    if (h > 0 && Math.abs(h - untrack(() => rowPx)) > 1) rowPx = h
  })

  // The viewport's own height drives how many rows a window holds; without it
  // the first paint would fall back to the 600px guess and under-render.
  $effect(() => {
    if (viewportEl) viewportPx = viewportEl.clientHeight
  })

  // Normalize select options to {value,label}; optionsFn (dynamic) wins over the
  // static string list, so existing string-option selects keep working.
  function selectOptions(m: EditMeta<T>): { value: string; label: string }[] {
    const raw = m.optionsFn ? m.optionsFn() : (m.options ?? [])
    return raw.map((o) => (typeof o === 'string' ? { value: o, label: o } : o))
  }

  // datalist ids must be unique on the page; scope by grid title + column key.
  const dlId = (key: PropertyKey) =>
    `dl-${title.toLowerCase().replace(/[^a-z0-9]+/g, '-')}-${String(key)}`

  // Merge sibling fields when an autocomplete value is recognized. Mutating the
  // $state proxy (draft or a data[] row) is deeply reactive, so dependent inputs
  // update in place.
  function applyAutofill(m: EditMeta<T>, target: Record<string, unknown>, value: unknown) {
    const patch = m.autofill?.(String(value ?? ''))
    if (patch) Object.assign(target, patch)
  }
</script>

<section class="space-y-3">
  <div class="flex items-end justify-between gap-3">
    <div>
      <h2 class="text-lg font-semibold text-foreground">{title}</h2>
      {#if description}
        <p class="text-sm text-muted-foreground">{description}</p>
      {/if}
    </div>
    <span class="text-xs text-muted-foreground">{data.length} rows</span>
  </div>

  {#if error}
    <div
      class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
    >
      {error}
    </div>
  {/if}

  <!-- one shared datalist per autocomplete column (referenced by all row inputs) -->
  {#each columns as col (col.accessorKey)}
    {@const m = meta(col)}
    {#if m.editor === 'autocomplete'}
      <datalist id={dlId(col.accessorKey as PropertyKey)}>
        {#each m.suggestions?.() ?? [] as opt (opt)}<option value={opt}></option>{/each}
      </datalist>
    {/if}
  {/each}

  <!-- A height-capped scroll region, not a full-height one: the horizontal
       scrollbar belongs at the bottom of the VIEWPORT. Wrapping the whole table
       put it at the bottom of the content — 20,000px down on a real collection,
       so scrolling sideways meant scrolling to the end of the table and back. -->
  <div
    bind:this={viewportEl}
    onscroll={onScroll}
    class="max-h-[70vh] overflow-auto rounded-xl border bg-card shadow-sm"
  >
    <table class="table-fixed border-collapse text-sm tnum" style={`min-width:max(100%, ${tableMinWidth}px)`}>
      <thead class="sticky top-0 z-20">
        {#each view.headerGroups as hg (hg.id)}
          <tr class="border-b bg-muted">
            {#each hg.headers as header (header.id)}
              {@const col = header.column.columnDef as GridColumn<T>}
              {@const m = meta(col)}
              {@const left = pinOf(col)}
              <th
                style={left === undefined
                  ? `width:${widthPx(m)}px`
                  : `width:${widthPx(m)}px;left:${left}px`}
                class={cn(
                  'px-3 py-2 font-medium text-muted-foreground select-none bg-muted',
                  align(m),
                  left !== undefined && 'sticky z-30',
                  header.column.getCanSort() && 'cursor-pointer hover:text-foreground',
                )}
                onclick={header.column.getToggleSortingHandler()}
              >
                <span
                  class={cn('inline-flex items-center gap-1', m.align === 'right' && 'flex-row-reverse')}
                >
                  {col.header}
                  {#if header.column.getCanSort()}
                    {#if header.column.getIsSorted() === 'asc'}
                      <ArrowUp class="size-3" />
                    {:else if header.column.getIsSorted() === 'desc'}
                      <ArrowDown class="size-3" />
                    {:else}
                      <ArrowUpDown class="size-3 opacity-40" />
                    {/if}
                  {/if}
                </span>
              </th>
            {/each}
            <th class="px-2" style={`width:${ACTIONS_COL_PX}px`}></th>
          </tr>
        {/each}
      </thead>

      <tbody bind:this={tbodyEl}>
        <!-- Spacer rows stand in for the un-rendered window above and below, so
             the scrollbar still measures the whole table. -->
        {#if slice.padTop > 0}
          <tr aria-hidden="true" style={`height:${slice.padTop}px`}></tr>
        {/if}

        {#each view.rows.slice(slice.start, slice.end) as row, i (row.original.id)}
          {@const rowIndex = slice.start + i}
          <!-- The row owns its background and the frozen cells inherit it (below),
               so hover/sold styling reaches them without being restated. It must
               stay OPAQUE: a frozen cell scrolls over the other columns, and a
               translucent tint would let them show through it. -->
          <tr
            class={cn(
              'group border-b bg-card last:border-0 hover:bg-accent',
              rowClass?.(row.original),
            )}
            data-row-index={rowIndex}
          >
            {#each columns as col (col.accessorKey)}
              {@const m = meta(col)}
              {@const key = col.accessorKey as keyof T}
              {@const left = pinOf(col)}
              <td
                style={left === undefined ? undefined : `left:${left}px`}
                class={cn(
                  'px-1.5 py-1',
                  align(m),
                  // bg-inherit: take the row's own (opaque) background, so a frozen
                  // cell follows hover and row state without duplicating either.
                  left !== undefined && 'sticky z-10 bg-inherit',
                )}
              >
                {#if m.enabled && !m.enabled(row.original)}
                  <span class="block px-1.5 py-1 text-muted-foreground/50">—</span>
                {:else if m.display}
                  <span class={cn('block px-1.5 py-1', align(m))}>{m.display(row.original)}</span>
                {:else if m.readOnly}
                  <span class={cn('block px-1.5 py-1 text-muted-foreground', align(m))}>
                    {String(row.original[key] ?? '')}
                  </span>
                {:else if m.editor === 'select'}
                  <select
                    class="w-full rounded-md border border-transparent bg-transparent px-1.5 py-1 hover:border-input focus:border-ring focus:bg-card focus:outline-none"
                    bind:value={row.original[key]}
                    onchange={() => saveRow(row.original)}
                  >
                    {#each selectOptions(m) as opt (opt.value)}
                      <option value={opt.value}>{opt.label}</option>
                    {/each}
                  </select>
                {:else if m.editor === 'autocomplete'}
                  <input
                    type="text"
                    list={dlId(key as PropertyKey)}
                    placeholder={m.placeholder}
                    title={String(row.original[key] ?? '')}
                    class={cn(
                      'w-full rounded-md border border-transparent bg-transparent px-1.5 py-1 hover:border-input focus:border-ring focus:bg-card focus:outline-none',
                      align(m),
                    )}
                    bind:value={row.original[key]}
                    onchange={() => {
                      applyAutofill(m, row.original as Record<string, unknown>, row.original[key])
                      saveRow(row.original)
                    }}
                  />
                {:else if m.editor === 'checkbox'}
                  <div class="flex justify-center px-1.5 py-1">
                    <input
                      type="checkbox"
                      class="size-4 rounded border-input"
                      checked={Boolean(row.original[key])}
                      onchange={(e) => {
                        ;(row.original as Record<string, unknown>)[key as string] = e.currentTarget.checked
                        saveRow(row.original)
                      }}
                    />
                  </div>
                {:else}
                  <input
                    type={m.editor === 'number' ? 'number' : m.editor === 'date' ? 'date' : 'text'}
                    step={m.step}
                    min={m.min}
                    placeholder={m.placeholder}
                    class={cn(
                      'w-full rounded-md border border-transparent bg-transparent px-1.5 py-1 hover:border-input focus:border-ring focus:bg-card focus:outline-none',
                      align(m),
                    )}
                    bind:value={row.original[key]}
                    onchange={() => saveRow(row.original)}
                  />
                {/if}
              </td>
            {/each}
            <td class="px-1">
              <div class="flex items-center justify-center gap-0.5">
                {#if rowAction}
                  <Button
                    variant="ghost"
                    size="icon"
                    title={rowActionTitle ?? 'Action'}
                    onclick={() => rowAction?.(row.original)}
                  >
                    <DollarSign class="size-4 text-muted-foreground hover:text-primary" />
                  </Button>
                {/if}
                {#if rowAction2}
                  <Button
                    variant="ghost"
                    size="icon"
                    title={rowActionTitle2 ?? 'Photos'}
                    onclick={() => rowAction2?.(row.original)}
                  >
                    <Camera class="size-4 text-muted-foreground hover:text-primary" />
                  </Button>
                {/if}
                <!-- Arms the delete; confirmDelete() is what actually removes the row.
                     KEEP title="Delete row" — qa/do-tab.e2e.mjs selects existing rows
                     by it (it is how the suite tells a real row from the draft row). -->
                <Button
                  variant="ghost"
                  size="icon"
                  title="Delete row"
                  onclick={() => (pendingDelete = row.original)}
                >
                  <Trash2 class="size-4 text-muted-foreground hover:text-destructive" />
                </Button>
              </div>
            </td>
          </tr>
        {/each}

        {#if slice.padBottom > 0}
          <tr aria-hidden="true" style={`height:${slice.padBottom}px`}></tr>
        {/if}

        <!-- New-row editor, pinned to the bottom of the viewport. Left in flow it
             would sit below the last row — which the height cap now puts a whole
             collection's worth of scrolling away. -->
        <tr class="group sticky bottom-0 z-20 border-t bg-muted">
          {#each columns as col (col.accessorKey)}
            {@const m = meta(col)}
            {@const key = col.accessorKey as keyof Draft}
            {@const left = pinOf(col)}
            <td
              style={left === undefined ? undefined : `left:${left}px`}
              class={cn('px-1.5 py-1 bg-muted', align(m), left !== undefined && 'sticky z-10')}
            >
              {#if m.display || m.readOnly || (m.enabled && !m.enabled(draft as Partial<T>))}
                <span class="block px-1.5 py-1 text-muted-foreground">—</span>
              {:else if m.editor === 'select'}
                <select
                  class="w-full rounded-md border border-input bg-card px-1.5 py-1 focus:border-ring focus:outline-none"
                  bind:value={draft[key]}
                >
                  {#each selectOptions(m) as opt (opt.value)}
                    <option value={opt.value}>{opt.label}</option>
                  {/each}
                </select>
              {:else if m.editor === 'autocomplete'}
                <input
                  type="text"
                  list={dlId(key as PropertyKey)}
                  placeholder={m.placeholder ?? col.header?.toString()}
                  class={cn('w-full rounded-md border border-input bg-card px-1.5 py-1 focus:border-ring focus:outline-none', align(m))}
                  bind:value={draft[key]}
                  oninput={() => applyAutofill(m, draft as Record<string, unknown>, draft[key])}
                  onkeydown={(e) => e.key === 'Enter' && addRow()}
                />
              {:else if m.editor === 'checkbox'}
                <div class="flex justify-center px-1.5 py-1">
                  <input
                    type="checkbox"
                    class="size-4 rounded border-input"
                    checked={Boolean(draft[key])}
                    onchange={(e) => ((draft as Record<string, unknown>)[key as string] = e.currentTarget.checked)}
                  />
                </div>
              {:else}
                <input
                  type={m.editor === 'number' ? 'number' : m.editor === 'date' ? 'date' : 'text'}
                  step={m.step}
                  min={m.min}
                  placeholder={m.placeholder ?? col.header?.toString()}
                  class={cn('w-full rounded-md border border-input bg-card px-1.5 py-1 focus:border-ring focus:outline-none', align(m))}
                  bind:value={draft[key]}
                  onkeydown={(e) => e.key === 'Enter' && addRow()}
                />
              {/if}
            </td>
          {/each}
          <td class="bg-muted px-1 text-center">
            <Button variant="ghost" size="icon" title="Add row" disabled={busy} onclick={addRow}>
              <Plus class="size-4 text-primary" />
            </Button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</section>

{#if pendingDelete}
  <ConfirmDialog
    heading="Delete this row?"
    confirmLabel="Delete"
    onCancel={() => (pendingDelete = null)}
    onConfirm={confirmDelete}
  >
    <p>
      <b class="text-foreground">{describe(pendingDelete)}</b>
      will be permanently removed from {title}.
    </p>
    <p>
      There is no undo and no trash: the only way back is restoring a backup, which costs you
      everything entered since.
    </p>
  </ConfirmDialog>
{/if}
