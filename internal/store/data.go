package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// --- inserts -----------------------------------------------------------------

// InsertItemType inserts a catalog row and returns its new id.
func (s *Store) InsertItemType(t model.ItemType) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO item_type (kind, name, metal, asw_oz, fineness, year, mint, mintmark, refs)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		t.Kind, t.Name, t.Metal, t.ASWOz, t.Fineness, t.Year, t.Mint, t.Mintmark, t.References)
	if err != nil {
		return 0, fmt.Errorf("insert item_type: %w", err)
	}
	return res.LastInsertId()
}

// InsertHolding inserts a specimen row and returns its new id.
func (s *Store) InsertHolding(h model.Holding) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO lots (item_type_id, activity, qty, gross_weight, purity, weight_unit,
		   basis_usd, premium_usd, face_value_usd, acquired, source, location, insured_value,
		   attributes, notes, disposed, disposed_usd)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.ItemTypeID, h.Activity, h.Qty, h.GrossWeight, h.Purity, h.WeightUnit,
		h.BasisUSD, h.PremiumUSD, h.FaceValueUSD, h.Acquired, h.Source, h.Location, h.InsuredValue,
		h.Attributes, h.Notes, h.Disposed, h.DisposedUSD)
	if err != nil {
		return 0, fmt.Errorf("insert holding: %w", err)
	}
	return res.LastInsertId()
}

// InsertRollTxn inserts a roll transaction and returns its new id.
func (s *Store) InsertRollTxn(t model.RollTxn) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO roll_txns (date, bank, action, denom, unit, amount, face_usd, notes)
		 VALUES (?,?,?,?,?,?,?,?)`,
		t.Date, t.Bank, t.Action, t.Denom, t.Unit, t.Amount, t.FaceUSD, t.Notes)
	if err != nil {
		return 0, fmt.Errorf("insert roll_txn: %w", err)
	}
	return res.LastInsertId()
}

// InsertTrip inserts a trip and returns its new id.
func (s *Store) InsertTrip(t model.Trip) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO trips (date, bank, miles, hours) VALUES (?,?,?,?)`,
		t.Date, t.Bank, t.Miles, t.Hours)
	if err != nil {
		return 0, fmt.Errorf("insert trip: %w", err)
	}
	return res.LastInsertId()
}

// InsertSupply inserts a supply and returns its new id.
func (s *Store) InsertSupply(x model.Supply) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO supplies (date, item, cost_usd) VALUES (?,?,?)`,
		x.Date, x.Item, x.CostUSD)
	if err != nil {
		return 0, fmt.Errorf("insert supply: %w", err)
	}
	return res.LastInsertId()
}

// InsertKeeper inserts a keeper and returns its new id.
func (s *Store) InsertKeeper(k model.Keeper) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO keepers (denom, count, face_usd) VALUES (?,?,?)`,
		k.Denom, k.Count, k.FaceUSD)
	if err != nil {
		return 0, fmt.Errorf("insert keeper: %w", err)
	}
	return res.LastInsertId()
}

// PutSpot upserts a spot observation keyed by as_of.
func (s *Store) PutSpot(sp model.Spot) error {
	_, err := s.db.Exec(
		`INSERT INTO spot (as_of, gold_usd, silver_usd, source) VALUES (?,?,?,?)
		 ON CONFLICT(as_of) DO UPDATE SET gold_usd=excluded.gold_usd,
		   silver_usd=excluded.silver_usd, source=excluded.source`,
		sp.AsOf, sp.GoldUSD, sp.SilverUSD, sp.Source)
	if err != nil {
		return fmt.Errorf("put spot: %w", err)
	}
	return nil
}

// --- settings ----------------------------------------------------------------

// PutSettings serializes Settings into the key/value settings table. BoxFaceUSD
// is stored as a JSON blob; scalars as their text form.
func (s *Store) PutSettings(cfg model.Settings) error {
	box, err := json.Marshal(cfg.BoxFaceUSD)
	if err != nil {
		return err
	}
	kv := map[string]string{
		"value_time":                    strconv.FormatBool(cfg.ValueTime),
		"hourly_rate_usd":               strconv.FormatFloat(cfg.HourlyRateUSD, 'g', -1, 64),
		"irs_mileage_rate_usd_per_mile": strconv.FormatFloat(cfg.IRSMileageRate, 'g', -1, 64),
		"silver_buyback_factor_40pct":   strconv.FormatFloat(cfg.SilverBuyback40pct, 'g', -1, 64),
		"silver_buyback_factor_90pct":   strconv.FormatFloat(cfg.SilverBuyback90pct, 'g', -1, 64),
		"box_face_usd":                  string(box),
	}
	for k, v := range kv {
		if _, err := s.db.Exec(
			`INSERT INTO settings (key, value) VALUES (?,?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			return fmt.Errorf("put setting %s: %w", k, err)
		}
	}
	return nil
}

// GetSettings loads Settings, starting from DefaultSettings and overriding with
// any stored keys.
func (s *Store) GetSettings() (model.Settings, error) {
	cfg := model.DefaultSettings()
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return cfg, fmt.Errorf("get settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return cfg, err
		}
		switch k {
		case "value_time":
			cfg.ValueTime, _ = strconv.ParseBool(v)
		case "hourly_rate_usd":
			cfg.HourlyRateUSD, _ = strconv.ParseFloat(v, 64)
		case "irs_mileage_rate_usd_per_mile":
			cfg.IRSMileageRate, _ = strconv.ParseFloat(v, 64)
		case "silver_buyback_factor_40pct":
			cfg.SilverBuyback40pct, _ = strconv.ParseFloat(v, 64)
		case "silver_buyback_factor_90pct":
			cfg.SilverBuyback90pct, _ = strconv.ParseFloat(v, 64)
		case "box_face_usd":
			m := map[string]float64{}
			if json.Unmarshal([]byte(v), &m) == nil && len(m) > 0 {
				cfg.BoxFaceUSD = m
			}
		}
	}
	return cfg, rows.Err()
}

// --- load / resolve ----------------------------------------------------------

// LatestSpot returns the most recent spot observation, or a zero Spot if none.
func (s *Store) LatestSpot() (model.Spot, error) {
	var sp model.Spot
	var src sql.NullString
	err := s.db.QueryRow(
		`SELECT as_of, gold_usd, silver_usd, source FROM spot ORDER BY as_of DESC LIMIT 1`).
		Scan(&sp.AsOf, &sp.GoldUSD, &sp.SilverUSD, &src)
	if err == sql.ErrNoRows {
		return model.Spot{}, nil
	}
	sp.Source = src.String
	return sp, err
}

// ResolveDataset loads the whole store into the flat, resolved Dataset the calc
// engine consumes: each holding is joined to its item_type and reduced to a Lot.
func (s *Store) ResolveDataset() (model.Dataset, error) {
	var d model.Dataset

	// item_type catalog, indexed by id, for resolving holdings.
	types := map[int64]model.ItemType{}
	rows, err := s.db.Query(`SELECT id, kind, name, metal, asw_oz, fineness FROM item_type`)
	if err != nil {
		return d, fmt.Errorf("load item_type: %w", err)
	}
	for rows.Next() {
		var t model.ItemType
		var fineness sql.NullString
		if err := rows.Scan(&t.ID, &t.Kind, &t.Name, &t.Metal, &t.ASWOz, &fineness); err != nil {
			rows.Close()
			return d, err
		}
		t.Fineness = fineness.String
		types[t.ID] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}

	// holdings -> resolved lots
	rows, err = s.db.Query(
		`SELECT id, item_type_id, activity, qty, gross_weight, purity, basis_usd,
		   face_value_usd, acquired, source
		 FROM lots WHERE disposed IS NULL OR disposed = '' ORDER BY id`)
	if err != nil {
		return d, fmt.Errorf("load lots: %w", err)
	}
	for rows.Next() {
		var h model.Holding
		var source sql.NullString
		if err := rows.Scan(&h.ID, &h.ItemTypeID, &h.Activity, &h.Qty, &h.GrossWeight,
			&h.Purity, &h.BasisUSD, &h.FaceValueUSD, &h.Acquired, &source); err != nil {
			rows.Close()
			return d, err
		}
		h.Source = source.String
		d.Lots = append(d.Lots, model.Resolve(h, types[h.ItemTypeID]))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}

	if d.RollTxns, err = s.loadRollTxns(); err != nil {
		return d, err
	}
	if d.Trips, err = s.loadTrips(); err != nil {
		return d, err
	}
	if d.Supplies, err = s.loadSupplies(); err != nil {
		return d, err
	}
	if d.Keepers, err = s.loadKeepers(); err != nil {
		return d, err
	}
	if d.Spot, err = s.LatestSpot(); err != nil {
		return d, err
	}
	if d.Settings, err = s.GetSettings(); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Store) loadRollTxns() ([]model.RollTxn, error) {
	rows, err := s.db.Query(`SELECT id, date, bank, action, denom, unit, amount, face_usd, notes FROM roll_txns ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load roll_txns: %w", err)
	}
	defer rows.Close()
	var out []model.RollTxn
	for rows.Next() {
		var t model.RollTxn
		var bank, denom, unit, notes sql.NullString
		var amount sql.NullFloat64
		if err := rows.Scan(&t.ID, &t.Date, &bank, &t.Action, &denom, &unit, &amount, &t.FaceUSD, &notes); err != nil {
			return nil, err
		}
		t.Bank, t.Denom, t.Unit, t.Notes, t.Amount = bank.String, denom.String, unit.String, notes.String, amount.Float64
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) loadTrips() ([]model.Trip, error) {
	rows, err := s.db.Query(`SELECT id, date, bank, miles, hours FROM trips ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load trips: %w", err)
	}
	defer rows.Close()
	var out []model.Trip
	for rows.Next() {
		var t model.Trip
		var date, bank sql.NullString
		if err := rows.Scan(&t.ID, &date, &bank, &t.Miles, &t.Hours); err != nil {
			return nil, err
		}
		t.Date, t.Bank = date.String, bank.String
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) loadSupplies() ([]model.Supply, error) {
	rows, err := s.db.Query(`SELECT id, date, item, cost_usd FROM supplies ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load supplies: %w", err)
	}
	defer rows.Close()
	var out []model.Supply
	for rows.Next() {
		var x model.Supply
		var date, item sql.NullString
		if err := rows.Scan(&x.ID, &date, &item, &x.CostUSD); err != nil {
			return nil, err
		}
		x.Date, x.Item = date.String, item.String
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) loadKeepers() ([]model.Keeper, error) {
	rows, err := s.db.Query(`SELECT id, denom, count, face_usd FROM keepers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load keepers: %w", err)
	}
	defer rows.Close()
	var out []model.Keeper
	for rows.Next() {
		var k model.Keeper
		var denom sql.NullString
		if err := rows.Scan(&k.ID, &denom, &k.Count, &k.FaceUSD); err != nil {
			return nil, err
		}
		k.Denom = denom.String
		out = append(out, k)
	}
	return out, rows.Err()
}
