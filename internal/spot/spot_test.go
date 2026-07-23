package spot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// --- provider (gold-api) ------------------------------------------------------

func TestGoldAPIFetch(t *testing.T) {
	prices := map[string]string{
		"/price/XAU": `{"name":"Gold","price":3000.5}`,
		"/price/XAG": `{"name":"Silver","price":30.25}`,
		"/price/XPT": `{"name":"Platinum","price":900}`,
		"/price/XPD": `{"name":"Palladium","price":1000}`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := prices[r.URL.Path]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	defer ts.Close()

	p := NewGoldAPI(ts.URL, ts.Client())
	sp, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if sp.GoldUSD != 3000.5 || sp.SilverUSD != 30.25 || sp.PlatinumUSD != 900 || sp.PalladiumUSD != 1000 {
		t.Errorf("unexpected spot: %+v", sp)
	}
	if sp.Source != "gold-api.com" {
		t.Errorf("source = %q, want gold-api.com", sp.Source)
	}
}

// A required metal (gold) missing → Fetch errors; optional metals missing → tolerated.
func TestGoldAPIPartial(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/price/XAU":
			w.Write([]byte(`{"price":3000}`))
		case "/price/XAG":
			w.Write([]byte(`{"price":30}`))
		default: // XPT/XPD → 404, optional
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	sp, err := NewGoldAPI(ts.URL, ts.Client()).Fetch(context.Background())
	if err != nil {
		t.Fatalf("optional metals should be tolerated: %v", err)
	}
	if sp.GoldUSD != 3000 || sp.SilverUSD != 30 || sp.PlatinumUSD != 0 {
		t.Errorf("unexpected partial spot: %+v", sp)
	}

	// Now make gold (required) fail → whole fetch errors.
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer ts2.Close()
	if _, err := NewGoldAPI(ts2.URL, ts2.Client()).Fetch(context.Background()); err == nil {
		t.Error("expected error when required metal fails")
	}
}

func TestByNameAndDisabled(t *testing.T) {
	if !Disabled("none") || !Disabled("manual") || Disabled("gold-api") {
		t.Error("Disabled() classification wrong")
	}
	if _, err := ByName("gold-api", nil); err != nil {
		t.Errorf("gold-api should resolve: %v", err)
	}
	if _, err := ByName("nonesuch", nil); err == nil {
		t.Error("unknown provider should error")
	}
}

// --- poller -------------------------------------------------------------------

// fakeProvider returns a fixed spot, or an error if errMsg is set.
type fakeProvider struct {
	spot   model.Spot
	errMsg string
	calls  int
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Fetch(context.Context) (model.Spot, error) {
	f.calls++
	if f.errMsg != "" {
		return model.Spot{}, errors.New(f.errMsg)
	}
	return f.spot, nil
}

// memSink is an in-memory Sink: PutSpot appends and LatestSpotObservation returns
// the chronologically latest raw observation, matching the store's freshness path.
type memSink struct{ spots []model.Spot }

func (m *memSink) PutSpot(s model.Spot) error { m.spots = append(m.spots, s); return nil }
func (m *memSink) LatestSpotObservation() (model.Spot, error) {
	var latest model.Spot
	for _, spot := range m.spots {
		if spot.AsOf > latest.AsOf {
			latest = spot
		}
	}
	return latest, nil
}

func newTestPoller(p Provider, sink Sink, interval time.Duration, now func() time.Time) *Poller {
	return &Poller{Provider: p, Sink: sink, Interval: interval, now: now, logf: func(string, ...any) {}}
}

func TestPollerStalenessGate(t *testing.T) {
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	now := base
	sink := &memSink{}
	prov := &fakeProvider{spot: model.Spot{GoldUSD: 4000, SilverUSD: 60}}
	p := newTestPoller(prov, sink, 6*time.Hour, func() time.Time { return now })

	// 1) Empty sink → stale → fetch + store.
	if !p.tick(context.Background()) {
		t.Fatal("first tick should store (sink empty)")
	}
	if len(sink.spots) != 1 {
		t.Fatalf("want 1 stored, got %d", len(sink.spots))
	}
	if _, ok := parseAsOf(sink.spots[0].AsOf); !ok {
		t.Errorf("stored as_of %q not RFC3339/date parseable", sink.spots[0].AsOf)
	}

	// 2) Same instant → not stale → no new store.
	if p.tick(context.Background()) || len(sink.spots) != 1 {
		t.Errorf("tick within interval should be a no-op; stored=%d", len(sink.spots))
	}

	// 3) Advance past the interval → stale → store again.
	now = base.Add(7 * time.Hour)
	if !p.tick(context.Background()) || len(sink.spots) != 2 {
		t.Errorf("tick after interval should store; stored=%d", len(sink.spots))
	}
}

func TestPollerFreshnessIgnoresSameDayValuationPreference(t *testing.T) {
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	sink := &memSink{spots: []model.Spot{
		{AsOf: "2026-07-23T08:00:00Z", GoldUSD: 4000, Source: "poller"},
		{AsOf: "2026-07-23", GoldUSD: 3950, Source: "manual"},
	}}
	prov := &fakeProvider{spot: model.Spot{GoldUSD: 4010, SilverUSD: 60}}
	p := newTestPoller(prov, sink, 6*time.Hour, func() time.Time { return now })

	if p.tick(context.Background()) {
		t.Fatal("same-day poller observation is fresh; manual valuation preference must not trigger a fetch")
	}
	if prov.calls != 0 || len(sink.spots) != 2 {
		t.Fatalf("freshness gate called provider %d times and stored %d rows", prov.calls, len(sink.spots))
	}
}

func TestPollerGracefulOnError(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	sink := &memSink{}
	prov := &fakeProvider{errMsg: "network down"}
	p := newTestPoller(prov, sink, time.Hour, func() time.Time { return now })

	if p.tick(context.Background()) {
		t.Error("tick should report no store on provider error")
	}
	if len(sink.spots) != 0 {
		t.Errorf("nothing should be stored on error, got %d", len(sink.spots))
	}
	if prov.calls != 1 {
		t.Errorf("provider should have been called once, got %d", prov.calls)
	}
}

func TestPollerRunStopsOnContextCancel(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	sink := &memSink{}
	prov := &fakeProvider{spot: model.Spot{GoldUSD: 4000}}
	p := newTestPoller(prov, sink, time.Hour, func() time.Time { return now })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
