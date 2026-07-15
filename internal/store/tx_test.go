package store

import (
	"errors"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// The transaction surface (om-u3el). WithWrite is a MUTEX, not a transaction — it has
// no Begin/Commit/Rollback and rolls nothing back. WithTx is the real thing, and it is
// what the legacy importer (and, next, the UI's compound workflows) writes through.

func txStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestWithTxRollsBackEverything: an error returned from the callback undoes every row
// the callback wrote — including the branch + alias that resolveBranchID forks from a
// typed bank name, which is the store's hidden writer and used to survive as an orphan.
func TestWithTxRollsBackEverything(t *testing.T) {
	s := txStore(t)

	boom := errors.New("boom") // any failure at all: a DB error, a disk-full, a caller's own guard
	err := s.WithTx(func(tx *Tx) error {
		typeID, err := tx.InsertItemType(model.ItemType{Kind: "coin", Name: "Eagle", Metal: "gold", FineOzEach: 1})
		if err != nil {
			return err
		}
		// The lot points at the item_type inserted a statement ago, INSIDE this
		// transaction: an uncommitted row is already visible to its own tx, and the
		// FK holds against it.
		if _, err := tx.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "bullion", Qty: 1, Acquired: "2025-01-01"}); err != nil {
			return err
		}
		// A bank name nobody has typed before: forks a branch AND a branch_alias.
		if _, err := tx.InsertRollTxn(model.RollTxn{Date: "2025-01-02", Bank: "Ghost Bank", Action: "buy", FaceUSD: 500}); err != nil {
			return err
		}
		if _, err := tx.InsertTrip(model.Trip{Date: "2025-01-02", Bank: "Ghost Bank", Miles: 4}); err != nil {
			return err
		}
		if _, err := tx.InsertSupply(model.Supply{Date: "2025-01-02", Item: "tubes", CostUSD: 8}); err != nil {
			return err
		}
		if _, err := tx.InsertKeeper(model.Keeper{Denom: "halves", Count: 2, FaceUSD: 1}); err != nil {
			return err
		}
		if _, err := tx.InsertLoss(model.Loss{Date: "2025-01-02", AmountUSD: 1, Reason: "spill"}); err != nil {
			return err
		}
		if err := tx.PutSpot(model.Spot{AsOf: "2025-01-02", GoldUSD: 4000, SilverUSD: 60}); err != nil {
			return err
		}
		if err := tx.PutSettings(model.DefaultSettings()); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx returned %v, want the callback's error", err)
	}

	for _, table := range []string{
		"item_type", "lots", "roll_txns", "trips", "supplies", "keepers", "losses",
		"branches", "branch_aliases", "spot", "settings",
	} {
		if n := countTable(t, s, table); n != 0 {
			t.Errorf("%s: %d rows after a rolled-back transaction, want 0", table, n)
		}
	}
}

// TestWithTxCommits: the happy path actually persists, and the tx-bound writers land
// the same rows their auto-commit *Store twins would.
func TestWithTxCommits(t *testing.T) {
	s := txStore(t)

	err := s.WithTx(func(tx *Tx) error {
		id, err := tx.InsertItemType(model.ItemType{Kind: "coin", Name: "Eagle", Metal: "gold", FineOzEach: 1})
		if err != nil {
			return err
		}
		if _, err := tx.InsertHolding(model.Holding{ItemTypeID: id, Activity: "bullion", Qty: 2, Acquired: "2025-01-01"}); err != nil {
			return err
		}
		_, err = tx.InsertRollTxn(model.RollTxn{Date: "2025-01-02", Bank: "First Federal", Action: "buy", FaceUSD: 500})
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	for table, want := range map[string]int{
		"item_type": 1, "lots": 1, "roll_txns": 1, "branches": 1, "branch_aliases": 1,
	} {
		if n := countTable(t, s, table); n != want {
			t.Errorf("%s = %d rows, want %d", table, n, want)
		}
	}
	// The typed bank resolved to a real branch, and the txn points at it — by its stable
	// branch_uid now (om-c8ei), not the recyclable branch_id (dropped in 0011).
	var buid string
	if err := s.db.QueryRow(`SELECT branch_uid FROM roll_txns`).Scan(&buid); err != nil {
		t.Fatal(err)
	}
	if buid == "" {
		t.Error("roll_txn has no branch_uid — resolveBranchUID did not fork the branch inside the tx")
	}
}

// TestTxMutationsValidate: the transaction-bound writers are NOT a second, unvalidated
// door into the ledger. Every one of them runs the same model rules as its *Store twin —
// the AST guard (validate_ast_test.go) enforces that they call a validator at all; this
// pins that the call actually rejects, and that a rejected row leaves nothing behind.
func TestTxMutationsValidate(t *testing.T) {
	s := txStore(t)

	cases := map[string]func(*Tx) error{
		"item_type: capitalized metal": func(tx *Tx) error {
			_, err := tx.InsertItemType(model.ItemType{Name: "dimes", Metal: "Silver"})
			return err
		},
		"holding: unknown activity": func(tx *Tx) error {
			_, err := tx.InsertHolding(model.Holding{Activity: "hoard", Qty: 1, Acquired: "2025-01-01"})
			return err
		},
		"holding: blank acquired": func(tx *Tx) error {
			_, err := tx.InsertHolding(model.Holding{Activity: "bullion", Qty: 1})
			return err
		},
		"roll_txn: unknown action": func(tx *Tx) error {
			_, err := tx.InsertRollTxn(model.RollTxn{Date: "2025-01-02", Bank: "Ghost Bank", Action: "purchase"})
			return err
		},
		"trip: unparseable date": func(tx *Tx) error {
			_, err := tx.InsertTrip(model.Trip{Date: "Jan 2", Bank: "Ghost Bank"})
			return err
		},
		"branch: negative fee": func(tx *Tx) error {
			_, err := tx.InsertBranch(model.Branch{Name: "Ghost Bank", CoinFeeUSD: -1})
			return err
		},
		"supply: negative cost": func(tx *Tx) error {
			_, err := tx.InsertSupply(model.Supply{Date: "2025-01-02", Item: "tubes", CostUSD: -8})
			return err
		},
		"loss: negative amount": func(tx *Tx) error {
			_, err := tx.InsertLoss(model.Loss{Date: "2025-01-02", AmountUSD: -1})
			return err
		},
		"keeper: negative count": func(tx *Tx) error {
			_, err := tx.InsertKeeper(model.Keeper{Denom: "halves", Count: -2})
			return err
		},
		"spot: negative price": func(tx *Tx) error {
			return tx.PutSpot(model.Spot{AsOf: "2025-01-02", GoldUSD: -1})
		},
		"settings: negative hourly rate": func(tx *Tx) error {
			cfg := model.DefaultSettings()
			cfg.HourlyRateUSD = -5
			return tx.PutSettings(cfg)
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.WithTx(mutate)
			if err == nil {
				t.Fatal("the tx writer accepted an invalid row — a second, UNVALIDATED door into the ledger")
			}
			if !errors.Is(err, model.ErrInvalid) {
				t.Errorf("error %v does not unwrap to model.ErrInvalid", err)
			}
		})
	}

	// A rejected row also forks no branch on its way out (validation runs before
	// resolveBranchID), and rolls back regardless.
	for _, table := range []string{"item_type", "lots", "roll_txns", "trips", "supplies", "keepers", "losses", "branches", "spot"} {
		if n := countTable(t, s, table); n != 0 {
			t.Errorf("%s: %d rows after only-rejected transactions, want 0", table, n)
		}
	}
}

// TestStoreInsertsStillAutoCommit: the eight public *Store inserts kept their names,
// their signatures AND their auto-commit behavior — the API, the demo seeder and the
// spot poller all still write through them, outside any transaction.
func TestStoreInsertsStillAutoCommit(t *testing.T) {
	s := txStore(t)

	id, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Eagle", Metal: "gold", FineOzEach: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertHolding(model.Holding{ItemTypeID: id, Activity: "bullion", Qty: 1, Acquired: "2025-01-01"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRollTxn(model.RollTxn{Date: "2025-01-02", Bank: "Auto Bank", Action: "buy", FaceUSD: 100}); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"item_type": 1, "lots": 1, "roll_txns": 1, "branches": 1} {
		if n := countTable(t, s, table); n != want {
			t.Errorf("%s = %d rows, want %d", table, n, want)
		}
	}
	// And a WithTx afterwards still works — the lock and the connection were returned.
	if err := s.WithTx(func(tx *Tx) error {
		_, err := tx.InsertSupply(model.Supply{Date: "2025-01-03", Item: "flips", CostUSD: 2})
		return err
	}); err != nil {
		t.Fatalf("WithTx after auto-commit inserts: %v", err)
	}
	if n := countTable(t, s, "supplies"); n != 1 {
		t.Errorf("supplies = %d, want 1", n)
	}
}
