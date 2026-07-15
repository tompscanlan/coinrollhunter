package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// Internal (package api) tests for the crash-window-free ingest core (om-9occ). They drive
// writeOriginalAndInsert directly, which is the only way to exercise the "file written, then
// the tx fails, so the file is removed" path deterministically: the public upload handler
// rejects a vanished owner at its EARLY check (before any file is written), so a clean tx
// failure after the write is only reachable via a true concurrent race — impossible to time
// through the black-box endpoint. Calling the helper with an already-absent owner reproduces
// exactly that ownerGone rollback, on demand.

// newIngestEnv builds a store with one lot to hang photos off and a photoHandler wired to a
// real on-disk originals/cache tree.
func newIngestEnv(t *testing.T) (*photoHandler, string) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	typeID, err := s.InsertItemType(model.ItemType{Kind: "coin", Name: "Mercury Dime", Metal: "silver"})
	if err != nil {
		t.Fatal(err)
	}
	lotID, err := s.InsertHolding(model.Holding{ItemTypeID: typeID, Activity: "crh", Qty: 1, BasisUSD: 0.1, Acquired: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := s.DB().QueryRow(`SELECT uid FROM lots WHERE id=?`, lotID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	h := &photoHandler{
		s:         s,
		photosDir: filepath.Join(t.TempDir(), "photos"),
		cacheDir:  filepath.Join(t.TempDir(), "photos-cache"),
	}
	return h, owner
}

// AC2/AC3 (success half): a successful ingest writes the original at its FINAL path
// <owner>/<uid>.<ext> (no temp, no rename), and the stored row's uid matches that filename.
func TestWriteOriginalAndInsertPlacesOriginalAtFinalPath(t *testing.T) {
	h, owner := newIngestEnv(t)
	uid := store.NewUID()

	photo, ownerGone, err := h.writeOriginalAndInsert(uid, "lot", owner, "obverse", "png", "cap", []byte("bytes"))
	if err != nil || ownerGone {
		t.Fatalf("ingest failed: ownerGone=%v err=%v", ownerGone, err)
	}
	if photo.UID != uid {
		t.Errorf("row uid %q != pre-minted %q", photo.UID, uid)
	}
	final := h.originalPath(owner, uid, "png")
	if _, err := os.Stat(final); err != nil {
		t.Errorf("original not at its final path %s: %v", final, err)
	}
	// No .upload-*.part temp anywhere in the owner dir — the rename is gone entirely.
	entries, _ := os.ReadDir(filepath.Join(h.photosDir, owner))
	for _, en := range entries {
		if en.Name() != uid+".png" {
			t.Errorf("unexpected residue in the originals tree: %s (want only %s.png)", en.Name(), uid)
		}
	}
	// The row is really there and resolvable by that uid.
	got, err := h.s.PhotoByUID(uid)
	if err != nil {
		t.Fatalf("committed row not resolvable by its uid: %v", err)
	}
	if got.UID != uid {
		t.Errorf("PhotoByUID returned uid %q, want %q", got.UID, uid)
	}
}

// AC3 (failure half): when the tx fails because the owner vanished mid-upload, the file the
// ingest already wrote is REMOVED and NO row lands — a failed upload leaves neither a row nor
// a stray final original.
func TestWriteOriginalAndInsertRemovesFileWhenOwnerGone(t *testing.T) {
	h, owner := newIngestEnv(t)

	// Delete the lot so the in-tx OwnerExists re-check fails (the ownerGone path).
	var lotID int64
	if err := h.s.DB().QueryRow(`SELECT id FROM lots WHERE uid=?`, owner).Scan(&lotID); err != nil {
		t.Fatal(err)
	}
	if err := h.s.DeleteHolding(lotID); err != nil {
		t.Fatal(err)
	}

	uid := store.NewUID()
	final := h.originalPath(owner, uid, "png")
	photo, ownerGone, err := h.writeOriginalAndInsert(uid, "lot", owner, "", "png", "", []byte("bytes"))
	if !ownerGone {
		t.Errorf("ownerGone = false, want true (the in-tx re-check must catch the vanished owner)")
	}
	if err == nil {
		t.Error("err = nil, want the abort error from the vanished-owner rollback")
	}
	if photo.UID != "" {
		t.Errorf("a photo (%q) came back from a failed ingest", photo.UID)
	}
	// No stray final file.
	if _, statErr := os.Stat(final); !os.IsNotExist(statErr) {
		t.Errorf("the original at %s survived a failed upload (stat err=%v) — it must be removed", final, statErr)
	}
	// No row.
	var n int
	if err := h.s.DB().QueryRow(`SELECT count(*) FROM photos WHERE owner_uid=?`, owner).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a failed upload left %d photo row(s)", n)
	}
}

// AC2 (O_EXCL): the original is created with O_EXCL, so a uid whose file already exists fails
// the create rather than OVERWRITING the other original — and the pre-existing bytes are left
// untouched, with no row inserted.
func TestWriteOriginalAndInsertO_EXCLDoesNotOverwrite(t *testing.T) {
	h, owner := newIngestEnv(t)
	uid := store.NewUID()
	final := h.originalPath(owner, uid, "png")

	// Pre-place a file at exactly the path the ingest would write, with sentinel bytes.
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		t.Fatal(err)
	}
	const sentinel = "PRE-EXISTING ORIGINAL — MUST NOT BE CLOBBERED"
	if err := os.WriteFile(final, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ownerGone, err := h.writeOriginalAndInsert(uid, "lot", owner, "", "png", "", []byte("new bytes that must not land"))
	if ownerGone {
		t.Error("ownerGone = true, want false (this is an O_EXCL collision, not a vanished owner)")
	}
	if err == nil {
		t.Error("err = nil, want the O_EXCL create failure — a collision must never overwrite")
	}
	// The pre-existing file is byte-identical.
	after, readErr := os.ReadFile(final)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != sentinel {
		t.Errorf("the pre-existing original was clobbered: got %q", string(after))
	}
	// No row landed.
	var n int
	if err := h.s.DB().QueryRow(`SELECT count(*) FROM photos WHERE owner_uid=?`, owner).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("an O_EXCL-failed ingest still inserted %d row(s)", n)
	}
}
