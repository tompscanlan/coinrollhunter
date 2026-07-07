package store_test

import (
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// TestResolveDatasetReadPathColumnAlignment pins the SELECT/Scan column order of
// ResolveDataset's live-lots query. premium_usd, basis_usd, and face_value_usd are
// all REAL columns, so a same-type reorder in the SELECT (without a matching Scan
// reorder) would compile, run, and pass every count-based test while silently
// serving each lot's money fields swapped. The seeded values below are deliberately
// distinct so any such swap makes an assertion fail. Guards the unplanned data.go
// read-path edit from om-0pqe (audit #5).
func TestResolveDatasetReadPathColumnAlignment(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	itID, err := s.InsertItemType(model.ItemType{
		Name:       "1 oz American Gold Eagle",
		Metal:      "gold",
		FineOzEach: 1.0,
		Fineness:   "22k .9167",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Distinct values across the adjacent REAL money columns so a swap is caught.
	_, err = s.InsertHolding(model.Holding{
		ItemTypeID:   itID,
		Activity:     "bullion",
		Qty:          3,
		BasisUSD:     1512,
		PremiumUSD:   62,
		FaceValueUSD: 25,
		Acquired:     "2026-01-01",
		Source:       "Blue Moon Bullion",
	})
	if err != nil {
		t.Fatal(err)
	}

	d, err := s.ResolveDataset()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Lots) != 1 {
		t.Fatalf("ResolveDataset returned %d lots, want 1", len(d.Lots))
	}
	lot := d.Lots[0]

	// Money columns — the swap-sensitive trio. PremiumUSD is the om-5 field; Basis
	// and Face bracket it in the SELECT and pin the reorder both directions.
	if lot.BasisUSD != 1512 {
		t.Errorf("BasisUSD = %v, want 1512 (column misaligned?)", lot.BasisUSD)
	}
	if lot.PremiumUSD != 62 {
		t.Errorf("PremiumUSD = %v, want 62 (column misaligned or not selected?)", lot.PremiumUSD)
	}
	if lot.FaceValueUSD != 25 {
		t.Errorf("FaceValueUSD = %v, want 25 (column misaligned?)", lot.FaceValueUSD)
	}

	// Broader alignment: Qty (pre-money) and the join-resolved fields (post-money,
	// keyed on item_type_id) pin the rest of the column list around the edit.
	if lot.Qty != 3 {
		t.Errorf("Qty = %v, want 3", lot.Qty)
	}
	if lot.Product != "1 oz American Gold Eagle" {
		t.Errorf("Product = %q, want the seeded item type (item_type_id misaligned?)", lot.Product)
	}
	if lot.Metal != "gold" {
		t.Errorf("Metal = %q, want gold", lot.Metal)
	}
}
