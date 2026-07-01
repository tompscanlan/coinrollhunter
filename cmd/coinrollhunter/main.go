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
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/api"
	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/demo"
	"github.com/tompscanlan/coinrollhunter/internal/legacy"
	"github.com/tompscanlan/coinrollhunter/internal/spot"
	"github.com/tompscanlan/coinrollhunter/internal/store"
	"github.com/tompscanlan/coinrollhunter/web"
)

// version is stamped at build time via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
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
  coinrollhunter migrate --holdings FILE --crh FILE [--db crh.db]
      Import the prototype JSON (pm_holdings.json + crh_ledger.json) into SQLite.
  coinrollhunter serve [--db crh.db] [--addr 127.0.0.1:8787]
      Serve the REST API + embedded web UI on localhost.
  coinrollhunter demo [--db demo.db] [--reset]
      Seed a separate database with ~15 months of fictional data and serve it —
      a full dashboard to explore before entering your own. Your real data is
      untouched; --reset regenerates the demo from scratch.
  coinrollhunter version
      Print the build version.
`)
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

	return serveStore(s, *dbPath, *addr, *spotProvider, *spotInterval)
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
	return serveStore(s, *dbPath, *addr, "none", 6*time.Hour)
}

// serveStore runs the HTTP server (REST API + embedded UI) over an open store
// until interrupted — shared by `serve` and `demo`.
func serveStore(s *store.Store, dbPath, addr, spotProvider string, spotInterval time.Duration) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(s, web.FS()),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown on Ctrl-C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Background spot polling (ADR-007): keep valuations fresh while we run. Opt out with
	// --spot-provider=none; any fetch failure is logged and skipped (manual entry remains).
	if !spot.Disabled(spotProvider) {
		if prov, err := spot.ByName(spotProvider, nil); err != nil {
			fmt.Fprintln(os.Stderr, "spot:", err, "— polling disabled")
		} else {
			go spot.NewPoller(prov, s, spotInterval).Run(ctx)
		}
	}

	errc := make(chan error, 1)
	go func() {
		fmt.Printf("coinrollhunter serving on http://%s  (db: %s)\n", addr, dbPath)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		fmt.Println("\nshutting down…")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
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
