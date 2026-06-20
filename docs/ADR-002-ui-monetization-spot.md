# ADR-002: UI stack, monetization, and live spot pricing for CoinRollHunter

**Status:** Proposed
**Date:** 2026-06-20
**Deciders:** Tom (owner)
**Builds on:** [ADR-001](ADR-001-architecture.md) (local-first single Go binary + SQLite, hosted-ready)

---

## Context

ADR-001 set the backbone: one Go binary serving a local web UI over SQLite. This ADR fills the
three gaps needed to make it a sharp, share-ready, tip-supported product:

1. **Front-end stack** — it has to look like a real paid app, with spreadsheet-style editing.
2. **Monetization** — "pay $5 if you like it, tip more" for an offline local tool.
3. **Live spot prices** — gold/silver must update automatically, cheaply.

Product name: **CoinRollHunter** ("CRH" is the community's own shorthand, so it reads instantly).

---

## Decision

**UI:** Svelte 5 single-page app, built with Vite, styled with Tailwind + **shadcn-svelte**
components, editable grids via **TanStack Table (Svelte adapter)**, and a small chart lib
(**LayerCake** or **uPlot**) for spot history. The Vite build outputs static assets that are
embedded into the Go binary with `go:embed` — so distribution stays "one file."

**Monetization:** pay-what-you-want with a suggested **$5**, sold through a **merchant-of-record**
(**Lemon Squeezy** or **Polar**) that handles global sales-tax/VAT. **No license enforcement** —
an honest in-app "Support CoinRollHunter" link is the whole mechanism. The app is fully functional
whether or not someone pays.

**Spot pricing:** call a **free-tier metals API directly** from the app for now, behind a small
`SpotProvider` interface so the source is swappable. When scale strains the free tier, flip a
config flag to point at a **cached central proxy** (one subscription serves everyone) — no app
rewrite. Manual entry remains the always-available offline fallback, and every fetch is stored in
the `spot` table for history/charts.

---

## Options Considered

### Front-end framework

| Option | Look/feel | Bundle/perf | Ecosystem | Fit |
|--------|-----------|-------------|-----------|-----|
| **Svelte 5 + Tailwind + shadcn-svelte (chosen)** | Sharp | Smallest, no virtual DOM | Smaller but sufficient | Owner's pick; lean; embeds cleanly |
| React + Tailwind + shadcn/ui | Sharp | Heavier runtime | Largest | Safe default; more deps |
| Plain HTML + CSS framework | OK | Tiny | n/a | Too plain for a paid feel; weak grids |

**Why Svelte 5:** least JavaScript shipped, compiles to small static assets (ideal for `go:embed`),
and shadcn-svelte gives the same premium component look as the React original. Trade-off: smaller
community than React, but every piece we need (grid, charts, components) has a mature Svelte path.

### Data grid (spreadsheet editing — R4/R5 from ADR-001)

- **TanStack Table, Svelte adapter (chosen):** headless, fully styleable to match shadcn, inline
  edit/add/delete, sorting/filtering. Most control, a bit more wiring.
- **AG Grid Community:** Excel-like out of the box, heavier and visually distinct from shadcn.
- Fallback: a hand-rolled editable table (only if we want zero grid dependency).

### Monetization

| Option | Tax handled for you | Fees | Friction | Notes |
|--------|--------------------|------|----------|-------|
| **Lemon Squeezy / Polar (MoR) (chosen)** | Yes (merchant of record) | ~5% + payment fees | Low | They remit VAT/sales tax; best for selling to strangers |
| Ko-fi / Gumroad | No / partial | Lower | Low | Simpler, but tax is your problem |
| Stripe + license keys | No | Lowest | High | Build + crackable enforcement; not worth it |

**Why MoR:** the scariest part of selling publicly is sales-tax/VAT compliance the moment a
stranger in another state/country pays. A merchant-of-record absorbs that for a small cut.

### Spot pricing source

| Option | Cost | Scale risk | Privacy | Notes |
|--------|------|-----------|---------|-------|
| **Free-tier API direct, provider interface (chosen)** | $0 | Per-user quota; fine at low volume | App calls upstream directly | Start here; abstracted for easy swap |
| Cached central proxy | ~$0-16/mo, one sub | Solves quota; secret key server-side | App calls our proxy | Phase 2 when volume demands |
| Manual only | $0 | None | Total | Kept as permanent fallback |

Candidates for the free tier: gold-api.com, GoldAPI.io, Metals.Dev free, api.metals.live
(keyless under ~30k req/mo). The `SpotProvider` interface means switching providers or moving to
the proxy is a config change, not a refactor.

---

## Trade-off Analysis

All three decisions favor **starting lean with a clean seam to scale**, matching the
"personal tool, share-ready" scope:

- Svelte ships less and embeds smaller than React, at the cost of a smaller (but adequate) ecosystem.
- Pay-what-you-want via MoR trades a ~5% cut for zero tax/compliance burden and zero enforcement code.
- Free-tier-direct spot trades a per-user quota ceiling for $0 cost today; the provider interface
  caps the cost of being wrong (swap to a cached proxy later without touching the UI).

---

## Consequences

**Easier:** a premium look with little custom design (shadcn-svelte); small embeddable bundle;
selling to the public without tax headaches; changing spot providers via config.

**Harder / new work:** we add a JS/TS toolchain (Node + Vite) to the build — the Go binary now
depends on a prebuilt `dist/` (CI must run the Svelte build before `go build`); we must wire
TanStack Table cells for inline editing; we must register with an MoR and add an in-app support link.

**Revisit later:** the cached spot proxy (when free-tier limits bite); whether to add optional
license/entitlement if we ever ship paid-only features; AG Grid if TanStack styling proves slow to build.

---

## Repository & secrets

When we create the `coinrollhunter` repo, check in: the existing prototype as a reference
(`portfolio.py`, `dashboard.html`, the two JSON files, `Coins_Portfolio.xlsx`), this `docs/` folder
(both ADRs), the Go app, and the Svelte front-end source.

**Do not commit secrets.** Any spot-API key lives in an env var / local config, never in git.
`.gitignore` must include: `*.db` (user data), API keys/`.env`, `node_modules/`, `web/dist/`
build output (built in CI), and OS/editor cruft. Ship a `.env.example` documenting the keys.

---

## Action Items (amends ADR-001 plan)

1. [ ] Confirm name + reserve `coinrollhunter` GitHub org and `.com`/`.app` domain.
2. [ ] Scaffold Vite + Svelte 5 + Tailwind + shadcn-svelte in `/web`; wire `go:embed` of `web/dist`.
3. [ ] Add TanStack Table editable grids for lots / roll_txns / trips / supplies / keepers.
4. [ ] Implement `SpotProvider` interface + one free-tier provider + manual override; persist to `spot`.
5. [ ] Add spot-history chart (LayerCake or uPlot).
6. [ ] Register MoR (Lemon Squeezy or Polar); add in-app "Support" link; ship binaries via that store.
7. [ ] `.gitignore` + `.env.example`; ensure CI builds `web/dist` before `go build`.
