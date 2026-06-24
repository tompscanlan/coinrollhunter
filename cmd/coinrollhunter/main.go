// Command coinrollhunter is the single binary for the local-first coins &
// bullion tracker (ADR-001). Subcommands:
//
//	coinrollhunter migrate --holdings pm_holdings.json --crh crh_ledger.json [--db crh.db]
//	    import the prototype's JSON files into a SQLite database.
//
// More subcommands (serve) land as Phase 1 progresses.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/legacy"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

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
