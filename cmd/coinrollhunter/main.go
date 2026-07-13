// Command coinrollhunter is the single binary for the local-first coins &
// bullion tracker (ADR-001). Subcommands:
//
//	coinrollhunter migrate --holdings pm_holdings.json --crh crh_ledger.json [--db crh.db]
//	    import the prototype's JSON files into a SQLite database.
//	coinrollhunter serve [--db crh.db] [--addr 127.0.0.1:8787]
//	    serve the REST API + embedded web UI on localhost.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/api"
	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/demo"
	"github.com/tompscanlan/coinrollhunter/internal/export"
	"github.com/tompscanlan/coinrollhunter/internal/legacy"
	"github.com/tompscanlan/coinrollhunter/internal/spot"
	"github.com/tompscanlan/coinrollhunter/internal/store"
	"github.com/tompscanlan/coinrollhunter/web"
)

// version is stamped at build time via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	// No arguments: this is a double-click, not a shell. Run the app rather than
	// printing usage to a console that is about to close (om-9p0l).
	if len(os.Args) < 2 {
		if err := runApp(); err != nil {
			showFatal("CoinRollHunter could not start", err.Error())
			os.Exit(1)
		}
		return
	}
	switch os.Args[1] {
	case "migrate":
		if err := runMigrate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	case "demo":
		if err := runDemo(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "demo:", err)
			os.Exit(1)
		}
	case "backup":
		// No "backup:" prefix here — store.Backup's errors already carry one, and
		// the user does not need to be told twice.
		if err := runBackup(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "export":
		// As with backup: export's errors already say "export:".
		if err := runExport(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "version", "-v", "--version":
		fmt.Printf("coinrollhunter %s\n", version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `coinrollhunter — local-first coins & bullion tracker

usage:
  coinrollhunter
      No arguments: start the app and open it in a browser. This is what a
      double-clicked binary does. The database lives in your user data
      directory, unless there is already a crh.db in the current directory.
  coinrollhunter migrate --holdings FILE --crh FILE [--db crh.db]
      Import the prototype JSON (pm_holdings.json + crh_ledger.json) into SQLite.
  coinrollhunter serve [--db crh.db] [--addr 127.0.0.1:8787]
      Serve the REST API + embedded web UI on localhost.
  coinrollhunter demo [--db demo.db] [--reset]
      Seed a separate database with ~15 months of fictional data and serve it —
      a full dashboard to explore before entering your own. Your real data is
      untouched; --reset regenerates the demo from scratch.
  coinrollhunter backup [--db crh.db] DEST.db
      Write a consistent snapshot of your data to a single file — safe to run
      while the app is open, and safe to copy anywhere. Copying crh.db by hand
      is not: recent changes may still live in its -wal sidecar.
  coinrollhunter export [--db crh.db] DIR
      Write everything you own into DIR as spreadsheets you can open anywhere:
      a CSV per table, a data.json that keeps the types, and your photos in a
      folder beside them. That is "leave with your data"; backup is "restore my
      app". DIR must be empty or new; export won't overwrite files already there,
      and if it fails partway it removes only the files it wrote.
  coinrollhunter version
      Print the build version.
`)
}

// runExport writes the data-export bundle to a plain directory: a CSV per table, a
// lossless data.json, a manifest, and the photo originals. Same bundle the UI's
// "Export my data" button downloads as a zip — this form exists so the photos are
// browsable in a file manager right beside the spreadsheet, with no dialog and no
// unpacking step.
func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database (default: the same one the app uses)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: coinrollhunter export [--db crh.db] DIR")
	}
	dest := fs.Arg(0)

	// Default to whatever the app itself would open, so `export` finds the user's real
	// data without them having to know where it lives.
	src := *dbPath
	if src == "" {
		var err error
		if src, err = defaultDBPath(); err != nil {
			return err
		}
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("no database at %s", src)
	}

	// Read-only over the user's data, structurally — not just by convention. store.Open
	// applies pending migrations as a side effect (store.go), so opening src directly
	// would UPGRADE an old snapshot (a v9 archive) to the latest schema on disk before
	// reading it. So export never opens the user's file as a database: it snapshots it to
	// a throwaway, migrates and reads the COPY, and discards it.
	//
	// The snapshot is store.BackupFile (VACUUM INTO), not a byte copy — the same call the
	// `backup` command runs against live databases. store.Backup's docstring spells out why
	// a plain copy is wrong: this app runs a background spot poller that writes with no user
	// action, so a byte copy taken mid-commit can be torn, and a copy of the main file alone
	// loses anything still in a -wal sidecar. VACUUM INTO takes a read transaction and writes
	// a self-contained file, and (verified) preserves user_version — so the copy migrates
	// v9 -> latest exactly as the source would have, and the source is only ever read.
	tmpDir, err := os.MkdirTemp("", "coinrollhunter-export")
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	work := filepath.Join(tmpDir, "source-copy.db")
	if err := store.BackupFile(src, work); err != nil {
		return err
	}

	s, err := store.Open(work)
	if err != nil {
		return err
	}
	defer s.Close()

	// The photo files live beside the user's REAL database (src), not beside the throwaway
	// copy — deriving the root from the copy's path would silently drop every photo. Resolve
	// symlinks first: if src is a link, the photos sit beside the real file it points at.
	if err := export.WriteDir(context.Background(), s, export.PhotoRoot(export.ResolveDBPath(src)), dest); err != nil {
		return err
	}
	fmt.Printf("Exported %s -> %s\n", src, dest)
	fmt.Println("A CSV per table, a data.json that keeps the types, your photos in photos/, and a manifest.")
	fmt.Println("Open the CSVs in any spreadsheet. Nothing here needs CoinRollHunter to read it.")
	fmt.Println("These are the only files written here; export won't touch anything else in the directory.")
	return nil
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	holdingsPath := fs.String("holdings", "", "path to pm_holdings.json")
	crhPath := fs.String("crh", "", "path to crh_ledger.json")
	dbPath := fs.String("db", "crh.db", "path to the SQLite database to create/update")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *holdingsPath == "" || *crhPath == "" {
		return fmt.Errorf("--holdings and --crh are required")
	}

	holdings, err := os.ReadFile(*holdingsPath)
	if err != nil {
		return fmt.Errorf("read holdings: %w", err)
	}
	crh, err := os.ReadFile(*crhPath)
	if err != nil {
		return fmt.Errorf("read crh: %w", err)
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	if err := legacy.Import(s, holdings, crh); err != nil {
		return err
	}

	// Report a quick summary so the user sees it worked.
	d, err := s.ResolveDataset()
	if err != nil {
		return err
	}
	r := calc.Compute(d)
	fmt.Printf("Imported into %s\n", *dbPath)
	fmt.Printf("  lots:          %d\n", len(d.Lots))
	fmt.Printf("  roll txns:     %d\n", len(d.RollTxns))
	fmt.Printf("  bullion P/L:   $%.2f\n", r.BullionUnreal)
	fmt.Printf("  CRH net (cash): $%.2f  -> %s\n", r.CRHNetReal, r.Verdict())
	fmt.Printf("  to redeposit:  $%.2f\n", r.ToRedeposit)
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "crh.db", "path to the SQLite database")
	addr := fs.String("addr", "127.0.0.1:8787", "address to listen on (localhost only by default)")
	spotProvider := fs.String("spot-provider", envOr("CRH_SPOT_PROVIDER", "gold-api"),
		"spot price provider id, or 'none' to disable background polling (env CRH_SPOT_PROVIDER)")
	spotInterval := fs.Duration("spot-interval", envDur("CRH_SPOT_INTERVAL", 6*time.Hour),
		"spot poll cadence, e.g. 6h (env CRH_SPOT_INTERVAL)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	return serveStore(s, serveOpts{
		dbPath:       *dbPath,
		addr:         *addr,
		spotProvider: *spotProvider,
		spotInterval: *spotInterval,
	})
}

// runBackup writes a consistent snapshot of the database to a single file, while
// the app is running. The point is that the result is safe to copy anywhere — onto
// a USB stick, into a sync folder, across to another machine — which a live crh.db
// is not: its recent commits may still be sitting in a -wal sidecar.
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database (default: the same one the app uses)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: coinrollhunter backup [--db crh.db] DEST.db")
	}
	dest := fs.Arg(0)

	// Default to whatever the app itself would open, so `backup` finds the user's
	// real data without them having to know where it lives.
	src := *dbPath
	if src == "" {
		var err error
		if src, err = defaultDBPath(); err != nil {
			return err
		}
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("no database at %s", src)
	}

	// BackupFile, not Open+Backup: Open applies pending migrations, so backing up
	// through it would upgrade the database you were trying to snapshot *before*
	// upgrading it — exactly the backup you'd want if an upgrade went wrong.
	if err := store.BackupFile(src, dest); err != nil {
		return err
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return err
	}
	fmt.Printf("Backed up %s -> %s (%.1f MB)\n", src, dest, float64(fi.Size())/(1<<20))
	fmt.Println("This is a complete, self-contained database — copy it anywhere.")
	return nil
}

// runDemo seeds a separate demo database with the fictional dataset (only when
// it's empty, so demo edits survive restarts) and serves it. --reset deletes
// and regenerates. The user's real database is never touched.
func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	dbPath := fs.String("db", "demo.db", "path to the demo SQLite database (kept separate from your real data)")
	addr := fs.String("addr", "127.0.0.1:8787", "address to listen on (localhost only by default)")
	reset := fs.Bool("reset", false, "delete the demo database and regenerate it from scratch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *reset {
		if err := os.Remove(*dbPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("reset %s: %w", *dbPath, err)
		}
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	var rows int
	if err := s.DB().QueryRow(`SELECT (SELECT count(*) FROM roll_txns) + (SELECT count(*) FROM lots)`).Scan(&rows); err != nil {
		return err
	}
	if rows > 0 {
		fmt.Printf("Demo database %s already seeded — serving it as-is (use --reset to regenerate).\n", *dbPath)
	} else {
		fmt.Printf("Seeding %s with ~15 months of fictional hunt + bullion data…\n", *dbPath)
		if err := demo.Seed(s, time.Now()); err != nil {
			return err
		}
		d, err := s.ResolveDataset()
		if err != nil {
			return err
		}
		r := calc.Compute(d)
		fmt.Printf("  lots: %d · roll txns: %d · face searched: $%.0f across %d buys\n",
			len(d.Lots)+len(d.Disposed), len(d.RollTxns), r.FaceSearched, int(r.BuyCount))
		fmt.Printf("  CRH net (cash): $%.2f · bullion P/L: $%.2f · to redeposit: $%.2f\n",
			r.CRHNetReal, r.BullionUnreal, r.ToRedeposit)
	}
	fmt.Println("Everything here is fictional — poke, edit, delete freely.")

	// Spot polling stays off: the demo ships its own price history, and keeping
	// it deterministic beats mixing in live quotes.
	return serveStore(s, serveOpts{
		dbPath:       *dbPath,
		addr:         *addr,
		spotProvider: "none",
		spotInterval: 6 * time.Hour,
	})
}

// serveOpts configures serveStore. addr and listener are alternatives: the CLI
// paths name an address and let the server bind it; the double-click path binds
// first (so it can react to a port already in use) and hands the listener over.
type serveOpts struct {
	dbPath       string
	addr         string
	listener     net.Listener
	spotProvider string
	spotInterval time.Duration
	// onReady, if set, is called with the UI's URL once the port is bound and
	// serving. The launch path opens the browser here rather than on a timer —
	// the listener is the only honest signal that the UI can answer.
	onReady func(url string)
}

// serveStore runs the HTTP server (REST API + embedded UI) over an open store
// until interrupted — shared by `serve`, `demo`, and the double-click path.
func serveStore(s *store.Store, o serveOpts) error {
	ln := o.listener
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", o.addr); err != nil {
			return err
		}
	}
	defer ln.Close()
	addr := ln.Addr().String()

	// Quit from the UI. With no console there is no Ctrl-C, so a GUI user has no
	// other way to stop the server — it would just linger in Task Manager. The
	// route is wrapped here rather than added to internal/api: shutting the
	// process down is the command's business, not the API's.
	quit := make(chan struct{})
	var quitOnce sync.Once
	mux := http.NewServeMux()
	mux.Handle("/", api.Handler(s, web.FS()))
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		quitOnce.Do(func() { close(quit) })
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown on Ctrl-C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Background spot polling (ADR-007): keep valuations fresh while we run. Opt out with
	// --spot-provider=none; any fetch failure is logged and skipped (manual entry remains).
	if !spot.Disabled(o.spotProvider) {
		if prov, err := spot.ByName(o.spotProvider, nil); err != nil {
			fmt.Fprintln(os.Stderr, "spot:", err, "— polling disabled")
		} else {
			go spot.NewPoller(prov, s, o.spotInterval).Run(ctx)
		}
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	url := "http://" + addr
	fmt.Printf("coinrollhunter serving on %s  (db: %s)\n", url, o.dbPath)
	if o.onReady != nil {
		o.onReady(url)
	}

	shutdown := func() error {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-quit:
		fmt.Println("quit requested — shutting down…")
		return shutdown()
	case <-ctx.Done():
		fmt.Println("\nshutting down…")
		return shutdown()
	}
}

// envOr returns the environment value for key, or def if it is unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDur parses a duration (e.g. "6h") from the environment, falling back to def.
func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
