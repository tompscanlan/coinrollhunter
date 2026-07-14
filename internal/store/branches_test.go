package store

import (
	"database/sql"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// TestMigration0008Backfill pins the ADR-010 hard cutover on *pre-existing* data:
// it stands up the pre-0008 shape (a free-text bank column on roll_txns + trips),
// seeds the exact fork the ADR describes, then applies the real embedded 0008 SQL
// and asserts the backfill deduped correctly and dropped the column. Fresh-DB tests
// can't catch this — they run the backfill over empty tables.
func TestMigration0008Backfill(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Pre-0008 schema (minimal) + the forked history.
	mustExec(t, db, `CREATE TABLE roll_txns (id INTEGER PRIMARY KEY, bank TEXT, face_usd REAL)`)
	mustExec(t, db, `CREATE TABLE trips (id INTEGER PRIMARY KEY, bank TEXT, miles REAL)`)
	mustExec(t, db, `INSERT INTO roll_txns (bank, face_usd) VALUES
		('Chase Main St', 500), ('Chase Main Street', 500), (' Chase Main St ', 250), ('', 100)`)
	mustExec(t, db, `INSERT INTO trips (bank, miles) VALUES ('Chase Main St', 6), ('Riverbend CU', 12)`)

	// Apply the actual migration under test.
	sqlBytes, err := migrationsFS.ReadFile("migrations/0008_branches.sql")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, string(sqlBytes))

	// Three branches: the two Chase spellings are distinct; the whitespace variant
	// folds into "Chase Main St"; the empty string seeds nothing; the trips-only
	// "Riverbend CU" is picked up by the roll_txns ∪ trips union.
	if n := count(t, db, `SELECT count(*) FROM branches`); n != 3 {
		t.Fatalf("branches = %d, want 3", n)
	}
	if n := count(t, db, `SELECT count(*) FROM branch_aliases`); n != 3 {
		t.Fatalf("aliases = %d, want 3", n)
	}

	// The two "Chase Main St" rows (exact + whitespace) share one branch_id; the
	// "Chase Main Street" typo forks its own; the empty row stays NULL.
	exact := branchOf(t, db, 500)  // id 1, "Chase Main St"
	spaced := branchOf(t, db, 250) // id 3, " Chase Main St " → folds into id 1's branch
	typo := branchOfTypo(t, db)
	empty := branchOfEmpty(t, db)
	if !exact.Valid || !spaced.Valid || exact.Int64 != spaced.Int64 {
		t.Errorf("whitespace variant did not fold: exact=%v spaced=%v", exact, spaced)
	}
	if !typo.Valid || typo.Int64 == exact.Int64 {
		t.Errorf("typo fork should be a distinct branch: typo=%v exact=%v", typo, exact)
	}
	if empty.Valid {
		t.Errorf("empty bank should backfill NULL branch_id, got %v", empty)
	}

	// The bank column is gone; branch_id took its place.
	if hasColumn(t, db, "roll_txns", "bank") {
		t.Error("roll_txns.bank should be dropped after hard cutover")
	}
	if !hasColumn(t, db, "roll_txns", "branch_id") {
		t.Error("roll_txns.branch_id should exist")
	}
	if hasColumn(t, db, "trips", "bank") {
		t.Error("trips.bank should be dropped")
	}

	// Every branch got a lowercase v4-shaped uid.
	var uid string
	if err := db.QueryRow(`SELECT uid FROM branches LIMIT 1`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if len(uid) != 36 || uid[14] != '4' {
		t.Errorf("uid %q is not a v4 uuid", uid)
	}
}

// TestBranchResolveMergeRoundtrip drives the runtime cutover through the Store API:
// a typo forks a fresh branch, both spellings carry their own history, and a merge
// folds them into one survivor that keeps all the history and still resolves the
// old spelling.
func TestBranchResolveMergeRoundtrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Two buys, same real branch, two spellings → two branches (find-or-create).
	if _, err := s.InsertRollTxn(model.RollTxn{Date: "2026-01-01", Bank: "Chase Main St", Action: "buy", Denom: "halves", FaceUSD: 500}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRollTxn(model.RollTxn{Date: "2026-01-02", Bank: "Chase Main Street", Action: "buy", Denom: "dimes", FaceUSD: 250}); err != nil {
		t.Fatal(err)
	}
	branches, err := s.ListBranches()
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %d, want 2 (typo forks)", len(branches))
	}

	// Merge the second into the first; the survivor keeps both txns.
	survivor, loser := branches[0].ID, branches[1].ID
	if err := s.MergeBranches(survivor, []int64{loser}); err != nil {
		t.Fatal(err)
	}
	branches, _ = s.ListBranches()
	if len(branches) != 1 {
		t.Fatalf("after merge branches = %d, want 1", len(branches))
	}
	txns, _ := s.ListRollTxns()
	for _, tx := range txns {
		if tx.BranchID != survivor {
			t.Errorf("txn %d branch_id = %d, want survivor %d", tx.ID, tx.BranchID, survivor)
		}
	}
	// The loser's old spelling still resolves — its alias moved to the survivor.
	got, err := resolveBranchID(s.db, "Chase Main Street")
	if err != nil {
		t.Fatal(err)
	}
	if got != survivor {
		t.Errorf("old spelling resolves to %d, want survivor %d", got, survivor)
	}
}

// --- test helpers ------------------------------------------------------------

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %.40q: %v", q, err)
	}
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// branchOf returns the branch_id of the first roll_txns row with the given face_usd.
func branchOf(t *testing.T, db *sql.DB, face float64) sql.NullInt64 {
	t.Helper()
	var id sql.NullInt64
	q := `SELECT branch_id FROM roll_txns WHERE face_usd = ? ORDER BY id LIMIT 1`
	if err := db.QueryRow(q, face).Scan(&id); err != nil {
		t.Fatalf("branchOf(%v): %v", face, err)
	}
	return id
}

func branchOfTypo(t *testing.T, db *sql.DB) sql.NullInt64 {
	t.Helper()
	// The "Chase Main Street" row is the second $500 buy (id 2).
	var id sql.NullInt64
	if err := db.QueryRow(`SELECT branch_id FROM roll_txns WHERE id = 2`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func branchOfEmpty(t *testing.T, db *sql.DB) sql.NullInt64 {
	t.Helper()
	var id sql.NullInt64
	if err := db.QueryRow(`SELECT branch_id FROM roll_txns WHERE face_usd = 100`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func hasColumn(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == col {
			return true
		}
	}
	return false
}
