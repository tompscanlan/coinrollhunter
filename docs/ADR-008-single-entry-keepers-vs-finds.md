# ADR-008: The single-entry rule — keepers vs. CRH finds (and sold finds' float)

**Status:** Accepted
**Date:** 2026-07-07
**Deciders:** Tom (owner)
**Builds on:** ADR-001 (roll-flow / cash-in reconciliation, R7 normalized face),
ADR-003 (catalog/specimen split, `lots.roll_txn_id` find→buy link), ADR-005
(reconcile / shrinkage — *"clad keepers reduce only the float, never CRH net"*),
ADR-006 (find taxonomy: `category`/`subcategory`/`trophy`).
**Closes:** audit gap #9 (keepers-vs-find-face double-count seam) — bead `om-co69`.

---

## Context

Two parallel "kept" concepts both reduce the redeposit float:

- the **keepers** table (clad parked at face → `clad_face`), and
- **CRH find lots'** face (`activity='crh'` basis → `find_cost`),

and `to_redeposit = bought − returned − kept(finds + clad) − lost` folds both in
(`calc.go`). The two tables are **unlinked** — `keepers` has no reference to a lot —
so nothing dedupes them. Three concrete gaps followed:

1. **Structural double-count, unguarded.** A coin logged once as a taxonomy find
   *and* again as a bulk keeper is counted twice on the kept side, understating
   `to_redeposit` (the dashboard reads *more* "recovered" than is physically true).
   This only mis-states the **float** — never CRH net, which never sees `clad_face`
   (ADR-005) — so a fix must not chase a phantom CRH-net symptom.
2. **The single-entry rule was undefined.** The UI presented a **metal-based** split
   ("Silver finds → lot / Clad keepers → keepers"), while the app's own reference
   dataset (`internal/demo/demo.go`) and ADR-006 model a **notability-based** split:
   individually-notable coins of *any* metal — including clad Proof/Error/PMD — are
   logged as `crh` lots with taxonomy; only bulk/uncategorized clad goes to keepers.
   A user following the UI's simpler framing had no way to know a clad coin already
   logged as a taxonomy find must not also go into keepers.
3. **Keepers weren't auditable.** No date/box, so a later reconcile pass couldn't
   tell whether a keeper batch had already been counted — a second, date-independent
   double-count vector.

Plus a related bug: a **sold (disposed) CRH find's face left the kept side entirely**.
`calc.Compute` summed find face over *live* lots only, so selling a find silently
dropped its face out of `kept_face` and reopened `to_redeposit` by that amount.

## Decision

### (a) The single-entry rule is NOTABILITY-BASED

Any **individually-notable** coin — silver **or** clad (Proof, Error, Key Date, PMD,
AU+, CAD, World, …) — belongs as a **CRH find lot** with ADR-006 taxonomy
(`category`/`subcategory`/`trophy`). **Only bulk / uncategorized clad** goes into the
**keepers** table. This matches `demo.go` + ADR-006 — the model the app already
invests in — and is the *opposite* of the old metal-based UI copy.

This is a **UI copy / guidance** change (`LoggedFinds.svelte`, `Reconcile.svelte`),
**not** a model or enum rewrite. Which table a create writes to is unchanged; the new
copy tells the user *keepers are for bulk/uncategorized clad only — if you already
logged this coin as a taxonomy find, don't add it here too.*

### (b) Keepers become auditable (migration 0007)

Add **nullable** `date TEXT` + `roll_txn_id INTEGER` to `keepers` (0007). A keeper
batch can now be attributed to a box/date like a lot (`roll_txn_id` logically
references `roll_txns.id`, mirroring `lots.roll_txn_id` from 0004; no FK — local
single-writer store). **Fully backward-compatible:** legacy keeper rows carry NULL
(empty date / zero `roll_txn_id`) and compute exactly as before — `clad_face` is
unaffected. This lets **Reconcile warn** when a keeper is added against a box/date
that already has `activity='crh'` find lots recorded ("already recorded this
session"), closing the double-count and the "can't audit against a period" gaps
together.

### (c) A sold (disposed) CRH find's face STAYS in `kept_face` permanently

`kept_face`'s find term sums find `basis_usd` across **both** the live set (`d.Lots`,
`IsFind()`) **and** the disposed set (`d.Disposed` where `Activity=="crh"`), counted
exactly once. Rationale: `to_redeposit` reconciles the **ORIGINAL find-time float**
(the dollars pulled off the search table), not live inventory — a later sale must not
retroactively reopen a float that was already reconciled.

**CRITICAL scope — CRH net + total basis MUST NOT MOVE.** The existing `fCost`
(`calc.go`, live finds only) also feeds `crh_net_melt`/`crh_net_real`/`crh_net_time`,
`total_basis` (`tBasis`), and the `FindCost`/`FindMelt`/`FindRealizable` report
fields. Per ADR-005 (*"keepers reduce only the float, never CRH net"*) and because a
disposed find's P&L is **already realized separately** as `proceeds − basis`, none of
those may absorb the disposed find's basis. So `fCost` is **not** redefined. A
**separate** kept-side term — `keptFindFace = fCost(live) + Σ basis over disposed crh
finds`, surfaced as the report field `disposed_find_face` — feeds `kept_face` **only**.
Every other consumer of `fCost` stays live-only and unchanged. This is a **float-only**
change, mirroring ADR-005's clad-keepers principle.

Identity: `KeptFace == CladFace + FindCost + DisposedFindFace` (holds for any dataset;
collapses to `CladFace + FindCost` when nothing is disposed).

## Consequences

- **+** One clear, durable single-entry rule (notability, not metal), stated in both
  workflow components, with a Reconcile double-count warning wired to a real read of
  existing crh finds for the chosen box.
- **+** Keepers are auditable/box-attributable; selling a find no longer silently
  reopens the reconciled float.
- **+** CRH net, total basis, per-box yield, and the Find\* report fields are provably
  unmoved (regression-pinned in `calc_test.go`): the disposed-find face is a strictly
  separate float-only term.
- **−** Two more nullable columns + a new report field. Marginal, additive, backward-
  compatible.

## Alternatives considered

- **Metal-based rule (option A):** silver→lot, clad→keeper, matching the old UI copy.
  Rejected — it discards ADR-006 taxonomy for clad trophies and contradicts the demo
  data the app already ships.
- **Widen the shared `fCost` to include disposed basis (the tempting one-liner).**
  Rejected — it would silently move CRH net *and* total basis (both consume `fCost`)
  and break the ADR-005 invariant. A separate term is mandatory.
- **A hard FK / dedupe between keepers and lots.** Rejected for now — the notability
  rule + the box/date audit dimension + the Reconcile warning address the double-count
  at the workflow level without a schema-level join a local single-writer store
  doesn't need.

## Update 2026-07-14 (om-5psc): a structural `kept` flag, and the dedupe stays rejected

The double-count seam this ADR mitigated with copy + a **non-blocking** Reconcile
warning is now closed **structurally**. Migration **0012** adds `lots.kept` (additive,
`DEFAULT 0`, the trophy pattern); a find you keep is **one flagged find row**, and
LoggedFinds no longer writes a keeper for a coin it just recorded as a find. One coin,
one row — the duplicate is **prevented at entry**, not detected after the fact. The
keeper table is now **clad-only by convention** (bulk/uncategorized clad); it is not
dropped, and a genuinely clad-only keeper — including a legacy NULL-date/NULL-box one —
round-trips untouched.

The flag is **math-neutral**: `internal/calc` is unchanged (zero diff). A CRH find's
face is on the kept side of the float via `fCost` whether or not it is flagged — the bug
was the second **row**, not the formula — so `kept` records **data-entry intent**, not
accounting. The identities above (`KeptFace == CladFace + FindCost + DisposedFindFace`;
`crh_net_*` / `total_basis` live-only) hold unchanged, and om-nass's verdicts are
provably unmoved (`TestKeptFindNoDoubleCount`: the double-count was float-only,
buggy-vs-correct delta 0).

**The §Alternatives rejection of a "hard FK / dedupe between keepers and lots" is
VINDICATED, not overturned.** This change deduplicates **nothing**: the migration is
additive and touches **zero** existing keeper rows. Automatic collapse of *existing*
duplicates was proven unsafe — a keeper is a **batch, not a coin** (its face is inside a
larger total, with no row-to-row pair to collapse); for the populations that matter (the
legacy import, the demo seeder, every pre-0007 row) the match key is **empty** (no date,
no box); and box+date co-location is the **signature of a correct entry**, so a detector
false-positives on good data. A false positive here is **silent, unrecoverable** money
loss (no undo — om-lv4q), against a false negative that is merely today's benign status
quo. So repairing pre-existing duplicates is a **user-adjudicated** step in the app (a
separate tandem bead, om-cqmp), never a migration or a boot-time action. The workflow
change stops every **future** duplicate; that is the whole forward value and it needs no
dedupe.
