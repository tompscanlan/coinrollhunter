package calc

import (
	"fmt"
	"math"
	"strconv"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// --- raw-invalid values reaching the money math -----------------------------
//
// Compute sums the lot fields RAW. There is no read-side validation anywhere in
// this package, and there deliberately isn't one now either: the write-time
// validators guard NEW writes only, so a historical row, a legacy import, or a
// hand-edited database carries whatever it carries, and `bBasis += l.BasisUSD`
// adds it. A single lot with basis_usd = -100 understates bullion_basis and
// total_basis by $100 and says nothing about it.
//
// The decision is FLAG, DON'T CHANGE, and each half of that is load-bearing:
//
//   - Don't REFUSE. One bad legacy row must not brick the summary — that is the
//     exact failure the no-CHECK decision was taken to avoid, and it would leave
//     the user with a blank app and no way to find the row.
//   - Don't CLAMP or EXCLUDE. A total that silently disagrees with the sum of the
//     rows the user can see is a worse lie than a total that is merely wrong: it
//     is wrong AND unauditable.
//   - So: the totals are bit-identical to what they were, and the row is named
//     here. "Opening bad data is reasonable; silently computing from it is not."
//
// Scope is what Compute actually SUMS. A bad date does not move a number, so it is
// not an anomaly here — internal/doctor reports it, along with every non-lots table.

// invalidValues records the lots whose raw field values cannot be true. Live lots
// AND disposed lots: a disposed lot's basis reaches realized_basis / realized_gain
// and, for a find, the float via disposed_find_face → kept_face → to_redeposit.
func invalidValues(d model.Dataset) []Anomaly {
	out := []Anomaly{}
	for _, l := range d.Lots {
		out = append(out, lotValueAnomalies(l.ID, l.Product, l.Activity, []numField{
			{"qty", l.Qty, "fine oz, and through it market value"},
			{"basis_usd", l.BasisUSD, basisDetail(l.IsFind())},
			{"face_value_usd", l.FaceValueUSD, "the row's face value"},
			{"premium_usd", l.PremiumUSD, "the displayed premium over melt"},
			{"fine_oz_each", l.FineOzEach, "fine oz, and through it market value"},
		})...)
	}
	for _, dl := range d.Disposed {
		out = append(out, lotValueAnomalies(dl.ID, dl.Product, dl.Activity, []numField{
			{"qty", dl.Qty, "the sold quantity"},
			{"basis_usd", dl.BasisUSD, "realized_basis and realized_gain" + disposedFloatNote(dl.Activity)},
			{"proceeds_usd", dl.ProceedsUSD, "realized_proceeds and realized_gain"},
		})...)
	}
	return out
}

// numField is one money/quantity column and the total it feeds, so the anomaly can
// say which number moved rather than only which column is wrong.
type numField struct {
	name  string
	value float64
	feeds string
}

func basisDetail(isFind bool) string {
	if isFind {
		return "find_cost, total_basis, and the float via kept_face"
	}
	return "bullion_basis and total_basis"
}

// disposedFloatNote names the extra total a SOLD FIND's basis reaches. A disposed
// find's face stays on the kept side of the float permanently, so a negative one
// does not just misreport history — it moves to_redeposit today.
func disposedFloatNote(activity string) string {
	if activity == "crh" {
		return ", and the float via disposed_find_face"
	}
	return ""
}

// lotValueAnomalies checks one lot's activity plus its numeric columns. Activity is
// checked here rather than per-field because it is not a quantity: a value outside
// {bullion, crh} does not make a number wrong, it makes the row land in the WRONG
// BUCKET — IsFind() is `activity == "crh"`, so anything else (including blank) is
// silently counted as bullion by every report.
func lotValueAnomalies(id int64, product, activity string, fields []numField) []Anomaly {
	var out []Anomaly
	if activity != "bullion" && activity != "crh" {
		out = append(out, Anomaly{
			LotID: id, Product: product, Field: "activity", Value: activity,
			Detail: fmt.Sprintf("activity %q is neither %q nor %q — this row is counted as bullion, and is invisible to every CRH report", activity, "bullion", "crh"),
		})
	}
	for _, f := range fields {
		// Order matters: NaN and ±Inf are not "< 0", so the finite check must come
		// first or a NaN basis passes as ordinary. It is also the more serious of the
		// two — see the comment on nonFinite.
		if !isFinite(f.value) {
			out = append(out, Anomaly{
				LotID: id, Product: product, Field: f.name, Value: formatValue(f.value),
				Detail: fmt.Sprintf("%s is not a finite number — it poisons %s, and any total it reaches cannot be encoded as JSON at all (see doctor)", f.name, f.feeds),
			})
			continue
		}
		if f.value < 0 {
			out = append(out, Anomaly{
				LotID: id, Product: product, Field: f.name, Value: formatValue(f.value),
				Detail: fmt.Sprintf("%s is negative — it was summed as-is into %s", f.name, f.feeds),
			})
		}
	}
	return out
}

// isFinite reports whether v is a real number. A NaN or ±Inf column value passes
// model's nonNeg (NaN < 0 is false) and then propagates through every sum that
// touches it, so it is strictly worse than a negative: it does not corrupt one
// total, it corrupts all of them. And because encoding/json cannot encode either
// one, a single such row makes GET /api/summary fail to serialize — the dashboard
// goes blank with no explanation, and THIS anomaly never reaches the browser
// either. That is why `coinrollhunter doctor` exists as a second door: it is the
// surface that still answers when the normal one cannot.
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// formatValue renders a column value for display. 'g' keeps a plain number plain
// ("-100" not "-100.000000") and still round-trips NaN/Inf as words.
func formatValue(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
