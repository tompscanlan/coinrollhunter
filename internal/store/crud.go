package store

import (
	"database/sql"
	"fmt"

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
	rows, err := s.db.Query(`SELECT id, kind, name, metal, fine_oz_each, fineness, year, mint, mintmark, refs FROM item_type ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list item_type: %w", err)
	}
	defer rows.Close()
	var out []model.ItemType
	for rows.Next() {
		var t model.ItemType
		var fineness, year, mint, mintmark, refs sql.NullString
		if err := rows.Scan(&t.ID, &t.Kind, &t.Name, &t.Metal, &t.FineOzEach, &fineness, &year, &mint, &mintmark, &refs); err != nil {
			return nil, err
		}
		t.Fineness, t.Year, t.Mint, t.Mintmark, t.References = fineness.String, year.String, mint.String, mintmark.String, refs.String
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateItemType(id int64, t model.ItemType) error {
	res, err := s.db.Exec(
		`UPDATE item_type SET kind=?, name=?, metal=?, fine_oz_each=?, fineness=?, year=?, mint=?, mintmark=?, refs=? WHERE id=?`,
		t.Kind, t.Name, t.Metal, t.FineOzEach, t.Fineness, t.Year, t.Mint, t.Mintmark, t.References, id)
	return affected(res, err, "update item_type")
}

func (s *Store) DeleteItemType(id int64) error { return s.deleteByID("item_type", id) }

// --- holdings (lots) ---------------------------------------------------------

func (s *Store) ListHoldings() ([]model.Holding, error) {
	rows, err := s.db.Query(
		`SELECT id, item_type_id, roll_txn_id, activity, qty, gross_weight, purity, weight_unit, basis_usd,
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
		var wu, src, loc, attr, notes, cat, subcat, disp sql.NullString
		var trophy int64
		if err := rows.Scan(&h.ID, &h.ItemTypeID, &rtid, &h.Activity, &h.Qty, &h.GrossWeight, &h.Purity,
			&wu, &h.BasisUSD, &h.PremiumUSD, &h.FaceValueUSD, &h.Acquired, &src, &loc,
			&h.InsuredValue, &attr, &notes, &cat, &subcat, &trophy, &disp, &h.DisposedUSD); err != nil {
			return nil, err
		}
		h.RollTxnID = rtid.Int64
		h.WeightUnit, h.Source, h.Location = wu.String, src.String, loc.String
		h.Attributes, h.Notes, h.Disposed = attr.String, notes.String, disp.String
		h.Category, h.Subcategory, h.Trophy = cat.String, subcat.String, trophy != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) UpdateHolding(id int64, h model.Holding) error {
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
	res, err := s.db.Exec(
		`UPDATE roll_txns SET date=?, bank=?, action=?, denom=?, unit=?, amount=?, face_usd=?, source_type=?, notes=? WHERE id=?`,
		t.Date, t.Bank, t.Action, t.Denom, t.Unit, t.Amount, t.FaceUSD, t.SourceType, t.Notes, id)
	return affected(res, err, "update roll_txn")
}

func (s *Store) DeleteRollTxn(id int64) error { return s.deleteByID("roll_txns", id) }

// --- trips -------------------------------------------------------------------

func (s *Store) ListTrips() ([]model.Trip, error) { return s.loadTrips() }

func (s *Store) UpdateTrip(id int64, t model.Trip) error {
	res, err := s.db.Exec(`UPDATE trips SET date=?, bank=?, miles=?, hours=? WHERE id=?`,
		t.Date, t.Bank, t.Miles, t.Hours, id)
	return affected(res, err, "update trip")
}

func (s *Store) DeleteTrip(id int64) error { return s.deleteByID("trips", id) }

// --- supplies ----------------------------------------------------------------

func (s *Store) ListSupplies() ([]model.Supply, error) { return s.loadSupplies() }

func (s *Store) UpdateSupply(id int64, x model.Supply) error {
	res, err := s.db.Exec(`UPDATE supplies SET date=?, item=?, cost_usd=? WHERE id=?`,
		x.Date, x.Item, x.CostUSD, id)
	return affected(res, err, "update supply")
}

func (s *Store) DeleteSupply(id int64) error { return s.deleteByID("supplies", id) }

// --- keepers -----------------------------------------------------------------

func (s *Store) ListKeepers() ([]model.Keeper, error) { return s.loadKeepers() }

func (s *Store) UpdateKeeper(id int64, k model.Keeper) error {
	res, err := s.db.Exec(`UPDATE keepers SET denom=?, count=?, face_usd=?, date=?, roll_txn_id=? WHERE id=?`,
		k.Denom, k.Count, k.FaceUSD, nullStr(k.Date), nullID(k.RollTxnID), id)
	return affected(res, err, "update keeper")
}

func (s *Store) DeleteKeeper(id int64) error { return s.deleteByID("keepers", id) }

// --- losses (shrinkage write-offs; ADR-005) ----------------------------------

func (s *Store) ListLosses() ([]model.Loss, error) { return s.loadLosses() }

func (s *Store) UpdateLoss(id int64, l model.Loss) error {
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
