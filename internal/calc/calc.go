// Package calc is the profitability engine — a faithful Go port of the
// prototype's portfolio.py compute(). It answers: is bullion up or down, is the
// hunt paying for itself, and have we redeposited everything not kept?
//
// It operates on a resolved model.Dataset (holdings already joined to their item
// types), so it is blind to the catalog/specimen storage split (ADR-003).
package calc

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// EnrichedLot is a lot with its market valuation filled in.
type EnrichedLot struct {
	model.Lot
	FineOz    float64  `json:"fine_oz"`
	MarketUSD float64  `json:"market_usd"`
	UnrealUSD float64  `json:"unreal_usd"`
	UnrealPct *float64 `json:"unreal_pct"` // null when basis is 0 (undefined % — UI shows "n/a"); JSON can't carry ±Inf
}

// Report is the full computed result, mirroring portfolio.py's compute() output.
type Report struct {
	Spot model.Spot    `json:"spot"`
	Lots []EnrichedLot `json:"lots"`

	// Bullion investment (mark-to-market)
	BullionBasis  float64 `json:"bullion_basis"`
	BullionMarket float64 `json:"bullion_market"`
	BullionUnreal float64 `json:"bullion_unreal"`
	BullionPct    float64 `json:"bullion_pct"`
	GoldOz        float64 `json:"gold_oz"`
	GoldBasis     float64 `json:"gold_basis"`
	GoldMarket    float64 `json:"gold_market"`

	// CRH finds
	FindOz         float64 `json:"find_oz"`
	FindCost       float64 `json:"find_cost"`       // face paid
	FindMelt       float64 `json:"find_melt"`       // full melt at spot
	FindRealizable float64 `json:"find_realizable"` // after dealer haircut

	// CRH operating costs
	Gas      float64 `json:"gas"`
	Hours    float64 `json:"hours"`
	Supplies float64 `json:"supplies"`
	OpCost   float64 `json:"op_cost"`

	// Shrinkage write-offs (ADR-005): face declared lost at reconcile. A real
	// cash cost — surfaced as its own line, not folded into op_cost — that both
	// closes the float and reduces CRH net.
	Losses float64 `json:"losses"`

	// Float / cash-in reconciliation
	Buys     float64 `json:"buys"`
	Returns  float64 `json:"returns"`
	CladFace float64 `json:"clad_face"`
	KeptFace float64 `json:"kept_face"`
	// DisposedFindFace is the face (basis) of SOLD CRH finds that stays on the
	// kept side of the float (om-co69 / ADR-008). It is a component of KeptFace
	// only — deliberately NOT part of CRH net or total basis (those are
	// live-only). KeptFace == CladFace + FindCost + DisposedFindFace.
	DisposedFindFace float64 `json:"disposed_find_face"`
	ToRedeposit      float64 `json:"to_redeposit"`
	Reconciled       bool    `json:"reconciled"`

	// Activity KPIs (ADR-006): coarse "how much hunting" stats over buy txns.
	BuyCount    int     `json:"buy_count"`    // number of buy roll-txns
	BranchCount int     `json:"branch_count"` // distinct non-empty bank strings among buys
	AvgBuyUSD   float64 `json:"avg_buy_usd"`  // mean face per buy (0 if no buys)

	// Box throughput
	BoxesByDenom map[string]float64 `json:"boxes_by_denom"`
	TotalBoxes   float64            `json:"total_boxes"`
	FaceSearched float64            `json:"face_searched"`
	BoxYields    []BoxYield         `json:"box_yields"` // per-box find attribution

	// CRH net
	CRHNetMelt float64 `json:"crh_net_melt"`
	CRHNetReal float64 `json:"crh_net_real"`
	CRHNetTime float64 `json:"crh_net_time"`
	// CRHNetLifetime is the "is coin roll hunting costing you money?" answer over
	// the WHOLE history of the hunt (om-nass): CRHNetReal + RealizedGainCRH. The
	// crhNet* figures above are live-only, so selling a winning find drops its
	// value out of them while the op costs that produced it remain — the headline
	// flips negative on a hunt that made money. This ADDS the sold finds' realized
	// P&L back in; it does not mutate CRHNetReal (which stays the "current
	// holdings" figure). Bullion realized gain is NOT part of it.
	CRHNetLifetime float64 `json:"crh_net_lifetime"`
	HourlyRate     float64 `json:"hourly_rate"`

	// Realized (sold holdings)
	Realized         []RealizedLot `json:"realized"`
	RealizedProceeds float64       `json:"realized_proceeds"`
	RealizedBasis    float64       `json:"realized_basis"`
	RealizedGain     float64       `json:"realized_gain"`
	// The realized gain split by activity, so a bullion sale can never be read as
	// a hunt result (om-nass). "Bullion" is everything that is not a CRH find —
	// including a disposed lot with a blank/unknown activity — so the identity
	// RealizedGain == RealizedGainCRH + RealizedGainBullion is exact for any
	// dataset. Only RealizedGainCRH feeds CRHNetLifetime.
	RealizedGainCRH     float64 `json:"realized_gain_crh"`
	RealizedGainBullion float64 `json:"realized_gain_bullion"`

	// Whole portfolio
	TotalBasis  float64 `json:"total_basis"`
	TotalMarket float64 `json:"total_market"`
	TotalUnreal float64 `json:"total_unreal"`

	// Anomalies lists the lots whose free-text classification did NOT resolve, so
	// the money math had to fall back (om-t0fs). Purely ADDITIVE: Compute still
	// returns the same Report, every existing field is untouched, and a consumer
	// that doesn't know the key ignores it. Non-nil so it serializes as [] not null.
	// Rendering it is a separate bead (om-ay3b) — this field only records.
	Anomalies []Anomaly `json:"anomalies"`
}

// Anomaly is one lot whose metal or fineness string could not be classified. Both
// kinds silently corrupted money before om-t0fs: an unrecognized metal values the
// whole lot at $0 spot, and an unparseable fineness on silver used to pay FULL
// melt (no dealer haircut). Each one names the offending row and the offending
// value so the data can actually be found and fixed.
type Anomaly struct {
	LotID   int64  `json:"lot_id"`
	Product string `json:"product"` // the row, in human terms
	Field   string `json:"field"`   // "metal" | "fineness"
	Value   string `json:"value"`   // the offending free text, verbatim
	Detail  string `json:"detail"`  // what the math did about it
}

// RealizedLot is a sold holding with its realized gain (proceeds - basis).
type RealizedLot struct {
	model.DisposedLot
	GainUSD float64 `json:"gain_usd"`
}

// BoxYield attributes CRH finds to the box (roll txn) they were pulled from, so
// you can see which banks/boxes actually produce silver. One per buy txn.
type BoxYield struct {
	RollTxnID    int64   `json:"roll_txn_id"`
	Date         string  `json:"date"`
	Bank         string  `json:"bank"`      // resolved branch name (ADR-010)
	BranchID     int64   `json:"branch_id"` // stable grouping key; survives a rename/merge
	Denom        string  `json:"denom"`
	FaceUSD      float64 `json:"face_usd"`
	FindCount    int     `json:"find_count"`
	FindOz       float64 `json:"find_oz"`
	FindValueUSD float64 `json:"find_value_usd"` // realizable (after dealer haircut)
	YieldPct     float64 `json:"yield_pct"`      // find_value / face * 100
}

// --- classification of free-text metal + fineness (om-t0fs) ------------------
//
// `metal` and `fineness` are human text that reaches the money math directly, and
// om-1czp's validation only guards NEW writes — historical rows, legacy imports,
// and hand-edited databases never pass through it, so THIS is the only defense
// they meet. Both halves used to fail silently and in the flattering direction:
// metal="Silver" valued at $0 spot, and a fineness of ".900" paid full melt with
// no dealer haircut (an ~11% overstatement). So: normalize first, classify by
// range, and record what does not resolve (Report.Anomalies) instead of guessing.

// normalizeMetal folds a free-text metal to the canonical form the spot table is
// keyed on, tolerating case and surrounding whitespace ("Silver", " SILVER " ->
// "silver"). ok reports whether it resolved to a metal we actually price; a BLANK
// metal returns ("", false) just like an unknown one, because both value at $0 —
// but only a NON-BLANK one is an anomaly (see Compute). Blank is legal and
// load-bearing: clad "junk" types (error coins, world coins) carry no melt metal
// (model/validate.go), and flagging them would fire on correct data.
func normalizeMetal(metal string) (string, bool) {
	m := strings.ToLower(strings.TrimSpace(metal))
	switch m {
	case "gold", "silver", "platinum", "palladium":
		return m, true
	default:
		return m, false
	}
}

// spotFor returns the spot price for a metal; any metal without a price column
// (including a blank one) contributes 0.
func spotFor(s model.Spot, metal string) float64 {
	m, ok := normalizeMetal(metal)
	if !ok {
		return 0
	}
	switch m {
	case "gold":
		return s.GoldUSD
	case "silver":
		return s.SilverUSD
	case "platinum":
		return s.PlatinumUSD
	case "palladium":
		return s.PalladiumUSD
	default:
		return 0
	}
}

// fineClass says whether a fineness string carried a usable number.
type fineClass int

const (
	fineUnstated    fineClass = iota // blank: not stated. NOT an anomaly — an absent fineness is not a corrupt one, and plenty of legitimate rows leave it empty.
	fineParsed                       // a numeric fine fraction in (0,1]
	fineUnparseable                  // non-blank, but not a fineness ("40 grain", "junk")
)

// finenessFraction normalizes free-text fineness to a numeric fine fraction in
// (0,1]. It accepts the notations that actually show up in a coin ledger:
//
//	"90%" · " 40% " · "90 %"          percent
//	".900" · "0.900" · ".9999"        decimal fraction
//	"90" · "900" · "9999"             bare number, scaled by magnitude
//	"22k .9167" · "22 karat"          karat (n/24)
//	"80% (CAD)" · "90% (pre-1965)"    a marked number plus annotation
//
// The load-bearing rule is the one that makes "40 grain" UNPARSEABLE rather than
// 40%: a BARE number (no %, no decimal point, no karat) is only a fineness if it
// stands alone or is followed by an explicit fineness word. A number carrying any
// other unit is a WEIGHT, and mistaking it for a fineness is what haircut this lot
// at the 40% rate. A number that *is* explicitly marked (%, ".", karat) may be
// followed by free annotation — "80% (CAD)" is a real fineness with a note on it.
func finenessFraction(raw string) (float64, fineClass) {
	fields := strings.Fields(strings.ToLower(raw))
	if len(fields) == 0 {
		return 0, fineUnstated
	}
	head, rest := fields[0], fields[1:]

	percent := strings.HasSuffix(head, "%")
	head = strings.TrimSuffix(head, "%")
	karat := false
	if !percent {
		for _, suf := range []string{"karat", "carat", "kt", "k"} {
			if len(head) > len(suf) && strings.HasSuffix(head, suf) {
				head, karat = strings.TrimSuffix(head, suf), true
				break
			}
		}
	}
	// An explicit marker (%, karat, or a decimal point) means the number is stated
	// as a fineness, so anything after it is annotation we can ignore.
	marked := percent || karat || strings.Contains(head, ".")

	n, err := strconv.ParseFloat(head, 64)
	if err != nil {
		return 0, fineUnparseable
	}

	if !marked && len(rest) > 0 {
		switch rest[0] {
		case "%":
			percent = true
		case "k", "kt", "karat", "carat":
			karat = true
		case "fine", "fineness": // "900 fine" — the number IS the fineness
		default:
			return 0, fineUnparseable // "40 grain": a unit, therefore a weight
		}
	}

	var f float64
	switch {
	case percent:
		f = n / 100
	case karat:
		f = n / 24
	default:
		// A bare number, scaled by magnitude: 0.9 · 90 · 900 · 9999 all mean .900+.
		switch {
		case n <= 1:
			f = n
		case n <= 100:
			f = n / 100
		case n <= 1000:
			f = n / 1000
		case n <= 10000:
			f = n / 10000
		}
	}
	if f <= 0 || f > 1 {
		return 0, fineUnparseable // not a fine fraction: "900%", "40k", "-1"
	}
	return f, fineParsed
}

// The junk-silver buckets, as RANGES over the normalized fine fraction. The
// tolerance is wide enough for notation noise (".900" == "90%" == "900") and tight
// enough to keep the things that are NOT US junk silver out of the buckets:
// sterling (.925), 22k (.9167) and world 80% coin all fall outside and keep full
// melt, exactly as they did under the old prefix match.
const fineTol = 0.015

func near(f, target float64) bool { return math.Abs(f-target) <= fineTol }

// conservativeBuyback is the factor for a SILVER lot whose fineness we could not
// parse (AC6). The old code returned 1.0 — full melt, the single most OPTIMISTIC
// number available — which is precisely the bug: bad data silently flattered the
// books. We cannot know the grade, so we assume the worst dealer haircut we know
// about instead of the best. It is deliberately never > 1.0 (a haircut can't make
// metal worth more than melt), and the lot is reported in Report.Anomalies so the
// number is explained rather than merely quiet.
func conservativeBuyback(s model.Settings) float64 {
	return math.Min(1.0, math.Min(s.SilverBuyback40pct, s.SilverBuyback90pct))
}

// enrich values a lot at spot. Mirrors portfolio.py enrich().
func enrich(l model.Lot, s model.Spot) EnrichedLot {
	fineOz := l.Qty * l.FineOzEach
	market := fineOz * spotFor(s, l.Metal)
	basis := l.BasisUSD
	unreal := market - basis
	// Percent return is undefined when basis is 0 (infinite return on zero cost);
	// emit null rather than ±Inf, which json.Marshal can't encode (a single Inf
	// would fail the whole /summary response). The UI renders null as "n/a".
	var pct *float64
	if basis != 0 {
		p := unreal / basis * 100
		pct = &p
	}
	return EnrichedLot{Lot: l, FineOz: fineOz, MarketUSD: market, UnrealUSD: unreal, UnrealPct: pct}
}

// buybackFactor is the estimated realizable dealer payout vs melt for junk
// silver. Mirrors portfolio.py buyback_factor(), but classifies off the NORMALIZED
// numeric fineness rather than prefix-matching the raw string (om-t0fs): the old
// strings.HasPrefix read ".900" as "not junk" (full melt, an 11% overstatement)
// and "40 grain" as 40% junk (a haircut on a weight).
func buybackFactor(l model.Lot, s model.Settings) float64 {
	// The metal guard folds case/whitespace exactly like spotFor, or a "Silver"
	// row would take the silver SPOT price and then skip the silver HAIRCUT — both
	// halves of the money math wrong on the same row.
	if m, _ := normalizeMetal(l.Metal); m != "silver" {
		return 1.0
	}
	f, class := finenessFraction(l.Fineness)
	switch class {
	case fineUnstated:
		return 1.0 // no fineness claimed ⇒ nothing to haircut against; unchanged
	case fineUnparseable:
		return conservativeBuyback(s) // AC6 — see conservativeBuyback
	}
	switch {
	case near(f, 0.40):
		return s.SilverBuyback40pct
	case near(f, 0.35):
		// War nickels (1942–45) are low-grade junk; dealers haircut them at
		// least as hard as 40%. The prototype left these at 1.0 (no haircut),
		// which overstates realizable value — lump them with 40% instead.
		return s.SilverBuyback40pct
	case near(f, 0.90):
		return s.SilverBuyback90pct
	default:
		// A parseable fineness that is not a US junk bucket (.999 rounds, .925
		// sterling, 80% world coin): dealers pay near melt. No haircut, and NOT an
		// anomaly — we understood the number, it just isn't junk silver.
		return 1.0
	}
}

// classify records the lots whose free-text metal/fineness did not resolve, so a
// fallback in the money math is never silent (om-t0fs AC5). Two kinds, and only
// two:
//
//	(a) a NON-BLANK metal that resolves to no priced metal   -> valued at $0 spot
//	(b) an UNPARSEABLE fineness on a silver lot              -> conservative haircut
//
// A BLANK metal is NOT an anomaly. It is legal and load-bearing — clad "junk"
// types (error coins, world coins) carry no melt metal and correctly value at $0
// (model/validate.go). Flagging it would fire on correct data across a huge share
// of the ledger, which is how a warning becomes noise and stops being read.
func classify(lots []model.Lot) []Anomaly {
	// Non-nil so it serializes as [] not null (the summary handler writes the
	// Report straight out).
	out := []Anomaly{}
	for _, l := range lots {
		m, known := normalizeMetal(l.Metal)
		if !known && m != "" {
			out = append(out, Anomaly{
				LotID: l.ID, Product: l.Product, Field: "metal", Value: l.Metal,
				Detail: fmt.Sprintf("metal %q is not a metal we price (gold, silver, platinum, palladium) — this lot is valued at $0 spot", l.Metal),
			})
		}
		if m != "silver" {
			continue // the fineness only reaches the money math on silver
		}
		if _, class := finenessFraction(l.Fineness); class == fineUnparseable {
			out = append(out, Anomaly{
				LotID: l.ID, Product: l.Product, Field: "fineness", Value: l.Fineness,
				Detail: fmt.Sprintf("fineness %q is not a fineness — the dealer haircut fell back to the most conservative known factor", l.Fineness),
			})
		}
	}
	return out
}

// Compute runs the full engine over a resolved dataset. Faithful port of
// portfolio.py compute(); box throughput is derived from normalized face
// (ADR-001 R7) rather than an explicit boxes field.
func Compute(d model.Dataset) Report {
	s := d.Settings
	spot := d.Spot

	lots := make([]EnrichedLot, len(d.Lots))
	for i, l := range d.Lots {
		lots[i] = enrich(l, spot)
	}

	var bullion, finds []EnrichedLot
	for _, l := range lots {
		if l.IsFind() {
			finds = append(finds, l)
		} else {
			bullion = append(bullion, l)
		}
	}

	// --- Bullion investment ---
	var bBasis, bMarket float64
	var gOz, gBasis, gMarket float64
	for _, l := range bullion {
		bBasis += l.BasisUSD
		bMarket += l.MarketUSD
		// Normalized like spotFor, or a "Gold" row would be valued at gold spot in
		// bullion_market yet vanish from the gold_* KPIs.
		if m, _ := normalizeMetal(l.Metal); m == "gold" {
			gOz += l.FineOz
			gBasis += l.BasisUSD
			gMarket += l.MarketUSD
		}
	}
	bUnreal := bMarket - bBasis

	// --- CRH finds ---
	var fCost, fMelt, fRealizable, fOz float64
	for _, l := range finds {
		fCost += l.BasisUSD
		fMelt += l.MarketUSD
		fRealizable += l.MarketUSD * buybackFactor(l.Lot, s)
		fOz += l.FineOz
	}

	// --- CRH operating costs ---
	var gas, hours float64
	for _, t := range d.Trips {
		gas += t.Miles * s.IRSMileageRate
		hours += t.Hours
	}
	var supplies float64
	for _, x := range d.Supplies {
		supplies += x.CostUSD
	}
	opCost := gas + supplies

	// --- float, kept, cash-in reconciliation (+ activity KPIs, ADR-006) ---
	var buys, returns float64
	var buyCount int
	// Count distinct branches by the stable branch_id (ADR-010), not the free-text
	// name — so a typo fork that's been merged, or a branch that's been renamed,
	// counts once. Buys with no branch link (branch_id 0) don't count.
	branches := map[int64]bool{}
	for _, t := range d.RollTxns {
		switch t.Action {
		case "buy":
			buys += t.FaceUSD
			buyCount++
			if t.BranchID != 0 {
				branches[t.BranchID] = true
			}
		case "return":
			returns += t.FaceUSD
		}
	}
	avgBuy := 0.0
	if buyCount > 0 {
		avgBuy = buys / float64(buyCount)
	}
	var cladFace float64
	for _, k := range d.Keepers {
		cladFace += k.FaceUSD
	}
	// Shrinkage write-offs (ADR-005): face declared lost at reconcile. It closes
	// the float just like a return, but represents value gone rather than coin
	// recovered, so it is tracked separately and also hits CRH net below.
	var losses float64
	for _, l := range d.Losses {
		losses += l.AmountUSD
	}
	// (c) om-co69: a sold (disposed) CRH find's face STAYS on the kept side of the
	// float permanently. to_redeposit reconciles the ORIGINAL find-time float (the
	// dollars pulled off the search table), not live inventory, so a later sale
	// must not reopen a float that was already reconciled. This is a FLOAT-ONLY
	// term: it feeds keptFace only and must NOT enter CRH net (crhNet* below) or
	// total basis (tBasis) — those stay live-only via fCost, and a disposed find's
	// P&L is realized separately as proceeds − basis (see ADR-005 + ADR-008).
	var disposedFindFace float64
	for _, dl := range d.Disposed {
		if dl.Activity == "crh" {
			disposedFindFace += dl.BasisUSD
		}
	}
	keptFindFace := fCost + disposedFindFace // live + disposed CRH find face
	keptFace := cladFace + keptFindFace
	toRedeposit := buys - returns - keptFace - losses
	reconciled := math.Abs(toRedeposit) < 0.01

	// --- box throughput (derived from normalized face; ADR-001 R7) ---
	boxesByDenom := map[string]float64{}
	var totalBoxes float64
	for _, t := range d.RollTxns {
		if t.Action != "buy" {
			continue
		}
		boxFace, ok := s.BoxFaceUSD[t.Denom]
		if !ok || boxFace == 0 {
			continue
		}
		b := t.FaceUSD / boxFace
		boxesByDenom[t.Denom] += b
		totalBoxes += b
	}

	// --- CRH net --- (losses are a real cash cost; subtract alongside op cost)
	crhNetMelt := fMelt - fCost - opCost - losses
	crhNetReal := fRealizable - fCost - opCost - losses
	crhNetTime := crhNetReal - hours*s.HourlyRateUSD

	// --- per-box yield (which boxes/banks actually produced silver) ---
	boxByID := map[int64]*BoxYield{}
	for _, t := range d.RollTxns {
		if t.Action != "buy" {
			continue
		}
		boxByID[t.ID] = &BoxYield{RollTxnID: t.ID, Date: t.Date, Bank: t.Bank, BranchID: t.BranchID, Denom: t.Denom, FaceUSD: t.FaceUSD}
	}
	for _, l := range finds {
		if l.RollTxnID == 0 {
			continue
		}
		if by, ok := boxByID[l.RollTxnID]; ok {
			by.FindValueUSD += l.MarketUSD * buybackFactor(l.Lot, s)
			by.FindOz += l.FineOz
			by.FindCount++
		}
	}
	// Non-nil so it serializes as [] not null (the summary handler writes the
	// Report directly; an empty DB must not crash the UI's {#if ...length}).
	boxYields := []BoxYield{}
	for _, t := range d.RollTxns {
		if t.Action != "buy" {
			continue
		}
		by := boxByID[t.ID]
		if by.FaceUSD != 0 {
			by.YieldPct = by.FindValueUSD / by.FaceUSD * 100
		}
		boxYields = append(boxYields, *by)
	}

	// --- realized (sold holdings) ---
	// om-nass: split the realized gain by activity while we already have the lot
	// in hand. A CRH find's P&L is realized here — separately from crhNet* above,
	// which is live-only by ADR-008 (c) — and rGainCRH is what folds it back into
	// the LIFETIME figure below. Note this touches the GAIN (proceeds − basis), a
	// different quantity from the disposed find's BASIS: the basis stays float-only
	// (disposedFindFace → keptFace) and is still barred from fCost / crhNet* /
	// tBasis. Widening fCost is the one-liner ADR-008 §Alternatives rejects.
	//
	// "Bullion" is deliberately NOT-crh (else), not Activity == "bullion", so a lot
	// with a blank/unknown activity still lands somewhere and
	// realized_gain == realized_gain_crh + realized_gain_bullion holds exactly.
	// (Mirrors the UI's `activity !== 'crh'` bullion filter.)
	realized := make([]RealizedLot, len(d.Disposed))
	var rProceeds, rBasis float64
	var rGainCRH, rGainBullion float64
	for i, dl := range d.Disposed {
		g := dl.ProceedsUSD - dl.BasisUSD
		realized[i] = RealizedLot{DisposedLot: dl, GainUSD: g}
		rProceeds += dl.ProceedsUSD
		rBasis += dl.BasisUSD
		if dl.Activity == "crh" {
			rGainCRH += g
		} else {
			rGainBullion += g
		}
	}
	// The lifetime answer to "is the hunt costing you money?": the live finds you
	// still hold, plus what the finds you already sold actually earned. Bullion
	// realized gain is excluded — it is a separate long-term hold, not a hunt result.
	crhNetLifetime := crhNetReal + rGainCRH

	// --- totals ---
	tBasis := bBasis + fCost
	tMarket := bMarket + fRealizable

	bPct := 0.0
	if bBasis != 0 {
		bPct = bUnreal / bBasis * 100
	}

	return Report{
		Spot: spot,
		Lots: lots,

		BullionBasis:  bBasis,
		BullionMarket: bMarket,
		BullionUnreal: bUnreal,
		BullionPct:    bPct,
		GoldOz:        gOz,
		GoldBasis:     gBasis,
		GoldMarket:    gMarket,

		FindOz:         fOz,
		FindCost:       fCost,
		FindMelt:       fMelt,
		FindRealizable: fRealizable,

		Gas:      gas,
		Hours:    hours,
		Supplies: supplies,
		OpCost:   opCost,
		Losses:   losses,

		Buys:             buys,
		Returns:          returns,
		CladFace:         cladFace,
		KeptFace:         keptFace,
		DisposedFindFace: disposedFindFace,
		ToRedeposit:      toRedeposit,
		Reconciled:       reconciled,

		BuyCount:    buyCount,
		BranchCount: len(branches),
		AvgBuyUSD:   avgBuy,

		BoxesByDenom: boxesByDenom,
		TotalBoxes:   totalBoxes,
		FaceSearched: buys,
		BoxYields:    boxYields,

		CRHNetMelt:     crhNetMelt,
		CRHNetReal:     crhNetReal,
		CRHNetTime:     crhNetTime,
		CRHNetLifetime: crhNetLifetime,
		HourlyRate:     s.HourlyRateUSD,

		Realized:            realized,
		RealizedProceeds:    rProceeds,
		RealizedBasis:       rBasis,
		RealizedGain:        rProceeds - rBasis,
		RealizedGainCRH:     rGainCRH,
		RealizedGainBullion: rGainBullion,

		TotalBasis:  tBasis,
		TotalMarket: tMarket,
		TotalUnreal: tMarket - tBasis,

		// Additive (om-t0fs): the rows whose classification did not resolve. Live
		// lots only — a disposed lot's P&L is proceeds − basis, so its metal never
		// reaches spot and cannot silently misvalue anything.
		Anomalies: classify(d.Lots),
	}
}

// Verdict summarizes the CRH cash outcome. Mirrors portfolio.py verdict().
func (r Report) Verdict() string {
	switch {
	case r.CRHNetReal > 0:
		return "PROFITABLE (cash basis)"
	case r.CRHNetReal == 0:
		return "BREAK-EVEN"
	default:
		return "COSTING MONEY"
	}
}
