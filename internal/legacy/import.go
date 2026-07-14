// Package legacy imports the prototype's two JSON files (pm_holdings.json +
// crh_ledger.json) into the SQLite store. It is the one-time migration named in
// ADR-001 Phase 0, adapted to the ADR-003 catalog/specimen split: each flat
// prototype lot is fanned into a synthesized item_type (deduped) plus a holding.
//
// It is also the new-user on-ramp — the spreadsheet migration path — so it is ATOMIC
// and it reports EVERY bad row at once (om-u3el). Import runs in three phases:
//
//  1. PLAN     — parse both files into the model structs that will be written,
//     remembering where in the file each one came from.
//  2. VALIDATE — run every model validator up front and collect ALL the failures
//     into one ImportErrors report. A user with three typos hears about
//     three typos once, not one typo three times.
//  3. WRITE    — the whole plan inside ONE store transaction (store.WithTx), so any
//     failure — a rejected row, a missing table, a full disk — leaves the
//     database byte-for-byte unchanged.
//
// Both halves are load-bearing. The pre-validate pass is the UX; the TRANSACTION is
// the guarantee, because it also covers the failures no amount of validation can
// foresee. Before om-u3el this wrote with no transaction at all: the first bad row
// aborted mid-stream, the rows ahead of it stayed committed (settings and spot are
// upserts, so even the user's settings were quietly rewritten from a file that was
// then rejected), a typed bank name had already forked an orphan branch — and the
// corrected re-run then DUPLICATED every row the failed run had left behind.
package legacy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// holdingsFile mirrors prototype pm_holdings.json (only the fields we import).
type holdingsFile struct {
	SpotReference struct {
		Gold   float64 `json:"gold_usd_per_ozt"`
		Silver float64 `json:"silver_usd_per_ozt"`
		AsOf   string  `json:"as_of"`
	} `json:"spot_reference"`
	Lots []struct {
		ID           string  `json:"id"`
		Acquired     string  `json:"acquired"`
		Source       string  `json:"source"`
		Product      string  `json:"product"`
		Category     string  `json:"category"`
		Metal        string  `json:"metal"`
		Fineness     string  `json:"fineness"`
		Qty          float64 `json:"qty"`
		FineOzEach   float64 `json:"fine_oz_each"`
		BasisUSD     float64 `json:"basis_usd"`
		FaceValueUSD float64 `json:"face_value_usd"`
	} `json:"lots"`
}

// crhFile mirrors prototype crh_ledger.json (only the fields we import).
type crhFile struct {
	Settings struct {
		ValueTime          bool               `json:"value_time"`
		HourlyRateUSD      float64            `json:"hourly_rate_usd"`
		IRSMileageRate     float64            `json:"irs_mileage_rate_usd_per_mile"`
		SilverBuyback90pct float64            `json:"silver_buyback_factor_90pct"`
		SilverBuyback40pct float64            `json:"silver_buyback_factor_40pct"`
		BoxFaceUSD         map[string]float64 `json:"box_face_usd"`
	} `json:"settings"`
	RollTransactions []struct {
		Date    string  `json:"date"`
		Bank    string  `json:"bank"`
		Action  string  `json:"action"`
		CashUSD float64 `json:"cash_usd"`
		Denom   string  `json:"denom"`
		Boxes   float64 `json:"boxes"`
		Notes   string  `json:"notes"`
	} `json:"roll_transactions"`
	Trips []struct {
		Date  string  `json:"date"`
		Bank  string  `json:"bank"`
		Miles float64 `json:"miles"`
		Hours float64 `json:"hours"`
	} `json:"trips"`
	Supplies []struct {
		Date    string  `json:"date"`
		Item    string  `json:"item"`
		CostUSD float64 `json:"cost_usd"`
	} `json:"supplies"`
	KeepersClad struct {
		HalvesCount     int64   `json:"halves_count"`
		HalvesFaceUSD   float64 `json:"halves_face_usd"`
		QuartersCount   int64   `json:"quarters_count"`
		QuartersFaceUSD float64 `json:"quarters_face_usd"`
	} `json:"keepers_clad"`
}

// Import parses the prototype JSON blobs and writes them into s, ATOMICALLY: either
// every row lands or none does. It is additive — it inserts new rows and does not
// clear existing data — so re-running a file that already imported SUCCESSFULLY does
// duplicate it. What it will never do is duplicate after a FAILED run, because a
// failed run writes nothing.
//
// A file with invalid rows comes back as an *ImportErrors naming every one of them
// (and unwrapping to model.ErrInvalid).
func Import(s *store.Store, holdingsJSON, crhJSON []byte) error {
	p, err := newPlan(holdingsJSON, crhJSON)
	if err != nil {
		return err
	}
	// Phase 2: every rule, every row, before the first write.
	if err := p.validate(); err != nil {
		return err
	}
	// Phase 3: one transaction around the lot. Nothing inside write() may call a
	// *store.Store method — only the *store.Tx it is handed. See store.WithTx: the open
	// transaction holds SQLite's single connection, so a Store call would block forever.
	return s.WithTx(p.write)
}

// --- phase 1: the plan --------------------------------------------------------

// row pairs a model value with where in the source file it came from, so a rejected
// row reports as `pm_holdings.json lots[2]: metal "Silver" is not valid (…)` instead
// of as an anonymous failure somewhere in a 900-row file.
type row[T any] struct {
	where string
	v     T
}

// plannedLot is a holding whose item_type has not been inserted yet: typeIdx points at
// the synthesized (deduped) catalog entry in plan.types, and write() fills in the id
// that insert hands back. Holding.Validate does not look at item_type_id, so the row
// still validates in full before anything is written.
type plannedLot struct {
	where   string
	typeIdx int
	h       model.Holding
}

// plan is the whole import, resolved into model structs and ready to write. Building it
// is pure — it touches no database — which is what lets the validation pass run to
// completion over every row before a single statement is issued.
type plan struct {
	settings row[model.Settings]
	spot     *row[model.Spot] // nil when the file carries no spot reference
	types    []row[model.ItemType]
	lots     []plannedLot
	txns     []row[model.RollTxn]
	trips    []row[model.Trip]
	supplies []row[model.Supply]
	keepers  []row[model.Keeper]
}

const (
	holdingsFileName = "pm_holdings.json"
	crhFileName      = "crh_ledger.json"
)

func newPlan(holdingsJSON, crhJSON []byte) (*plan, error) {
	var h holdingsFile
	if err := json.Unmarshal(holdingsJSON, &h); err != nil {
		return nil, fmt.Errorf("parse holdings json: %w", err)
	}
	var c crhFile
	if err := json.Unmarshal(crhJSON, &c); err != nil {
		return nil, fmt.Errorf("parse crh json: %w", err)
	}
	p := &plan{}

	// Settings: start from prototype defaults, override with the file's values.
	cfg := model.DefaultSettings()
	cfg.ValueTime = c.Settings.ValueTime
	if c.Settings.HourlyRateUSD != 0 {
		cfg.HourlyRateUSD = c.Settings.HourlyRateUSD
	}
	if c.Settings.IRSMileageRate != 0 {
		cfg.IRSMileageRate = c.Settings.IRSMileageRate
	}
	if c.Settings.SilverBuyback40pct != 0 {
		cfg.SilverBuyback40pct = c.Settings.SilverBuyback40pct
	}
	if c.Settings.SilverBuyback90pct != 0 {
		cfg.SilverBuyback90pct = c.Settings.SilverBuyback90pct
	}
	if len(c.Settings.BoxFaceUSD) > 0 {
		cfg.BoxFaceUSD = c.Settings.BoxFaceUSD
	}
	p.settings = row[model.Settings]{where: crhFileName + " settings", v: cfg}

	// Spot reference.
	if h.SpotReference.Gold != 0 || h.SpotReference.Silver != 0 {
		p.spot = &row[model.Spot]{
			where: holdingsFileName + " spot_reference",
			v: model.Spot{
				AsOf:      h.SpotReference.AsOf,
				GoldUSD:   h.SpotReference.Gold,
				SilverUSD: h.SpotReference.Silver,
				Source:    "prototype import",
			},
		}
	}

	// Lots -> synthesized item_type (deduped) + holding.
	typeIdx := map[string]int{} // dedupe key -> index into p.types
	for i, l := range h.Lots {
		where := fmt.Sprintf("%s lots[%d]", holdingsFileName, i)
		key := strings.Join([]string{l.Product, l.Metal, l.Fineness, fmt.Sprintf("%g", l.FineOzEach)}, "|")
		idx, ok := typeIdx[key]
		if !ok {
			idx = len(p.types)
			p.types = append(p.types, row[model.ItemType]{
				where: where + " (item_type)",
				v: model.ItemType{
					Kind:       kindFor(l.Category),
					Name:       l.Product,
					Metal:      l.Metal,
					FineOzEach: l.FineOzEach, // standardized per-unit metal weight (troy oz)
					Fineness:   l.Fineness,
				},
			})
			typeIdx[key] = idx
		}
		p.lots = append(p.lots, plannedLot{
			where:   where,
			typeIdx: idx,
			h: model.Holding{
				Activity:     activityFor(l.ID),
				Qty:          l.Qty,
				BasisUSD:     l.BasisUSD,
				FaceValueUSD: l.FaceValueUSD,
				Acquired:     l.Acquired,
				Source:       l.Source,
			},
		})
	}

	// Roll transactions: cash_usd is the normalized face; preserve box entry unit.
	for i, t := range c.RollTransactions {
		rt := model.RollTxn{
			Date:    t.Date,
			Bank:    t.Bank,
			Action:  t.Action,
			Denom:   t.Denom,
			FaceUSD: t.CashUSD,
			Notes:   t.Notes,
		}
		if t.Boxes != 0 {
			rt.Unit, rt.Amount = "box", t.Boxes
		}
		p.txns = append(p.txns, row[model.RollTxn]{
			where: fmt.Sprintf("%s roll_transactions[%d]", crhFileName, i), v: rt,
		})
	}

	for i, t := range c.Trips {
		p.trips = append(p.trips, row[model.Trip]{
			where: fmt.Sprintf("%s trips[%d]", crhFileName, i),
			v:     model.Trip{Date: t.Date, Bank: t.Bank, Miles: t.Miles, Hours: t.Hours},
		})
	}
	for i, x := range c.Supplies {
		p.supplies = append(p.supplies, row[model.Supply]{
			where: fmt.Sprintf("%s supplies[%d]", crhFileName, i),
			v:     model.Supply{Date: x.Date, Item: x.Item, CostUSD: x.CostUSD},
		})
	}

	// Clad keepers: fan the prototype's single object into one row per denom.
	clad := c.KeepersClad
	if clad.HalvesCount != 0 || clad.HalvesFaceUSD != 0 {
		p.keepers = append(p.keepers, row[model.Keeper]{
			where: crhFileName + " keepers_clad (halves)",
			v:     model.Keeper{Denom: "halves", Count: clad.HalvesCount, FaceUSD: clad.HalvesFaceUSD},
		})
	}
	if clad.QuartersCount != 0 || clad.QuartersFaceUSD != 0 {
		p.keepers = append(p.keepers, row[model.Keeper]{
			where: crhFileName + " keepers_clad (quarters)",
			v:     model.Keeper{Denom: "quarters", Count: clad.QuartersCount, FaceUSD: clad.QuartersFaceUSD},
		})
	}
	return p, nil
}

// --- phase 2: pre-validate ----------------------------------------------------

// validatable is what every planned row is: a model type carrying its own rules
// (internal/model/validate.go, om-1czp). The importer does not invent a second set of
// rules — it runs the store's rules EARLY, so it can report them all at once instead
// of discovering them one abort at a time.
type validatable interface{ Validate() error }

// validate runs every model validator over the whole plan and returns an *ImportErrors
// naming EVERY bad row, or nil. It is pure: it writes nothing, so a file that fails
// here has not touched the database at all.
//
// The store still validates each row again on the way in, and that is deliberate: this
// pass exists for the REPORT, while the store's chokepoint (validate_ast_test.go) is
// the GUARANTEE. If the two ever disagree, the store wins and the transaction rolls the
// import back — the failure mode is a confusing error, never a bad row.
func (p *plan) validate() error {
	var rows []*RowError
	check := func(where string, v validatable) {
		if err := v.Validate(); err != nil {
			rows = append(rows, &RowError{Where: where, Err: err})
		}
	}

	check(p.settings.where, p.settings.v)
	if p.spot != nil {
		check(p.spot.where, p.spot.v)
	}
	for _, t := range p.types {
		check(t.where, t.v)
	}
	for _, l := range p.lots {
		check(l.where, l.h)
	}
	for _, t := range p.txns {
		check(t.where, t.v)
	}
	for _, t := range p.trips {
		check(t.where, t.v)
	}
	for _, x := range p.supplies {
		check(x.where, x.v)
	}
	for _, k := range p.keepers {
		check(k.where, k.v)
	}

	if len(rows) == 0 {
		return nil
	}
	return &ImportErrors{Rows: rows}
}

// --- phase 3: write -----------------------------------------------------------

// write inserts the whole plan through tx. Every statement — including the branch rows
// a typed bank name forks inside InsertRollTxn/InsertTrip — belongs to the single
// transaction WithTx opened, so returning an error here undoes all of it.
//
// It must never call a method on the *store.Store: the transaction holds SQLite's one
// connection, and a Store call would block on it forever (see store.WithTx).
func (p *plan) write(tx *store.Tx) error {
	if err := tx.PutSettings(p.settings.v); err != nil {
		return fmt.Errorf("%s: %w", p.settings.where, err)
	}
	if p.spot != nil {
		if err := tx.PutSpot(p.spot.v); err != nil {
			return fmt.Errorf("%s: %w", p.spot.where, err)
		}
	}

	typeIDs := make([]int64, len(p.types))
	for i, t := range p.types {
		id, err := tx.InsertItemType(t.v)
		if err != nil {
			return fmt.Errorf("%s: %w", t.where, err)
		}
		typeIDs[i] = id
	}
	for _, l := range p.lots {
		h := l.h
		h.ItemTypeID = typeIDs[l.typeIdx] // the id its synthesized catalog row just got
		if _, err := tx.InsertHolding(h); err != nil {
			return fmt.Errorf("%s: %w", l.where, err)
		}
	}
	for _, t := range p.txns {
		if _, err := tx.InsertRollTxn(t.v); err != nil {
			return fmt.Errorf("%s: %w", t.where, err)
		}
	}
	for _, t := range p.trips {
		if _, err := tx.InsertTrip(t.v); err != nil {
			return fmt.Errorf("%s: %w", t.where, err)
		}
	}
	for _, x := range p.supplies {
		if _, err := tx.InsertSupply(x.v); err != nil {
			return fmt.Errorf("%s: %w", x.where, err)
		}
	}
	for _, k := range p.keepers {
		if _, err := tx.InsertKeeper(k.v); err != nil {
			return fmt.Errorf("%s: %w", k.where, err)
		}
	}
	return nil
}

// --- error reporting ----------------------------------------------------------

// RowError is one rejected row: where it came from and what is wrong with it. Err is
// the model.FieldError, so it names the offending field.
type RowError struct {
	Where string // e.g. `crh_ledger.json roll_transactions[3]`
	Err   error  // a model.FieldError (unwraps to model.ErrInvalid)
}

func (e *RowError) Error() string { return e.Where + ": " + e.Err.Error() }
func (e *RowError) Unwrap() error { return e.Err }

// ImportErrors is the pre-validate pass's report: EVERY bad row in the file, not just
// the first. Failing one row at a time is what burns the on-ramp — the user fixes a
// row, re-runs, hits the next one, N times, on their first day with the app.
type ImportErrors struct{ Rows []*RowError }

func (e *ImportErrors) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d invalid row(s); nothing was written:", len(e.Rows))
	for _, r := range e.Rows {
		b.WriteString("\n  - " + r.Error())
	}
	return b.String()
}

// Unwrap exposes every row error, so errors.Is(err, model.ErrInvalid) holds for the
// whole report (each RowError unwraps to a FieldError, which unwraps to ErrInvalid).
func (e *ImportErrors) Unwrap() []error {
	out := make([]error, len(e.Rows))
	for i, r := range e.Rows {
		out[i] = r
	}
	return out
}

// --- mapping helpers ----------------------------------------------------------

// activityFor maps the prototype's "id starts with FIND" convention to the
// explicit activity column.
func activityFor(id string) string {
	if strings.HasPrefix(strings.ToUpper(id), "FIND") {
		return "crh"
	}
	return "bullion"
}

// kindFor maps the prototype's loose category to an ADR-003 item kind.
func kindFor(category string) string {
	switch strings.ToLower(category) {
	case "bullion":
		return "coin"
	case "junk":
		return "junk"
	case "":
		return "other"
	default:
		return strings.ToLower(category)
	}
}
