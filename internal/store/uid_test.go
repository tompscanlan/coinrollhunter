package store

import (
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// ADR-009 (c): lots.uid and roll_txns.uid are ALTERed-in columns, and a UNIQUE
// index does NOT imply NOT NULL — SQLite treats NULLs as mutually distinct, so any
// number of rows can carry a NULL uid straight through the index. The schema
// cannot hold this line. These tests are the guard that does.
//
// Invariant tests in the ADR-001 spirit: they assert a property that must hold for
// any dataset, not a worked example. A photo orphaned by a NULL owner uid is
// unrecoverable — nothing else on disk says which coin it was.

// openTestStore gives a migrated in-memory database — every migration 0001..0009
// applied, exactly as a real one would be.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// everyRowHasAUID is the invariant. Anything that can create a lot, a roll txn or
// an item type has to satisfy it, which is why the callers below drive real store
// methods rather than raw SQL.
//
// item_type joined the list with migration 0010: export emits lots.item_type_uid
// as the join key from a specimen to what it *is*, and the rowid it replaces is
// recycled by a delete — which would repoint an old spreadsheet's lots at the
// wrong coin type. ADR-009 named this exact trigger ("a live problem only if
// export starts emitting item_type_id as a foreign key").
func everyRowHasAUID(t *testing.T, s *Store) {
	t.Helper()
	for _, table := range []string{"item_type", "lots", "roll_txns", "photos"} {
		var bad int
		q := `SELECT count(*) FROM ` + table + ` WHERE uid IS NULL OR trim(uid) = ''`
		if err := s.db.QueryRow(q).Scan(&bad); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if bad != 0 {
			t.Errorf("%s: %d row(s) with a NULL or empty uid — identity is unrecoverable", table, bad)
		}
	}
}

func TestEveryInsertPathGeneratesAUID(t *testing.T) {
	s := openTestStore(t)

	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver", FineOzEach: 0.0723})
	if err != nil {
		t.Fatal(err)
	}
	txnID, err := s.InsertRollTxn(model.RollTxn{Date: "2026-07-01", Bank: "Test CU", Action: "buy", Denom: "dimes", Unit: "box", Amount: 1, FaceUSD: 250})
	if err != nil {
		t.Fatal(err)
	}
	lotID, err := s.InsertHolding(model.Holding{
		ItemTypeID: typeID, RollTxnID: txnID, Activity: "crh",
		Qty: 10, BasisUSD: 1.0, FaceValueUSD: 1.0, Acquired: "2026-07-01",
	})
	if err != nil {
		t.Fatal(err)
	}

	everyRowHasAUID(t, s)

	// The partial-sell path is the one that gets missed: it carves out a NEW lot row
	// without the word "insert" appearing anywhere in the caller's vocabulary. The
	// user sold half a lot; the store created a specimen.
	if err := s.SellHolding(lotID, 4, 30.0, "2026-07-05"); err != nil {
		t.Fatal(err)
	}
	var lots int
	if err := s.db.QueryRow(`SELECT count(*) FROM lots`).Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if lots != 2 {
		t.Fatalf("partial sale should have split the lot in two, got %d lots", lots)
	}
	everyRowHasAUID(t, s)
}

// The failure this whole ADR exists to prevent. lots.id is a bare rowid alias, so
// SQLite hands out max(rowid)+1: delete the highest lot, insert another, and the
// integer comes back. A photo filed under it would be adopted, silently, by a
// different coin.
func TestDeleteThenInsertRecyclesTheIDButNeverTheUID(t *testing.T) {
	s := openTestStore(t)
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Kennedy Half", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	mk := func() (int64, string) {
		t.Helper()
		id, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.5, Acquired: "2026-07-01"})
		if err != nil {
			t.Fatal(err)
		}
		var uid string
		if err := s.db.QueryRow(`SELECT uid FROM lots WHERE id=?`, id).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		return id, uid
	}

	firstID, firstUID := mk()
	if err := s.DeleteHolding(firstID); err != nil {
		t.Fatal(err)
	}
	secondID, secondUID := mk()

	if secondID != firstID {
		t.Logf("note: rowid was not recycled here (%d then %d) — the uid guarantee is what we actually rely on", firstID, secondID)
	}
	if secondUID == firstUID {
		t.Fatal("uid was REUSED across a delete+insert — a photo would rebind to the wrong coin")
	}
	if !looksLikeUUIDv4(secondUID) {
		t.Errorf("uid %q is not a lowercase UUIDv4", secondUID)
	}
}

// Photos are a NEW table, so unlike the two ALTERed columns they can declare
// NOT NULL UNIQUE outright — and SQLite enforces it. Pin that, so a later
// migration cannot quietly relax it.
func TestPhotosTableEnforcesUIDAndSupportsManyPerOwner(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES (NULL,'lot','abc','obverse',0,'jpg')`); err == nil {
		t.Error("photos accepted a NULL uid — the schema NOT NULL is gone")
	}
	if _, err := s.db.Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES ('dup','lot','abc','obverse',0,'jpg')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES ('dup','lot','abc','reverse',1,'jpg')`); err == nil {
		t.Error("photos accepted a duplicate uid — the UNIQUE constraint is gone")
	}

	// Two per item is a floor, not a ceiling: a mintmark close-up, doubling, a slab
	// label. And a photo must be able to hang off a box/roll record, not just a coin.
	for _, p := range []struct{ uid, kind, owner, role string }{
		{"p2", "lot", "abc", "reverse"},
		{"p3", "lot", "abc", "detail"},
		{"p4", "lot", "abc", "detail"},
		{"p5", "roll_txn", "xyz", "box-end"},
		{"p6", "roll_txn", "xyz", "receipt"},
	} {
		if _, err := s.db.Exec(
			`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES (?,?,?,?,0,'jpg')`,
			p.uid, p.kind, p.owner, p.role); err != nil {
			t.Fatalf("insert %s: %v", p.uid, err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM photos WHERE owner_kind='lot' AND owner_uid='abc'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("want 4 photos on the lot, got %d", n)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM photos WHERE owner_kind='roll_txn' AND owner_uid='xyz'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("want 2 photos on the roll txn, got %d", n)
	}

	// A NULL role would LOSE the photo: the gallery query filters
	// `role NOT IN ('obverse','reverse')`, and NULL NOT IN (...) is NULL, not true —
	// so the image would sit in the database, on disk, and never render.
	if _, err := s.db.Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES ('p7','lot','abc',NULL,0,'jpg')`); err == nil {
		t.Error("photos accepted a NULL role — un-roled images would vanish from the gallery")
	}
}

// The migration backfills existing rows in SQL (no Go step), relying on
// randomblob() re-evaluating per row. If it ever evaluated once, every legacy row
// would share one uid — and the UNIQUE index would only catch it on the second row.
func TestMigrationBackfillGivesLegacyRowsDistinctUIDs(t *testing.T) {
	s := openTestStore(t)

	// Rows as they existed before 0009: written straight to SQL with no uid.
	for i := 0; i < 25; i++ {
		if _, err := s.db.Exec(`INSERT INTO roll_txns (uid, date, action, face_usd) VALUES (NULL,'2026-01-01','buy',250)`); err != nil {
			t.Fatal(err)
		}
	}
	// Re-run the backfill exactly as the migration writes it.
	if _, err := s.db.Exec(`UPDATE roll_txns SET uid =
	  lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
	  substr(lower(hex(randomblob(2))), 2) || '-' ||
	  substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
	  lower(hex(randomblob(6)))
	  WHERE uid IS NULL`); err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.Query(`SELECT uid FROM roll_txns`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if seen[uid] {
			t.Fatalf("backfill produced a DUPLICATE uid %q — randomblob() was evaluated once, not per row", uid)
		}
		if !looksLikeUUIDv4(uid) {
			t.Fatalf("backfilled uid %q is not a lowercase UUIDv4", uid)
		}
		seen[uid] = true
	}
	if len(seen) != 25 {
		t.Errorf("want 25 distinct backfilled uids, got %d", len(seen))
	}
	everyRowHasAUID(t, s)
}

// Migration 0010 (item_type.uid) backfills rows that already exist. A catalog row
// written before 0010 has to come out the other side with the SAME id — every lot
// in the database points at it by that integer — and a fresh, distinct uid.
func TestItemTypeBackfillKeepsIDsAndGivesDistinctUIDs(t *testing.T) {
	s := openTestStore(t)

	// Catalog rows as they existed before 0010: no uid column value.
	ids := map[int64]bool{}
	for i := 0; i < 25; i++ {
		res, err := s.db.Exec(
			`INSERT INTO item_type (uid, kind, name, metal, fine_oz_each) VALUES (NULL,'coin','Roosevelt Dime','silver',0.0723)`)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		ids[id] = true
	}
	// Re-run the backfill exactly as the migration writes it.
	if _, err := s.db.Exec(`UPDATE item_type SET uid =
	  lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
	  substr(lower(hex(randomblob(2))), 2) || '-' ||
	  substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' ||
	  lower(hex(randomblob(6)))
	  WHERE uid IS NULL`); err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.Query(`SELECT id, uid FROM item_type`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var id int64
		var uid string
		if err := rows.Scan(&id, &uid); err != nil {
			t.Fatal(err)
		}
		if !ids[id] {
			t.Errorf("item_type id %d is not one of the ids the rows were inserted with — the backfill renumbered the catalog", id)
		}
		if seen[uid] {
			t.Fatalf("backfill produced a DUPLICATE uid %q — randomblob() was evaluated once, not per row", uid)
		}
		if !looksLikeUUIDv4(uid) {
			t.Fatalf("backfilled uid %q is not a lowercase UUIDv4", uid)
		}
		seen[uid] = true
	}
	if len(seen) != 25 {
		t.Errorf("want 25 distinct backfilled uids, got %d", len(seen))
	}
	everyRowHasAUID(t, s)
}

// looksLikeUUIDv4 checks shape, version and variant, and that it is lowercase —
// the paths built from these land on case-insensitive filesystems (Windows, macOS),
// where an uppercase variant would collide.
func looksLikeUUIDv4(s string) bool {
	if len(s) != 36 || s != strings.ToLower(s) {
		return false
	}
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return false
	}
	for i, want := range []int{8, 4, 4, 4, 12} {
		if len(parts[i]) != want {
			return false
		}
		if strings.Trim(parts[i], "0123456789abcdef") != "" {
			return false
		}
	}
	return strings.HasPrefix(parts[2], "4") && strings.ContainsAny(parts[3][:1], "89ab")
}
