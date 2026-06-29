// Finds reporting (ADR-006): the "1 per face $" hit-rate view, modeled on a real
// hunter's dataset (OKF projects/coinrollhunter/crh-field-data-dimes.md).
//
// For each denomination it reports, per find category (and subcategory), how many face
// dollars you must search to find one — overall and broken down by acquisition source
// (machine_roll/customer_roll/box/bag/loose). Every cell carries its sample size and a
// low-confidence flag, because hit-rate is a sampling statistic: a bare point estimate is
// misleading at small N (the dataset's central lesson).
//
// It is pure reporting derived from the resolved dataset — finds (counts) and buys (face
// searched) — and adds no new inputs. Find counts include *disposed* finds (a sold trophy
// still happened), so "lifetime finds by category" survives coins leaving live inventory.
package calc

import (
	"sort"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// LowConfidenceN is the sample-size (coins found) below which a hit-rate cell is flagged
// low-confidence. Small enough to be a hint, not a hard gate.
const LowConfidenceN = 5

// FindsReport is the full hit-rate view, one section per denomination.
type FindsReport struct {
	TotalFaceSearched float64       `json:"total_face_searched"`
	LowConfidenceN    float64       `json:"low_confidence_n"`
	Sources           []string      `json:"sources"`      // source_types present, canonical order
	Unattributed      float64       `json:"unattributed"` // finds (coins) with no linked buy
	Denoms            []DenomReport `json:"denoms"`
}

// DenomReport is the hit-rate grid for one denomination.
type DenomReport struct {
	Denom         string             `json:"denom"`
	FaceSearched  float64            `json:"face_searched"`  // total buy face for this denom
	CoinsSearched float64            `json:"coins_searched"` // derived: face / per-coin face
	FaceBySource  map[string]float64 `json:"face_by_source"` // buy face per source_type
	Categories    []CategoryReport   `json:"categories"`
}

// CategoryReport is one find category's hit-rate (overall + per source), with its
// subcategory breakdown beneath it.
type CategoryReport struct {
	Category      string              `json:"category"`
	Count         float64             `json:"count"`        // coins found in this category
	HitPerFace    float64             `json:"hit_per_face"` // face searched / count ("1 per $X")
	LowConfidence bool                `json:"low_confidence"`
	BySource      []SourceCell        `json:"by_source"`
	Subcategories []SubcategoryReport `json:"subcategories,omitempty"`
}

// SubcategoryReport mirrors CategoryReport for a named subcategory (e.g. "Mercury").
type SubcategoryReport struct {
	Subcategory   string       `json:"subcategory"`
	Count         float64      `json:"count"`
	HitPerFace    float64      `json:"hit_per_face"`
	LowConfidence bool         `json:"low_confidence"`
	BySource      []SourceCell `json:"by_source"`
}

// SourceCell is one (category|subcategory) × source hit-rate cell.
type SourceCell struct {
	Source        string  `json:"source"`
	Count         float64 `json:"count"`
	HitPerFace    float64 `json:"hit_per_face"` // 0 when count is 0 (treat as N/A)
	LowConfidence bool    `json:"low_confidence"`
}

// findRec is the unified shape for a find, whether live or disposed.
type findRec struct {
	rollTxnID   int64
	category    string
	subcategory string
	qty         float64 // coins
}

// ComputeFindsReport computes the hit-rate view from a resolved dataset.
func ComputeFindsReport(d model.Dataset) FindsReport {
	// roll-txn lookup, for attributing a find to its buy's denom + source_type.
	txByID := make(map[int64]model.RollTxn, len(d.RollTxns))
	for _, t := range d.RollTxns {
		txByID[t.ID] = t
	}

	// Face searched per denom and per (denom, source_type), from buys.
	faceByDenom := map[string]float64{}
	faceByDenomSource := map[string]map[string]float64{}
	sourceSeen := map[string]bool{}
	var totalFace float64
	for _, t := range d.RollTxns {
		if t.Action != "buy" {
			continue
		}
		totalFace += t.FaceUSD
		faceByDenom[t.Denom] += t.FaceUSD
		if faceByDenomSource[t.Denom] == nil {
			faceByDenomSource[t.Denom] = map[string]float64{}
		}
		faceByDenomSource[t.Denom][t.SourceType] += t.FaceUSD
		sourceSeen[t.SourceType] = true
	}

	// Gather finds (live + disposed), attributed to a denom via their linked buy.
	var finds []findRec
	for _, l := range d.Lots {
		if l.IsFind() {
			finds = append(finds, findRec{l.RollTxnID, l.Category, l.Subcategory, l.Qty})
		}
	}
	for _, dl := range d.Disposed {
		if dl.Activity == "crh" {
			finds = append(finds, findRec{dl.RollTxnID, dl.Category, dl.Subcategory, dl.Qty})
		}
	}

	// counts[denom][category]; subCounts[denom][category][subcategory]; both also per source.
	type cnt struct {
		total    float64
		bySource map[string]float64
	}
	newCnt := func() *cnt { return &cnt{bySource: map[string]float64{}} }
	catCounts := map[string]map[string]*cnt{}            // denom -> category -> cnt
	subCounts := map[string]map[string]map[string]*cnt{} // denom -> category -> subcat -> cnt
	denomSeen := map[string]bool{}
	var unattributed float64

	for _, f := range finds {
		denom, source := "", ""
		if t, ok := txByID[f.rollTxnID]; ok && f.rollTxnID != 0 {
			denom, source = t.Denom, t.SourceType
		} else {
			unattributed += f.qty
		}
		denomSeen[denom] = true

		cat := f.category
		if cat == "" {
			cat = "Uncategorized"
		}
		if catCounts[denom] == nil {
			catCounts[denom] = map[string]*cnt{}
			subCounts[denom] = map[string]map[string]*cnt{}
		}
		if catCounts[denom][cat] == nil {
			catCounts[denom][cat] = newCnt()
			subCounts[denom][cat] = map[string]*cnt{}
		}
		catCounts[denom][cat].total += f.qty
		catCounts[denom][cat].bySource[source] += f.qty

		if f.subcategory != "" {
			if subCounts[denom][cat][f.subcategory] == nil {
				subCounts[denom][cat][f.subcategory] = newCnt()
			}
			subCounts[denom][cat][f.subcategory].total += f.qty
			subCounts[denom][cat][f.subcategory].bySource[source] += f.qty
		}
	}

	// Assemble, sorted for determinism.
	sources := sortedSources(sourceSeen)
	rep := FindsReport{
		TotalFaceSearched: totalFace,
		LowConfidenceN:    LowConfidenceN,
		Sources:           sources,
		Unattributed:      unattributed,
	}

	denoms := unionKeys(denomSeen, faceByDenom)
	sort.Slice(denoms, func(i, j int) bool { return denomLess(denoms[i], denoms[j]) })

	for _, denom := range denoms {
		dr := DenomReport{
			Denom:         denom,
			FaceSearched:  faceByDenom[denom],
			FaceBySource:  faceByDenomSource[denom],
			CoinsSearched: coinsSearched(denom, faceByDenom[denom]),
		}
		if dr.FaceBySource == nil {
			dr.FaceBySource = map[string]float64{}
		}
		faceSrc := faceByDenomSource[denom]

		var cats []string
		for c := range catCounts[denom] {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		for _, c := range cats {
			cc := catCounts[denom][c]
			cr := CategoryReport{
				Category:      c,
				Count:         cc.total,
				HitPerFace:    hit(faceByDenom[denom], cc.total),
				LowConfidence: cc.total < LowConfidenceN,
				BySource:      sourceCells(sources, cc.bySource, faceSrc),
			}
			var subs []string
			for sc := range subCounts[denom][c] {
				subs = append(subs, sc)
			}
			sort.Strings(subs)
			for _, sc := range subs {
				scc := subCounts[denom][c][sc]
				cr.Subcategories = append(cr.Subcategories, SubcategoryReport{
					Subcategory:   sc,
					Count:         scc.total,
					HitPerFace:    hit(faceByDenom[denom], scc.total),
					LowConfidence: scc.total < LowConfidenceN,
					BySource:      sourceCells(sources, scc.bySource, faceSrc),
				})
			}
			dr.Categories = append(dr.Categories, cr)
		}
		rep.Denoms = append(rep.Denoms, dr)
	}
	return rep
}

// hit is the "1 per face $" rate: face searched / count. 0 (treated as N/A) when count is 0.
func hit(face, count float64) float64 {
	if count <= 0 {
		return 0
	}
	return face / count
}

// sourceCells builds the per-source row for a category/subcategory, in canonical order.
func sourceCells(sources []string, counts, faceBySource map[string]float64) []SourceCell {
	cells := make([]SourceCell, 0, len(sources))
	for _, s := range sources {
		n := counts[s]
		cells = append(cells, SourceCell{
			Source:        s,
			Count:         n,
			HitPerFace:    hit(faceBySource[s], n),
			LowConfidence: n > 0 && n < LowConfidenceN,
		})
	}
	return cells
}

// coinsSearched derives coins from face for a known denom (0 if the denom is unknown).
func coinsSearched(denom string, face float64) float64 {
	v := coinFaceValue(denom)
	if v == 0 {
		return 0
	}
	return face / v
}

func coinFaceValue(denom string) float64 {
	switch denom {
	case "halves":
		return 0.50
	case "quarters":
		return 0.25
	case "dimes":
		return 0.10
	case "nickels":
		return 0.05
	case "cents", "pennies":
		return 0.01
	default:
		return 0
	}
}

// sortedSources returns the seen source_types in a stable, human-meaningful order
// (the high-yield channel first), unknown ("") last.
func sortedSources(seen map[string]bool) []string {
	order := map[string]int{
		"customer_roll": 0, "machine_roll": 1, "box": 2, "bag": 3, "loose": 4, "": 99,
	}
	var out []string
	for s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, iok := order[out[i]]
		oj, jok := order[out[j]]
		if iok && jok && oi != oj {
			return oi < oj
		}
		if iok != jok {
			return iok // known sources before unknown extras
		}
		return out[i] < out[j]
	})
	return out
}

// denomLess orders denoms by coin value (halves→cents), unknowns last alphabetically.
func denomLess(a, b string) bool {
	order := map[string]int{"halves": 0, "quarters": 1, "dimes": 2, "nickels": 3, "cents": 4, "": 99}
	oa, aok := order[a]
	ob, bok := order[b]
	if aok && bok && oa != ob {
		return oa < ob
	}
	if aok != bok {
		return aok
	}
	return a < b
}

// unionKeys returns the union of a set's keys and a map's keys.
func unionKeys(set map[string]bool, m map[string]float64) []string {
	seen := map[string]bool{}
	var out []string
	for k := range set {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range m {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}
