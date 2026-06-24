package legacy

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// TestImportSampleEndToEnd proves the full Phase 0–1 path lines up: prototype
// JSON -> migrate -> SQLite (catalog/specimen) -> resolve -> calc reproduces the
// same numbers the calc package verifies on its in-memory fixture. If migration
// or resolution drops or mangles data, the headline numbers move.
func TestImportSampleEndToEnd(t *testing.T) {
	root := filepath.Join("..", "..", "prototype", "sample-data")
	holdings, err := os.ReadFile(filepath.Join(root, "pm_holdings.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	crh, err := os.ReadFile(filepath.Join(root, "crh_ledger.sample.json"))
	if err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := Import(s, holdings, crh); err != nil {
		t.Fatalf("import: %v", err)
	}

	d, err := s.ResolveDataset()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	r := calc.Compute(d)

	approx := func(label string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-6 {
			t.Errorf("%s = %.6f, want %.6f", label, got, want)
		}
	}

	// Same headline values the calc worked-example test derives from the fixture.
	approx("find_oz", r.FindOz, 1.36404)
	approx("find_cost", r.FindCost, 4.25)
	approx("find_realizable", r.FindRealizable, 66.55896)
	approx("op_cost", r.OpCost, 12.20)
	approx("crh_net_real", r.CRHNetReal, 50.10896)
	approx("to_redeposit", r.ToRedeposit, -11.00)
	approx("bullion_unreal", r.BullionUnreal, -35.96)
	approx("total_boxes", r.TotalBoxes, 2.0)
	if r.Verdict() != "PROFITABLE (cash basis)" {
		t.Errorf("verdict = %q", r.Verdict())
	}

	// Catalog/specimen split: 4 prototype lots -> 4 holdings, but the two 90%
	// silver types differ by product/asw so we expect 4 distinct item_types here.
	var nTypes, nLots int
	if err := s.DB().QueryRow(`SELECT count(*) FROM item_type`).Scan(&nTypes); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM lots`).Scan(&nLots); err != nil {
		t.Fatal(err)
	}
	if nLots != 4 {
		t.Errorf("lots = %d, want 4", nLots)
	}
	if nTypes != 4 {
		t.Errorf("item_types = %d, want 4 (one per distinct product/metal/fineness/asw)", nTypes)
	}
}

// TestImportIsIdempotentSchema confirms a second Open on the same store is a
// no-op migration (version stays at the latest, no error).
func TestImportReopenStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crh.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	v1, _ := s.Version()
	s.Close()

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	v2, _ := s2.Version()
	if v1 != v2 || v1 < 1 {
		t.Errorf("version drift: first=%d second=%d (want equal, >=1)", v1, v2)
	}
}
