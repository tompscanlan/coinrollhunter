package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/demo"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// The bundle is a published FORMAT: once a user has built a spreadsheet on it,
// changing it is expensive. These tests are the guard on that. Two of them —
// TestBundleCoversEveryTable and TestBundleCoversEveryColumn — are deliberately
// written against the LIVE SCHEMA rather than against the exporter, so a future
// migration that adds a table or a column BREAKS them. That is the point: whoever
// adds it must then decide, consciously, how it leaves the app.

// newStore opens a migrated store in a temp dir (a real file, not ":memory:", so
// the photo tree beside it has somewhere to live).
func newStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "crh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seeded is the richest fixture we have: ~15 months of fictional hunting, which
// exercises item_type, lots, roll_txns, trips, supplies, keepers, losses, spot and
// branches with real, messy data.
func seeded(t *testing.T) *store.Store {
	t.Helper()
	s := newStore(t)
	if err := demo.Seed(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	return s
}

// bundleDir exports s and returns the directory it landed in. The test stores are real
// files, so photos live beside them — PhotoRoot(s.Path()) is the honest root.
func bundleDir(t *testing.T, s *store.Store) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := WriteDir(context.Background(), s, PhotoRoot(s.Path()), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readCSV(t *testing.T, dir, name string) [][]string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(recs) == 0 {
		t.Fatalf("%s: no header row", name)
	}
	return recs
}

// header/data split: row 0 is the header, the rest are data rows.
func header(t *testing.T, dir, name string) []string { return readCSV(t, dir, name)[0] }
func rows(t *testing.T, dir, name string) [][]string { return readCSV(t, dir, name)[1:] }
func col(hdr []string, name string) int {
	for i, h := range hdr {
		if h == name {
			return i
		}
	}
	return -1
}

func readManifest(t *testing.T, dir string) manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// schemaTables asks the database itself what user tables exist. sqlite_schema is
// the official modern name for the catalog.
func schemaTables(t *testing.T, s *store.Store) []string {
	t.Helper()
	rs, err := s.DB().Query(
		`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var n string
		if err := rs.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func schemaColumns(t *testing.T, s *store.Store, table string) []string {
	t.Helper()
	rs, err := s.DB().Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var n string
		if err := rs.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatalf("%s: no columns — does the table exist?", table)
	}
	return out
}

func count(t *testing.T, s *store.Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// --- A3: every table in the database leaves with the user -----------------------

// A migration that adds a table must break this. If it does, the fix is not to
// edit the test — it is to decide whether the new table is the user's data (it
// almost certainly is) and add it to the bundle.
func TestBundleCoversEveryTable(t *testing.T) {
	s := seeded(t)
	dir := bundleDir(t, s)
	m := readManifest(t, dir)

	inBundle := map[string]bool{}
	for _, f := range m.Files {
		if name, ok := strings.CutSuffix(f.Name, ".csv"); ok {
			inBundle[name] = true
		}
	}
	for _, table := range schemaTables(t, s) {
		if !inBundle[table] {
			t.Errorf("table %q is in the database but not in the bundle — it would be LOST on export.\n"+
				"If a migration just added it: that is this test working. Add it to the exporter.", table)
		}
		if _, err := os.Stat(filepath.Join(dir, table+".csv")); err != nil {
			t.Errorf("%s.csv: %v", table, err)
		}
	}
	// And nothing invented: the bundle must not claim a table the database lacks.
	real := map[string]bool{}
	for _, table := range schemaTables(t, s) {
		real[table] = true
	}
	for name := range inBundle {
		if !real[name] {
			t.Errorf("bundle carries %q.csv but no such table exists", name)
		}
	}
}

// --- A4: every column in every table leaves with the user -----------------------

// derivedColumns are the ONLY columns the bundle is allowed to add beyond the real
// schema: uids resolved through a foreign key, plus the photo's path. Declared here,
// in the test, on purpose — so that adding a derived column to the exporter also
// breaks this test and has to be a deliberate act.
var derivedColumns = map[string][]string{
	"lots":           {"item_type_uid", "roll_txn_uid"},
	"roll_txns":      {"branch_uid"},
	"keepers":        {"roll_txn_uid"},
	"trips":          {"branch_uid"},
	"branch_aliases": {"branch_uid"},
	"photos":         {"path"},
}

// A migration that adds a column must break this. Same rule as above: the fix is in
// the exporter, not here.
func TestBundleCoversEveryColumn(t *testing.T) {
	s := seeded(t)
	dir := bundleDir(t, s)

	for _, table := range schemaTables(t, s) {
		want := append([]string{}, schemaColumns(t, s, table)...)
		want = append(want, derivedColumns[table]...)
		got := append([]string{}, header(t, dir, table+".csv")...)
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("%s.csv header does not match the schema.\n  schema+derived: %v\n  csv:            %v\n"+
				"If a migration just added a column: that is this test working. Add it to the exporter.",
				table, want, got)
		}
	}
}

// --- A5: uid leads, and every foreign key resolves to one -----------------------

func TestUIDLeadsAndForeignKeysResolveToUIDs(t *testing.T) {
	s := seeded(t)
	dir := bundleDir(t, s)

	// Every table that HAS a uid leads with it: the row key is the first thing you
	// see in the spreadsheet, and the first thing a photo filename is built from.
	for _, table := range []string{"item_type", "lots", "roll_txns", "branches", "photos"} {
		if got := header(t, dir, table+".csv")[0]; got != "uid" {
			t.Errorf("%s.csv leads with %q, want uid", table, got)
		}
	}

	// lots.item_type_uid resolves to the catalog row's uid — this is the join that
	// says WHAT THE COIN IS, and the one a recycled rowid would silently corrupt.
	types := map[string]string{} // id -> uid
	th := header(t, dir, "item_type.csv")
	for _, r := range rows(t, dir, "item_type.csv") {
		types[r[col(th, "id")]] = r[col(th, "uid")]
	}
	lh := header(t, dir, "lots.csv")
	checked := 0
	for _, r := range rows(t, dir, "lots.csv") {
		id, uid := r[col(lh, "item_type_id")], r[col(lh, "item_type_uid")]
		if types[id] == "" {
			t.Fatalf("lots row points at item_type %q, which is not in item_type.csv", id)
		}
		if uid != types[id] {
			t.Errorf("lots.item_type_uid %q does not resolve to item_type %q (uid %q)", uid, id, types[id])
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no lots in the fixture — this test proved nothing")
	}

	// A NULL foreign key is an EMPTY cell, never "0". "0" is a row id, and a
	// spreadsheet that joins on it lands on whatever row 0 becomes.
	nulls := 0
	for _, r := range rows(t, dir, "lots.csv") {
		if r[col(lh, "roll_txn_id")] == "0" || r[col(lh, "roll_txn_uid")] == "0" {
			t.Fatal("a NULL roll_txn_id exported as \"0\" — that is a join key pointing at nothing")
		}
		if r[col(lh, "roll_txn_id")] == "" {
			if r[col(lh, "roll_txn_uid")] != "" {
				t.Error("lots row has no roll_txn_id but carries a roll_txn_uid")
			}
			nulls++
		}
	}
	if nulls == 0 {
		t.Log("note: the fixture had no unattributed lot, so the empty-cell case rests on the \"0\" assertion above")
	}
}

// --- A6: data.json is the lossless half of the bundle ---------------------------

// CSV structurally cannot tell a NULL from an empty string — both are two commas
// with nothing between them. This schema is full of nullable columns, so the bundle
// carries data.json as well, and THAT is what makes "no data loss" literally true.
func TestDataJSONDistinguishesNullFromEmptyString(t *testing.T) {
	s := newStore(t)
	// Two keeper rows: one with a NULL date (a pre-ADR-008 row), one with an empty
	// string (a row someone cleared). They are different facts.
	if _, err := s.DB().Exec(`INSERT INTO keepers (id, denom, count, face_usd, date) VALUES (1,'dimes',10,1.0,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO keepers (id, denom, count, face_usd, date) VALUES (2,'dimes',10,1.0,'')`); err != nil {
		t.Fatal(err)
	}
	dir := bundleDir(t, s)

	// The CSV cannot express the difference — assert that, so the reason data.json
	// exists is written down in a test and not just in a comment.
	kh := header(t, dir, "keepers.csv")
	kr := rows(t, dir, "keepers.csv")
	if kr[0][col(kh, "date")] != "" || kr[1][col(kh, "date")] != "" {
		t.Fatal("expected both keeper dates to be empty cells in CSV")
	}

	var data map[string][]map[string]any
	b, err := os.ReadFile(filepath.Join(dir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	keepers := data["keepers"]
	if len(keepers) != 2 {
		t.Fatalf("want 2 keepers in data.json, got %d", len(keepers))
	}
	if v, ok := keepers[0]["date"]; !ok || v != nil {
		t.Errorf("data.json lost the NULL keeper date: got %#v", v)
	}
	if v, ok := keepers[1]["date"]; !ok || v != "" {
		t.Errorf("data.json lost the empty-string keeper date: got %#v", v)
	}
	// Numbers stay numbers, not strings.
	if v, ok := keepers[0]["face_usd"].(float64); !ok || v != 1.0 {
		t.Errorf("data.json did not keep face_usd as a number: %#v", keepers[0]["face_usd"])
	}
}

// --- A7: photos are never silently dropped --------------------------------------

// writePhoto inserts a photos row AND drops a real byte-file where the app would
// keep it: <db dir>/photos/<owner_uid>/<photo_uid>.<ext>.
func writePhoto(t *testing.T, s *store.Store, uid, ownerKind, ownerUID, role, ext string, content []byte) {
	t.Helper()
	if _, err := s.DB().Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext, caption, created)
		 VALUES (?,?,?,?,0,?,?,?)`,
		uid, ownerKind, ownerUID, role, ext, "a caption", "2026-07-12"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(s.Path()), "photos", ownerUID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, uid+"."+ext), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The photos table exists and is empty (migration 0009); the photo FEATURE (om-6hlp)
// has not shipped. This test is what turns "export must never silently drop photos"
// from a promise into a passing test BEFORE photos exist — so om-6hlp never has to
// touch internal/export.
func TestPhotosAreCopiedIntoTheBundleByteForByte(t *testing.T) {
	s := newStore(t)
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.1, Acquired: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	var lotUID string
	if err := s.DB().QueryRow(`SELECT uid FROM lots LIMIT 1`).Scan(&lotUID); err != nil {
		t.Fatal(err)
	}

	obverse := []byte("\xff\xd8\xff\xe0 not really a jpeg, but bytes are bytes")
	writePhoto(t, s, "11111111-1111-4111-8111-111111111111", "lot", lotUID, "obverse", "jpg", obverse)

	dir := bundleDir(t, s)

	// (a) the file is in the bundle, at the same relative path;
	rel := filepath.Join("photos", lotUID, "11111111-1111-4111-8111-111111111111.jpg")
	got, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("photo missing from the bundle: %v", err)
	}
	// (b) byte-identical;
	if !bytes.Equal(got, obverse) {
		t.Error("photo bytes changed on the way into the bundle")
	}
	// (c) photos.csv has a row whose path column resolves to it.
	ph := header(t, dir, "photos.csv")
	pr := rows(t, dir, "photos.csv")
	if len(pr) != 1 {
		t.Fatalf("want 1 photos row, got %d", len(pr))
	}
	path := pr[0][col(ph, "path")]
	if want := "photos/" + lotUID + "/11111111-1111-4111-8111-111111111111.jpg"; path != want {
		t.Errorf("photos.csv path = %q, want %q", path, want)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path))); err != nil {
		t.Errorf("photos.csv path does not resolve inside the bundle: %v", err)
	}
	// The caption and role survive — they are what make the picture mean something.
	if pr[0][col(ph, "role")] != "obverse" || pr[0][col(ph, "caption")] != "a caption" {
		t.Error("photos.csv lost the role or the caption")
	}

	m := readManifest(t, dir)
	if m.Photos.Dir != "photos/" || m.Photos.Count != 1 {
		t.Errorf("manifest photos = %+v, want dir photos/ count 1", m.Photos)
	}
}

// An empty photos table still reserves the shape: photos.csv with its columns, a
// photos/ directory, and photos.count = 0 in the manifest. Adding a file or a
// directory to a format users have built spreadsheets against is a breaking change;
// reserving them now costs nothing (ADR-009 (d)).
func TestEmptyPhotosTableStillReservesTheTree(t *testing.T) {
	s := seeded(t)
	dir := bundleDir(t, s)

	if got := len(rows(t, dir, "photos.csv")); got != 0 {
		t.Fatalf("fixture has photos? got %d rows", got)
	}
	if len(header(t, dir, "photos.csv")) == 0 {
		t.Error("photos.csv has no header — the columns are not reserved")
	}
	fi, err := os.Stat(filepath.Join(dir, "photos"))
	if err != nil || !fi.IsDir() {
		t.Errorf("photos/ is not reserved as a directory: %v", err)
	}
	if m := readManifest(t, dir); m.Photos.Dir != "photos/" || m.Photos.Count != 0 {
		t.Errorf("manifest photos = %+v, want dir photos/ count 0", m.Photos)
	}
}

// Trashed photos leave with the user too. "Leave with your data" means all of it — a
// user who trashed a photo by accident must find it in their export.
//
// Photos will be SOFT-deleted (om-6hlp): the row stays, marked inactive. That column
// does not exist yet, and inventing it here would be pre-empting om-6hlp's schema. So
// this test does the next migration's ALTER itself and pins the two properties that
// make the promise true — the ones that are entirely in this bead's hands:
//
//  1. the exporter FILTERS NOTHING. Not the row, not the file. A photo the user
//     trashed is still the user's photo, and export never looks at why a row is there.
//  2. the flag cannot be dropped in silence. photos.csv declares its columns, so the
//     moment om-6hlp's migration lands, the column-coverage guard fails until the
//     exporter carries the new column. Adding it is then a decision someone makes,
//     not something that quietly does not happen.
func TestASoftDeletedPhotoCannotSlipOutOfTheBundle(t *testing.T) {
	s := newStore(t)
	// Exactly the ALTER om-6hlp's soft delete needs.
	if _, err := s.DB().Exec(`ALTER TABLE photos ADD COLUMN inactive INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	writePhoto(t, s, "22222222-2222-4222-8222-222222222222", "lot", "owner-uid", "detail", "jpg", []byte("trashed but mine"))
	if _, err := s.DB().Exec(`UPDATE photos SET inactive = 1`); err != nil {
		t.Fatal(err)
	}

	dir := bundleDir(t, s)

	// (1) no filter — the trashed row and the trashed FILE both leave with the user.
	if got := len(rows(t, dir, "photos.csv")); got != 1 {
		t.Errorf("the trashed photo was FILTERED OUT of photos.csv (%d rows) — leaving with your data means all of it", got)
	}
	got, err := os.ReadFile(filepath.Join(dir, "photos", "owner-uid", "22222222-2222-4222-8222-222222222222.jpg"))
	if err != nil {
		t.Fatalf("the trashed photo's FILE was left behind: %v", err)
	}
	if string(got) != "trashed but mine" {
		t.Error("trashed photo bytes changed")
	}

	// (2) and the flag itself cannot go missing quietly: with the column in the schema
	// and not in the bundle, the column-coverage guard fails. That is what forces
	// om-6hlp to carry it, and it is why photos.csv declares its columns instead of
	// discovering them — a SELECT * exporter would pass every check while shipping
	// whatever it happened to find.
	want := append(schemaColumns(t, s, "photos"), derivedColumns["photos"]...)
	if len(header(t, dir, "photos.csv")) == len(want) {
		t.Error("a new photos column reached the bundle without anyone adding it — " +
			"the column-coverage guard cannot fire, so om-6hlp could drop the inactive flag silently")
	}
}

// A photos row whose file is gone is a corrupt state — but ONE such row must not
// take the whole export down with it. The rest of the collection (every CSV, the
// data.json, and every photo that IS on disk) is exactly what the user came to
// retrieve, and a hard failure hands them nothing. So: record the gap in the
// manifest by name, and export everything else normally. Absence stays loud (it is
// named in manifest.missing) without being fatal.
func TestAMissingPhotoFileIsRecordedNotFatal(t *testing.T) {
	s := newStore(t)
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.1, Acquired: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	// One photo with a real file on disk, one whose file is gone.
	writePhoto(t, s, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "lot", "o1", "obverse", "jpg", []byte("present"))
	if _, err := s.DB().Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES ('gone-uid','lot','o1','reverse',1,'jpg')`); err != nil {
		t.Fatal(err)
	}

	dir := bundleDir(t, s) // must NOT error

	// The whole bundle is here: every CSV, data.json, manifest, the photo that exists.
	for _, name := range []string{"lots.csv", "item_type.csv", "data.json", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("a missing photo took down the whole bundle — %s is absent: %v", name, err)
		}
	}
	if _, err := os.ReadFile(filepath.Join(dir, "photos", "o1", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.jpg")); err != nil {
		t.Errorf("the photo that WAS on disk did not make it into the bundle: %v", err)
	}
	// And both photos are still in photos.csv — export drops no rows.
	if got := len(rows(t, dir, "photos.csv")); got != 2 {
		t.Errorf("photos.csv has %d rows, want 2 (a missing FILE must not drop the ROW)", got)
	}

	// The gap is named in the manifest, loudly — the absent file, and only it.
	m := readManifest(t, dir)
	if len(m.Missing) != 1 || m.Missing[0] != "photos/o1/gone-uid.jpg" {
		t.Errorf("manifest.missing = %v, want exactly [photos/o1/gone-uid.jpg]", m.Missing)
	}
	// photos.count is the files actually IN the bundle, not the row count.
	if m.Photos.Count != 1 {
		t.Errorf("manifest photos.count = %d, want 1 (one file present, one missing)", m.Photos.Count)
	}
}

// #3, defense against a corrupt or hostile database: a photos row whose owner_uid,
// uid or ext contains a path separator or ".." must NOT be able to write a file
// outside the bundle. photos/<owner_uid>/<photo_uid>.<ext> is built from raw column
// values; owner_uid = "../../../../etc" would otherwise escape the bundle root
// entirely. The traversal row is refused (named in manifest.missing), and nothing
// lands outside the bundle.
func TestATraversalPhotoPathCannotEscapeTheBundle(t *testing.T) {
	s := newStore(t)
	// A row that tries to climb out of the bundle and drop a file next to it.
	if _, err := s.DB().Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES ('evil','lot','../../escaped','obverse',0,'jpg')`); err != nil {
		t.Fatal(err)
	}
	// Give it a real file at the honest location, so the only way it escapes is if the
	// exporter builds the traversal path.
	honest := filepath.Join(filepath.Dir(s.Path()), "photos", "../../escaped")
	if err := os.MkdirAll(honest, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(honest, "evil.jpg"), []byte("pwned"), 0o644)
	}

	parent := t.TempDir()
	dir := filepath.Join(parent, "bundle")
	if err := WriteDir(context.Background(), s, PhotoRoot(s.Path()), dir); err != nil {
		t.Fatalf("export should complete, refusing the row — got %v", err)
	}

	// Nothing was written outside the bundle root.
	walkSaw := false
	filepath.WalkDir(parent, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if strings.HasPrefix(rel, "..") {
			walkSaw = true
			t.Errorf("export wrote OUTSIDE the bundle: %s", p)
		}
		return nil
	})
	if walkSaw {
		t.Fatal("path traversal escaped the bundle root")
	}
	// And the refusal is recorded, not silent.
	m := readManifest(t, dir)
	if len(m.Missing) != 1 {
		t.Errorf("manifest.missing = %v, want the refused traversal row named", m.Missing)
	}
}

// A photo file that IS on disk but cannot be READ (permission-denied, or corrupt) must be
// recorded and skipped, exactly like an absent one — NOT abort the whole export. The
// README promises "a corrupt or moved file is noted here... never stops the rest," and an
// earlier version only honored that for os.ErrNotExist. A directory sitting where the file
// should be is a portable, root-safe stand-in for "present but unreadable": os.ReadFile
// returns an error on it even when the tests run as root (a chmod 000 file would not).
func TestAnUnreadablePhotoFileIsRecordedNotFatal(t *testing.T) {
	s := newStore(t)
	writePhoto(t, s, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "lot", "o1", "obverse", "jpg", []byte("readable"))
	if _, err := s.DB().Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES ('unreadable','lot','o2','obverse',0,'jpg')`); err != nil {
		t.Fatal(err)
	}
	// Put a DIRECTORY where photos/o2/unreadable.jpg's file would be — reading it fails.
	blocked := filepath.Join(filepath.Dir(s.Path()), "photos", "o2", "unreadable.jpg")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	dir := bundleDir(t, s) // must NOT error — an unreadable file is not a reason to abort

	// The readable photo made it; the whole bundle is intact.
	if _, err := os.ReadFile(filepath.Join(dir, "photos", "o1", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.jpg")); err != nil {
		t.Errorf("an unreadable sibling took down a readable photo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Errorf("an unreadable photo took down the whole bundle: %v", err)
	}
	// The unreadable one is recorded, not silently gone.
	m := readManifest(t, dir)
	if len(m.Missing) != 1 || m.Missing[0] != "photos/o2/unreadable.jpg" {
		t.Errorf("manifest.missing = %v, want [photos/o2/unreadable.jpg]", m.Missing)
	}
	if m.Photos.Count != 1 {
		t.Errorf("photos.count = %d, want 1 (only the readable one is in the bundle)", m.Photos.Count)
	}
}

// A corrupt/hostile row whose segment is a Windows-reserved name — a bare drive letter
// like "C:", or a trailing dot Windows silently strips — must be refused here, not left to
// die at os.Create on a Windows box (which would re-break the one-bad-row-doesn't-abort
// design, but only for Windows users). safeSegment reserves those cross-platform.
func TestWindowsReservedPhotoSegmentsAreRefused(t *testing.T) {
	for _, bad := range []string{"C:", "aux.", "a:b", "trailing "} {
		if safeSegment(bad) {
			t.Errorf("safeSegment(%q) = true, want false (unsafe on Windows)", bad)
		}
	}
	for _, ok := range []string{"11111111-1111-4111-8111-111111111111", "jpg", "png", "o1"} {
		if !safeSegment(ok) {
			t.Errorf("safeSegment(%q) = false, want true (a legitimate uid/ext)", ok)
		}
	}

	// End to end: a row with owner_uid "C:" is refused and recorded, and export completes.
	s := newStore(t)
	if _, err := s.DB().Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES ('u','lot','C:','obverse',0,'jpg')`); err != nil {
		t.Fatal(err)
	}
	dir := bundleDir(t, s)
	if m := readManifest(t, dir); len(m.Missing) != 1 {
		t.Errorf("manifest.missing = %v, want the reserved-name row refused and recorded", m.Missing)
	}
}

// The zip and the directory must stay byte-identical, and a "." segment is a way they
// could quietly diverge: the dir sink cleans "photos/./x" to "photos/x" while the zip
// keeps it verbatim. The guard rejects "." (and "") so neither sink ever sees one.
func TestGuardRejectsDotAndEmptySegments(t *testing.T) {
	for _, bad := range []string{"photos/./x.jpg", "photos//x.jpg", "photos/../x.jpg", "/abs.csv", "a\\b"} {
		if err := guardEntryName(bad); err == nil {
			t.Errorf("guardEntryName(%q) = nil, want an error", bad)
		}
	}
	for _, ok := range []string{"lots.csv", "photos/", "photos/owner/uid.jpg", "manifest.json"} {
		if err := guardEntryName(ok); err != nil {
			t.Errorf("guardEntryName(%q) = %v, want nil (a legitimate entry)", ok, err)
		}
	}
}

// --- A8: the settings canary ----------------------------------------------------

// The known tunables PutSettings writes (data.go), sorted. Nothing else is expected to
// live in the settings table; anything that does is flagged in the manifest.
var knownSettingKeysSorted = []string{
	"box_face_usd",
	"hourly_rate_usd",
	"irs_mileage_rate_usd_per_mile",
	"silver_buyback_factor_40pct",
	"silver_buyback_factor_90pct",
	"value_time",
}

// Happy path: with only the known tunables in the settings table, the manifest flags
// nothing. This proves the guard does not cry wolf over normal data.
func TestSettingsWithOnlyKnownKeysFlagsNothing(t *testing.T) {
	s := seeded(t)
	if err := s.PutSettings(model.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	dir := bundleDir(t, s)

	sh := header(t, dir, "settings.csv")
	var got []string
	for _, r := range rows(t, dir, "settings.csv") {
		got = append(got, r[col(sh, "key")])
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(knownSettingKeysSorted, ",") {
		t.Errorf("PutSettings wrote keys other than the six known tunables.\n  got:  %v\n  want: %v", got, knownSettingKeysSorted)
	}
	if m := readManifest(t, dir); len(m.UnexpectedSettings) != 0 {
		t.Errorf("the manifest flagged known tunables as unexpected: %v", m.UnexpectedSettings)
	}
}

// The real guard. The scout verified no secret lives in settings TODAY, but GetSettings
// reads ANY key (data.go) — the table is an open k/v bag, and a future feature could park
// a token there. An allow-list that DROPPED unknown keys would be data loss; instead the
// exporter carries them (leaving with your data means all of it) AND names anything beyond
// the known tunables in manifest.unexpected_settings. So a credential parked in settings
// surfaces loudly at export time instead of leaking silently, and the decision to redact
// is forced consciously.
//
// Crucially this drives the DATA path, not PutSettings: the rogue key is written straight
// to the table, exactly the way a real leak would arrive. The old test used PutSettings,
// which only ever writes the six — so it could never actually exercise an unknown key.
func TestARogueSettingsKeyIsFlaggedNotDropped(t *testing.T) {
	s := seeded(t)
	if err := s.PutSettings(model.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	// Bypass PutSettings entirely — write directly, the way a leak would.
	if _, err := s.DB().Exec(`INSERT INTO settings (key, value) VALUES ('spot_api_token','sk-live-not-a-tunable')`); err != nil {
		t.Fatal(err)
	}
	dir := bundleDir(t, s)

	// NOT dropped: the row is still in the export (no silent data loss).
	sh := header(t, dir, "settings.csv")
	found := false
	for _, r := range rows(t, dir, "settings.csv") {
		if r[col(sh, "key")] == "spot_api_token" {
			found = true
		}
	}
	if !found {
		t.Error("the rogue key was DROPPED from settings.csv — that is silent data loss")
	}
	// Flagged: the manifest names it, so it cannot leak unnoticed.
	m := readManifest(t, dir)
	if len(m.UnexpectedSettings) != 1 || m.UnexpectedSettings[0] != "spot_api_token" {
		t.Errorf("manifest.unexpected_settings = %v, want [spot_api_token] — a parked credential must be flagged", m.UnexpectedSettings)
	}
}

// --- A9: spot history is exported whole -----------------------------------------

// Spot is not a refetchable cache: the provider serves the CURRENT price only, so a
// dropped history is unrecoverable — you cannot look up what silver cost last March.
// Two years of daily prices, and the oldest day must still be in the file.
func TestSpotCSVCarriesEveryRowNotAWindow(t *testing.T) {
	s := seeded(t)
	// Two years of daily prices, in one transaction (730 separate commits is a slow
	// test, not a better one).
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 730; i++ {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO spot (as_of, gold_usd, silver_usd, source) VALUES (?,4000,60,'test')`,
			day.AddDate(0, 0, i).Format("2006-01-02")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	dir := bundleDir(t, s)
	inDB := count(t, s, "spot")
	sr := rows(t, dir, "spot.csv")
	if len(sr) != inDB {
		t.Errorf("spot.csv has %d rows, the table has %d — history was truncated", len(sr), inDB)
	}
	sh := header(t, dir, "spot.csv")
	if oldest := sr[0][col(sh, "as_of")]; oldest != "2024-01-01" {
		t.Errorf("oldest exported spot is %s — the export is a window, not the history", oldest)
	}
}

// --- row counts, every table ----------------------------------------------------

func TestEveryTableExportsEveryRow(t *testing.T) {
	s := seeded(t)
	dir := bundleDir(t, s)

	var data map[string][]map[string]any
	b, err := os.ReadFile(filepath.Join(dir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}

	for _, table := range schemaTables(t, s) {
		want := count(t, s, table)
		if got := len(rows(t, dir, table+".csv")); got != want {
			t.Errorf("%s.csv has %d rows, the table has %d", table, got, want)
		}
		if got := len(data[table]); got != want {
			t.Errorf("data.json[%s] has %d rows, the table has %d", table, got, want)
		}
	}
}

// --- A10: the manifest ----------------------------------------------------------

func TestManifestDescribesTheBundleItShipsWith(t *testing.T) {
	s := seeded(t)
	dir := bundleDir(t, s)
	m := readManifest(t, dir)

	if m.FormatVersion != 1 {
		t.Errorf("format_version = %d, want 1", m.FormatVersion)
	}
	v, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if m.DBSchemaVersion != v {
		t.Errorf("db_schema_version = %d, want the live PRAGMA user_version %d", m.DBSchemaVersion, v)
	}
	if _, err := time.Parse(time.RFC3339, m.ExportedAt); err != nil {
		t.Errorf("exported_at %q is not RFC3339: %v", m.ExportedAt, err)
	}

	// Every file it claims: present, with the row count and the sha256 it advertises.
	// This is what lets a user verify a bundle is intact years later, without the app.
	if len(m.Files) != len(schemaTables(t, s))+1 { // + data.json
		t.Errorf("manifest lists %d files, want one per table plus data.json", len(m.Files))
	}
	for _, f := range m.Files {
		b, err := os.ReadFile(filepath.Join(dir, f.Name))
		if err != nil {
			t.Errorf("manifest lists %s, which is not in the bundle: %v", f.Name, err)
			continue
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Errorf("%s: sha256 in the manifest does not match the file", f.Name)
		}
		if name, ok := strings.CutSuffix(f.Name, ".csv"); ok {
			if want := count(t, s, name); f.Rows != want {
				t.Errorf("%s: manifest says %d rows, the table has %d", f.Name, f.Rows, want)
			}
		}
	}
}

// --- A2: one builder, two sinks -------------------------------------------------

// The zip the UI downloads and the directory the CLI writes are the SAME bundle.
// Asserted, not eyeballed: same file set, same bytes.
func TestZipAndDirectoryAreTheSameBundle(t *testing.T) {
	s := seeded(t)
	writePhoto(t, s, "33333333-3333-4333-8333-333333333333", "lot", "o9", "detail", "png", []byte("pixels"))

	dir := bundleDir(t, s)

	var buf bytes.Buffer
	if err := WriteZip(context.Background(), s, PhotoRoot(s.Path()), &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}

	inZip := map[string][]byte{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") { // directory entry (photos/)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		inZip[f.Name] = b
	}

	onDisk := map[string][]byte{}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		onDisk[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(inZip) != len(onDisk) {
		t.Fatalf("zip has %d files, the directory has %d\n  zip: %v\n  dir: %v",
			len(inZip), len(onDisk), keys(inZip), keys(onDisk))
	}
	for name, want := range onDisk {
		got, ok := inZip[name]
		if !ok {
			t.Errorf("%s is in the directory bundle but not in the zip", name)
			continue
		}
		// exported_at is a clock read, and the two bundles were built a moment apart.
		// Everything else must be byte-identical.
		if name == "manifest.json" {
			if blankExportedAt(t, got) != blankExportedAt(t, want) {
				t.Error("manifest.json differs between the zip and the directory")
			}
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs between the zip and the directory", name)
		}
	}
	if _, ok := inZip["photos/o9/33333333-3333-4333-8333-333333333333.png"]; !ok {
		t.Error("the zip did not carry the photo tree")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func blankExportedAt(t *testing.T, b []byte) string {
	t.Helper()
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m.ExportedAt = ""
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// --- A1: the directory sink refuses to clobber ----------------------------------

// Mirrors the rule store.Backup already keeps: a command that can silently overwrite
// the thing you were trying to save is a footgun in the one place you least want one.
func TestWriteDirRefusesANonEmptyDirectory(t *testing.T) {
	s := seeded(t)
	root := PhotoRoot(s.Path())
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := WriteDir(context.Background(), s, root, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteDir(context.Background(), s, root, dir); err == nil {
		t.Fatal("a second export over the same directory succeeded — the first bundle was silently overwritten")
	}

	// An empty directory the user made themselves is fine: they said where to put it.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteDir(context.Background(), s, root, empty); err != nil {
		t.Errorf("refused an EMPTY directory the user pointed at: %v", err)
	}
}

// --- A13: export never writes to the database -----------------------------------

func TestExportNeverWritesToTheDatabase(t *testing.T) {
	s := seeded(t)
	before := fileSum(t, s.Path())
	bundleDir(t, s)
	if after := fileSum(t, s.Path()); after != before {
		t.Error("the database file changed during an export — export is READ-ONLY over the user's data")
	}
}

func fileSum(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// --- the download filename ------------------------------------------------------

func TestFilenameIsDatedAndObvious(t *testing.T) {
	got := Filename(time.Date(2026, 7, 12, 9, 30, 0, 0, time.UTC))
	if want := "coinrollhunter-export-2026-07-12.zip"; got != want {
		t.Errorf("Filename = %q, want %q", got, want)
	}
}

// Numbers must not come out in scientific notation: a spreadsheet reading
// "1.234e+06" as text is a support ticket.
func TestNumbersAreWrittenPlainly(t *testing.T) {
	s := newStore(t)
	if _, err := s.DB().Exec(
		`INSERT INTO losses (id, date, amount_usd, reason) VALUES (1,'2026-01-01',1234567.5,'big')`); err != nil {
		t.Fatal(err)
	}
	dir := bundleDir(t, s)
	lh := header(t, dir, "losses.csv")
	got := rows(t, dir, "losses.csv")[0][col(lh, "amount_usd")]
	if strings.ContainsAny(got, "eE") {
		t.Errorf("amount_usd = %q — scientific notation in a spreadsheet cell", got)
	}
	if f, err := strconv.ParseFloat(got, 64); err != nil || f != 1234567.5 {
		t.Errorf("amount_usd = %q, want 1234567.5", got)
	}
}

// --- snapshot consistency (the browser export was not transactional) ------------

// pausableSink wraps a sink and blocks the export the first time a named entry is
// created, so a test can hold the exporter at a precise point and probe what a
// concurrent writer can do.
type pausableSink struct {
	inner   sink
	pauseOn string
	paused  chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (p *pausableSink) Create(name string) (io.Writer, error) {
	if name == p.pauseOn {
		p.once.Do(func() {
			close(p.paused)
			<-p.resume
		})
	}
	return p.inner.Create(name)
}

// Export reads twelve tables. On the CLI that is safe (it reads a throwaway snapshot), but
// the BROWSER path reads the LIVE store, and a write landing between the item_type read and
// the lots read would ship a bundle whose lot points at an item_type_uid absent from
// item_type.csv. The reads must share one read transaction, which (the store is
// MaxOpenConns(1)) holds the single connection so any concurrent write serializes AFTER the
// reads — no interleave window.
//
// This pins it in the READ phase: pause inside the read transaction, right after the first
// table is read, and prove a concurrent write CANNOT complete until the reads finish. Per-
// query reads on the live store would let the write slip in during the pause.
func TestExportReadsBlockConcurrentWrites(t *testing.T) {
	s := newStore(t)
	if _, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"}); err != nil {
		t.Fatal(err)
	}

	paused := make(chan struct{})
	resume := make(chan struct{})
	afterFirstTableRead = func() {
		close(paused)
		<-resume
	}
	t.Cleanup(func() { afterFirstTableRead = nil })

	done := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		done <- WriteZip(context.Background(), s, PhotoRoot(s.Path()), &buf)
	}()

	<-paused // the export has read the first table and is holding the read transaction

	wDone := make(chan struct{})
	go func() {
		// Blocks on the single connection until the read tx releases it — unless the reads
		// aren't transactional, in which case the connection is free now and this returns.
		_, _ = s.InsertItemType(model.ItemType{Kind: "coin", Name: "Late Arrival", Metal: "silver"})
		close(wDone)
	}()

	select {
	case <-wDone:
		close(resume)
		<-done
		t.Fatal("a concurrent write completed DURING the read phase — the reads are not snapshot-consistent (no wrapping read transaction)")
	case <-time.After(750 * time.Millisecond):
		// Correct: the write is blocked behind the export's read transaction.
	}

	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-wDone // the previously-blocked write completes now that the tx is released
}

// The inverse, and the proof that finding #2 is fixed: once the read transaction is closed,
// the (potentially slow) file writing and photo copying must NOT hold the store's single
// connection. So a concurrent write DURING the write phase proceeds immediately — the UI and
// the spot poller aren't frozen for the whole export. The pause is now on a file Create,
// which happens after the tx has been released.
func TestExportFileWritesDoNotBlockConcurrentWrites(t *testing.T) {
	s := newStore(t)
	if _, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"}); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ps := &pausableSink{
		inner:   dirSink(dir),
		pauseOn: "item_type.csv", // the first FILE written — the tx is already closed by now
		paused:  make(chan struct{}),
		resume:  make(chan struct{}),
	}

	done := make(chan error, 1)
	go func() { done <- write(context.Background(), s, PhotoRoot(s.Path()), ps) }()

	<-ps.paused // paused mid-WRITE — the read transaction is already released

	wDone := make(chan struct{})
	go func() {
		_, _ = s.InsertItemType(model.ItemType{Kind: "coin", Name: "During Write", Metal: "silver"})
		close(wDone)
	}()

	select {
	case <-wDone:
		// Correct: the connection is free during the write phase, so the write completes.
	case <-time.After(2 * time.Second):
		close(ps.resume)
		<-done
		t.Fatal("a concurrent write BLOCKED during the file-writing phase — the export still holds the DB connection while doing I/O (finding #2 not fixed)")
	}

	close(ps.resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// --- #4a: a non-finite number is a loud, precise error, not a silent null -------

// SQLite REAL is IEEE-754, so a column can hold +Inf/NaN (reachable if an external tool
// writes it). json.Marshal turns +Inf into a useless generic error and CSV would render
// something a spreadsheet misreads. Export must refuse it with a message that names the
// table, the column, and the row — on BOTH sinks — so the user can fix the one bad cell.
func TestANonFiniteNumberFailsLoudlyNamingTheRow(t *testing.T) {
	s := newStore(t)
	// 9e999 overflows to +Inf on the way into the REAL column (a portable way to get a
	// non-finite value into SQLite without the driver rejecting a literal Inf).
	if _, err := s.DB().Exec(
		`INSERT INTO spot (as_of, gold_usd, silver_usd, source) VALUES ('2026-03-04', 9e999, 60, 'external tool')`); err != nil {
		t.Fatal(err)
	}
	var got float64
	if err := s.DB().QueryRow(`SELECT gold_usd FROM spot WHERE as_of='2026-03-04'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !math.IsInf(got, 1) {
		t.Skipf("this SQLite build stored 9e999 as %v, not +Inf — the non-finite path is unreachable here", got)
	}

	assertNamed := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("export succeeded with a +Inf value — a spreadsheet cannot represent it")
		}
		for _, want := range []string{"spot", "gold_usd", "2026-03-04"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err.Error(), want)
			}
		}
	}

	// Directory sink.
	assertNamed(t, WriteDir(context.Background(), s, PhotoRoot(s.Path()), filepath.Join(t.TempDir(), "bundle")))
	// Zip sink — same builder, same guarantee.
	var buf bytes.Buffer
	assertNamed(t, WriteZip(context.Background(), s, PhotoRoot(s.Path()), &buf))
}

// --- #4b: the directory sink is atomic — a failure leaves no partial to block a retry ---

// A mid-export failure (here, a +Inf value) must not leave the destination as a
// half-written, non-empty directory — because the no-clobber rule would then refuse
// every retry, wedging the user. The bundle is staged elsewhere and moved into place only
// on success, so a failed export leaves DIR absent and a retry (after the cause is fixed)
// succeeds.
func TestADirExportFailureLeavesNoPartialToBlockRetry(t *testing.T) {
	s := newStore(t)
	if _, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO spot (as_of, gold_usd, silver_usd, source) VALUES ('2026-03-04', 9e999, 60, 'x')`); err != nil {
		t.Fatal(err)
	}
	var got float64
	_ = s.DB().QueryRow(`SELECT gold_usd FROM spot WHERE as_of='2026-03-04'`).Scan(&got)
	if !math.IsInf(got, 1) {
		t.Skip("this SQLite build did not store +Inf; the failure path is unreachable")
	}

	dir := filepath.Join(t.TempDir(), "bundle")
	if err := WriteDir(context.Background(), s, PhotoRoot(s.Path()), dir); err == nil {
		t.Fatal("expected the export to fail on the +Inf value")
	}
	// The destination must be absent or empty — NOT a partial bundle.
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		t.Fatalf("a failed export left %d files in the destination — the no-clobber rule will now block every retry", len(entries))
	}

	// Fix the bad row; the retry must succeed (proving nothing partial is in the way).
	if _, err := s.DB().Exec(`DELETE FROM spot WHERE as_of='2026-03-04'`); err != nil {
		t.Fatal(err)
	}
	if err := WriteDir(context.Background(), s, PhotoRoot(s.Path()), dir); err != nil {
		t.Fatalf("retry after fixing the bad row failed — a partial bundle was left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Errorf("the retry did not produce a complete bundle: %v", err)
	}
}

// --- FIX A: write-in-place (no atomic rename) — cleanup on failure, never destroy ------

// Exporting into a directory that is a SYMLINK to another location must write the files
// through the link into the real target and LEAVE THE SYMLINK in place. The round-4 atomic
// rename did the opposite: RemoveAll(dir) deleted the link and Rename installed a local
// directory at its name, so the bundle never reached the synced/removable target while the
// command exited 0. Write-in-place fixes it — we open files under dir, which the OS resolves
// through the link into the target.
func TestExportIntoASymlinkedDirLandsInTheTargetAndKeepsTheLink(t *testing.T) {
	s := seeded(t)

	base := t.TempDir()
	target := filepath.Join(base, "real-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "bundle") // a symlink standing in for the export destination
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	if err := WriteDir(context.Background(), s, PhotoRoot(s.Path()), link); err != nil {
		t.Fatal(err)
	}

	// The link is still a link (not replaced by a real directory).
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the destination symlink was replaced by a real directory (mode %v) — the bundle never reached the real target", fi.Mode())
	}
	// The files landed in the REAL target, through the link.
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		t.Errorf("the bundle did not land in the symlink's target: %v", err)
	}
}

// `export .` — into an empty current directory — must succeed. The round-4 code did
// RemoveAll(dir) on success, and Go rejects RemoveAll(".") outright, so `export .` failed.
// Write-in-place never removes the destination on success, so "." works.
func TestExportIntoDotSucceeds(t *testing.T) {
	s := seeded(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(cwd) })

	if err := WriteDir(context.Background(), s, PhotoRoot(s.Path()), "."); err != nil {
		t.Fatalf("export . failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(empty, "manifest.json")); err != nil {
		t.Errorf("export . did not write the bundle into the current directory: %v", err)
	}
}

// The no-clobber race: the emptiness check runs at the start, but the round-4 code did
// RemoveAll(dir) at the END (on success), so anything a concurrent process dropped into dir
// during the long export was silently deleted. Write-in-place removes nothing on success, so
// a file that appears mid-export survives. Proven deterministically: pause the export mid-
// write, drop a file into dir, resume, and assert the file is still there afterward.
func TestASuccessfulExportDoesNotDestroyFilesAddedDuringIt(t *testing.T) {
	s := seeded(t)

	dir := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil { // pre-existing empty dir (a synced folder)
		t.Fatal(err)
	}
	ps := &pausableSink{
		inner:   &recordingDirSink{inner: dirSink(dir)},
		pauseOn: "data.json", // partway through the write phase
		paused:  make(chan struct{}),
		resume:  make(chan struct{}),
	}

	done := make(chan error, 1)
	go func() { done <- write(context.Background(), s, PhotoRoot(s.Path()), ps) }()

	<-ps.paused
	// A concurrent process drops a file into the destination AFTER the emptiness check.
	bystander := filepath.Join(dir, "notes-from-another-app.txt")
	if err := os.WriteFile(bystander, []byte("do not delete me"), 0o644); err != nil {
		t.Fatal(err)
	}
	close(ps.resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// The export succeeded and must NOT have deleted the bystander file.
	if got, err := os.ReadFile(bystander); err != nil || string(got) != "do not delete me" {
		t.Errorf("a successful export destroyed a file added to the directory during the export (err=%v) — the no-clobber race is back", err)
	}
}

// --- round 6: harden write-in-place against a racing writer in the TARGET dir ----------

// #3 (structural): every file a sink opens must be CLOSED before WriteDir's cleanup runs —
// on the error path too. An open handle can't be removed on Windows, so a leftover partial
// would block every retry. This asserts the close-ordering directly, independent of platform.
type closeSpyWriter struct {
	failWrite bool
	closed    bool
}

func (w *closeSpyWriter) Write(p []byte) (int, error) {
	if w.failWrite {
		return 0, fmt.Errorf("simulated disk-full")
	}
	return len(p), nil
}
func (w *closeSpyWriter) Close() error { w.closed = true; return nil }

type spySink struct{ w *closeSpyWriter }

func (s spySink) Create(string) (io.Writer, error) { return s.w, nil }

func TestBundleWritersCloseTheirFileEvenWhenTheWriteFails(t *testing.T) {
	// writeCSV
	{
		w := &closeSpyWriter{failWrite: true}
		if _, err := writeCSV(spySink{w}, "item_type.csv", []string{"a"}, [][]any{{int64(1)}}); err == nil {
			t.Error("writeCSV: expected the write error to surface")
		}
		if !w.closed {
			t.Error("writeCSV left the file OPEN after a write error — Windows cleanup would fail on it")
		}
	}
	// writeJSON
	{
		w := &closeSpyWriter{failWrite: true}
		if _, err := writeJSON(spySink{w}, "data.json", map[string]int{"a": 1}); err == nil {
			t.Error("writeJSON: expected the write error to surface")
		}
		if !w.closed {
			t.Error("writeJSON left the file OPEN after a write error")
		}
	}
	// copyPhoto
	{
		src := filepath.Join(t.TempDir(), "p.jpg")
		if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		w := &closeSpyWriter{failWrite: true}
		if _, err := copyPhoto(spySink{w}, src, "photos/o/p.jpg"); err == nil {
			t.Error("copyPhoto: expected the write error to surface")
		}
		if !w.closed {
			t.Error("copyPhoto left the file OPEN after a write error")
		}
	}
}

// #1: a file appearing in the destination AFTER the no-clobber check must NOT be silently
// overwritten. With O_EXCL, the collision is a loud error and the concurrent file is intact.
func TestExportRefusesToOverwriteAFileThatAppearsMidExport(t *testing.T) {
	s := seeded(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil { // empty when the no-clobber check runs
		t.Fatal(err)
	}
	// After the check passes (during the read phase), a concurrent process drops a file whose
	// name collides with a bundle file.
	afterFirstTableRead = func() {
		_ = os.WriteFile(filepath.Join(dir, "item_type.csv"), []byte("CONCURRENT — DO NOT OVERWRITE"), 0o644)
	}
	t.Cleanup(func() { afterFirstTableRead = nil })

	if err := WriteDir(context.Background(), s, PhotoRoot(s.Path()), dir); err == nil {
		t.Fatal("export overwrote a file that appeared mid-export — a silent truncate")
	}
	got, err := os.ReadFile(filepath.Join(dir, "item_type.csv"))
	if err != nil {
		t.Fatalf("the concurrent file was removed: %v", err)
	}
	if string(got) != "CONCURRENT — DO NOT OVERWRITE" {
		t.Errorf("the concurrent file was overwritten: %q", got)
	}
}

// #2: a mid-export failure must clean up ONLY the files we wrote — never a concurrent file,
// even one under a directory we created. Here a concurrent manifest.json (dropped mid-export)
// triggers the O_EXCL failure at the end; a top-level bystander and a bystander under our
// photos/ dir must both survive, while our own outputs are removed.
func TestFailureCleanupRemovesOnlyOurFilesNotConcurrentOnes(t *testing.T) {
	s := seeded(t)
	// A photo of ours, so photos/<owner>/<uid>.jpg is written and tracked.
	writePhoto(t, s, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "lot", "ours", "obverse", "jpg", []byte("our photo"))

	dir := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	afterFirstTableRead = func() {
		// Concurrent content that must survive cleanup:
		_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep me"), 0o644)
		_ = os.MkdirAll(filepath.Join(dir, "photos"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, "photos", "intruder.txt"), []byte("keep me too"), 0o644)
		// A concurrent manifest.json collides at the very end -> O_EXCL failure after all our
		// files are written.
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o644)
	}
	t.Cleanup(func() { afterFirstTableRead = nil })

	if err := WriteDir(context.Background(), s, PhotoRoot(s.Path()), dir); err == nil {
		t.Fatal("expected the concurrent manifest.json collision to fail the export")
	}

	// Concurrent files survive.
	if b, err := os.ReadFile(filepath.Join(dir, "notes.txt")); err != nil || string(b) != "keep me" {
		t.Errorf("cleanup deleted a concurrent top-level file (err=%v)", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "photos", "intruder.txt")); err != nil || string(b) != "keep me too" {
		t.Errorf("cleanup deleted a concurrent file under a directory we created (err=%v)", err)
	}
	// Our own outputs are gone.
	if _, err := os.Stat(filepath.Join(dir, "item_type.csv")); !os.IsNotExist(err) {
		t.Errorf("cleanup left our item_type.csv behind (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data.json")); !os.IsNotExist(err) {
		t.Errorf("cleanup left our data.json behind (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "photos", "ours", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.jpg")); !os.IsNotExist(err) {
		t.Errorf("cleanup left our photo file behind (err=%v)", err)
	}
}
