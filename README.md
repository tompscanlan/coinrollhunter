# CoinRollHunter

Track coin-roll-hunting (CRH) and precious-metals bullion together, and answer the
questions that actually matter: **is my bullion profitable, is coin roll hunting paying
for itself, and have I cashed in everything I didn't keep?**

Local-first and private: your holdings live in a SQLite file on your machine and never
leave it. Planned to ship as a single cross-platform binary you double-click to run.

> **Status:** early. The `prototype/` folder is the working reference (Python engine +
> interactive HTML). The shippable app (Go + Svelte) is being built per `docs/`.

## What's here

- `docs/ADR-001-architecture.md` — local-first single Go binary + SQLite, hosted-ready.
- `docs/ADR-002-ui-monetization-spot.md` — Svelte 5 UI, pay-what-you-want, spot pricing.
- `prototype/` — the current reference implementation:
  - `portfolio.py` — stdlib engine: bullion mark-to-market, CRH net, cash-in
    reconciliation, box throughput; `--xlsx` / `--html` exporters.
  - `dashboard.html` — interactive dashboard (reads/writes the JSON data files).
  - `sample-data/` — fictional sample data so you can try it without your own.
- `CLAUDE.md` — context for picking up in a Claude Code / CLI session.

## Try the prototype

```bash
cd prototype
python3 portfolio.py \
  --holdings sample-data/pm_holdings.sample.json \
  --crh sample-data/crh_ledger.sample.json
```

Open `prototype/dashboard.html` in Chrome/Edge and load the two sample JSON files.

## Privacy

Personal holdings are **never** committed. `pm_holdings.json`, `crh_ledger.json`, and
`*.db` are git-ignored; only the fictional `*.sample.json` files live in the repo.

## License

MIT © 2026 Tom Scanlan
