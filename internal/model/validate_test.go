package model

import (
	"errors"
	"strings"
	"testing"
)

// One rejected case per invariant per type, plus a representative accepted case.
// Every rejection must (a) be an ErrInvalid so the API maps it to a 400, and
// (b) name the offending field in the message so the client can point at the cell.

func TestValidateRejectsOneCasePerInvariant(t *testing.T) {
	cases := []struct {
		name  string
		err   error  // the value under test's Validate() result
		field string // substring the message must contain (usually the field name)
	}{
		// ItemType
		{"itemtype bad metal", ItemType{Metal: "goldd"}.Validate(), "metal"},
		{"itemtype negative fine oz", ItemType{Metal: "gold", FineOzEach: -1}.Validate(), "fine_oz_each"},

		// Holding
		{"holding bad activity", Holding{Activity: "gold", Acquired: "2026-01-01"}.Validate(), "activity"},
		{"holding empty activity", Holding{Activity: "", Acquired: "2026-01-01"}.Validate(), "activity"},
		{"holding negative qty", Holding{Activity: "crh", Qty: -1, Acquired: "2026-01-01"}.Validate(), "qty"},
		{"holding negative basis", Holding{Activity: "crh", BasisUSD: -0.01, Acquired: "2026-01-01"}.Validate(), "basis_usd"},
		{"holding negative gross weight", Holding{Activity: "crh", GrossWeight: -1, Acquired: "2026-01-01"}.Validate(), "gross_weight"},
		{"holding negative face", Holding{Activity: "crh", FaceValueUSD: -1, Acquired: "2026-01-01"}.Validate(), "face_value_usd"},
		{"holding purity over 1", Holding{Activity: "crh", Purity: 9.9, Acquired: "2026-01-01"}.Validate(), "purity"},
		{"holding purity under 0", Holding{Activity: "crh", Purity: -0.1, Acquired: "2026-01-01"}.Validate(), "purity"},
		{"holding bad weight unit", Holding{Activity: "crh", WeightUnit: "gr", Acquired: "2026-01-01"}.Validate(), "weight_unit"},
		{"holding empty acquired", Holding{Activity: "crh", Acquired: ""}.Validate(), "acquired"},
		{"holding bad acquired", Holding{Activity: "crh", Acquired: "nope"}.Validate(), "acquired"},
		{"holding bad disposed", Holding{Activity: "crh", Acquired: "2026-01-01", Disposed: "soon"}.Validate(), "disposed"},

		// RollTxn
		{"rolltxn bad action", RollTxn{Action: "sell", Date: "2026-01-01"}.Validate(), "action"},
		{"rolltxn empty action", RollTxn{Action: "", Date: "2026-01-01"}.Validate(), "action"},
		{"rolltxn negative amount", RollTxn{Action: "buy", Amount: -1, Date: "2026-01-01"}.Validate(), "amount"},
		{"rolltxn negative face", RollTxn{Action: "buy", FaceUSD: -1, Date: "2026-01-01"}.Validate(), "face_usd"},
		{"rolltxn empty date", RollTxn{Action: "buy", Date: ""}.Validate(), "date"},

		// Trip
		{"trip negative miles", Trip{Miles: -1}.Validate(), "miles"},
		{"trip negative hours", Trip{Hours: -1}.Validate(), "hours"},
		{"trip bad date", Trip{Date: "2026/01/01"}.Validate(), "date"},

		// Branch
		{"branch negative fee", Branch{CoinFeeUSD: -1}.Validate(), "coin_fee_usd"},
		{"branch negative box limit", Branch{BoxLimit: -1}.Validate(), "box_limit"},
		{"branch negative cooldown", Branch{CooldownDays: -1}.Validate(), "cooldown_days"},

		// Supply
		{"supply negative cost", Supply{CostUSD: -1}.Validate(), "cost_usd"},
		{"supply bad date", Supply{Date: "yesterday"}.Validate(), "date"},

		// Keeper
		{"keeper negative count", Keeper{Count: -1}.Validate(), "count"},
		{"keeper negative face", Keeper{FaceUSD: -1}.Validate(), "face_usd"},
		{"keeper bad date", Keeper{Date: "13-13-13"}.Validate(), "date"},

		// Loss
		{"loss negative amount", Loss{AmountUSD: -1, Date: "2026-01-01"}.Validate(), "amount_usd"},
		{"loss empty date", Loss{AmountUSD: 1, Date: ""}.Validate(), "date"},

		// Spot
		{"spot negative gold", Spot{GoldUSD: -1}.Validate(), "gold_usd"},
		{"spot negative silver", Spot{SilverUSD: -1}.Validate(), "silver_usd"},

		// Settings
		{"settings negative hourly", Settings{HourlyRateUSD: -1}.Validate(), "hourly_rate_usd"},
		{"settings negative box face", Settings{BoxFaceUSD: map[string]float64{"halves": -1}}.Validate(), "box_face_usd"},

		// Sale
		{"sale zero qty", ValidateSale(0, 10, "2026-01-01"), "qty"},
		{"sale negative qty", ValidateSale(-1, 10, "2026-01-01"), "qty"},
		{"sale negative proceeds", ValidateSale(1, -1, "2026-01-01"), "proceeds_usd"},
		{"sale bad date", ValidateSale(1, 10, "nope"), "date"},

		// Photo — ext is a CLOSED path-segment whitelist; a bogus ext is a file written to a
		// wrong-shaped name. The set is {jpg,jpeg,png,webp,pdf} (om-9o4n.2 added pdf); anything
		// else is rejected, naming "ext".
		{"photo svg ext rejected", Photo{OwnerKind: "lot", OwnerUID: "u", Ext: "svg"}.Validate(), "ext"},
		{"photo exe ext rejected", Photo{OwnerKind: "lot", OwnerUID: "u", Ext: "exe"}.Validate(), "ext"},
		{"photo gif ext rejected", Photo{OwnerKind: "lot", OwnerUID: "u", Ext: "gif"}.Validate(), "ext"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.err == nil {
				t.Fatalf("expected a validation error, got nil")
			}
			if !errors.Is(c.err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid — the API cannot map it to a 400", c.err)
			}
			if !strings.Contains(c.err.Error(), c.field) {
				t.Errorf("message %q does not name field %q — the client cannot point at the cell", c.err.Error(), c.field)
			}
		})
	}
}

// The rules must ACCEPT the shapes the real fixture producers (demo seeder, legacy
// importer, spot poller, sample-data) actually write, or they reject valid data.
// A blank metal (clad junk types), a blank weight_unit, a 0 purity, a blank
// optional date, a "g"/"kg" unit, and a $0 face are all legitimate here.
func TestValidateAcceptsRealFixtureShapes(t *testing.T) {
	ok := []struct {
		name string
		err  error
	}{
		{"clad item type (blank metal, derived fine oz)", ItemType{Kind: "junk", Metal: "", FineOzEach: 0}.Validate()},
		{"bullion coin", ItemType{Kind: "coin", Metal: "gold", FineOzEach: 1.0}.Validate()},
		{"bullion holding, blank unit, 0 purity, 0 face", Holding{Activity: "bullion", Qty: 1, BasisUSD: 1512, Acquired: "2020-08-28"}.Validate()},
		{"bar holding, gross x purity, grams", Holding{Activity: "bullion", Qty: 1, GrossWeight: 311, Purity: 0.999, WeightUnit: "g", BasisUSD: 262, Acquired: "2021-01-01"}.Validate()},
		{"find holding, ozt, disposed set", Holding{Activity: "crh", Qty: 19, BasisUSD: 1.9, FaceValueUSD: 1.9, WeightUnit: "ozt", Acquired: "2025-03-10", Disposed: "2026-04-01", DisposedUSD: 210}.Validate()},
		{"buy txn", RollTxn{Action: "buy", Denom: "halves", Unit: "box", Amount: 1, FaceUSD: 500, Date: "2025-03-08"}.Validate()},
		{"denomless mixed return", RollTxn{Action: "return", Denom: "", Unit: "face", Amount: 480, FaceUSD: 480, Date: "2025-03-12"}.Validate()},
		{"return with no unit/amount (legacy import shape)", RollTxn{Action: "return", FaceUSD: 496, Date: "2025-03-12"}.Validate()},
		{"trip, no date is legal", Trip{Miles: 6, Hours: 0.5, Date: "2025-03-08"}.Validate()},
		{"synthesized branch (resolveBranchID shape)", Branch{Name: "Riverbend CU", Buys: true, Dumps: true, Active: true}.Validate()},
		{"branch with western hemisphere coords", Branch{Name: "x", Lat: -33.8, Lon: -122.4}.Validate()},
		{"supply", Supply{Item: "coin tubes", CostUSD: 8, Date: "2025-03-01"}.Validate()},
		{"legacy keeper, no date", Keeper{Denom: "halves", Count: 12, FaceUSD: 6}.Validate()},
		{"audited keeper, date set", Keeper{Denom: "dimes", Count: 90, FaceUSD: 9, Date: "2026-07-07"}.Validate()},
		{"loss", Loss{AmountUSD: 2.40, Reason: "machine miscount", Date: "2026-06-29"}.Validate()},
		{"poller spot (RFC3339 as_of not rejected)", Spot{AsOf: "2026-07-14T12:00:00Z", GoldUSD: 4000, SilverUSD: 60}.Validate()},
		{"default settings", DefaultSettings().Validate()},
		{"sale", ValidateSale(4, 240, "2026-04-01")},
		// Photo — every accepted attachment ext: the images, plus 'pdf' the document (om-9o4n.2).
		{"photo jpg", Photo{OwnerKind: "lot", OwnerUID: "u", Ext: "jpg"}.Validate()},
		{"photo png", Photo{OwnerKind: "lot", OwnerUID: "u", Ext: "png"}.Validate()},
		{"photo webp", Photo{OwnerKind: "lot", OwnerUID: "u", Ext: "webp"}.Validate()},
		{"photo pdf document", Photo{OwnerKind: "lot", OwnerUID: "u", Ext: "pdf"}.Validate()},
		{"photo pdf uppercase (stored lowercase)", Photo{OwnerKind: "roll_txn", OwnerUID: "u", Ext: "PDF"}.Validate()},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			if c.err != nil {
				t.Errorf("validation rejected a valid fixture shape: %v", c.err)
			}
		})
	}
}
