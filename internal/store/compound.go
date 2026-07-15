package store

import (
	"database/sql"
	"fmt"
	"math"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// Package store, compound workflows (om-2sl6). Every user-visible "action" in the Do
// tab used to be assembled client-side out of several independent POSTs, with no
// transaction spanning them: a failure after the first left the ledger half-written
// with no undo, and the user's natural response (re-press the still-populated form)
// duplicated the part that succeeded. These methods move the seam server-side — one
// method per compound action, each opening ONE transaction (Store.WithTx) — so a
// failure at any step leaves the DB byte-for-byte as it was.
//
// They are ORCHESTRATORS, not leaf writers: they never touch the DB directly, they
// compose the SAME tx-bound, self-validating mutations the legacy importer uses
// (Tx.Insert* / Tx.Update*), each of which validates in its own body — the AST-guard
// chokepoint (validate_ast_test.go). That is why they are named Record*/Revise*
// rather than Insert*/Update*: they are deliberately outside the leaf-mutation set the
// guard enumerates, because every ACTUAL write still funnels through a guarded Tx twin.
// The composite is not a new door into the ledger; it is a lock around several existing
// ones.
//
// One rule (store.go's WithTx warning made concrete): NEVER call a *Store method from
// inside these. WithTx holds the pool's only connection AND the write lock; a *Store
// call would block on the connection forever, or deadlock on the lock. Compose *Tx
// methods and the execer-taking helpers only.

// ItemTypeSpec is the catalog half of a holding-with-type write: the fields that
// find-or-create the item_type a holding points at. It mirrors the client's old
// ensureItemType (grids.svelte.ts) — Name+Metal+Fineness identify the type; a matched
// row whose FineOzEach has drifted is refreshed.
type ItemTypeSpec struct {
	Name       string
	Metal      string
	Fineness   string
	FineOzEach float64
}

// FindSpec is one logged find: the catalog type it needs plus the CRH holding itself.
// RecordFinds fills in Holding.ItemTypeID (from the resolved type) and Holding.RollTxnID
// (from the resolved box); the caller leaves both zero.
type FindSpec struct {
	Type    ItemTypeSpec
	Holding model.Holding
}

// BoxRef names the box a logged-finds submission attributes its finds and keepers to:
// an EXISTING roll_txn by id (passed straight through — an unknown id resolves to a NULL
// link on write, D3), a NEW one created inside the same transaction (its forked branch
// rolls back with the finds), or neither (zero id + nil New => no box).
type BoxRef struct {
	ExistingID int64
	New        *model.RollTxn
}

// RecordPurchase is the "Bought a box" action (om-2sl6 seam a): a roll_txn buy and,
// optionally, the bank trip logged in the same gesture — ONE transaction. Branch
// resolution for BOTH runs inside the tx (Tx.InsertRollTxn / Tx.InsertTrip), so a
// failed trip rolls back the box AND the branch the box forked — no orphan branch
// (seam f, the 18:47 pitfall).
func (s *Store) RecordPurchase(purchase model.RollTxn, trip *model.Trip) (rollTxnID, tripID int64, err error) {
	err = s.WithTx(func(tx *Tx) error {
		var e error
		if rollTxnID, e = tx.InsertRollTxn(purchase); e != nil {
			return e
		}
		if trip != nil {
			if tripID, e = tx.InsertTrip(*trip); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return rollTxnID, tripID, nil
}

// RecordFinds is the "Logged finds" action (om-2sl6 seam b): an optional box (existing
// or created inline), then N CRH finds — each find-or-creating its item_type — and M
// clad keepers, all attributed to that box, all in ONE transaction. A failure at any
// find or keeper rolls back the whole submission, including a box and the branch forked
// for it (seam f). Post-om-5psc a kept find is one flagged holding row, never a
// duplicate keeper: this method writes exactly the finds and keepers the caller passes.
func (s *Store) RecordFinds(box BoxRef, finds []FindSpec, keepers []model.Keeper) error {
	return s.WithTx(func(tx *Tx) error {
		boxID, err := tx.resolveBox(box)
		if err != nil {
			return err
		}
		for _, f := range finds {
			tid, err := tx.ensureItemType(f.Type, f.Holding.Activity)
			if err != nil {
				return err
			}
			h := f.Holding
			h.ItemTypeID = tid
			h.RollTxnID = boxID
			if _, err := tx.InsertHolding(h); err != nil {
				return err
			}
		}
		for _, k := range keepers {
			k.RollTxnID = boxID
			if _, err := tx.InsertKeeper(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordHolding is the holdings-with-type CREATE (om-2sl6 seams c-create, d NewBullion,
// e Reconcile.addFind): find-or-create the item_type for spec, then insert the holding
// pointing at it — ONE transaction. Returns the new holding id.
func (s *Store) RecordHolding(spec ItemTypeSpec, h model.Holding) (int64, error) {
	var id int64
	err := s.WithTx(func(tx *Tx) error {
		tid, err := tx.ensureItemType(spec, h.Activity)
		if err != nil {
			return err
		}
		h.ItemTypeID = tid
		id, err = tx.InsertHolding(h)
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ReviseHolding is the Edit-tab Holdings grid UPDATE (om-2sl6 seam c-update): it
// find-or-creates the item_type for spec and applies the caller's merge onto the stored
// holding, then writes — ONE transaction. `apply` is the PUT merge (decode the request
// body ONTO the current row, so a column the client does not name is a column it cannot
// blank — om-kyq7); it runs INSIDE the tx, on the row read INSIDE the tx, so the
// read-merge-write is atomic against other writers exactly as the generic PUT's
// WithWrite makes it. ItemTypeID on the merged row is overwritten with the resolved
// type, so the catalog fields — not a stale id — decide which type the lot points at.
func (s *Store) ReviseHolding(id int64, spec ItemTypeSpec, apply func(model.Holding) (model.Holding, error)) error {
	return s.WithTx(func(tx *Tx) error {
		cur, err := getHolding(tx.db, id)
		if err != nil {
			return err
		}
		merged, err := apply(cur)
		if err != nil {
			return err
		}
		tid, err := tx.ensureItemType(spec, merged.Activity)
		if err != nil {
			return err
		}
		merged.ItemTypeID = tid
		return tx.UpdateHolding(id, merged)
	})
}

// resolveBox returns the box id a logged-finds submission attributes to: a new roll_txn
// created inside this tx (its forked branch rolls back with the finds), an existing id
// passed straight through, or 0 for no box.
func (tx *Tx) resolveBox(b BoxRef) (int64, error) {
	if b.New != nil {
		return tx.InsertRollTxn(*b.New)
	}
	return b.ExistingID, nil
}

// ensureItemType find-or-creates the item_type for a holding-with-type write and returns
// its id — the server-side twin of the client's old ensureItemType. On a match it
// refreshes a drifted FineOzEach (> 1e-9, as the client did); on a miss it inserts a new
// catalog row (kind "junk" for CRH finds, "coin" otherwise). Both the update and the
// insert go through the validating Tx mutations, so a bad catalog value is a rejected,
// rolled-back write like any other.
func (tx *Tx) ensureItemType(spec ItemTypeSpec, activity string) (int64, error) {
	t, found, err := findItemType(tx.db, spec)
	if err != nil {
		return 0, err
	}
	if found {
		if math.Abs(t.FineOzEach-spec.FineOzEach) > 1e-9 {
			t.FineOzEach = spec.FineOzEach
			if err := tx.UpdateItemType(t.ID, t); err != nil {
				return 0, err
			}
		}
		return t.ID, nil
	}
	kind := "coin"
	if activity == "crh" {
		kind = "junk"
	}
	name := spec.Name
	if name == "" {
		name = "Unnamed" // mirrors the client's `row.product || 'Unnamed'`
	}
	return tx.InsertItemType(model.ItemType{
		Kind: kind, Name: name, Metal: spec.Metal, FineOzEach: spec.FineOzEach, Fineness: spec.Fineness,
	})
}

// findItemType returns the FIRST item_type (lowest id) whose name+metal+fineness match
// spec after folding case + surrounding whitespace, mirroring the client's
// types.find(t => norm(t.name)===norm(product) && …). The whole row is read so a
// drift-refresh (ensureItemType) preserves every other catalog column (year/mint/refs),
// exactly as the client's {...match, fine_oz_each} spread did.
func findItemType(x execer, spec ItemTypeSpec) (model.ItemType, bool, error) {
	var t model.ItemType
	var uid, fineness, year, mint, mintmark, refs sql.NullString
	err := x.QueryRow(
		`SELECT id, uid, kind, name, metal, fine_oz_each, fineness, year, mint, mintmark, refs FROM item_type
		 WHERE lower(trim(name)) = lower(trim(?))
		   AND lower(trim(metal)) = lower(trim(?))
		   AND lower(trim(coalesce(fineness,''))) = lower(trim(?))
		 ORDER BY id LIMIT 1`,
		spec.Name, spec.Metal, spec.Fineness).
		Scan(&t.ID, &uid, &t.Kind, &t.Name, &t.Metal, &t.FineOzEach, &fineness, &year, &mint, &mintmark, &refs)
	if err == sql.ErrNoRows {
		return model.ItemType{}, false, nil
	}
	if err != nil {
		return model.ItemType{}, false, fmt.Errorf("find item_type %q/%q/%q: %w", spec.Name, spec.Metal, spec.Fineness, err)
	}
	t.UID = uid.String
	t.Fineness, t.Year, t.Mint, t.Mintmark, t.References = fineness.String, year.String, mint.String, mintmark.String, refs.String
	return t, true, nil
}

// getHolding reads a single holding by id in the SAME shape ListHoldings returns
// (roll_txn_uid resolved back to the box's CURRENT id), so ReviseHolding's merge starts
// from exactly the row the generic PUT merge would have fetched. Returns ErrNotFound
// when no row matches — a PUT to a missing holding is a 404, as the generic route is.
func getHolding(x execer, id int64) (model.Holding, error) {
	var h model.Holding
	var rtid sql.NullInt64
	var uid, wu, src, loc, attr, notes, cat, subcat, disp sql.NullString
	var trophy, kept int64
	err := x.QueryRow(
		`SELECT l.id, l.uid, l.item_type_id, rt.id, l.activity, l.qty, l.gross_weight, l.purity, l.weight_unit, l.basis_usd,
		   l.premium_usd, l.face_value_usd, l.acquired, l.source, l.location, l.insured_value, l.attributes,
		   l.notes, l.category, l.subcategory, l.trophy, l.kept, l.disposed, l.disposed_usd
		 FROM lots l LEFT JOIN roll_txns rt ON rt.uid = l.roll_txn_uid WHERE l.id = ?`, id).
		Scan(&h.ID, &uid, &h.ItemTypeID, &rtid, &h.Activity, &h.Qty, &h.GrossWeight, &h.Purity,
			&wu, &h.BasisUSD, &h.PremiumUSD, &h.FaceValueUSD, &h.Acquired, &src, &loc,
			&h.InsuredValue, &attr, &notes, &cat, &subcat, &trophy, &kept, &disp, &h.DisposedUSD)
	if err == sql.ErrNoRows {
		return model.Holding{}, ErrNotFound
	}
	if err != nil {
		return model.Holding{}, fmt.Errorf("get holding %d: %w", id, err)
	}
	h.UID = uid.String
	h.RollTxnID = rtid.Int64
	h.WeightUnit, h.Source, h.Location = wu.String, src.String, loc.String
	h.Attributes, h.Notes, h.Disposed = attr.String, notes.String, disp.String
	h.Category, h.Subcategory, h.Trophy, h.Kept = cat.String, subcat.String, trophy != 0, kept != 0
	return h, nil
}
