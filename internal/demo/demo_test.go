package demo

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

func seedDataset(t *testing.T) model.Dataset {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	if err := Seed(s, now); err != nil {
		t.Fatal(err)
	}
	d, err := s.ResolveDataset()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The demo exists to look like a real hunter's books: field-data scale, a
// cash-positive hunt, an honest float, and every reporting surface populated.
func TestSeedShape(t *testing.T) {
	d := seedDataset(t)
	r := calc.Compute(d)

	// Scale: ~$40k face over ~500 buys (the field-data sizing from ADR-006).
	if r.FaceSearched < 25000 || r.FaceSearched > 60000 {
		t.Errorf("face searched $%.0f, want ~$40k (25k..60k)", r.FaceSearched)
	}
	if r.BuyCount < 350 || r.BuyCount > 800 {
		t.Errorf("buy count %v, want ~520 (350..800)", r.BuyCount)
	}
	if r.BranchCount < 8 {
		t.Errorf("branch count %v, want >= 8", r.BranchCount)
	}

	// The demo story: the hunt pays for itself.
	if r.CRHNetReal <= 0 {
		t.Errorf("CRH net $%.2f, want > 0", r.CRHNetReal)
	}
	// The float is honest: outstanding (last week's un-returned coin), not $0,
	// so the Return-to-bank workflow has work to do — but bounded.
	if r.ToRedeposit <= 0 || r.ToRedeposit > 2500 {
		t.Errorf("to_redeposit $%.2f, want positive and plausible (0..2500]", r.ToRedeposit)
	}
	// Reconciliation identity holds exactly (returns are derived).
	lost := 0.0
	for _, l := range d.Losses {
		lost += l.AmountUSD
	}
	if got := r.Buys - r.Returns - r.KeptFace - lost; math.Abs(got-r.ToRedeposit) > 0.01 {
		t.Errorf("identity: buys-returns-kept-lost = %.2f, to_redeposit = %.2f", got, r.ToRedeposit)
	}

	// Both trackers populated, both sides of realized P&L exercised.
	var bullion, crh, trophies int
	for _, l := range d.Lots {
		switch l.Activity {
		case "bullion":
			bullion++
		case "crh":
			crh++
		}
		if l.Trophy {
			trophies++
		}
	}
	if bullion < 4 || crh < 100 {
		t.Errorf("lots: %d bullion / %d crh, want >=4 / >=100", bullion, crh)
	}
	if trophies < 2 {
		t.Errorf("trophies %d, want >= 2 live trophy lots", trophies)
	}
	if len(d.Disposed) < 2 {
		t.Errorf("disposed lots %d, want >= 2 (rounds + Barbers sold)", len(d.Disposed))
	}
	if r.RealizedGain == 0 {
		t.Error("realized gain is zero, want nonzero realized P&L")
	}

	// Spot history backfilled (for valuation now + the over-time chart later).
	if d.Spot.SilverUSD < 40 || d.Spot.GoldUSD < 3000 {
		t.Errorf("latest spot Au=%.0f Ag=%.1f, want the seeded recent-ish prices", d.Spot.GoldUSD, d.Spot.SilverUSD)
	}

	// The whole report must survive JSON (guards the zero-basis +Inf regression).
	if _, err := json.Marshal(r); err != nil {
		t.Fatalf("report not JSON-serializable: %v", err)
	}
}

func TestSeedFindsReport(t *testing.T) {
	d := seedDataset(t)
	fr := calc.ComputeFindsReport(d)

	if len(fr.Denoms) < 3 {
		t.Fatalf("finds report covers %d denoms, want >= 3", len(fr.Denoms))
	}
	if fr.Unattributed != 0 {
		t.Errorf("unattributed finds %v, want 0 (every demo find links to its buy)", fr.Unattributed)
	}

	// The confidence signal should demo both states: solid cells and thin ones.
	var confident, thin int
	for _, dn := range fr.Denoms {
		for _, c := range dn.Categories {
			if c.Count == 0 {
				continue
			}
			if c.LowConfidence {
				thin++
			} else {
				confident++
			}
		}
	}
	if confident == 0 || thin == 0 {
		t.Errorf("hit-rate cells: %d confident / %d low-confidence, want both > 0", confident, thin)
	}
}

// Same now -> byte-identical dataset: the demo must not drift between runs.
func TestSeedDeterministic(t *testing.T) {
	a := seedDataset(t)
	b := seedDataset(t)
	ra, rb := calc.Compute(a), calc.Compute(b)
	if ra.FaceSearched != rb.FaceSearched || ra.BuyCount != rb.BuyCount || ra.CRHNetReal != rb.CRHNetReal {
		t.Errorf("non-deterministic seed: face %v vs %v, buys %v vs %v, net %v vs %v",
			ra.FaceSearched, rb.FaceSearched, ra.BuyCount, rb.BuyCount, ra.CRHNetReal, rb.CRHNetReal)
	}
}
