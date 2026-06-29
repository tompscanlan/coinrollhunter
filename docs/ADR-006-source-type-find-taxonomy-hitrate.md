# ADR-006: Acquisition source-type, find taxonomy, trophy flag, and the hit-rate report

**Status:** Accepted
**Date:** 2026-06-29
**Deciders:** Tom (owner)
**Builds on:** ADR-001 (roll-flow, R7 normalized face), ADR-003 (catalog/specimen split,
`lots.roll_txn_id` find→buy link), ADR-005 (modeling distinct concepts explicitly).
**Motivated by:** a real hunter's 15-month dimes dataset captured in OKF
(`projects/coinrollhunter/crh-field-data-dimes.md` — r/CRH "Over a year of CRH data, Part 4").
That spreadsheet is a battle-tested view of which CRH dimensions and rollups actually matter;
this ADR adopts the load-bearing ones.

---

## Context

The field dataset segments **everything** by *acquisition format* — MR (machine-wrapped
rolls), CR (customer-wrapped rolls), Box (sealed bank box), Bag ($1k loose), Other/Loose —
and that single dimension is the strongest predictor of yield in the whole dataset: ~all the
silver came from CR, almost none from MR. The headline artifact the hunter publishes is a
**"1 per face $" hit-rate** table: for each find category, how many face dollars you must
search to find one, broken down by source. It's derived entirely from two inputs we already
store — finds (counts) and buys (face searched) — *except* we don't yet record (a) the
acquisition format of a buy, or (b) what *kind* of find each find is.

Three gaps, then:

1. **No acquisition-format dimension.** `roll_txns` has `unit` (box/roll/face/coin) but that's
   a *quantity* unit — "how much did you buy" — not *how it was wrapped*. They're orthogonal:
   you can buy "$250 of customer-wrapped rolls" (unit=face or roll, source=customer_roll) or
   "1 box" (unit=box, source=box). Without the format axis, the most informative rollup in the
   data is impossible.
2. **No find taxonomy.** A CRH find (`lots` with `activity='crh'`) carries product/metal/
   fineness but no rollup category (Proof / 90% silver / 2009 / error / PMD / …). So we can
   count finds but not produce the per-category hit-rate.
3. **No trophy distinction.** Notable finds ("19 Barbers in one $25 roll") are the shareable
   highlights, but they're indistinguishable from routine finds.

A fourth, cheap win: the dataset's summary KPIs (number of buys, distinct branches, average
buy) are one SQL aggregate away and worth surfacing.

The dataset also teaches a **methodology caveat** that shapes the report: hit-rate is a
*sampling* statistic with selection bias (the hunter stopped buying MR once CR proved better,
so MR's sample is tiny) and survivorship (trophies get sold or albumed and leave the live
inventory). A bare "1 per $X" point estimate is misleading at small N. So the report must
carry the **sample size** alongside the rate, and "lifetime finds" must be counted from the
find record, not from current holdings.

## Decision

### 1. Acquisition source-type on `roll_txns` (a new, orthogonal dimension)

Add `roll_txns.source_type TEXT`. Open vocabulary (no CHECK, matching `action`/`denom`/`unit`):

| value | meaning |
|---|---|
| `machine_roll` | machine/armored-carrier-wrapped rolls (MR) |
| `customer_roll` | hand/customer-wrapped rolls (CR) — the high-yield channel |
| `box` | sealed bank box of rolls |
| `bag` | $N bag of loose coin |
| `loose` | loose / pocket change / coin-machine reject trays (Other/Loose) |
| `''` | unknown / unspecified (default; back-compatible) |

This is **kept distinct from `unit`**, not folded into it (the ADR-005 principle: model
distinct concepts explicitly). `unit` answers "in what increment did you buy and how do we
normalize to face" (R7); `source_type` answers "what wrapping / yield class." A buy has both.

`bag` is also recognized as a `unit` value (it stops at box/roll/face/coin today); a dime bag
= $1,000 = 10k coins. Bag→face normalization is a UI concern (like box); the backend's
`face_usd` stays the R7 source of truth, so no calc change is required for the unit itself.

### 2. Find taxonomy on `lots` (denom-scoped, open vocabulary)

Add `lots.category TEXT` and `lots.subcategory TEXT`, meaningful for `activity='crh'` rows.
Open set (no CHECK) — the vocabulary is *documented*, not enforced, because it is **denom-
scoped** (a dime's categories ≠ a cent's) and will grow. Recommended dime vocabulary, lifted
from the field data:

```
Proof · Silver (sub: Mercury|Barber|Seated Liberty|Roosevelt 90%) · Other Silver
· Key Date (sub: 2009|1996-W|82 No-P) · Variety (sub: 69/70 Proof Reverse)
· AU+ · CAD · World · Error (sub: minor|major) · PMD (sub: Oreo|Slider|roller|parking lot|fire|bent|tooled)
```

The taxonomy is data, not code: `calc` groups by whatever `category`/`subcategory` strings are
present, so adding a denom or a bucket needs no engine change. (A future denom→vocabulary
registry can drive a dropdown in the UI; out of scope here.)

### 3. Trophy flag on `lots`

Add `lots.trophy INTEGER NOT NULL DEFAULT 0` (boolean). A normal editable column (CRUD/grid)
so a "greatest hits" feed is a filter, not manual curation.

### 4. The hit-rate report (`calc.FindsReport`) — with a confidence signal

A new pure-reporting view, derived from the resolved dataset (no new inputs):

- **Face searched** per `denom` and per `(denom, source_type)` = Σ `face_usd` of `action='buy'`
  roll txns. (Box throughput already proves we can roll up buys by denom.)
- **Find counts** per `(denom, category[, subcategory])` and per source — a find's denom and
  source come from its linked buy (`lots.roll_txn_id → roll_txns`). Finds with no link fall in
  an `unattributed` bucket (which is itself a useful "link your finds" nudge).
- **Hit rate** = `face_searched / count` ("1 per face $"). Higher = rarer; undefined when
  count = 0 (omit / NA, like the source).
- **Confidence:** every cell carries its **`count` (n)**; the report marks a cell
  `low_confidence` when `n` is below a small threshold (default 5). The UI greys/annotates thin
  cells. This is the dataset's central lesson made structural — never ship a point estimate
  without its N.

Counts come from the **find record**, including disposed finds (survivorship: a sold trophy
still *happened*), so "lifetime finds by category" is correct even after trophies leave live
inventory. (Live valuation continues to exclude disposed lots, unchanged.)

Exposed at `GET /api/finds-report`; the existing `GET /api/summary` is unchanged except for §5.

### 5. Summary KPIs

`calc.Report` gains `BuyCount`, `BranchCount`, `AvgBuyUSD` (over `action='buy'` txns:
count, distinct non-empty `bank`, mean `face_usd`). "Branch" granularity is whatever the
`bank` string carries today; a dedicated `branch` field is deferred (see below).

### Schema (migration 0006)

```sql
ALTER TABLE roll_txns ADD COLUMN source_type TEXT;
ALTER TABLE lots ADD COLUMN category    TEXT;
ALTER TABLE lots ADD COLUMN subcategory TEXT;
ALTER TABLE lots ADD COLUMN trophy      INTEGER NOT NULL DEFAULT 0;
```

All additive with safe defaults: existing rows read back as unknown/uncategorized/non-trophy,
so the importer, calc, and the worked-example tests are unaffected until data is entered.

## Alternatives considered

**A. Fold source-type into `unit` (one enum: box/roll/face/coin/MR/CR/bag/loose).** Rejected —
it conflates quantity with wrapping, so you could no longer say "$250 of customer rolls" (both
a quantity *and* a format). Two orthogonal facts deserve two columns (ADR-005 precedent).

**B. Store category/trophy in the existing `attributes` JSON escape hatch instead of columns.**
Rejected for the *primary* taxonomy: hit-rate grouping and the trophy filter are first-class
queries and grid-editable fields; burying them in JSON makes them awkward to aggregate and
edit. `attributes` remains the right home for the long tail (cert#, country, gemstone).

**C. A CHECK constraint / lookup table enforcing the category vocabulary.** Rejected now —
the vocabulary is denom-scoped and still evolving; a hard constraint would fight data entry
and force a migration per new bucket. Open strings + documented vocab matches how `action`/
`denom`/`unit`/`source_type` are already handled. A soft registry can come later for the UI.

**D. Compute the hit-rate in the UI from `/api/summary`.** Rejected — it's an accounting-shaped
aggregate (face-by-denom-by-source ÷ find-counts-by-category) that belongs in `calc` with the
rest of the math and its own tests, not re-derived in TypeScript.

## Consequences

- **+** The dataset's single most predictive dimension (source-type) and its headline artifact
  (the hit-rate report) become first-class, with sample-size honesty built in.
- **+** All additive/back-compatible: zero impact on existing data, importer, or the calc
  worked-example until categories/source-types are entered.
- **+** Finds linked to their buy (ADR-003) now pay off twice: per-box yield *and* per-source
  hit-rate, from the same link.
- **+** Trophy feed and KPIs are cheap filters/aggregates over data we now keep.
- **−** Four new columns + their CRUD/grid wiring, and a new endpoint + report type to test.
  Marginal cost given the generic `resource[T]` plumbing.
- **−** Per-source attribution depends on finds being **linked to a buy**; unlinked finds land
  in an `unattributed` bucket. Acceptable — it's visible and nudges good linking.

## Deferred (explicitly out of scope here)

- **`branch` as its own field** (vs. free-text `bank`): KPIs use `bank` for now.
- **`country` for World finds** and foreign-silver melt: use `attributes` until a column earns
  its place (field-data note §5).
- **Denom→vocabulary registry / dropdowns** and the **`--demo` seed** sized like the dataset
  (~$40k face / ~404k coins).
- **UI surfacing** of the report/KPIs/trophy feed/source-type entry — a follow-up pass; this
  ADR lands the data model, calc, and API.
