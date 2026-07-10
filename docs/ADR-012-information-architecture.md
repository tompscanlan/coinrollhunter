# ADR-012: Information architecture — three altitudes, a verb hub, progressive disclosure

**Status:** Proposed
**Date:** 2026-07-10
**Deciders:** Tom (owner)
**Builds on:** ADR-002 (the Svelte 5 + Tailwind + shadcn-svelte UI stack), ADR-006 (the
analysis surfaces — hit-rate, per-bank/per-box yield — that this ADR relocates).
**Governs the UI of:** ADR-010 (branches — the first feature that must enter as an *addition*,
not an Overview section), ADR-011 (routing/runs UI, pending), and the box-orders follow-up
(bead `om-2h7a`). Every feature after this one has a UI contract to build against.

---

## Context

The app grew feature-first, and it shows in one place: **`Dashboard.svelte` (the Overview tab)
is ~13 stacked sections in a single scroll** — verdict card, four headline stats, composition,
stack-by-type, bullion table, finds table, reconciliation banner, four more stats, three more
KPIs, hunt-yield-by-bank, trophy feed, hit-rate grid, realized table, and a spot-price *editor*.
It answers "am I OK?" and "what's my hit-rate by denom × category × source?" in the same
uninterrupted column, and it makes you enter data (spot) inside a report you came to read.

The good news is the bones are already right. The app has three top-level views behind the
segmented toggle in `App.svelte`, and they are three genuine **altitudes**:

- **Overview** (`Dashboard.svelte`) — the numbers.
- **Do** (`Do.svelte`) — a verb hub: *"What did you do?"* → six action tiles → one focused
  workflow (`workflows/*.svelte`). Each tile carries a plain subtitle and a live, data-aware
  `hint` (the "Returned to bank" tile shows `$X outstanding`). This is already the
  minimal-and-obvious, new-user surface.
- **Edit** — the six `EditableGrid` data tables; the correction/backstop layer.

So the problem is narrower than "the UI is bad." It is two specific things:

1. **Overview conflates altitudes.** *Glance* (verdict + headline stats — what a new user needs
   and most users want daily), *ledger* (bullion/finds/realized tables + reconciliation), and
   *analysis* (yield-by-bank, hit-rate grid, trophies, composition, stack-by-type) are dumped
   together, plus a stray *editor* (spot) that doesn't belong in a read view at all.

2. **There is no rule for where a new feature goes.** The pipeline is only getting longer —
   branches (ADR-010), box orders (`om-2h7a`), routing (ADR-011) — and absent a contract, each
   will accrete another Overview section, exactly the way the first thirteen did. This is the
   force the owner named: *extra features must be additions, not overload; new users shouldn't
   need to get involved; all of it should be obvious.*

The new-user case makes it concrete. A fresh `crh.db` renders a dashboard built for the
15-month `demo` dataset: a verdict card reading "$0.00, you're fine," empty tables, zero KPIs.
The first thing a new user should see is the *one obvious next action*, not a wall of zeros.

## Decision

### 1. Four surfaces, one altitude each

Split Overview's two jobs and name the boundary. The nav becomes **Overview · Do · Insights ·
Edit**, and each surface owns exactly one altitude:

| Surface | Altitude | Owns | Read/write |
|---|---|---|---|
| **Overview** | *Glance + ledger* | Verdict card, headline stats, bullion + finds tables, reconciliation banner, realized table | read-only |
| **Insights** | *Analysis* | Hunt-yield-by-bank, hit-rate grid, trophy feed, composition, stack-by-type — and future "which banks pay off" (ADR-010) + route yield (ADR-011) | read-only |
| **Do** | *Act* | The verb hub; every feature-action is a tile here | write, focused |
| **Edit** | *Correct* | The `EditableGrid` tables; entities live here | write, tabular |

Overview slims to what you can *read without interpreting*: is the hunt costing money, what's
it worth, is the float square. Everything that requires study or a legend moves to Insights,
which a day-to-day user simply never has to open.

### 2. The addition contract — verbs, tables, nudges; never an Overview section

Every feature from here on decomposes into at most three parts, each with a fixed home:

- **New data** → a new `EditableGrid` tab under **Edit** (branches, box_orders).
- **A new action** → a tile under **Do** (`Order boxes ahead`, `Plan a run`), reusing the
  existing `Action` shape (`id`/`title`/`sub`/`icon`/`enabled`/`hint`).
- **A proactive signal** → a Do tile **`hint`** ("2 calls due", "4 branches due"). At most
  *one* aggregate status chip may reach Overview; anything richer is a hint or an Insights row.

**No feature adds a section to `Dashboard.svelte`.** That file's growth is what this ADR exists
to stop. The nudge infrastructure the new features need already ships — Do's `hint` line is it.

### 3. Progressive disclosure — surfaces stay dormant until data earns them

Depth is *earned*, not front-loaded, reusing Do's existing `enabled` + "soon" affordance:

- **Routing** (`Plan a run`) stays hidden/disabled until several branches carry coordinates.
- **"Which banks pay off"** (Insights) doesn't render until more than one branch has been hunted.
- **"Calls due"** (Do hint) appears only when an open order exists.

A new user on their first box meets none of it. Each surface reveals itself with a nudge at the
moment its data makes it useful ("You've hunted 5 branches — see which pay off →"), so the owner
never has to opt into complexity and never trips over it early. Reveal triggers are simple
derived predicates (count/exists), not stored flags — consistent with ADR-010 (d).

### 4. New-user landing is an action, not a wall of zeros

An empty database lands on **Do**, or Overview renders a get-started empty state whose single
call to action funnels into Do's "Bought a box." First run is one obvious step. The `bank`
field stays a plain text input on that first workflow — a new user never meets "branches",
"routes", or "orders" as concepts until they have reason to.

### 5. Read views host no editors

Overview and Insights are read-only. The **spot-price editor moves off `Dashboard.svelte`** to
`SettingsPanel` (or a small Do micro-action); Overview keeps the read-only spot *freshness chip*
it already shows. This removes the mode confusion of an input box inside a report.

## Alternatives considered

**A. Keep one Overview; make the analysis sections collapsible.** Rejected as the *boundary*.
A collapsed section still shows its header and still says "hit-rate by source" to someone who
came to check if they're losing money — the noise is reduced, not removed. The boundary should
be a surface you don't open, not a fold you scroll past. (Collapsibles *within* Insights are
fine — that's altitude-appropriate.)

**B. An "advanced mode" toggle in Settings.** Rejected — it makes the user responsible for
discovering depth. Dormancy-by-data (Decision 3) reveals each surface automatically at the
moment it becomes useful, which is what "obvious" actually requires.

**C. A dedicated top-level tab per feature (Branches, Orders, Routes, …).** Rejected — that is
the Overload one level up: the nav grows without bound as features land. Verbs collapse many
features into one hub (Do); tables collapse many entities into one hub (Edit). Two hubs absorb
an arbitrary number of features; N tabs do not.

**D. Do nothing; decide per feature.** Rejected — per-feature judgment with no contract is
exactly how Overview reached thirteen sections. The point of the ADR is to make the cheap,
wrong default (add a section) unavailable.

## Consequences

- **+** Every future feature has a predetermined home (Edit grid / Do tile / Do hint), and
  `Dashboard.svelte` stops growing. The contract is the deliverable, more than the refactor.
- **+** New users meet one obvious action; the analysis depth is earned, never front-loaded.
- **+** Insights is the home ADR-010's "which banks pay off" and ADR-011's route-yield views
  needed anyway — they now have somewhere to land that isn't Overview.
- **+** Reuses Do's `hint`/`enabled` patterns and the `EditableGrid` tab pattern, so most of
  this is relocation, not new UI machinery.
- **−** A one-time refactor: lift the analysis cluster out of `Dashboard.svelte` into an
  `Insights.svelte`, add the fourth nav entry, move the spot editor. Visual-regression risk on
  a screen with a lot of existing markup.
- **−** Dormancy triggers are logic to keep honest (when does routing light up?); mitigated by
  keeping them simple derived predicates, not a settings matrix.
- **−** A fourth top-level tab is one more choice in the nav; mitigated because day-to-day use
  lives in Overview + Do, and Insights/Edit are opt-in.

## Implementation notes (for the later build)

Not part of the decision, but to save re-discovery:

- New component `Insights.svelte`; move `HuntYield`, `HitRateGrid`, `TrophyFeed`, `Composition`,
  `StackByType` out of `Dashboard.svelte` into it. Overview keeps the verdict `Card`, the
  `StatCard` rows, the bullion/finds/realized tables, and the reconciliation banner.
- `App.svelte`: extend `type View` with `'insights'`; add the tab button; default `view` to
  `'do'` (or an Overview empty-state) when `report` shows an empty dataset.
- `Do.svelte`: add `order`/`route` actions to `actions`, gated by `enabled` on the reveal
  predicates in Decision 3; their `hint`s carry "calls due" / "branches due".
- Move the spot `<form>` block out of `Dashboard.svelte` into `SettingsPanel.svelte`.
