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
// name resolves to 0 (no branch). A newly-typed variant still forks a fresh
// branch by design; the address-book merge (ADR-010 (b)) is how forks get repointed.
//
// It is the store's hidden writer: called by InsertRollTxn, InsertTrip and their
// Update twins, it INSERTs on a miss. It takes an execer rather than binding to
// s.db so that a branch forked inside a transaction is rolled back with it — before
// om-u3el a failed import could leave an orphan bank branch behind.
func resolveBranchID(x execer, name string) (int64, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return 0, nil
	}
	var id int64
	err := x.QueryRow(
		`SELECT b.id FROM branches b
		 LEFT JOIN branch_aliases a ON a.branch_id = b.id
		 WHERE b.name = ? OR a.alias = ? LIMIT 1`, n, n).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case err == sql.ErrNoRows:
		b := model.Branch{Name: n, Buys: true, Dumps: true, Active: true}
		// The branch is synthesized here, not supplied by a caller, but it still goes
		// through the model rules: this is a write into the ledger like any other, and
		// nothing may reach insertBranch without passing them.
		if err := b.Validate(); err != nil {
			return 0, err
		}
		return insertBranch(x, b)
	default:
		return 0, fmt.Errorf("resolve branch %q: %w", n, err)
	}
}

// resolveBranchUID find-or-creates the branch for a typed bank name (via
// resolveBranchID) and returns its STABLE uid — the value roll_txns.branch_uid /
// trips.branch_uid now store instead of the recyclable integer id (om-c8ei, ADR-009).
// An empty name resolves to "" (a NULL branch_uid). The branch demonstrably exists in
// the same statement it is resolved, so this is the id->uid half of the store's Shape-A
// contract: the integer keeps travelling on the wire, only the STORED link is the uid.
func resolveBranchUID(x execer, name string) (string, error) {
	id, err := resolveBranchID(x, name)
	if err != nil {
		return "", err
	}
	if id == 0 {
		return "", nil
	}
	var uid sql.NullString
	if err := x.QueryRow(`SELECT uid FROM branches WHERE id = ?`, id).Scan(&uid); err != nil {
		return "", fmt.Errorf("resolve branch uid for %q: %w", name, err)
	}
	// branches.uid is NOT NULL at the schema level (0008), so this is defensive only:
	// a null there would blank the link (blank beats wrong), never error the write.
	return uid.String, nil
}

// rollTxnUID maps a caller-supplied box (roll_txn) id to that box's stable uid — the
// value lots.roll_txn_uid / keepers.roll_txn_uid now store (om-c8ei, ADR-009). It is
// the id->uid half of Shape A: on WRITE the box's current id is resolved to its uid, so
// a later delete+insert that recycles the integer can no longer re-adopt this child.
//   - id 0 (no link)        -> nil (SQL NULL).
//   - id names no box (D3)  -> nil (SQL NULL). blank beats wrong; do NOT reject the
//     write, or legacy import would gain a new failure mode on the new-user on-ramp.
//   - id names a live box   -> its uid.
// Returns `any` so the nil case binds as SQL NULL directly.
func rollTxnUID(x execer, id int64) (any, error) {
	if id == 0 {
		return nil, nil
	}
	var uid sql.NullString
	err := x.QueryRow(`SELECT uid FROM roll_txns WHERE id = ?`, id).Scan(&uid)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve roll_txn uid for %d: %w", id, err)
	}
	if !uid.Valid {
		return nil, nil
	}
	return uid.String, nil
}

// --- inserts -----------------------------------------------------------------
//
// Each insert exists twice: an exported method on *Store (auto-commit — one
// statement, its own implicit transaction) and its twin on *Tx (the same write,
// inside a caller's WithTx transaction). Both validate, then hand the SQL to one
// private helper that takes an execer, so there is exactly one copy of every
// statement and the two paths cannot drift.
//
// The repeated `x.Validate()` line is deliberate and must stay in each method's own
// body: internal/store/validate_ast_test.go reads the BODY of every mutation, and
// folding the call into the shared helper would blind that chokepoint guard. The
// guard is what stops a transaction-bound writer from becoming a second,
// UNVALIDATED door into the ledger — precisely the hole om-1czp closed.

// InsertItemType inserts a catalog row and returns its new id. As with lots and
// roll_txns, the uid is server-generated and never taken from the caller: it is the
// catalog entry's permanent identity (ADR-009, migration 0010), and item_type.uid
// has no schema-level NOT NULL to fall back on — this insert path IS the guarantee.
func (s *Store) InsertItemType(t model.ItemType) (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, err
	}
	return insertItemType(s.db, t)
}

// InsertItemType inserts a catalog row inside the transaction. See *Store.InsertItemType.
func (tx *Tx) InsertItemType(t model.ItemType) (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, err
	}
	return insertItemType(tx.db, t)
}

func insertItemType(x execer, t model.ItemType) (int64, error) {
	res, err := x.Exec(
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
	if err := h.Validate(); err != nil {
		return 0, err
	}
	return insertHolding(s.db, h)
}

// InsertHolding inserts a specimen row inside the transaction. See *Store.InsertHolding.
func (tx *Tx) InsertHolding(h model.Holding) (int64, error) {
	if err := h.Validate(); err != nil {
		return 0, err
	}
	return insertHolding(tx.db, h)
}

func insertHolding(x execer, h model.Holding) (int64, error) {
	ruid, err := rollTxnUID(x, h.RollTxnID) // id->uid: store the box's stable uid, not its rowid
	if err != nil {
		return 0, err
	}
	res, err := x.Exec(
		`INSERT INTO lots (uid, item_type_id, roll_txn_uid, activity, qty, gross_weight, purity, weight_unit,
		   basis_usd, premium_usd, face_value_usd, acquired, source, location, insured_value,
		   attributes, notes, category, subcategory, trophy, kept, disposed, disposed_usd)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		newUID(), h.ItemTypeID, ruid, h.Activity, h.Qty, h.GrossWeight, h.Purity, h.WeightUnit,
		h.BasisUSD, h.PremiumUSD, h.FaceValueUSD, h.Acquired, h.Source, h.Location, h.InsuredValue,
		h.Attributes, h.Notes, h.Category, h.Subcategory, b2i(h.Trophy), b2i(h.Kept), h.Disposed, h.DisposedUSD)
	if err != nil {
		return 0, fmt.Errorf("insert holding: %w", err)
	}
	return res.LastInsertId()
}

// InsertRollTxn inserts a roll transaction and returns its new id. The typed bank
// name find-or-creates a branch (ADR-010); only the resolved branch's stable uid is
// stored (om-c8ei).
func (s *Store) InsertRollTxn(t model.RollTxn) (int64, error) {
	// Validate before resolveBranchID: a bad txn must not fork a branch as a side effect.
	if err := t.Validate(); err != nil {
		return 0, err
	}
	// Auto-commit, single statement on s.db (om-u3el's contract): the demo seeder wraps a
	// whole run in its own ambient BEGIN and the poller writes bare, so this must NOT open
	// its own transaction. Seam f (om-2sl6) — a "rolled-back box must leave no orphan
	// branch" — is a COMPOUND-path concern: it is closed by RecordPurchase/RecordFinds,
	// which resolve the branch through Tx.InsertRollTxn inside the one workflow transaction.
	return insertRollTxn(s.db, t)
}

// InsertRollTxn inserts a roll transaction inside the transaction — including any
// branch its bank name forks, which rolls back with it. See *Store.InsertRollTxn.
func (tx *Tx) InsertRollTxn(t model.RollTxn) (int64, error) {
	// Validate before resolveBranchID: a bad txn must not fork a branch as a side effect.
	if err := t.Validate(); err != nil {
		return 0, err
	}
	return insertRollTxn(tx.db, t)
}

func insertRollTxn(x execer, t model.RollTxn) (int64, error) {
	buid, err := resolveBranchUID(x, t.Bank)
	if err != nil {
		return 0, err
	}
	res, err := x.Exec(
		`INSERT INTO roll_txns (uid, date, branch_uid, action, denom, unit, amount, face_usd, source_type, notes)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		newUID(), t.Date, nullStr(buid), t.Action, t.Denom, t.Unit, t.Amount, t.FaceUSD, t.SourceType, t.Notes)
	if err != nil {
		return 0, fmt.Errorf("insert roll_txn: %w", err)
	}
	return res.LastInsertId()
}

// InsertTrip inserts a trip and returns its new id (bank find-or-creates a branch).
func (s *Store) InsertTrip(t model.Trip) (int64, error) {
	// Validate before resolveBranchID: a bad trip must not fork a branch as a side effect.
	if err := t.Validate(); err != nil {
		return 0, err
	}
	// Auto-commit, single statement on s.db — see *Store.InsertRollTxn on why this stays
	// non-transactional (the demo seeder's ambient BEGIN) and where seam f is closed.
	return insertTrip(s.db, t)
}

// InsertTrip inserts a trip inside the transaction — including any branch its bank
// name forks, which rolls back with it. See *Store.InsertTrip.
func (tx *Tx) InsertTrip(t model.Trip) (int64, error) {
	// Validate before resolveBranchID: a bad trip must not fork a branch as a side effect.
	if err := t.Validate(); err != nil {
		return 0, err
	}
	return insertTrip(tx.db, t)
}

func insertTrip(x execer, t model.Trip) (int64, error) {
	buid, err := resolveBranchUID(x, t.Bank)
	if err != nil {
		return 0, err
	}
	res, err := x.Exec(`INSERT INTO trips (date, branch_uid, miles, hours) VALUES (?,?,?,?)`,
		t.Date, nullStr(buid), t.Miles, t.Hours)
	if err != nil {
		return 0, fmt.Errorf("insert trip: %w", err)
	}
	return res.LastInsertId()
}

// InsertBranch inserts a branch (server-generated opaque uid) and records its
// canonical name as an alias so resolveBranchID and merges can find it. Returns
// the new id.
func (s *Store) InsertBranch(b model.Branch) (int64, error) {
	if err := b.Validate(); err != nil {
		return 0, err
	}
	return insertBranch(s.db, b)
}

// InsertBranch inserts a branch inside the transaction. See *Store.InsertBranch.
func (tx *Tx) InsertBranch(b model.Branch) (int64, error) {
	if err := b.Validate(); err != nil {
		return 0, err
	}
	return insertBranch(tx.db, b)
}

// insertBranch writes the branch and its canonical-name alias — TWO statements, which
// is why it must be able to run inside the caller's transaction: on the auto-commit
// path a failure between them leaves a branch with no alias.
func insertBranch(x execer, b model.Branch) (int64, error) {
	res, err := x.Exec(
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
		if _, err := x.Exec(`INSERT OR IGNORE INTO branch_aliases (branch_id, alias) VALUES (?,?)`, id, name); err != nil {
			return 0, fmt.Errorf("insert branch alias: %w", err)
		}
	}
	return id, nil
}

// InsertSupply inserts a supply and returns its new id.
func (s *Store) InsertSupply(x model.Supply) (int64, error) {
	if err := x.Validate(); err != nil {
		return 0, err
	}
	return insertSupply(s.db, x)
}

// InsertSupply inserts a supply inside the transaction. See *Store.InsertSupply.
func (tx *Tx) InsertSupply(x model.Supply) (int64, error) {
	if err := x.Validate(); err != nil {
		return 0, err
	}
	return insertSupply(tx.db, x)
}

func insertSupply(x execer, sup model.Supply) (int64, error) {
	res, err := x.Exec(`INSERT INTO supplies (date, item, cost_usd) VALUES (?,?,?)`,
		sup.Date, sup.Item, sup.CostUSD)
	if err != nil {
		return 0, fmt.Errorf("insert supply: %w", err)
	}
	return res.LastInsertId()
}

// InsertLoss inserts a shrinkage/loss adjustment and returns its new id (ADR-005).
func (s *Store) InsertLoss(l model.Loss) (int64, error) {
	if err := l.Validate(); err != nil {
		return 0, err
	}
	return insertLoss(s.db, l)
}

// InsertLoss inserts a loss inside the transaction. See *Store.InsertLoss.
func (tx *Tx) InsertLoss(l model.Loss) (int64, error) {
	if err := l.Validate(); err != nil {
		return 0, err
	}
	return insertLoss(tx.db, l)
}

func insertLoss(x execer, l model.Loss) (int64, error) {
	res, err := x.Exec(`INSERT INTO losses (date, amount_usd, reason, scope) VALUES (?,?,?,?)`,
		l.Date, l.AmountUSD, l.Reason, l.Scope)
	if err != nil {
		return 0, fmt.Errorf("insert loss: %w", err)
	}
	return res.LastInsertId()
}

// InsertKeeper inserts a keeper and returns its new id. date + the box link (ADR-008)
// are nullable: an empty date stores SQL NULL, and the box id is resolved to the box's
// stable uid (om-c8ei) — id 0 or an unknown box stores a NULL roll_txn_uid.
func (s *Store) InsertKeeper(k model.Keeper) (int64, error) {
	if err := k.Validate(); err != nil {
		return 0, err
	}
	return insertKeeper(s.db, k)
}

// InsertKeeper inserts a keeper inside the transaction. See *Store.InsertKeeper.
func (tx *Tx) InsertKeeper(k model.Keeper) (int64, error) {
	if err := k.Validate(); err != nil {
		return 0, err
	}
	return insertKeeper(tx.db, k)
}

func insertKeeper(x execer, k model.Keeper) (int64, error) {
	ruid, err := rollTxnUID(x, k.RollTxnID) // id->uid: the box link is a stable uid now
	if err != nil {
		return 0, err
	}
	res, err := x.Exec(`INSERT INTO keepers (denom, count, face_usd, date, roll_txn_uid) VALUES (?,?,?,?,?)`,
		k.Denom, k.Count, k.FaceUSD, nullStr(k.Date), ruid)
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
	// The sale has no model struct of its own, so it validates through the shared
	// helper — routing qty/proceeds/date through model.ErrInvalid so a bad sale is a
	// 400, not the 500 the ad-hoc checks used to produce.
	if err := model.ValidateSale(qty, proceeds, date); err != nil {
		return err
	}
	// Under the store write lock: a sale that commits inside the window of a partial
	// update (which reads the lot, merges the body onto it, and writes it back) would
	// be erased by that write-back — the merge's snapshot predates the sale. Sales are
	// the one mutation a Holdings-grid edit never names and so can never intend.
	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var h model.Holding
	var rtuid, wu, src, loc, attr, notes, cat, subcat, disp sql.NullString
	var trophy, kept int64
	err = tx.QueryRow(
		`SELECT item_type_id, roll_txn_uid, activity, qty, gross_weight, purity, weight_unit,
		   basis_usd, premium_usd, face_value_usd, acquired, source, location, insured_value,
		   attributes, notes, category, subcategory, trophy, kept, disposed, disposed_usd
		 FROM lots WHERE id=?`, id).Scan(
		&h.ItemTypeID, &rtuid, &h.Activity, &h.Qty, &h.GrossWeight, &h.Purity, &wu,
		&h.BasisUSD, &h.PremiumUSD, &h.FaceValueUSD, &h.Acquired, &src, &loc, &h.InsuredValue,
		&attr, &notes, &cat, &subcat, &trophy, &kept, &disp, &h.DisposedUSD)
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
	// the caller's vocabulary; the user sold half a lot. The box link (roll_txn_uid)
	// must ride onto the carve-out too, or every partially-sold find loses its box
	// (om-c8ei): rtuid is the ORIGINAL lot's stored uid, copied through verbatim —
	// already a stable uid, so no id->uid resolution here — and binds as SQL NULL when
	// the source lot had no box. trophy AND kept ride across the same way (om-5psc): a
	// partial sale of a KEPT find must not silently un-keep the carved-out portion —
	// the om-hdk5 hand-enumerated-column trap CLAUDE.md names, which no guard catches.
	if _, err := tx.Exec(
		`INSERT INTO lots (uid, item_type_id, roll_txn_uid, activity, qty, gross_weight, purity, weight_unit,
		   basis_usd, premium_usd, face_value_usd, acquired, source, location, insured_value,
		   attributes, notes, category, subcategory, trophy, kept, disposed, disposed_usd)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		newUID(), h.ItemTypeID, rtuid, h.Activity, qty, h.GrossWeight, h.Purity, wu.String,
		soldBasis, soldPremium, soldFace, h.Acquired, src.String, loc.String, 0,
		attr.String, notes.String, cat.String, subcat.String, trophy, kept, date, proceeds); err != nil {
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
	if err := sp.Validate(); err != nil {
		return err
	}
	return putSpot(s.db, sp)
}

// PutSpot upserts a spot observation inside the transaction. See *Store.PutSpot.
func (tx *Tx) PutSpot(sp model.Spot) error {
	if err := sp.Validate(); err != nil {
		return err
	}
	return putSpot(tx.db, sp)
}

func putSpot(x execer, sp model.Spot) error {
	_, err := x.Exec(
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
	if err := cfg.Validate(); err != nil {
		return err
	}
	return putSettings(s.db, cfg)
}

// PutSettings writes the settings inside the transaction — six upserts that land or
// roll back as one. It is what keeps a REJECTED import file from silently rewriting
// the user's settings (they are upserts, so they used to survive the failure).
// See *Store.PutSettings.
func (tx *Tx) PutSettings(cfg model.Settings) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return putSettings(tx.db, cfg)
}

func putSettings(x execer, cfg model.Settings) error {
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
		"strip_exif_on_import":          strconv.FormatBool(cfg.StripEXIFOnImport),
	}
	for k, v := range kv {
		if _, err := x.Exec(
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
		case "strip_exif_on_import":
			cfg.StripEXIFOnImport, _ = strconv.ParseBool(v)
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
		`SELECT as_of, gold_usd, silver_usd, platinum_usd, palladium_usd, source FROM spot
		 ORDER BY substr(as_of,1,10) DESC,
		          CASE WHEN lower(source)='manual' THEN 1 ELSE 0 END DESC,
		          as_of DESC
		 LIMIT 1`).
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

	// holdings -> resolved lots. The box link is stored as roll_txn_uid (om-c8ei); a
	// LEFT JOIN resolves it back to the box's CURRENT id, so calc keeps seeing the
	// integer RollTxnID it always did — now always the right box, never a recycled
	// rowid. A deleted box leaves rt.id NULL -> RollTxnID 0 (blank, not wrong).
	// l.uid rides along so the resolved Lot carries it (T3, om-6hlp): report.lots is the
	// only place the front end sees a lot's uid, and the trophy feed keys its photos by it.
	// NullString because lots.uid has no schema NOT NULL (ADR-009 (c)).
	rows, err = s.db.Query(
		`SELECT l.id, l.uid, l.item_type_id, rt.id, l.activity, l.qty, l.gross_weight, l.purity, l.weight_unit, l.basis_usd,
		   l.premium_usd, l.face_value_usd, l.acquired, l.source, l.category, l.subcategory, l.trophy, l.kept
		 FROM lots l LEFT JOIN roll_txns rt ON rt.uid = l.roll_txn_uid
		 WHERE l.disposed IS NULL OR l.disposed = '' ORDER BY l.id`)
	if err != nil {
		return d, fmt.Errorf("load lots: %w", err)
	}
	for rows.Next() {
		var h model.Holding
		var rtid sql.NullInt64
		var uid, source, cat, subcat, wu sql.NullString
		var trophy, kept int64
		if err := rows.Scan(&h.ID, &uid, &h.ItemTypeID, &rtid, &h.Activity, &h.Qty, &h.GrossWeight,
			&h.Purity, &wu, &h.BasisUSD, &h.PremiumUSD, &h.FaceValueUSD, &h.Acquired, &source, &cat, &subcat, &trophy, &kept); err != nil {
			rows.Close()
			return d, err
		}
		h.UID = uid.String
		h.RollTxnID = rtid.Int64
		h.WeightUnit = wu.String
		h.Source = source.String
		h.Category, h.Subcategory, h.Trophy, h.Kept = cat.String, subcat.String, trophy != 0, kept != 0
		d.Lots = append(d.Lots, model.Resolve(h, types[h.ItemTypeID]))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}

	// disposed holdings -> realized P&L (resolved name/metal via the catalog). Same
	// box-link resolution as the live lots above: roll_txn_uid -> the box's current id.
	drows, err := s.db.Query(
		`SELECT l.id, l.item_type_id, rt.id, l.activity, l.qty, l.basis_usd, l.disposed_usd, l.disposed,
		   l.category, l.subcategory
		 FROM lots l LEFT JOIN roll_txns rt ON rt.uid = l.roll_txn_uid
		 WHERE l.disposed IS NOT NULL AND l.disposed != '' ORDER BY l.disposed, l.id`)
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
	// Resolve the branch through the stable branch_uid link (om-c8ei), reading back the
	// branch's CURRENT id (b.id) so BranchID keeps grouping by one identity per branch
	// after a rename or merge — and a deleted/merged-away branch resolves to blank
	// (b.id NULL -> BranchID 0, name ""), never onto a recycled rowid's replacement.
	rows, err := s.db.Query(`SELECT r.id, r.uid, r.date, b.id, b.name, r.action, r.denom, r.unit,
	  r.amount, r.face_usd, r.source_type, r.notes
	  FROM roll_txns r LEFT JOIN branches b ON b.uid = r.branch_uid ORDER BY r.id`)
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
	rows, err := s.db.Query(`SELECT t.id, t.date, b.id, b.name, t.miles, t.hours
	  FROM trips t LEFT JOIN branches b ON b.uid = t.branch_uid ORDER BY t.id`)
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
	// The box link is stored as roll_txn_uid (om-c8ei); resolve it back to the box's
	// current id (rt.id) so RollTxnID keeps carrying the integer the model always did.
	rows, err := s.db.Query(`SELECT k.id, k.denom, k.count, k.face_usd, k.date, rt.id
	  FROM keepers k LEFT JOIN roll_txns rt ON rt.uid = k.roll_txn_uid ORDER BY k.id`)
	if err != nil {
		return nil, fmt.Errorf("load keepers: %w", err)
	}
	defer rows.Close()
	var out []model.Keeper
	for rows.Next() {
		var k model.Keeper
		var denom, date sql.NullString
		var rtid sql.NullInt64
		// date/roll_txn link (ADR-008) are nullable; legacy rows and a deleted box scan
		// back as empty/zero and leave cladFace unchanged.
		if err := rows.Scan(&k.ID, &denom, &k.Count, &k.FaceUSD, &date, &rtid); err != nil {
			return nil, err
		}
		k.Denom, k.Date, k.RollTxnID = denom.String, date.String, rtid.Int64
		out = append(out, k)
	}
	return out, rows.Err()
}
