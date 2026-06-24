// Package model holds the core domain types shared by the store, calc, and API
// layers. They mirror the SQLite schema in ADR-001 + ADR-003.
//
// Storage uses a catalog/specimen split (ADR-003): an ItemType catalog holds
// reference data for a kind of thing, and Holding rows are the specimens you own
// that point at an ItemType. The calc engine, however, consumes a flat resolved
// view (Lot) produced by joining a Holding to its ItemType — so the math stays a
// faithful port of the prototype and is blind to the storage split.
package model

// ItemType is catalog/reference data for a kind of thing (a coin type, a bar
// product, a junk-silver denomination). Entered once, shared by many holdings.
type ItemType struct {
	ID         int64   `json:"id"`
	Kind       string  `json:"kind"`           // coin|round|bar|junk|jewelry|other
	Name       string  `json:"name"`           // "1 oz American Gold Eagle"
	Metal      string  `json:"metal"`          // gold|silver|platinum|palladium
	ASWOz      float64 `json:"asw_oz"`         // actual metal weight per unit (0 if derived from gross*purity)
	Fineness   string  `json:"fineness"`       // "90%", "22k .9167", ".9999"
	Year       string  `json:"year,omitempty"` // optional mint year
	Mint       string  `json:"mint,omitempty"` // optional mint/maker
	Mintmark   string  `json:"mintmark,omitempty"`
	References string  `json:"references,omitempty"` // JSON: {"pcgs":..., "km":...}
}

// Holding is a specimen you own — a quantity of some ItemType, with acquisition,
// custody, and disposal data. ADR-003.
type Holding struct {
	ID           int64   `json:"id"`
	ItemTypeID   int64   `json:"item_type_id"`
	Activity     string  `json:"activity"` // "bullion" | "crh"
	Qty          float64 `json:"qty"`
	GrossWeight  float64 `json:"gross_weight,omitempty"` // per unit; with Purity derives fine oz when ASWOz==0
	Purity       float64 `json:"purity,omitempty"`       // 0..1
	WeightUnit   string  `json:"weight_unit,omitempty"`  // "ozt" | "g" | "kg"
	BasisUSD     float64 `json:"basis_usd"`              // total paid (face for CRH finds)
	PremiumUSD   float64 `json:"premium_usd,omitempty"`  // paid over melt at acquisition
	FaceValueUSD float64 `json:"face_value_usd"`
	Acquired     string  `json:"acquired"` // ISO date
	Source       string  `json:"source"`
	Location     string  `json:"location,omitempty"` // custody: home safe, SDB, depository
	InsuredValue float64 `json:"insured_value,omitempty"`
	Attributes   string  `json:"attributes,omitempty"` // JSON escape hatch (grade, cert#, gemstone, hallmark)
	Notes        string  `json:"notes"`
	Disposed     string  `json:"disposed,omitempty"` // ISO date if sold
	DisposedUSD  float64 `json:"disposed_usd,omitempty"`
}

// Lot is the flat, resolved engine view of a holding (Holding joined to its
// ItemType). This is what calc operates on; it mirrors the prototype's lot shape
// so the ported math is unchanged.
type Lot struct {
	ID           int64   `json:"id"`
	Activity     string  `json:"activity"` // "bullion" | "crh"
	Product      string  `json:"product"`  // from ItemType.Name
	Metal        string  `json:"metal"`    // from ItemType.Metal
	Fineness     string  `json:"fineness"` // from ItemType.Fineness
	Qty          float64 `json:"qty"`
	FineOzEach   float64 `json:"fine_oz_each"` // resolved: ASWOz, else GrossWeight*Purity
	BasisUSD     float64 `json:"basis_usd"`
	FaceValueUSD float64 `json:"face_value_usd"`
	Acquired     string  `json:"acquired"`
	Source       string  `json:"source"`
}

// IsFind reports whether the lot is a coin-roll-hunting find (vs. bullion).
func (l Lot) IsFind() bool { return l.Activity == "crh" }

// Resolve joins a holding to its item type to produce the flat engine view.
// Fine ounces per unit come from the catalog's ASWOz when set, otherwise from the
// specimen's gross weight * purity (bars, generic rounds, jewelry).
func Resolve(h Holding, t ItemType) Lot {
	fineEach := t.ASWOz
	if fineEach == 0 {
		fineEach = h.GrossWeight * h.Purity
	}
	return Lot{
		ID:           h.ID,
		Activity:     h.Activity,
		Product:      t.Name,
		Metal:        t.Metal,
		Fineness:     t.Fineness,
		Qty:          h.Qty,
		FineOzEach:   fineEach,
		BasisUSD:     h.BasisUSD,
		FaceValueUSD: h.FaceValueUSD,
		Acquired:     h.Acquired,
		Source:       h.Source,
	}
}

// RollTxn is a single buy/return of coin against the float, normalized to face
// dollars. The entry unit (box/roll/face/coin) is preserved for display, but
// FaceUSD is the source of truth (see ADR-001 R7).
type RollTxn struct {
	ID      int64   `json:"id"`
	Date    string  `json:"date"`
	Bank    string  `json:"bank"`
	Action  string  `json:"action"` // "buy" | "return"
	Denom   string  `json:"denom"`  // halves|quarters|dimes|nickels|cents
	Unit    string  `json:"unit"`   // "box" | "roll" | "face" | "coin"
	Amount  float64 `json:"amount"` // quantity in that unit
	FaceUSD float64 `json:"face_usd"`
	Notes   string  `json:"notes"`
}

// Trip is a sourcing run; Miles drives the mileage-based gas cost.
type Trip struct {
	ID    int64   `json:"id"`
	Date  string  `json:"date"`
	Bank  string  `json:"bank"`
	Miles float64 `json:"miles"`
	Hours float64 `json:"hours"`
}

// Supply is a consumable cost of the hunt (tubes, flips, etc.).
type Supply struct {
	ID      int64   `json:"id"`
	Date    string  `json:"date"`
	Item    string  `json:"item"`
	CostUSD float64 `json:"cost_usd"`
}

// Keeper is clad coin parked at face (recoverable, not a loss) — kept out of the
// redeposit float.
type Keeper struct {
	ID      int64   `json:"id"`
	Denom   string  `json:"denom"`
	Count   int64   `json:"count"`
	FaceUSD float64 `json:"face_usd"`
}

// Spot is a metals price observation; every fetch is appended so we keep history.
type Spot struct {
	AsOf      string  `json:"as_of"`
	GoldUSD   float64 `json:"gold_usd"`
	SilverUSD float64 `json:"silver_usd"`
	Source    string  `json:"source"`
}

// Dataset is the full resolved in-memory store the calc engine operates on.
type Dataset struct {
	Lots     []Lot
	RollTxns []RollTxn
	Trips    []Trip
	Supplies []Supply
	Keepers  []Keeper
	Spot     Spot
	Settings Settings
}

// Settings holds the tunables the math depends on. Defaults match the prototype.
type Settings struct {
	ValueTime          bool               `json:"value_time"`
	HourlyRateUSD      float64            `json:"hourly_rate_usd"`
	IRSMileageRate     float64            `json:"irs_mileage_rate_usd_per_mile"`
	SilverBuyback40pct float64            `json:"silver_buyback_factor_40pct"`
	SilverBuyback90pct float64            `json:"silver_buyback_factor_90pct"`
	BoxFaceUSD         map[string]float64 `json:"box_face_usd"`
}

// DefaultSettings returns the prototype's defaults (used when a field is absent).
func DefaultSettings() Settings {
	return Settings{
		ValueTime:          false,
		HourlyRateUSD:      0,
		IRSMileageRate:     0.70,
		SilverBuyback40pct: 0.80,
		SilverBuyback90pct: 0.90,
		BoxFaceUSD: map[string]float64{
			"halves": 500, "quarters": 500, "dimes": 250, "nickels": 100, "cents": 25,
		},
	}
}
