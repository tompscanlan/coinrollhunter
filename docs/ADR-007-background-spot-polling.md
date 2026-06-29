# ADR-007: Background spot-price polling (implementing ADR-002's `SpotProvider`)

**Status:** Accepted
**Date:** 2026-06-29
**Deciders:** Tom (owner)
**Builds on:** ADR-002 (free-tier metals API behind a swappable `SpotProvider`; manual entry
as the permanent offline fallback; every fetch appended to the `spot` table for history).

---

## Context

ADR-002 *proposed* a `SpotProvider` interface and live pricing, but it was never built: today
the only ways spot gets into the DB are the prototype import and a manual `POST /api/spot`.
So valuations drift until the owner remembers to paste in a price.

The owner wants the running app to **collect spot prices occasionally while it's up**, so
"current value" stays fresh on its own — without turning the app into something that needs an
always-on internet connection or a paid key to function. This ADR implements ADR-002's
deferred action item #4 and adds the *polling* behavior ADR-002 didn't specify.

## Decision

### 1. The `SpotProvider` seam (`internal/spot`)

```go
type Provider interface {
    Name() string
    Fetch(ctx context.Context) (model.Spot, error)
}
```

A provider returns a `model.Spot` (gold/silver/platinum/palladium USD + `source`). One HTTP
provider ships now, written against a **configurable free-tier endpoint** (per ADR-002:
gold-api.com / metals.dev / api.metals.live class), with the response→`Spot` mapping isolated
so swapping providers — or pointing at the future cached proxy — is a config change, not a
refactor. The API key (if any) comes from an **env var**, never git (ADR-002 secrets rule);
`.env.example` documents it.

### 2. A background `Poller`, started by `serve`

`serve` launches one goroutine that:

1. **Staleness-gates** every fetch: it only calls the provider when the latest stored spot is
   older than the poll interval. This avoids burning free-tier quota and avoids clobbering a
   fresh manual entry.
2. Runs an initial check shortly after start, then on a ticker at the configured interval.
3. On success, **appends to history** via `PutSpot`. Each fetch is its own history point
   (ADR-002: "every fetch is stored"): `as_of` is an **RFC3339 UTC timestamp**, which is unique
   per fetch and sorts chronologically alongside the date-only rows from manual entry / the
   prototype import — so `LatestSpot` always returns the newest observation regardless of source.
   The staleness gate (not granularity) is what bounds write volume.
4. **Degrades gracefully:** a provider error is logged and skipped, never fatal. Manual entry
   and the last-known price remain the fallback (ADR-002). The hunt does not depend on the API.
5. Stops cleanly on server shutdown (shares the `serve` context / signal handling).

### 3. Configuration (opt-out, offline-safe)

```
serve [--spot-provider NAME] [--spot-interval DUR]
  --spot-provider   provider id, or "none"/"manual" to disable polling   (env: CRH_SPOT_PROVIDER)
  --spot-interval   poll cadence, e.g. 6h                                 (default 6h; env: CRH_SPOT_INTERVAL)
  (provider URL / API key via env, documented in .env.example)
```

Polling is **on by default** with a conservative interval, but a single flag (`none`) disables
it, and any failure is silent-but-logged, so a fully offline run behaves exactly as today.

## Alternatives considered

**A. External cron / systemd timer hitting `POST /api/spot`.** Rejected — breaks the
single-binary, local-first, "just run it" promise (ADR-001/002). The collector should live in
the process the user already started.

**B. Fetch on demand inside `GET /api/summary`.** Rejected — couples every dashboard read to a
network round-trip (slow, fails offline) and multiplies quota use per page view. Polling
decouples freshness from reads; the summary always serves the last stored price instantly.

**C. Build nothing; keep manual-only.** Rejected — it's the status quo the owner asked to fix.
Manual entry stays as the fallback, but it shouldn't be the *only* path.

## Consequences

- **+** Valuations stay current on their own while the app runs; no always-on connection or key
  required for the app to function.
- **+** Implements ADR-002's `SpotProvider` seam, so the provider (or the future cached proxy)
  is swappable by config.
- **+** History accrues automatically for the spot chart (ADR-004 groundwork), one point/day.
- **+** Offline/keyless/failed-fetch runs behave like today (manual fallback, last-known price).
- **−** One more goroutine + an outbound HTTP dependency to keep healthy; mitigated by the
  staleness gate, graceful degradation, and the `none` opt-out.
- **−** Free-tier quota/accuracy varies by provider; the swappable seam + the cached-proxy
  escape hatch (ADR-002) cap the cost of a bad provider.
