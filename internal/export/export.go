// Package export writes the user's data out in a form they can keep, read, and
// take somewhere else — the "you can always leave with your data" half of a
// local-first promise (ADR-009 (d), om-9cua).
//
// It is deliberately NOT a backup. `coinrollhunter backup` writes a database:
// machine-readable, restorable, starts a new instance. An export bundle is a
// spreadsheet: human-readable, openable in Excel or Numbers or a text editor,
// with the photos sitting in a folder beside it. Two different promises, two
// different artifacts — so there is no crh.db inside the bundle.
//
// The bundle:
//
//	item_type.csv  lots.csv  roll_txns.csv  keepers.csv  trips.csv  supplies.csv
//	losses.csv  branches.csv  branch_aliases.csv  spot.csv  settings.csv  photos.csv
//	data.json      — the same rows, typed, with NULL preserved (CSV cannot)
//	manifest.json  — format version, schema version, row counts, checksums
//	photos/        — <owner_uid>/<photo_uid>.<ext>, the originals
//
// Everything, always: no filters, no options, no checkboxes. An export with
// options is an export with a way to silently produce an incomplete file.
package export

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// FormatVersion is the version of the BUNDLE — not of the database. Bump it on any
// column, file, or directory added, removed, or renamed.
//
// The contract for whoever writes the importer: a bundle whose format_version is
// HIGHER than you understand must be REFUSED, loudly and whole. Never partially
// import one. A user who exported from a newer version and re-imported into an
// older one would otherwise get a silently truncated collection back.
const FormatVersion = 1

// Filename is what the browser saves the download as. Dated, because the second
// thing a user does with an export is make another one.
func Filename(t time.Time) string {
	return "coinrollhunter-export-" + t.Format("2006-01-02") + ".zip"
}

// --- the format ---------------------------------------------------------------

// derived is a column the exporter computes rather than reads: a foreign key
// resolved to the stable uid of the row it points at.
//
// This is the whole reason migration 0010 exists. A CSV that joins lots to
// item_type on an integer is a CSV that silently repoints at the wrong coin type
// the moment a catalog row is deleted and the rowid is handed out again. The
// integer stays in the bundle (it is the data), but the uid beside it is the join
// key that survives.
type derived struct {
	name string // the CSV column
	expr string // the SQL that produces it
	join string // the LEFT JOIN it needs, if any
}

// table is one exported table: which columns, in what order, plus the derived ones.
//
// The column list is written out rather than discovered with SELECT *, and that is
// on purpose: it is what lets a test compare the bundle against PRAGMA table_info
// and BREAK when a migration adds a column. A self-discovering exporter can never
// fail that test, and a column that leaves the app without anyone deciding it should
// is exactly the silent loss this format exists to prevent.
type table struct {
	name    string
	cols    []string // real columns, in the order the CSV carries them
	derived []derived
	orderBy string
}

var (
	itemTypeUID = derived{"item_type_uid", "it.uid", "LEFT JOIN item_type it ON it.id = t.item_type_id"}
	rollTxnUID  = derived{"roll_txn_uid", "rt.uid", "LEFT JOIN roll_txns rt ON rt.id = t.roll_txn_id"}
	branchUID   = derived{"branch_uid", "b.uid", "LEFT JOIN branches b ON b.id = t.branch_id"}
	// The one column a user can actually follow: photos.csv -> the file on disk, in
	// one step, with no filename convention to reconstruct.
	photoPath = derived{"path", `'photos/' || t.owner_uid || '/' || t.uid || '.' || t.ext`, ""}
)

// tables is the bundle. Every table in the database is here — a table missing from
// this list is a table the user loses, which is what TestBundleCoversEveryTable
// exists to catch. Tables that have a uid lead with it: it is the row's identity,
// and the first thing you want to see when you open the sheet.
var tables = []table{
	{name: "item_type", orderBy: "t.id",
		cols: []string{"uid", "id", "kind", "name", "metal", "fine_oz_each", "fineness", "year", "mint", "mintmark", "refs"}},

	{name: "lots", orderBy: "t.id",
		cols: []string{"uid", "id", "item_type_id", "roll_txn_id", "activity", "qty", "gross_weight", "purity",
			"weight_unit", "basis_usd", "premium_usd", "face_value_usd", "acquired", "source", "location",
			"insured_value", "attributes", "notes", "category", "subcategory", "trophy", "disposed", "disposed_usd"},
		derived: []derived{itemTypeUID, rollTxnUID}},

	{name: "roll_txns", orderBy: "t.id",
		cols:    []string{"uid", "id", "date", "branch_id", "action", "denom", "unit", "amount", "face_usd", "source_type", "notes"},
		derived: []derived{branchUID}},

	{name: "keepers", orderBy: "t.id",
		cols:    []string{"id", "denom", "count", "face_usd", "date", "roll_txn_id"},
		derived: []derived{rollTxnUID}},

	{name: "trips", orderBy: "t.id",
		cols:    []string{"id", "date", "branch_id", "miles", "hours"},
		derived: []derived{branchUID}},

	{name: "supplies", orderBy: "t.id",
		cols: []string{"id", "date", "item", "cost_usd"}},

	{name: "losses", orderBy: "t.id",
		cols: []string{"id", "date", "amount_usd", "reason", "scope"}},

	{name: "branches", orderBy: "t.id",
		cols: []string{"uid", "id", "name", "institution", "address", "phone", "lat", "lon", "hours",
			"buys", "dumps", "denoms", "box_limit", "box_lead_days", "coin_fee_usd", "cooldown_days", "notes", "active"}},

	{name: "branch_aliases", orderBy: "t.alias",
		cols:    []string{"branch_id", "alias"},
		derived: []derived{branchUID}},

	// Spot is user data, not a cache. The price feed serves the CURRENT price only —
	// you cannot re-fetch what silver cost last March, so a dropped history is gone.
	{name: "spot", orderBy: "t.as_of",
		cols: []string{"as_of", "gold_usd", "silver_usd", "platinum_usd", "palladium_usd", "source"}},

	{name: "settings", orderBy: "t.key",
		cols: []string{"key", "value"}},

	// Photos ship as a real table with real columns even while the photo feature
	// (om-6hlp) is still ahead of us: adding a file or a column to a format users have
	// already built spreadsheets against is a breaking change, and reserving them now
	// costs nothing (ADR-009 (d)). No filter of any kind — a photo the user moved to
	// the trash is still the user's photo.
	{name: "photos", orderBy: "t.owner_uid, t.seq, t.uid",
		cols:    []string{"uid", "id", "owner_kind", "owner_uid", "role", "seq", "ext", "caption", "created"},
		derived: []derived{photoPath}},
}

// --- the manifest --------------------------------------------------------------

type manifest struct {
	// The version of this FORMAT. An importer that does not understand it must refuse
	// the bundle rather than partially read it.
	FormatVersion int `json:"format_version"`
	// The database's PRAGMA user_version at export time — which migrations the columns
	// below reflect. The same number the migration runner keys on.
	DBSchemaVersion int              `json:"db_schema_version"`
	ExportedAt      string           `json:"exported_at"`
	Files           []manifestFile   `json:"files"`
	Photos          manifestPhotoDir `json:"photos"`
	// Files a row referenced but that are NOT in the bundle: a photo whose file was gone
	// from disk, or a row whose path was unsafe and refused. Absence stays loud (named
	// here) without being fatal — one corrupt row must not deny the user the rest of their
	// data. Always present (empty when nothing is missing) so a consumer can rely on it.
	Missing []string `json:"missing"`
	// Keys found in the settings table that are NOT one of the known tunables (see
	// knownSettingKeys). Nothing is dropped — that would be data loss — but a credential
	// parked in the open k/v settings bag surfaces here instead of leaking silently.
	// Always present (empty in the normal case).
	UnexpectedSettings []string `json:"unexpected_settings"`
}

// manifestFile is what lets a user (or an importer) verify a bundle is intact years
// later, with no app and no network: count the rows, checksum the bytes.
type manifestFile struct {
	Name   string `json:"name"`
	Rows   int    `json:"rows"`
	SHA256 string `json:"sha256"`
}

type manifestPhotoDir struct {
	Dir   string `json:"dir"`
	Count int    `json:"count"`
}

// --- the two sinks -------------------------------------------------------------

// sink is where a bundle's files land. Two of them exist and no more is planned: the
// zip the browser downloads, and the plain directory the CLI writes so the photos are
// browsable in a file manager beside the spreadsheet. archive/zip.Writer already IS
// this interface.
type sink interface {
	Create(name string) (io.Writer, error)
}

// PhotoRoot is where photo files live for a database at dbPath: beside it, in a photos/
// directory (ADR-009: photos/<owner_uid>/<photo_uid>.<ext>). An in-memory database has no
// directory, and no photos, so its root is "".
//
// This is a function, not something the exporter derives from the store it reads, and that
// is the fix for a real regression: the CLI reads the data from a throwaway COPY of the
// database, and a photo root derived from the copy's path points at an empty temp dir — so
// every photo would be silently dropped. The photo root is the path to the user's REAL
// data, passed in explicitly by each caller, and cannot drift from the store being read.
//
// Callers should pass a symlink-resolved path (see ResolveDBPath): photos live beside the
// real file, not beside a link pointing at it.
func PhotoRoot(dbPath string) string {
	if dbPath == "" || dbPath == ":memory:" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), "photos")
}

// ResolveDBPath returns dbPath with symlinks resolved, so the photo directory is derived
// from where the database REALLY lives. If ~/crh.db is a symlink to /srv/coins/crh.db, the
// photos sit under /srv/coins/photos/ — deriving the root from the link's own directory
// would find none and ship a photo-less bundle. A path that can't be resolved (a broken
// link, a race) falls back to the raw spelling, which is the safe existing behaviour.
func ResolveDBPath(dbPath string) string {
	if dbPath == "" || dbPath == ":memory:" {
		return dbPath
	}
	if resolved, err := filepath.EvalSymlinks(dbPath); err == nil {
		return resolved
	}
	return dbPath
}

// WriteZip writes the bundle to w as a zip. This is the UI's download: a browser can
// only take a single file, and Explorer opens a zip as a folder while macOS
// auto-extracts it — so a zip is the right artifact on every platform we ship.
//
// photoRoot is the directory the photo files live in (PhotoRoot of the user's real
// database), passed explicitly so it can never be confused with the store's own path.
func WriteZip(s *store.Store, photoRoot string, w io.Writer) error {
	zw := zip.NewWriter(w)
	if err := write(s, photoRoot, zipSink{zw}); err != nil {
		return err
	}
	return zw.Close()
}

// zipSink is the zip writer as a bundle sink, with the same entry-name guard the
// directory sink applies — so a ".." in an entry name cannot ride into the archive
// and traverse on someone else's extraction.
type zipSink struct{ zw *zip.Writer }

func (z zipSink) Create(name string) (io.Writer, error) {
	if err := guardEntryName(name); err != nil {
		return nil, err
	}
	return z.zw.Create(name)
}

// guardEntryName is the last line of defense for both sinks: it rejects any bundle
// entry name that is absolute or has a ".", ".." or empty segment. Legitimate names are
// built from fixed strings and validated uids; this catches a bad photo path before it
// can escape the bundle root (dir sink) or become a traversal entry (zip sink). Photo
// segments are also checked at the source (safeSegment), so this should never fire for
// them — it is belt-and-suspenders, and it also covers the hardcoded file names.
//
// Rejecting "." and "" (not just "..") keeps the zip and the directory byte-identical: a
// name like "photos/./x.jpg" would sail into the zip verbatim while the dir sink cleaned
// it to "photos/x.jpg", and the two bundles would silently disagree.
func guardEntryName(name string) error {
	trimmed := strings.TrimSuffix(name, "/") // a reserved directory entry ("photos/")
	if trimmed == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return fmt.Errorf("export: unsafe bundle path %q", name)
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("export: unsafe bundle path %q", name)
		}
	}
	return nil
}

// WriteDir writes the same bundle into dir as plain files. It refuses to write into a
// directory that already has anything in it — the rule `backup` already keeps, for the
// reason it keeps it: a command that can silently overwrite the thing you were trying
// to save is a footgun in the one place you least want one.
//
// photoRoot is the directory the photo files live in (PhotoRoot of the user's real
// database), passed explicitly so it can never be confused with the store's own path.
//
// The write is ATOMIC: the bundle is staged in a sibling temp directory and renamed into
// place only on full success. So ANY mid-export failure — a non-finite number, a broken
// sink, a full disk — leaves the destination absent, never a half-written partial. That
// matters because the no-clobber rule above would otherwise refuse every retry of a
// destination left non-empty by a failed run, wedging the user.
func WriteDir(s *store.Store, photoRoot, dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil && len(entries) > 0:
		return fmt.Errorf("export: %s is not empty (refusing to overwrite what is already there)", dir)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("export: %s: %w", dir, err)
	}

	// Stage in a sibling of dir (same filesystem, so the rename is atomic and cheap). A
	// dot prefix keeps a leftover from ever looking like a real bundle.
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("export: %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, ".crh-export-*")
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(staging)
		}
	}()

	if err := write(s, photoRoot, dirSink(staging)); err != nil {
		return err
	}
	// Move the finished bundle into place. dir is absent or an empty directory (the
	// no-clobber check guaranteed it); remove the empty one so the rename lands cleanly
	// on every platform.
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("export: %s: %w", dir, err)
	}
	if err := os.Rename(staging, dir); err != nil {
		return fmt.Errorf("export: %s: %w", dir, err)
	}
	committed = true
	return nil
}

// dirSink writes a bundle entry as a file under a directory, creating parents (the
// photo tree is two levels deep).
type dirSink string

func (d dirSink) Create(name string) (io.Writer, error) {
	if err := guardEntryName(name); err != nil {
		return nil, err
	}
	p := filepath.Join(string(d), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if strings.HasSuffix(name, "/") { // a reserved-but-empty directory
		return io.Discard, os.MkdirAll(p, 0o755)
	}
	return os.Create(p)
}

// --- the builder ---------------------------------------------------------------

// querier is what read/copyPhotos/unexpectedSettingKeys run their queries through.
// Both *sql.DB and *sql.Tx satisfy it, which is what lets write route every read of an
// export through ONE transaction.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// write builds one bundle into one sink. Both public entry points come through here,
// so the zip and the directory cannot drift apart. photoRoot is where the photo files
// live on disk — passed in, never derived from the store (see PhotoRoot).
//
// Every read runs inside ONE read transaction. On the CLI that is belt-and-suspenders
// (it already reads a throwaway snapshot), but the browser path reads the LIVE store, and
// twelve separate queries could otherwise straddle a write — shipping a bundle whose lot
// points at an item_type_uid that isn't in item_type.csv. The store is MaxOpenConns(1), so
// an open read tx holds the single connection and any concurrent write serializes AFTER
// the export: there is no interleave window, which is the guarantee.
func write(s *store.Store, photoRoot string, out sink) error {
	tx, err := s.DB().BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("export: begin read transaction: %w", err)
	}
	defer tx.Rollback() // read-only: rollback is the whole lifecycle, never a commit

	m := manifest{
		FormatVersion:      FormatVersion,
		ExportedAt:         time.Now().UTC().Format(time.RFC3339),
		Photos:             manifestPhotoDir{Dir: "photos/"},
		Missing:            []string{},
		UnexpectedSettings: []string{},
	}
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&m.DBSchemaVersion); err != nil {
		return fmt.Errorf("export: read schema version: %w", err)
	}
	if m.UnexpectedSettings, err = unexpectedSettingKeys(tx); err != nil {
		return err
	}

	// data.json is the lossless half of the bundle. CSV cannot tell a NULL from an
	// empty string — both are two commas with nothing between them — and this schema is
	// full of nullable columns, so "no data loss" is only literally true with this file
	// in the bundle. It is also what a future importer reads.
	data := map[string][]jsonRow{}

	for _, tb := range tables {
		cols, rows, err := read(tx, tb)
		if err != nil {
			return err
		}
		csvSum, err := writeCSV(out, tb.name+".csv", cols, rows)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, manifestFile{Name: tb.name + ".csv", Rows: len(rows), SHA256: csvSum})

		data[tb.name] = make([]jsonRow, 0, len(rows))
		for _, r := range rows {
			data[tb.name] = append(data[tb.name], jsonRow{cols: cols, vals: r})
		}
	}

	sum, err := writeJSON(out, "data.json", data)
	if err != nil {
		return err
	}
	total := 0
	for _, rows := range data {
		total += len(rows)
	}
	m.Files = append(m.Files, manifestFile{Name: "data.json", Rows: total, SHA256: sum})

	if m.Photos.Count, m.Missing, err = copyPhotos(tx, photoRoot, out); err != nil {
		return err
	}
	_, err = writeJSON(out, "manifest.json", m)
	return err
}

// knownSettingKeys is the fixed set of tunables the app itself stores (store.PutSettings,
// data.go). The settings table is an open key/value bag — GetSettings reads any key — so
// this is the reference the canary compares against.
var knownSettingKeys = map[string]bool{
	"value_time":                    true,
	"hourly_rate_usd":               true,
	"irs_mileage_rate_usd_per_mile": true,
	"silver_buyback_factor_40pct":   true,
	"silver_buyback_factor_90pct":   true,
	"box_face_usd":                  true,
}

// unexpectedSettingKeys returns any settings key that is not one of the known tunables,
// sorted. It DROPS nothing — the keys are still exported (leaving with your data means all
// of it) — but it names them in the manifest so a credential parked in settings surfaces
// loudly instead of leaking silently. Verified today there is none; this keeps it honest.
func unexpectedSettingKeys(q querier) ([]string, error) {
	rs, err := q.Query(`SELECT key FROM settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("export settings: %w", err)
	}
	defer rs.Close()
	out := []string{}
	for rs.Next() {
		var k string
		if err := rs.Scan(&k); err != nil {
			return nil, fmt.Errorf("export settings: %w", err)
		}
		if !knownSettingKeys[k] {
			out = append(out, k)
		}
	}
	return out, rs.Err()
}

// read runs one table's query and returns its CSV header and its rows, with every
// value normalized: SQL NULL is nil, everything else is a string, an int64 or a
// float64. Both the CSV and data.json render from these same values, so the two
// halves of the bundle cannot disagree.
func read(q querier, tb table) ([]string, [][]any, error) {
	cols := make([]string, 0, len(tb.cols)+len(tb.derived))
	sel := make([]string, 0, cap(cols))
	for _, c := range tb.cols {
		cols = append(cols, c)
		sel = append(sel, "t."+c)
	}
	var joins []string
	for _, d := range tb.derived {
		cols = append(cols, d.name)
		sel = append(sel, d.expr)
		if d.join != "" {
			joins = append(joins, d.join)
		}
	}
	query := "SELECT " + strings.Join(sel, ", ") + " FROM " + tb.name + " t " +
		strings.Join(joins, " ") + " ORDER BY " + tb.orderBy

	rs, err := q.Query(query)
	if err != nil {
		return nil, nil, fmt.Errorf("export %s: %w", tb.name, err)
	}
	defer rs.Close()

	var out [][]any
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, fmt.Errorf("export %s: %w", tb.name, err)
		}
		for i, v := range vals {
			switch v := v.(type) {
			case float64:
				// A REAL column can hold Inf/NaN (an external tool can write one). CSV would
				// render something a spreadsheet misreads, and json.Marshal(+Inf) fails with a
				// generic, undebuggable error. Refuse it here, naming the exact cell, so the
				// user can fix the one bad row rather than lose the whole export to a mystery.
				if math.IsInf(v, 0) || math.IsNaN(v) {
					return nil, nil, fmt.Errorf(
						"export %s: column %q is a non-finite number (%v) in row %s — a spreadsheet cannot hold it; correct that row and export again",
						tb.name, cols[i], v, rowLabel(cols, vals))
				}
			case []byte:
				// []byte -> string so JSON emits text, not base64. Lossless for valid UTF-8,
				// which is all the app writes; SQLite TEXT can technically hold invalid UTF-8
				// (only reachable via an external tool), and json.Marshal substitutes U+FFFD
				// for it — a best-effort we accept rather than base64-encode every text column.
				vals[i] = string(v)
			}
		}
		out = append(out, vals)
	}
	return cols, out, rs.Err()
}

// rowLabel names a row in an error: its uid if the table has one, else its identifying
// first column (id / as_of / key / branch_id) — enough for the user to find and fix it.
func rowLabel(cols []string, vals []any) string {
	for i, c := range cols {
		if c == "uid" {
			return fmt.Sprintf("uid=%v", vals[i])
		}
	}
	if len(cols) == 0 {
		return "?"
	}
	return fmt.Sprintf("%s=%v", cols[0], vals[0])
}

// cell renders one value for a spreadsheet. A NULL is an EMPTY cell — never "0",
// which is a row id, and a sheet that joins on it lands on whatever row 0 becomes.
func cell(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		// 'f', not 'g': a spreadsheet reading "1.234567e+06" as text is a support
		// ticket. -1 keeps the shortest form that round-trips exactly.
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// jsonRow keeps data.json's keys in the same order as the CSV's columns, so the two
// files read as the same table rather than as two unrelated dumps.
type jsonRow struct {
	cols []string
	vals []any
}

func (r jsonRow) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, c := range r.cols {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(r.vals[i])
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// --- writing, with a checksum as we go -----------------------------------------

func writeCSV(out sink, name string, cols []string, rows [][]any) (string, error) {
	w, err := create(out, name)
	if err != nil {
		return "", err
	}
	cw := csv.NewWriter(w)
	rec := make([]string, len(cols))
	if err := cw.Write(cols); err != nil {
		return "", fmt.Errorf("export %s: %w", name, err)
	}
	for _, r := range rows {
		for i, v := range r {
			rec[i] = cell(v)
		}
		if err := cw.Write(rec); err != nil {
			return "", fmt.Errorf("export %s: %w", name, err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return "", fmt.Errorf("export %s: %w", name, err)
	}
	return w.close(name)
}

func writeJSON(out sink, name string, v any) (string, error) {
	w, err := create(out, name)
	if err != nil {
		return "", err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("export %s: %w", name, err)
	}
	return w.close(name)
}

// create opens a bundle entry and tees everything written to it through a sha256, so
// the manifest's checksums are of the bytes that actually shipped rather than of a
// second, hopefully-identical render.
func create(out sink, name string) (*hashWriter, error) {
	w, err := out.Create(name)
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", name, err)
	}
	return &hashWriter{w: w, h: sha256.New()}, nil
}

type hashWriter struct {
	w io.Writer
	h hash.Hash
}

func (hw *hashWriter) Write(p []byte) (int, error) {
	n, err := hw.w.Write(p)
	if n > 0 {
		hw.h.Write(p[:n])
	}
	return n, err
}

// close finishes the entry (the directory sink hands back a real file; the zip
// writer does not) and returns its checksum.
func (hw *hashWriter) close(name string) (string, error) {
	if c, ok := hw.w.(io.Closer); ok {
		if err := c.Close(); err != nil {
			return "", fmt.Errorf("export %s: %w", name, err)
		}
	}
	return hex.EncodeToString(hw.h.Sum(nil)), nil
}

// --- photos ---------------------------------------------------------------------

// copyPhotos copies every photo file from root into the bundle. Originals only: the
// resized derivatives the app renders from are a regenerable cache, not the user's data,
// and shipping both would double the bundle for nothing. It returns how many files were
// actually written, and the relative paths of any that were referenced by a row but NOT
// put in the bundle (absent, unreadable, or refused as unsafe).
//
// root is passed in (PhotoRoot of the user's real database), never derived from the store
// being read — the CLI reads a throwaway copy, and deriving the root from the copy's path
// pointed at an empty temp dir and silently dropped every photo. An empty root ("") means
// an in-memory store, which has no photos.
//
// Two things must NOT happen, and both took review rounds to get right:
//
//   - A single unreadable file must not take the whole export down. The rest of the
//     collection is exactly what the user came to retrieve; a hard failure hands them
//     nothing. So an absent, permission-denied, or corrupt file is RECORDED in missing[]
//     and export carries on. Absence stays loud (named in the manifest) without being
//     fatal. Only a failure to WRITE the bundle (a broken sink) is fatal.
//   - A row's path is built from raw column values, so an owner_uid or uid or ext
//     carrying a separator, "..", or another unsafe token could write OUTSIDE the bundle.
//     Such a row is refused (recorded in missing[]), never written — checked here at the
//     source (safeSegment), and again at the sink (guardEntryName) as a second line.
func copyPhotos(q querier, root string, out sink) (int, []string, error) {
	missing := []string{}
	if _, err := out.Create("photos/"); err != nil { // reserved even when empty
		return 0, missing, fmt.Errorf("export photos/: %w", err)
	}

	rs, err := q.Query(`SELECT owner_uid, uid, ext FROM photos ORDER BY owner_uid, seq, uid`)
	if err != nil {
		return 0, missing, fmt.Errorf("export photos: %w", err)
	}
	defer rs.Close()

	n := 0
	for rs.Next() {
		var ownerUID, uid, ext string
		if err := rs.Scan(&ownerUID, &uid, &ext); err != nil {
			return 0, missing, fmt.Errorf("export photos: %w", err)
		}
		rel := "photos/" + ownerUID + "/" + uid + "." + ext
		// A path segment that could climb out of the bundle (or break os.Create on the
		// user's platform) is refused, not written. root == "" (in-memory) has no files.
		if root == "" || !safeSegment(ownerUID) || !safeSegment(uid) || !safeSegment(ext) {
			missing = append(missing, rel)
			continue
		}
		copied, err := copyPhoto(out, filepath.Join(root, ownerUID, uid+"."+ext), rel)
		if err != nil {
			return 0, missing, err // the SINK failed — the bundle itself can't be written
		}
		if !copied {
			missing = append(missing, rel) // the source file was absent or unreadable
			continue
		}
		n++
	}
	return n, missing, rs.Err()
}

// safeSegment reports whether a photo path segment is safe to use as a directory or file
// name on any platform we ship. A UUID or a "jpg" always passes; this refuses a corrupt or
// hostile row before it becomes a path. Rejected: empty; a path separator (/ or \); ".."
// or "." as a component or substring; and the extra characters Windows forbids — ':' (so a
// corrupt "C:" can't become a drive-relative path), '*?"<>|', control bytes, and a trailing
// '.' or space (Windows strips them, changing the name). Reserving these keeps the
// one-bad-row-doesn't-break-the-bundle promise true on Windows too, not just here.
func safeSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.Contains(s, "..") {
		return false
	}
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	return true
}

// copyPhoto copies one photo file into the bundle. It reads the whole file before writing
// anything, so an unreadable source never leaves a truncated entry in the bundle: a file
// that is absent, permission-denied, or corrupt mid-read reports copied=false with no
// error (the caller records it as missing and moves on), while a failure to WRITE the
// bundle entry — a broken sink, a full disk — is a real error and stops the export.
func copyPhoto(out sink, src, rel string) (copied bool, err error) {
	data, err := os.ReadFile(src)
	if err != nil {
		// Absent, permission-denied, corrupt: not the user's fault to lose the rest of
		// their data over. Recorded by the caller as missing, never fatal.
		return false, nil
	}
	w, err := out.Create(rel)
	if err != nil {
		return false, fmt.Errorf("export %s: %w", rel, err)
	}
	if _, err := w.Write(data); err != nil {
		return false, fmt.Errorf("export %s: %w", rel, err)
	}
	if c, ok := w.(io.Closer); ok {
		if err := c.Close(); err != nil {
			return false, fmt.Errorf("export %s: %w", rel, err)
		}
	}
	return true, nil
}
