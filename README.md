# CoinRollHunter

**Track coin roll hunting (CRH) and precious-metals bullion together — and finally answer
the question that matters: _is the hunt actually paying for itself?_**

Local-first and private: your holdings live in a SQLite file on your own machine and never
leave it. No account, no cloud, no telemetry. It ships as a single cross-platform binary —
run it and open the dashboard in your browser.

<p align="center">
  <img src="docs/screenshots/hero.png" alt="CoinRollHunter dashboard — PROFITABLE (cash basis) +$48.55" width="900">
</p>

<p align="center">
  <b><a href="https://github.com/tompscanlan/coinrollhunter/releases/latest">⬇&nbsp; Download for macOS · Windows · Linux</a></b><br>
  <sub>free / pay what you want · no account · runs entirely on your machine</sub>
</p>

---

## Why

- **Knows if coin roll hunting is paying off.** Near-free silver finds vs. the real costs
  (face redeposited short, gas, supplies, shrinkage) → one honest cash-basis verdict.
- **Bullion and the hunt, side by side but separate.** Your long-term stack (what it's worth
  at today's spot vs. what you paid) and CRH cash-flow answer different questions — kept apart
  on purpose, so a gold dip never hides the fact that the hunt is in the black.
- **Mirrors the hobby, not a spreadsheet.** The **Do** tab is the *verbs* you actually do —
  *Bought a box · Logged finds · Returned to bank · Reconcile · New coin · Sold* — and it
  records the right rows for you. The raw grids are still there in **Edit** for corrections.
- **Local-first by construction.** One SQLite file, on your machine. Privacy is the design,
  not a setting.
- **One file, every platform.** Pure-Go SQLite (no CGO) means a single static binary for
  macOS, Windows, and Linux — no install, no runtime, no dependencies.

## What it looks like

**The dashboard** — is the hunt paying for itself, what's the stack worth, what's left to redeposit:

![Overview dashboard](docs/screenshots/overview.png)

**The “Do” tab** — log what you actually did; it writes the right rows:

![Do tab — workflow tiles](docs/screenshots/do.png)

**The “Edit” tab** — spreadsheet-style grids whenever you want to edit directly:

![Edit tab — editable grids](docs/screenshots/edit.png)

## Download & run

1. Grab the archive for your OS from the [latest release](https://github.com/tompscanlan/coinrollhunter/releases/latest).
2. Unpack it.
3. **Double-click it** — `CoinRollHunter.exe` on Windows, `coinrollhunter` on macOS/Linux.

That's it. The app starts and opens your dashboard in a browser window. No terminal, no
URL to type. Quit it with the ⏻ button in the top right.

Your data is saved to a single SQLite file in your user data directory:

| | |
|---|---|
| **Windows** | `%LOCALAPPDATA%\CoinRollHunter\crh.db` |
| **macOS** | `~/Library/Application Support/CoinRollHunter/crh.db` |
| **Linux** | `~/.local/share/coinrollhunter/crh.db` |

(If you already have a `crh.db` sitting next to the binary — how earlier versions
worked — that one keeps being used, so upgrading never loses your holdings.)

### Backing up

Don't copy `crh.db` by hand while the app is open. SQLite keeps recent changes in a
`-wal` sidecar file, so a plain copy can miss your latest edits or catch a write
half-finished. Use:

```bash
./coinrollhunter backup my-coins-2026-07-12.db
```

That writes one complete, self-contained database — no sidecars, nothing else needed —
and it's safe to run with the app still open. Copy the result anywhere: a USB stick, a
sync folder, another machine. Open it later with `./coinrollhunter serve --db my-coins-2026-07-12.db`.

It won't overwrite an existing backup, and it never modifies the database it's reading —
so it's also the right thing to run *before* upgrading to a new version.

### Leaving with your data

A backup is a copy the *app* can restore. An **export** is a copy *you* can read — and
you can take one whenever you like, from **Settings → Your data**, or from a terminal:

```bash
./coinrollhunter export my-collection-2026-07-12/
```

Either way you get the same bundle. The button downloads it as a zip; the command writes
it as a plain folder, so your photos are sitting right there beside the spreadsheets.

```
item_type.csv       the coin types you've catalogued
lots.csv            what you own — every specimen, live and sold
roll_txns.csv       boxes and rolls bought, coin returned to the bank
keepers.csv         clad you kept back
trips.csv           bank runs (miles + hours)
supplies.csv        tubes, flips, wrappers
losses.csv          shrinkage written off at reconcile
branches.csv        your bank address book
branch_aliases.csv  the older spellings each branch is known by
spot.csv            every metal price you've ever recorded
settings.csv        your tunables
photos.csv          one row per picture, with the file it points at
data.json           the same rows, typed — see below
manifest.json       what's in the bundle, and its checksums
photos/             the picture files themselves
```

**Open the CSVs in anything.** Excel, Numbers, LibreOffice, a text editor. That's the point:
nothing in the bundle needs CoinRollHunter to read it.

**`uid` is the column that makes it safe to leave.** Every row that matters carries one: an
opaque id that is never reused, even after you delete something. Where a spreadsheet would
normally join on a row number — and silently point at the wrong coin once a row is deleted
and the number gets handed out again — this bundle joins on `uid`. So `lots.csv` carries
both `item_type_id` (the internal number) and `item_type_uid` (the one that keeps its
meaning), and the same for the box a find came from and the branch a trip went to.

**The photos join is one column.** `photos.csv` has a `path` column — `photos/<coin>/<photo>.jpg`
— pointing straight at the file in the bundle. No filename convention to work out, and the
folder still reads sensibly in a file manager: one folder per coin or per box.

> **A word about photo files.** They're exported as the originals you imported, which means
> they still carry whatever your camera saved inside them. On a phone, that usually includes
> **the location the photo was taken** — often your home. Worth knowing before you send an
> export to anyone.

**`data.json` is the same data, losslessly.** CSV can't tell an empty cell from "nothing was
ever recorded here" — both are two commas with nothing between them. `data.json` keeps the
difference (and keeps numbers as numbers), so it's the file to hand to a program. "Lossless"
means every number, every empty-vs-nothing distinction, and all normal text — everything the
app itself writes. (The one exception: text that isn't valid Unicode, which only turns up if
some other tool wrote straight to the database, comes out best-effort.)

**`manifest.json` says what a bundle is.** Two version numbers, and they mean different things:

- `format_version` — the version of *this bundle format*. It starts at 1 and changes only when
  a file or a column does. Anything reading a bundle should **refuse** one whose
  `format_version` is higher than it understands, rather than importing part of it.
- `db_schema_version` — which database migration the columns reflect.

It also lists every file with its row count and its SHA-256, so you (or a future importer) can
check a bundle is intact years from now, with no app and no network. Two more lists round it
out: `missing` names any photo a row points to that wasn't included — absent, unreadable, or a
name that wasn't safe to write (noted here, never silently skipped, and it never stops the rest
of the export) — and `unexpected_settings` names any setting beyond the app's known handful, a
tripwire so a value that doesn't belong in an export you share can't slip out unnoticed. (A
tool reading a bundle should treat the `missing` entries as plain labels, not paths to open —
they come straight from the data.)

Exporting never touches your database. The command reads a throwaway snapshot, so pointing it at
an old archive can't quietly upgrade the file you were trying to preserve.

`coinrollhunter export DIR` writes into an **empty directory you choose** (it refuses a directory
that already has files in it). If a file with the same name as one of the bundle's turns up in
that directory *while the export is running*, it stops with an error rather than overwriting it;
and if an export fails partway, it removes only the files it wrote — never anything that was
already there or that another program put there.

> **Unpack it first.** Windows will happily run an `.exe` straight from inside the zip
> preview, but it does that by unpacking to a temporary folder that gets cleaned up later.
> Drag the folder out of the zip before you click.

> **Unsigned binaries.** The releases aren't code-signed yet, so the first launch trips
> Gatekeeper (macOS: right-click → **Open**, then **Open**) or SmartScreen (Windows:
> **More info → Run anyway**). The source is right here if you'd rather build it yourself.

### From a terminal

Every subcommand still works. On Windows use `cli\coinrollhunter.exe` — the top-level
`CoinRollHunter.exe` is built for the GUI subsystem and can't print to a console.

```bash
./coinrollhunter serve --db crh.db --addr 127.0.0.1:8787
```

Want to see it populated before entering your own holdings? Run the demo:

```bash
./coinrollhunter demo              # then open http://127.0.0.1:8787
```

That seeds a **separate** `demo.db` with ~15 months of fictional hunting — ~$44k face
searched across ~500 buys, a bullion stack, trophies, sales, an outstanding float — so
every screen has something on it, including the hit-rate grid with honest low-sample
warnings. Poke, edit, and delete freely; it never touches your real `crh.db`. Start over
any time with `./coinrollhunter demo --reset`.

There's also a smaller fixture if you prefer the importer route:

```bash
./coinrollhunter migrate \
  --holdings sample-data/pm_holdings.sample.json \
  --crh sample-data/crh_ledger.sample.json
./coinrollhunter serve
```

## Privacy

Your holdings are yours. `pm_holdings.json`, `crh_ledger.json`, and every `*.db` are
git-ignored and never committed — only fictional `*.sample.json` files live in this repo.
The app makes no network calls except an optional spot-price lookup, and even that has a
manual-entry fallback.

## Build from source

Needs Go 1.26 and Node 22.

```bash
make build            # builds the Svelte UI, then the Go binary with the UI embedded
make run              # build + serve in one step
make release          # cross-compile archives for every platform into dist/
```

For UI development with hot reload, run the Go API (`./coinrollhunter serve`) and, in
another shell, `cd web/app && npm run dev` — Vite proxies `/api` to the Go server.

Pushing a tag (`git tag v0.1.0 && git push origin v0.1.0`) triggers the release workflow,
which builds and publishes the cross-platform archives to a GitHub Release.

## What's here

- `cmd/coinrollhunter` — the single binary (`migrate`, `serve`, `demo`, `version`).
- `internal/` — `model`, `calc` (the profitability engine), `store` (SQLite), `legacy`
  (sample/JSON importer), `api` (REST), `demo` (the fictional demo-dataset seeder).
- `web/app` — the Svelte 5 + Vite + Tailwind UI (shadcn-style components, TanStack editable
  grids), built to `web/dist` and embedded via `go:embed`.
- `docs/ADR-*` — the architecture decisions (single Go binary + SQLite, the UI/monetization/
  spot stack, the catalog/specimen data model, reconcile/shrinkage, the find taxonomy).
- `sample-data/` — a fictional dataset (`*.sample.json`) for trying the app and for tests.
- `CLAUDE.md` — context for picking the project back up in a Claude Code / CLI session.

## License

MIT © 2026 Tom Scanlan
