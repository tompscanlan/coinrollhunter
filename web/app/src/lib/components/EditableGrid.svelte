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
    /** Read-only computed display (e.g. a derived/joined column). */
    display?: (row: T) => string
    readOnly?: boolean
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
  import { Plus, Trash2, ArrowUpDown, ArrowUp, ArrowDown, DollarSign } from 'lucide-svelte'

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
    /** Bump to force a reload from the parent (e.g. after an out-of-grid sale). */
    reloadSignal?: number
  } = $props()

  let data = $state<T[]>([])
  let draft = $state<Draft>(untrack(() => blank()))
  let sorting = $state<SortingState>([])
  let error = $state('')
  let busy = $state(false)

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

  async function deleteRow(id: number) {
    try {
      busy = true
      await remove(id)
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
  const ACTIONS_COL_PX = 84
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

  <div class="overflow-x-auto rounded-xl border bg-card shadow-sm">
    <table class="table-fixed border-collapse text-sm tnum" style={`min-width:max(100%, ${tableMinWidth}px)`}>
      <thead>
        {#each view.headerGroups as hg (hg.id)}
          <tr class="border-b bg-muted/40">
            {#each hg.headers as header (header.id)}
              {@const col = header.column.columnDef as GridColumn<T>}
              {@const m = meta(col)}
              <th
                style={`width:${widthPx(m)}px`}
                class={cn(
                  'px-3 py-2 font-medium text-muted-foreground select-none',
                  align(m),
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

      <tbody>
        {#each view.rows as row (row.original.id)}
          <tr class="border-b last:border-0 hover:bg-accent/40">
            {#each columns as col (col.accessorKey)}
              {@const m = meta(col)}
              {@const key = col.accessorKey as keyof T}
              <td class={cn('px-1.5 py-1', align(m))}>
                {#if m.display}
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
                <Button
                  variant="ghost"
                  size="icon"
                  title="Delete row"
                  onclick={() => deleteRow(row.original.id)}
                >
                  <Trash2 class="size-4 text-muted-foreground hover:text-destructive" />
                </Button>
              </div>
            </td>
          </tr>
        {/each}

        <!-- new-row editor -->
        <tr class="border-t bg-muted/30">
          {#each columns as col (col.accessorKey)}
            {@const m = meta(col)}
            {@const key = col.accessorKey as keyof Draft}
            <td class={cn('px-1.5 py-1', align(m))}>
              {#if m.display || m.readOnly}
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
          <td class="px-1 text-center">
            <Button variant="ghost" size="icon" title="Add row" disabled={busy} onclick={addRow}>
              <Plus class="size-4 text-primary" />
            </Button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</section>
