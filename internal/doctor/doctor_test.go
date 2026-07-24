package doctor_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/doctor"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// open gives each test a fresh migrated database. Bad rows are seeded with raw SQL
// on purpose: every Go write path validates, so the ONLY way to produce
// the rows doctor exists to find is to go around it — which is exactly how they got
// into real users' databases (legacy imports, hand edits, rows that predate the
// validators).
func open(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func exec(t *testing.T, s *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := s.DB().Exec(q, args...); err != nil {
		t.Fatalf("seed failed (a DB constraint was added — this codebase forbids one): %v", err)
	}
}

func scan(t *testing.T, s *store.Store) *doctor.Report {
	t.Helper()
	r, err := doctor.Scan(context.Background(), s)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return r
}

// findings filters to one class + table so a test asserts about what it seeded
// without being coupled to everything else the scan happens to notice.
func findings(r *doctor.Report, class doctor.Class, table string) []doctor.Finding {
	var out []doctor.Finding
	for _, f := range r.Findings {
		if f.Class == class && f.Table == table {
			out = append(out, f)
		}
	}
	return out
}

// TestCleanDatabaseIsSilent is the false-positive gate, and it is the most important
// test in this file. A scan that cries wolf on correct data is worse than no scan:
// the user learns to ignore it, and the one real finding scrolls past unread. Every
// check added here must keep this test passing on ordinary, correct entries —
// including the legitimately-blank ones (a keeper with no box, a lot with no
// fineness, a trip with no bank) that make up most of a real ledger.
func TestCleanDatabaseIsSilent(t *testing.T) {
	s := open(t)

	typeID, err := s.InsertItemType(model.ItemType{Kind: "junk", Name: "90% Roosevelt dime", Metal: "silver", FineOzEach: 0.0723, Fineness: "90%"})
	if err != nil {
		t.Fatal(err)
	}
	boxID, err := s.InsertRollTxn(model.RollTxn{Date: "2026-03-01", Bank: "Chase Main", Action: "buy", Denom: "dimes", Unit: "box", Amount: 1, FaceUSD: 250})
	if err != nil {
		t.Fatal(err)
	}
	// A find pulled from that box, on the same day.
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: boxID, Activity: "crh", Qty: 1, BasisUSD: 0.10, Acquired: "2026-03-01"}); err != nil {
		t.Fatal(err)
	}
	// A find dated AFTER its box — normal (searched the box over a few evenings).
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: boxID, Activity: "crh", Qty: 1, BasisUSD: 0.10, Acquired: "2026-03-04"}); err != nil {
		t.Fatal(err)
	}
	// Legacy-shaped rows: a keeper with NO box and NO date is legal and common (the
	// spreadsheet on-ramp writes exactly this), and must never be reported.
	if _, err := s.InsertKeeper(model.Keeper{Denom: "dimes", Count: 40, FaceUSD: 4}); err != nil {
		t.Fatal(err)
	}
	// A keeper attributed to the box, same denom — the correct entry.
	if _, err := s.InsertKeeper(model.Keeper{Denom: "dimes", Count: 100, FaceUSD: 10, Date: "2026-03-01", RollTxnID: boxID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTrip(model.Trip{Date: "2026-03-01", Bank: "Chase Main", Miles: 6, Hours: 0.5}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertSupply(model.Supply{Date: "2026-03-01", Item: "coin tubes", CostUSD: 8}); err != nil {
		t.Fatal(err)
	}

	r := scan(t, s)
	if !r.Healthy() {
		t.Fatalf("a correct database produced findings — this is the noise failure mode:\nfindings=%+v\nunreadable=%+v", r.Findings, r.Unreadable)
	}
	if r.Scanned["lots"] != 2 || r.Scanned["keepers"] != 2 {
		t.Errorf("scanned counts look wrong: %+v — a report that found nothing must still say how much it looked at", r.Scanned)
	}
}

// TestInvalidRowsAreReported covers rows that no validator ever saw, which
// calc.Compute sums anyway. The negative basis is the headline case — it understates
// the money and says nothing.
func TestInvalidRowsAreReported(t *testing.T) {
	s := open(t)
	exec(t, s, `INSERT INTO item_type (uid, kind, name, metal, fine_oz_each)
	            VALUES ('11111111-1111-4111-8111-111111111111','junk','Hand-edited bar','silver',1)`)
	exec(t, s, `INSERT INTO lots (uid, item_type_id, activity, qty, purity, basis_usd, acquired)
	            VALUES ('22222222-2222-4222-8222-222222222222', 1, 'bullion', 1, 0, -100, '2026-01-02')`)
	exec(t, s, `INSERT INTO roll_txns (uid, date, action, face_usd)
	            VALUES ('33333333-3333-4333-8333-333333333333','2026-01-02','bogus',500)`)

	r := scan(t, s)

	lots := findings(r, doctor.ClassInvalid, "lots")
	if len(lots) != 1 {
		t.Fatalf("got %d invalid lots findings, want 1; all = %+v", len(lots), r.Findings)
	}
	if lots[0].Field != "basis_usd" {
		t.Errorf("field = %q, want basis_usd", lots[0].Field)
	}
	// model.FieldError names the column but carries no value ("basis_usd must not be
	// negative"), which leaves the user hunting for a row they were just handed. The
	// value is read back off the struct so the finding is self-contained.
	if lots[0].Value != "-100" {
		t.Errorf("value = %q, want -100 — a finding that names the column but not what is in it is half a finding", lots[0].Value)
	}
	if !strings.Contains(lots[0].Label, "Hand-edited bar") {
		t.Errorf("label = %q — a finding must name the coin, not just a rowid, or the user cannot find it", lots[0].Label)
	}
	if !strings.Contains(lots[0].Detail, "computed with") {
		t.Errorf("detail = %q — it must say what the app is currently DOING about the bad row, not only that it is bad", lots[0].Detail)
	}

	// action outside buy/return is the worst silent corruption in the schema: the
	// txn vanishes from the float AND from face searched.
	if got := findings(r, doctor.ClassInvalid, "roll_txns"); len(got) != 1 || got[0].Field != "action" {
		t.Errorf("roll_txns findings = %+v, want one on field 'action'", got)
	}
	if r.Counts[string(doctor.ClassInvalid)] != 2 {
		t.Errorf("invalid count = %d, want 2; counts = %+v", r.Counts[string(doctor.ClassInvalid)], r.Counts)
	}
	if r.Healthy() {
		t.Error("Healthy() must be false when there are findings")
	}
}

// TestNonFiniteIsReported covers the value no validator catches: model's nonNeg
// tests `v < 0`, which is false for +Inf. One such row makes every total it reaches
// infinite AND makes the summary response fail to encode, so the user sees a blank
// dashboard and no error. This scan is the only surface that can explain it.
func TestNonFiniteIsReported(t *testing.T) {
	s := open(t)
	exec(t, s, `INSERT INTO item_type (uid, kind, name, metal, fine_oz_each)
	            VALUES ('11111111-1111-4111-8111-111111111111','bar','Poisoned bar','silver',1)`)
	exec(t, s, `INSERT INTO lots (uid, item_type_id, activity, qty, purity, basis_usd, acquired)
	            VALUES ('22222222-2222-4222-8222-222222222222', 1, 'bullion', 1, 0, 9e999, '2026-01-02')`)

	got := findings(scan(t, s), doctor.ClassInvalid, "lots")
	if len(got) != 1 || got[0].Field != "basis_usd" {
		t.Fatalf("findings = %+v, want one on basis_usd", got)
	}
	if got[0].Value != "+Inf" {
		t.Errorf("value = %q, want +Inf verbatim", got[0].Value)
	}
	if !strings.Contains(got[0].Detail, "blank dashboard") {
		t.Errorf("detail = %q — the consequence the user actually experiences is the blank dashboard; say it", got[0].Detail)
	}
}

// TestOrphanLinksAreReported is the reason this needs its own class: once links ride
// the stable uid, a deleted box leaves the child's uid dangling, every read path
// resolves it to a blank integer, and blank is INDISTINGUISHABLE from never-linked.
// The row looks fine in the grid. Only the stored uid column knows, and only this
// scan reads it.
func TestOrphanLinksAreReported(t *testing.T) {
	s := open(t)
	typeID, err := s.InsertItemType(model.ItemType{Kind: "junk", Name: "Mercury dime", Metal: "silver", FineOzEach: 0.0723})
	if err != nil {
		t.Fatal(err)
	}
	boxID, err := s.InsertRollTxn(model.RollTxn{Date: "2026-02-01", Bank: "Fifth Third", Action: "buy", Denom: "dimes", FaceUSD: 250})
	if err != nil {
		t.Fatal(err)
	}
	lotID, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: boxID, Activity: "crh", Qty: 1, BasisUSD: 0.1, Acquired: "2026-02-01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertKeeper(model.Keeper{Denom: "dimes", Count: 50, FaceUSD: 5, Date: "2026-02-01", RollTxnID: boxID}); err != nil {
		t.Fatal(err)
	}
	tripID, err := s.InsertTrip(model.Trip{Date: "2026-02-01", Bank: "Fifth Third", Miles: 4})
	if err != nil {
		t.Fatal(err)
	}

	// The everyday correction workflow: delete the box you just entered wrong. The
	// find and the keeper now dangle. Then delete the branch, orphaning the trip.
	if err := s.DeleteRollTxn(boxID); err != nil {
		t.Fatal(err)
	}
	branches, err := s.ListBranches()
	if err != nil || len(branches) == 0 {
		t.Fatalf("expected a branch to have been created; %v %+v", err, branches)
	}
	if err := s.DeleteBranch(branches[0].ID); err != nil {
		t.Fatal(err)
	}

	// Confirm the premise first: the normal read path shows NO box. That blankness is
	// the bug this class exists to make visible.
	holdings, err := s.ListHoldings()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range holdings {
		if h.ID == lotID && h.RollTxnID != 0 {
			t.Fatalf("premise broken: orphaned find still reads back box %d", h.RollTxnID)
		}
	}

	r := scan(t, s)
	for _, tc := range []struct{ table, field string }{
		{"lots", "roll_txn_uid"},
		{"keepers", "roll_txn_uid"},
		{"trips", "branch_uid"},
	} {
		got := findings(r, doctor.ClassOrphan, tc.table)
		if len(got) != 1 {
			t.Errorf("%s: got %d orphan findings, want 1; all = %+v", tc.table, len(got), r.Findings)
			continue
		}
		if got[0].Field != tc.field {
			t.Errorf("%s: field = %q, want %q", tc.table, got[0].Field, tc.field)
		}
		if got[0].Value == "" {
			t.Errorf("%s: the dangling uid is the forensic trace — it must be reported verbatim", tc.table)
		}
	}
	if got := findings(r, doctor.ClassOrphan, "trips"); len(got) == 1 && got[0].RowID != tripID {
		t.Errorf("trip finding names row %d, want %d", got[0].RowID, tripID)
	}
}

// TestSuspectLinksAreReported covers the re-adoptions that are frozen wrong and
// cannot be honestly repaired, but CAN be pointed at. Both checks are conservative
// by construction — see the silent-on-clean-data assertions below, which are the
// half that keeps this class from becoming noise.
func TestSuspectLinksAreReported(t *testing.T) {
	s := open(t)
	typeID, err := s.InsertItemType(model.ItemType{Kind: "junk", Name: "Washington quarter", Metal: "silver", FineOzEach: 0.1808})
	if err != nil {
		t.Fatal(err)
	}
	boxID, err := s.InsertRollTxn(model.RollTxn{Date: "2026-04-10", Bank: "Chase", Action: "buy", Denom: "quarters", FaceUSD: 500})
	if err != nil {
		t.Fatal(err)
	}
	// A find dated BEFORE the box was bought — impossible, so either the date is
	// wrong or it is attached to the wrong box.
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: boxID, Activity: "crh", Qty: 1, BasisUSD: 0.25, Acquired: "2026-04-02"}); err != nil {
		t.Fatal(err)
	}
	// A keeper of DIMES attributed to a box of quarters.
	if _, err := s.InsertKeeper(model.Keeper{Denom: "dimes", Count: 40, FaceUSD: 4, Date: "2026-04-10", RollTxnID: boxID}); err != nil {
		t.Fatal(err)
	}
	// Case-only difference is NOT a conflict.
	if _, err := s.InsertKeeper(model.Keeper{Denom: "Quarters", Count: 40, FaceUSD: 10, Date: "2026-04-10", RollTxnID: boxID}); err != nil {
		t.Fatal(err)
	}
	// A BULLION lot dated before the box is not a find and is not suspect — bullion
	// is bought, not pulled out of a box.
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, RollTxnID: boxID, Activity: "bullion", Qty: 1, BasisUSD: 30, Acquired: "2026-04-02"}); err != nil {
		t.Fatal(err)
	}

	r := scan(t, s)

	lots := findings(r, doctor.ClassSuspect, "lots")
	if len(lots) != 1 {
		t.Fatalf("got %d suspect lots findings, want 1 (the crh find only, not the bullion lot); all = %+v", len(lots), r.Findings)
	}
	if lots[0].Field != "acquired" || lots[0].Value != "2026-04-02" {
		t.Errorf("finding = %+v, want field acquired / value 2026-04-02", lots[0])
	}
	if !strings.Contains(lots[0].Detail, "2026-04-10") {
		t.Errorf("detail = %q — it must quote the box date, or the user cannot tell which of the two is wrong", lots[0].Detail)
	}

	keepers := findings(r, doctor.ClassSuspect, "keepers")
	if len(keepers) != 1 {
		t.Fatalf("got %d suspect keepers findings, want 1 (dimes-in-a-quarters-box; 'Quarters' is a case fold, not a conflict); all = %+v", len(keepers), r.Findings)
	}
	if keepers[0].Value != "dimes" {
		t.Errorf("value = %q, want dimes", keepers[0].Value)
	}
}

// TestSuspectChecksNeedBothSides pins the conservatism directly. A blank denom or a
// non-ISO date means "not recorded", not "wrong" — the legacy import writes exactly
// those, and accusing them would fire on the biggest population in the ledger.
func TestSuspectChecksNeedBothSides(t *testing.T) {
	s := open(t)
	exec(t, s, `INSERT INTO item_type (uid, kind, name, metal, fine_oz_each)
	            VALUES ('11111111-1111-4111-8111-111111111111','junk','Legacy dime','silver',0.0723)`)
	exec(t, s, `INSERT INTO roll_txns (uid, date, action, denom, face_usd)
	            VALUES ('33333333-3333-4333-8333-333333333333','', 'buy', '', 250)`)
	// Blank acquired + blank box denom on both sides of both checks.
	exec(t, s, `INSERT INTO lots (uid, item_type_id, roll_txn_uid, activity, qty, purity, basis_usd, acquired)
	            VALUES ('22222222-2222-4222-8222-222222222222', 1, '33333333-3333-4333-8333-333333333333', 'crh', 1, 0, 0.1, '')`)
	exec(t, s, `INSERT INTO keepers (denom, count, face_usd, roll_txn_uid)
	            VALUES ('', 40, 4, '33333333-3333-4333-8333-333333333333')`)

	r := scan(t, s)
	if got := findings(r, doctor.ClassSuspect, "lots"); len(got) != 0 {
		t.Errorf("a blank acquired date was accused: %+v", got)
	}
	if got := findings(r, doctor.ClassSuspect, "keepers"); len(got) != 0 {
		t.Errorf("a blank denom was accused: %+v", got)
	}
}

// TestScanIsReadOnly is the guarantee the whole design rests on: the scan repairs
// nothing, because heuristic repair false-positives on GOOD data — and a false
// positive here is silent, unrecoverable money loss with no undo. Every row is
// compared byte-for-byte across a scan.
func TestScanIsReadOnly(t *testing.T) {
	s := open(t)
	exec(t, s, `INSERT INTO item_type (uid, kind, name, metal, fine_oz_each)
	            VALUES ('11111111-1111-4111-8111-111111111111','junk','Bad','bogus',-1)`)
	exec(t, s, `INSERT INTO lots (uid, item_type_id, roll_txn_uid, activity, qty, purity, basis_usd, acquired)
	            VALUES ('22222222-2222-4222-8222-222222222222', 1, 'gone-forever', 'bogus', -5, 9.9, -1, 'nope')`)
	exec(t, s, `INSERT INTO keepers (denom, count, face_usd, roll_txn_uid)
	            VALUES ('dimes', 40, 4, 'gone-forever')`)

	before := dump(t, s)
	r := scan(t, s)
	if len(r.Findings) == 0 {
		t.Fatal("premise broken: this fixture is meant to be full of findings")
	}
	if after := dump(t, s); after != before {
		t.Errorf("doctor MUTATED the database — it is read-only by design.\nbefore: %s\nafter:  %s", before, after)
	}
}

// dump serializes every row doctor touches, so a mutation anywhere shows up as a
// diff rather than needing a per-column assertion.
func dump(t *testing.T, s *store.Store) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id, uid, item_type_id, roll_txn_uid, activity, qty, purity, basis_usd, acquired FROM lots ORDER BY id`,
		`SELECT id, uid, kind, name, metal, fine_oz_each FROM item_type ORDER BY id`,
		`SELECT id, denom, count, face_usd, roll_txn_uid FROM keepers ORDER BY id`,
		`SELECT id, uid, date, branch_uid, action, denom, face_usd FROM roll_txns ORDER BY id`,
		`SELECT id, date, branch_uid, miles, hours FROM trips ORDER BY id`,
		`SELECT id, uid, name FROM branches ORDER BY id`,
	} {
		rows, err := s.DB().Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			cells := make([]any, len(cols))
			for i := range cells {
				cells[i] = new(any)
			}
			if err := rows.Scan(cells...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			enc, _ := json.Marshal(cells)
			b.Write(enc)
			b.WriteByte('\n')
		}
		rows.Close()
	}
	return b.String()
}

// TestUnreadableTableDoesNotAbortTheScan is the honesty property. A user runs doctor
// precisely when things are broken, so one table that cannot be read must not cost
// them the report on the other nine — and it must be reported rather than swallowed,
// or "0 findings" from a half-failed scan reads as a clean bill of health.
func TestUnreadableTableDoesNotAbortTheScan(t *testing.T) {
	s := open(t)
	exec(t, s, `INSERT INTO item_type (uid, kind, name, metal, fine_oz_each)
	            VALUES ('11111111-1111-4111-8111-111111111111','junk','Fine type','silver',0.0723)`)
	// A string in a money column. SQLite has affinity, not types, so this WRITE
	// SUCCEEDS (NOT NULL is satisfied) — and the store then scans basis_usd into a
	// plain float64, which fails the entire lots query. One cell, and every list,
	// grid and total over lots is dead, with nothing saying which cell.
	exec(t, s, `INSERT INTO lots (uid, item_type_id, activity, qty, purity, basis_usd, acquired)
	            VALUES ('22222222-2222-4222-8222-222222222222', 1, 'bullion', 1, 0, 'abc', '2026-01-02')`)
	// A perfectly good row in a DIFFERENT table, which the scan must still reach.
	exec(t, s, `INSERT INTO roll_txns (uid, date, action, face_usd)
	            VALUES ('33333333-3333-4333-8333-333333333333','2026-01-02','bogus',500)`)

	r := scan(t, s)

	var sawLots bool
	for _, u := range r.Unreadable {
		if u.Table == "lots" {
			sawLots = true
		}
	}
	if !sawLots {
		t.Fatalf("the unreadable lots table was not reported: %+v", r.Unreadable)
	}
	if r.Healthy() {
		t.Error("Healthy() must be false when a table could not be read — otherwise a half-failed scan reads as a clean bill of health")
	}
	if got := findings(r, doctor.ClassInvalid, "roll_txns"); len(got) != 1 {
		t.Errorf("the scan stopped at the bad table: roll_txns findings = %+v, want 1", got)
	}
}

// TestReportSerializesEmptyAsLists keeps the UI simple: a clean scan must produce
// [] rather than null, or every consumer needs a null guard before it can count.
func TestReportSerializesEmptyAsLists(t *testing.T) {
	enc, err := json.Marshal(scan(t, open(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"findings":[]`, `"unreadable":[]`} {
		if !strings.Contains(string(enc), want) {
			t.Errorf("report JSON = %s, want it to contain %s", enc, want)
		}
	}
}
