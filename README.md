# CoinRollHunter

Track coin-roll-hunting (CRH) and precious-metals bullion together, and answer the
questions that actually matter: **is my bullion profitable, is coin roll hunting paying
for itself, and have I cashed in everything I didn't keep?**

Local-first and private: your holdings live in a SQLite file on your machine and never
leave it. Ships as a single cross-platform binary — run it and the dashboard opens in
your browser.

> **Status:** the Go + Svelte app works. A single binary embeds a Svelte 5 dashboard and
> serves it on localhost over a REST API. The `prototype/` folder remains the behavior
> reference (Python engine + interactive HTML).

## What's here

- `cmd/coinrollhunter` — the single binary (`migrate`, `serve`, `version`).
- `internal/` — `model`, `calc` (profitability engine), `store` (SQLite), `legacy`
  (prototype JSON importer), `api` (REST).
- `web/app` — the Svelte 5 + Vite + Tailwind UI (shadcn-style components, TanStack
  editable grids). Built to `web/dist` and embedded via `go:embed`.
- `docs/ADR-00{1,2,3}` — architecture, UI/monetization/spot, catalog/specimen model.
- `prototype/` — the reference implementation (`portfolio.py`, `dashboard.html`,
  fictional `sample-data/`).
- `CLAUDE.md` — context for picking up in a Claude Code / CLI session.

## Run it

```bash
make build            # builds the UI then the Go binary (needs Go 1.26 + Node 22)
# Optional: load the fictional sample data to see it populated
./coinrollhunter migrate \
  --holdings prototype/sample-data/pm_holdings.sample.json \
  --crh prototype/sample-data/crh_ledger.sample.json
./coinrollhunter serve     # then open http://127.0.0.1:8787
```

`make run` builds and serves in one step. For UI development with hot reload, run the Go
API (`./coinrollhunter serve`) and `cd web/app && npm run dev` — Vite proxies `/api` to it.

## Releases (cross-platform binaries)

Pure-Go SQLite means clean cross-compilation with no per-OS toolchain:

```bash
make release          # -> dist/ archives for linux, windows, macOS (amd64 + arm64)
```

Or push a tag (`git tag v0.1.0 && git push origin v0.1.0`) and the `release` workflow
builds and publishes the same archives to a draft GitHub Release. A `.goreleaser.yaml` is
provided as an alternative.

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
