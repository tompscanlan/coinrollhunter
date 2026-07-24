package calc

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// find returns the anomalies for one lot id + field, so a test can assert about a
// specific finding without depending on the order the two producers run in.
func anomaliesFor(as []Anomaly, lotID int64, field string) []Anomaly {
	var out []Anomaly
	for _, a := range as {
		if a.LotID == lotID && a.Field == field {
			out = append(out, a)
		}
	}
	return out
}

// TestInvalidBasisIsFlaggedAndStillSummed is the whole FLAG-DON'T-CHANGE decision
// in one test: the negative basis MUST show up as an anomaly, and every total MUST
// be bit-identical to what it was before the flag existed.
//
// The second half is the part that is easy to "improve" into a bug. Excluding or
// clamping the bad row would make bullion_basis disagree with the sum of the rows
// the user can see in the grid — wrong AND unauditable, which is worse than wrong.
// If this test ever fails because someone made Compute skip the row, the fix is to
// revert that, not to update the expectation.
func TestInvalidBasisIsFlaggedAndStillSummed(t *testing.T) {
	d := model.Dataset{
		Spot:     model.Spot{GoldUSD: 4000, SilverUSD: 60},
		Settings: model.DefaultSettings(),
		Lots: []model.Lot{
			{ID: 1, Activity: "bullion", Product: "Gold Eagle", Metal: "gold", Qty: 1, FineOzEach: 1, BasisUSD: 500},
			// A raw-inserted negative basis no validator ever saw.
			{ID: 2, Activity: "bullion", Product: "hand-edited bar", Metal: "silver", Qty: 1, FineOzEach: 1, BasisUSD: -100},
		},
	}
	r := Compute(d)

	got := anomaliesFor(r.Anomalies, 2, "basis_usd")
	if len(got) != 1 {
		t.Fatalf("got %d basis_usd anomalies for lot 2, want 1; all = %+v", len(got), r.Anomalies)
	}
	if got[0].Value != "-100" {
		t.Errorf("anomaly value = %q, want the offending number verbatim (-100)", got[0].Value)
	}
	if got[0].Product != "hand-edited bar" || got[0].Detail == "" {
		t.Errorf("an anomaly must name the row and say what the math did: %+v", got[0])
	}
	if a := anomaliesFor(r.Anomalies, 1, "basis_usd"); len(a) != 0 {
		t.Errorf("the clean lot was flagged: %+v", a)
	}

	// FLAG, DON'T CHANGE — the totals still sum the raw values exactly as before.
	approx(t, "bullion_basis still sums the negative row", r.BullionBasis, 400)
	approx(t, "total_basis still sums the negative row", r.TotalBasis, 400)
}

// TestInvalidValueAnomalyKinds pins the columns worth flagging and, just as
// importantly, the ones that must stay silent. Zero is a legal basis (a gift, a
// found coin) and a legal quantity; flagging it would fire on correct data, which
// is how a warning becomes noise nobody reads (the blank-metal lesson from the
// classification fix: only a non-blank bad value is worth flagging).
func TestInvalidValueAnomalyKinds(t *testing.T) {
	base := model.Lot{Activity: "bullion", Product: "row", Metal: "silver", Qty: 1, FineOzEach: 1}
	mut := func(f func(*model.Lot)) model.Lot { l := base; f(&l); return l }

	for _, tc := range []struct {
		name  string
		lot   model.Lot
		field string // "" = expect no anomaly at all
	}{
		{"negative qty", mut(func(l *model.Lot) { l.Qty = -1 }), "qty"},
		{"negative basis", mut(func(l *model.Lot) { l.BasisUSD = -1 }), "basis_usd"},
		{"negative face value", mut(func(l *model.Lot) { l.FaceValueUSD = -0.5 }), "face_value_usd"},
		{"negative premium", mut(func(l *model.Lot) { l.PremiumUSD = -3 }), "premium_usd"},
		{"negative fine oz", mut(func(l *model.Lot) { l.FineOzEach = -1 }), "fine_oz_each"},
		{"unknown activity", mut(func(l *model.Lot) { l.Activity = "bulion" }), "activity"},
		{"blank activity", mut(func(l *model.Lot) { l.Activity = "" }), "activity"},
		{"NaN basis", mut(func(l *model.Lot) { l.BasisUSD = math.NaN() }), "basis_usd"},
		{"+Inf basis", mut(func(l *model.Lot) { l.BasisUSD = math.Inf(1) }), "basis_usd"},

		{"zero basis is legal", mut(func(l *model.Lot) { l.BasisUSD = 0 }), ""},
		{"zero qty is legal", mut(func(l *model.Lot) { l.Qty = 0 }), ""},
		{"crh activity is legal", mut(func(l *model.Lot) { l.Activity = "crh" }), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.lot.ID = 9
			as := Compute(model.Dataset{Settings: model.DefaultSettings(), Lots: []model.Lot{tc.lot}}).Anomalies
			if tc.field == "" {
				if len(as) != 0 {
					t.Fatalf("correct data was flagged: %+v", as)
				}
				return
			}
			if len(as) != 1 || as[0].Field != tc.field {
				t.Fatalf("anomalies = %+v, want exactly one on field %q", as, tc.field)
			}
		})
	}
}

// TestDisposedLotValuesAreChecked closes the gap the classification fix leaves
// open. Classification is live-lots-only for a good reason (a sold lot's metal
// never reaches spot), but a sold lot's BASIS very much reaches the money:
// realized_basis, realized_gain, and — for a find — the float, because a disposed
// find's face stays on the kept side permanently. A negative one moves to_redeposit
// today.
func TestDisposedLotValuesAreChecked(t *testing.T) {
	d := model.Dataset{
		Settings: model.DefaultSettings(),
		Disposed: []model.DisposedLot{
			{ID: 11, Activity: "crh", Product: "sold half", Qty: 1, BasisUSD: -0.5, ProceedsUSD: 12},
			{ID: 12, Activity: "bullion", Product: "sold bar", Qty: 1, BasisUSD: 20, ProceedsUSD: -3},
		},
	}
	r := Compute(d)

	if a := anomaliesFor(r.Anomalies, 11, "basis_usd"); len(a) != 1 {
		t.Fatalf("a sold find's negative basis must be flagged; anomalies = %+v", r.Anomalies)
	} else if !strings.Contains(a[0].Detail, "float") {
		t.Errorf("a sold FIND's basis reaches the float — the detail should say so: %q", a[0].Detail)
	}
	if a := anomaliesFor(r.Anomalies, 12, "proceeds_usd"); len(a) != 1 {
		t.Fatalf("negative proceeds must be flagged; anomalies = %+v", r.Anomalies)
	}

	// Still summed raw: realized_basis = -0.5 + 20, realized_proceeds = 12 - 3.
	approx(t, "realized_basis sums the raw values", r.RealizedBasis, 19.5)
	approx(t, "realized_proceeds sums the raw values", r.RealizedProceeds, 9)
	approx(t, "the sold find's face still rides the float", r.DisposedFindFace, -0.5)
}

// TestNonFiniteBreaksTheSummaryResponse is the honest statement of what the
// Anomalies surface can and cannot do, and the reason `coinrollhunter doctor` is a
// second door rather than a convenience. A NaN or ±Inf column value passes model's
// nonNeg (NaN < 0 is false), poisons every total it touches, and then makes the
// whole Report FAIL TO ENCODE — so the browser gets no dashboard AND no anomaly.
// Only an out-of-band scan can tell the user what happened.
func TestNonFiniteBreaksTheSummaryResponse(t *testing.T) {
	d := model.Dataset{
		Settings: model.DefaultSettings(),
		Lots:     []model.Lot{{ID: 1, Activity: "bullion", Product: "poisoned", Qty: 1, BasisUSD: math.NaN()}},
	}
	r := Compute(d)

	if a := anomaliesFor(r.Anomalies, 1, "basis_usd"); len(a) != 1 {
		t.Fatalf("a NaN basis must be flagged; anomalies = %+v", r.Anomalies)
	}
	if !math.IsNaN(r.TotalBasis) {
		t.Errorf("total_basis = %v, want NaN — Compute still sums raw, it does not sanitize", r.TotalBasis)
	}
	if _, err := json.Marshal(r); err == nil {
		t.Error("expected the Report to fail to encode: if this ever passes, GET /api/summary can carry the warning itself and doctor's why-it-exists comment should be revisited")
	}
}

// TestSampleFixtureStillClean re-pins TestSampleReportNoAnomalies against the NEW
// producer as well: adding a second source of anomalies must not start firing on
// the committed correct fixture.
func TestSampleFixtureStillClean(t *testing.T) {
	if a := invalidValues(sampleDataset()); len(a) != 0 {
		t.Errorf("clean sample fixture produced value anomalies: %+v", a)
	}
}
