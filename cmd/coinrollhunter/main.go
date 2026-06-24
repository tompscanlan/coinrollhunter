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
	"github.com/tompscanlan/coinrollhunter/internal/legacy"
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
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Handler(s, web.FS()),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown on Ctrl-C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		fmt.Printf("coinrollhunter serving on http://%s  (db: %s)\n", *addr, *dbPath)
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
