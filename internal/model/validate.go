package model

// Server-side validation for the mutable domain types (om-1czp). The API used to
// decode a JSON body straight into the store with nothing between decode and
// insert, so a typo — or a direct curl — could land a record that "can't be true"
// (a negative basis, an unknown metal, an unparseable date) and then silently
// poison every downstream number. These rules are the front-door integrity check.
//
// Scope is deliberately narrow: every invariant here maps to a concrete
// money-corruption consequence read off the code, not an aesthetic preference.
//   - metal outside spotFor's switch (calc.go) values the whole holding at $0.
//   - action outside the buy/return switch (calc.go) makes the txn vanish from the
//     float and from face searched.
//   - weight_unit that falls through grossWeightToTroyOunces' switch multiplies
//     fine oz by ~31x.
//   - activity that is neither bullion nor crh is invisible to every report.
//   - a negative money/quantity, or a purity outside 0..1, is arithmetically false.
//
// What is NOT enforced, on purpose: the open vocabularies (category, subcategory,
// source, location, notes, losses.reason/scope, supplies.item, branch free-text,
// item_type.kind, roll_txns.denom/unit/source_type, keepers.denom) stay open per
// ADR-006; spot.as_of is left alone because the poller writes RFC3339, not ISO; and
// the aggregate "returns cannot exceed the outstanding float" rule is not a row
// invariant (it is order-dependent and a negative float is a state ADR-005 models).
//
// These are pure functions with no store or DB dependency, so they are trivially
// testable and the store is free to call them at its single mutation chokepoint.
// The DB itself is intentionally NOT changed: adding a CHECK constraint to an
// existing table makes any user database holding a pre-existing bad row fail to
// open (the rebuild's INSERT...SELECT aborts inside the migration's transaction).

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalid is the sentinel every validation failure wraps. The API layer keys a
// 400 (rather than a 500) off errors.Is(err, ErrInvalid).
var ErrInvalid = errors.New("invalid")

// FieldError names the offending field and why it was rejected, so the client can
// show a usable message ("purity must be between 0 and 1") and highlight the cell,
// not just "invalid". It unwraps to ErrInvalid so writeErr can map it to a 400.
type FieldError struct {
	Field string // the JSON/column name, e.g. "purity"
	Msg   string // phrased to read after the field name, e.g. "must be between 0 and 1"
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + " " + e.Msg
}

// Unwrap makes errors.Is(err, ErrInvalid) true for every FieldError.
func (e *FieldError) Unwrap() error { return ErrInvalid }

// invalid builds a FieldError. msg is written to follow the field name.
func invalid(field, msg string) error { return &FieldError{Field: field, Msg: msg} }

// nonNeg rejects a negative money/quantity value.
func nonNeg(field string, v float64) error {
	if v < 0 {
		return invalid(field, "must not be negative")
	}
	return nil
}

// nonNegInt rejects a negative integer count.
func nonNegInt(field string, v int64) error {
	if v < 0 {
		return invalid(field, "must not be negative")
	}
	return nil
}

const isoLayout = "2006-01-02"

// isoDate validates a yyyy-mm-dd date. An empty value is rejected only when the
// field is required; optional dates (a not-yet-disposed lot, a legacy keeper) are
// legally blank. spot.as_of deliberately does NOT go through here.
func isoDate(field, v string, required bool) error {
	if strings.TrimSpace(v) == "" {
		if required {
			return invalid(field, "is required")
		}
		return nil
	}
	if _, err := time.Parse(isoLayout, v); err != nil {
		return invalid(field, fmt.Sprintf("%q is not a valid date (want YYYY-MM-DD)", v))
	}
	return nil
}

// exact validates that v is one of allowed, comparing exactly (no case folding):
// the consuming calc switches (spotFor, the buy/return switch, IsFind) are
// case-sensitive, so accepting "Gold" here would still silently value it at $0.
func exact(field, v, want string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return invalid(field, fmt.Sprintf("%q is not valid (want %s)", v, want))
}

// knownWeightUnit reports whether unit is one grossWeightToTroyOunces converts
// explicitly rather than falling through to its silent ozt default. Kept in sync
// with grossWeightToTroyOunces (model.go): a unit it handles is not corrupting, so
// only the values that hit the default are rejected. Case/space are folded because
// grossWeightToTroyOunces folds them too.
func knownWeightUnit(unit string) bool {
	switch strings.TrimSpace(strings.ToLower(unit)) {
	case "", "ozt", "troy_oz", "troyoz", "g", "gram", "grams", "kg", "kilogram", "kilograms":
		return true
	default:
		return false
	}
}

// Validate checks an item_type catalog row. Metal drives spot valuation; an unknown
// metal values every holding of the type at $0 (calc.spotFor). Blank is legal and
// load-bearing: clad "junk" types (error coins, world coins) carry no melt metal.
func (t ItemType) Validate() error {
	if err := exact("metal", t.Metal, `gold, silver, platinum, palladium, or blank`,
		"", "gold", "silver", "platinum", "palladium"); err != nil {
		return err
	}
	return nonNeg("fine_oz_each", t.FineOzEach)
}

// Validate checks a holding (lot) row.
func (h Holding) Validate() error {
	if err := exact("activity", h.Activity, `"bullion" or "crh"`, "bullion", "crh"); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		v    float64
	}{
		{"qty", h.Qty}, {"gross_weight", h.GrossWeight}, {"basis_usd", h.BasisUSD},
		{"premium_usd", h.PremiumUSD}, {"face_value_usd", h.FaceValueUSD},
		{"insured_value", h.InsuredValue}, {"disposed_usd", h.DisposedUSD},
	} {
		if err := nonNeg(f.name, f.v); err != nil {
			return err
		}
	}
	if h.Purity < 0 || h.Purity > 1 {
		// Purity is a 0..1 fraction (ADR/model.go), not a percent. 0 is legal: it
		// means "derive fine oz from fine_oz_each" for bullion with no gross weight.
		return invalid("purity", "must be between 0 and 1")
	}
	if !knownWeightUnit(h.WeightUnit) {
		return invalid("weight_unit", fmt.Sprintf("%q is not a recognized unit (use ozt, g, or kg)", h.WeightUnit))
	}
	if err := isoDate("acquired", h.Acquired, true); err != nil {
		return err
	}
	return isoDate("disposed", h.Disposed, false)
}

// Validate checks a roll transaction. action outside buy/return is dropped by the
// calc switch — the single worst silent corruption, since the txn vanishes from
// both the float and face searched. denom/unit/source_type are intentionally open
// (blank is legal: a mixed return, an unspecified unit).
func (t RollTxn) Validate() error {
	if err := exact("action", t.Action, `"buy" or "return"`, "buy", "return"); err != nil {
		return err
	}
	if err := nonNeg("amount", t.Amount); err != nil {
		return err
	}
	if err := nonNeg("face_usd", t.FaceUSD); err != nil {
		return err
	}
	return isoDate("date", t.Date, true)
}

// Validate checks a sourcing trip. The bank name is resolved to a branch as a side
// effect of the insert, so this runs first to keep a bad trip from forking a branch.
func (t Trip) Validate() error {
	if err := nonNeg("miles", t.Miles); err != nil {
		return err
	}
	if err := nonNeg("hours", t.Hours); err != nil {
		return err
	}
	return isoDate("date", t.Date, false)
}

// Validate checks a branch (address-book) row. Lat/Lon are unconstrained — they are
// legitimately negative in the western/southern hemispheres, and stay 0 until
// geocoded.
func (b Branch) Validate() error {
	if err := nonNeg("coin_fee_usd", b.CoinFeeUSD); err != nil {
		return err
	}
	if err := nonNegInt("box_limit", int64(b.BoxLimit)); err != nil {
		return err
	}
	if err := nonNegInt("box_lead_days", int64(b.BoxLeadDays)); err != nil {
		return err
	}
	return nonNegInt("cooldown_days", int64(b.CooldownDays))
}

// Validate checks a supply (consumable cost) row.
func (x Supply) Validate() error {
	if err := nonNeg("cost_usd", x.CostUSD); err != nil {
		return err
	}
	return isoDate("date", x.Date, false)
}

// Validate checks a keeper (bulk clad parked at face) row. Date is nullable (legacy
// keeper rows carry none).
func (k Keeper) Validate() error {
	if err := nonNegInt("count", k.Count); err != nil {
		return err
	}
	if err := nonNeg("face_usd", k.FaceUSD); err != nil {
		return err
	}
	return isoDate("date", k.Date, false)
}

// Validate checks a loss (shrinkage write-off, ADR-005) row.
func (l Loss) Validate() error {
	if err := nonNeg("amount_usd", l.AmountUSD); err != nil {
		return err
	}
	return isoDate("date", l.Date, true)
}

// Validate checks a spot observation. Only the prices are checked: as_of is left
// untouched because the poller writes an RFC3339 timestamp, not a yyyy-mm-dd date,
// and a strict ISO rule would reject every automatic price update.
func (s Spot) Validate() error {
	for _, f := range []struct {
		name string
		v    float64
	}{
		{"gold_usd", s.GoldUSD}, {"silver_usd", s.SilverUSD},
		{"platinum_usd", s.PlatinumUSD}, {"palladium_usd", s.PalladiumUSD},
	} {
		if err := nonNeg(f.name, f.v); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the settings blob (the tunables the math depends on).
func (s Settings) Validate() error {
	for _, f := range []struct {
		name string
		v    float64
	}{
		{"hourly_rate_usd", s.HourlyRateUSD},
		{"irs_mileage_rate_usd_per_mile", s.IRSMileageRate},
		{"silver_buyback_factor_40pct", s.SilverBuyback40pct},
		{"silver_buyback_factor_90pct", s.SilverBuyback90pct},
	} {
		if err := nonNeg(f.name, f.v); err != nil {
			return err
		}
	}
	for denom, v := range s.BoxFaceUSD {
		if v < 0 {
			return invalid("box_face_usd["+denom+"]", "must not be negative")
		}
	}
	return nil
}

// Validate checks a photo row (om-6hlp). Scope is the two fields that become PATH
// SEGMENTS — owner_kind and ext — plus the link and the order, because a bad value
// there is not a wrong number, it is a file written to the wrong place or a photo
// that cannot be found. owner_kind and ext are CLOSED (they name a directory and a
// file suffix); role stays OPEN (ADR-006/ADR-009 — documented vocab, not enforced; a
// blank role is defaulted to 'detail' at insert, never rejected, so the NULL-role
// trap 0009 called out cannot recur). uid/owner_uid/seq/ext are server-assigned or
// validated here so nothing user-controlled reaches the filesystem.
func (p Photo) Validate() error {
	if err := exact("owner_kind", p.OwnerKind, `"lot" or "roll_txn"`, "lot", "roll_txn"); err != nil {
		return err
	}
	if strings.TrimSpace(p.OwnerUID) == "" {
		return invalid("owner_uid", "is required")
	}
	// ext is a path segment, sniffed from the bytes server-side — never the client's
	// filename. Compared lowercase because that is the form it is stored and pathed in.
	if err := exact("ext", strings.ToLower(strings.TrimSpace(p.Ext)), "jpg, jpeg, png, or webp",
		"jpg", "jpeg", "png", "webp"); err != nil {
		return err
	}
	return nonNegInt("seq", p.Seq)
}

// ValidateSale checks a holding sale (POST /api/lots/{id}/sell). It has no model
// struct of its own — the sold quantity, proceeds and date come straight off the
// request — so the store's SellHolding chokepoint calls this. qty must be strictly
// positive: a zero/negative sale is meaningless and the partial-sale fraction
// (qty/held) would divide by it.
func ValidateSale(qty, proceeds float64, date string) error {
	if qty <= 0 {
		return invalid("qty", "must be greater than 0")
	}
	if err := nonNeg("proceeds_usd", proceeds); err != nil {
		return err
	}
	return isoDate("date", date, true)
}
