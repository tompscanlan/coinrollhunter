package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// om-c8ei: the four durable "which box / which branch" links moved off the recyclable
// integer rowid onto the never-recycled uid (migration 0011). These tests reproduce the
// re-adoption bug the move exists to kill, pin the migration's behavior on pre-existing
// data (including the backfill-before-repoint ordering and the brick-free open of an
// orphaned DB), and lock the resulting schema shape. The repros are written against the
// public store API, so on the PRE-fix tree (integer links) they FAIL — that is the bar.

// holdingByID / keeperByID read one row back through the store's normal read path, so
// they observe the box link exactly as calc/api/web would.
func holdingByID(t *testing.T, s *Store, id int64) model.Holding {
	t.Helper()
	hs, err := s.ListHoldings()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hs {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("holding %d not found", id)
	return model.Holding{}
}

func keeperByID(t *testing.T, s *Store, id int64) model.Keeper {
	t.Helper()
	ks, err := s.ListKeepers()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ks {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("keeper %d not found", id)
	return model.Keeper{}
}

// AC1 — THE RE-ADOPTION REPRO. Insert box A (halves @ Chase), link a find AND a keeper
// to it, delete box A, insert an unrelated box B (dimes @ Riverbend) that RECYCLES A's
// rowid. The old find and keeper must NOT be attributed to box B — they resolve to blank
// (blank beats wrong). On the pre-fix integer-link tree, both re-adopt onto B and this
// test FAILS. That is the bar (contract AC1).
func TestRollTxnReadoptionResolvesToBlankNotWrongBox(t *testing.T) {
	s := openTestStore(t)

	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Kennedy Half", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}

	// Box A is the newest (only) roll_txn, so its rowid is the MAX — the one SQLite
	// recycles on the next insert after a delete (verified: middle rows do not recycle).
	boxA, err := s.InsertRollTxn(model.RollTxn{Date: "2026-07-01", Bank: "Chase Main St", Action: "buy", Denom: "halves", Unit: "box", Amount: 1, FaceUSD: 500})
	if err != nil {
		t.Fatal(err)
	}
	findID, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: boxA, Activity: "crh", Qty: 1, BasisUSD: 0.5, Acquired: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	keepID, err := s.InsertKeeper(model.Keeper{Denom: "halves", Count: 2, FaceUSD: 1.0, Date: "2026-07-01", RollTxnID: boxA})
	if err != nil {
		t.Fatal(err)
	}

	// Delete box A — the exact correction workflow that triggers the bug (om-lv4q: one
	// stray click deletes a row), then enter an unrelated box.
	if err := s.DeleteRollTxn(boxA); err != nil {
		t.Fatal(err)
	}
	boxB, err := s.InsertRollTxn(model.RollTxn{Date: "2026-07-02", Bank: "Riverbend CU", Action: "buy", Denom: "dimes", Unit: "box", Amount: 1, FaceUSD: 250})
	if err != nil {
		t.Fatal(err)
	}

	// PIN the hazard: box B took box A's integer id, or the test proves nothing.
	if boxB != boxA {
		t.Fatalf("SQLite did not recycle the rowid (A=%d B=%d) — the re-adoption hazard was not reproduced", boxA, boxB)
	}

	find := holdingByID(t, s, findID)
	if find.RollTxnID == boxB {
		t.Fatal("re-adoption: a find pulled from the DELETED halves box now reports under the NEW dimes box (right row, wrong parent)")
	}
	if find.RollTxnID != 0 {
		t.Errorf("orphaned find should resolve to blank (0), got box %d", find.RollTxnID)
	}
	keeper := keeperByID(t, s, keepID)
	if keeper.RollTxnID == boxB {
		t.Fatal("re-adoption: a keeper logged against the DELETED box now reports under the NEW box")
	}
	if keeper.RollTxnID != 0 {
		t.Errorf("orphaned keeper should resolve to blank (0), got box %d", keeper.RollTxnID)
	}
}

// AC2 — BRANCH RE-ADOPTION REPRO. A trip to branch A; delete branch A; a new branch B
// recycles the rowid. The old trip must NOT report bank == B's name — it resolves to
// blank. Pre-fix, the integer branch_id recycles onto B and the trip re-parents.
func TestBranchReadoptionResolvesToBlank(t *testing.T) {
	s := openTestStore(t)

	// InsertTrip forks branch A ("Chase Main St"), the newest (only) branch -> max rowid.
	if _, err := s.InsertTrip(model.Trip{Date: "2026-07-01", Bank: "Chase Main St", Miles: 6}); err != nil {
		t.Fatal(err)
	}
	branches, err := s.ListBranches()
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 {
		t.Fatalf("want 1 forked branch, got %d", len(branches))
	}
	branchA := branches[0].ID

	if err := s.DeleteBranch(branchA); err != nil {
		t.Fatal(err)
	}
	branchB, err := s.InsertBranch(model.Branch{Name: "Riverbend CU", Buys: true, Dumps: true, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if branchB != branchA {
		t.Fatalf("branch rowid was not recycled (A=%d B=%d) — hazard not reproduced", branchA, branchB)
	}

	trips, err := s.ListTrips()
	if err != nil {
		t.Fatal(err)
	}
	if len(trips) != 1 {
		t.Fatalf("want the 1 orphaned trip to survive, got %d", len(trips))
	}
	if trips[0].Bank == "Riverbend CU" {
		t.Fatal("re-adoption: a trip to the DELETED branch now reports the REPLACEMENT bank's name")
	}
	if trips[0].Bank != "" || trips[0].BranchID != 0 {
		t.Errorf("orphaned trip should resolve to blank, got bank=%q branch=%d", trips[0].Bank, trips[0].BranchID)
	}
}

// AC7 — MergeBranches still repoints, now by uid. Two spellings fork two branches; a
// merge folds the loser into the survivor. Every repointed roll_txn/trip carries the
// survivor's stable uid, resolves to the survivor's name, and the loser's spelling still
// resolves (its alias moved).
func TestMergeRepointsBranchUidOntoSurvivor(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.InsertRollTxn(model.RollTxn{Date: "2026-01-01", Bank: "Chase Main St", Action: "buy", Denom: "halves", FaceUSD: 500}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRollTxn(model.RollTxn{Date: "2026-01-02", Bank: "Chase Main Street", Action: "buy", Denom: "dimes", FaceUSD: 250}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTrip(model.Trip{Date: "2026-01-02", Bank: "Chase Main Street", Miles: 5}); err != nil {
		t.Fatal(err)
	}

	branches, err := s.ListBranches()
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 {
		t.Fatalf("want 2 branches (typo forks), got %d", len(branches))
	}
	survivor, loser := branches[0], branches[1] // ordered by name: "Chase Main St" < "...Street"

	if err := s.MergeBranches(survivor.ID, []int64{loser.ID}); err != nil {
		t.Fatal(err)
	}

	// Both buys now carry the survivor's uid.
	var onSurvivor int
	if err := s.db.QueryRow(`SELECT count(*) FROM roll_txns WHERE branch_uid=?`, survivor.UID).Scan(&onSurvivor); err != nil {
		t.Fatal(err)
	}
	if onSurvivor != 2 {
		t.Errorf("want 2 roll_txns repointed onto the survivor uid, got %d", onSurvivor)
	}
	// No row still carries the loser's uid.
	var onLoser int
	if err := s.db.QueryRow(`SELECT count(*) FROM roll_txns WHERE branch_uid=?`, loser.UID).Scan(&onLoser); err != nil {
		t.Fatal(err)
	}
	if onLoser != 0 {
		t.Errorf("%d roll_txns still carry the merged-away branch's uid", onLoser)
	}

	txns, _ := s.ListRollTxns()
	for _, tx := range txns {
		if tx.Bank != survivor.Name || tx.BranchID != survivor.ID {
			t.Errorf("txn %d resolved to bank=%q branch=%d, want survivor %q/%d", tx.ID, tx.Bank, tx.BranchID, survivor.Name, survivor.ID)
		}
	}
	trips, _ := s.ListTrips()
	for _, tr := range trips {
		if tr.BranchID != survivor.ID {
			t.Errorf("trip %d branch = %d, want survivor %d", tr.ID, tr.BranchID, survivor.ID)
		}
	}
	// The loser's old spelling still resolves through the moved alias.
	if got, _ := resolveBranchID(s.db, "Chase Main Street"); got != survivor.ID {
		t.Errorf("old spelling resolves to %d, want survivor %d", got, survivor.ID)
	}
}

// AC8/AC9 — the resulting schema shape. The recyclable integer links are gone, the
// stable uid links took their place, branch_aliases keeps its integer (the one deliberate
// exception), no new foreign key was added, and user_version is the current head (12 after
// om-5psc's additive 0012_kept_flag; 0011 is the cutover this test still pins below).
func TestUidCutoverSchemaShape(t *testing.T) {
	s := openTestStore(t)

	if v, err := s.Version(); err != nil || v != 12 {
		t.Errorf("user_version = %d (err %v), want 12", v, err)
	}
	for _, tc := range []struct{ table, col string }{
		{"lots", "roll_txn_id"}, {"keepers", "roll_txn_id"}, {"roll_txns", "branch_id"}, {"trips", "branch_id"},
	} {
		if hasColumn(t, s.db, tc.table, tc.col) {
			t.Errorf("%s still has the recyclable %s column — 0011 should have dropped it", tc.table, tc.col)
		}
	}
	for _, tc := range []struct{ table, col string }{
		{"lots", "roll_txn_uid"}, {"keepers", "roll_txn_uid"}, {"roll_txns", "branch_uid"}, {"trips", "branch_uid"},
	} {
		if !hasColumn(t, s.db, tc.table, tc.col) {
			t.Errorf("%s is missing the stable %s column", tc.table, tc.col)
		}
	}
	// The one deliberate exception: branch_aliases.branch_id STAYS an integer.
	if !hasColumn(t, s.db, "branch_aliases", "branch_id") {
		t.Error("branch_aliases.branch_id should STAY an integer (it cannot orphan)")
	}
	// No new foreign key: exactly one table declares a REFERENCES clause — the
	// pre-existing lots.item_type_id from 0001.
	var refs int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND sql LIKE '%REFERENCES%'`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Errorf("want exactly 1 table with a REFERENCES clause (lots/item_type, pre-existing), got %d", refs)
	}
}

// --- AC4 / AC5: the migration on PRE-EXISTING data ------------------------------------

// scalarStr reads one column that may be SQL NULL.
func scalarStr(t *testing.T, s *Store, q string, args ...any) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// AC4 — BACKFILL + REPOINT on a database seeded at schema 0010. A box with a NULL uid
// (legal before 0011 — the ALTERed column has no schema not-null) is backfilled FIRST,
// so a child pointing at it repoints to the backfilled value rather than being blanked.
// Correctly-linked children freeze the right uid; dangling and unlinked children resolve
// to NULL. The same three states for lots/keepers -> boxes and roll_txns/trips ->
// branches.
func TestMigration0011BackfillsAndRepoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "at0010.db")
	raw, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)

	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 10 {
			continue
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
	}
	mustExec(t, raw, `PRAGMA user_version = 10`)

	// A catalog row for the lots to point at (item_type.id = 1).
	mustExec(t, raw, `INSERT INTO item_type (uid, kind, name, metal) VALUES ('it-uid','coin','Dime','silver')`)
	// box1 carries a NULL uid (must be backfilled BEFORE the repoint); box2 has one.
	mustExec(t, raw, `INSERT INTO roll_txns (id, uid, date, action, face_usd, branch_id) VALUES (1, NULL, '2026-01-01','buy',250, 1)`)
	mustExec(t, raw, `INSERT INTO roll_txns (id, uid, date, action, face_usd, branch_id) VALUES (2, 'box2-uid', '2026-01-02','buy',250, 999)`)
	// branches (uid is NOT NULL at 0008, so supply one).
	mustExec(t, raw, `INSERT INTO branches (id, uid, name) VALUES (1, 'br1-uid', 'Chase')`)
	mustExec(t, raw, `INSERT INTO branches (id, uid, name) VALUES (2, 'br2-uid', 'Riverbend')`)
	// lots: A -> box1 (null-uid, backfilled), B -> box2, C -> 999 (dangling), D -> none.
	mustExec(t, raw, `INSERT INTO lots (uid, item_type_id, roll_txn_id, activity, qty, basis_usd, acquired) VALUES ('lotA', 1, 1,   'crh', 1, 0.5, '2026-01-01')`)
	mustExec(t, raw, `INSERT INTO lots (uid, item_type_id, roll_txn_id, activity, qty, basis_usd, acquired) VALUES ('lotB', 1, 2,   'crh', 1, 0.5, '2026-01-01')`)
	mustExec(t, raw, `INSERT INTO lots (uid, item_type_id, roll_txn_id, activity, qty, basis_usd, acquired) VALUES ('lotC', 1, 999, 'crh', 1, 0.5, '2026-01-01')`)
	mustExec(t, raw, `INSERT INTO lots (uid, item_type_id, activity, qty, basis_usd, acquired)              VALUES ('lotD', 1, 'bullion', 1, 0.5, '2026-01-01')`)
	// keepers: one on box1, one dangling.
	mustExec(t, raw, `INSERT INTO keepers (denom, count, face_usd, roll_txn_id) VALUES ('halves', 2, 1.0, 1)`)
	mustExec(t, raw, `INSERT INTO keepers (denom, count, face_usd, roll_txn_id) VALUES ('dimes',  2, 0.2, 999)`)
	// trips: one on branch2, one dangling.
	mustExec(t, raw, `INSERT INTO trips (id, date, branch_id, miles) VALUES (1, '2026-01-01', 2,   5)`)
	mustExec(t, raw, `INSERT INTO trips (id, date, branch_id, miles) VALUES (2, '2026-01-02', 999, 5)`)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen through the store -> migration 0011 applies (backfill, add, repoint, drop).
	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrate to 0011: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// (a) every roll_txns row now has a non-empty uid — box1's NULL was backfilled first.
	var bad int
	if err := s.db.QueryRow(`SELECT count(*) FROM roll_txns WHERE uid IS NULL OR uid=''`).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Errorf("roll_txns has %d NULL/empty uid after the STEP-1 backfill", bad)
	}
	box1uid := scalarStr(t, s, `SELECT uid FROM roll_txns WHERE id=1`)
	if !box1uid.Valid || !looksLikeUUIDv4(box1uid.String) {
		t.Fatalf("box1 uid = %v, want a backfilled lowercase v4", box1uid)
	}

	// (b) correctly-linked children froze the RIGHT uid — including lotA, whose box only
	// got a uid during this migration (backfill-before-repoint working end to end).
	if got := scalarStr(t, s, `SELECT roll_txn_uid FROM lots WHERE uid='lotA'`); got.String != box1uid.String {
		t.Errorf("lotA roll_txn_uid = %v, want box1's backfilled uid %q", got, box1uid.String)
	}
	if got := scalarStr(t, s, `SELECT roll_txn_uid FROM lots WHERE uid='lotB'`); got.String != "box2-uid" {
		t.Errorf("lotB roll_txn_uid = %v, want box2-uid", got)
	}
	if got := scalarStr(t, s, `SELECT roll_txn_uid FROM keepers WHERE denom='halves'`); got.String != box1uid.String {
		t.Errorf("keeper-on-box1 roll_txn_uid = %v, want box1's uid", got)
	}

	// (c) a dangling integer and an absent one both land as NULL (blank, not a dangling
	// string, not 0).
	if got := scalarStr(t, s, `SELECT roll_txn_uid FROM lots WHERE uid='lotC'`); got.Valid {
		t.Errorf("lotC (dangling roll_txn_id 999) got roll_txn_uid %q, want NULL", got.String)
	}
	if got := scalarStr(t, s, `SELECT roll_txn_uid FROM lots WHERE uid='lotD'`); got.Valid {
		t.Errorf("lotD (no box link) got roll_txn_uid %q, want NULL", got.String)
	}
	if got := scalarStr(t, s, `SELECT roll_txn_uid FROM keepers WHERE denom='dimes'`); got.Valid {
		t.Errorf("dangling keeper got roll_txn_uid %q, want NULL", got.String)
	}

	// (d) the same three states for roll_txns/trips -> branches.
	if got := scalarStr(t, s, `SELECT branch_uid FROM roll_txns WHERE id=1`); got.String != "br1-uid" {
		t.Errorf("box1 branch_uid = %v, want br1-uid", got)
	}
	if got := scalarStr(t, s, `SELECT branch_uid FROM roll_txns WHERE id=2`); got.Valid {
		t.Errorf("box2 (dangling branch_id 999) got branch_uid %q, want NULL", got.String)
	}
	if got := scalarStr(t, s, `SELECT branch_uid FROM trips WHERE id=1`); got.String != "br2-uid" {
		t.Errorf("trip1 branch_uid = %v, want br2-uid", got)
	}
	if got := scalarStr(t, s, `SELECT branch_uid FROM trips WHERE id=2`); got.Valid {
		t.Errorf("trip2 (dangling branch_id 999) got branch_uid %q, want NULL", got.String)
	}
}

// AC5 — NO BRICKED DATABASE. A database carrying a pre-existing orphan (a lots row whose
// roll_txn_id names no box) still opens, lists, and computes after 0011. The migration
// adds no constraint an existing DB could violate — the sibling of om-1czp's brick test.
func TestOrphanedLinkDBStillOpensThrough0011(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orphaned.db")
	raw, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 10 {
			continue
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
	}
	mustExec(t, raw, `PRAGMA user_version = 10`)
	mustExec(t, raw, `INSERT INTO item_type (uid, kind, name, metal) VALUES ('it-uid','coin','Dime','silver')`)
	// An orphan: a find whose box was deleted and whose rowid nobody took. No such box.
	mustExec(t, raw, `INSERT INTO lots (uid, item_type_id, roll_txn_id, activity, qty, basis_usd, acquired) VALUES ('orphan', 1, 424242, 'crh', 1, 0.5, '2026-01-01')`)
	mustExec(t, raw, `INSERT INTO keepers (denom, count, face_usd, roll_txn_id) VALUES ('halves', 2, 1.0, 424242)`)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// Must migrate/open cleanly — a failure here is a ledger that will not launch.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening a database with a pre-existing orphan link FAILED — 0011 bricked it: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Lists, and the orphan reads back blank (not attributed, not 0-as-a-real-box).
	hs, err := s.ListHoldings()
	if err != nil {
		t.Fatalf("listing an orphaned-link db failed: %v", err)
	}
	if len(hs) != 1 || hs[0].RollTxnID != 0 {
		t.Errorf("orphaned find should read back with a blank box link, got %+v", hs)
	}
	// And it computes (ResolveDataset drives the same resolve).
	if _, err := s.ResolveDataset(); err != nil {
		t.Fatalf("ResolveDataset over an orphaned-link db failed: %v", err)
	}
}

// --- F1: an update preserves an already-orphaned row's stored uid ---------------------

// storedUID reads a row's stored *_uid column directly (bypassing the read-path resolve),
// so a test can prove what is actually persisted, not just what reads back.
func storedUID(t *testing.T, s *Store, q string, args ...any) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// F1 — editing an UNRELATED cell on an already-orphaned find/keeper must PRESERVE the
// dead box's stored uid, not overwrite it to NULL. The blank box link on the wire
// (RollTxnID 0, how an orphan reads back) means "no change to the box link" now, mirroring
// SellHolding — so the forensic orphan-uid trace that export preserves survives an edit.
// Fails before the F1 fix (the update re-resolved 0 -> NULL); passes after.
func TestUpdatePreservesOrphanedBoxUid(t *testing.T) {
	s := openTestStore(t)
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Kennedy Half", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.InsertRollTxn(model.RollTxn{Date: "2026-07-01", Bank: "Chase Main St", Action: "buy", Denom: "halves", FaceUSD: 500})
	if err != nil {
		t.Fatal(err)
	}
	deadUID := storedUID(t, s, `SELECT uid FROM roll_txns WHERE id=?`, box)
	findID, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: box, Activity: "crh", Qty: 1, BasisUSD: 0.5, Acquired: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	keepID, err := s.InsertKeeper(model.Keeper{Denom: "halves", Count: 2, FaceUSD: 1.0, Date: "2026-07-01", RollTxnID: box})
	if err != nil {
		t.Fatal(err)
	}

	// Delete the box: the find + keeper now read back blank (RollTxnID 0), but their
	// stored uid still names the dead box.
	if err := s.DeleteRollTxn(box); err != nil {
		t.Fatal(err)
	}
	find := holdingByID(t, s, findID)
	if find.RollTxnID != 0 {
		t.Fatalf("precondition: orphaned find should read RollTxnID 0, got %d", find.RollTxnID)
	}

	// Edit an unrelated cell and write back — the PUT-as-merge shape (RollTxnID stays 0).
	find.Notes = "edited after the box was deleted"
	if err := s.UpdateHolding(findID, find); err != nil {
		t.Fatal(err)
	}
	if got := storedUID(t, s, `SELECT roll_txn_uid FROM lots WHERE id=?`, findID); !got.Valid || got.String != deadUID.String {
		t.Errorf("orphaned find's stored roll_txn_uid = %v after an unrelated edit, want the dead box's uid %q preserved", got, deadUID.String)
	}

	keeper := keeperByID(t, s, keepID)
	if keeper.RollTxnID != 0 {
		t.Fatalf("precondition: orphaned keeper should read RollTxnID 0, got %d", keeper.RollTxnID)
	}
	keeper.Count = 3
	if err := s.UpdateKeeper(keepID, keeper); err != nil {
		t.Fatal(err)
	}
	if got := storedUID(t, s, `SELECT roll_txn_uid FROM keepers WHERE id=?`, keepID); !got.Valid || got.String != deadUID.String {
		t.Errorf("orphaned keeper's stored roll_txn_uid = %v after an unrelated edit, want %q preserved", got, deadUID.String)
	}
}

// F1 — the same preservation for the branch link on roll_txns/trips: a branch that was
// deleted reads back Bank "" on the wire, and an unrelated edit must not erase the stored
// branch_uid.
func TestUpdatePreservesOrphanedBranchUid(t *testing.T) {
	s := openTestStore(t)
	boxID, err := s.InsertRollTxn(model.RollTxn{Date: "2026-01-01", Bank: "Chase Main St", Action: "buy", Denom: "halves", FaceUSD: 500})
	if err != nil {
		t.Fatal(err)
	}
	tripID, err := s.InsertTrip(model.Trip{Date: "2026-01-01", Bank: "Chase Main St", Miles: 6})
	if err != nil {
		t.Fatal(err)
	}
	branches, err := s.ListBranches()
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 {
		t.Fatalf("want 1 branch, got %d", len(branches))
	}
	deadUID := branches[0].UID

	if err := s.DeleteBranch(branches[0].ID); err != nil {
		t.Fatal(err)
	}

	// Read the roll_txn back (Bank now ""), edit an unrelated cell, write back.
	txns, err := s.ListRollTxns()
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 1 || txns[0].Bank != "" {
		t.Fatalf("precondition: orphaned roll_txn should read blank bank, got %+v", txns)
	}
	rt := txns[0]
	rt.Notes = "edited after the branch was deleted"
	if err := s.UpdateRollTxn(boxID, rt); err != nil {
		t.Fatal(err)
	}
	if got := storedUID(t, s, `SELECT branch_uid FROM roll_txns WHERE id=?`, boxID); !got.Valid || got.String != deadUID {
		t.Errorf("orphaned roll_txn's stored branch_uid = %v after an unrelated edit, want %q preserved", got, deadUID)
	}

	// Same for the trip.
	trips, err := s.ListTrips()
	if err != nil {
		t.Fatal(err)
	}
	tr := trips[0]
	tr.Miles = 7
	if err := s.UpdateTrip(tripID, tr); err != nil {
		t.Fatal(err)
	}
	if got := storedUID(t, s, `SELECT branch_uid FROM trips WHERE id=?`, tripID); !got.Valid || got.String != deadUID {
		t.Errorf("orphaned trip's stored branch_uid = %v after an unrelated edit, want %q preserved", got, deadUID)
	}
}

// --- F2: D3 pinned on the post-cutover write path -------------------------------------

// F2 — writing a NONZERO phantom box id (no such box) stores NULL, never errors and never
// 400s (the frozen D3 decision). Guards against a future "reject unknown id" regression.
// The old phantom-id assertions (RollTxnID 42/43) were correctly rewritten to real boxes
// when the cutover landed, leaving D3 unpinned on the write path until now.
func TestWriteWithUnknownBoxIsStoredAsNull(t *testing.T) {
	s := openTestStore(t)
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	const phantom = 999999 // no such roll_txn

	findID, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: phantom, Activity: "crh", Qty: 1, BasisUSD: 0.5, Acquired: "2026-07-01"})
	if err != nil {
		t.Fatalf("InsertHolding with an unknown box id must NOT error (D3 stores NULL), got %v", err)
	}
	if got := storedUID(t, s, `SELECT roll_txn_uid FROM lots WHERE id=?`, findID); got.Valid {
		t.Errorf("unknown box id -> stored roll_txn_uid should be NULL, got %q", got.String)
	}
	if find := holdingByID(t, s, findID); find.RollTxnID != 0 {
		t.Errorf("unknown box id -> read-back RollTxnID should be 0 (blank), got %d", find.RollTxnID)
	}

	keepID, err := s.InsertKeeper(model.Keeper{Denom: "dimes", Count: 5, FaceUSD: 0.5, RollTxnID: phantom})
	if err != nil {
		t.Fatalf("InsertKeeper with an unknown box id must NOT error (D3 stores NULL), got %v", err)
	}
	if got := storedUID(t, s, `SELECT roll_txn_uid FROM keepers WHERE id=?`, keepID); got.Valid {
		t.Errorf("unknown box id -> stored keeper roll_txn_uid should be NULL, got %q", got.String)
	}
	if k := keeperByID(t, s, keepID); k.RollTxnID != 0 {
		t.Errorf("unknown box id -> read-back keeper RollTxnID should be 0, got %d", k.RollTxnID)
	}
}
