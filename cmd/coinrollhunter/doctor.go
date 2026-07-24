package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tompscanlan/coinrollhunter/internal/doctor"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// runDoctor is the SECOND door onto the same scan the app's Data-health panel
// shows (internal/doctor). Nothing here is CLI-only — everything it reports is
// visible in the app — but this door earns its place twice over:
//
//   - Support. "Run `coinrollhunter doctor` and paste the output" is one line, and
//     what comes back names rows rather than describing symptoms.
//   - It still answers when the app does not. A single infinite value in a money
//     column makes GET /api/summary fail to encode, so the dashboard goes blank
//     with nothing to act on; a string in a REAL column kills every read of that
//     table. Those are exactly the moments the in-app surface cannot help.
//
// It reports and stops. There is no --fix and there should not be one: heuristic
// repair was proven to false-positive on correct data, and a false positive on a
// ledger is silent, unrecoverable money loss with no undo.
//
// It returns healthy=false when the scan completed but found something, so the
// exit code carries the answer too.
func runDoctor(args []string) (healthy bool, err error) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database (default: the same one the app uses)")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if fs.NArg() != 0 {
		return false, fmt.Errorf("usage: coinrollhunter doctor [--db crh.db] [--json]")
	}

	src := *dbPath
	if src == "" {
		if src, err = defaultDBPath(); err != nil {
			return false, err
		}
	}
	if _, err := os.Stat(src); err != nil {
		return false, fmt.Errorf("no database at %s", src)
	}

	// Read-only STRUCTURALLY, not by convention — the same discipline `export` uses
	// and for the same reason: store.Open applies pending migrations as a side effect,
	// so opening the user's file directly would WRITE to the very database we are
	// promising only to inspect. So scan a throwaway snapshot instead.
	//
	// store.BackupFile (VACUUM INTO) rather than a byte copy: this app runs a
	// background spot poller, so a copy taken mid-commit can be torn, and copying the
	// main file alone loses anything still in a -wal sidecar. It is safe to run while
	// the app is open, which matters — the user reaching for doctor usually has the
	// app in front of them, looking wrong.
	tmpDir, err := os.MkdirTemp("", "coinrollhunter-doctor")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmpDir)
	work := filepath.Join(tmpDir, "source-copy.db")
	if err := store.BackupFile(src, work); err != nil {
		return false, err
	}

	s, err := store.Open(work)
	if err != nil {
		// The snapshot could not even be migrated. That IS the finding, and it is the
		// one the user most needs stated plainly, because it means the app itself
		// cannot start on this file.
		return false, fmt.Errorf("this database could not be opened: %w\n"+
			"Your file was not touched — this was a throwaway copy. Restore your most recent\n"+
			"backup (the folder written by `coinrollhunter backup`) and use that instead", err)
	}
	defer s.Close()

	r, err := doctor.Scan(context.Background(), s)
	if err != nil {
		return false, err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return r.Healthy(), enc.Encode(r)
	}
	printReport(os.Stdout, src, r)
	return r.Healthy(), nil
}

// printReport writes the report for a human to read and to paste into a support
// thread. Grouped by class, because the three classes want three different
// reactions: an invalid row is wrong and should be fixed, an orphan is broken and
// may not be worth fixing, a suspect is a question.
func printReport(w *os.File, dbPath string, r *doctor.Report) {
	fmt.Fprintf(w, "CoinRollHunter health check\n%s\n\n", dbPath)

	if len(r.Unreadable) > 0 {
		// First, and unmissable: a scan that could not read a table has NOT cleared
		// that table, and presenting a finding count without this would imply it had.
		fmt.Fprintf(w, "COULD NOT READ %d of the tables — the rows in them were not checked at all:\n", len(r.Unreadable))
		for _, u := range r.Unreadable {
			fmt.Fprintf(w, "  %-24s %s\n", u.Table, u.Error)
		}
		fmt.Fprint(w, "\n  A table that will not load usually means one cell holds text where a number\n"+
			"  belongs. Everything in the app that reads that table is broken until it is fixed.\n\n")
	}

	if len(r.Findings) == 0 {
		if len(r.Unreadable) == 0 {
			fmt.Fprintf(w, "No problems found across %s.\n", describeScanned(r.Scanned))
		}
		return
	}

	fmt.Fprintf(w, "%d problem(s) across %s:\n", len(r.Findings), describeScanned(r.Scanned))
	for _, g := range []struct {
		class  doctor.Class
		header string
	}{
		{doctor.ClassInvalid, "INVALID — these rows cannot be true, and the totals are being computed from them anyway"},
		{doctor.ClassOrphan, "ORPHANED — these rows point at a box or a bank that no longer exists"},
		{doctor.ClassSuspect, "SUSPECT — these look wrong but might not be; they need your eyes, not a fix"},
	} {
		var group []doctor.Finding
		for _, f := range r.Findings {
			if f.Class == g.class {
				group = append(group, f)
			}
		}
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n\n", g.header)
		for _, f := range group {
			where := f.Table
			if f.RowID != 0 {
				where = fmt.Sprintf("%s #%d", f.Table, f.RowID)
			}
			fmt.Fprintf(w, "  %s", where)
			if f.Label != "" {
				fmt.Fprintf(w, "  %s", f.Label)
			}
			fmt.Fprintln(w)
			if f.Field != "" {
				fmt.Fprintf(w, "      %s = %s\n", f.Field, quoteValue(f.Value))
			}
			fmt.Fprintf(w, "      %s\n", f.Detail)
		}
	}

	fmt.Fprint(w, "\nNothing was changed. Every fix here is yours to make in the app's Edit tab —\n"+
		"this check will not guess, because guessing wrong on a ledger is worse than the bug.\n")
}

// quoteValue makes a blank or space-padded value visible. "acquired = " reads like
// a display bug; `acquired = ""` reads like the finding it is.
func quoteValue(v string) string {
	if v == "" || strings.TrimSpace(v) != v {
		return fmt.Sprintf("%q", v)
	}
	return v
}

// describeScanned turns the per-table counts into "1,204 row(s) in 8 tables". A
// scan that found nothing has to say how much it looked at, or "no problems found"
// is indistinguishable from "did not look". Only tables that were actually READ are
// counted here — the ones that failed are reported separately and far louder.
func describeScanned(scanned map[string]int) string {
	total := 0
	for _, n := range scanned {
		total += n
	}
	return fmt.Sprintf("%s row(s) in %d tables", withThousands(total), len(scanned))
}

// withThousands groups an integer for reading: 1204 -> "1,204".
func withThousands(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
