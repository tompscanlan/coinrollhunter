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
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
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

// WriteZip writes the bundle to w as a zip. This is the UI's download: a browser can
// only take a single file, and Explorer opens a zip as a folder while macOS
// auto-extracts it — so a zip is the right artifact on every platform we ship.
func WriteZip(s *store.Store, w io.Writer) error {
	zw := zip.NewWriter(w)
	if err := write(s, zw); err != nil {
		return err
	}
	return zw.Close()
}

// WriteDir writes the same bundle into dir as plain files. It refuses to write into a
// directory that already has anything in it — the rule `backup` already keeps, for the
// reason it keeps it: a command that can silently overwrite the thing you were trying
// to save is a footgun in the one place you least want one.
func WriteDir(s *store.Store, dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil && len(entries) > 0:
		return fmt.Errorf("export: %s is not empty (refusing to overwrite what is already there)", dir)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("export: %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("export: %s: %w", dir, err)
	}
	return write(s, dirSink(dir))
}

// dirSink writes a bundle entry as a file under a directory, creating parents (the
// photo tree is two levels deep).
type dirSink string

func (d dirSink) Create(name string) (io.Writer, error) {
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

// write builds one bundle into one sink. Both public entry points come through here,
// so the zip and the directory cannot drift apart.
func write(s *store.Store, out sink) error {
	m := manifest{
		FormatVersion: FormatVersion,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Photos:        manifestPhotoDir{Dir: "photos/"},
	}
	var err error
	if m.DBSchemaVersion, err = s.Version(); err != nil {
		return fmt.Errorf("export: read schema version: %w", err)
	}

	// data.json is the lossless half of the bundle. CSV cannot tell a NULL from an
	// empty string — both are two commas with nothing between them — and this schema is
	// full of nullable columns, so "no data loss" is only literally true with this file
	// in the bundle. It is also what a future importer reads.
	data := map[string][]jsonRow{}

	for _, tb := range tables {
		cols, rows, err := read(s, tb)
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

	if m.Photos.Count, err = copyPhotos(s, out); err != nil {
		return err
	}
	_, err = writeJSON(out, "manifest.json", m)
	return err
}

// read runs one table's query and returns its CSV header and its rows, with every
// value normalized: SQL NULL is nil, everything else is a string, an int64 or a
// float64. Both the CSV and data.json render from these same values, so the two
// halves of the bundle cannot disagree.
func read(s *store.Store, tb table) ([]string, [][]any, error) {
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
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + tb.name + " t " +
		strings.Join(joins, " ") + " ORDER BY " + tb.orderBy

	rs, err := s.DB().Query(q)
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
			if b, ok := v.([]byte); ok {
				vals[i] = string(b) // otherwise JSON base64-encodes it
			}
		}
		out = append(out, vals)
	}
	return cols, out, rs.Err()
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

// copyPhotos copies every photo file into the bundle and returns how many. Originals
// only: the resized derivatives the app renders from are a regenerable cache, not the
// user's data, and shipping both would double the bundle for nothing.
//
// A photos row whose file is missing FAILS the export, by name. It is tempting to
// skip it and carry on — and that is exactly the silent drop this bead exists to
// prevent. A bundle handed over as complete, minus a coin's only picture, is worse
// than an error the user can act on.
func copyPhotos(s *store.Store, out sink) (int, error) {
	if _, err := out.Create("photos/"); err != nil { // reserved even when empty
		return 0, fmt.Errorf("export photos/: %w", err)
	}
	root := photoRoot(s)

	rs, err := s.DB().Query(`SELECT owner_uid, uid, ext FROM photos ORDER BY owner_uid, seq, uid`)
	if err != nil {
		return 0, fmt.Errorf("export photos: %w", err)
	}
	defer rs.Close()

	n := 0
	for rs.Next() {
		var ownerUID, uid, ext string
		if err := rs.Scan(&ownerUID, &uid, &ext); err != nil {
			return 0, fmt.Errorf("export photos: %w", err)
		}
		rel := "photos/" + ownerUID + "/" + uid + "." + ext
		if err := copyPhoto(out, filepath.Join(root, ownerUID, uid+"."+ext), rel); err != nil {
			return 0, err
		}
		n++
	}
	return n, rs.Err()
}

func copyPhoto(out sink, src, rel string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("export %s: the photo is in your database but its file is missing from disk: %w", rel, err)
	}
	defer f.Close()
	w, err := out.Create(rel)
	if err != nil {
		return fmt.Errorf("export %s: %w", rel, err)
	}
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("export %s: %w", rel, err)
	}
	if c, ok := w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// photoRoot is where the app keeps photo files: beside the database (ADR-009). An
// in-memory store has no directory, and no photos either.
func photoRoot(s *store.Store) string {
	p := s.Path()
	if p == "" || p == ":memory:" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "photos")
}
