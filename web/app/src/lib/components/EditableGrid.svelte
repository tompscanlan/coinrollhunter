<script lang="ts" module>
  import type { ColumnDef } from '@tanstack/table-core'

  /** Per-column editing metadata, carried on ColumnDef.meta. */
  export interface EditMeta<T> {
    editor?: 'text' | 'number' | 'date' | 'select'
    options?: readonly string[]
    step?: number
    min?: number
    placeholder?: string
    align?: 'left' | 'right'
    width?: string
    /** Read-only computed display (e.g. a derived/joined column). */
    display?: (row: T) => string
    readOnly?: boolean
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
  import { Plus, Trash2, ArrowUpDown, ArrowUp, ArrowDown } from 'lucide-svelte'

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
    onStateChange: () => {}, // sorting is the only state we drive; no-op the rest
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    renderFallbackValue: undefined,
  })

  // setOptions inside the derived keeps the instance in lock-step with $state,
  // and reading data/sorting registers them as deps — order-safe.
  const view = $derived.by(() => {
    table.setOptions((prev) => ({ ...prev, data, state: { ...prev.state, sorting } }))
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

  <div class="overflow-x-auto rounded-xl border bg-card shadow-sm">
    <table class="w-full border-collapse text-sm tnum">
      <thead>
        {#each view.headerGroups as hg (hg.id)}
          <tr class="border-b bg-muted/40">
            {#each hg.headers as header (header.id)}
              {@const col = header.column.columnDef as GridColumn<T>}
              {@const m = meta(col)}
              <th
                style={m.width ? `width:${m.width}` : ''}
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
            <th class="w-10 px-2"></th>
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
                    {#each m.options ?? [] as opt (opt)}
                      <option value={opt}>{opt}</option>
                    {/each}
                  </select>
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
            <td class="px-1 text-center">
              <Button
                variant="ghost"
                size="icon"
                title="Delete row"
                onclick={() => deleteRow(row.original.id)}
              >
                <Trash2 class="size-4 text-muted-foreground hover:text-destructive" />
              </Button>
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
                  {#each m.options ?? [] as opt (opt)}
                    <option value={opt}>{opt}</option>
                  {/each}
                </select>
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
