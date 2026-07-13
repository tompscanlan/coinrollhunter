package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// The compat rule is the one place this change could silently eat someone's
// data: every install to date keeps crh.db next to the binary, so if the new
// per-user data dir ever wins over an existing crh.db, that user opens the app
// to an empty dashboard and concludes their holdings are gone.
func TestDefaultDBPathPrefersExistingCwdDB(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if err := os.WriteFile(dbName, []byte("not really a db"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := defaultDBPath()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(dbName)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("with a crh.db in cwd, defaultDBPath() = %q, want the existing db %q", got, want)
	}
}

// The other half: a fresh install must NOT write into the working directory,
// because for a double-clicked binary that is Downloads — or a temp dir, if
// Windows ran the .exe straight out of the zip preview, in which case the
// holdings evaporate.
func TestDefaultDBPathUsesDataDirWhenCwdIsEmpty(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	data := t.TempDir()
	t.Setenv("LOCALAPPDATA", data) // windows
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", data) // darwin

	got, err := defaultDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, data) {
		t.Errorf("with an empty cwd, defaultDBPath() = %q, want it under the user data dir %q", got, data)
	}
	if filepath.Dir(got) == dir {
		t.Errorf("defaultDBPath() = %q — must not land in the working directory", got)
	}
	if filepath.Base(got) != dbName {
		t.Errorf("defaultDBPath() = %q, want basename %q", got, dbName)
	}
}

// instanceAt decides whether a failed bind means "we are already running" (open
// the existing window) or "someone else owns this port" (move to another one).
// Getting it wrong the first way opens a browser onto a stranger's server.
func TestInstanceAtRecognisesOnlyUs(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
	}{
		{
			name: "our health endpoint",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"status":"ok"}`))
			},
			want: true,
		},
		{
			name: "some other server answering 200 on that path",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`<html>not us</html>`))
			},
			want: false,
		},
		{
			name:    "something on the port that 404s",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			addr := strings.TrimPrefix(srv.URL, "http://")
			if got := instanceAt(addr); got != tc.want {
				t.Errorf("instanceAt() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstanceAtOnADeadPort(t *testing.T) {
	// A port nobody is listening on: a connection error, not a bad response.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	if instanceAt(addr) {
		t.Error("instanceAt() = true for a closed port, want false")
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })
}

// `coinrollhunter export DIR` is the "browse the photos in a file manager beside the
// spreadsheet" form — the same bundle the UI downloads as a zip, written as plain
// files. It keeps backup's no-clobber rule: a command that can silently overwrite
// the thing you were trying to save is a footgun in the one place you least want one.
func TestExportWritesABundleAndRefusesToClobberIt(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "crh.db")
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	bundle := filepath.Join(dir, "bundle")
	if err := runExport([]string{"--db", db, bundle}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lots.csv", "item_type.csv", "data.json", "manifest.json", "photos"} {
		if _, err := os.Stat(filepath.Join(bundle, name)); err != nil {
			t.Errorf("bundle is missing %s: %v", name, err)
		}
	}
	if err := runExport([]string{"--db", db, bundle}); err == nil {
		t.Error("a second export over the same directory succeeded — the first bundle was silently overwritten")
	}
	if err := runExport([]string{"--db", db}); err == nil {
		t.Error("export with no destination succeeded — it must say where it would have written")
	}
}

// EXPORT MUST NOT MUTATE THE USER'S DATABASE. The obvious implementation —
// store.Open(src) — is wrong: Open applies pending migrations as a side effect, so
// exporting an OLD snapshot (a v9 archive from before migration 0010) would silently
// upgrade it to v10 on disk before reading it. That is the exact defect BackupFile
// exists to avoid: it opens raw, precisely so a backup never upgrades the thing it is
// preserving. Export owes the user the same promise — it is read-only over their data.
//
// This test builds a genuine v9-schema database (all tables, but item_type has no uid
// and user_version is 9), exports it, and asserts the SOURCE FILE is byte-for-byte
// what it was: still v9, still no uid column. It fails on an Open(src) implementation
// and passes on copy-migrate-the-copy.
func TestExportDoesNotUpgradeAnOldSourceDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "v9.db")

	// A full, real schema at the latest version…
	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.1, Acquired: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// …wound back to look exactly like a database last touched before 0010 shipped:
	// drop item_type.uid and its index, and set the schema version to 9. This is what a
	// user's archived export-from-an-old-release would be on disk today.
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX idx_item_type_uid`,
		`ALTER TABLE item_type DROP COLUMN uid`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			raw.Close()
			t.Fatalf("building the v9 fixture (%s): %v", stmt, err)
		}
	}
	raw.Close()

	before := fileSHA(t, db)

	bundle := filepath.Join(dir, "bundle")
	if err := runExport([]string{"--db", db, bundle}); err != nil {
		t.Fatalf("export of a v9 database failed: %v", err)
	}

	// The bundle is still correct — the throwaway copy was migrated, so item_type_uid
	// is emitted and joins resolve. (The bundle's own contents are tested exhaustively
	// in internal/export; here we just confirm export produced a real one.)
	if _, err := os.Stat(filepath.Join(bundle, "manifest.json")); err != nil {
		t.Errorf("no manifest in the bundle: %v", err)
	}

	// THE POINT: the user's file is untouched. Same bytes, same schema version, no uid.
	if after := fileSHA(t, db); after != before {
		t.Error("the source database file CHANGED during export — export is not read-only over the user's data")
	}
	check, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var version int
	if err := check.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 9 {
		t.Errorf("export upgraded the source database to schema version %d — it must stay 9", version)
	}
	var uidCols int
	if err := check.QueryRow(`SELECT count(*) FROM pragma_table_info('item_type') WHERE name='uid'`).Scan(&uidCols); err != nil {
		t.Fatal(err)
	}
	if uidCols != 0 {
		t.Error("export added a uid column to the source item_type table — it migrated the user's data")
	}
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// EXPORT MUST NOT LOSE PHOTOS. Photos live beside the user's REAL database
// (photos/<owner_uid>/<photo_uid>.<ext>, ADR-009). The A13 fix reads the data from a
// throwaway COPY of the database — but the photo files are NOT copied, and if the
// exporter derives the photo root from the copy's path, it looks in an empty temp dir,
// finds nothing, and (photos being non-fatal) exits 0 with an empty photos/ dir. A real
// coin's only picture, silently dropped. The photo root must come from the ORIGINAL src.
//
// This is the data-integrity core of the feature, so it is asserted end-to-end through
// the CLI, not just in the export package (whose tests never go through the copy path).
func TestCLIExportKeepsPhotosThatLiveBesideTheRealDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "crh.db")

	s, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.1, Acquired: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	// A photo row, and its real file on disk beside the database (where the app keeps it).
	const owner, photo = "owner-uid-1", "11111111-1111-4111-8111-111111111111"
	if _, err := s.DB().Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES (?,?,?,?,0,?)`,
		photo, "lot", owner, "obverse", "jpg"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	want := []byte("\xff\xd8\xff\xe0 the only picture of this coin")
	photoDir := filepath.Join(dir, "photos", owner)
	if err := os.MkdirAll(photoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(photoDir, photo+".jpg"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	bundle := filepath.Join(dir, "bundle")
	if err := runExport([]string{"--db", db, bundle}); err != nil {
		t.Fatal(err)
	}

	// The photo is in the bundle, byte-for-byte.
	got, err := os.ReadFile(filepath.Join(bundle, "photos", owner, photo+".jpg"))
	if err != nil {
		t.Fatalf("the photo beside the real DB was DROPPED from the export: %v", err)
	}
	if string(got) != string(want) {
		t.Error("photo bytes changed on the way into the bundle")
	}
	// And nothing was reported missing — the file was right there.
	var m struct {
		Missing []string `json:"missing"`
		Photos  struct {
			Count int `json:"count"`
		} `json:"photos"`
	}
	mb, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Missing) != 0 {
		t.Errorf("manifest.missing = %v, want empty (the photo existed beside the DB)", m.Missing)
	}
	if m.Photos.Count != 1 {
		t.Errorf("manifest photos.count = %d, want 1", m.Photos.Count)
	}
}
