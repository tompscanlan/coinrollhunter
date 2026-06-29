# ADR-005: Reconcile / shrinkage — booking the unaccounted float as a loss

**Status:** Accepted
**Date:** 2026-06-29
**Deciders:** Tom (owner)
**Relates to:** ADR-001 (roll-flow / cash-in reconciliation), the workflow-first "Do"
tab (bead `om-tuu9`), and audit gap #10 (no period/session dimension).

---

## Context

The CRH float reconciliation (ADR-001 R7) computes

```
to_redeposit = bought − returned − kept(finds + clad)
```

and assumes **perfect accounting**: every dollar of face you bought is eventually
returned to a bank, kept as a silver find, or kept as a clad keeper. Reality is messier:

- the coin-counting machine miscounts,
- coins get lost between the search table and the bank,
- a deposit comes up short and nobody reconciles it to the penny.

So in practice `to_redeposit` sits **perpetually nonzero** — a few dollars of phantom
float. The dashboard then nags "still to redeposit" forever, and the number is often
*wrong*: that money isn't awaiting a bank run, it's **gone**.

The owner wants a **Reconcile / close-the-books** action: assert "everything I'm going to
return is returned, and here is my physical inventory (finds + clad keepers)" → the app
declares the remaining unaccounted face **lost**, books it as an **expense (shrinkage /
cost of doing business)**, drives the float to `$0`, and reduces CRH net accordingly.

The hard requirement: this must be **honest and auditable, not a silent fudge**. A loss
is a real accounting event with a date, an amount, and a reason — not a number the app
quietly subtracts.

### The "forgotten inventory" trap (owner's review note)

A naive reconcile would book the *entire* unaccounted float as a loss. But some of that
float may not be lost at all — it may be **clad keepers or silver finds the owner simply
hasn't recorded yet**, physically sitting in inventory. Booking those as shrinkage would
double-error: overstate the loss *and* understate inventory. So reconcile must not assume
"unaccounted == lost".

## Decision

### Data model: a dedicated `losses` table

**Add a dedicated `losses` table** (date / amount_usd / reason / scope), rather than
overloading `roll_txns` with a new `action='loss'`.

A loss row is folded into `calc` two ways:

1. **Float:** it counts toward the redeposit reconciliation, so
   `to_redeposit = bought − returned − kept − lost`. Booking the unaccounted face as a
   loss is what drives the float to `$0`.
2. **CRH net:** it is a real cash cost (you paid face for coins you can no longer recover),
   so it is subtracted from CRH net alongside gas + supplies. It is surfaced as its **own
   line** (`losses`), not buried inside `op_cost`, so the loss is visible and auditable.

The reconciliation banner reads:

```
Bought $B − returned $R − kept $K − lost $L = $0.00
```

### Workflow: split before you lose, and keep it correctable

The **Reconcile** tile guards against the forgotten-inventory trap (owner's choice,
2026-06-29):

1. It shows the current **unaccounted** amount (= `to_redeposit`).
2. It first lets the owner **record any keepers / finds they forgot** — those create
   real keeper/find rows and reduce the float *without* booking a loss.
3. **Only the genuine remainder** is booked as a `losses` row, driving the float to `$0`.

A loss is also **correctable**, because honesty cuts both ways: a `losses` row is a normal
record in the Edit layer (full CRUD). If coins later resurface, delete or reduce the loss
and add the keeper/find — the float reopens by exactly that amount.

### Schema (migration 0005)

```sql
CREATE TABLE losses (
  id         INTEGER PRIMARY KEY,
  date       TEXT NOT NULL,           -- ISO date the period was closed
  amount_usd REAL NOT NULL,           -- face declared lost (drives the float to 0)
  reason     TEXT,                    -- "machine miscount", "short deposit", ...
  scope      TEXT                     -- free-text period/session/bank tag (audit gap #10 seam)
);
```

## Alternatives considered

**A. New `roll_txns.action = 'loss'`.** Cheapest — no migration (the `action` column has
no CHECK constraint), reuses the existing model/CRUD/grid. Rejected because it **conflates
two different concepts**: a roll txn is *coin physically moving* (a buy adds float, a
return removes it because the coin went back to a bank). A loss is the *opposite* of a
return — the value is gone, not recovered — and it carries a reason/scope a coin movement
doesn't. Overloading the table would also pollute box-throughput math (which iterates
`action='buy'`) and the roll-txn grid with rows whose `denom`/`unit`/`amount` fields are
meaningless, and would muddy `returns` (today cleanly "money recovered"). ADR-003 set the
precedent of modeling distinct concepts explicitly rather than overloading.

**B. Silent fudge** (clamp `to_redeposit` to 0 when "close" enough). Rejected outright —
violates the honest/auditable requirement; the money really was spent.

## Consequences

- **+** Honest, auditable loss line with date/reason/scope; the float closes legitimately.
- **+** The split-first flow means real inventory isn't mis-booked as shrinkage, and a loss
  is reversible if coins resurface (delete/adjust the row).
- **+** `returns` keeps its clean meaning (coin handed back to a bank); `op_cost` keeps its
  meaning (gas + supplies). Losses is a separate, visible third cost line.
- **+** `scope` is the first seam toward per-period/per-session accounting (audit gap #10)
  and over-time reporting, without committing to a full period model yet.
- **−** One more table + CRUD + grid to maintain. Accepted: it's a thin table and the
  generic `resource[T]` wiring makes the cost marginal.
- **−** CRH net now has three cost inputs (gas, supplies, losses) instead of two — the
  worked-example test and dashboard are updated to match.

## Accounting note (why a loss reduces CRH net, with no double-count)

You paid `$face` for a box. You will only ever recover `returned + kept`. The unaccounted
remainder is cash you can't get back → a genuine cash loss, so it is subtracted from CRH
net. It does **not** double-count: `find_cost` (finds' face) and clad keepers are already
handled — finds via `− find_cost`, clad keepers as recoverable-at-face (they reduce only
the float, never CRH net). The loss is a *distinct* bucket of face that is neither
returned, found, nor kept.
