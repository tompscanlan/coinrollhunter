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
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
      Serve the REST API + embedded web UI on localhost. The address must be a
      loopback one: the API has no password, so serving it to a network would hand
      your whole ledger to anyone who can reach the port. Pass --unsafe-network with
      a non-loopback --addr if you really mean it.
  coinrollhunter demo [--db demo.db] [--reset]
      Seed a separate database with ~15 months of fictional data and serve it —
      a full dashboard to explore before entering your own. Your real data is
      untouched; --reset regenerates the demo from scratch.
  coinrollhunter backup [--db crh.db] DEST/
      Write a complete, restorable backup into the directory DEST: the database
      plus your photo originals — everything needed to start a fresh instance.
      Safe to run while the app is open (a hand copy of crh.db can miss recent
      changes still in its -wal sidecar). Copy the whole folder to keep it all.
      DEST must be empty or new. (A DEST ending in .db is the OLD single-file
      form and is refused — it would silently omit your photos.)
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
		// The import is atomic (om-u3el): a failure wrote NOTHING, so the fix-and-rerun
		// loop is safe. Say so — and if the file has invalid rows, print EVERY one of
		// them, each naming its row and its field, so the user fixes the whole file in
		// one pass instead of rediscovering the next typo on every re-run. This is the
		// new-user on-ramp; it is the first thing the app ever says to them.
		var bad *legacy.ImportErrors
		if errors.As(err, &bad) {
			fmt.Fprintf(os.Stderr, "%s could not be imported — %d row(s) need fixing:\n\n",
				*holdingsPath+" / "+*crhPath, len(bad.Rows))
			for _, r := range bad.Rows {
				fmt.Fprintf(os.Stderr, "  %s\n      %s\n", r.Where, r.Err)
			}
			fmt.Fprintf(os.Stderr, "\nNothing was written to %s. Fix the rows above and run it again —\n"+
				"a failed import changes nothing, so re-running cannot duplicate anything.\n", *dbPath)
			return fmt.Errorf("%d invalid row(s); nothing was written", len(bad.Rows))
		}
		return fmt.Errorf("%w (nothing was written to %s — the import is atomic)", err, *dbPath)
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
	unsafeNetwork := fs.Bool("unsafe-network", false,
		"allow a non-loopback --addr. The API has no password: anyone who can reach that address can read and change your whole ledger")
	spotProvider := fs.String("spot-provider", envOr("CRH_SPOT_PROVIDER", "gold-api"),
		"spot price provider id, or 'none' to disable background polling (env CRH_SPOT_PROVIDER)")
	spotInterval := fs.Duration("spot-interval", envDur("CRH_SPOT_INTERVAL", 6*time.Hour),
		"spot poll cadence, e.g. 6h (env CRH_SPOT_INTERVAL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Before opening the database: a refused bind should not have touched anything.
	if err := checkAddr(*addr, *unsafeNetwork); err != nil {
		return err
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	return serveStore(s, serveOpts{
		dbPath:        *dbPath,
		addr:          *addr,
		unsafeNetwork: *unsafeNetwork,
		spotProvider:  *spotProvider,
		spotInterval:  *spotInterval,
	})
}

// runBackup writes a COMPLETE, RESTORABLE bundle into a directory: the database plus
// the photo originals, everything needed to start a fresh CoinRollHunter instance
// (om-6hlp, g/N1). The moment photos became files on disk, a bare-.db backup started
// silently omitting every image while telling the user it was "complete" — so the shape
// changed on purpose, and the old `backup DEST.db` form is now a HARD ERROR rather than a
// backup that quietly loses pictures.
//
// Distinct from `export`: backup is the machine-readable, restore-my-app artifact
// (db + originals); export is the human-readable, leave-with-my-data one (CSV/JSON +
// photos). Both carry the originals; the derivative cache is regenerable and in NEITHER.
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database (default: the same one the app uses)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: coinrollhunter backup [--db crh.db] DEST/  (a directory — db + your photos)")
	}
	dest := fs.Arg(0)

	// The breaking change, made loud: a `.db` argument is the OLD single-file form, which
	// would omit the user's photos. Refuse it with a message that says what to do instead,
	// rather than writing a backup that silently loses images.
	if strings.EqualFold(filepath.Ext(dest), ".db") {
		return fmt.Errorf("backup now writes a DIRECTORY (the database AND your photos), not a single .db file.\n"+
			"Give a directory path, e.g.  coinrollhunter backup %s\n"+
			"That folder holds crh.db plus a photos/ tree — copy the whole folder to keep everything.",
			strings.TrimSuffix(dest, filepath.Ext(dest)))
	}

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

	// Refuse a non-empty destination — the rule `export` keeps, for the reason it keeps
	// it: a backup command that can silently clobber the previous backup is a footgun in
	// the one place you least want one.
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return fmt.Errorf("backup: %s is not empty (refusing to overwrite what is already there)", dest)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: %s: %w", dest, err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("backup: %s: %w", dest, err)
	}

	// BackupFile, not Open+Backup: Open applies pending migrations, so backing up
	// through it would upgrade the database you were trying to snapshot *before*
	// upgrading it — exactly the backup you'd want if an upgrade went wrong.
	dbDest := filepath.Join(dest, "crh.db")
	if err := store.BackupFile(src, dbDest); err != nil {
		return err
	}

	// Copy the ORIGINALS tree (not the derivative cache — regenerable, and a separate
	// sibling dir, so copying photos/ excludes it by construction). Resolve symlinks so
	// the photos beside the REAL database are found (REUSE the export helpers).
	photoRoot := export.PhotoRoot(export.ResolveDBPath(src))
	photos, err := copyTree(photoRoot, filepath.Join(dest, "photos"))
	if err != nil {
		return fmt.Errorf("backup photos: %w", err)
	}

	fi, err := os.Stat(dbDest)
	if err != nil {
		return err
	}
	fmt.Printf("Backed up %s -> %s/  (crh.db %.1f MB + %d photo file(s))\n",
		src, dest, float64(fi.Size())/(1<<20), photos)
	fmt.Printf("The whole folder is the backup. Restore by pointing CoinRollHunter at %s.\n", dbDest)
	return nil
}

// photoCacheRoot is where a database's regenerable derivative cache lives: a sibling of
// the originals tree, NOT inside it (so a backup/export that copies photos/ never picks it
// up), gitignored, and rebuildable from the originals at any time (om-6hlp, R2). An
// in-memory/absent dbPath has no cache, so its root is "".
func photoCacheRoot(dbPath string) string {
	if dbPath == "" || dbPath == ":memory:" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), "photos-cache")
}

// copyTree copies every file under src into dst, recreating the directory structure. It
// is the backup's photo copier: originals only (src is the originals tree). A src that
// does not exist (no photos yet) is not an error — nothing to copy. Returns how many
// files were written.
func copyTree(src, dst string) (int, error) {
	if src == "" {
		return 0, nil
	}
	info, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil // no photos yet
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", src)
	}
	// os.Stat FOLLOWED a symlinked root (so IsDir passed), but filepath.WalkDir does NOT
	// follow symlinks — a symlinked photos/ root (the originals moved to another drive and
	// symlinked back) would be visited as one non-regular entry and skipped, and the backup
	// would silently copy ZERO files: a backup that lies. Resolve the root to its real
	// directory before walking. Symlinks WITHIN the tree stay skipped (below) — intended.
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		return 0, err
	}
	n := 0
	err = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks/devices — a backup copies real files only
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// copyFile copies one regular file's bytes to dst, refusing to overwrite (O_EXCL) — the
// destination was verified empty, so a collision means a concurrent writer, which is a
// loud error, not a silent clobber.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// runDemo seeds a separate demo database with the fictional dataset (only when
// it's empty, so demo edits survive restarts) and serves it. --reset deletes
// and regenerates. The user's real database is never touched.
func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	dbPath := fs.String("db", "demo.db", "path to the demo SQLite database (kept separate from your real data)")
	addr := fs.String("addr", "127.0.0.1:8787", "address to listen on (localhost only by default)")
	unsafeNetwork := fs.Bool("unsafe-network", false,
		"allow a non-loopback --addr. The API has no password: anyone who can reach that address can read and change the served ledger")
	reset := fs.Bool("reset", false, "delete the demo database and regenerate it from scratch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkAddr(*addr, *unsafeNetwork); err != nil {
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
		dbPath:        *dbPath,
		addr:          *addr,
		unsafeNetwork: *unsafeNetwork,
		spotProvider:  "none",
		spotInterval:  6 * time.Hour,
	})
}

// serveOpts configures serveStore. addr and listener are alternatives: the CLI
// paths name an address and let the server bind it; the double-click path binds
// first (so it can react to a port already in use) and hands the listener over.
type serveOpts struct {
	dbPath   string
	addr     string
	listener net.Listener
	// unsafeNetwork is the explicit "yes, serve this to the network" opt-in that a
	// non-loopback addr requires (om-6ex5). It also relaxes the guard's Host check to
	// the bound address — the app's own page has to be able to reach it.
	unsafeNetwork bool
	spotProvider  string
	spotInterval  time.Duration
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
		if err := checkAddr(o.addr, o.unsafeNetwork); err != nil {
			return err
		}
		var err error
		if ln, err = net.Listen("tcp", o.addr); err != nil {
			return err
		}
	}
	defer ln.Close()
	addr := ln.Addr().String()
	// What we actually bound, not what was asked for: the launch path hands over a
	// listener with no addr set, and an ephemeral :0 only becomes a real port here.
	// The guard pins to this.
	o.addr = addr
	if o.unsafeNetwork && !api.IsLoopbackAddr(addr) {
		fmt.Fprint(os.Stderr, unsafeNetworkWarning(addr))
	}

	// Quit from the UI. With no console there is no Ctrl-C, so a GUI user has no
	// other way to stop the server — it would just linger in Task Manager. The
	// route is wrapped here rather than added to internal/api: shutting the
	// process down is the command's business, not the API's.
	quit := make(chan struct{})
	var quitOnce sync.Once

	srv := &http.Server{
		Handler:           appHandler(s, func() { quitOnce.Do(func() { close(quit) }) }, o),
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

// appHandler is what the server serves: the API, plus the command's own POST /api/quit,
// wrapped in the loopback guard (om-6ex5).
//
// The guard goes HERE, around the OUTER mux, and that placement is the whole point: the
// quit route is registered in this package, not in api.Handler, so a guard applied inside
// the API package would leave the one endpoint that kills the process open to any webpage
// the user happens to have open. It is a separate function so the tests can drive exactly
// the handler that ships.
func appHandler(s *store.Store, onQuit func(), o serveOpts) http.Handler {
	mux := http.NewServeMux()
	// Photos live beside the database: originals under photos/, the regenerable
	// derivative cache under the sibling photos-cache/ (om-6hlp). Resolve symlinks first
	// (REUSE export.PhotoRoot/ResolveDBPath — the same helpers export uses, so the two
	// never disagree about where a photo is). An in-memory/absent dbPath yields "" and the
	// photo routes degrade to 404/500 rather than touching the filesystem.
	resolvedDB := export.ResolveDBPath(o.dbPath)
	photosDir := export.PhotoRoot(resolvedDB)
	cacheDir := photoCacheRoot(resolvedDB)
	mux.Handle("/", api.Handler(s, web.FS(), photosDir, cacheDir))
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		onQuit()
	})
	return api.Guard(mux, api.GuardOpts{UnsafeNetwork: o.unsafeNetwork, BoundAddr: o.addr})
}

// checkAddr refuses a non-loopback bind unless the user explicitly asked for one.
//
// A --addr on a real interface serves the ENTIRE unauthenticated API — the whole ledger,
// read AND write — to everyone who can reach the port, with nothing in the app objecting.
// Nothing in this repo has ever documented a LAN use case, so the default is no. The
// escape hatch stays because it is cheaper to keep than to re-add, and someone putting
// their own proxy in front of it knows what they are doing.
func checkAddr(addr string, unsafeNetwork bool) error {
	if unsafeNetwork || api.IsLoopbackAddr(addr) {
		return nil
	}
	return fmt.Errorf(`--addr %s would serve CoinRollHunter beyond this computer.
The API has no password. Anyone who can reach that address — everyone on the wifi, say —
could read your entire ledger and change or delete any of it.
Use a loopback address like 127.0.0.1:8787, or, if you really mean it (behind a proxy you
trust, on a network you trust), pass --unsafe-network as well.`, addr)
}

// unsafeNetworkWarning is what --unsafe-network prints once it has bound. It is loud on
// purpose: the flag is not a mode, it is a risk, and the person who typed it should see
// exactly what they just published.
func unsafeNetworkWarning(addr string) string {
	return "\n" +
		"  !!  --unsafe-network: CoinRollHunter is listening on " + addr + " — not just this computer.\n" +
		"  !!  There is NO password on this API. Anyone who can reach that address can read your\n" +
		"  !!  entire ledger, and can change or delete any of it (including recording your\n" +
		"  !!  holdings as sold). Only do this on a network you trust, behind a proxy you trust.\n\n"
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
