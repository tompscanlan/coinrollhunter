package store

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// om-5psc: the kept flag on a CRH find. These pin migration 0012's promises —
// ADDITIVE ONLY, touches zero keeper rows, repairs nothing — plus the two silent
// landmines the guards do NOT catch (the partial-sale carve-out; the migration
// never mutating a keeper).

// AC3 + AC4 — migration 0012 is additive and touches NO existing row. A database
// seeded at schema 0011 that ALREADY contains the duplicate (a crh find lot AND a
// keeper batch for the same box on the same date), a genuinely clad-only keeper,
// and a LEGACY keeper (NULL date, NULL box) is opened with the new binary so 0012
// applies. Nothing is repaired: every keeper row survives byte-for-byte, every
// pre-existing lot reads kept=0, the float numbers are unchanged, and the DB opens.
func TestMigration0012AdditiveTouchesNoKeeper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "at0011.db")
	raw, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)

	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 11 { // seed at the schema JUST BEFORE this bead's 0012
			continue
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
	}
	mustExec(t, raw, `PRAGMA user_version = 11`)

	// A catalog row for the find to point at.
	mustExec(t, raw, `INSERT INTO item_type (uid, kind, name, metal) VALUES ('it-uid','junk','90% half','silver')`)
	// The box, its return, and (at 0011) links ride roll_txn_uid, not the dropped integer.
	mustExec(t, raw, `INSERT INTO roll_txns (id, uid, date, action, denom, face_usd, branch_uid) VALUES (1, 'box1', '2026-06-01','buy','halves',500.00, NULL)`)
	mustExec(t, raw, `INSERT INTO roll_txns (id, uid, date, action, face_usd, branch_uid) VALUES (2, 'ret1', '2026-06-02','return',499.50, NULL)`)
	// The duplicate: a crh find lot on box1 (unflagged — it predates the flag).
	mustExec(t, raw, `INSERT INTO lots (uid, item_type_id, roll_txn_uid, activity, qty, basis_usd, face_value_usd, acquired) VALUES ('findA', 1, 'box1', 'crh', 1, 0.50, 0.50, '2026-06-01')`)
	// ...AND the keeper batch for the SAME coin, same box, same day — the double-count.
	mustExec(t, raw, `INSERT INTO keepers (denom, count, face_usd, date, roll_txn_uid) VALUES ('halves', 1, 0.50, '2026-06-01', 'box1')`)
	// A genuinely clad-only keeper: a real bulk batch with no matching find.
	mustExec(t, raw, `INSERT INTO keepers (denom, count, face_usd, date, roll_txn_uid) VALUES ('quarters', 20, 5.00, '2026-06-05', 'box1')`)
	// A LEGACY keeper as internal/legacy and demo produce it: NULL date, NULL box.
	mustExec(t, raw, `INSERT INTO keepers (denom, count, face_usd, date, roll_txn_uid) VALUES ('dimes', 40, 4.00, NULL, NULL)`)

	// Snapshot the keepers table BEFORE 0012 — the exact bytes the migration must not touch.
	var beforeN, beforeCount int64
	var beforeFace float64
	if err := raw.QueryRow(`SELECT count(*), coalesce(sum(count),0), coalesce(sum(face_usd),0) FROM keepers`).
		Scan(&beforeN, &beforeCount, &beforeFace); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// (a) THE DB OPENS — no brick. Open applies every migration past the seeded v11: 0012
	// (this bead's additive kept ALTER) AND 0013 (om-6hlp's additive photo inactive flag),
	// both plain additive ALTERs, so the head is now 13.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open through 0013: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if v, _ := s.Version(); v != 13 {
		t.Errorf("user_version = %d, want 13 (head after 0012 kept + 0013 photo flag)", v)
	}

	// (b) THE KEEPERS TABLE IS BYTE-FOR-BYTE UNCHANGED — not one row deleted, not one
	// face_usd or count altered. This is the whole safety claim of an additive migration.
	var afterN, afterCount int64
	var afterFace float64
	if err := s.db.QueryRow(`SELECT count(*), coalesce(sum(count),0), coalesce(sum(face_usd),0) FROM keepers`).
		Scan(&afterN, &afterCount, &afterFace); err != nil {
		t.Fatal(err)
	}
	if afterN != beforeN || afterCount != beforeCount || math.Abs(afterFace-beforeFace) > 1e-9 {
		t.Errorf("0012 mutated keepers: before (rows %d, count %d, face %.2f) != after (rows %d, count %d, face %.2f)",
			beforeN, beforeCount, beforeFace, afterN, afterCount, afterFace)
	}

	// (c) EVERY PRE-EXISTING LOT READS kept=0. The migration repairs nothing; it only
	// defaults the new column.
	var flagged int
	if err := s.db.QueryRow(`SELECT count(*) FROM lots WHERE kept != 0`).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if flagged != 0 {
		t.Errorf("%d pre-existing lots came out of 0012 with kept != 0 — the migration must default it, not set it", flagged)
	}

	// (d) AC4 — the clad-only AND the legacy keeper round-trip untouched through ListKeepers.
	ks, err := s.ListKeepers()
	if err != nil {
		t.Fatal(err)
	}
	byDenom := map[string]model.Keeper{}
	for _, k := range ks {
		byDenom[k.Denom] = k
	}
	if q := byDenom["quarters"]; q.Count != 20 || math.Abs(q.FaceUSD-5.00) > 1e-9 || q.Date != "2026-06-05" {
		t.Errorf("clad-only keeper changed: %+v, want count 20 / face 5.00 / date 2026-06-05", q)
	}
	if d := byDenom["dimes"]; d.Count != 40 || math.Abs(d.FaceUSD-4.00) > 1e-9 || d.Date != "" || d.RollTxnID != 0 {
		t.Errorf("legacy keeper changed: %+v, want count 40 / face 4.00 / NULL date / no box", d)
	}

	// (e) THE FLOAT NUMBERS ARE UNMOVED. The migration repairs the double-count NOT AT
	// ALL, so kept_face stays inflated and to_redeposit stays negative — today's bug,
	// preserved. cladFace = 0.50+5.00+4.00 = 9.50; keptFindFace = the find's 0.50; so
	// kept_face = 10.00 and to_redeposit = 500 - 499.50 - 10.00 = -9.50, exactly as
	// before the migration (the inputs are untouched, proven above).
	r := calc.Compute(mustResolve(t, s))
	approxKept(t, "clad_face (all three keepers, untouched)", r.CladFace, 9.50)
	approxKept(t, "kept_face (still double-counts — 0012 repairs nothing)", r.KeptFace, 10.00)
	approxKept(t, "to_redeposit (still negative — the pre-existing bug survives)", r.ToRedeposit, -9.50)
}

// AC5 (grep companion, in code) — the migration file itself carries NO DELETE and NO
// UPDATE, so no boot/migration path can mutate a keeper. Pinned here as a live read of
// the embedded SQL so a future edit that adds one fails the suite, not just a grep.
func TestMigration0012SQLIsAdditiveOnly(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var sqlText string
	for _, m := range ms {
		if m.version == 12 {
			sqlText = m.sql
		}
	}
	if sqlText == "" {
		t.Fatal("migration 0012 not found")
	}
	low := toLowerASCII(sqlText)
	for _, banned := range []string{"delete", "update", "drop"} {
		if containsWord(low, banned) {
			t.Errorf("migration 0012 contains a %q statement — it must be additive-only (ADD COLUMN)", banned)
		}
	}
	if !containsWord(low, "alter") || !containsWord(low, "add") {
		t.Error("migration 0012 is not the additive ALTER TABLE ... ADD COLUMN it must be")
	}
}

// AC10 + LANDMINE 1 — a PARTIAL sale of a kept find leaves BOTH the retained lot and
// the carved-out disposed lot carrying kept=1. SellHolding hand-enumerates every lots
// column into the carve-out INSERT; drop kept there and a partially-sold kept find
// silently un-keeps itself, with no guard to catch it (the om-hdk5 trap).
func TestPartialSaleOfKeptFindKeepsTheFlag(t *testing.T) {
	s := openTestStore(t)

	typeID, err := s.InsertItemType(model.ItemType{Kind: "junk", Name: "90% half", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	// A kept find of qty 2. Sell one; the lot splits into a retained qty-1 lot and a
	// disposed qty-1 carve-out. Both are the same physical kind of coin — both kept.
	id, err := s.InsertHolding(model.Holding{
		ItemTypeID: typeID, Activity: "crh", Qty: 2, BasisUSD: 1.00, FaceValueUSD: 1.00,
		Acquired: "2026-06-01", Kept: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SellHolding(id, 1, 12.00, "2026-06-10"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.Query(`SELECT kept, disposed FROM lots WHERE item_type_id=? ORDER BY id`, typeID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var kept int64
		var disposed sql.NullString
		if err := rows.Scan(&kept, &disposed); err != nil {
			t.Fatal(err)
		}
		n++
		which := "retained"
		if disposed.Valid && disposed.String != "" {
			which = "carved-out (disposed)"
		}
		if kept != 1 {
			t.Errorf("%s row after a partial sale has kept=%d, want 1 — a kept find un-kept itself in the carve-out", which, kept)
		}
	}
	if n != 2 {
		t.Fatalf("partial sale produced %d lots, want 2 (a retained + a carved-out row)", n)
	}
}

// The mirror: an UNkept find stays kept=0 across a partial sale — the flag rides
// across faithfully in BOTH directions, not just defaulting to whatever was hardcoded.
func TestPartialSaleOfUnkeptFindStaysUnkept(t *testing.T) {
	s := openTestStore(t)
	typeID, err := s.InsertItemType(model.ItemType{Kind: "junk", Name: "90% half", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.InsertHolding(model.Holding{
		ItemTypeID: typeID, Activity: "crh", Qty: 2, BasisUSD: 1.00, FaceValueUSD: 1.00, Acquired: "2026-06-01", Kept: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SellHolding(id, 1, 12.00, "2026-06-10"); err != nil {
		t.Fatal(err)
	}
	var flagged int
	if err := s.db.QueryRow(`SELECT count(*) FROM lots WHERE item_type_id=? AND kept != 0`, typeID).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if flagged != 0 {
		t.Errorf("an unkept find gained kept=1 across a partial sale (%d flagged rows)", flagged)
	}
}

// --- small local helpers (kept out of the shared test surface) -------------------

func mustResolve(t *testing.T, s *Store) model.Dataset {
	t.Helper()
	d, err := s.ResolveDataset()
	if err != nil {
		t.Fatalf("resolve dataset: %v", err)
	}
	return d
}

func approxKept(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %.4f, want %.4f", label, got, want)
	}
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// containsWord reports whether s contains word bounded by non-letters — so "update"
// matches an UPDATE statement but not the "updated" inside a comment word.
func containsWord(s, word string) bool {
	isLetter := func(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] != word {
			continue
		}
		if i > 0 && isLetter(s[i-1]) {
			continue
		}
		if i+len(word) < len(s) && isLetter(s[i+len(word)]) {
			continue
		}
		return true
	}
	return false
}
