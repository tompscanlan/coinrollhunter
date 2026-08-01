package spot

import (
	"context"
	"log"
	"time"
)

// Poller periodically refreshes spot prices while the server runs (ADR-007). It is
// staleness-gated (only fetches when the latest stored price is older than Interval),
// appends every successful fetch to history, and degrades gracefully — a provider error
// is logged and skipped, never fatal, so the last-known price and manual entry remain the
// fallback (ADR-002).
type Poller struct {
	Provider Provider
	Sink     Sink
	Interval time.Duration

	// now and logf are injectable for tests; nil uses time.Now / log.Printf.
	now  func() time.Time
	logf func(string, ...any)
}

// NewPoller builds a poller with sensible defaults (now=time.Now, logf=log.Printf).
func NewPoller(p Provider, sink Sink, interval time.Duration) *Poller {
	return &Poller{Provider: p, Sink: sink, Interval: interval, now: time.Now, logf: log.Printf}
}

func (p *Poller) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *Poller) print(format string, args ...any) {
	if p.logf != nil {
		p.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Run does an initial staleness-gated fetch, then repeats every Interval until ctx is
// cancelled (server shutdown). Blocking — start it in a goroutine.
func (p *Poller) Run(ctx context.Context) {
	if p.Interval <= 0 {
		p.Interval = 6 * time.Hour
	}
	p.print("spot: polling via %s every %s (manual entry still available)", p.Provider.Name(), p.Interval)
	p.tick(ctx)
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

// tick fetches once if the stored price is stale, and appends the result. Returns true if
// a new observation was stored (used by tests).
func (p *Poller) tick(ctx context.Context) bool {
	if !p.stale() {
		return false
	}
	sp, err := p.Provider.Fetch(ctx)
	if err != nil {
		p.print("spot: fetch via %s failed: %v (keeping last price)", p.Provider.Name(), err)
		return false
	}
	// Append, not overwrite: every fetch is its own history point (ADR-002). An RFC3339
	// UTC timestamp keeps as_of unique and chronologically sortable alongside date-only
	// manual/import rows.
	sp.AsOf = p.clock().UTC().Format(time.RFC3339)
	if sp.Source == "" {
		sp.Source = p.Provider.Name()
	}
	if err := p.Sink.PutSpot(sp); err != nil {
		p.print("spot: store failed: %v", err)
		return false
	}
	p.print("spot: updated via %s — gold=%.2f silver=%.2f", sp.Source, sp.GoldUSD, sp.SilverUSD)
	return true
}

// stale reports whether the latest stored observation is older than Interval (or missing
// / unparseable). On a read error it returns true so we still attempt a fetch.
func (p *Poller) stale() bool {
	last, err := p.Sink.LatestSpotObservation()
	if err != nil || last.AsOf == "" {
		return true
	}
	ts, ok := parseAsOf(last.AsOf)
	if !ok {
		return true
	}
	return p.clock().UTC().Sub(ts) >= p.Interval
}

// parseAsOf accepts both the poller's RFC3339 timestamps and the date-only form used by
// manual entry / the prototype import.
func parseAsOf(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
