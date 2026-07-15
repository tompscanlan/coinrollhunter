package store

import (
	"errors"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// The compound-workflow atomicity proofs (om-2sl6). Each user action is ONE store
// transaction: a failure at ANY step must leave the DB byte-for-byte as it was — zero
// roll_txns, zero item_type, zero lots, zero keepers, zero trips, AND zero branches
// (the sneaky one: resolveBranchID forks a branch as a side effect of the box insert,
// and a "rolled-back" box must not leave it behind — seam f). These tests assert the
// row counts across every table before and after, not just that the call errored: a
// test that only checks the error does not prove rollback.

// allTables is every table a compound write can touch (branches + branch_aliases are
// the orphan-branch trace seam f closes).
var compoundTables = []string{
	"item_type", "lots", "roll_txns", "trips", "keepers", "branches", "branch_aliases",
}

func assertAllEmpty(t *testing.T, s *Store) {
	t.Helper()
	for _, table := range compoundTables {
		if n := countTable(t, s, table); n != 0 {
			t.Errorf("%s: %d rows after a rolled-back compound action, want 0", table, n)
		}
	}
}

func validPurchase(bank string) model.RollTxn {
	return model.RollTxn{Date: "2026-02-01", Bank: bank, Action: "buy", Denom: "halves", Unit: "box", Amount: 1, FaceUSD: 500}
}

// newBox is validPurchase as an addressable *model.RollTxn, for BoxRef.New.
func newBox(bank string) *model.RollTxn {
	p := validPurchase(bank)
	return &p
}

func validFind(product string) FindSpec {
	return FindSpec{
		Type:    ItemTypeSpec{Name: product, Metal: "silver", Fineness: "90%", FineOzEach: 0.36169},
		Holding: model.Holding{Activity: "crh", Qty: 1, BasisUSD: 0.5, FaceValueUSD: 0.5, Acquired: "2026-02-01"},
	}
}

// --- RecordPurchase: seam a (purchase + trip) and seam f (no orphan branch) ---------

func TestRecordPurchaseCommits(t *testing.T) {
	s := txStore(t)
	rollID, tripID, err := s.RecordPurchase(validPurchase("Stock Yards"),
		&model.Trip{Date: "2026-02-01", Bank: "Stock Yards", Miles: 8, Hours: 0.5})
	if err != nil {
		t.Fatalf("RecordPurchase: %v", err)
	}
	if rollID == 0 || tripID == 0 {
		t.Fatalf("ids: roll=%d trip=%d, want both nonzero", rollID, tripID)
	}
	for table, want := range map[string]int{"roll_txns": 1, "trips": 1, "branches": 1, "branch_aliases": 1} {
		if n := countTable(t, s, table); n != want {
			t.Errorf("%s = %d, want %d", table, n, want)
		}
	}
	// The purchase and the trip share the ONE branch the purchase forked — resolveBranchID
	// found the uncommitted row inside the same tx, so it did not double-fork.
	if n := countTable(t, s, "branches"); n != 1 {
		t.Errorf("branches = %d, want 1 (purchase + trip must share one forked branch)", n)
	}
}

func TestRecordPurchaseNoTripStillCommits(t *testing.T) {
	s := txStore(t)
	if _, tripID, err := s.RecordPurchase(validPurchase("First Federal"), nil); err != nil || tripID != 0 {
		t.Fatalf("RecordPurchase(nil trip): tripID=%d err=%v", tripID, err)
	}
	if n := countTable(t, s, "roll_txns"); n != 1 {
		t.Errorf("roll_txns = %d, want 1", n)
	}
	if n := countTable(t, s, "trips"); n != 0 {
		t.Errorf("trips = %d, want 0", n)
	}
}

// TestRecordPurchaseRollsBackWithNoOrphanBranch is the seam-f proof for "bought a box":
// a VALID purchase that forks a brand-new branch, followed by an INVALID trip. The trip's
// rejection (om-1czp validation) is a real, non-synthetic mid-transaction failure, and it
// must roll back the box AND the branch the box forked — leaving nothing behind.
func TestRecordPurchaseRollsBackWithNoOrphanBranch(t *testing.T) {
	s := txStore(t)
	// Sanity: the bank is genuinely new, so a committed purchase WOULD fork a branch.
	if n := countTable(t, s, "branches"); n != 0 {
		t.Fatalf("precondition: branches = %d, want 0", n)
	}
	_, _, err := s.RecordPurchase(
		validPurchase("Ghost Branch"),
		&model.Trip{Date: "2026-02-01", Bank: "Ghost Branch", Miles: -5}, // negative miles -> ErrInvalid
	)
	if err == nil {
		t.Fatal("RecordPurchase accepted an invalid trip")
	}
	if !errors.Is(err, model.ErrInvalid) {
		t.Errorf("error %v does not unwrap to ErrInvalid", err)
	}
	assertAllEmpty(t, s)
}

// --- RecordFinds: seam b (box + N finds + M keepers) and seam f ---------------------

func TestRecordFindsCommitsAndLinks(t *testing.T) {
	s := txStore(t)
	err := s.RecordFinds(
		BoxRef{New: newBox("Fifth Third")},
		[]FindSpec{validFind("90% half"), validFind("90% dime")},
		[]model.Keeper{{Denom: "halves", Count: 10, FaceUSD: 5, Date: "2026-02-01"}},
	)
	if err != nil {
		t.Fatalf("RecordFinds: %v", err)
	}
	for table, want := range map[string]int{"roll_txns": 1, "item_type": 2, "lots": 2, "keepers": 1, "branches": 1} {
		if n := countTable(t, s, table); n != want {
			t.Errorf("%s = %d, want %d", table, n, want)
		}
	}
	// Every find and the keeper must resolve back to the ONE box that was created inline.
	var boxID int64
	if err := s.db.QueryRow(`SELECT id FROM roll_txns`).Scan(&boxID); err != nil {
		t.Fatal(err)
	}
	holdings, _ := s.ListHoldings()
	for _, h := range holdings {
		if h.RollTxnID != boxID {
			t.Errorf("holding %d links to box %d, want %d", h.ID, h.RollTxnID, boxID)
		}
	}
	keepers, _ := s.ListKeepers()
	for _, k := range keepers {
		if k.RollTxnID != boxID {
			t.Errorf("keeper %d links to box %d, want %d", k.ID, k.RollTxnID, boxID)
		}
	}
}

func TestRecordFindsExistingBoxDoesNotForkBranch(t *testing.T) {
	s := txStore(t)
	boxID, err := s.InsertRollTxn(validPurchase("Commonwealth"))
	if err != nil {
		t.Fatal(err)
	}
	before := countTable(t, s, "branches")
	if err := s.RecordFinds(BoxRef{ExistingID: boxID}, []FindSpec{validFind("90% quarter")}, nil); err != nil {
		t.Fatalf("RecordFinds(existing box): %v", err)
	}
	if n := countTable(t, s, "branches"); n != before {
		t.Errorf("branches = %d, want %d (an existing box must not fork a new branch)", n, before)
	}
	holdings, _ := s.ListHoldings()
	if len(holdings) != 1 || holdings[0].RollTxnID != boxID {
		t.Errorf("find did not link to the existing box: %+v", holdings)
	}
}

// TestRecordFindsRollsBackWithNoOrphanBranch is the seam-b + seam-f proof: a new box that
// forks a brand-new branch, a valid first find, then an INVALID second find. The whole
// submission — box, branch, item_type, first lot — must roll back.
func TestRecordFindsRollsBackWithNoOrphanBranch(t *testing.T) {
	s := txStore(t)
	badFind := validFind("bad find")
	badFind.Holding.Activity = "hoard" // not bullion|crh -> ErrInvalid, on the LAST step
	err := s.RecordFinds(
		BoxRef{New: newBox("Phantom Bank")},
		[]FindSpec{validFind("90% half"), badFind},
		nil,
	)
	if err == nil {
		t.Fatal("RecordFinds accepted an invalid find")
	}
	if !errors.Is(err, model.ErrInvalid) {
		t.Errorf("error %v does not unwrap to ErrInvalid", err)
	}
	assertAllEmpty(t, s)
}

// A failing KEEPER (the very last step) also rolls back the box, its branch, and every
// find written ahead of it.
func TestRecordFindsRollsBackOnBadKeeper(t *testing.T) {
	s := txStore(t)
	err := s.RecordFinds(
		BoxRef{New: newBox("Phantom Bank")},
		[]FindSpec{validFind("90% half")},
		[]model.Keeper{{Denom: "halves", Count: -1}}, // negative count -> ErrInvalid
	)
	if err == nil || !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("RecordFinds error = %v, want ErrInvalid", err)
	}
	assertAllEmpty(t, s)
}

// --- RecordHolding / ReviseHolding: seam c (Edit grid), d (NewBullion), e (Reconcile) --

func TestRecordHoldingCreatesTypeAndLot(t *testing.T) {
	s := txStore(t)
	id, err := s.RecordHolding(
		ItemTypeSpec{Name: "1 oz Gold Eagle", Metal: "gold", FineOzEach: 1},
		model.Holding{Activity: "bullion", Qty: 1, BasisUSD: 3950, Acquired: "2026-02-02"},
	)
	if err != nil {
		t.Fatalf("RecordHolding: %v", err)
	}
	if id == 0 {
		t.Fatal("RecordHolding returned id 0")
	}
	for table, want := range map[string]int{"item_type": 1, "lots": 1} {
		if n := countTable(t, s, table); n != want {
			t.Errorf("%s = %d, want %d", table, n, want)
		}
	}
	// A second holding of the SAME product reuses the catalog row (find-or-create).
	if _, err := s.RecordHolding(
		ItemTypeSpec{Name: "1 oz Gold Eagle", Metal: "gold", FineOzEach: 1},
		model.Holding{Activity: "bullion", Qty: 2, BasisUSD: 7900, Acquired: "2026-02-03"},
	); err != nil {
		t.Fatal(err)
	}
	if n := countTable(t, s, "item_type"); n != 1 {
		t.Errorf("item_type = %d, want 1 (the second lot must reuse the catalog row)", n)
	}
	if n := countTable(t, s, "lots"); n != 2 {
		t.Errorf("lots = %d, want 2", n)
	}
}

// TestRecordHoldingRollsBackTypeOnBadHolding: a bad holding rejects AFTER the item_type
// was find-or-created, so the new catalog row must roll back with it — no orphan type.
func TestRecordHoldingRollsBackTypeOnBadHolding(t *testing.T) {
	s := txStore(t)
	_, err := s.RecordHolding(
		ItemTypeSpec{Name: "Orphan Type", Metal: "gold", FineOzEach: 1},
		model.Holding{Activity: "hoard", Qty: 1, Acquired: "2026-02-02"}, // bad activity
	)
	if err == nil || !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("RecordHolding error = %v, want ErrInvalid", err)
	}
	if n := countTable(t, s, "item_type"); n != 0 {
		t.Errorf("item_type = %d, want 0 (the find-or-created type must roll back)", n)
	}
	if n := countTable(t, s, "lots"); n != 0 {
		t.Errorf("lots = %d, want 0", n)
	}
}

// TestReviseHoldingMergePreservesUnnamedColumns: the Edit-grid update is a MERGE. A field
// the caller's apply does not touch (notes, insured_value, the disposal) survives — the
// om-kyq7 guarantee, now inside the compound tx.
func TestReviseHoldingMergePreservesUnnamedColumns(t *testing.T) {
	s := txStore(t)
	id, err := s.RecordHolding(
		ItemTypeSpec{Name: "Mercury dime", Metal: "silver", Fineness: "90%", FineOzEach: 0.07234},
		model.Holding{Activity: "crh", Qty: 1, BasisUSD: 2, FaceValueUSD: 2, Acquired: "2026-02-02", Notes: "grandpa's", InsuredValue: 300},
	)
	if err != nil {
		t.Fatal(err)
	}
	// apply names ONLY qty — the merge the grid does when you edit one cell.
	err = s.ReviseHolding(id,
		ItemTypeSpec{Name: "Mercury dime", Metal: "silver", Fineness: "90%", FineOzEach: 0.07234},
		func(cur model.Holding) (model.Holding, error) { cur.Qty = 5; return cur, nil })
	if err != nil {
		t.Fatalf("ReviseHolding: %v", err)
	}
	got, err := getHolding(s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Qty != 5 {
		t.Errorf("qty = %v, want 5 (the named edit must land)", got.Qty)
	}
	if got.Notes != "grandpa's" {
		t.Errorf("notes = %q, want %q (a merge must not blank an unnamed column)", got.Notes, "grandpa's")
	}
	if got.InsuredValue != 300 {
		t.Errorf("insured_value = %v, want 300 (a merge must not blank an unnamed column)", got.InsuredValue)
	}
	if n := countTable(t, s, "item_type"); n != 1 {
		t.Errorf("item_type = %d, want 1 (editing a lot must reuse its catalog row)", n)
	}
}

// TestReviseHoldingRollsBackTypeOnBadMerge: the merge produces an invalid holding, so the
// UPDATE rejects — and the item_type find-or-created a step earlier (a brand-new type
// here) must roll back with it, and the stored holding must be untouched.
func TestReviseHoldingRollsBackTypeOnBadMerge(t *testing.T) {
	s := txStore(t)
	id, err := s.RecordHolding(
		ItemTypeSpec{Name: "1 oz Gold Eagle", Metal: "gold", FineOzEach: 1},
		model.Holding{Activity: "bullion", Qty: 1, BasisUSD: 3950, Acquired: "2026-02-02"},
	)
	if err != nil {
		t.Fatal(err)
	}
	typesBefore := countTable(t, s, "item_type")
	// A NEW catalog name (so ensureItemType would INSERT) + a merge that makes the holding
	// invalid (bad activity on the final UPDATE).
	err = s.ReviseHolding(id,
		ItemTypeSpec{Name: "Freshly Typed", Metal: "gold", FineOzEach: 1},
		func(cur model.Holding) (model.Holding, error) { cur.Activity = "hoard"; return cur, nil })
	if err == nil || !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("ReviseHolding error = %v, want ErrInvalid", err)
	}
	if n := countTable(t, s, "item_type"); n != typesBefore {
		t.Errorf("item_type = %d, want %d (the find-or-created type must roll back)", n, typesBefore)
	}
	got, err := getHolding(s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity != "bullion" || got.Qty != 1 {
		t.Errorf("holding mutated by a rolled-back update: activity=%q qty=%v", got.Activity, got.Qty)
	}
}

func TestReviseHoldingMissingIsNotFound(t *testing.T) {
	s := txStore(t)
	err := s.ReviseHolding(999,
		ItemTypeSpec{Name: "x", Metal: "gold"},
		func(cur model.Holding) (model.Holding, error) { return cur, nil })
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("ReviseHolding(missing) = %v, want ErrNotFound", err)
	}
}
