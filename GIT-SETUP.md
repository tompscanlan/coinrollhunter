# Get this onto GitHub (private) and onto another laptop

The working tree is ready. Run these on your own machine (not inside Cowork — git needs a
normal filesystem). Pick the path that matches your setup.

## 1. Create the private repo + first push (from THIS laptop)

```bash
cd D:\Tom\Documents\Claude\Projects\coins\coinrollhunter   # PowerShell/cmd
git init -b main
git add .
git commit -m "Initial scaffold: ADRs, prototype reference, sample data"
```

### Option A — GitHub CLI (easiest; install: https://cli.github.com)
```bash
gh auth login          # once, if you haven't
gh repo create tompscanlan/coinrollhunter --private --source=. --remote=origin --push
```

### Option B — no CLI
1. Make an empty PRIVATE repo at https://github.com/new
   - Owner: tompscanlan · Name: coinrollhunter · Private · do NOT add README/license/gitignore
2. Then:
```bash
git remote add origin https://github.com/tompscanlan/coinrollhunter.git
git push -u origin main
```

## 2. Pick up on a DIFFERENT laptop

```bash
git clone https://github.com/tompscanlan/coinrollhunter.git
cd coinrollhunter
```

### Continue in Claude Code
```bash
claude          # run inside the repo; it auto-reads CLAUDE.md for full context
```
Then say: "Start Phase 0-1 from CLAUDE.md / docs/ADR-001."

## Notes
- Your real holdings are NOT in this repo (git-ignored). Keep them in your own
  `pm_holdings.json` / `crh_ledger.json` outside the repo, or in a private data folder.
- `.env` is git-ignored; copy `.env.example` to `.env` and fill in any spot-API key.
