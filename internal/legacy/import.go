// Package legacy imports the prototype's two JSON files (pm_holdings.json +
// crh_ledger.json) into the SQLite store. It is the one-time migration named in
// ADR-001 Phase 0, adapted to the ADR-003 catalog/specimen split: each flat
// prototype lot is fanned into a synthesized item_type (deduped) plus a holding.
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

// Import parses the prototype JSON blobs and writes them into s. It is additive:
// it inserts new rows and does not clear existing data.
func Import(s *store.Store, holdingsJSON, crhJSON []byte) error {
	var h holdingsFile
	if err := json.Unmarshal(holdingsJSON, &h); err != nil {
		return fmt.Errorf("parse holdings json: %w", err)
	}
	var c crhFile
	if err := json.Unmarshal(crhJSON, &c); err != nil {
		return fmt.Errorf("parse crh json: %w", err)
	}

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
	if err := s.PutSettings(cfg); err != nil {
		return err
	}

	// Spot reference.
	if h.SpotReference.Gold != 0 || h.SpotReference.Silver != 0 {
		if err := s.PutSpot(model.Spot{
			AsOf:      h.SpotReference.AsOf,
			GoldUSD:   h.SpotReference.Gold,
			SilverUSD: h.SpotReference.Silver,
			Source:    "prototype import",
		}); err != nil {
			return err
		}
	}

	// Lots -> synthesized item_type (deduped) + holding.
	typeIDs := map[string]int64{} // dedupe key -> item_type id
	for _, l := range h.Lots {
		key := strings.Join([]string{l.Product, l.Metal, l.Fineness, fmt.Sprintf("%g", l.FineOzEach)}, "|")
		typeID, ok := typeIDs[key]
		if !ok {
			id, err := s.InsertItemType(model.ItemType{
				Kind:       kindFor(l.Category),
				Name:       l.Product,
				Metal:      l.Metal,
				FineOzEach: l.FineOzEach, // standardized per-unit metal weight (troy oz)
				Fineness:   l.Fineness,
			})
			if err != nil {
				return err
			}
			typeID = id
			typeIDs[key] = id
		}
		if _, err := s.InsertHolding(model.Holding{
			ItemTypeID:   typeID,
			Activity:     activityFor(l.ID),
			Qty:          l.Qty,
			BasisUSD:     l.BasisUSD,
			FaceValueUSD: l.FaceValueUSD,
			Acquired:     l.Acquired,
			Source:       l.Source,
		}); err != nil {
			return err
		}
	}

	// Roll transactions: cash_usd is the normalized face; preserve box entry unit.
	for _, t := range c.RollTransactions {
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
		if _, err := s.InsertRollTxn(rt); err != nil {
			return err
		}
	}

	for _, t := range c.Trips {
		if _, err := s.InsertTrip(model.Trip{Date: t.Date, Bank: t.Bank, Miles: t.Miles, Hours: t.Hours}); err != nil {
			return err
		}
	}
	for _, x := range c.Supplies {
		if _, err := s.InsertSupply(model.Supply{Date: x.Date, Item: x.Item, CostUSD: x.CostUSD}); err != nil {
			return err
		}
	}

	// Clad keepers: fan the prototype's single object into one row per denom.
	clad := c.KeepersClad
	if clad.HalvesCount != 0 || clad.HalvesFaceUSD != 0 {
		if _, err := s.InsertKeeper(model.Keeper{Denom: "halves", Count: clad.HalvesCount, FaceUSD: clad.HalvesFaceUSD}); err != nil {
			return err
		}
	}
	if clad.QuartersCount != 0 || clad.QuartersFaceUSD != 0 {
		if _, err := s.InsertKeeper(model.Keeper{Denom: "quarters", Count: clad.QuartersCount, FaceUSD: clad.QuartersFaceUSD}); err != nil {
			return err
		}
	}
	return nil
}

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
