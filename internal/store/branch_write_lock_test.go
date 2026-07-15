package store

import (
	"sync"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// orphanAliases counts branch_aliases rows whose branch_id no longer names a live
// branch. Such a row is the om-bz89 hazard made concrete: a name that still resolves
// (name -> alias -> branch_id) onto a rowid whose branch is gone, and which a later
// recycled rowid can silently re-adopt (the om-c8ei wrong-parent adoption).
func orphanAliases(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	err := s.db.QueryRow(
		`SELECT count(*) FROM branch_aliases a
		   WHERE NOT EXISTS (SELECT 1 FROM branches b WHERE b.id = a.branch_id)`).Scan(&n)
	if err != nil {
		t.Fatalf("count orphan aliases: %v", err)
	}
	return n
}

// TestUpdateBranchDeleteBranchNoOrphanAlias (AC4) drives UpdateBranch and DeleteBranch
// on the same branch id concurrently and asserts no orphan alias ever survives.
//
// It exercises the atomicity the fix provides. Before the fix UpdateBranch ran its
// UPDATE and its alias INSERT as two SEPARATE auto-commit statements with the store
// write lock taken by NEITHER method, so — even under SetMaxOpenConns(1), which only
// serializes single statements — a concurrent DeleteBranch could commit its whole
// transaction (delete aliases + delete branch) in the gap between UpdateBranch's two
// statements, and UpdateBranch's INSERT then wrote an alias onto a branch that was
// already gone. That interleaving leaves orphanAliases() > 0, so a version WITHOUT the
// fix can (and, across these rounds, does) fail this test. After the fix UpdateBranch's
// two statements share one transaction (the tx holds the sole connection across both,
// so nothing interleaves) and DeleteBranch takes the write lock, so the two are
// serialized and the orphan is impossible in EITHER order:
//   - update-then-delete: the delete removes the branch and every alias -> none orphaned;
//   - delete-then-update: the UPDATE matches 0 rows -> ErrNotFound -> rollback, no INSERT.
//
// Run under -race (make check / the recipe runs the whole package with -race). A fresh
// in-memory store per round keeps the rounds independent and re-creates the recyclable
// rowid each time, mirroring the real hazard.
func TestUpdateBranchDeleteBranchNoOrphanAlias(t *testing.T) {
	// The pre-fix window is wide — the unfixed tree orphans an alias within single-digit
	// rounds — so 100 keeps ample regression-detection margin without taxing every -race
	// `make check` run (400 rounds spun up 400 fresh migrated :memory: stores, ~115s).
	const rounds = 100
	for i := 0; i < rounds; i++ {
		s, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		id, err := s.InsertBranch(model.Branch{Name: "Original", Buys: true, Dumps: true, Active: true})
		if err != nil {
			s.Close()
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			// ErrNotFound is expected when the delete wins the race — not a failure.
			_ = s.UpdateBranch(id, model.Branch{Name: "Renamed", Buys: true, Dumps: true, Active: true})
		}()
		go func() {
			defer wg.Done()
			_ = s.DeleteBranch(id)
		}()
		wg.Wait()

		if n := orphanAliases(t, s); n != 0 {
			s.Close()
			t.Fatalf("round %d: %d orphan alias row(s) survived — UpdateBranch inserted an "+
				"alias after DeleteBranch removed the branch (the two are not atomic/serialized)", i, n)
		}
		s.Close()
	}
}

// TestMergeIntoNonexistentSurvivorMutatesNothing folds om-h9bn's regression guard into
// this fire. MergeBranches resolves the survivor's uid as its FIRST tx statement
// (SELECT uid FROM branches WHERE id=?), so a survivor that does not exist yields
// sql.ErrNoRows and the deferred Rollback fires before any UPDATE/DELETE runs. This is
// already true incidentally (om-c8ei's uid rewrite forced the existence check as a side
// effect); the test pins it so a future refactor of MergeBranches cannot silently
// reintroduce the original om-h9bn destruction — merging into a nonexistent id
// repointing a real branch's whole history onto a dangling id and deleting the valid row.
func TestMergeIntoNonexistentSurvivorMutatesNothing(t *testing.T) {
	s := openTestStore(t)

	// A real loser branch with history: one buy + one trip fork "Riverbend CU".
	if _, err := s.InsertRollTxn(model.RollTxn{Date: "2026-01-01", Bank: "Riverbend CU", Action: "buy", Denom: "halves", FaceUSD: 500}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTrip(model.Trip{Date: "2026-01-02", Bank: "Riverbend CU", Miles: 8}); err != nil {
		t.Fatal(err)
	}
	branches, err := s.ListBranches()
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 {
		t.Fatalf("want 1 branch, got %d", len(branches))
	}
	loser := branches[0]

	// Snapshot everything the merge could touch.
	type snap struct{ branches, aliases, rtOnLoser, tripsOnLoser int }
	take := func() snap {
		return snap{
			branches:     count(t, s.db, `SELECT count(*) FROM branches`),
			aliases:      count(t, s.db, `SELECT count(*) FROM branch_aliases`),
			rtOnLoser:    count(t, s.db, `SELECT count(*) FROM roll_txns WHERE branch_uid='`+loser.UID+`'`),
			tripsOnLoser: count(t, s.db, `SELECT count(*) FROM trips WHERE branch_uid='`+loser.UID+`'`),
		}
	}
	before := take()

	// 999999 is not a live branch id (ids start at 1). The merge must error and mutate nothing.
	const nonexistentSurvivor = 999999
	if err := s.MergeBranches(nonexistentSurvivor, []int64{loser.ID}); err == nil {
		t.Fatal("merge into a nonexistent survivor should return an error, got nil")
	}

	after := take()
	if before != after {
		t.Errorf("merge into a nonexistent survivor mutated state: before=%+v after=%+v", before, after)
	}
	// The loser branch — with its whole history — must still be present and intact.
	var present int
	if err := s.db.QueryRow(`SELECT count(*) FROM branches WHERE id=?`, loser.ID).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present != 1 {
		t.Errorf("loser branch id=%d present count = %d, want 1 (its row was destroyed)", loser.ID, present)
	}
	if got, _ := resolveBranchID(s.db, "Riverbend CU"); got != loser.ID {
		t.Errorf("loser's name resolves to %d, want the intact loser %d", got, loser.ID)
	}
}
