# Prototype (reference implementation)

The original working version. Treat it as the behavior spec for the Go/Svelte rewrite.

- `portfolio.py` — stdlib engine. Run against the sample data:
  ```bash
  python3 portfolio.py --holdings sample-data/pm_holdings.sample.json \
                       --crh sample-data/crh_ledger.sample.json
  # add --xlsx out.xlsx (needs openpyxl) or --html out.html
  ```
- `dashboard.html` — open in Chrome/Edge, load the two sample JSON files. Add/edit
  entries; saves back to the files (Chromium) or downloads them (Firefox/Safari).
- `sample-data/*.sample.json` — fictional data, safe to commit.

Real data files (`pm_holdings.json`, `crh_ledger.json`) are git-ignored on purpose.
