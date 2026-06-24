# ADR-003: Catalog/specimen data model for holdings

**Status:** Accepted
**Date:** 2026-06-24
**Deciders:** Tom (owner)
**Supersedes:** the single flat `lots` table in ADR-001's "Data Model" section.

---

## Context

ADR-001 collapsed the prototype's two JSON files into one flat `lots` table — just
enough to compute bullion mark-to-market and CRH cash flow. That model is thin **by
design**: it answers "is bullion up or down?" and "is the hunt paying for itself?" and
nothing else.

Reviewing the model against how people who actually keep precious-metals and collectible
inventory work surfaced real gaps:

- **Stackers** want gross weight + purity stored separately (derive fine oz), weight units
  (grams/kilos, not just troy oz), metals beyond gold/silver (Pt/Pd), a structured
  form/kind (coin/round/bar/junk/jewelry), premium over melt, and storage/custody +
  insured value.
- **Numismatists** model coins almost orthogonally: grade + grader + cert #, catalog
  references (PCGS#, Krause KM#), year/mint/variety/strike, and a numismatic value that has
  nothing to do with spot.
- **Jewelry/artifacts** add gemstones, hallmarks, appraisals, and artistic value distinct
  from melt.
- **Librarians/registrars** would change the *structure*, not just add fields:
  1. **Catalog vs. specimen (FRBR).** Separate the *type* ("1986 1 oz American Silver
     Eagle", with its ASW, design, mintage) from the *specimens* held. Enter reference data
     once; attach many holdings.
  2. **Controlled vocabularies / authority files** instead of free text, so "American Gold
     Eagle" / "Gold Eagle 1oz" / "AGE" don't fragment into three unqueryable strings.
  3. **Accession numbers** — stable unique IDs, never reused (our autoincrement `id`).
  4. **Provenance & deaccession** as append-only records — never delete; mark withdrawn.
  5. **Multiple valuation types** (melt / market / insurance / replacement) as dated events.

This is a $5 "super easy to run" tool (ADR-001 R1/R3), not a museum collection-management
system. Over-modeling kills the product. But two structural choices cost little now and
prevent a painful migration later.

## Decision

Adopt the **catalog/specimen split** as the core storage model, plus cheap extension fields:

1. **`item_type` (catalog).** Reference data for a *kind of thing* — entered once, shared by
   many holdings: `kind` (coin/round/bar/junk/jewelry/other), `name`, `metal`, `asw_oz`
   (actual metal weight per unit for standardized types), `fineness`, `year`, `mint`,
   `mintmark`, `references` (PCGS#/KM#/etc., JSON).
2. **`lots` (specimens/holdings).** What you actually own, pointing at an `item_type`:
   `item_type_id`, `activity` ('bullion' | 'crh'), `qty`, `gross_weight` + `purity` +
   `weight_unit` (derive fine oz for bars/generic when `asw_oz` is absent), `basis_usd`,
   `premium_usd`, `face_value_usd`, `acquired`, `source`, `location`, `insured_value`,
   `attributes` (JSON escape hatch for the long-tail collectible fields — grade, cert #,
   gemstone, hallmark — without over-normalizing), `disposed`, `disposed_usd`.

**Store inputs, derive outputs.** Per-lot fine troy ounces are computed, never stored as the
single source of truth:

```
fine_oz_per_unit = item_type.asw_oz            if set (standardized coins/junk)
                 = gross_weight * purity        otherwise (bars, generic, jewelry)
fine_oz          = qty * fine_oz_per_unit
```

**Calc reads a resolved view, not the split.** The store exposes a resolver that joins each
holding to its `item_type` and yields a flat `model.Lot` (metal, fineness, fine_oz_each,
basis, activity, …) — exactly the shape the ported `portfolio.py` engine expects. The
catalog/specimen split therefore lives entirely in storage + migration; the math is
unchanged and the golden test values are unaffected.

### Controlled vocabulary — partial now, enforced later

`item_type.kind` and `metal` draw from a fixed list; free-text `name`/`mint` get full
authority-file treatment (autocomplete, dedupe) only when the UI lands. The table structure
is what we need now; vocabulary enforcement is additive.

### Explicitly deferred (not in this model)

Full grading/cert workflows, gemstone 4-Cs as first-class columns, provenance *chains*
(beyond `source` + acquired/disposed), conservation logs, registry sets, and photo/document
management. The `attributes` JSON column holds these ad hoc until a feature justifies
promoting one to a real column.

## Consequences

**Easier:** reference data entered once and reused; per-metal/per-type rollups; bars,
generic rounds, and jewelry all fit (gross × purity); inventory richness grows in
`attributes` without schema churn; a future controlled-vocabulary/authority layer has a home.

**Harder / new work:** the `migrate` command must **synthesize `item_type` rows** from the
prototype's flat lots (dedupe by product/metal/fineness/asw) and link holdings to them; calc
gains a resolver join. Both are one-time, contained.

**Revisit later:** promoting hot `attributes` keys to columns; full authority files /
controlled vocab; valuation-event history (appraisals); ADR-001's hosted phase 2 (the split
is, if anything, friendlier to multi-user).
