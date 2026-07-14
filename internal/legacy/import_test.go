package legacy

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// TestImportSampleEndToEnd proves the full Phase 0–1 path lines up: prototype
// JSON -> migrate -> SQLite (catalog/specimen) -> resolve -> calc reproduces the
// same numbers the calc package verifies on its in-memory fixture. If migration
// or resolution drops or mangles data, the headline numbers move.
func TestImportSampleEndToEnd(t *testing.T) {
	root := filepath.Join("..", "..", "sample-data")
	holdings, err := os.ReadFile(filepath.Join(root, "pm_holdings.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	crh, err := os.ReadFile(filepath.Join(root, "crh_ledger.sample.json"))
	if err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := Import(s, holdings, crh); err != nil {
		t.Fatalf("import: %v", err)
	}

	d, err := s.ResolveDataset()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	r := calc.Compute(d)

	approx := func(label string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-6 {
			t.Errorf("%s = %.6f, want %.6f", label, got, want)
		}
	}

	// Same headline values the calc worked-example test derives from the fixture.
	approx("find_oz", r.FindOz, 1.36404)
	approx("find_cost", r.FindCost, 4.25)
	approx("find_realizable", r.FindRealizable, 66.55896)
	approx("op_cost", r.OpCost, 12.20)
	approx("crh_net_real", r.CRHNetReal, 50.10896)
	approx("to_redeposit", r.ToRedeposit, -11.00)
	approx("bullion_unreal", r.BullionUnreal, -35.96)
	approx("total_boxes", r.TotalBoxes, 2.0)
	if r.Verdict() != "PROFITABLE (cash basis)" {
		t.Errorf("verdict = %q", r.Verdict())
	}

	// Catalog/specimen split: 4 prototype lots -> 4 holdings, but the two 90%
	// silver types differ by product/fine-oz so we expect 4 distinct item_types here.
	var nTypes, nLots int
	if err := s.DB().QueryRow(`SELECT count(*) FROM item_type`).Scan(&nTypes); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM lots`).Scan(&nLots); err != nil {
		t.Fatal(err)
	}
	if nLots != 4 {
		t.Errorf("lots = %d, want 4", nLots)
	}
	if nTypes != 4 {
		t.Errorf("item_types = %d, want 4 (one per distinct product/metal/fineness/fine-oz)", nTypes)
	}
}

// TestImportIsIdempotentSchema confirms a second Open on the same store is a
// no-op migration (version stays at the latest, no error).
func TestImportReopenStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crh.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	v1, _ := s.Version()
	s.Close()

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	v2, _ := s2.Version()
	if v1 != v2 || v1 < 1 {
		t.Errorf("version drift: first=%d second=%d (want equal, >=1)", v1, v2)
	}
}

// --- atomicity (om-u3el) -----------------------------------------------------
//
// The importer is the new-user on-ramp (the spreadsheet migration path). Before
// om-u3el it wrote with no transaction: the first rejected row aborted the run
// mid-stream, the rows ahead of it were already committed, and the user's fixed
// re-run then DUPLICATED every one of them. These tests pin the fix — a failed
// import must leave the database byte-for-byte unchanged, whatever failed and
// wherever it failed.

// importedTables is every table the importer can write, including the two the
// hidden 7th writer (store.resolveBranchID -> InsertBranch) forks from a typed
// bank name, and the two upserts (spot, settings) that used to survive a failure
// and silently rewrite the user's settings from a file that was then rejected.
var importedTables = []string{
	"item_type", "lots", "roll_txns", "trips", "supplies", "keepers",
	"branches", "branch_aliases", "spot", "settings",
}

func countRows(t *testing.T, s *store.Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func snapshot(t *testing.T, s *store.Store) map[string]int {
	t.Helper()
	m := map[string]int{}
	for _, tb := range importedTables {
		m[tb] = countRows(t, s, tb)
	}
	return m
}

func assertUnchanged(t *testing.T, s *store.Store, before map[string]int) {
	t.Helper()
	for _, tb := range importedTables {
		if got := countRows(t, s, tb); got != before[tb] {
			t.Errorf("%s: %d rows after a FAILED import, want %d — the import was NOT atomic",
				tb, got, before[tb])
		}
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const goodHoldings = `{
  "spot_reference": {"gold_usd_per_ozt": 4000, "silver_usd_per_ozt": 60, "as_of": "2026-01-01"},
  "lots": [
    {"id":"B1","acquired":"2025-01-15","source":"Dealer","product":"1 oz Gold Eagle","category":"bullion",
     "metal":"gold","fineness":"22k","qty":1,"fine_oz_each":1.0,"basis_usd":3950,"face_value_usd":50},
    {"id":"FIND1","acquired":"2025-03-10","source":"Test Bank (CRH)","product":"40% halves","category":"junk",
     "metal":"silver","fineness":"40%","qty":8,"fine_oz_each":0.1479,"basis_usd":4,"face_value_usd":4}
  ]
}`

// goodCRH names a bank on both a roll txn and a trip, so a run that gets that far
// forks a branch + alias through resolveBranchID — the orphan rows AC3 is about.
const goodCRH = `{
  "settings": {"value_time": false, "hourly_rate_usd": 25, "irs_mileage_rate_usd_per_mile": 0.7,
    "silver_buyback_factor_90pct": 0.9, "silver_buyback_factor_40pct": 0.8, "box_face_usd": {"halves": 500}},
  "roll_transactions": [
    {"date":"2025-03-08","bank":"Test Bank","action":"buy","cash_usd":500,"denom":"halves","boxes":1,"notes":""},
    {"date":"2025-03-09","bank":"Test Bank","action":"return","cash_usd":489,"denom":"halves","notes":""}
  ],
  "trips": [{"date":"2025-03-08","bank":"Test Bank","miles":6,"hours":0.5}],
  "supplies": [{"date":"2025-03-01","item":"coin tubes","cost_usd":8}],
  "keepers_clad": {"halves_count":12,"halves_face_usd":6,"quarters_count":20,"quarters_face_usd":5}
}`

// badSupplyCRH is goodCRH with a SECOND supply carrying a negative cost. It is the
// Nth-row-is-invalid case, and it is deliberately late in the write order: under the
// old sequential importer everything before it — settings, spot, item_types, lots,
// roll_txns, trips AND the forked branch — was already committed when it blew up.
const badSupplyCRH = `{
  "settings": {"value_time": false, "hourly_rate_usd": 25, "irs_mileage_rate_usd_per_mile": 0.7,
    "silver_buyback_factor_90pct": 0.9, "silver_buyback_factor_40pct": 0.8, "box_face_usd": {"halves": 500}},
  "roll_transactions": [
    {"date":"2025-03-08","bank":"Test Bank","action":"buy","cash_usd":500,"denom":"halves","boxes":1,"notes":""},
    {"date":"2025-03-09","bank":"Test Bank","action":"return","cash_usd":489,"denom":"halves","notes":""}
  ],
  "trips": [{"date":"2025-03-08","bank":"Test Bank","miles":6,"hours":0.5}],
  "supplies": [
    {"date":"2025-03-01","item":"coin tubes","cost_usd":8},
    {"date":"2025-03-02","item":"flips","cost_usd":-3}
  ],
  "keepers_clad": {"halves_count":12,"halves_face_usd":6,"quarters_count":20,"quarters_face_usd":5}
}`

// TestImportBadRowWritesNothing — AC: an import whose Nth row is invalid writes ZERO
// rows, in EVERY table the importer touches, including no orphan bank branch.
func TestImportBadRowWritesNothing(t *testing.T) {
	s := openStore(t)
	before := snapshot(t, s)

	err := Import(s, []byte(goodHoldings), []byte(badSupplyCRH))
	if err == nil {
		t.Fatal("import of a file with a negative supply cost: want an error, got nil")
	}
	if !errors.Is(err, model.ErrInvalid) {
		t.Errorf("error %v does not unwrap to model.ErrInvalid", err)
	}
	assertUnchanged(t, s, before)
}

// TestImportRollsBackMidImportDBError — AC1 covers ALL failure modes, not just the
// validation-shaped ones. Every row here is VALID, so the pre-validate pass passes
// and the writes actually start; the keepers table is then missing, so the last
// insert fails at the DB. Only a real transaction can undo the six tables already
// written by then. (Pre-validation alone would half-write here — this is the test
// that proves the transaction is the load-bearing part.)
func TestImportRollsBackMidImportDBError(t *testing.T) {
	s := openStore(t)
	if _, err := s.DB().Exec(`DROP TABLE keepers`); err != nil {
		t.Fatal(err)
	}
	tables := []string{"item_type", "lots", "roll_txns", "trips", "supplies", "branches", "branch_aliases", "spot", "settings"}
	before := map[string]int{}
	for _, tb := range tables {
		before[tb] = countRows(t, s, tb)
	}

	err := Import(s, []byte(goodHoldings), []byte(goodCRH))
	if err == nil {
		t.Fatal("import into a database missing the keepers table: want an error, got nil")
	}
	if errors.Is(err, model.ErrInvalid) {
		t.Errorf("a DB error was reported as a validation error: %v", err)
	}
	for _, tb := range tables {
		if got := countRows(t, s, tb); got != before[tb] {
			t.Errorf("%s: %d rows after a FAILED import, want %d — the import was NOT atomic",
				tb, got, before[tb])
		}
	}
}

// TestImportRetryAfterFailureDoesNotDuplicate — AC: the user fixes the file and
// re-runs. The failed attempt wrote nothing, so the corrected run must land exactly
// what a clean single import lands.
func TestImportRetryAfterFailureDoesNotDuplicate(t *testing.T) {
	s := openStore(t)
	if err := Import(s, []byte(goodHoldings), []byte(badSupplyCRH)); err == nil {
		t.Fatal("bad import: want an error, got nil")
	}
	// The user fixes the negative cost and runs it again, into the SAME database.
	if err := Import(s, []byte(goodHoldings), []byte(goodCRH)); err != nil {
		t.Fatalf("corrected import: %v", err)
	}
	after := snapshot(t, s)

	clean := openStore(t)
	if err := Import(clean, []byte(goodHoldings), []byte(goodCRH)); err != nil {
		t.Fatalf("clean import: %v", err)
	}
	want := snapshot(t, clean)

	for _, tb := range importedTables {
		if after[tb] != want[tb] {
			t.Errorf("%s: %d rows after failed-then-corrected import, want %d (a clean single import) — rows were DUPLICATED",
				tb, after[tb], want[tb])
		}
	}
}

// TestImportReportsEveryBadRow — the pre-validate pass: a file with invalid rows in
// four different tables must name ALL of them in one report, not just the first, and
// each must identify its row and its field. Failing one row at a time is the on-ramp
// burner this bead is about: fix, re-run, hit the next one.
func TestImportReportsEveryBadRow(t *testing.T) {
	const badHoldings = `{
	  "spot_reference": {"gold_usd_per_ozt": 4000, "silver_usd_per_ozt": 60, "as_of": "2026-01-01"},
	  "lots": [
	    {"id":"B1","acquired":"2025-01-15","source":"Dealer","product":"Eagle","category":"bullion",
	     "metal":"gold","fineness":"22k","qty":1,"fine_oz_each":1.0,"basis_usd":3950,"face_value_usd":50},
	    {"id":"B2","acquired":"2025-02-01","source":"Dealer","product":"90% dimes","category":"junk",
	     "metal":"Silver","fineness":"90%","qty":1,"fine_oz_each":7.234,"basis_usd":520,"face_value_usd":10},
	    {"id":"B3","acquired":"","source":"Dealer","product":"Maple","category":"bullion",
	     "metal":"silver","fineness":".9999","qty":5,"fine_oz_each":1.0,"basis_usd":150,"face_value_usd":0}
	  ]
	}`
	const badCRH = `{
	  "settings": {"hourly_rate_usd": 25},
	  "roll_transactions": [
	    {"date":"2025-03-08","bank":"Test Bank","action":"purchase","cash_usd":500,"denom":"halves","boxes":1}
	  ],
	  "trips": [{"date":"March 8","bank":"Test Bank","miles":6,"hours":0.5}],
	  "supplies": [{"date":"2025-03-01","item":"coin tubes","cost_usd":-8}]
	}`

	s := openStore(t)
	before := snapshot(t, s)

	err := Import(s, []byte(badHoldings), []byte(badCRH))
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	assertUnchanged(t, s, before)

	var ie *ImportErrors
	if !errors.As(err, &ie) {
		t.Fatalf("error is %T, want *legacy.ImportErrors: %v", err, err)
	}
	// 5 bad rows: the capitalized metal (item_type), the blank acquired (lot), the
	// bad action, the unparseable trip date, the negative supply cost.
	if len(ie.Rows) != 5 {
		t.Errorf("reported %d bad rows, want 5:\n%v", len(ie.Rows), err)
	}
	msg := err.Error()
	for _, want := range []string{
		`lots[1]`, `metal`, // capitalized "Silver"
		`lots[2]`, `acquired`, // blank required date
		`roll_transactions[0]`, `action`, // "purchase"
		`trips[0]`, `date`, // "March 8"
		`supplies[0]`, `cost_usd`, // negative
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("report does not mention %q:\n%s", want, msg)
		}
	}
	// Every row error still unwraps to the sentinel the API keys a 400 off.
	if !errors.Is(err, model.ErrInvalid) {
		t.Errorf("report does not unwrap to model.ErrInvalid")
	}
}
