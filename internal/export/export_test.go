package export

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

// bundleDir exports s and returns the directory it landed in.
func bundleDir(t *testing.T, s *store.Store) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := WriteDir(s, dir); err != nil {
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
	if err := WriteDir(s, dir); err != nil {
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
	if err := WriteZip(s, &buf); err != nil {
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
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := WriteDir(s, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteDir(s, dir); err == nil {
		t.Fatal("a second export over the same directory succeeded — the first bundle was silently overwritten")
	}

	// An empty directory the user made themselves is fine: they said where to put it.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteDir(s, empty); err != nil {
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
