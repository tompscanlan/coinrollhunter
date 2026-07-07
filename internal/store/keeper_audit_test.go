package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// TestKeeperAuditColumnsFreshDB pins migration 0007 (om-co69, ADR-008) on a fresh
// store: keepers gains the nullable date + roll_txn_id columns, a legacy-shaped
// keeper (no date/box) loads with empty/zero, and an auditable keeper round-trips
// both new fields.
func TestKeeperAuditColumnsFreshDB(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	assertKeeperColumns(t, s)

	// Legacy-shaped keeper (no date/roll_txn_id) — must load with empty/zero.
	if _, err := s.InsertKeeper(model.Keeper{Denom: "halves", Count: 12, FaceUSD: 6.00}); err != nil {
		t.Fatal(err)
	}
	// Auditable keeper carrying the session date + box it was logged against.
	if _, err := s.InsertKeeper(model.Keeper{Denom: "dimes", Count: 90, FaceUSD: 9.00, Date: "2026-07-07", RollTxnID: 42}); err != nil {
		t.Fatal(err)
	}

	ks, err := s.ListKeepers()
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 2 {
		t.Fatalf("ListKeepers returned %d keepers, want 2", len(ks))
	}
	legacy, audited := ks[0], ks[1]
	if legacy.FaceUSD != 6.00 || legacy.Date != "" || legacy.RollTxnID != 0 {
		t.Errorf("legacy keeper = %+v, want face 6.00, empty date, zero roll_txn_id", legacy)
	}
	if audited.FaceUSD != 9.00 || audited.Date != "2026-07-07" || audited.RollTxnID != 42 {
		t.Errorf("audited keeper = %+v, want face 9.00, date 2026-07-07, roll_txn_id 42", audited)
	}

	// Update round-trips the new columns too.
	if err := s.UpdateKeeper(audited.ID, model.Keeper{Denom: "dimes", Count: 100, FaceUSD: 10.00, Date: "2026-07-08", RollTxnID: 43}); err != nil {
		t.Fatal(err)
	}
	ks, _ = s.ListKeepers()
	if got := ks[1]; got.FaceUSD != 10.00 || got.Date != "2026-07-08" || got.RollTxnID != 43 {
		t.Errorf("after update, keeper = %+v, want face 10.00, date 2026-07-08, roll_txn_id 43", got)
	}
}

// TestKeeper0007OverExisting0006 proves 0007 is additive/non-destructive: it
// applies on top of a DB already at version 6 that has a pre-existing keeper row
// in the old 4-column shape. The row survives with NULL (empty/zero) new columns
// and unchanged face; the new insert path works on the upgraded DB.
func TestKeeper0007OverExisting0006(t *testing.T) {
	path := filepath.Join(t.TempDir(), "at0006.db")

	// Bring a raw DB up to EXACTLY version 6 — simulating a DB created before 0007
	// existed. Apply migrations 0001..0006 in order, then stamp user_version=6.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 6 {
			continue
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
	}
	if _, err := raw.Exec("PRAGMA user_version = 6"); err != nil {
		t.Fatal(err)
	}
	// A pre-existing keeper row in the OLD (pre-0007) 4-column shape.
	if _, err := raw.Exec(`INSERT INTO keepers (denom, count, face_usd) VALUES ('halves', 62, 31.00)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen via Open → migrate() applies 0007 on top of the existing 0006 DB.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("reopen/migrate to 0007: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if v, _ := s.Version(); v < 7 {
		t.Fatalf("schema version = %d, want >= 7 after 0007", v)
	}
	assertKeeperColumns(t, s)

	// The pre-existing 0006 row survived: face unchanged, new columns NULL.
	ks, err := s.ListKeepers()
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 1 {
		t.Fatalf("want 1 surviving keeper, got %d", len(ks))
	}
	if ks[0].FaceUSD != 31.00 || ks[0].Date != "" || ks[0].RollTxnID != 0 {
		t.Errorf("surviving keeper = %+v, want face 31.00, empty date, zero roll_txn_id", ks[0])
	}

	// The new insert path (with the new columns) works on the upgraded DB.
	if _, err := s.InsertKeeper(model.Keeper{Denom: "dimes", Count: 90, FaceUSD: 9.00, Date: "2026-07-07", RollTxnID: 7}); err != nil {
		t.Fatalf("insert on upgraded DB: %v", err)
	}
}

// assertKeeperColumns fails unless PRAGMA table_info(keepers) lists both new columns.
func assertKeeperColumns(t *testing.T, s *Store) {
	t.Helper()
	rows, err := s.DB().Query(`PRAGMA table_info(keepers)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"date", "roll_txn_id"} {
		if !cols[want] {
			t.Errorf("keepers table missing column %q (PRAGMA table_info)", want)
		}
	}
}
