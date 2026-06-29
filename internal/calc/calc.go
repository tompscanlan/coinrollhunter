// Package calc is the profitability engine — a faithful Go port of the
// prototype's portfolio.py compute(). It answers: is bullion up or down, is the
// hunt paying for itself, and have we redeposited everything not kept?
//
// It operates on a resolved model.Dataset (holdings already joined to their item
// types), so it is blind to the catalog/specimen storage split (ADR-003).
package calc

import (
	"math"
	"strings"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// EnrichedLot is a lot with its market valuation filled in.
type EnrichedLot struct {
	model.Lot
	FineOz    float64 `json:"fine_oz"`
	MarketUSD float64 `json:"market_usd"`
	UnrealUSD float64 `json:"unreal_usd"`
	UnrealPct float64 `json:"unreal_pct"` // +Inf if basis is 0
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

	// Float / cash-in reconciliation
	Buys        float64 `json:"buys"`
	Returns     float64 `json:"returns"`
	CladFace    float64 `json:"clad_face"`
	KeptFace    float64 `json:"kept_face"`
	ToRedeposit float64 `json:"to_redeposit"`
	Reconciled  bool    `json:"reconciled"`

	// Box throughput
	BoxesByDenom map[string]float64 `json:"boxes_by_denom"`
	TotalBoxes   float64            `json:"total_boxes"`
	FaceSearched float64            `json:"face_searched"`
	BoxYields    []BoxYield         `json:"box_yields"` // per-box find attribution

	// CRH net
	CRHNetMelt float64 `json:"crh_net_melt"`
	CRHNetReal float64 `json:"crh_net_real"`
	CRHNetTime float64 `json:"crh_net_time"`
	HourlyRate float64 `json:"hourly_rate"`

	// Realized (sold holdings)
	Realized         []RealizedLot `json:"realized"`
	RealizedProceeds float64       `json:"realized_proceeds"`
	RealizedBasis    float64       `json:"realized_basis"`
	RealizedGain     float64       `json:"realized_gain"`

	// Whole portfolio
	TotalBasis  float64 `json:"total_basis"`
	TotalMarket float64 `json:"total_market"`
	TotalUnreal float64 `json:"total_unreal"`
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
	Bank         string  `json:"bank"`
	Denom        string  `json:"denom"`
	FaceUSD      float64 `json:"face_usd"`
	FindCount    int     `json:"find_count"`
	FindOz       float64 `json:"find_oz"`
	FindValueUSD float64 `json:"find_value_usd"` // realizable (after dealer haircut)
	YieldPct     float64 `json:"yield_pct"`      // find_value / face * 100
}

// spotFor returns the spot price for a metal; any metal without a price column
// contributes 0.
func spotFor(s model.Spot, metal string) float64 {
	switch metal {
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

// enrich values a lot at spot. Mirrors portfolio.py enrich().
func enrich(l model.Lot, s model.Spot) EnrichedLot {
	fineOz := l.Qty * l.FineOzEach
	market := fineOz * spotFor(s, l.Metal)
	basis := l.BasisUSD
	unreal := market - basis
	pct := math.Inf(1)
	if basis != 0 {
		pct = unreal / basis * 100
	}
	return EnrichedLot{Lot: l, FineOz: fineOz, MarketUSD: market, UnrealUSD: unreal, UnrealPct: pct}
}

// buybackFactor is the estimated realizable dealer payout vs melt for junk
// silver. Mirrors portfolio.py buyback_factor().
func buybackFactor(l model.Lot, s model.Settings) float64 {
	if l.Metal != "silver" {
		return 1.0
	}
	switch {
	case strings.HasPrefix(l.Fineness, "40"):
		return s.SilverBuyback40pct
	case strings.HasPrefix(l.Fineness, "35"):
		// War nickels (1942–45) are low-grade junk; dealers haircut them at
		// least as hard as 40%. The prototype left these at 1.0 (no haircut),
		// which overstates realizable value — lump them with 40% instead.
		return s.SilverBuyback40pct
	case strings.HasPrefix(l.Fineness, "90"):
		return s.SilverBuyback90pct
	default:
		return 1.0
	}
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
		if l.Metal == "gold" {
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

	// --- float, kept, cash-in reconciliation ---
	var buys, returns float64
	for _, t := range d.RollTxns {
		switch t.Action {
		case "buy":
			buys += t.FaceUSD
		case "return":
			returns += t.FaceUSD
		}
	}
	var cladFace float64
	for _, k := range d.Keepers {
		cladFace += k.FaceUSD
	}
	keptFace := cladFace + fCost
	toRedeposit := buys - returns - keptFace
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

	// --- CRH net ---
	crhNetMelt := fMelt - fCost - opCost
	crhNetReal := fRealizable - fCost - opCost
	crhNetTime := crhNetReal - hours*s.HourlyRateUSD

	// --- per-box yield (which boxes/banks actually produced silver) ---
	boxByID := map[int64]*BoxYield{}
	for _, t := range d.RollTxns {
		if t.Action != "buy" {
			continue
		}
		boxByID[t.ID] = &BoxYield{RollTxnID: t.ID, Date: t.Date, Bank: t.Bank, Denom: t.Denom, FaceUSD: t.FaceUSD}
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
	var boxYields []BoxYield
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
	realized := make([]RealizedLot, len(d.Disposed))
	var rProceeds, rBasis float64
	for i, dl := range d.Disposed {
		realized[i] = RealizedLot{DisposedLot: dl, GainUSD: dl.ProceedsUSD - dl.BasisUSD}
		rProceeds += dl.ProceedsUSD
		rBasis += dl.BasisUSD
	}

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

		Buys:        buys,
		Returns:     returns,
		CladFace:    cladFace,
		KeptFace:    keptFace,
		ToRedeposit: toRedeposit,
		Reconciled:  reconciled,

		BoxesByDenom: boxesByDenom,
		TotalBoxes:   totalBoxes,
		FaceSearched: buys,
		BoxYields:    boxYields,

		CRHNetMelt: crhNetMelt,
		CRHNetReal: crhNetReal,
		CRHNetTime: crhNetTime,
		HourlyRate: s.HourlyRateUSD,

		Realized:         realized,
		RealizedProceeds: rProceeds,
		RealizedBasis:    rBasis,
		RealizedGain:     rProceeds - rBasis,

		TotalBasis:  tBasis,
		TotalMarket: tMarket,
		TotalUnreal: tMarket - tBasis,
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
