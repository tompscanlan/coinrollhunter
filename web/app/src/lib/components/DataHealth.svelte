<script lang="ts">
  // Data health — the PRIMARY door onto the read-only data scan (internal/doctor).
  // `coinrollhunter doctor` is the same scan for support threads and for the case
  // where the app cannot start; nothing here is CLI-only.
  //
  // The design constraint that shapes this whole panel: it must never become noise.
  // A ledger's health warning is read once, and if the first thing it shows is a
  // pile of things that turn out to be fine, it is never read again. So the three
  // classes stay SEPARATE with three different framings — wrong / broken / worth a
  // look — and there is no "fix it" button, deliberately: heuristic repair
  // false-positives on correct data, and a false positive here is silent,
  // unrecoverable money loss with no undo.
  import { api } from '$lib/api'
  import type { DoctorReport, DoctorClass, DoctorFinding } from '$lib/types'
  import { doctorHealthy } from '$lib/types'
  import Button from '$lib/components/ui/Button.svelte'
  import { CheckCircle2, AlertTriangle, HelpCircle, Unlink, XCircle } from 'lucide-svelte'

  let { onClose }: { onClose: () => void } = $props()

  let report = $state<DoctorReport | null>(null)
  let error = $state('')
  let loading = $state(true)

  $effect(() => {
    api
      .doctor()
      .then((r) => (report = r))
      .catch((e) => (error = (e as Error).message))
      .finally(() => (loading = false))
  })

  // Each class gets its own heading and its own tone. Merging them into one count
  // would be the single easiest way to make this panel useless: "12 problems" reads
  // as alarm when 9 of them are questions the user can reasonably answer "no" to.
  const groups: { class: DoctorClass; title: string; blurb: string }[] = [
    {
      class: 'invalid',
      title: 'These rows cannot be true',
      blurb:
        'A value here is impossible — a negative amount, an unknown category, a date that is not a date. Your totals are being added up from them anyway, so the numbers on Overview are off by whatever these rows contribute.',
    },
    {
      class: 'orphan',
      title: 'These point at something that was deleted',
      blurb:
        'The box or bank branch these were attached to is gone. Nothing is lost — the rows still count toward your money — but they read as though they were never attached to anything, so they drop out of per-box and per-bank views.',
    },
    {
      class: 'suspect',
      title: 'These are worth a look',
      blurb:
        'Nothing here is definitely wrong. Two rows disagree about something they normally agree on, which is often how a mis-typed date or a find on the wrong box shows up. If they are right, leave them.',
    },
  ]

  function findingsIn(cls: DoctorClass): DoctorFinding[] {
    return report?.findings.filter((f) => f.class === cls) ?? []
  }

  const scannedRows = $derived(
    report ? Object.values(report.scanned).reduce((a, b) => a + b, 0) : 0,
  )
  const scannedTables = $derived(report ? Object.keys(report.scanned).length : 0)

  /** Blank and space-padded values are findings too — show them, don't render nothing. */
  function showValue(v: string): string {
    return v === '' || v.trim() !== v ? JSON.stringify(v) : v
  }

  function where(f: DoctorFinding): string {
    return f.row_id === 0 ? f.table : `${f.table} #${f.row_id}`
  }
</script>

<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true">
  <div class="max-h-[90svh] w-full max-w-2xl space-y-5 overflow-y-auto rounded-xl border bg-card p-5 shadow-lg">
    <div>
      <h3 class="text-lg font-semibold text-foreground">Data health</h3>
      <p class="text-sm text-muted-foreground">
        A read-only check of your ledger. It never changes anything — every fix below is yours to
        make in the Edit tab.
      </p>
    </div>

    {#if error}
      <p class="text-sm text-destructive">Couldn't run the check: {error}</p>
    {:else if loading}
      <p class="text-sm text-muted-foreground">Checking…</p>
    {:else if report}
      <!-- Unreadable tables come FIRST and loudest. A scan that could not read a
           table has not cleared it, and showing a finding count above this would
           imply it had. -->
      {#if report.unreadable.length > 0}
        <section class="space-y-2 rounded-lg border border-destructive/40 bg-destructive/10 p-4">
          <h4 class="flex items-center gap-2 text-sm font-semibold text-destructive">
            <XCircle class="size-4 shrink-0" />
            {report.unreadable.length} table(s) could not be read at all
          </h4>
          <p class="text-xs text-destructive/90">
            The rows in these were not checked — this report says nothing about them. Usually one
            cell holds text where a number belongs, and everything in the app that reads that table
            is broken until it is fixed.
          </p>
          <ul class="space-y-1 text-xs">
            {#each report.unreadable as u (u.table)}
              <li><span class="font-medium">{u.table}</span> — {u.error}</li>
            {/each}
          </ul>
        </section>
      {/if}

      {#if doctorHealthy(report)}
        <div class="flex items-start gap-3 rounded-lg border bg-muted/30 p-4">
          <CheckCircle2 class="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-500" />
          <div>
            <p class="text-sm font-medium text-foreground">Nothing to fix</p>
            <p class="text-sm text-muted-foreground">
              Checked {scannedRows.toLocaleString()} rows across {scannedTables} tables.
            </p>
          </div>
        </div>
      {:else if report.findings.length > 0}
        <p class="text-sm text-muted-foreground">
          {report.findings.length} thing(s) to look at, out of {scannedRows.toLocaleString()} rows
          in {scannedTables} tables.
        </p>
      {/if}

      {#each groups as g (g.class)}
        {@const found = findingsIn(g.class)}
        {#if found.length > 0}
          <section class="space-y-2">
            <h4 class="flex items-center gap-2 text-sm font-semibold text-foreground">
              {#if g.class === 'invalid'}
                <AlertTriangle class="size-4 shrink-0 text-destructive" />
              {:else if g.class === 'orphan'}
                <Unlink class="size-4 shrink-0 text-amber-600 dark:text-amber-500" />
              {:else}
                <HelpCircle class="size-4 shrink-0 text-muted-foreground" />
              {/if}
              {g.title} ({found.length})
            </h4>
            <p class="text-xs text-muted-foreground">{g.blurb}</p>
            <ul class="space-y-2">
              {#each found as f (`${f.table}-${f.row_id}-${f.field}-${f.value}`)}
                <li class="rounded-lg border bg-muted/20 px-3 py-2 text-sm">
                  <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                    <span class="font-medium text-foreground">{where(f)}</span>
                    {#if f.label}<span class="text-muted-foreground">{f.label}</span>{/if}
                  </div>
                  {#if f.field}
                    <div class="mt-0.5 font-mono text-xs text-foreground">
                      {f.field} = {showValue(f.value)}
                    </div>
                  {/if}
                  <p class="mt-1 text-xs text-muted-foreground">{f.detail}</p>
                </li>
              {/each}
            </ul>
          </section>
        {/if}
      {/each}
    {/if}

    <div class="flex items-center justify-between gap-2 pt-1">
      <p class="text-xs text-muted-foreground">
        Same check as <code>coinrollhunter doctor</code>, which also works if the app won't start.
      </p>
      <Button variant="ghost" onclick={onClose}>Close</Button>
    </div>
  </div>
</div>
