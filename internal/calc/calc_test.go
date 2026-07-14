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
			// Both buys at one branch (branch_id 1) → branch_count = 1 (ADR-010: distinct branch_id).
			{ID: 1, Date: "2025-03-08", Bank: "Sample Bank", BranchID: 1, Action: "buy", Denom: "halves", FaceUSD: 500.00},
			{ID: 2, Date: "2025-03-20", Bank: "Sample Bank", BranchID: 1, Action: "buy", Denom: "quarters", FaceUSD: 500.00},
			{ID: 3, Date: "2025-03-12", Bank: "Sample Bank", BranchID: 1, Action: "return", FaceUSD: 496.00},
			{ID: 4, Date: "2025-03-24", Bank: "Sample Bank", BranchID: 1, Action: "return", FaceUSD: 499.75},
		},
		Trips:    []model.Trip{{ID: 1, Date: "2025-03-08", Bank: "Sample Bank", BranchID: 1, Miles: 6, Hours: 0.5}},
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

// TestLossReconciliation is the worked example for ADR-005: booking the
// unaccounted float as a loss drives to_redeposit to $0 and reduces CRH net by
// exactly the loss amount (a loss is a real cash cost, not a free reset).
func TestLossReconciliation(t *testing.T) {
	// $500 halves bought, $480 returned, $15 clad kept, one 90% silver find
	// (face $0.50). Accounted = 480 + 15 + 0.50 = 495.50 → $4.50 unaccounted.
	base := model.Dataset{
		Spot:     model.Spot{SilverUSD: 60},
		Settings: model.DefaultSettings(),
		Lots: []model.Lot{
			{Activity: "crh", Metal: "silver", Fineness: "90%", Qty: 2, FineOzEach: 0.18084, BasisUSD: 0.50},
		},
		RollTxns: []model.RollTxn{
			{Action: "buy", Denom: "halves", FaceUSD: 500},
			{Action: "return", FaceUSD: 480},
		},
		Keepers: []model.Keeper{{Denom: "halves", FaceUSD: 15}},
	}

	// Before reconcile: $4.50 still "outstanding", not reconciled.
	before := Compute(base)
	approx(t, "before to_redeposit", before.ToRedeposit, 4.50)
	if before.Reconciled {
		t.Errorf("before reconciled = true, want false ($4.50 outstanding)")
	}

	// Book the $4.50 as a loss (the Reconcile action).
	withLoss := base
	withLoss.Losses = []model.Loss{{Date: "2026-06-29", AmountUSD: 4.50, Reason: "machine miscount"}}
	after := Compute(withLoss)

	approx(t, "losses", after.Losses, 4.50)
	approx(t, "after to_redeposit", after.ToRedeposit, 0.00)
	if !after.Reconciled {
		t.Errorf("after reconciled = false, want true (float closed)")
	}
	// CRH net drops by exactly the loss; nothing else changed.
	approx(t, "crh_net drops by the loss", after.CRHNetReal, before.CRHNetReal-4.50)
	approx(t, "op_cost unchanged (loss is its own line)", after.OpCost, before.OpCost)
}

// TestDisposedFindStaysKept is the headline pin for om-co69 (decision (c)):
// selling a CRH find must NOT change keptFace or to_redeposit — the sold find's
// face stays on the kept side of the float permanently — while CRH net and total
// basis MUST NOT absorb the disposed find's basis (they stay live-only; a
// disposed find's P&L is realized separately). It compares three datasets that
// differ only in where one CRH find sits: live inventory, sold, or absent.
func TestDisposedFindStaysKept(t *testing.T) {
	const findBasis = 5.00 // the CRH find's face/basis dollars

	// build makes the base dataset and places the one find where `where` says:
	// "live" -> a live crh Lot; "sold" -> a disposed crh lot; "absent" -> neither.
	build := func(where string) model.Dataset {
		d := model.Dataset{
			Spot:     model.Spot{GoldUSD: 4000, SilverUSD: 60},
			Settings: model.DefaultSettings(),
			Lots: []model.Lot{
				{Activity: "bullion", Metal: "gold", Qty: 1, FineOzEach: 1.0, BasisUSD: 1000.00},
			},
			RollTxns: []model.RollTxn{
				{Action: "buy", Denom: "halves", FaceUSD: 500},
				{Action: "return", FaceUSD: 400},
			},
			Keepers: []model.Keeper{{Denom: "halves", FaceUSD: 20}},
		}
		switch where {
		case "live":
			d.Lots = append(d.Lots, model.Lot{
				Activity: "crh", Metal: "silver", Fineness: "90%", Qty: 1, FineOzEach: 0.18084, BasisUSD: findBasis,
			})
		case "sold":
			d.Disposed = []model.DisposedLot{
				{Activity: "crh", Metal: "silver", Qty: 1, BasisUSD: findBasis, ProceedsUSD: 8.00, Disposed: "2026-05-01"},
			}
		case "absent":
			// nothing
		}
		return d
	}

	live := Compute(build("live"))
	sold := Compute(build("sold"))
	absent := Compute(build("absent"))

	// AC1 — sold-find face stays kept: selling the find does NOT move keptFace or
	// to_redeposit, and keptFace includes the find's basis exactly once.
	approx(t, "keptFace: sold == live (sale doesn't change it)", sold.KeptFace, live.KeptFace)
	approx(t, "to_redeposit: sold == live (sale doesn't reopen the float)", sold.ToRedeposit, live.ToRedeposit)
	approx(t, "keptFace rose by exactly the find basis vs absent", sold.KeptFace, absent.KeptFace+findBasis)
	approx(t, "to_redeposit fell by exactly the find basis vs absent", sold.ToRedeposit, absent.ToRedeposit-findBasis)
	approx(t, "disposed_find_face == the sold find's basis", sold.DisposedFindFace, findBasis)
	// counted exactly once: for the sold dataset there is no live find, so all of
	// the kept find face comes from the disposed side.
	approx(t, "sold: FindCost is live-only (0 here)", sold.FindCost, 0)
	approx(t, "sold: keptFindFace = live FindCost + disposed face", sold.KeptFace, sold.CladFace+sold.FindCost+sold.DisposedFindFace)

	// AC2 — CRH net + total basis MUST NOT MOVE: a disposed find's basis must not
	// leak into CRH net or total basis. Byte-identical to the "find absent" case
	// (the live-only formula), which is the strongest no-move pin: if fCost were
	// silently widened to include disposed basis, these would drift.
	approx(t, "crh_net_melt unchanged by the sale", sold.CRHNetMelt, absent.CRHNetMelt)
	approx(t, "crh_net_real unchanged by the sale", sold.CRHNetReal, absent.CRHNetReal)
	approx(t, "crh_net_time unchanged by the sale", sold.CRHNetTime, absent.CRHNetTime)
	approx(t, "total_basis unchanged by the sale", sold.TotalBasis, absent.TotalBasis)
	// And they equal the live-only formula recomputed from the sold report fields.
	approx(t, "sold crh_net_real = realizable-cost-op-loss (live-only)", sold.CRHNetReal, sold.FindRealizable-sold.FindCost-sold.OpCost-sold.Losses)
	approx(t, "sold total_basis = bullion+find_cost (live-only)", sold.TotalBasis, sold.BullionBasis+sold.FindCost)
}

// TestCRHNetLifetime is the worked example for om-nass: the LIVE headline
// (crh_net_real) legitimately reads a loss the moment you sell a winning find —
// the find leaves the live set, its realizable value goes with it, and the op
// costs of the hunt that produced it remain. That is not a bug to "fix" by
// widening fCost (ADR-008 §Alternatives rejects exactly that); the answer is a
// SECOND figure, crh_net_lifetime = crh_net_real + realized_gain_crh, which
// folds the separately-realized CRH P&L back in.
//
// The bead's example: a 90% silver find, $0.50 face, sold for $90, against $20
// of logged op costs. Live: −$20. Lifetime: +$69.50.
func TestCRHNetLifetime(t *testing.T) {
	// A sold CRH find and nothing else live. op_cost comes from a supply row
	// (gas is derived from trips × mileage, so a supply is the clean $20).
	sold := model.Dataset{
		Spot:     model.Spot{SilverUSD: 60},
		Settings: model.DefaultSettings(),
		Supplies: []model.Supply{{Date: "2026-04-01", Item: "coin tubes", CostUSD: 20.00}},
		Disposed: []model.DisposedLot{
			{Activity: "crh", Metal: "silver", Qty: 1, BasisUSD: 0.50, ProceedsUSD: 90.00, Disposed: "2026-05-01"},
		},
	}
	r := Compute(sold)

	// The find is sold, so the LIVE find terms are empty.
	approx(t, "find_cost (live-only)", r.FindCost, 0)
	approx(t, "find_realizable (live-only)", r.FindRealizable, 0)
	approx(t, "op_cost", r.OpCost, 20.00)
	approx(t, "losses", r.Losses, 0)

	// The defect symptom, preserved deliberately: the live figure reads a $20
	// loss. crh_net_real MUST stay live-only (ADR-008 (c) / om-co69).
	approx(t, "crh_net_real is live-only (reads the op-cost loss)", r.CRHNetReal, -20.00)

	// The sold find's P&L is realized separately — and now aggregated by activity.
	approx(t, "realized_gain_crh = proceeds - basis", r.RealizedGainCRH, 89.50)
	approx(t, "realized_gain_bullion (none disposed)", r.RealizedGainBullion, 0)
	approx(t, "realized_gain = crh + bullion", r.RealizedGain, 89.50)

	// The lifetime figure restores the truth: the hunt made money.
	approx(t, "crh_net_lifetime = live + realized crh", r.CRHNetLifetime, 69.50)

	// Emit the headline figures so `go test -v` carries the worked example in its
	// output — the flip this bead is about is legible without reading the source.
	t.Logf("om-nass worked example: crh_net_real = %.2f · realized_gain_crh = %.2f · crh_net_lifetime = %.2f",
		r.CRHNetReal, r.RealizedGainCRH, r.CRHNetLifetime)

	// D4: Verdict() stays keyed on the LIVE figure — unchanged by this bead.
	if r.Verdict() != "COSTING MONEY" {
		t.Errorf("verdict = %q, want COSTING MONEY (Verdict keys off crh_net_real)", r.Verdict())
	}

	// AC6 — bullion realized gain is SEPARATE and never enters the CRH lifetime
	// figure. Same dataset, but the disposed lot is bullion: crh_net_lifetime is
	// EXACTLY crh_net_real (nothing added), while realized_gain still moves.
	bullionSale := sold
	bullionSale.Disposed = []model.DisposedLot{
		{Activity: "bullion", Metal: "gold", Qty: 1, BasisUSD: 3950.00, ProceedsUSD: 4200.00, Disposed: "2026-05-01"},
	}
	b := Compute(bullionSale)
	approx(t, "bullion sale: realized_gain_bullion", b.RealizedGainBullion, 250.00)
	approx(t, "bullion sale: realized_gain_crh stays 0", b.RealizedGainCRH, 0)
	if b.CRHNetLifetime != b.CRHNetReal {
		t.Errorf("bullion sale moved crh_net_lifetime: lifetime %.6f != live %.6f — bullion P&L must never enter the CRH figure",
			b.CRHNetLifetime, b.CRHNetReal)
	}
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
		{"with-loss", model.Dataset{
			Settings: model.DefaultSettings(),
			Spot:     model.Spot{GoldUSD: 4000, SilverUSD: 60},
			Lots: []model.Lot{
				{Activity: "crh", Metal: "silver", Fineness: "90%", Qty: 2, FineOzEach: 0.18084, BasisUSD: 0.50},
			},
			RollTxns: []model.RollTxn{{Action: "buy", Denom: "halves", FaceUSD: 500}, {Action: "return", FaceUSD: 480}},
			Keepers:  []model.Keeper{{Denom: "halves", FaceUSD: 15}},
			Losses:   []model.Loss{{Date: "2026-06-29", AmountUSD: 3.00, Reason: "machine miscount"}},
		}},
		// A dataset with BOTH a live find and a sold (disposed) CRH find, so the
		// kept-face identity is exercised across live + disposed (om-co69). The
		// disposed find's basis must show up in DisposedFindFace / KeptFace but not
		// in FindCost (live-only).
		{"disposed-find", model.Dataset{
			Settings: model.DefaultSettings(),
			Spot:     model.Spot{GoldUSD: 4000, SilverUSD: 60},
			Lots: []model.Lot{
				{Activity: "crh", Metal: "silver", Fineness: "90%", Qty: 1, FineOzEach: 0.18084, BasisUSD: 0.25},
			},
			Disposed: []model.DisposedLot{
				{Activity: "crh", Metal: "silver", Qty: 3, BasisUSD: 0.75, ProceedsUSD: 12.00, Disposed: "2026-05-01"},
			},
			RollTxns: []model.RollTxn{{Action: "buy", Denom: "halves", FaceUSD: 500}, {Action: "return", FaceUSD: 480}},
			Keepers:  []model.Keeper{{Denom: "halves", FaceUSD: 15}},
		}},
		// om-nass: disposed lots of every activity flavour at once — a crh find, a
		// bullion lot, and one whose activity is BLANK/unknown (a legacy or hand-
		// imported row). The realized split is "crh vs everything else", so the
		// blank one lands in realized_gain_bullion and the AC3 identity
		// (realized_gain == crh + bullion) stays EXACT rather than leaking a lot.
		{"disposed-mixed-activities", model.Dataset{
			Settings: model.DefaultSettings(),
			Spot:     model.Spot{GoldUSD: 4000, SilverUSD: 60},
			Lots: []model.Lot{
				{Activity: "crh", Metal: "silver", Fineness: "90%", Qty: 1, FineOzEach: 0.18084, BasisUSD: 0.25},
			},
			Disposed: []model.DisposedLot{
				{Activity: "crh", Metal: "silver", Qty: 1, BasisUSD: 0.50, ProceedsUSD: 90.00, Disposed: "2026-05-01"},
				{Activity: "bullion", Metal: "gold", Qty: 1, BasisUSD: 3950.00, ProceedsUSD: 4200.00, Disposed: "2026-05-02"},
				{Activity: "", Metal: "silver", Qty: 1, BasisUSD: 10.00, ProceedsUSD: 7.50, Disposed: "2026-05-03"},
			},
			RollTxns: []model.RollTxn{{Action: "buy", Denom: "halves", FaceUSD: 500}, {Action: "return", FaceUSD: 480}},
			Supplies: []model.Supply{{Item: "coin tubes", CostUSD: 20.00}},
			Keepers:  []model.Keeper{{Denom: "halves", FaceUSD: 15}},
		}},
		// om-nass / AC6: ONLY a bullion sale. The CRH lifetime figure must not move
		// at all — a bullion sale is not a hunt result.
		{"disposed-bullion-only", model.Dataset{
			Settings: model.DefaultSettings(),
			Spot:     model.Spot{GoldUSD: 4000, SilverUSD: 60},
			Lots: []model.Lot{
				{Activity: "crh", Metal: "silver", Fineness: "90%", Qty: 2, FineOzEach: 0.18084, BasisUSD: 0.50},
			},
			Disposed: []model.DisposedLot{
				{Activity: "bullion", Metal: "gold", Qty: 1, BasisUSD: 3950.00, ProceedsUSD: 4200.00, Disposed: "2026-05-01"},
			},
			RollTxns: []model.RollTxn{{Action: "buy", Denom: "halves", FaceUSD: 500}, {Action: "return", FaceUSD: 480}},
			Keepers:  []model.Keeper{{Denom: "halves", FaceUSD: 15}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Compute(tc.d)
			approx(t, "op_cost = gas+supplies", r.OpCost, r.Gas+r.Supplies)
			// kept-face identity (om-co69): kept = clad + live-find-cost +
			// disposed-find-face. Spans the live set (FindCost) AND sold CRH finds
			// (DisposedFindFace); collapses to clad+find_cost when nothing is disposed.
			approx(t, "kept = clad+find_cost+disposed_find_face", r.KeptFace, r.CladFace+r.FindCost+r.DisposedFindFace)
			approx(t, "to_redeposit = buys-returns-kept-lost", r.ToRedeposit, r.Buys-r.Returns-r.KeptFace-r.Losses)
			approx(t, "crh_net_real = realizable-cost-op-loss", r.CRHNetReal, r.FindRealizable-r.FindCost-r.OpCost-r.Losses)
			approx(t, "crh_net_melt = melt-cost-op-loss", r.CRHNetMelt, r.FindMelt-r.FindCost-r.OpCost-r.Losses)
			// om-nass: the lifetime CRH figure folds the separately-realized CRH P&L
			// back into the live one — and NOTHING else. Bullion realized gain stays
			// out (that's a separate investment, ADR-005's "bullion is a separate
			// long-term hold"), so the two identities below are what keep them apart.
			approx(t, "crh_net_lifetime = crh_net_real + realized_gain_crh", r.CRHNetLifetime, r.CRHNetReal+r.RealizedGainCRH)
			approx(t, "realized_gain = realized_gain_crh + realized_gain_bullion", r.RealizedGain, r.RealizedGainCRH+r.RealizedGainBullion)
			// No disposed CRH find ⇒ the lifetime figure IS the live figure, exactly.
			if r.RealizedGainCRH == 0 && r.CRHNetLifetime != r.CRHNetReal {
				t.Errorf("no realized crh gain, but crh_net_lifetime %.6f != crh_net_real %.6f", r.CRHNetLifetime, r.CRHNetReal)
			}
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

// --- om-t0fs: classification of free-text metal / fineness -------------------
//
// The money math used to key off raw human text: buybackFactor prefix-matched the
// fineness string and spotFor did an exact, case-sensitive switch on the metal.
// Both failed SILENTLY (full melt payout / $0 spot). These tables pin the parse.

// TestBuybackFactorClassification is AC1/AC2/AC3: the fineness is normalized to a
// numeric fine-fraction and classified by RANGE — no prefix matching — and the
// metal guard folds case/whitespace like spotFor does.
func TestBuybackFactorClassification(t *testing.T) {
	s := model.DefaultSettings() // 40% -> 0.80, 90% -> 0.90
	const (
		f90   = 0.90 // SilverBuyback90pct
		f40   = 0.80 // SilverBuyback40pct (war nickels lump in here too)
		none  = 1.00 // no dealer haircut
		worst = 0.80 // the conservative branch: min(40pct, 90pct, 1.0)
	)
	for _, tc := range []struct {
		name     string
		metal    string
		fineness string
		want     float64
	}{
		// AC1 — every notation for 90% junk silver takes the 90% haircut.
		{"90 pct sign", "silver", "90%", f90},
		{"90 bare", "silver", "90", f90},
		{"90 point-nine-hundred", "silver", ".900", f90},
		{"90 zero-point-nine-hundred", "silver", "0.900", f90},
		{"90 padded pct", "silver", " 90% ", f90},
		{"90 per-mille", "silver", "900", f90},
		// AC1 — 40% junk silver.
		{"40 pct sign", "silver", "40%", f40},
		{"40 point-four-hundred", "silver", ".400", f40},
		{"40 padded pct", "silver", " 40% ", f40},
		// AC1 — war nickels stay lumped with the 40% haircut (deliberate).
		{"35 war nickel pct", "silver", "35%", f40},
		{"35 war nickel decimal", "silver", ".350", f40},
		// AC2 — a weight is NOT a fineness: no 40% haircut off "40".
		{"40 grain is a weight", "silver", "40 grain", worst},
		{"90 grain is a weight", "silver", "90 grain", worst},
		{"pure gibberish", "silver", "junk", worst},
		// Review finding 2 — a DECIMAL-marked weight is still a weight, not junk.
		{"0.4 oz is a weight", "silver", "0.4 oz", worst},
		{"point-9 oz is a weight", "silver", ".9 oz", worst},
		{"point-4 g is a weight", "silver", ".4 g", worst},
		{"0.4 oz round is a weight", "silver", "0.4 oz round", worst},
		// Review finding 4 — NaN must not classify as a parseable fraction (1.0).
		{"nan% is not a fineness", "silver", "nan%", worst},
		// Review boundary — a marked fineness with NON-unit annotation still parses.
		{"point-900 with annotation", "silver", ".900 coin", f90},
		{"90% with parenthetical note", "silver", "90% (worn)", f90},
		// AC3 — the metal guard normalizes: a 'Silver' lot reaches the haircut.
		{"capitalized Silver 90%", "Silver", "90%", f90},
		{"shouty SILVER 90%", "SILVER", ".900", f90},
		{"padded silver 40%", " silver ", "40%", f40},
		{"capitalized Silver, bad fineness", "Silver", "40 grain", worst},
		// Non-junk but perfectly parseable finenesses keep full melt (unchanged).
		{"999 bullion round", "silver", ".999", none},
		{"9999 bullion", "silver", ".9999", none},
		{"sterling", "silver", ".925", none},
		{"world 80% with annotation", "silver", "80% (CAD)", none},
		{"blank fineness is not stated, not corrupt", "silver", "", none},
		// Non-silver never takes a silver haircut, whatever the fineness says.
		{"gold 22k", "gold", "22k .9167", none},
		{"gold that reads like 90", "gold", "90%", none},
		{"blank metal", "", "90%", none},
		{"unknown metal", "rhodium", "90%", none},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := model.Lot{Activity: "crh", Metal: tc.metal, Fineness: tc.fineness, Qty: 1, FineOzEach: 1}
			if got := buybackFactor(l, s); math.Abs(got-tc.want) > tol {
				t.Errorf("buybackFactor(metal=%q, fineness=%q) = %.4f, want %.4f",
					tc.metal, tc.fineness, got, tc.want)
			}
		})
	}
}

// TestSpotForNormalizesMetal is AC3/AC4: case and surrounding whitespace fold to
// the right spot price; a BLANK metal still values at $0 (legal, load-bearing —
// clad junk carries no melt metal); an unknown metal still values at $0.
func TestSpotForNormalizesMetal(t *testing.T) {
	s := model.Spot{GoldUSD: 4000, SilverUSD: 60, PlatinumUSD: 1000, PalladiumUSD: 900}
	for _, tc := range []struct {
		metal string
		want  float64
	}{
		{"gold", 4000}, {"Gold", 4000}, {"GOLD", 4000}, {" gold ", 4000},
		{"silver", 60}, {"Silver", 60}, {"SILVER", 60}, {" silver ", 60},
		{"platinum", 1000}, {"Platinum", 1000}, {"PLATINUM", 1000}, {" platinum ", 1000},
		{"palladium", 900}, {"Palladium", 900}, {"PALLADIUM", 900}, {" palladium ", 900},
		{"", 0},           // AC4: blank is legal — $0, silently
		{"   ", 0},        // whitespace-only is blank
		{"rhodium", 0},    // unknown metal: no price column
		{"silver-ish", 0}, // not a metal we price
	} {
		t.Run("metal="+tc.metal, func(t *testing.T) {
			if got := spotFor(s, tc.metal); got != tc.want {
				t.Errorf("spotFor(%q) = %.2f, want %.2f", tc.metal, got, tc.want)
			}
		})
	}
}

// TestFinenessFraction pins the normalizer itself: free text in, a numeric fine
// fraction (or "unparseable") out. The classification above is a RANGE test on
// this number, which is why "40 grain" must not yield 0.40.
func TestFinenessFraction(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  float64
		class fineClass
	}{
		{"90%", 0.90, fineParsed},
		{"90", 0.90, fineParsed},
		{".900", 0.900, fineParsed},
		{"0.900", 0.900, fineParsed},
		{" 90% ", 0.90, fineParsed},
		{"900", 0.900, fineParsed},
		{"40%", 0.40, fineParsed},
		{".400", 0.400, fineParsed},
		{" 40% ", 0.40, fineParsed},
		{"35%", 0.35, fineParsed},
		{".350", 0.350, fineParsed},
		{".999", 0.999, fineParsed},
		{"9999", 0.9999, fineParsed},
		{"22k .9167", 22.0 / 24.0, fineParsed},
		{"80% (CAD)", 0.80, fineParsed},
		{"90% (pre-1965)", 0.90, fineParsed},
		{".900 coin", 0.900, fineParsed}, // annotation after a marked number
		{"90% (worn)", 0.90, fineParsed},
		{"", 0, fineUnstated},
		{"   ", 0, fineUnstated},
		{"40 grain", 0, fineUnparseable},
		{"junk", 0, fineUnparseable},
		{"-", 0, fineUnparseable},
		{"900%", 0, fineUnparseable}, // 9.0 fine is not a thing
		{"0", 0, fineUnparseable},
		// om-t0fs review finding 2 — a decimal-MARKED weight is still a weight: the
		// unit word must not be discarded as annotation and the number bucketed.
		{"0.4 oz", 0, fineUnparseable},
		{".9 oz", 0, fineUnparseable},
		{".4 g", 0, fineUnparseable},
		{"0.4 oz round", 0, fineUnparseable}, // unit word even with more text after
		{".9 oz bar", 0, fineUnparseable},
		{"5 grams", 0, fineUnparseable},
		{"90 gr", 0, fineUnparseable},
		// om-t0fs review finding 4 — NaN/Inf must not slip past the range check.
		{"nan%", 0, fineUnparseable},
		{"inf", 0, fineUnparseable},
	} {
		t.Run("fineness="+tc.in, func(t *testing.T) {
			got, class := finenessFraction(tc.in)
			if class != tc.class {
				t.Fatalf("finenessFraction(%q) class = %v, want %v", tc.in, class, tc.class)
			}
			if class == fineParsed && math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("finenessFraction(%q) = %.6f, want %.6f", tc.in, got, tc.want)
			}
		})
	}
}

// TestDoubleWhammyCapitalMetal is the headline defect: a historical/imported row
// with metal="Silver" used to miss BOTH halves of the money math — $0 spot from
// spotFor AND no dealer haircut from buybackFactor. It must now compute exactly
// like its lowercase twin.
func TestDoubleWhammyCapitalMetal(t *testing.T) {
	build := func(metal string) model.Dataset {
		return model.Dataset{
			Spot:     model.Spot{SilverUSD: 60},
			Settings: model.DefaultSettings(),
			Lots: []model.Lot{
				{ID: 1, Activity: "crh", Metal: metal, Fineness: "90%", Qty: 10, FineOzEach: 0.18084, BasisUSD: 2.50},
			},
		}
	}
	lower := Compute(build("silver"))
	upper := Compute(build("Silver"))

	approx(t, "find_melt: 'Silver' values at silver spot", upper.FindMelt, 10*0.18084*60) // 108.504
	approx(t, "find_melt matches the lowercase twin", upper.FindMelt, lower.FindMelt)
	approx(t, "find_realizable takes the 90% haircut", upper.FindRealizable, 10*0.18084*60*0.90)
	approx(t, "find_realizable matches the lowercase twin", upper.FindRealizable, lower.FindRealizable)
	approx(t, "crh_net_real matches the lowercase twin", upper.CRHNetReal, lower.CRHNetReal)
	if len(upper.Anomalies) != 0 {
		t.Errorf("a resolvable metal is not an anomaly, got %+v", upper.Anomalies)
	}
}

// TestClassificationAnomalies is AC5: the additive Report field records the rows
// whose classification did not resolve — and AC4: a BLANK metal is NOT one of
// them. Blank is legal (clad junk carries no melt metal); flagging it would fire
// on correct data across a huge share of the ledger.
func TestClassificationAnomalies(t *testing.T) {
	d := model.Dataset{
		Spot:     model.Spot{GoldUSD: 4000, SilverUSD: 60},
		Settings: model.DefaultSettings(),
		Lots: []model.Lot{
			// clean rows — no anomaly
			{ID: 1, Activity: "bullion", Product: "Gold Eagle", Metal: "gold", Fineness: "22k .9167", Qty: 1, FineOzEach: 1},
			{ID: 2, Activity: "crh", Product: "90% quarter", Metal: "Silver", Fineness: ".900", Qty: 1, FineOzEach: 0.18084},
			// AC4: blank metal is LEGAL — $0 melt, and NO anomaly.
			{ID: 3, Activity: "crh", Product: "1972 error cent", Metal: "", Fineness: "", Qty: 1, FineOzEach: 0},
			// (a) non-blank metal that resolves to no known spot metal
			{ID: 4, Activity: "bullion", Product: "mystery round", Metal: "rhodium", Fineness: ".999", Qty: 1, FineOzEach: 1},
			// (b) unparseable fineness on a silver lot
			{ID: 5, Activity: "crh", Product: "mislabelled half", Metal: "silver", Fineness: "40 grain", Qty: 1, FineOzEach: 0.1479},
		},
	}
	r := Compute(d)

	if len(r.Anomalies) != 2 {
		t.Fatalf("got %d anomalies, want 2 (rhodium metal + '40 grain' fineness); %+v", len(r.Anomalies), r.Anomalies)
	}
	for _, a := range r.Anomalies {
		if a.LotID == 3 {
			t.Errorf("blank metal was flagged — it is LEGAL and must stay silent: %+v", a)
		}
	}
	metal, fine := r.Anomalies[0], r.Anomalies[1]
	if metal.LotID != 4 || metal.Field != "metal" || metal.Value != "rhodium" {
		t.Errorf("metal anomaly = %+v, want lot 4 / field metal / value rhodium", metal)
	}
	if metal.Product != "mystery round" {
		t.Errorf("anomaly must identify the offending row: product = %q", metal.Product)
	}
	if fine.LotID != 5 || fine.Field != "fineness" || fine.Value != "40 grain" {
		t.Errorf("fineness anomaly = %+v, want lot 5 / field fineness / value '40 grain'", fine)
	}
	if metal.Detail == "" || fine.Detail == "" {
		t.Error("every anomaly needs a human-readable detail")
	}

	// AC6 — the unparseable fineness took the CONSERVATIVE branch, not full melt.
	// Lot 5 melt = 0.1479 * 60 = 8.874; realizable must be the worst known haircut
	// (0.80), NOT 1.0. Lot 2 (90%) contributes its own 0.90 haircut.
	melt5, melt2 := 0.1479*60, 0.18084*60
	approx(t, "unparseable fineness takes the worst known haircut", r.FindRealizable, melt2*0.90+melt5*0.80)
	if r.FindRealizable >= r.FindMelt {
		t.Error("an unparseable fineness must not silently pay full melt (the most flattering answer)")
	}
}

// TestSampleReportNoAnomalies is AC7's other half: the clean committed fixture
// must produce ZERO anomalies — no false positives on correct data.
func TestSampleReportNoAnomalies(t *testing.T) {
	if a := Compute(sampleDataset()).Anomalies; len(a) != 0 {
		t.Errorf("clean sample fixture produced anomalies: %+v", a)
	}
}

// TestWeightUnitFinenessIsAnomaly closes om-t0fs review findings 2 and 4. A
// decimal point used to flip a WEIGHT string into "marked number, discard the
// unit" — so "0.4 oz" silently took a 40% junk haircut (main returned 1.0), no
// anomaly. And a NaN slipped past the range check as a parsed fraction -> 1.0. Both
// are the silent-flattering-garbage failure mode this whole bead is about: each
// must now take the CONSERVATIVE factor AND raise a fineness anomaly, exactly like
// "40 grain".
func TestWeightUnitFinenessIsAnomaly(t *testing.T) {
	s := model.DefaultSettings()
	worst := math.Min(1.0, math.Min(s.SilverBuyback40pct, s.SilverBuyback90pct)) // 0.80
	for _, fineness := range []string{"0.4 oz", ".9 oz", ".4 g", "0.4 oz round", ".9 oz bar", "5 grams", "90 gr", "nan%"} {
		t.Run("fineness="+fineness, func(t *testing.T) {
			l := model.Lot{ID: 7, Activity: "crh", Product: "mislabelled find", Metal: "silver", Fineness: fineness, Qty: 1, FineOzEach: 0.1}
			if got := buybackFactor(l, s); math.Abs(got-worst) > tol {
				t.Errorf("buybackFactor(fineness=%q) = %.4f, want conservative %.4f (a weight/NaN is not junk silver)", fineness, got, worst)
			}
			as := Compute(model.Dataset{Spot: model.Spot{SilverUSD: 60}, Settings: s, Lots: []model.Lot{l}}).Anomalies
			if len(as) != 1 || as[0].Field != "fineness" || as[0].Value != fineness {
				t.Errorf("fineness=%q anomalies = %+v, want exactly one fineness anomaly carrying that value", fineness, as)
			}
		})
	}
}

// TestMarkedFinenessAnnotationStillParses is the over-rejection boundary the
// reviewer flagged for finding 2: the unit-word rejection must catch weight units
// WITHOUT eating a legitimate marked fineness whose trailing token is ordinary
// annotation. These must still parse, bucket normally, and raise NO anomaly.
func TestMarkedFinenessAnnotationStillParses(t *testing.T) {
	s := model.DefaultSettings()
	for _, tc := range []struct {
		fineness string
		want     float64
	}{
		{".900 coin", 0.90},  // annotation, not a unit
		{"90% (worn)", 0.90}, // parenthetical note
		{"80% (CAD)", 1.00},  // world coin, outside the junk buckets
		{"22k .9167", 1.00},  // karat; .9167 is outside the 90% band -> full melt
	} {
		t.Run("fineness="+tc.fineness, func(t *testing.T) {
			l := model.Lot{Activity: "crh", Metal: "silver", Fineness: tc.fineness, Qty: 1, FineOzEach: 1}
			if got := buybackFactor(l, s); math.Abs(got-tc.want) > tol {
				t.Errorf("buybackFactor(fineness=%q) = %.4f, want %.4f", tc.fineness, got, tc.want)
			}
			if a := Compute(model.Dataset{Spot: model.Spot{SilverUSD: 60}, Settings: s, Lots: []model.Lot{l}}).Anomalies; len(a) != 0 {
				t.Errorf("fineness=%q raised anomalies %+v, want none (it parses)", tc.fineness, a)
			}
		})
	}
}
