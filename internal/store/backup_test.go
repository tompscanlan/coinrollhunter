package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// sqlOpen reaches past Store to the raw driver, so a test can stand up a database
// at an arbitrary schema version without Open() migrating it out from under us.
func sqlOpen(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }

// The claim Backup makes is specifically that it works on a LIVE database — the app
// is open, the server is running, you did not stop anything. So test it that way:
// write, back up without closing, and check the snapshot is complete and readable
// on its own.
func TestBackupSnapshotsALiveDatabase(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "crh.db")

	s, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver", FineOzEach: 0.0723})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := s.InsertHolding(model.Holding{
			ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.10, Acquired: "2026-07-01",
		}); err != nil {
			t.Fatal(err)
		}
	}

	dest := filepath.Join(dir, "backup.db")
	if err := s.Backup(dest); err != nil {
		t.Fatal(err)
	}

	// One file, no sidecars. That is the property that makes it copyable: a -wal or
	// -shm left beside it would mean the snapshot is incomplete on its own.
	for _, sidecar := range []string{dest + "-wal", dest + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			t.Errorf("backup left a %s sidecar — the file is not self-contained", filepath.Base(sidecar))
		}
	}

	// The source is still open and usable; a backup must not disturb it.
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.10, Acquired: "2026-07-02"}); err != nil {
		t.Fatalf("source database broken after backup: %v", err)
	}

	// The snapshot opens standalone and carries every row committed before it ran —
	// and not the one committed after.
	b, err := Open(dest)
	if err != nil {
		t.Fatalf("backup will not open: %v", err)
	}
	defer b.Close()

	var n int
	if err := b.db.QueryRow(`SELECT count(*) FROM lots`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Errorf("backup has %d lots, want the 50 committed before it ran", n)
	}
	everyRowHasAUID(t, b)

	var ver int
	if err := b.db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if srcVer, _ := s.Version(); ver != srcVer {
		t.Errorf("backup schema version %d != source %d", ver, srcVer)
	}
}

// The backup you most want is the one taken *before* an upgrade — so BackupFile
// must not migrate the source on its way through. Open() does, which is why the CLI
// does not go through it.
func TestBackupFileDoesNotMigrateTheSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "old.db")

	// A database from an older release: schema version 0, none of our tables.
	db, err := sqlOpen(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE lots (id INTEGER PRIMARY KEY); PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	dest := filepath.Join(dir, "snap.db")
	if err := BackupFile(src, dest); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{"source": src, "snapshot": dest} {
		d, err := sqlOpen(path)
		if err != nil {
			t.Fatal(err)
		}
		var v int
		if err := d.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		d.Close()
		if v != 4 {
			t.Errorf("%s is at schema version %d, want 4 — backup migrated it", name, v)
		}
	}
}

// A backup command that can silently overwrite the previous backup is a footgun in
// the one place you least want one.
func TestBackupRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "crh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dest := filepath.Join(dir, "backup.db")
	if err := os.WriteFile(dest, []byte("an earlier backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(dest); err == nil {
		t.Fatal("Backup overwrote an existing file")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "an earlier backup" {
		t.Error("the existing backup was clobbered")
	}
}
