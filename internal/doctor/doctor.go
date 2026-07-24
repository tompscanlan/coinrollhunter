// Package doctor is a READ-ONLY health scan of a CoinRollHunter database. It
// reports rows that cannot be true, links that dangle, and links that are
// technically resolvable but do not add up — and it repairs nothing.
//
// # Why it exists
//
// Three defenses were built into this app, and each one left the same gap behind:
//
//   - The write-time validators put a check in front of every write. They guard
//     NEW writes only. A historical row, a spreadsheet import, or a hand-edited
//     database never passes through them, and calc.Compute sums whatever it finds —
//     so a lot with basis_usd = -100 understates the headline money by $100,
//     silently.
//   - The stable-uid migration moved the box/branch links onto never-recycled uids,
//     so a deleted parent leaves the child's link DANGLING rather than silently
//     re-adopted by whatever row inherited the rowid. Dangling resolves to blank,
//     which is the right answer — but blank is indistinguishable from never-linked,
//     so a find that lost its box looks exactly like a find that never had one.
//   - That same migration also established that a database re-adopted BEFORE the
//     links moved onto uids is frozen wrong and cannot be honestly repaired. It can,
//     however, be reported: a find acquired before the box it came from is not
//     proof, but it is a conservative suspect worth a human's eyes.
//
// # Why it does not repair
//
// Heuristic repair was proven to false-positive on correct data, and a false
// positive here is silent, unrecoverable money loss with no undo. So the scan
// reports and stops. Every fix is the user's, made in the grids.
//
// # Two doors, one scan
//
// The GUI's Data-health panel (GET /api/doctor) and `coinrollhunter doctor` both
// call Scan. The CLI door matters for more than support tickets: when a database
// is broken enough that the app cannot serve or migrate, it is the only surface
// left — see the Unreadable field, and calc's note on non-finite values, which
// make GET /api/summary fail to encode at all.
package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// Class is what kind of problem a finding is. The three classes are the three
// observability gaps above, in descending order of certainty: an invalid row is
// wrong, an orphan link is definitely broken, a suspect link only looks wrong.
type Class string

const (
	// ClassInvalid — the row cannot be true: it fails the same model validator every
	// write has to pass. It is being computed with anyway.
	ClassInvalid Class = "invalid"
	// ClassOrphan — the row's stored link points at a uid no row has: the parent was
	// deleted. Reads resolve it to blank, so the row renders as if never linked.
	ClassOrphan Class = "orphan"
	// ClassSuspect — the link resolves, but the two rows disagree about something
	// they should agree on. NOT proof of corruption; a prompt to look.
	ClassSuspect Class = "suspect"
)

// Finding is one problem, named precisely enough that the user can go find the row
// in the Edit grids and fix it themselves. That is the whole contract: doctor's
// output is only useful if every line ends at an actionable row.
type Finding struct {
	Class Class  `json:"class"`
	Table string `json:"table"`  // the table to open in the Edit tab
	RowID int64  `json:"row_id"` // that table's id (0 for the id-less singletons: spot, settings)
	Label string `json:"label"`  // the row in human terms — a product name, a date, a denom
	Field string `json:"field"`  // the column at fault
	Value string `json:"value"`  // the offending value, verbatim
	// Detail says what is wrong AND what the app is currently doing about it. The
	// second half is the point: "invalid" without a consequence reads as pedantry,
	// and the user has no way to judge whether it is worth their evening.
	Detail string `json:"detail"`
}

// TableError is a table the scan could not read at all. It is kept separate from
// Findings on purpose: "no findings" from a scan that failed to read half the
// database is a lie, and it is exactly the situation a user runs doctor in.
//
// It is reachable, not theoretical. SQLite columns have affinity, not types, so a
// hand edit in a SQLite browser can put the string "abc" in basis_usd — the NOT NULL
// constraint is satisfied and the write succeeds. The store then scans that column
// into a plain float64, which fails the ENTIRE query, not one row. Every list, grid
// and total over that table is dead until the cell is fixed, and nothing in the app
// says which cell.
type TableError struct {
	Table string `json:"table"`
	Error string `json:"error"`
}

// Report is the whole scan. Counts and Scanned are for the summary line: "3
// problems across 1,240 rows" is what a user reads, and a scan that found nothing
// needs to say how much it looked at or it reads as a no-op.
type Report struct {
	Findings   []Finding      `json:"findings"`
	Unreadable []TableError   `json:"unreadable"`
	Counts     map[string]int `json:"counts"`  // by class
	Scanned    map[string]int `json:"scanned"` // rows read, by table
}

// Healthy reports whether the scan both completed and found nothing. Both halves
// matter — see TableError.
func (r *Report) Healthy() bool { return len(r.Findings) == 0 && len(r.Unreadable) == 0 }

// Scan runs the full read-only health scan. It never writes, and it never returns
// early on a bad table: a database with one unreadable table must still get a
// report about the other nine.
//
// An error comes back only when the scan could not run at all (a closed database,
// a cancelled context). Anything else is in the Report.
func Scan(ctx context.Context, s *store.Store) (*Report, error) {
	r := &Report{
		Findings:   []Finding{}, // non-nil: these serialize as [] not null
		Unreadable: []TableError{},
		Counts:     map[string]int{string(ClassInvalid): 0, string(ClassOrphan): 0, string(ClassSuspect): 0},
		Scanned:    map[string]int{},
	}

	scanInvalid(s, r)
	if err := scanOrphans(ctx, s, r); err != nil {
		return nil, err
	}
	if err := scanSuspects(ctx, s, r); err != nil {
		return nil, err
	}

	for _, f := range r.Findings {
		r.Counts[string(f.Class)]++
	}
	return r, nil
}

// --- class: invalid ----------------------------------------------------------

// scanInvalid walks every table that has both a list accessor and a validator, and
// asks the codebase's OWN definition of a valid row. Reusing model.Validate rather
// than restating the rules is the point: doctor can never drift from what the write
// path enforces, so it cannot report a row as bad that the grid would accept, or
// stay quiet about one the grid would reject.
//
// The photos table is deliberately out of scope: it holds no money, and a bad photo
// row corrupts no total. The photo tree has its own integrity story elsewhere.
func scanInvalid(s *store.Store, r *Report) {
	// The catalog up front, so a lot's finding can name the coin rather than a rowid.
	// A finding the user cannot connect to something they recognize is a finding they
	// will not act on. If the catalog itself is unreadable the labels degrade to the
	// bare id — the scan still runs.
	names := map[int64]string{}
	if types, err := s.ListItemTypes(); err == nil {
		for _, t := range types {
			names[t.ID] = t.Name
		}
	}

	scanTable(r, "item_type", s.ListItemTypes, func(t model.ItemType) (int64, string) { return t.ID, t.Name })
	scanTable(r, "lots", s.ListHoldings, func(h model.Holding) (int64, string) { return h.ID, holdingLabel(h, names) })
	scanTable(r, "roll_txns", s.ListRollTxns, func(t model.RollTxn) (int64, string) {
		return t.ID, strings.TrimSpace(t.Date + " " + t.Action + " " + t.Denom)
	})
	scanTable(r, "trips", s.ListTrips, func(t model.Trip) (int64, string) { return t.ID, strings.TrimSpace(t.Date + " " + t.Bank) })
	scanTable(r, "branches", s.ListBranches, func(b model.Branch) (int64, string) { return b.ID, b.Name })
	scanTable(r, "supplies", s.ListSupplies, func(x model.Supply) (int64, string) {
		return x.ID, strings.TrimSpace(x.Date + " " + x.Item)
	})
	scanTable(r, "keepers", s.ListKeepers, func(k model.Keeper) (int64, string) {
		return k.ID, fmt.Sprintf("%s ×%d", k.Denom, k.Count)
	})
	scanTable(r, "losses", s.ListLosses, func(l model.Loss) (int64, string) {
		return l.ID, strings.TrimSpace(l.Date + " " + l.Reason)
	})
	// spot and settings have no id — RowID stays 0 and the label carries the identity.
	scanTable(r, "spot", s.ListSpot, func(sp model.Spot) (int64, string) { return 0, sp.AsOf })
	scanTable(r, "settings", func() ([]model.Settings, error) {
		cfg, err := s.GetSettings()
		return []model.Settings{cfg}, err
	}, func(model.Settings) (int64, string) { return 0, "settings" })
}

// validator is what every model row implements.
type validator interface{ Validate() error }

// scanTable lists one table and checks every row. Generic over the row type so each
// table is one line above and the checks cannot be applied inconsistently.
func scanTable[T validator](r *Report, table string, list func() ([]T, error), identify func(T) (int64, string)) {
	rows, err := list()
	if err != nil {
		// Not fatal. A table whose read path fails is the loudest possible finding,
		// but it must not cost the user the report on every other table.
		r.Unreadable = append(r.Unreadable, TableError{Table: table, Error: err.Error()})
		return
	}
	r.Scanned[table] = len(rows)
	for _, row := range rows {
		id, label := identify(row)
		if err := row.Validate(); err != nil {
			r.Findings = append(r.Findings, invalidFinding(table, id, label, row, err))
		}
		r.Findings = append(r.Findings, nonFiniteFindings(table, id, label, row)...)
	}
}

// invalidFinding turns a validator's rejection into a finding, keeping the field
// name when the validator gave one (model.FieldError) so the user can go straight
// to the offending cell.
//
// Known limitation, stated rather than hidden: every Validate() returns on its
// FIRST bad field, so a row with two problems reports one, and fixing it can
// reveal the next on the following run. Reporting all of them would mean a second
// implementation of the rules that could disagree with the write path — a worse
// trade than a second run.
func invalidFinding(table string, id int64, label string, row any, err error) Finding {
	f := Finding{Class: ClassInvalid, Table: table, RowID: id, Label: label, Detail: err.Error()}
	var fe *model.FieldError
	if errors.As(err, &fe) {
		f.Field = fe.Field
		f.Detail = fe.Field + " " + fe.Msg + " — this row is being computed with as-is"
		// The validator names the column but not what is IN it (model.FieldError
		// carries no value), and "basis_usd must not be negative" without the number
		// makes the user hunt for a row they were just handed. Read it back off the
		// struct by the same json name the validator used.
		f.Value = fieldValue(row, fe.Field)
	}
	return f
}

// fieldValue reads one column off a row by its json/column name — the name the
// validators, the API and the grids all agree on. Returns "" when the field cannot
// be found, which is the honest answer: better a finding with no value than a
// finding with the wrong one.
func fieldValue(row any, jsonField string) string {
	v := reflect.ValueOf(row)
	if v.Kind() != reflect.Struct {
		return ""
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if jsonName(t.Field(i)) != jsonField {
			continue
		}
		switch fv := v.Field(i); fv.Kind() {
		case reflect.String:
			return fv.String()
		case reflect.Float64:
			return strconv.FormatFloat(fv.Float(), 'g', -1, 64)
		case reflect.Int, reflect.Int64:
			return strconv.FormatInt(fv.Int(), 10)
		case reflect.Bool:
			return strconv.FormatBool(fv.Bool())
		default:
			return fmt.Sprint(fv.Interface())
		}
	}
	return ""
}

// nonFiniteFindings reports ±Infinity in any float column, which no validator
// catches: model's nonNeg tests `v < 0`, and that is false for +Inf and for NaN.
//
// This is worth its own check because it is the most destructive value a column can
// hold and the most invisible. One infinite basis makes every total that touches it
// infinite, and encoding/json cannot encode ±Inf at all — so GET /api/summary fails
// to serialize and the dashboard goes blank with NO error the user can act on. This
// scan is the surface that still answers.
//
// Reflection rather than a hand-written list of columns per table: a column added
// later is covered automatically, where a list would silently stop being complete.
func nonFiniteFindings(table string, id int64, label string, row any) []Finding {
	var out []Finding
	v := reflect.ValueOf(row)
	if v.Kind() != reflect.Struct {
		return nil
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if v.Field(i).Kind() != reflect.Float64 {
			continue
		}
		x := v.Field(i).Float()
		if !math.IsInf(x, 0) && !math.IsNaN(x) {
			continue
		}
		name := jsonName(t.Field(i))
		out = append(out, Finding{
			Class: ClassInvalid, Table: table, RowID: id, Label: label,
			Field:  name,
			Value:  strconv.FormatFloat(x, 'g', -1, 64),
			Detail: name + " is not a finite number — every total it reaches becomes infinite, and the summary response cannot be encoded at all (a blank dashboard)",
		})
	}
	return out
}

// jsonName is the column name as the API and the grids know it, falling back to the
// Go field name so a field without a tag still reports something usable.
func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return f.Name
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return f.Name
	}
	return tag
}

// holdingLabel names a lot the way the user thinks of it — the product from the
// catalog, with the id kept so it can still be found in the grid when two lots share
// a product name (which they routinely do).
func holdingLabel(h model.Holding, names map[int64]string) string {
	if n := names[h.ItemTypeID]; n != "" {
		return fmt.Sprintf("%s (lot %d)", n, h.ID)
	}
	return fmt.Sprintf("lot %d", h.ID)
}

// --- class: orphan -----------------------------------------------------------

// orphanScan is one child→parent uid link. Table and column names are literals
// here and never reach this from user input.
type orphanScan struct {
	child, link, parent string
	label               string // SQL expression producing the row's human handle
	detail              string
}

// The four durable links the stable-uid migration moved onto uids. branch_aliases.branch_id
// stays an integer and is deliberately absent: it cannot orphan, because it is deleted in the
// same transaction as its branch and repointed by MergeBranches before the loser goes.
var orphanScans = []orphanScan{
	{
		child: "lots", link: "roll_txn_uid", parent: "roll_txns",
		label:  `coalesce((SELECT t.name FROM item_type t WHERE t.id = c.item_type_id), '') || ' (lot ' || c.id || ')'`,
		detail: "this find points at a box that no longer exists — it reads back as having no box, which looks exactly like a find that was never linked to one, so it silently drops out of per-box and per-bank yield",
	},
	{
		child: "keepers", link: "roll_txn_uid", parent: "roll_txns",
		label:  `coalesce(c.denom,'') || ' ×' || coalesce(c.count,0)`,
		detail: "this keeper batch points at a box that no longer exists — its face still counts toward kept_face, but it can no longer be attributed to the session it came from",
	},
	{
		child: "roll_txns", link: "branch_uid", parent: "branches",
		label:  `coalesce(c.date,'') || ' ' || coalesce(c.action,'') || ' ' || coalesce(c.denom,'')`,
		detail: "this purchase points at a bank branch that no longer exists — it reads back with a blank bank, and stops counting toward branch_count and per-bank yield",
	},
	{
		child: "trips", link: "branch_uid", parent: "branches",
		label:  `coalesce(c.date,'')`,
		detail: "this trip points at a bank branch that no longer exists — its miles and hours still cost you, but they can no longer be attributed to a bank",
	},
}

// scanOrphans finds links whose stored uid matches no row. NOT EXISTS rather than a
// LEFT JOIN ... IS NULL because the whole question is about the stored uid column,
// which every normal read path has already resolved away to a blank integer.
func scanOrphans(ctx context.Context, s *store.Store, r *Report) error {
	for _, o := range orphanScans {
		q := fmt.Sprintf(
			`SELECT c.id, c.%[1]s, %[2]s FROM %[3]s c
			  WHERE c.%[1]s IS NOT NULL AND c.%[1]s != ''
			    AND NOT EXISTS (SELECT 1 FROM %[4]s p WHERE p.uid = c.%[1]s)
			  ORDER BY c.id`, o.link, o.label, o.child, o.parent)
		rows, err := s.DB().QueryContext(ctx, q)
		if err != nil {
			r.Unreadable = append(r.Unreadable, TableError{Table: o.child + "." + o.link, Error: err.Error()})
			continue
		}
		if err := eachRow(rows, func(rows *sql.Rows) error {
			var id int64
			var uid string
			var label sql.NullString
			if err := rows.Scan(&id, &uid, &label); err != nil {
				return err
			}
			r.Findings = append(r.Findings, Finding{
				Class: ClassOrphan, Table: o.child, RowID: id, Label: strings.TrimSpace(label.String),
				Field: o.link, Value: uid, Detail: o.detail,
			})
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// --- class: suspect ----------------------------------------------------------

// scanSuspects reports links that resolve but do not add up. These are the
// re-adoptions the stable-uid migration froze in place: nothing distinguishes such
// a link from a correct one, because the child never recorded a uid and the deleted
// box left no tombstone. So this cannot prove anything, and both checks are
// deliberately conservative — each fires only when BOTH sides carry a usable value,
// so a blank or legacy row is never accused.
func scanSuspects(ctx context.Context, s *store.Store, r *Report) error {
	// (1) A find cannot have been acquired before the box it came out of was bought.
	// length()=10 keeps the comparison meaningful: yyyy-mm-dd sorts lexically, and a
	// date that is not that shape is the invalid class's business, not this one.
	err := query(ctx, s, `
		SELECT l.id, coalesce(t.name,'') , l.acquired, rt.date, rt.id
		  FROM lots l
		  JOIN roll_txns rt ON rt.uid = l.roll_txn_uid
		  LEFT JOIN item_type t ON t.id = l.item_type_id
		 WHERE l.activity = 'crh'
		   AND length(l.acquired) = 10 AND length(rt.date) = 10
		   AND l.acquired < rt.date
		 ORDER BY l.id`,
		func(rows *sql.Rows) error {
			var id, boxID int64
			var name, acquired, boxDate string
			if err := rows.Scan(&id, &name, &acquired, &boxDate, &boxID); err != nil {
				return err
			}
			r.Findings = append(r.Findings, Finding{
				Class: ClassSuspect, Table: "lots", RowID: id,
				Label: strings.TrimSpace(name), Field: "acquired", Value: acquired,
				Detail: fmt.Sprintf("this find is dated before box %d, which was bought on %s — a coin cannot come out of a box that had not been bought yet, so either the date is wrong or the find is attached to the wrong box", boxID, boxDate),
			})
			return nil
		})
	if err != nil {
		r.Unreadable = append(r.Unreadable, TableError{Table: "lots→roll_txns (date)", Error: err.Error()})
	}

	// (2) A keeper batch pulled from a box of quarters should be quarters. Case and
	// surrounding space are folded so "Dimes" vs "dimes" is not reported as a conflict.
	err = query(ctx, s, `
		SELECT k.id, k.denom, rt.denom, rt.id
		  FROM keepers k
		  JOIN roll_txns rt ON rt.uid = k.roll_txn_uid
		 WHERE trim(k.denom) != '' AND trim(rt.denom) != ''
		   AND lower(trim(k.denom)) != lower(trim(rt.denom))
		 ORDER BY k.id`,
		func(rows *sql.Rows) error {
			var id, boxID int64
			var denom, boxDenom string
			if err := rows.Scan(&id, &denom, &boxDenom, &boxID); err != nil {
				return err
			}
			r.Findings = append(r.Findings, Finding{
				Class: ClassSuspect, Table: "keepers", RowID: id,
				Label: denom, Field: "denom", Value: denom,
				Detail: fmt.Sprintf("this batch is attributed to box %d, which was a box of %s — a keeper pulled from that box should be the same denomination, so one of the two is likely the wrong row", boxID, boxDenom),
			})
			return nil
		})
	if err != nil {
		r.Unreadable = append(r.Unreadable, TableError{Table: "keepers→roll_txns (denom)", Error: err.Error()})
	}
	return nil
}

// --- plumbing ----------------------------------------------------------------

func query(ctx context.Context, s *store.Store, q string, fn func(*sql.Rows) error) error {
	rows, err := s.DB().QueryContext(ctx, q)
	if err != nil {
		return err
	}
	return eachRow(rows, fn)
}

// eachRow closes rows on every path, including the one where fn fails — a scan that
// leaks the pool's single connection would hang the app it was diagnosing.
func eachRow(rows *sql.Rows, fn func(*sql.Rows) error) error {
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
