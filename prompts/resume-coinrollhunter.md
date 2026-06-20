# Resume prompt — CoinRollHunter (paste into Claude Code)

Use this to pick up development on any machine. Everything you need is in this git repo;
no other local files are required.

## 0. Get the code (run in a terminal)
```bash
git clone https://github.com/tompscanlan/coinrollhunter.git
cd coinrollhunter
claude            # start Claude Code in the repo root
```

## 1. Paste this prompt into Claude Code

---

You are resuming **CoinRollHunter**, a local-first tracker for coin roll hunting (CRH) and
precious-metals bullion. I only have this git repo — there are no other files on this machine,
so treat the repo as the complete source of truth.

First, read these in order and confirm you understand the plan:
1. `CLAUDE.md` — project context, decisions, conventions, and the profitability math to preserve.
2. `docs/ADR-001-architecture.md` — local-first single Go binary + SQLite, hosted-ready.
3. `docs/ADR-002-ui-monetization-spot.md` — Svelte 5 UI, pay-what-you-want, spot pricing.
4. `prototype/portfolio.py` and `prototype/dashboard.html` — the behavior spec to port. Run it
   against `prototype/sample-data/*.sample.json` to see expected output.

Then begin **Phase 0–1** from ADR-001:
1. `go mod init github.com/tompscanlan/coinrollhunter`; create the layout `/cmd`,
   `/internal/store`, `/internal/calc`, `/web`.
2. Define the SQLite schema from ADR-001 as migrations (lots, roll_txns with flexible
   box/roll/face/coin units normalized to face_usd, trips, supplies, keepers, spot, settings).
3. Write a `migrate` command that imports a user's existing `pm_holdings.json` + `crh_ledger.json`
   into SQLite (use the prototype JSON shapes; do NOT commit any real data).
4. Port the profitability math from `prototype/portfolio.py` into `internal/calc` with unit tests.
   **Pin these expected values** (from the owner's real data, used only as test fixtures — recreate
   them as a fixture file, do not embed personal holdings in the repo):
   - CRH net (cash, realizable) = **$103.81**
   - bullion unrealized = **−$1,546.43**
   - to-redeposit = **$163.50**
   - boxes searched = **3.1** (2.1 halves + 1 quarters)
   - buyback haircuts: 40% silver **0.80**, 90% silver **0.90**
   - CRH net = finds_realizable − face_cost − gas − supplies
   - cash-in: to_redeposit = bought − returned − kept(finds + clad)
5. Build the REST API (CRUD for all tables; spot get/set; summary endpoint) and `go:embed` the
   built Svelte app from `web/dist`.

Work in small, tested commits. Use Go for the app, TypeScript/Svelte 5 for the UI, Python only for
tooling. Never commit personal data — keep `pm_holdings.json`, `crh_ledger.json`, and `*.db`
git-ignored; ship only `*.sample.json`. Ask me before any destructive or irreversible step.

Start by reading the four files above and giving me a short plan + the first commit's scope.

---
