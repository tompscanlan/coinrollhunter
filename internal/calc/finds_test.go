package calc

import (
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// TestSummaryKPIs covers the ADR-006 activity stats over the shared sample fixture:
// 2 buys, both at "Sample Bank" (1 distinct branch), $500 each → avg $500.
func TestSummaryKPIs(t *testing.T) {
	r := Compute(sampleDataset())
	if r.BuyCount != 2 {
		t.Errorf("buy_count = %d, want 2", r.BuyCount)
	}
	if r.BranchCount != 1 {
		t.Errorf("branch_count = %d, want 1", r.BranchCount)
	}
	approx(t, "avg_buy_usd", r.AvgBuyUSD, 500.0)
}

// findsFixture is a small worked example for the hit-rate report. Two dime buys (CR $200,
// MR $50) plus finds tagged by category/subcategory and linked to their buy. One find is
// disposed (sold) and one is unattributed (no buy link) — both must still count.
func findsFixture() model.Dataset {
	return model.Dataset{
		Settings: model.DefaultSettings(),
		RollTxns: []model.RollTxn{
			{ID: 1, Action: "buy", Denom: "dimes", SourceType: "customer_roll", FaceUSD: 200},
			{ID: 2, Action: "buy", Denom: "dimes", SourceType: "machine_roll", FaceUSD: 50},
			{ID: 3, Action: "return", FaceUSD: 240}, // ignored by the finds report
		},
		Lots: []model.Lot{
			{Activity: "crh", RollTxnID: 1, Category: "Silver", Subcategory: "Mercury", Qty: 2},
			{Activity: "crh", RollTxnID: 1, Category: "Silver", Subcategory: "Roosevelt 90%", Qty: 6},
			{Activity: "crh", RollTxnID: 2, Category: "Silver", Subcategory: "Mercury", Qty: 1},
			{Activity: "crh", RollTxnID: 0, Category: "PMD", Qty: 3}, // unattributed
			{Activity: "bullion", RollTxnID: 0, Qty: 1},              // not a find — excluded
		},
		Disposed: []model.DisposedLot{
			{Activity: "crh", RollTxnID: 1, Category: "Silver", Subcategory: "Barber", Qty: 1}, // sold, still counts
		},
	}
}

func findDenom(r FindsReport, denom string) *DenomReport {
	for i := range r.Denoms {
		if r.Denoms[i].Denom == denom {
			return &r.Denoms[i]
		}
	}
	return nil
}

func findCat(d *DenomReport, cat string) *CategoryReport {
	for i := range d.Categories {
		if d.Categories[i].Category == cat {
			return &d.Categories[i]
		}
	}
	return nil
}

func findSub(c *CategoryReport, sub string) *SubcategoryReport {
	for i := range c.Subcategories {
		if c.Subcategories[i].Subcategory == sub {
			return &c.Subcategories[i]
		}
	}
	return nil
}

func cell(cells []SourceCell, src string) SourceCell {
	for _, c := range cells {
		if c.Source == src {
			return c
		}
	}
	return SourceCell{}
}

func TestFindsReport(t *testing.T) {
	r := ComputeFindsReport(findsFixture())

	approx(t, "total_face_searched", r.TotalFaceSearched, 250) // returns excluded
	approx(t, "unattributed", r.Unattributed, 3)               // the PMD find with no link
	if r.LowConfidenceN != LowConfidenceN {
		t.Errorf("low_confidence_n = %v, want %v", r.LowConfidenceN, float64(LowConfidenceN))
	}

	dimes := findDenom(r, "dimes")
	if dimes == nil {
		t.Fatal("no dimes denom report")
	}
	approx(t, "dimes face_searched", dimes.FaceSearched, 250)
	approx(t, "dimes coins_searched", dimes.CoinsSearched, 2500) // 250 / $0.10
	approx(t, "dimes face CR", dimes.FaceBySource["customer_roll"], 200)
	approx(t, "dimes face MR", dimes.FaceBySource["machine_roll"], 50)

	// Silver category: 2+6 (CR) + 1 (MR) + 1 (CR, sold Barber) = 10 coins.
	silver := findCat(dimes, "Silver")
	if silver == nil {
		t.Fatal("no Silver category")
	}
	approx(t, "silver count", silver.Count, 10)
	approx(t, "silver hit_per_face", silver.HitPerFace, 25) // 250/10
	if silver.LowConfidence {
		t.Error("silver should not be low-confidence at n=10")
	}
	// per-source: CR has 9 (2+6+1 barber), MR has 1.
	cr := cell(silver.BySource, "customer_roll")
	approx(t, "silver CR count", cr.Count, 9)
	approx(t, "silver CR hit", cr.HitPerFace, 200.0/9.0)
	mr := cell(silver.BySource, "machine_roll")
	approx(t, "silver MR count", mr.Count, 1)
	approx(t, "silver MR hit", mr.HitPerFace, 50) // 50/1
	if !mr.LowConfidence {
		t.Error("silver MR cell (n=1) should be low-confidence")
	}

	// Subcategories present and counted (incl. the disposed Barber).
	if findSub(silver, "Barber") == nil {
		t.Error("disposed Barber find missing from subcategories (survivorship broken)")
	}
	mercury := findSub(silver, "Mercury")
	if mercury == nil {
		t.Fatal("no Mercury subcategory")
	}
	approx(t, "mercury count", mercury.Count, 3) // 2 CR + 1 MR
	if !mercury.LowConfidence {
		t.Error("mercury (n=3) should be low-confidence")
	}
	roosie := findSub(silver, "Roosevelt 90%")
	approx(t, "roosie count", roosie.Count, 6)
	approx(t, "roosie hit", roosie.HitPerFace, 250.0/6.0)

	// Unattributed find lands in the "" denom bucket with no face → hit 0 (N/A).
	unk := findDenom(r, "")
	if unk == nil {
		t.Fatal("no unattributed denom bucket")
	}
	pmd := findCat(unk, "PMD")
	if pmd == nil {
		t.Fatal("no PMD category in unattributed bucket")
	}
	approx(t, "unattributed PMD count", pmd.Count, 3)
	approx(t, "unattributed PMD hit (N/A)", pmd.HitPerFace, 0)
}

// TestFindsReportEmpty: an empty dataset must not panic and yields no denoms.
func TestFindsReportEmpty(t *testing.T) {
	r := ComputeFindsReport(model.Dataset{})
	if len(r.Denoms) != 0 {
		t.Errorf("empty dataset: got %d denoms, want 0", len(r.Denoms))
	}
	approx(t, "empty total face", r.TotalFaceSearched, 0)
}
