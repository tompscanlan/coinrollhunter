package calc

import (
	"math"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// sampleDataset is the committed fictional sample set (prototype/sample-data),
// already in resolved form (holdings joined to their item types). It is the
// shared fixture for the worked-example and invariant tests. No personal data.
func sampleDataset() model.Dataset {
	return model.Dataset{
		Spot: model.Spot{AsOf: "2026-01-01", GoldUSD: 4000, SilverUSD: 60},
		Settings: model.Settings{
			HourlyRateUSD:      25.00,
			IRSMileageRate:     0.70,
			SilverBuyback40pct: 0.80,
			SilverBuyback90pct: 0.90,
			BoxFaceUSD: map[string]float64{
				"halves": 500, "quarters": 500, "dimes": 250, "nickels": 100, "cents": 25,
			},
		},
		Lots: []model.Lot{
			// Bullion
			{ID: 1, Activity: "bullion", Product: "1 oz American Gold Eagle", Metal: "gold", Fineness: "22k .9167", Qty: 1, FineOzEach: 1.0, BasisUSD: 3950.00, Acquired: "2025-01-15"},
			{ID: 2, Activity: "bullion", Product: "$10 face 90% silver dimes", Metal: "silver", Fineness: "90%", Qty: 1, FineOzEach: 7.234, BasisUSD: 520.00, Acquired: "2025-02-01"},
			// CRH finds
			{ID: 3, Activity: "crh", Product: "Kennedy 40% silver halves", Metal: "silver", Fineness: "40%", Qty: 8, FineOzEach: 0.1479, BasisUSD: 4.00, Acquired: "2025-03-10"},
			{ID: 4, Activity: "crh", Product: "Washington 90% silver quarter", Metal: "silver", Fineness: "90%", Qty: 1, FineOzEach: 0.18084, BasisUSD: 0.25, Acquired: "2025-03-22"},
		},
		RollTxns: []model.RollTxn{
			{ID: 1, Date: "2025-03-08", Bank: "Sample Bank", Action: "buy", Denom: "halves", FaceUSD: 500.00},
			{ID: 2, Date: "2025-03-20", Bank: "Sample Bank", Action: "buy", Denom: "quarters", FaceUSD: 500.00},
			{ID: 3, Date: "2025-03-12", Bank: "Sample Bank", Action: "return", FaceUSD: 496.00},
			{ID: 4, Date: "2025-03-24", Bank: "Sample Bank", Action: "return", FaceUSD: 499.75},
		},
		Trips:    []model.Trip{{ID: 1, Date: "2025-03-08", Bank: "Sample Bank", Miles: 6, Hours: 0.5}},
		Supplies: []model.Supply{{ID: 1, Date: "2025-03-01", Item: "coin tubes", CostUSD: 8.00}},
		Keepers: []model.Keeper{
			{ID: 1, Denom: "halves", Count: 12, FaceUSD: 6.00},
			{ID: 2, Denom: "quarters", Count: 20, FaceUSD: 5.00},
		},
	}
}

const tol = 1e-6

func approx(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.6f, want %.6f (diff %.2e)", label, got, want, got-want)
	}
}

// TestSampleReport is the worked example: every expected value is derived inline
// from the sample fixture so the test documents the intended math. These are the
// *current* expected outputs — if the engine's math intentionally changes, update
// the arithmetic and the expectation here deliberately; nothing pins us to an
// external oracle. Cross-checked once against the prototype reference.
func TestSampleReport(t *testing.T) {
	r := Compute(sampleDataset())

	// Finds (silver), valued at spot $60/ozt:
	//   40% halves: 8 * 0.1479 = 1.1832 oz ; 90% quarter: 1 * 0.18084 = 0.18084 oz
	approx(t, "find_oz", r.FindOz, 1.1832+0.18084) // 1.36404
	approx(t, "find_cost", r.FindCost, 4.00+0.25)  // face paid = 4.25
	//   melt = oz * 60
	approx(t, "find_melt", r.FindMelt, (1.1832*60)+(0.18084*60)) // 81.8424
	//   realizable = melt * buyback (40%->0.80, 90%->0.90)
	approx(t, "find_realizable", r.FindRealizable, (1.1832*60*0.80)+(0.18084*60*0.90)) // 66.55896

	// Operating costs: gas = 6 mi * $0.70 ; supplies = $8
	approx(t, "gas", r.Gas, 6*0.70) // 4.20
	approx(t, "supplies", r.Supplies, 8.00)
	approx(t, "op_cost", r.OpCost, 4.20+8.00)

	// CRH net (the headline verdict input): realizable - face - op_cost
	approx(t, "crh_net_real", r.CRHNetReal, 66.55896-4.25-12.20) // 50.10896
	approx(t, "crh_net_melt", r.CRHNetMelt, 81.8424-4.25-12.20)  // 65.3924
	if r.Verdict() != "PROFITABLE (cash basis)" {
		t.Errorf("verdict = %q, want PROFITABLE (cash basis)", r.Verdict())
	}

	// Cash-in reconciliation:
	//   buys 500+500 ; returns 496+499.75 ; clad keepers 6+5 ; kept = clad + find face
	approx(t, "buys", r.Buys, 1000.00)
	approx(t, "returns", r.Returns, 995.75)
	approx(t, "clad_face", r.CladFace, 11.00)
	approx(t, "kept_face", r.KeptFace, 11.00+4.25)              // 15.25
	approx(t, "to_redeposit", r.ToRedeposit, 1000-995.75-15.25) // -11.00
	if r.Reconciled {
		t.Errorf("reconciled = true, want false (–$11.00 outstanding)")
	}

	// Box throughput, derived from face / box_face (ADR-001 R7):
	//   halves 500/500 = 1 ; quarters 500/500 = 1
	approx(t, "total_boxes", r.TotalBoxes, 2.0)
	approx(t, "boxes.halves", r.BoxesByDenom["halves"], 1.0)
	approx(t, "boxes.quarters", r.BoxesByDenom["quarters"], 1.0)

	// Bullion mark-to-market:
	//   gold 1oz*4000=4000 (basis 3950) ; dimes 7.234oz*60=434.04 (basis 520)
	approx(t, "gold_market", r.GoldMarket, 4000.00)
	approx(t, "bullion_basis", r.BullionBasis, 4470.00)
	approx(t, "bullion_market", r.BullionMarket, 4000.00+434.04)  // 4434.04
	approx(t, "bullion_unreal", r.BullionUnreal, 4434.04-4470.00) // -35.96

	// Whole portfolio: basis = bullion basis + find face ; market uses realizable
	approx(t, "total_basis", r.TotalBasis, 4470.00+4.25)
	approx(t, "total_market", r.TotalMarket, 4434.04+66.55896)
}

// TestInvariants asserts the accounting identities that must hold for ANY
// dataset, regardless of the specific numbers. These survive intentional changes
// to spot prices, haircuts, or fixtures — they encode what the math *means*.
func TestInvariants(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    model.Dataset
	}{
		{"sample", sampleDataset()},
		{"empty", model.Dataset{Settings: model.DefaultSettings(), Spot: model.Spot{GoldUSD: 4000, SilverUSD: 60}}},
		{"finds-only", model.Dataset{
			Settings: model.DefaultSettings(),
			Spot:     model.Spot{GoldUSD: 4000, SilverUSD: 60},
			Lots: []model.Lot{
				{Activity: "crh", Metal: "silver", Fineness: "90%", Qty: 3, FineOzEach: 0.18084, BasisUSD: 0.75},
			},
			RollTxns: []model.RollTxn{{Action: "buy", Denom: "dimes", FaceUSD: 250}},
			Keepers:  []model.Keeper{{Denom: "dimes", FaceUSD: 2.50}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Compute(tc.d)
			approx(t, "op_cost = gas+supplies", r.OpCost, r.Gas+r.Supplies)
			approx(t, "kept = clad+find_cost", r.KeptFace, r.CladFace+r.FindCost)
			approx(t, "to_redeposit = buys-returns-kept", r.ToRedeposit, r.Buys-r.Returns-r.KeptFace)
			approx(t, "crh_net_real = realizable-cost-op", r.CRHNetReal, r.FindRealizable-r.FindCost-r.OpCost)
			approx(t, "crh_net_melt = melt-cost-op", r.CRHNetMelt, r.FindMelt-r.FindCost-r.OpCost)
			approx(t, "bullion_unreal = market-basis", r.BullionUnreal, r.BullionMarket-r.BullionBasis)
			approx(t, "total_basis = bullion+find_cost", r.TotalBasis, r.BullionBasis+r.FindCost)
			approx(t, "total_market = bullion+realizable", r.TotalMarket, r.BullionMarket+r.FindRealizable)
			approx(t, "total_unreal = market-basis", r.TotalUnreal, r.TotalMarket-r.TotalBasis)
			approx(t, "face_searched = buys", r.FaceSearched, r.Buys)

			// A dealer haircut can never make finds worth more than full melt.
			if r.FindRealizable > r.FindMelt+tol {
				t.Errorf("find_realizable %.4f > find_melt %.4f", r.FindRealizable, r.FindMelt)
			}
			// Reconciled iff the outstanding float is within a cent.
			if got := math.Abs(r.ToRedeposit) < 0.01; got != r.Reconciled {
				t.Errorf("reconciled = %v, but |to_redeposit|<0.01 = %v", r.Reconciled, got)
			}
			// Verdict must agree with the sign of the cash-basis net.
			switch {
			case r.CRHNetReal > 0 && r.Verdict() != "PROFITABLE (cash basis)":
				t.Errorf("net %.2f >0 but verdict %q", r.CRHNetReal, r.Verdict())
			case r.CRHNetReal < 0 && r.Verdict() != "COSTING MONEY":
				t.Errorf("net %.2f <0 but verdict %q", r.CRHNetReal, r.Verdict())
			}
		})
	}
}
