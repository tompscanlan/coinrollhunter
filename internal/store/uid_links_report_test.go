package store_test

import (
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// om-c8ei, behavioral half: deleting a box must not delete or corrupt its children, and
// the orphaned find must read back UNATTRIBUTED (blank), never re-adopted onto a recycled
// box. These drive the full store + calc path, the way the app does.

// AC3 — orphans resolve to blank, not to wrong, and ComputeFindsReport counts them as
// Unattributed. Deleting a box leaves its find AND keeper as rows (nothing cascades), and
// both read back with no box.
func TestDeletingABoxLeavesOrphansUnattributed(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.InsertRollTxn(model.RollTxn{Date: "2026-07-01", Bank: "Chase", Action: "buy", Denom: "dimes", Unit: "box", Amount: 1, FaceUSD: 250})
	if err != nil {
		t.Fatal(err)
	}
	findID, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: box, Activity: "crh", Category: "Silver", Subcategory: "Mercury", Qty: 3, BasisUSD: 0.3, Acquired: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	keepID, err := s.InsertKeeper(model.Keeper{Denom: "dimes", Count: 5, FaceUSD: 0.5, Date: "2026-07-01", RollTxnID: box})
	if err != nil {
		t.Fatal(err)
	}

	// Baseline: the find is attributed to its box, so nothing is unattributed.
	base, err := s.ResolveDataset()
	if err != nil {
		t.Fatal(err)
	}
	if got := calc.ComputeFindsReport(base).Unattributed; got != 0 {
		t.Fatalf("baseline unattributed = %v, want 0 (the find is attributed to its box)", got)
	}

	// Delete the box. Nothing cascades: the find and the keeper survive.
	if err := s.DeleteRollTxn(box); err != nil {
		t.Fatal(err)
	}
	hs, err := s.ListHoldings()
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0].ID != findID {
		t.Fatalf("deleting a box must not delete its find; got %+v", hs)
	}
	if hs[0].RollTxnID != 0 {
		t.Errorf("orphaned find should read back with no box (0), got %d", hs[0].RollTxnID)
	}
	ks, err := s.ListKeepers()
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 1 || ks[0].ID != keepID {
		t.Fatalf("deleting a box must not delete its keeper; got %+v", ks)
	}
	if ks[0].RollTxnID != 0 {
		t.Errorf("orphaned keeper should read back with no box (0), got %d", ks[0].RollTxnID)
	}

	// The report now counts the orphaned find's coins as Unattributed.
	after, err := s.ResolveDataset()
	if err != nil {
		t.Fatal(err)
	}
	if got := calc.ComputeFindsReport(after).Unattributed; got != 3 {
		t.Errorf("unattributed = %v, want 3 (the orphaned find's coins)", got)
	}
}

// AC6 — the partial-sale carve-out keeps its box. SellHolding of PART of a box-linked
// find carves out a NEW lot for the sold portion and shrinks the original; BOTH must
// still resolve to the same box (the trap ADR-009 was bitten by, now for the box link).
func TestPartialSaleCarriesBoxUidOntoCarveOut(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "90% Half", Metal: "silver", Fineness: "90%"})
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.InsertRollTxn(model.RollTxn{Date: "2026-01-01", Bank: "First National", Action: "buy", Denom: "halves", Unit: "box", Amount: 1, FaceUSD: 500})
	if err != nil {
		t.Fatal(err)
	}
	lotID, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: box, Activity: "crh", Qty: 10, BasisUSD: 30, PremiumUSD: 5, FaceValueUSD: 5, Acquired: "2026-01-15"})
	if err != nil {
		t.Fatal(err)
	}

	// Sell 4 of the 10 — the carve-out path, not full disposal.
	if err := s.SellHolding(lotID, 4, 40, "2026-04-01"); err != nil {
		t.Fatal(err)
	}

	hs, err := s.ListHoldings()
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 2 {
		t.Fatalf("partial sale should split the lot in two, got %d", len(hs))
	}
	var sold, kept *model.Holding
	for i := range hs {
		if hs[i].Disposed != "" {
			sold = &hs[i]
		} else {
			kept = &hs[i]
		}
	}
	if sold == nil || kept == nil {
		t.Fatalf("want one disposed carve-out and one live remainder, got %+v", hs)
	}
	if kept.RollTxnID != box {
		t.Errorf("the retained remainder lost its box: RollTxnID=%d, want %d", kept.RollTxnID, box)
	}
	if sold.RollTxnID != box {
		t.Errorf("the sold carve-out lost its box: RollTxnID=%d, want %d — every partially-sold find would lose its box", sold.RollTxnID, box)
	}
}
