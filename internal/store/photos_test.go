package store

import (
	"errors"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// The store side of om-6hlp. In-package (openTestStore, the uid_test.go precedent) so the
// tests can read the raw photos table alongside the public API.

// mkLot inserts a catalog row + a crh find and returns the lot's stable uid — the owner_uid
// a real photo hangs off.
func mkLot(t *testing.T, s *Store) string {
	t.Helper()
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.1, Acquired: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	var uid string
	if err := s.db.QueryRow(`SELECT uid FROM lots WHERE id=?`, id).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	return uid
}

// AC1: two uploads against the same owner land seq 1 then 2, each with a fresh v4 uid, the
// default role, and the sniffed ext. The uid is server-generated, never the caller's.
func TestInsertPhotoAssignsSeqUIDAndDefaultRole(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)

	first, err := s.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: owner, Ext: "JPG"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 {
		t.Errorf("first photo seq = %d, want 1", first.Seq)
	}
	if first.Role != "detail" {
		t.Errorf("blank role stored as %q, want the default 'detail'", first.Role)
	}
	if first.Ext != "jpg" {
		t.Errorf("ext %q was not normalized to lowercase 'jpg'", first.Ext)
	}
	if !looksLikeUUIDv4(first.UID) {
		t.Errorf("photo uid %q is not a lowercase v4", first.UID)
	}

	second, err := s.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: owner, Role: "reverse", Ext: "png"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != 2 {
		t.Errorf("second photo seq = %d, want 2", second.Seq)
	}
	if second.UID == first.UID {
		t.Error("two photos share a uid")
	}
}

// AC1 (om-9occ): InsertPhoto and its *Tx twin HONOR a caller-supplied, well-formed v4 uid
// (the upload mints it before the tx so the original is written at its final path first), and
// the row lands with exactly that uid — not a fresh one. Both forms are exercised, since both
// name the file the caller already placed.
func TestInsertPhotoHonorsCallerSuppliedUID(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)

	const want = "12345678-1234-4234-8234-123456789abc" // a well-formed lowercase v4
	got, err := s.InsertPhoto(model.Photo{UID: want, OwnerKind: "lot", OwnerUID: owner, Ext: "jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != want {
		t.Errorf("*Store.InsertPhoto minted %q, want the caller-supplied %q", got.UID, want)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT uid FROM photos WHERE id=?`, got.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != want {
		t.Errorf("row uid %q != caller-supplied %q — the file the upload wrote at that uid would be orphaned", stored, want)
	}

	// The *Tx twin (the path the upload actually uses) honors it too.
	const wantTx = "abcdef01-2345-4678-9abc-def012345678"
	var txGot model.Photo
	if err := s.WithTx(func(tx *Tx) error {
		p, err := tx.InsertPhoto(model.Photo{UID: wantTx, OwnerKind: "lot", OwnerUID: owner, Ext: "png"})
		txGot = p
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if txGot.UID != wantTx {
		t.Errorf("Tx.InsertPhoto minted %q, want the caller-supplied %q", txGot.UID, wantTx)
	}
}

// AC1 (om-9occ): a BLANK uid is still minted server-side (a fresh v4), so callers that do
// not pre-mint keep the old behavior.
func TestInsertPhotoMintsWhenUIDBlank(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)
	got, err := s.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: owner, Ext: "jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if !looksLikeUUIDv4(got.UID) {
		t.Errorf("a blank uid was not minted into a v4: %q", got.UID)
	}
}

// AC1 (om-9occ): a NON-blank but malformed uid is REJECTED (ErrInvalid), never silently
// replaced — and no row lands. Silently minting a different uid would strand the original the
// upload already wrote at the path named by the supplied uid. Covers both forms.
func TestInsertPhotoRejectsMalformedCallerUID(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)

	for _, bad := range []string{
		"not-a-uid",
		"12345678-1234-1234-8234-123456789abc", // well-formed shape but version nibble != 4
		"ABCDEF01-2345-4678-9ABC-DEF012345678", // uppercase — not the lowercase form pathed
		"12345678123446789abcdef012345678",     // no hyphens
	} {
		if _, err := s.InsertPhoto(model.Photo{UID: bad, OwnerKind: "lot", OwnerUID: owner, Ext: "jpg"}); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("*Store.InsertPhoto(uid=%q): err = %v, want ErrInvalid", bad, err)
		}
		if err := s.WithTx(func(tx *Tx) error {
			_, err := tx.InsertPhoto(model.Photo{UID: bad, OwnerKind: "lot", OwnerUID: owner, Ext: "jpg"})
			return err
		}); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("Tx.InsertPhoto(uid=%q): err = %v, want ErrInvalid", bad, err)
		}
	}
	// No row landed for any of the rejected uids.
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM photos WHERE owner_uid=?`, owner).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a malformed-uid insert landed %d row(s) — it must reject before the DB write", n)
	}
}

// AC6: a gallery read is ORDER BY (seq, uid) — deterministic across repeated reads, even
// for rows left at the default seq 0 — and a role='detail' (the default) photo IS visible
// (the NULL-role trap 0009 called out: an unrendered photo is a lost one).
func TestListPhotosIsTotalOrderAndShowsDetail(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)

	// Insert several with the SAME seq (0) written directly, to exercise the tie-break on uid.
	for _, uid := range []string{"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"} {
		if _, err := s.db.Exec(`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext) VALUES (?,?,?,?,0,'jpg')`,
			uid, "lot", owner, "detail"); err != nil {
			t.Fatal(err)
		}
	}
	var last []string
	for i := 0; i < 5; i++ {
		ps, err := s.ListPhotos("lot", owner)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, p := range ps {
			order = append(order, p.UID)
			if p.Role == "" {
				t.Error("a photo came back with an empty role — it would fall out of the gallery filter")
			}
		}
		if i > 0 && !equalStrs(order, last) {
			t.Errorf("read order flipped between reads: %v vs %v", last, order)
		}
		last = order
	}
	// Ties broken by uid ascending.
	if last[0] != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || last[2] != "cccccccc-cccc-4ccc-8ccc-cccccccccccc" {
		t.Errorf("ORDER BY seq, uid not honored: %v", last)
	}
}

// AC7 (store half): re-ordering / re-roling / re-captioning touches only role/seq/caption —
// never the path columns (uid/owner_kind/owner_uid/ext), so the file the path names is
// untouched.
func TestUpdatePhotoLeavesPathColumnsImmutable(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)
	p, err := s.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: owner, Role: "detail", Ext: "jpg"})
	if err != nil {
		t.Fatal(err)
	}
	// Overlay the editable fields (and, adversarially, try to change the path fields too).
	edited := p
	edited.Role, edited.Seq, edited.Caption = "obverse", 9, "the good one"
	edited.OwnerUID, edited.Ext, edited.UID = "somewhere-else", "png", "different-uid"
	if err := s.UpdatePhoto(p.ID, edited); err != nil {
		t.Fatal(err)
	}
	got, err := s.PhotoByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "obverse" || got.Seq != 9 || got.Caption != "the good one" {
		t.Errorf("role/seq/caption did not update: %+v", got)
	}
	if got.UID != p.UID || got.OwnerUID != owner || got.Ext != "jpg" {
		t.Errorf("a path column changed on update (uid/owner/ext) — the file would be orphaned: %+v", got)
	}
}

// AC9: soft delete flags inactive=1, keeps the row, and hides it from the gallery.
func TestSoftDeleteHidesButKeepsThePhoto(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)
	p, err := s.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: owner, Ext: "jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePhotoInactive(p.ID, true); err != nil {
		t.Fatal(err)
	}
	ps, err := s.ListPhotos("lot", owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Errorf("gallery still shows the trashed photo (%d rows)", len(ps))
	}
	got, err := s.PhotoByID(p.ID)
	if err != nil {
		t.Fatalf("the row was removed, not soft-deleted: %v", err)
	}
	if !got.Inactive {
		t.Error("inactive flag was not set")
	}
}

// R1 (AC9 owner-delete): deleting a lot with photos flags them inactive and KEEPS the rows —
// the files (elsewhere) survive, and export still carries them.
func TestDeleteLotSoftFlagsItsPhotos(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)
	var lotID int64
	if err := s.db.QueryRow(`SELECT id FROM lots WHERE uid=?`, owner).Scan(&lotID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: owner, Ext: "jpg"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteHolding(lotID); err != nil {
		t.Fatal(err)
	}
	// Lot row gone.
	var lots int
	if err := s.db.QueryRow(`SELECT count(*) FROM lots WHERE id=?`, lotID).Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if lots != 0 {
		t.Error("the lot row survived the delete")
	}
	// Both photo rows survive, both flagged inactive.
	var total, active int
	if err := s.db.QueryRow(`SELECT count(*), coalesce(sum(CASE WHEN inactive=0 THEN 1 ELSE 0 END),0) FROM photos WHERE owner_kind='lot' AND owner_uid=?`, owner).Scan(&total, &active); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("photo rows = %d, want 2 preserved after the lot delete", total)
	}
	if active != 0 {
		t.Errorf("%d photo(s) left ACTIVE after the owner was deleted — they should be soft-flagged", active)
	}
}

// The AST chokepoint (om-1czp/om-u3el) demands every photo mutation validate; here is the
// observable half: a bad owner_kind or ext is a 400-class ErrInvalid, not a DB write.
func TestInsertPhotoRejectsBadOwnerKindAndExt(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.InsertPhoto(model.Photo{OwnerKind: "keeper", OwnerUID: "x", Ext: "jpg"}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("bad owner_kind: err = %v, want ErrInvalid", err)
	}
	if _, err := s.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: "x", Ext: "gif"}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("bad ext: err = %v, want ErrInvalid", err)
	}
	if _, err := s.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: "", Ext: "jpg"}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("blank owner_uid: err = %v, want ErrInvalid", err)
	}
}

// The upload path re-checks the owner INSIDE its WithTx via tx.OwnerExists, because the
// handler's early ownerExists races a concurrent DeleteHolding (there is no FK from photos to
// the owner). This pins the mechanism: after the owner is deleted the in-tx check sees it
// gone — and without that check InsertPhoto would land an ACTIVE orphan, so the guard is
// load-bearing. (Codex review finding, om-usga.)
func TestOwnerExistsGuardsUploadAgainstOrphan(t *testing.T) {
	s := openTestStore(t)
	owner := mkLot(t, s)
	var lotID int64
	if err := s.db.QueryRow(`SELECT id FROM lots WHERE uid=?`, owner).Scan(&lotID); err != nil {
		t.Fatal(err)
	}

	// present before the delete
	if err := s.WithTx(func(tx *Tx) error {
		ok, err := tx.OwnerExists("lot", owner)
		if err != nil {
			return err
		}
		if !ok {
			t.Error("owner should exist before delete")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// a concurrent DeleteHolding commits
	if err := s.DeleteHolding(lotID); err != nil {
		t.Fatal(err)
	}

	// the in-tx re-check the upload now performs sees the deletion
	if err := s.WithTx(func(tx *Tx) error {
		ok, err := tx.OwnerExists("lot", owner)
		if err != nil {
			return err
		}
		if ok {
			t.Error("owner should be gone after delete — the upload's in-tx re-check must catch this")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// ...and it is load-bearing: WITHOUT the guard, InsertPhoto lands an active orphan.
	if err := s.WithTx(func(tx *Tx) error {
		_, err := tx.InsertPhoto(model.Photo{OwnerKind: "lot", OwnerUID: owner, Ext: "jpg"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var orphans int
	if err := s.db.QueryRow(`SELECT count(*) FROM photos WHERE owner_uid=? AND inactive=0`, owner).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 1 {
		t.Fatalf("unguarded InsertPhoto orphaned %d photos (want 1) — this is why the upload re-checks OwnerExists in-tx", orphans)
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
