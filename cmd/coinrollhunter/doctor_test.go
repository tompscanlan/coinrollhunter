package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/doctor"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// seedBadDB writes a database carrying rows no validator ever saw. Raw SQL on
// purpose: every Go write path validates, so the only way to produce the rows
// doctor exists to find is to go around it — which is how they reached real users
// (legacy imports, hand edits, rows that predate the validators).
func seedBadDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crh.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO item_type (uid, kind, name, metal, fine_oz_each)
		   VALUES ('11111111-1111-4111-8111-111111111111','bar','Hand-edited bar','silver',1)`,
		`INSERT INTO lots (uid, item_type_id, activity, qty, purity, basis_usd, acquired)
		   VALUES ('22222222-2222-4222-8222-222222222222', 1, 'bullion', 1, 0, -100, '2026-01-02')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("seed failed: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func sum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TestDoctorLeavesTheSourceDatabaseByteIdentical is the promise the command makes in
// its own help text ("it never changes anything... safe with the app open"), and it
// is not free: store.Open APPLIES PENDING MIGRATIONS, so pointing it at the user's
// file would write to the very database it promises only to inspect. The snapshot
// indirection is the only thing standing between this command and that, so pin the
// outcome rather than the mechanism.
func TestDoctorLeavesTheSourceDatabaseByteIdentical(t *testing.T) {
	src := seedBadDB(t)
	before := sum(t, src)

	healthy, err := runDoctor([]string{"--db", src, "--json"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if healthy {
		t.Fatal("premise broken: this fixture holds a negative basis and must not read healthy")
	}
	if after := sum(t, src); after != before {
		t.Errorf("doctor MUTATED the source database:\nbefore %s\nafter  %s", before, after)
	}
	for _, sidecar := range []string{src + "-wal", src + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			t.Errorf("doctor opened the source database directly — it left %s behind", filepath.Base(sidecar))
		}
	}
}

// TestDoctorExitStatusCarriesTheAnswer pins the scripted-use contract: the printed
// report is the result, and the status says whether there was one. A clean database
// must not exit 1, or "run doctor in CI" is useless.
func TestDoctorExitStatusCarriesTheAnswer(t *testing.T) {
	clean := filepath.Join(t.TempDir(), "crh.db")
	s, err := store.Open(clean)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	healthy, err := runDoctor([]string{"--db", clean})
	if err != nil {
		t.Fatalf("doctor on a clean database errored: %v", err)
	}
	if !healthy {
		t.Error("a clean database reported unhealthy — exit 1 on good data makes the check ignorable")
	}

	if _, err := runDoctor([]string{"--db", filepath.Join(t.TempDir(), "nope.db")}); err == nil {
		t.Error("a missing database must be an ERROR, not a clean bill of health")
	}
}

// TestPrintReportLeadsWithWhatWasNotChecked is the honesty property on the surface
// the user actually reads. A scan that could not read a table has NOT cleared it,
// so the unreadable banner has to come first and the reassuring "No problems found"
// line must not appear at all — otherwise a half-failed scan reads as a clean bill
// of health, which is exactly the state a user runs this command in.
func TestPrintReportLeadsWithWhatWasNotChecked(t *testing.T) {
	var buf bytes.Buffer
	printReport(&buf, "/tmp/crh.db", &doctor.Report{
		Findings: []doctor.Finding{{
			Class: doctor.ClassInvalid, Table: "roll_txns", RowID: 7,
			Label: "2026-01-02 bogus", Field: "action", Value: "bogus",
			Detail: "action is not valid — this row is being computed with as-is",
		}},
		Unreadable: []doctor.TableError{{Table: "lots", Error: `converting driver.Value type string ("abc")`}},
		Scanned:    map[string]int{"roll_txns": 1204},
	})
	out := buf.String()

	notChecked := strings.Index(out, "COULD NOT READ")
	if notChecked < 0 {
		t.Fatalf("the unreadable table was not reported at all:\n%s", out)
	}
	if problems := strings.Index(out, "problem(s) across"); problems >= 0 && problems < notChecked {
		t.Error("the finding count printed ABOVE the unreadable banner — it implies those tables came back clean")
	}
	if strings.Contains(out, "No problems found") {
		t.Errorf("a half-failed scan printed a clean bill of health:\n%s", out)
	}
	for _, want := range []string{"roll_txns #7", "action = bogus", "1,204 row(s)", "Nothing was changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q — a finding the user cannot act on is half a finding:\n%s", want, out)
		}
	}
}

// TestPrintReportMakesBlankValuesVisible: "acquired = " reads like a display bug,
// which is how a real finding gets dismissed. A blank or space-padded value is the
// finding, so it has to be shown as one.
func TestPrintReportMakesBlankValuesVisible(t *testing.T) {
	var buf bytes.Buffer
	printReport(&buf, "/tmp/crh.db", &doctor.Report{
		Findings: []doctor.Finding{{
			Class: doctor.ClassInvalid, Table: "spot", RowID: 0,
			Label: "2026-01-01", Field: "source", Value: "  ", Detail: "blank",
		}},
		Scanned: map[string]int{"spot": 2},
	})
	out := buf.String()

	if !strings.Contains(out, `source = "  "`) {
		t.Errorf("a space-padded value was printed raw, so it reads as a rendering bug:\n%s", out)
	}
	// RowID 0 is the id-less singletons, not row zero — printing "spot #0" would send
	// the user looking for a row that does not exist.
	if strings.Contains(out, "spot #0") {
		t.Errorf("an id-less table was printed with a fake row id:\n%s", out)
	}
}
