# ADR-010: Branches as first-class entities (and the seam for route planning)

**Status:** Proposed
**Date:** 2026-07-10
**Deciders:** Tom (owner)
**Builds on:** ADR-001 (R7 — a quantity you can *derive* is not an input; logical links over
foreign keys), ADR-002/ADR-007 (a provider interface with a keyless default and manual entry
as the permanent offline fallback), ADR-006 (per-box/per-bank yield — the data this ADR turns
into a routing objective), ADR-009 (opaque `uid` on any row an export or a path can name).
**Blocks:** multi-stop pickup/dropoff runs + route optimization (ADR-011).

---

## Context

The owner buys boxes from many branches and wants to plan a regional run — an ordered
pickup/dropoff circuit hit when he's already nearby. None of that is expressible today,
because **a bank is a string, not a thing.**

`bank TEXT` exists twice, independently, for the same real-world entity, and the two columns
are never joined: on `roll_txns` (`0001_init.sql:46`) and on `trips` (`0001_init.sql:55`).
Three consequences, all live:

1. **A typo forks a branch.** `HuntYield.svelte:14-20` groups the headline *"which banks pay
   off"* table on the raw string (`b.bank || '(unknown)'`). "Chase Main St" and "Chase Main
   Street" are two rows, each with half the history. `calc.go:238-239` counts `branch_count`
   the same way, so the same typo also inflates a summary KPI. This is not a hypothetical
   about future data; it is a property of the current grouping key.

2. **A trip is one bank, and its miles are typed by hand.** `trips` is
   `(id, date, bank, miles, hours)`. A run that touches five branches has no representation —
   not "hard to query," *absent*. And `calc.go:218-220` computes
   `gas = Σ(trip.Miles × IRSMileageRate)`, which flows through `opCost` straight into CRH net
   profit. A hand-typed number is a P&L input. ADR-001 R7 already settled the principle for
   box throughput (derived from face, never entered); miles are the same shape of mistake,
   still un-fixed.

3. **`trips.bank` is dead.** `calc` reads `Miles` and `Hours` from a trip and nothing else.
   The column is written and never consulted. Nor is there any `trip_id` on `roll_txns`
   anywhere in the tree — so the trip and the boxes bought on it are unlinked, and the gas
   cost of a run can never be attributed to the boxes that run produced.

The pickup/dropoff asymmetry is already real, and already unrepresentable. `demo/demo.go`
scatters buys across ten `demoBanks` but sends **every** `return` to a single `dumpBank`
constant (`demo.go:28`, used once at `demo.go:533`). The generator knows that where you dump
is not where you buy. The schema has no way to say it, so it was hardcoded in the fixture.

### What the owner actually wants — four surfaces, rising ambition

Walking the feature through from the driver's seat, the string→entity fix serves four
user-facing surfaces. All four read from one `branches` table; **this ADR builds the table and
the address book**, and sets direction for the rest (ADR-011). In rising order of ambition:

- **The address book — the everyday foundation.** Before any routing, a hunter wants a
  searchable, mappable notebook of *their* branches: phone, hours, whether it charges a coin
  fee, its box-order limit, which denominations it stocks, and — the single most valuable
  field — teller notes ("ask for Diane, Tuesdays"). For most hunters this notebook is the whole
  point of the app; routing is a bonus layered on top. It is literally the columns in
  Decision (a).

- **Nearby — opportunistic.** "I'll already be in that area; what's worth a five-minute
  detour?" Drop a pin (or use current location), get branches by distance with a CRH overlay:
  cooldown state, sells-my-denom, yield history, a standing order waiting. The anchor is
  *wherever you are*, not home — a corridor or a blob, not a loop.

- **A planned run — a saved, checkable drive list.** The Saturday circuit. This is where the
  logistics a bare pin-map ignores actually live (dump legs, order-ahead timing, bank hours —
  see *Direction*).

- **The rotation — "what's due."** The owner said "on a schedule," and the literal reading is
  one big loop, but the durable want is a *cadence*: cycle through branches so none is
  over-hunted and none goes stale. `max(roll_txns.date)` per branch already knows the last
  visit, so cooldown flips from a filter ("don't go here") into a prompt ("these four are past
  cooldown and untouched 30+ days — due"). Likely the most-opened view; it is why
  `cooldown_days` is a column.

## Decision

Promote the bank to an entity. **This ADR covers branches only** — the multi-stop run model
and the solver land in ADR-011, and are sketched under *Direction* below only insofar as they
constrain the columns chosen here.

### (a) A `branches` table

```sql
CREATE TABLE branches (
  id            INTEGER PRIMARY KEY,
  uid           TEXT NOT NULL UNIQUE,        -- ADR-009: opaque, never recycled
  name          TEXT NOT NULL,               -- canonical display name
  institution   TEXT,                        -- "Chase", "Riverbend CU" — brand, for grouping
  address       TEXT,
  phone         TEXT,                        -- you call to order boxes ahead; address-book core
  lat           REAL,                        -- NULL until geocoded or entered by hand
  lon           REAL,
  hours         TEXT,                        -- open vocabulary; structured windows → ADR-011
  buys          INTEGER NOT NULL DEFAULT 1,  -- sells boxes?    → eligible as a pickup stop
  dumps         INTEGER NOT NULL DEFAULT 1,  -- accepts returns? → eligible as a dropoff stop
  denoms        TEXT,                        -- denominations stocked ("halves,dimes") — "who has halves?"
  box_limit     INTEGER,                     -- max boxes they'll order per run; NULL = unknown
  box_lead_days INTEGER,                     -- order lead time; NULL = walk-in / unknown
  coin_fee_usd  REAL,                        -- per-order coin fee if any; a P&L line and a reason to skip
  cooldown_days INTEGER NOT NULL DEFAULT 0,  -- don't revisit inside this window
  notes         TEXT,                        -- teller names ("ask for Diane, Tues"), relationship, quirks
  active        INTEGER NOT NULL DEFAULT 1
);
```

`buys` and `dumps` are the schema's answer to `demo.go`'s `dumpBank`: pickup- and
dropoff-eligibility are *branch properties*, not a hardcoded name. The
`phone`/`denoms`/`box_limit`/`coin_fee_usd`/`notes` columns are the **address book proper** —
the highest value-per-effort part of this whole ADR, and useful with zero routing built (see
*Direction*). `coin_fee_usd` is a nullable flat per-order fee; the structured
fee→P&L wiring is left to whenever fees get modeled, but recording it now is what lets a hunter
cross a fee-charging branch off at a glance. `roll_txns.branch_id` and
`trips.branch_id` are added nullable, as **logical links, not foreign keys** — the choice
migrations 0004 and 0007 made, for the reason they gave (a local single-writer store), and
the one ADR-009 reaffirmed. Foreign keys *are* enforced on this connection (`store.go:28`),
so declaring one here would make a branch un-deletable while any history points at it.

### (b) Aliases absorb the free-text history

```sql
CREATE TABLE branch_aliases (
  branch_id INTEGER NOT NULL,
  alias     TEXT NOT NULL PRIMARY KEY
);
```

The migration seeds one branch per distinct trimmed non-empty `bank` string across
`roll_txns ∪ trips`, records the **raw original string** as an alias, and backfills
`branch_id` by joining through the alias. Merging the "Main St"/"Main Street" fork is then a
supported operation rather than a data-loss event: repoint the alias rows and the `branch_id`
rows at the survivor, delete the duplicate. Old exports and re-imports still resolve, because
the string they carry is still an alias.

`bank TEXT` stays, write-through, for one release. `calc` and the UI switch to `branch_id`;
a later migration drops the column once export round-trips are verified. Reversible, and it
keeps the backfill's provenance inspectable.

### (c) `uid` is generated in SQL, not Go

Migrations are pure embedded `.sql` applied on `Open` (`store.go:19-20`, `store.go:57-88`);
the runner has **no Go step**. A UUIDv4 backfill therefore cannot call into Go. Rather than
extend the runner, generate the uid in SQLite from `randomblob`:

```sql
lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
substr(lower(hex(randomblob(2))), 2) || '-' ||
substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
lower(hex(randomblob(6)))
```

Lowercase hex, per ADR-009's case-insensitive-filesystem rule. **This unblocks ADR-009 too**,
whose own `lots`/`roll_txns` uid backfill faces exactly this constraint and has not shipped —
no `uid` appears in any migration or in `model.go` today. Whichever lands first owns the
recipe; the migration numbers are assigned at implementation time, not here.

### (d) Derive quantities; store facts

R7 is narrower than "nothing derivable is stored": don't *cache a value that is a pure function
of current stored state*, since it goes stale when its inputs change. So **last visit**
(`max(roll_txns.date)`), **due-for-revisit** (`last_visit + cooldown_days < today`), and
**expected yield** (`BoxYield` aggregated by `branch_id`) stay derived, never columns. A **fact**
or an **intent**, though, is not a quantity and *is* stored: `box_lead_days` is inert until
paired with a placed-order record and a target pickup date, and the "call by Wednesday" reminder
those imply is a query over stored orders, not a cached value. That order entity — a call
log / standing-order table, plus the derived reminder — is deliberately out of scope here (its
own follow-up), and its absence is exactly why the rule is "derive quantities," not "store
nothing."

## Direction — the user-facing surfaces (built across ADR-010/011)

Recorded now because it is *why* branches carry the columns in (a). This ADR delivers the
address book; the nearby view is mostly this ADR plus the geocoding seam below; the planned
run and the rotation are ADR-011.

### The address book comes first

The unsexy version outweighs the optimizer, and it ships here: a searchable, mappable list of
branches with the fields in Decision (a), each row editable like every other grid in the app.
It answers the questions a hunter actually has before any route exists — *who stocks halves*
(`denoms`), *who charges a fee* (`coin_fee_usd`), *how many will they order and how far ahead*
(`box_limit`/`box_lead_days`), *who's the teller* (`notes`). Alias-backed dedup (Decision (b))
is what makes it trustworthy; a merge UI (list, pick survivor, repoint) is part of this slice,
not a follow-up. Don't let the solver eclipse it — for most hunters this is the surface they
open every day.

### What a planned run must respect (and a pin-map ignores)

Three things turn a list of stops into a route, and each is why a specific column exists:

- **The dropoff leg is half the driving.** You leave with cash, come back with boxes; then
  searched coin — the redeposit float, `to_redeposit = bought − returned − kept`, already
  computed by `calc` — has to go *out* to a branch that isn't where you bought it. Planning
  only pickups ignores the coin you're physically carrying. This is the whole reason `dumps`
  is separate from `buys`.

- **Ordering ahead is a precedence constraint.** Many branches won't hand you a box off the
  shelf — you call, they order, it's ready in `box_lead_days`. So Saturday's route depends on
  Wednesday's calls, and the run view must distinguish "ready for pickup today" from "call by
  Wednesday to have ready Saturday." A route that only knows geography sends you to an empty
  counter. (The placed-order record and its "calls due" reminder are their own follow-up — see
  Decision (d).)

- **Bank hours are a hard wall, not a nicety.** Branches close early, especially Saturdays,
  and coin orders are ready only certain days. A geographically perfect route that arrives at a
  locked door is worthless, so `hours` (free-text now, structured time windows in ADR-011) is
  load-bearing for the planned run — this is why time windows aren't merely a future
  optimization but a correctness constraint on the run.

### Coordinates and distance — a `RouteProvider` seam

**Coordinates and distances come from a `RouteProvider` seam** (`internal/route`), mirroring
`SpotProvider` exactly (ADR-002/ADR-007): an interface, a keyless free-tier default
(Nominatim geocode, OSRM matrix), an API key from env if a provider needs one, and **manual
`lat`/`lon` entry as the permanent offline fallback**. Geocode once, persist the coords, and
a provider outage degrades to haversine × a road factor rather than to nothing. Same seam,
same failure posture, third time.

### The optimizer maximizes expected net profit, not minimum miles

A shortest loop through every branch is the wrong objective for this app: it awards a stop to a
branch that has returned $0 across twenty boxes. Every input for the right objective already
exists — per-branch realizable find value (`BoxYield.FindValueUSD`, already post-haircut, so no
double discount), `IRSMileageRate`, and `HourlyRateUSD`:

```
maximize   Σ_{b ∈ S} ŷ_b · face_b  −  miles(S, order) · IRSMileageRate
                                    −  hours(S, order) · HourlyRateUSD
```

over a **subset** `S` of branches, not all of them. A dud branch twelve miles out simply
isn't on the route. `ŷ_b`, realizable find value per face dollar, is shrunk toward the global
rate `μ` by how much face has actually been searched there —
`ŷ_b = (Σ find_value_b + m·μ) / (Σ face_b + m)`, with `m` a one-box pseudo-count. A brand-new
branch gets the global mean instead of a division by zero; a heavily-sampled one converges on
its own rate. This reuses ADR-006's small-sample concern rather than re-litigating it.

The anchor is not always home. The *planned run* is a loop from a home base; the *nearby* view
optimizes from wherever you are — a point or a corridor, open-ended, not a closed tour. The
solver takes the start (and optional end) as parameters rather than assuming a round trip.

Two honest limits: yield is survivorship-affected (ADR-006 already flags disposed finds), and
branches get hunted out, so old boxes should decay in the estimate. The route is a
**suggestion and always overridable** — the same posture ADR-002 took toward spot prices.

### The solver sits behind an interface

Strategy chosen by stop count: exact Held-Karp under ~12 stops (instant, pure Go, no
dependency, provably optimal), nearest-neighbor + 2-opt/Or-opt above that. `demo.go`'s ten
banks suggest the exact regime is the common one. Time windows, float capacity, and a
no-dump-where-you-buy constraint turn this into a pickup-and-delivery problem with time
windows; they are deliberately **out of scope** for the first solver until the branch data
exists to feed them.

### Non-goals — what this deliberately is not

Naming these bounds the scope so "make banks more usable" doesn't drift into a mapping product:

- **Turn-by-turn navigation.** Plan the order in-app, then deep-link the ordered stops into
  Google/Apple Maps with one tap. Live traffic comes free; rebuilding a navigator does not.
- **Live box inventory from banks.** No such API exists — availability is a phone call. The app
  tracks *your* knowledge (`box_lead_days`, `box_limit`, `denoms`), never a real-time feed.
- **Sharing branch lists between users.** Regional hunters do trade good banks, but publishing
  which teller hooks you up is a privacy-and-moderation rabbit hole. Deferred indefinitely.
- **Multi-day territory / recurring-schedule optimization.** The single-run solver plus the
  "what's due" rotation cover the real need; anything more over-engineers a hobby.

## Alternatives considered

**A. Normalize `bank` strings on read (trim, casefold, fuzzy-match).** Rejected — it hides
the fork instead of letting the owner resolve it, and a fuzzy matcher that silently merges two
genuinely distinct branches on the same street is worse than the typo. Aliases make the merge
explicit, deliberate, and reversible.

**B. Keep `bank` free-text; add a separate `branch_geo` side table keyed by the string.**
Rejected — it keys durable geography off a mutable display string, which is ADR-009's mistake
one level up. Rename the branch and its coordinates orphan.

**C. Skip the entity; let the user paste a Google Maps route and record total miles.**
Rejected as the *end state*, though it remains the offline fallback. It leaves `branch_count`
and the yield table broken, keeps miles a hand-typed P&L input, and can never answer "is this
stop worth the gas," which is the actual question.

**D. Build branches, runs, stops, and the solver in one migration.** Rejected — routing
cannot be validated until branches carry real coordinates and real yield history. Branches
first is the cheap, independently valuable slice: it fixes the `byBank` grouping and the
`branch_count` KPI on its own, and it is the hard prerequisite for everything else.

## Consequences

- **+** The address book — the highest value-per-effort slice — serves every hunter with zero
  routing built, and ships in this ADR alone. For many, it's reason enough to use the app.
- **+** The headline "which banks pay off" table and `branch_count` stop forking on typos —
  the bug is fixed by the migration, before any routing exists.
- **+** Pickup/dropoff eligibility becomes data (`buys`/`dumps`) instead of a fixture constant.
- **+** Cooldown does double duty from one derived value (`max(date)`): a filter in the nearby
  view and the engine of the "what's due" rotation, with no new stored state.
- **+** Geography, hours, phone, fees, box limits, denominations, cooldown, and teller notes
  finally have somewhere to live; today they live in the owner's head.
- **+** The `uid`-in-SQL recipe unblocks ADR-009's stalled backfill at no extra cost.
- **+** `RouteProvider` reuses a seam the project has already validated twice, so an offline
  or keyless run behaves exactly as today.
- **−** Two schema migrations and a dual-read window while `bank` and `branch_id` coexist.
- **−** The alias merge is a genuine UI surface (list, pick survivor, repoint), not free.
- **−** Expected-yield routing is only as good as the history behind it, and it is
  survivorship-affected. Mitigated by shrinkage, recency decay, and never making the route
  binding.
