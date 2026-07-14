package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

func countTable(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// The behavioral half of the chokepoint proof (the AST guard is the mechanical
// half): every mutation, called with an invalid record, must reject it as an
// ErrInvalid AND leave the database untouched — no partial write, no branch forked
// as a side effect.
func TestEveryInsertRejectsInvalidAndWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		table string
		call  func(s *Store) error
	}{
		{"item_type bad metal", "item_type", func(s *Store) error {
			_, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "x", Metal: "bogus"})
			return err
		}},
		{"holding bad activity", "lots", func(s *Store) error {
			_, err := s.InsertHolding(model.Holding{ItemTypeID: 1, Activity: "bogus", Qty: 1, Acquired: "2026-01-01"})
			return err
		}},
		{"holding negative qty", "lots", func(s *Store) error {
			_, err := s.InsertHolding(model.Holding{ItemTypeID: 1, Activity: "crh", Qty: -5, Acquired: "2026-01-01"})
			return err
		}},
		{"rolltxn bad action", "roll_txns", func(s *Store) error {
			_, err := s.InsertRollTxn(model.RollTxn{Bank: "Ghost Bank", Action: "sell", FaceUSD: 1, Date: "2026-01-01"})
			return err
		}},
		{"trip negative miles", "trips", func(s *Store) error {
			_, err := s.InsertTrip(model.Trip{Bank: "Ghost Bank", Miles: -1, Date: "2026-01-01"})
			return err
		}},
		{"branch negative fee", "branches", func(s *Store) error {
			_, err := s.InsertBranch(model.Branch{Name: "x", CoinFeeUSD: -1})
			return err
		}},
		{"supply negative cost", "supplies", func(s *Store) error {
			_, err := s.InsertSupply(model.Supply{Item: "x", CostUSD: -1})
			return err
		}},
		{"loss negative amount", "losses", func(s *Store) error {
			_, err := s.InsertLoss(model.Loss{AmountUSD: -1, Date: "2026-01-01"})
			return err
		}},
		{"keeper negative count", "keepers", func(s *Store) error {
			_, err := s.InsertKeeper(model.Keeper{Denom: "halves", Count: -1})
			return err
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := openTestStore(t)
			before := countTable(t, s, c.table)
			branchesBefore := countTable(t, s, "branches")

			err := c.call(s)
			if !errors.Is(err, model.ErrInvalid) {
				t.Fatalf("got err %v, want a model.ErrInvalid", err)
			}
			if after := countTable(t, s, c.table); after != before {
				t.Errorf("%s row count changed %d -> %d: a rejected insert still wrote", c.table, before, after)
			}
			// The bank-name inserts must not have forked a branch as a side effect
			// (validation runs before resolveBranchID).
			if after := countTable(t, s, "branches"); after != branchesBefore {
				t.Errorf("a rejected insert forked a branch: branches %d -> %d", branchesBefore, after)
			}
		})
	}
}

// Update is the other unguarded door the review missed: a curl can poison an
// existing row exactly as easily as a new one. Every Update*/Put/Sell rejects an
// invalid record with ErrInvalid and leaves the stored row byte-identical.
func TestEveryUpdateRejectsInvalidAndDoesNotMutate(t *testing.T) {
	s := openTestStore(t)

	// Seed one valid row per table to update.
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Eagle", Metal: "gold", FineOzEach: 1})
	if err != nil {
		t.Fatal(err)
	}
	lotID, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "bullion", Qty: 2, BasisUSD: 100, Acquired: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	txnID, err := s.InsertRollTxn(model.RollTxn{Bank: "First National", Action: "buy", Denom: "halves", FaceUSD: 500, Date: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	tripID, err := s.InsertTrip(model.Trip{Bank: "First National", Miles: 6, Hours: 0.5, Date: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	branchID, err := s.InsertBranch(model.Branch{Name: "Solo Branch", CoinFeeUSD: 3})
	if err != nil {
		t.Fatal(err)
	}
	supplyID, err := s.InsertSupply(model.Supply{Item: "tubes", CostUSD: 8, Date: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	keeperID, err := s.InsertKeeper(model.Keeper{Denom: "halves", Count: 12, FaceUSD: 6})
	if err != nil {
		t.Fatal(err)
	}
	lossID, err := s.InsertLoss(model.Loss{AmountUSD: 5, Reason: "miscount", Date: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}

	updates := []struct {
		name string
		call func() error
	}{
		{"UpdateItemType bad metal", func() error {
			return s.UpdateItemType(typeID, model.ItemType{Kind: "coin", Name: "Eagle", Metal: "bogus"})
		}},
		{"UpdateHolding purity over 1", func() error {
			return s.UpdateHolding(lotID, model.Holding{ItemTypeID: typeID, Activity: "bullion", Qty: 2, Purity: 9.9, Acquired: "2026-01-01"})
		}},
		{"UpdateRollTxn bad action", func() error {
			return s.UpdateRollTxn(txnID, model.RollTxn{Bank: "First National", Action: "sell", FaceUSD: 500, Date: "2026-01-01"})
		}},
		{"UpdateTrip negative hours", func() error {
			return s.UpdateTrip(tripID, model.Trip{Bank: "First National", Miles: 6, Hours: -1, Date: "2026-01-01"})
		}},
		{"UpdateBranch negative cooldown", func() error {
			return s.UpdateBranch(branchID, model.Branch{Name: "Solo Branch", CooldownDays: -1})
		}},
		{"UpdateSupply negative cost", func() error {
			return s.UpdateSupply(supplyID, model.Supply{Item: "tubes", CostUSD: -1, Date: "2026-01-01"})
		}},
		{"UpdateKeeper bad date", func() error {
			return s.UpdateKeeper(keeperID, model.Keeper{Denom: "halves", Count: 12, FaceUSD: 6, Date: "nope"})
		}},
		{"UpdateLoss negative amount", func() error {
			return s.UpdateLoss(lossID, model.Loss{AmountUSD: -1, Date: "2026-01-01"})
		}},
	}

	for _, u := range updates {
		t.Run(u.name, func(t *testing.T) {
			if err := u.call(); !errors.Is(err, model.ErrInvalid) {
				t.Fatalf("got err %v, want a model.ErrInvalid", err)
			}
		})
	}

	// The seeded rows must be exactly as inserted — no rejected update leaked through.
	holdings, err := s.ListHoldings()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range holdings {
		if h.ID == lotID && (h.Purity != 0 || h.Qty != 2) {
			t.Errorf("holding was mutated by a rejected update: %+v", h)
		}
	}
	txns, _ := s.ListRollTxns()
	for _, tx := range txns {
		if tx.ID == txnID && tx.Action != "buy" {
			t.Errorf("roll_txn action mutated by a rejected update: %q", tx.Action)
		}
	}

	// SellHolding, PutSpot, PutSettings round out the 19.
	t.Run("SellHolding negative qty", func(t *testing.T) {
		if err := s.SellHolding(lotID, -1, 100, "2026-02-02"); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("got err %v, want model.ErrInvalid", err)
		}
		// The lot must not have been disposed.
		for _, h := range mustList(t, s) {
			if h.ID == lotID && h.Disposed != "" {
				t.Errorf("a rejected sale disposed the lot")
			}
		}
	})
	t.Run("PutSpot negative price", func(t *testing.T) {
		before := countTable(t, s, "spot")
		if err := s.PutSpot(model.Spot{AsOf: "2026-01-01", GoldUSD: -1}); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("got err %v, want model.ErrInvalid", err)
		}
		if after := countTable(t, s, "spot"); after != before {
			t.Errorf("a rejected PutSpot wrote a row: spot %d -> %d", before, after)
		}
	})
	t.Run("PutSettings negative rate", func(t *testing.T) {
		if err := s.PutSettings(model.Settings{HourlyRateUSD: -1}); !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("got err %v, want model.ErrInvalid", err)
		}
	})
}

func mustList(t *testing.T, s *Store) []model.Holding {
	t.Helper()
	hs, err := s.ListHoldings()
	if err != nil {
		t.Fatal(err)
	}
	return hs
}

// THE BRICK TEST (non-negotiable, and the whole reason this bead was rescoped to
// app-layer-only): the app must NEVER add a DB constraint that could make an
// existing user's database fail to open. This proves it stays true — a database
// carrying rows that violate the new app-layer rules, written by going AROUND the
// app with raw SQL, still opens, still lists, and the bad rows are still readable
// and deletable. If this ever fails, someone added a CHECK/trigger and bricked a
// user's ledger; do not "fix" it by deleting the test.
func TestExistingBadDataStillOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hasbadrows.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open fresh db: %v", err)
	}

	// Write rows that violate the new invariants, bypassing every Go check. If any of
	// these raw inserts is rejected, a DB-level constraint was added — exactly what
	// this bead forbids.
	if _, err := s.db.Exec(
		`INSERT INTO item_type (uid, kind, name, metal, fine_oz_each) VALUES
		 ('11111111-1111-4111-8111-111111111111','junk','Bad Type','bogus',-1)`); err != nil {
		t.Fatalf("raw item_type insert was rejected — a DB constraint was added: %v", err)
	}
	itemTypeID, _ := lastInsertID(t, s)
	if _, err := s.db.Exec(
		`INSERT INTO lots (uid, item_type_id, activity, qty, purity, basis_usd, acquired) VALUES
		 ('22222222-2222-4222-8222-222222222222', ?, 'bogus', -5, 9.9, -1, 'nope')`, itemTypeID); err != nil {
		t.Fatalf("raw lots insert was rejected — a DB constraint was added: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO roll_txns (uid, date, action, face_usd) VALUES
		 ('33333333-3333-4333-8333-333333333333','nope','bogus',-1)`); err != nil {
		t.Fatalf("raw roll_txns insert was rejected — a DB constraint was added: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The whole point: re-open a database that already holds violating rows. It must
	// migrate/open cleanly — every entry point calls Open, and a failure here is a
	// ledger that will not launch.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-opening a database with pre-existing bad rows FAILED — this is the bricking outcome the bead exists to prevent: %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	// The bad lot is still readable (List does not validate — only writes do).
	holdings, err := s2.ListHoldings()
	if err != nil {
		t.Fatalf("listing a db with bad rows failed: %v", err)
	}
	var bad *model.Holding
	for i := range holdings {
		if holdings[i].Activity == "bogus" {
			bad = &holdings[i]
		}
	}
	if bad == nil {
		t.Fatal("the pre-existing bad lot did not read back — data was lost")
	}
	if bad.Qty != -5 || bad.Purity != 9.9 {
		t.Errorf("bad lot did not round-trip its (invalid) values: %+v", bad)
	}

	// And a user must always be able to DELETE bad data (deletes never validate).
	if err := s2.DeleteHolding(bad.ID); err != nil {
		t.Errorf("could not delete a pre-existing bad row: %v", err)
	}
}

func lastInsertID(t *testing.T, s *Store) (int64, error) {
	t.Helper()
	var id int64
	err := s.db.QueryRow(`SELECT last_insert_rowid()`).Scan(&id)
	return id, err
}
