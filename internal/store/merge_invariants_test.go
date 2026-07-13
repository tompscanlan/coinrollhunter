package store_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// These two guard the invariant the API's partial update (PUT-as-merge) is built on.
//
// The merge preserves a column the client did not name by reading the stored row and
// writing it back. That only holds while the READ path returns everything the WRITE
// path writes. Drop one column from ListHoldings' SELECT while UpdateHolding still has
// it in its SET list, and every merge writes that column back as a zero value — which
// is the very data loss the merge exists to prevent, arriving through a different door.
//
// Schema-driven on purpose (cf. the PRAGMA coverage tests specced for the export):
// add a column to lots and these tests cover it without anyone remembering to.

// A lot read back and written back unchanged must survive the round trip intact.
func TestHoldingRoundTripsEveryColumnItWrites(t *testing.T) {
	s := openStore(t)

	typeID, err := s.InsertItemType(model.ItemType{
		Kind: "coin", Name: "1 oz American Gold Eagle", Metal: "gold", FineOzEach: 1, Fineness: "22k .9167",
	})
	if err != nil {
		t.Fatal(err)
	}
	boxID, err := s.InsertRollTxn(model.RollTxn{
		Date: "2026-01-01", Bank: "First National", Action: "buy", Denom: "halves", Unit: "box",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Every column distinctly non-zero: a zero would be indistinguishable from a column
	// that got blanked, which is the failure this is here to catch.
	want := model.Holding{
		ItemTypeID: typeID, RollTxnID: boxID, Activity: "crh", Qty: 3,
		GrossWeight: 31.1, Purity: 0.9167, WeightUnit: "g",
		BasisUSD: 2400.50, PremiumUSD: 85.25, FaceValueUSD: 50,
		Acquired: "2026-02-02", Source: "estate sale", Location: "safe deposit box 214",
		InsuredValue: 3100, Attributes: `{"grade":"MS65","cert":"12345678"}`,
		Notes:    "grandfather's; do not sell",
		Category: "Silver", Subcategory: "Mercury", Trophy: true,
		Disposed: "2026-05-05", DisposedUSD: 2750,
	}
	id, err := s.InsertHolding(want)
	if err != nil {
		t.Fatal(err)
	}

	// Read → write back verbatim → read. This is what a merge does when the client
	// names nothing, so it must be a no-op.
	before := findHolding(t, s, id)
	if err := s.UpdateHolding(id, before); err != nil {
		t.Fatal(err)
	}
	after := findHolding(t, s, id)

	if !reflect.DeepEqual(before, after) {
		t.Errorf("a read-then-write-back changed the row.\n before = %+v\n after  = %+v", before, after)
	}
	// And the row that came back is genuinely the one we wrote — a read path that
	// dropped a column would otherwise round-trip its own zero quite happily.
	before.ID, before.UID = 0, ""
	if !reflect.DeepEqual(before, want) {
		t.Errorf("the stored lot did not read back as written.\n got  = %+v\n want = %+v", before, want)
	}
}

// Every column of lots is a field of model.Holding. A column the model does not carry
// is one the app cannot read, write, or show — the failure that left location and
// insured_value wired end to end but invisible for months.
func TestHoldingModelsEveryLotsColumn(t *testing.T) {
	s := openStore(t)

	rows, err := s.DB().Query(`SELECT name FROM pragma_table_info('lots')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	modeled := map[string]bool{}
	rt := reflect.TypeOf(model.Holding{})
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("json")
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			modeled[name] = true
		}
	}

	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		if !modeled[col] {
			t.Errorf("lots.%s has no field on model.Holding — the app cannot see it, and a "+
				"partial update cannot preserve what it cannot read", col)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func findHolding(t *testing.T, s *store.Store, id int64) model.Holding {
	t.Helper()
	hs, err := s.ListHoldings()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hs {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("holding %d not found", id)
	return model.Holding{}
}
