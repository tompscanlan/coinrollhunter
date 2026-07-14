// Package store is the SQLite persistence layer (ADR-001 + ADR-003). It uses the
// pure-Go modernc.org/sqlite driver (no CGO) so the binary cross-compiles with a
// plain `go build`. Migrations are embedded SQL applied on Open and tracked by
// PRAGMA user_version.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the database handle.
type Store struct {
	db   *sql.DB
	path string
	// wmu serializes read-modify-write sequences against each other. SetMaxOpenConns(1)
	// serializes individual statements, but not a SELECT and a later UPDATE: another
	// writer can commit in the gap, and the RMW then writes back its stale snapshot.
	// The API's partial update (api.register's PUT) is exactly such a sequence — a sale
	// landing mid-merge would otherwise be undone by the merge's pre-sale read.
	wmu sync.Mutex
}

// WithWrite runs fn while holding the store's write lock, making a read-modify-write
// sequence atomic with respect to the other writers that take it. Not reentrant: fn
// must not call another WithWrite (or a store method that takes the lock itself).
//
// It is a MUTEX, not a transaction — there is no Begin/Commit/Rollback here, and
// nothing it wraps is rolled back on failure. If you need a multi-row write to land
// all-or-nothing (om-u3el: the legacy importer), use WithTx.
func (s *Store) WithWrite(fn func() error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return fn()
}

// execer is the write surface shared by *sql.DB and *sql.Tx. Every statement in this
// package's insert path is issued against one of these, so the exact same SQL serves
// the auto-commit *Store methods and their transaction-bound *Tx twins.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Tx is a transaction-bound writer: the handle WithTx hands its callback. Its
// mutations carry the same names, signatures and validation as their *Store
// counterparts — they just write through an open transaction instead of
// auto-committing, so the whole sequence lands or none of it does.
//
// A *Tx is only valid inside the WithTx callback that produced it; do not retain it.
type Tx struct{ db *sql.Tx }

// WithTx runs fn inside ONE database transaction: everything fn writes through the
// *Tx commits together, and ANY error fn returns — a rejected row, a missing table,
// a disk-full, a panic-free early return — rolls the whole thing back. It takes the
// store write lock for the same reason SellHolding and MergeBranches do (a
// read-modify-write elsewhere must not interleave with a multi-row sequence).
//
// *** fn MUST write ONLY through the *Tx it is handed. *** Calling any *Store method
// from inside fn DEADLOCKS, silently and permanently: Open sets SetMaxOpenConns(1)
// (SQLite tolerates one writer), so the open transaction holds the pool's only
// connection and the Store method's s.db.Exec blocks forever waiting for it to come
// back — and Store methods that take wmu themselves would block on the lock this
// already holds. The symptom is a hung test, not an error. This is why the insert
// path is tx-aware (data.go) rather than the transaction simply being wrapped around
// the existing auto-commit inserts.
func (s *Store) WithTx(fn func(*Tx) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once Commit succeeds; the safety net on every error path
	if err := fn(&Tx{db: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

// Open opens (creating if needed) the SQLite database at path and applies any
// pending migrations. Use ":memory:" for tests. Foreign keys are enforced.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// SQLite tolerates only one writer; keep a single connection to avoid
	// "database is locked" under the pure-Go driver.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying handle (for the API/CRUD layer).
func (s *Store) DB() *sql.DB { return s.db }

// Path is the file this store was opened from — ":memory:" for a test store. Photos
// live beside it (ADR-009: photos/<owner_uid>/<photo_uid>.<ext>), and the database
// handle alone cannot say where that is, so the exporter asks here.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Backup writes a consistent snapshot of the database to dest, safely, while the
// app is running.
//
// Copying crh.db with `cp` is the obvious move and it is wrong: SQLite keeps
// recently committed pages in a -wal sidecar, so a naive file copy can capture a
// database missing its most recent transactions, or catch a checkpoint mid-flight
// and produce a torn file that only fails later. VACUUM INTO takes a read
// transaction and writes a fully self-contained, defragmented database — one file,
// no sidecars, no need to stop the server first.
//
// It refuses to overwrite an existing file (SQLite's own rule, kept deliberately):
// a backup command that can silently clobber the previous backup is a footgun in
// the one place you least want one.
func (s *Store) Backup(dest string) error { return vacuumInto(s.db, dest) }

// BackupFile snapshots a database file without migrating it — which is the whole
// point of a backup: it must not alter what it is preserving. Open() applies
// pending migrations as a side effect, so backing up through it would silently
// upgrade the very database you were trying to capture *before* upgrading. This
// opens the file as-is, at whatever schema version it happens to be.
func BackupFile(src, dest string) error {
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer db.Close()
	return vacuumInto(db, dest)
}

func vacuumInto(db *sql.DB, dest string) error {
	if strings.TrimSpace(dest) == "" {
		return errors.New("backup: destination path is required")
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("backup: %s already exists (refusing to overwrite a backup)", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: %s: %w", dest, err)
	}
	if _, err := db.Exec(`VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("backup to %s: %w", dest, err)
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads embedded migrations/NNNN_name.sql, sorted by version.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: want NNNN_name.sql", e.Name())
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version: %w", e.Name(), err)
		}
		b, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// migrate applies every migration whose version exceeds PRAGMA user_version,
// each in its own transaction, then bumps user_version.
func (s *Store) migrate() error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", m.name, err)
		}
		// user_version doesn't accept placeholders; version is our own int.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("bump user_version for %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Version returns the applied schema version (PRAGMA user_version).
func (s *Store) Version() (int, error) {
	var v int
	err := s.db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}
