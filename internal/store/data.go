package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// newUID returns a lowercase RFC-4122 v4 UUID — the opaque, never-recycled identity
// carried by branches, lots, roll_txns and item_type (ADR-009). The 0008/0009/0010
// backfills generate the same shape in SQL (the migration runner has no Go step);
// this is the runtime path for rows created afterward.
func newUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// resolveBranchID maps a typed bank name to a branch id, creating the branch (and
// recording the name as its first alias) when no branch name or alias matches.
// Mirrors the Holdings grid's find-or-create of an item_type (ADR-003). An empty
// name resolves to 0 (a NULL branch_id). A newly-typed variant still forks a fresh
// branch by design; the address-book merge (ADR-010 (b)) is how forks get repointed.
func (s *Store) resolveBranchID(name string) (int64, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return 0, nil
	}
	var id int64
	err := s.db.QueryRow(
		`SELECT b.id FROM branches b
		 LEFT JOIN branch_aliases a ON a.branch_id = b.id
		 WHERE b.name = ? OR a.alias = ? LIMIT 1`, n, n).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case err == sql.ErrNoRows:
		return s.InsertBranch(model.Branch{Name: n, Buys: true, Dumps: true, Active: true})
	default:
		return 0, fmt.Errorf("resolve branch %q: %w", n, err)
	}
}

// --- inserts -----------------------------------------------------------------

// InsertItemType inserts a catalog row and returns its new id. As with lots and
// roll_txns, the uid is server-generated and never taken from the caller: it is the
// catalog entry's permanent identity (ADR-009, migration 0010), and item_type.uid
// has no schema-level NOT NULL to fall back on — this insert path IS the guarantee.
func (s *Store) InsertItemType(t model.ItemType) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO item_type (uid, kind, name, metal, fine_oz_each, fineness, year, mint, mintmark, refs)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		newUID(), t.Kind, t.Name, t.Metal, t.FineOzEach, t.Fineness, t.Year, t.Mint, t.Mintmark, t.References)
	if err != nil {
		return 0, fmt.Errorf("insert item_type: %w", err)
	}
	return res.LastInsertId()
}

// InsertHolding inserts a specimen row and returns its new id. The uid is
// server-generated and never taken from the caller: it is the row's permanent
// identity (ADR-009), and lots.uid has no schema-level NOT NULL to fall back on —
// this insert path IS the guarantee.
func (s *Store) InsertHolding(h model.Holding) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO lots (uid, item_type_id, roll_txn_id, activity, qty, gross_weight, purity, weight_unit,
		   basis_usd, premium_usd, face_value_usd, acquired, source, location, insured_value,
		   attributes, notes, category, subcategory, trophy, disposed, disposed_usd)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		newUID(), h.ItemTypeID, nullID(h.RollTxnID), h.Activity, h.Qty, h.GrossWeight, h.Purity, h.WeightUnit,
		h.BasisUSD, h.PremiumUSD, h.FaceValueUSD, h.Acquired, h.Source, h.Location, h.InsuredValue,
		h.Attributes, h.Notes, h.Category, h.Subcategory, b2i(h.Trophy), h.Disposed, h.DisposedUSD)
	if err != nil {
		return 0, fmt.Errorf("insert holding: %w", err)
	}
	return res.LastInsertId()
}

// InsertRollTxn inserts a roll transaction and returns its new id. The typed bank
// name find-or-creates a branch (ADR-010); only the resolved branch_id is stored.
func (s *Store) InsertRollTxn(t model.RollTxn) (int64, error) {
	bid, err := s.resolveBranchID(t.Bank)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO roll_txns (uid, date, branch_id, action, denom, unit, amount, face_usd, source_type, notes)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		newUID(), t.Date, nullID(bid), t.Action, t.Denom, t.Unit, t.Amount, t.FaceUSD, t.SourceType, t.Notes)
	if err != nil {
		return 0, fmt.Errorf("insert roll_txn: %w", err)
	}
	return res.LastInsertId()
}

// InsertTrip inserts a trip and returns its new id (bank find-or-creates a branch).
func (s *Store) InsertTrip(t model.Trip) (int64, error) {
	bid, err := s.resolveBranchID(t.Bank)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO trips (date, branch_id, miles, hours) VALUES (?,?,?,?)`,
		t.Date, nullID(bid), t.Miles, t.Hours)
	if err != nil {
		return 0, fmt.Errorf("insert trip: %w", err)
	}
	return res.LastInsertId()
}

// InsertBranch inserts a branch (server-generated opaque uid) and records its
// canonical name as an alias so resolveBranchID and merges can find it. Returns
// the new id.
func (s *Store) InsertBranch(b model.Branch) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO branches (uid, name, institution, address, phone, lat, lon, hours,
		   buys, dumps, denoms, box_limit, box_lead_days, coin_fee_usd, cooldown_days, notes, active)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		newUID(), strings.TrimSpace(b.Name), b.Institution, b.Address, b.Phone, b.Lat, b.Lon, b.Hours,
		b2i(b.Buys), b2i(b.Dumps), b.Denoms, b.BoxLimit, b.BoxLeadDays, b.CoinFeeUSD,
		b.CooldownDays, b.Notes, b2i(b.Active))
	if err != nil {
		return 0, fmt.Errorf("insert branch: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if name := strings.TrimSpace(b.Name); name != "" {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO branch_aliases (branch_id, alias) VALUES (?,?)`, id, name); err != nil {
			return 0, fmt.Errorf("insert branch alias: %w", err)
		}
	}
	return id, nil
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

// InsertLoss inserts a shrinkage/loss adjustment and returns its new id (ADR-005).
func (s *Store) InsertLoss(l model.Loss) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO losses (date, amount_usd, reason, scope) VALUES (?,?,?,?)`,
		l.Date, l.AmountUSD, l.Reason, l.Scope)
	if err != nil {
		return 0, fmt.Errorf("insert loss: %w", err)
	}
	return res.LastInsertId()
}

// InsertKeeper inserts a keeper and returns its new id. date/roll_txn_id (ADR-008)
// are nullable: an empty date and a zero roll_txn_id are stored as SQL NULL.
func (s *Store) InsertKeeper(k model.Keeper) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO keepers (denom, count, face_usd, date, roll_txn_id) VALUES (?,?,?,?,?)`,
		k.Denom, k.Count, k.FaceUSD, nullStr(k.Date), nullID(k.RollTxnID))
	if err != nil {
		return 0, fmt.Errorf("insert keeper: %w", err)
	}
	return res.LastInsertId()
}

// SellHolding records a sale of qty units of holding id for proceeds on date.
// A full sale (qty >= held) just marks the lot disposed; a partial sale splits
// it: a new disposed lot carries the sold qty with proportional basis/premium/
// face, and the original lot is reduced by the same. Realized P&L for the sold
// portion is proceeds - its basis. Runs in one transaction.
func (s *Store) SellHolding(id int64, qty, proceeds float64, date string) error {
	if qty <= 0 {
		return fmt.Errorf("sell qty must be > 0")
	}
	if date == "" {
		return fmt.Errorf("sell date required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var h model.Holding
	var rtid sql.NullInt64
	var wu, src, loc, attr, notes, cat, subcat, disp sql.NullString
	var trophy int64
	err = tx.QueryRow(
		`SELECT item_type_id, roll_txn_id, activity, qty, gross_weight, purity, weight_unit,
		   basis_usd, premium_usd, face_value_usd, acquired, source, location, insured_value,
		   attributes, notes, category, subcategory, trophy, disposed, disposed_usd
		 FROM lots WHERE id=?`, id).Scan(
		&h.ItemTypeID, &rtid, &h.Activity, &h.Qty, &h.GrossWeight, &h.Purity, &wu,
		&h.BasisUSD, &h.PremiumUSD, &h.FaceValueUSD, &h.Acquired, &src, &loc, &h.InsuredValue,
		&attr, &notes, &cat, &subcat, &trophy, &disp, &h.DisposedUSD)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if disp.Valid && disp.String != "" {
		return fmt.Errorf("holding %d is already disposed", id)
	}

	if qty >= h.Qty {
		if _, err := tx.Exec(`UPDATE lots SET disposed=?, disposed_usd=? WHERE id=?`,
			date, proceeds, id); err != nil {
			return err
		}
		return tx.Commit()
	}

	// Partial: carve out the sold portion as a new disposed lot.
	frac := qty / h.Qty
	soldBasis := h.BasisUSD * frac
	soldPremium := h.PremiumUSD * frac
	soldFace := h.FaceValueUSD * frac
	// A partial sale carves out a NEW lot row, so it needs its own uid — it is a
	// distinct specimen from the remainder, and it is the row a receipt or a
	// slab-label photo would hang off. Easy to miss: nothing here says "insert" in
	// the caller's vocabulary; the user sold half a lot.
	if _, err := tx.Exec(
		`INSERT INTO lots (uid, item_type_id, roll_txn_id, activity, qty, gross_weight, purity, weight_unit,
		   basis_usd, premium_usd, face_value_usd, acquired, source, location, insured_value,
		   attributes, notes, category, subcategory, trophy, disposed, disposed_usd)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		newUID(), h.ItemTypeID, nullID(rtid.Int64), h.Activity, qty, h.GrossWeight, h.Purity, wu.String,
		soldBasis, soldPremium, soldFace, h.Acquired, src.String, loc.String, 0,
		attr.String, notes.String, cat.String, subcat.String, trophy, date, proceeds); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE lots SET qty=?, basis_usd=?, premium_usd=?, face_value_usd=? WHERE id=?`,
		h.Qty-qty, h.BasisUSD-soldBasis, h.PremiumUSD-soldPremium, h.FaceValueUSD-soldFace, id); err != nil {
		return err
	}
	return tx.Commit()
}

// PutSpot upserts a spot observation keyed by as_of.
func (s *Store) PutSpot(sp model.Spot) error {
	_, err := s.db.Exec(
		`INSERT INTO spot (as_of, gold_usd, silver_usd, platinum_usd, palladium_usd, source)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT(as_of) DO UPDATE SET gold_usd=excluded.gold_usd,
		   silver_usd=excluded.silver_usd, platinum_usd=excluded.platinum_usd,
		   palladium_usd=excluded.palladium_usd, source=excluded.source`,
		sp.AsOf, sp.GoldUSD, sp.SilverUSD, sp.PlatinumUSD, sp.PalladiumUSD, sp.Source)
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
		`SELECT as_of, gold_usd, silver_usd, platinum_usd, palladium_usd, source FROM spot ORDER BY as_of DESC LIMIT 1`).
		Scan(&sp.AsOf, &sp.GoldUSD, &sp.SilverUSD, &sp.PlatinumUSD, &sp.PalladiumUSD, &src)
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
	rows, err := s.db.Query(`SELECT id, kind, name, metal, fine_oz_each, fineness FROM item_type`)
	if err != nil {
		return d, fmt.Errorf("load item_type: %w", err)
	}
	for rows.Next() {
		var t model.ItemType
		var fineness sql.NullString
		if err := rows.Scan(&t.ID, &t.Kind, &t.Name, &t.Metal, &t.FineOzEach, &fineness); err != nil {
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
		`SELECT id, item_type_id, roll_txn_id, activity, qty, gross_weight, purity, weight_unit, basis_usd,
		   premium_usd, face_value_usd, acquired, source, category, subcategory, trophy
		 FROM lots WHERE disposed IS NULL OR disposed = '' ORDER BY id`)
	if err != nil {
		return d, fmt.Errorf("load lots: %w", err)
	}
	for rows.Next() {
		var h model.Holding
		var rtid sql.NullInt64
		var source, cat, subcat, wu sql.NullString
		var trophy int64
		if err := rows.Scan(&h.ID, &h.ItemTypeID, &rtid, &h.Activity, &h.Qty, &h.GrossWeight,
			&h.Purity, &wu, &h.BasisUSD, &h.PremiumUSD, &h.FaceValueUSD, &h.Acquired, &source, &cat, &subcat, &trophy); err != nil {
			rows.Close()
			return d, err
		}
		h.RollTxnID = rtid.Int64
		h.WeightUnit = wu.String
		h.Source = source.String
		h.Category, h.Subcategory, h.Trophy = cat.String, subcat.String, trophy != 0
		d.Lots = append(d.Lots, model.Resolve(h, types[h.ItemTypeID]))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}

	// disposed holdings -> realized P&L (resolved name/metal via the catalog)
	drows, err := s.db.Query(
		`SELECT id, item_type_id, roll_txn_id, activity, qty, basis_usd, disposed_usd, disposed,
		   category, subcategory
		 FROM lots WHERE disposed IS NOT NULL AND disposed != '' ORDER BY disposed, id`)
	if err != nil {
		return d, fmt.Errorf("load disposed lots: %w", err)
	}
	for drows.Next() {
		var itemTypeID int64
		var dl model.DisposedLot
		var rtid sql.NullInt64
		var disposed, cat, subcat sql.NullString
		if err := drows.Scan(&dl.ID, &itemTypeID, &rtid, &dl.Activity, &dl.Qty, &dl.BasisUSD,
			&dl.ProceedsUSD, &disposed, &cat, &subcat); err != nil {
			drows.Close()
			return d, err
		}
		t := types[itemTypeID]
		dl.RollTxnID = rtid.Int64
		dl.Product, dl.Metal, dl.Disposed = t.Name, t.Metal, disposed.String
		dl.Category, dl.Subcategory = cat.String, subcat.String
		d.Disposed = append(d.Disposed, dl)
	}
	drows.Close()
	if err := drows.Err(); err != nil {
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
	if d.Losses, err = s.loadLosses(); err != nil {
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
	// Resolve the branch's canonical name through the logical branch_id link, so
	// grouping/display sees one identity per branch even after a rename or merge.
	rows, err := s.db.Query(`SELECT r.id, r.uid, r.date, r.branch_id, b.name, r.action, r.denom, r.unit,
	  r.amount, r.face_usd, r.source_type, r.notes
	  FROM roll_txns r LEFT JOIN branches b ON b.id = r.branch_id ORDER BY r.id`)
	if err != nil {
		return nil, fmt.Errorf("load roll_txns: %w", err)
	}
	defer rows.Close()
	var out []model.RollTxn
	for rows.Next() {
		var t model.RollTxn
		var branchID sql.NullInt64
		var uid, bank, denom, unit, st, notes sql.NullString
		var amount sql.NullFloat64
		if err := rows.Scan(&t.ID, &uid, &t.Date, &branchID, &bank, &t.Action, &denom, &unit, &amount, &t.FaceUSD, &st, &notes); err != nil {
			return nil, err
		}
		t.UID = uid.String // NullString: roll_txns.uid has no schema NOT NULL — see ListHoldings
		t.BranchID = branchID.Int64
		t.Bank, t.Denom, t.Unit, t.SourceType, t.Notes, t.Amount = bank.String, denom.String, unit.String, st.String, notes.String, amount.Float64
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) loadTrips() ([]model.Trip, error) {
	rows, err := s.db.Query(`SELECT t.id, t.date, t.branch_id, b.name, t.miles, t.hours
	  FROM trips t LEFT JOIN branches b ON b.id = t.branch_id ORDER BY t.id`)
	if err != nil {
		return nil, fmt.Errorf("load trips: %w", err)
	}
	defer rows.Close()
	var out []model.Trip
	for rows.Next() {
		var t model.Trip
		var branchID sql.NullInt64
		var date, bank sql.NullString
		if err := rows.Scan(&t.ID, &date, &branchID, &bank, &t.Miles, &t.Hours); err != nil {
			return nil, err
		}
		t.Date, t.BranchID, t.Bank = date.String, branchID.Int64, bank.String
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) loadBranches() ([]model.Branch, error) {
	rows, err := s.db.Query(`SELECT id, uid, name, institution, address, phone, lat, lon, hours,
	  buys, dumps, denoms, box_limit, box_lead_days, coin_fee_usd, cooldown_days, notes, active
	  FROM branches ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("load branches: %w", err)
	}
	defer rows.Close()
	var out []model.Branch
	for rows.Next() {
		var b model.Branch
		var inst, addr, phone, hours, denoms, notes sql.NullString
		var lat, lon, fee sql.NullFloat64
		var boxLimit, leadDays sql.NullInt64
		var buys, dumps, active int
		if err := rows.Scan(&b.ID, &b.UID, &b.Name, &inst, &addr, &phone, &lat, &lon, &hours,
			&buys, &dumps, &denoms, &boxLimit, &leadDays, &fee, &b.CooldownDays, &notes, &active); err != nil {
			return nil, err
		}
		b.Institution, b.Address, b.Phone, b.Hours, b.Denoms, b.Notes = inst.String, addr.String, phone.String, hours.String, denoms.String, notes.String
		b.Lat, b.Lon, b.CoinFeeUSD = lat.Float64, lon.Float64, fee.Float64
		b.BoxLimit, b.BoxLeadDays = int(boxLimit.Int64), int(leadDays.Int64)
		b.Buys, b.Dumps, b.Active = buys != 0, dumps != 0, active != 0
		out = append(out, b)
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

func (s *Store) loadLosses() ([]model.Loss, error) {
	rows, err := s.db.Query(`SELECT id, date, amount_usd, reason, scope FROM losses ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load losses: %w", err)
	}
	defer rows.Close()
	var out []model.Loss
	for rows.Next() {
		var l model.Loss
		var date, reason, scope sql.NullString
		if err := rows.Scan(&l.ID, &date, &l.AmountUSD, &reason, &scope); err != nil {
			return nil, err
		}
		l.Date, l.Reason, l.Scope = date.String, reason.String, scope.String
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) loadKeepers() ([]model.Keeper, error) {
	rows, err := s.db.Query(`SELECT id, denom, count, face_usd, date, roll_txn_id FROM keepers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load keepers: %w", err)
	}
	defer rows.Close()
	var out []model.Keeper
	for rows.Next() {
		var k model.Keeper
		var denom, date sql.NullString
		var rtid sql.NullInt64
		// date/roll_txn_id (ADR-008) are nullable; legacy rows scan back as
		// empty/zero and leave cladFace unchanged.
		if err := rows.Scan(&k.ID, &denom, &k.Count, &k.FaceUSD, &date, &rtid); err != nil {
			return nil, err
		}
		k.Denom, k.Date, k.RollTxnID = denom.String, date.String, rtid.Int64
		out = append(out, k)
	}
	return out, rows.Err()
}
