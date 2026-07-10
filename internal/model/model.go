// Package model holds the core domain types shared by the store, calc, and API
// layers. They mirror the SQLite schema in ADR-001 + ADR-003.
//
// Storage uses a catalog/specimen split (ADR-003): an ItemType catalog holds
// reference data for a kind of thing, and Holding rows are the specimens you own
// that point at an ItemType. The calc engine, however, consumes a flat resolved
// view (Lot) produced by joining a Holding to its ItemType — so the math stays a
// faithful port of the prototype and is blind to the storage split.
package model

import "strings"

// ItemType is catalog/reference data for a kind of thing (a coin type, a bar
// product, a junk-silver denomination). Entered once, shared by many holdings.
type ItemType struct {
	ID         int64   `json:"id"`
	Kind       string  `json:"kind"`           // coin|round|bar|junk|jewelry|other
	Name       string  `json:"name"`           // "1 oz American Gold Eagle"
	Metal      string  `json:"metal"`          // gold|silver|platinum|palladium
	FineOzEach float64 `json:"fine_oz_each"`   // fine metal oz per unit, troy (0 if derived from gross*purity)
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
	RollTxnID    int64   `json:"roll_txn_id,omitempty"` // box this find came from (0 = none)
	Activity     string  `json:"activity"`              // "bullion" | "crh"
	Qty          float64 `json:"qty"`
	GrossWeight  float64 `json:"gross_weight,omitempty"` // per unit; with Purity derives fine oz when FineOzEach==0
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
	// CRH find taxonomy (ADR-006): denom-scoped rollup buckets, open vocabulary. Trophy
	// flags a notable find for the highlights feed.
	Category    string  `json:"category,omitempty"`    // e.g. "Silver" | "PMD" | "Error" | "2009"
	Subcategory string  `json:"subcategory,omitempty"` // e.g. "Mercury" | "parking lot" | "major"
	Trophy      bool    `json:"trophy,omitempty"`
	Disposed    string  `json:"disposed,omitempty"` // ISO date if sold
	DisposedUSD float64 `json:"disposed_usd,omitempty"`
}

const gramsPerTroyOunce = 31.1034768

// grossWeightToTroyOunces normalizes a specimen's gross weight to troy ounces.
// Unknown units fall back to troy ounces (the historical behavior).
func grossWeightToTroyOunces(gross float64, unit string) float64 {
	switch strings.TrimSpace(strings.ToLower(unit)) {
	case "", "ozt", "troy_oz", "troyoz":
		return gross
	case "g", "gram", "grams":
		return gross / gramsPerTroyOunce
	case "kg", "kilogram", "kilograms":
		return gross * 1000.0 / gramsPerTroyOunce
	default:
		return gross
	}
}

// Lot is the flat, resolved engine view of a holding (Holding joined to its
// ItemType). This is what calc operates on; it mirrors the prototype's lot shape
// so the ported math is unchanged.
type Lot struct {
	ID           int64   `json:"id"`
	RollTxnID    int64   `json:"roll_txn_id,omitempty"` // box this find came from (0 = none)
	Activity     string  `json:"activity"`              // "bullion" | "crh"
	Product      string  `json:"product"`               // from ItemType.Name
	Metal        string  `json:"metal"`                 // from ItemType.Metal
	Fineness     string  `json:"fineness"`              // from ItemType.Fineness
	Qty          float64 `json:"qty"`
	FineOzEach   float64 `json:"fine_oz_each"` // resolved: ItemType.FineOzEach, else GrossWeight*Purity
	BasisUSD     float64 `json:"basis_usd"`
	FaceValueUSD float64 `json:"face_value_usd"`
	Acquired     string  `json:"acquired"`
	Source       string  `json:"source"`
	PremiumUSD   float64 `json:"premium_usd,omitempty"` // paid over melt at acquisition; a component of basis, display-only
	Category     string  `json:"category,omitempty"`    // CRH find taxonomy (ADR-006)
	Subcategory  string  `json:"subcategory,omitempty"` // CRH find taxonomy (ADR-006)
	Trophy       bool    `json:"trophy,omitempty"`
}

// IsFind reports whether the lot is a coin-roll-hunting find (vs. bullion).
func (l Lot) IsFind() bool { return l.Activity == "crh" }

// Resolve joins a holding to its item type to produce the flat engine view.
// Fine ounces per unit come from the catalog's FineOzEach when set, otherwise from
// the specimen's gross weight (normalized to troy oz via WeightUnit) * purity
// (bars, generic rounds, jewelry).
func Resolve(h Holding, t ItemType) Lot {
	fineEach := t.FineOzEach
	if fineEach == 0 {
		fineEach = grossWeightToTroyOunces(h.GrossWeight, h.WeightUnit) * h.Purity
	}
	return Lot{
		ID:           h.ID,
		RollTxnID:    h.RollTxnID,
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
		PremiumUSD:   h.PremiumUSD,
		Category:     h.Category,
		Subcategory:  h.Subcategory,
		Trophy:       h.Trophy,
	}
}

// RollTxn is a single buy/return of coin against the float, normalized to face
// dollars. The entry unit (box/roll/face/coin) is preserved for display, but
// FaceUSD is the source of truth (see ADR-001 R7).
type RollTxn struct {
	ID   int64  `json:"id"`
	Date string `json:"date"`
	// Bank is the branch's canonical name — a resolved display/entry value, not a
	// stored column since migration 0008 (ADR-010): writes find-or-create a branch
	// from this string and store BranchID; loads read the name back through it.
	Bank     string  `json:"bank"`
	BranchID int64   `json:"branch_id"` // logical link to branches.id (0 = none)
	Action   string  `json:"action"`    // "buy" | "return"
	Denom   string  `json:"denom"`  // dollars|halves|quarters|dimes|nickels|cents
	Unit    string  `json:"unit"`   // "box" | "roll" | "bag" | "face" | "coin"
	Amount  float64 `json:"amount"` // quantity in that unit
	FaceUSD float64 `json:"face_usd"`
	// SourceType is how the coin was wrapped/acquired — the high-signal yield axis from
	// ADR-006, orthogonal to Unit: machine_roll|customer_roll|box|bag|loose ("" = unknown).
	SourceType string `json:"source_type,omitempty"`
	Notes      string `json:"notes"`
}

// Trip is a sourcing run; Miles drives the mileage-based gas cost.
type Trip struct {
	ID       int64   `json:"id"`
	Date     string  `json:"date"`
	Bank     string  `json:"bank"`      // resolved branch name (see RollTxn.Bank)
	BranchID int64   `json:"branch_id"` // logical link to branches.id (0 = none)
	Miles    float64 `json:"miles"`
	Hours    float64 `json:"hours"`
}

// Branch is a bank branch as a first-class entity (ADR-010, migration 0008): the
// address book (phone/hours/fees/denoms/box limits/teller notes) plus the
// pickup/dropoff eligibility (Buys/Dumps) and cooldown that later drive routing.
// UID is opaque and server-generated (ADR-009); Lat/Lon stay 0 until geocoded
// (ADR-011 / om-w2tm). Free-text bank strings from before the cutover survive as
// rows in branch_aliases, which is how a merge repoints typo forks.
type Branch struct {
	ID           int64   `json:"id"`
	UID          string  `json:"uid"`
	Name         string  `json:"name"`
	Institution  string  `json:"institution"`
	Address      string  `json:"address"`
	Phone        string  `json:"phone"`
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	Hours        string  `json:"hours"`
	Buys         bool    `json:"buys"`
	Dumps        bool    `json:"dumps"`
	Denoms       string  `json:"denoms"`
	BoxLimit     int     `json:"box_limit"`
	BoxLeadDays  int     `json:"box_lead_days"`
	CoinFeeUSD   float64 `json:"coin_fee_usd"`
	CooldownDays int     `json:"cooldown_days"`
	Notes        string  `json:"notes"`
	Active       bool    `json:"active"`
}

// Supply is a consumable cost of the hunt (tubes, flips, etc.).
type Supply struct {
	ID      int64   `json:"id"`
	Date    string  `json:"date"`
	Item    string  `json:"item"`
	CostUSD float64 `json:"cost_usd"`
}

// Keeper is BULK/UNCATEGORIZED clad coin parked at face (recoverable, not a
// loss) — kept out of the redeposit float. Individually-notable coins of ANY
// metal (silver or clad) belong in a CRH find lot with ADR-006 taxonomy, not
// here (the notability-based single-entry rule; ADR-008).
//
// Date + RollTxnID (migration 0007, ADR-008) make a keeper auditable / box-
// attributable like a lot: which session/box a batch was logged against. Both
// are nullable — legacy keeper rows carry NULL (empty Date / zero RollTxnID) and
// compute exactly as before (cladFace is unaffected).
type Keeper struct {
	ID        int64   `json:"id"`
	Denom     string  `json:"denom"`
	Count     int64   `json:"count"`
	FaceUSD   float64 `json:"face_usd"`
	Date      string  `json:"date,omitempty"`        // ISO date the batch was logged (audit dimension)
	RollTxnID int64   `json:"roll_txn_id,omitempty"` // box (buy) this batch is attributed to (0 = none)
}

// Loss is a shrinkage / write-off booked when the float can't be fully
// reconciled — face that was bought but never returned, found, or kept (machine
// miscounts, lost coins, short deposits). Unlike a roll-txn 'return' (coin
// recovered), a loss is value gone: it drives the float to $0 and flows into
// calc as a real cash cost (ADR-005).
type Loss struct {
	ID        int64   `json:"id"`
	Date      string  `json:"date"`       // ISO date the period was closed
	AmountUSD float64 `json:"amount_usd"` // face declared lost
	Reason    string  `json:"reason"`     // "machine miscount", "short deposit", ...
	Scope     string  `json:"scope"`      // free-text period/session/bank tag
}

// Spot is a metals price observation; every fetch is appended so we keep history.
type Spot struct {
	AsOf         string  `json:"as_of"`
	GoldUSD      float64 `json:"gold_usd"`
	SilverUSD    float64 `json:"silver_usd"`
	PlatinumUSD  float64 `json:"platinum_usd"`
	PalladiumUSD float64 `json:"palladium_usd"`
	Source       string  `json:"source"`
}

// DisposedLot is a sold holding, resolved (joined to its item type) for
// realized-P&L reporting. Disposed holdings are excluded from the live Lots
// valuation; their realized gain is proceeds - basis of the sold portion.
type DisposedLot struct {
	ID          int64   `json:"id"`
	RollTxnID   int64   `json:"roll_txn_id,omitempty"` // buy this find came from (for source attribution)
	Activity    string  `json:"activity"`
	Product     string  `json:"product"`
	Metal       string  `json:"metal"`
	Qty         float64 `json:"qty"`
	BasisUSD    float64 `json:"basis_usd"`
	ProceedsUSD float64 `json:"proceeds_usd"`
	Disposed    string  `json:"disposed"`              // ISO date sold
	Category    string  `json:"category,omitempty"`    // CRH find taxonomy (ADR-006) — a sold find still counts
	Subcategory string  `json:"subcategory,omitempty"` // toward lifetime hit-rate (survivorship)
}

// Dataset is the full resolved in-memory store the calc engine operates on.
type Dataset struct {
	Lots     []Lot
	Disposed []DisposedLot // sold holdings, for realized P&L
	RollTxns []RollTxn
	Trips    []Trip
	Supplies []Supply
	Keepers  []Keeper
	Losses   []Loss // shrinkage write-offs (ADR-005)
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
			"dollars": 1000, "halves": 500, "quarters": 500, "dimes": 250, "nickels": 100, "cents": 25,
		},
	}
}
