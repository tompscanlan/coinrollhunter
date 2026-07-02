package model

import "testing"

func TestGrossWeightToTroyOunces(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gross float64
		unit  string
		want  float64
	}{
		{name: "ozt unchanged", gross: 10, unit: "ozt", want: 10},
		{name: "grams converted", gross: 31.1034768, unit: "g", want: 1},
		{name: "kilograms converted", gross: 2, unit: "kg", want: 64.30149313725596},
		{name: "unknown unit fallback", gross: 5, unit: "lbs", want: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := grossWeightToTroyOunces(tc.gross, tc.unit)
			diff := got - tc.want
			if diff < 0 {
				diff = -diff
			}
			if diff > 1e-12 {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveUsesGrossWeightPurityAndUnit(t *testing.T) {
	h := Holding{
		ID:           1,
		ItemTypeID:   1,
		Qty:          1,
		GrossWeight:  31.1034768,
		Purity:       0.999,
		WeightUnit:   "g",
		BasisUSD:     100,
		FaceValueUSD: 0,
		Acquired:     "2026-07-02",
		Source:       "test",
	}
	itemType := ItemType{ID: 1, Name: "10g Silver Bar", Metal: "silver", Fineness: ".999", FineOzEach: 0}
	lot := Resolve(h, itemType)
	if got, want := lot.FineOzEach, 0.999; got != want {
		t.Fatalf("FineOzEach = %v, want %v", got, want)
	}
}
