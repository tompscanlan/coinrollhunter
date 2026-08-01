// Package spot implements ADR-002's swappable `SpotProvider` seam and ADR-007's
// background poller. A Provider fetches current metals prices; the Poller periodically
// asks a Provider and appends the result to the spot history, staleness-gated and
// degrading gracefully so a fully offline run behaves like manual-only entry.
package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// Provider fetches current spot prices for the metals we value.
type Provider interface {
	Name() string
	Fetch(ctx context.Context) (model.Spot, error)
}

// Sink is the slice of the store the poller needs: read the chronologically latest
// stored observation (for the staleness gate) and append a new one. This is distinct
// from the valuation-facing latest spot, where a same-day manual correction wins.
// *store.Store satisfies this.
type Sink interface {
	LatestSpotObservation() (model.Spot, error)
	PutSpot(model.Spot) error
}

// Disabled reports whether a provider name means "don't poll" (manual entry only).
func Disabled(name string) bool {
	switch name {
	case "none", "off", "manual", "disabled":
		return true
	default:
		return false
	}
}

// ByName resolves a provider id to a Provider (ADR-002: swappable by config). Returns an
// error for unknown names; callers should check Disabled(name) first.
func ByName(name string, client *http.Client) (Provider, error) {
	switch name {
	case "", "gold-api", "gold-api.com":
		return NewGoldAPI("", client), nil
	default:
		return nil, fmt.Errorf("unknown spot provider %q (try gold-api, or none to disable)", name)
	}
}

// goldAPI is a keyless free-tier provider (api.gold-api.com), one small GET per metal.
// The response→Spot mapping is isolated here so swapping providers — or pointing at the
// future cached proxy (ADR-002) — is a config change, not a refactor.
type goldAPI struct {
	base   string
	client *http.Client
}

// NewGoldAPI builds the gold-api.com provider. base defaults to the public endpoint;
// client defaults to a 10s-timeout client. Both are injectable for tests.
func NewGoldAPI(base string, client *http.Client) Provider {
	if base == "" {
		base = "https://api.gold-api.com"
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return goldAPI{base: base, client: client}
}

func (g goldAPI) Name() string { return "gold-api.com" }

func (g goldAPI) Fetch(ctx context.Context) (model.Spot, error) {
	sp := model.Spot{Source: g.Name()}
	metals := []struct {
		code     string
		dst      *float64
		required bool
	}{
		{"XAU", &sp.GoldUSD, true},
		{"XAG", &sp.SilverUSD, true},
		{"XPT", &sp.PlatinumUSD, false},
		{"XPD", &sp.PalladiumUSD, false},
	}
	for _, m := range metals {
		price, err := g.price(ctx, m.code)
		if err != nil {
			if m.required {
				return model.Spot{}, fmt.Errorf("gold-api %s: %w", m.code, err)
			}
			continue // platinum/palladium optional — leave at 0
		}
		*m.dst = price
	}
	return sp, nil
}

// price fetches one metal's USD/ozt price. gold-api returns {"price": <float>, ...};
// JSON field matching is case-insensitive so "price"/"Price" both bind.
func (g goldAPI) price(ctx context.Context, code string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+"/price/"+code, nil)
	if err != nil {
		return 0, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	var body struct {
		Price float64 `json:"price"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	if body.Price <= 0 {
		return 0, fmt.Errorf("non-positive price for %s", code)
	}
	return body.Price, nil
}
