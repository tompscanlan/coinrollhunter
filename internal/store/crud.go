package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// This file rounds out the store with the list/update/delete operations the REST
// API needs. Inserts and the resolved-load live in data.go.

// deleteByID deletes a row by primary key from one of a fixed set of tables.
// The table name is never taken from user input — callers pass a literal.
func (s *Store) deleteByID(table string, id int64) error {
	res, err := s.db.Exec("DELETE FROM "+table+" WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete %s/%d: %w", table, id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrNotFound is returned by update/delete when no row matches the id.
var ErrNotFound = fmt.Errorf("not found")

// --- item_type ---------------------------------------------------------------

func (s *Store) ListItemTypes() ([]model.ItemType, error) {
	rows, err := s.db.Query(`SELECT id, uid, kind, name, metal, fine_oz_each, fineness, year, mint, mintmark, refs FROM item_type ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list item_type: %w", err)
	}
	defer rows.Close()
	var out []model.ItemType
	for rows.Next() {
		var t model.ItemType
		var uid, fineness, year, mint, mintmark, refs sql.NullString
		if err := rows.Scan(&t.ID, &uid, &t.Kind, &t.Name, &t.Metal, &t.FineOzEach, &fineness, &year, &mint, &mintmark, &refs); err != nil {
			return nil, err
		}
		// NullString, not string: item_type.uid has no schema-level NOT NULL (ADR-009 (c),
		// migration 0010), so a row written by some other tool could still read back NULL.
		// Scanning it as a plain string would error out the whole list rather than surface it.
		t.UID = uid.String
		t.Fineness, t.Year, t.Mint, t.Mintmark, t.References = fineness.String, year.String, mint.String, mintmark.String, refs.String
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateItemType(id int64, t model.ItemType) error {
	if err := t.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE item_type SET kind=?, name=?, metal=?, fine_oz_each=?, fineness=?, year=?, mint=?, mintmark=?, refs=? WHERE id=?`,
		t.Kind, t.Name, t.Metal, t.FineOzEach, t.Fineness, t.Year, t.Mint, t.Mintmark, t.References, id)
	return affected(res, err, "update item_type")
}

func (s *Store) DeleteItemType(id int64) error { return s.deleteByID("item_type", id) }

// --- holdings (lots) ---------------------------------------------------------

func (s *Store) ListHoldings() ([]model.Holding, error) {
	rows, err := s.db.Query(
		`SELECT id, uid, item_type_id, roll_txn_id, activity, qty, gross_weight, purity, weight_unit, basis_usd,
		   premium_usd, face_value_usd, acquired, source, location, insured_value, attributes,
		   notes, category, subcategory, trophy, disposed, disposed_usd
		 FROM lots ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list lots: %w", err)
	}
	defer rows.Close()
	var out []model.Holding
	for rows.Next() {
		var h model.Holding
		var rtid sql.NullInt64
		var uid, wu, src, loc, attr, notes, cat, subcat, disp sql.NullString
		var trophy int64
		if err := rows.Scan(&h.ID, &uid, &h.ItemTypeID, &rtid, &h.Activity, &h.Qty, &h.GrossWeight, &h.Purity,
			&wu, &h.BasisUSD, &h.PremiumUSD, &h.FaceValueUSD, &h.Acquired, &src, &loc,
			&h.InsuredValue, &attr, &notes, &cat, &subcat, &trophy, &disp, &h.DisposedUSD); err != nil {
			return nil, err
		}
		// NullString, not string: lots.uid has no schema-level NOT NULL (ADR-009 (c)),
		// so a row written by some other tool could still read back NULL. Scanning it
		// as a plain string would error out the whole list rather than surfacing it.
		h.UID = uid.String
		h.RollTxnID = rtid.Int64
		h.WeightUnit, h.Source, h.Location = wu.String, src.String, loc.String
		h.Attributes, h.Notes, h.Disposed = attr.String, notes.String, disp.String
		h.Category, h.Subcategory, h.Trophy = cat.String, subcat.String, trophy != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) UpdateHolding(id int64, h model.Holding) error {
	if err := h.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE lots SET item_type_id=?, roll_txn_id=?, activity=?, qty=?, gross_weight=?, purity=?, weight_unit=?,
		   basis_usd=?, premium_usd=?, face_value_usd=?, acquired=?, source=?, location=?,
		   insured_value=?, attributes=?, notes=?, category=?, subcategory=?, trophy=?, disposed=?, disposed_usd=? WHERE id=?`,
		h.ItemTypeID, nullID(h.RollTxnID), h.Activity, h.Qty, h.GrossWeight, h.Purity, h.WeightUnit,
		h.BasisUSD, h.PremiumUSD, h.FaceValueUSD, h.Acquired, h.Source, h.Location,
		h.InsuredValue, h.Attributes, h.Notes, h.Category, h.Subcategory, b2i(h.Trophy), h.Disposed, h.DisposedUSD, id)
	return affected(res, err, "update holding")
}

func (s *Store) DeleteHolding(id int64) error { return s.deleteByID("lots", id) }

// nullID maps a 0 id to SQL NULL (for the optional roll_txn_id link), else the id.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// nullStr maps an empty string to SQL NULL (for optional text columns like a
// keeper's audit date), else the string. Keeps nullable columns truly NULL for
// legacy/unspecified values rather than storing "".
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// b2i maps a bool to SQLite's 0/1 integer form (used for the lots.trophy flag).
func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// --- roll_txns ---------------------------------------------------------------

func (s *Store) ListRollTxns() ([]model.RollTxn, error) { return s.loadRollTxns() }

func (s *Store) UpdateRollTxn(id int64, t model.RollTxn) error {
	// Validate before resolveBranchID: a bad txn must not fork a branch as a side effect.
	if err := t.Validate(); err != nil {
		return err
	}
	bid, err := resolveBranchID(s.db, t.Bank)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE roll_txns SET date=?, branch_id=?, action=?, denom=?, unit=?, amount=?, face_usd=?, source_type=?, notes=? WHERE id=?`,
		t.Date, nullID(bid), t.Action, t.Denom, t.Unit, t.Amount, t.FaceUSD, t.SourceType, t.Notes, id)
	return affected(res, err, "update roll_txn")
}

func (s *Store) DeleteRollTxn(id int64) error { return s.deleteByID("roll_txns", id) }

// --- trips -------------------------------------------------------------------

func (s *Store) ListTrips() ([]model.Trip, error) { return s.loadTrips() }

func (s *Store) UpdateTrip(id int64, t model.Trip) error {
	// Validate before resolveBranchID: a bad trip must not fork a branch as a side effect.
	if err := t.Validate(); err != nil {
		return err
	}
	bid, err := resolveBranchID(s.db, t.Bank)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE trips SET date=?, branch_id=?, miles=?, hours=? WHERE id=?`,
		t.Date, nullID(bid), t.Miles, t.Hours, id)
	return affected(res, err, "update trip")
}

func (s *Store) DeleteTrip(id int64) error { return s.deleteByID("trips", id) }

// --- branches (ADR-010) ------------------------------------------------------

func (s *Store) ListBranches() ([]model.Branch, error) { return s.loadBranches() }

// UpdateBranch updates every column except the immutable uid. Editing the
// canonical name also records it as an alias, so the old name still resolves.
func (s *Store) UpdateBranch(id int64, b model.Branch) error {
	if err := b.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE branches SET name=?, institution=?, address=?, phone=?, lat=?, lon=?, hours=?,
		   buys=?, dumps=?, denoms=?, box_limit=?, box_lead_days=?, coin_fee_usd=?, cooldown_days=?, notes=?, active=?
		 WHERE id=?`,
		strings.TrimSpace(b.Name), b.Institution, b.Address, b.Phone, b.Lat, b.Lon, b.Hours,
		b2i(b.Buys), b2i(b.Dumps), b.Denoms, b.BoxLimit, b.BoxLeadDays, b.CoinFeeUSD,
		b.CooldownDays, b.Notes, b2i(b.Active), id)
	if err := affected(res, err, "update branch"); err != nil {
		return err
	}
	if name := strings.TrimSpace(b.Name); name != "" {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO branch_aliases (branch_id, alias) VALUES (?,?)`, id, name); err != nil {
			return fmt.Errorf("update branch alias: %w", err)
		}
	}
	return nil
}

// DeleteBranch removes a branch and its aliases. History rows whose branch_id
// still points here simply resolve to no name until reassigned — the merge path
// (MergeBranches) is the safe way to retire a duplicate, since it repoints first.
func (s *Store) DeleteBranch(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM branch_aliases WHERE branch_id=?`, id); err != nil {
		return fmt.Errorf("delete branch aliases: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM branches WHERE id=?`, id)
	if err := affected(res, err, "delete branch"); err != nil {
		return err
	}
	return tx.Commit()
}

// MergeBranches folds each loser branch into survivor: it repoints roll_txns,
// trips, and aliases from the losers onto survivor, then deletes the loser rows.
// This is the ADR-010 (b) dedup — the survivor keeps the whole history and every
// old free-text spelling still resolves through the moved aliases.
func (s *Store) MergeBranches(survivor int64, losers []int64) error {
	// Under the store write lock, for the same reason as SellHolding: this repoints
	// rows in other tables, and a partial update merging one of those rows concurrently
	// would write back the pre-merge branch_id and undo the repoint.
	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, loser := range losers {
		if loser == survivor {
			continue
		}
		if _, err := tx.Exec(`UPDATE roll_txns SET branch_id=? WHERE branch_id=?`, survivor, loser); err != nil {
			return fmt.Errorf("merge roll_txns: %w", err)
		}
		if _, err := tx.Exec(`UPDATE trips SET branch_id=? WHERE branch_id=?`, survivor, loser); err != nil {
			return fmt.Errorf("merge trips: %w", err)
		}
		if _, err := tx.Exec(`UPDATE branch_aliases SET branch_id=? WHERE branch_id=?`, survivor, loser); err != nil {
			return fmt.Errorf("merge aliases: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM branches WHERE id=?`, loser); err != nil {
			return fmt.Errorf("merge delete: %w", err)
		}
	}
	return tx.Commit()
}

// --- supplies ----------------------------------------------------------------

func (s *Store) ListSupplies() ([]model.Supply, error) { return s.loadSupplies() }

func (s *Store) UpdateSupply(id int64, x model.Supply) error {
	if err := x.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE supplies SET date=?, item=?, cost_usd=? WHERE id=?`,
		x.Date, x.Item, x.CostUSD, id)
	return affected(res, err, "update supply")
}

func (s *Store) DeleteSupply(id int64) error { return s.deleteByID("supplies", id) }

// --- keepers -----------------------------------------------------------------

func (s *Store) ListKeepers() ([]model.Keeper, error) { return s.loadKeepers() }

func (s *Store) UpdateKeeper(id int64, k model.Keeper) error {
	if err := k.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE keepers SET denom=?, count=?, face_usd=?, date=?, roll_txn_id=? WHERE id=?`,
		k.Denom, k.Count, k.FaceUSD, nullStr(k.Date), nullID(k.RollTxnID), id)
	return affected(res, err, "update keeper")
}

func (s *Store) DeleteKeeper(id int64) error { return s.deleteByID("keepers", id) }

// --- losses (shrinkage write-offs; ADR-005) ----------------------------------

func (s *Store) ListLosses() ([]model.Loss, error) { return s.loadLosses() }

func (s *Store) UpdateLoss(id int64, l model.Loss) error {
	if err := l.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE losses SET date=?, amount_usd=?, reason=?, scope=? WHERE id=?`,
		l.Date, l.AmountUSD, l.Reason, l.Scope, id)
	return affected(res, err, "update loss")
}

func (s *Store) DeleteLoss(id int64) error { return s.deleteByID("losses", id) }

// --- spot history ------------------------------------------------------------

func (s *Store) ListSpot() ([]model.Spot, error) {
	rows, err := s.db.Query(`SELECT as_of, gold_usd, silver_usd, platinum_usd, palladium_usd, source FROM spot ORDER BY as_of`)
	if err != nil {
		return nil, fmt.Errorf("list spot: %w", err)
	}
	defer rows.Close()
	var out []model.Spot
	for rows.Next() {
		var sp model.Spot
		var src sql.NullString
		if err := rows.Scan(&sp.AsOf, &sp.GoldUSD, &sp.SilverUSD, &sp.PlatinumUSD, &sp.PalladiumUSD, &src); err != nil {
			return nil, err
		}
		sp.Source = src.String
		out = append(out, sp)
	}
	return out, rows.Err()
}

// affected wraps an Exec result: it turns a 0-rows update into ErrNotFound.
func affected(res sql.Result, err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
