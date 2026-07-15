package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/model"
)

// Photos: the store side of om-6hlp. A photo row IS the immutable original; the
// derivative thumb/display cache is a separate, regenerable tree the store never
// reasons about (ADR-009 amendment (e)). The path is derived — photos/<owner_uid>/
// <photo_uid>.<ext> — so nothing here builds a path, and re-ordering or re-roling a
// photo is an UPDATE that touches no file (AC7).
//
// Every mutation below follows the om-u3el three-form pattern: an exported *Store
// method (auto-commit) and its *Tx twin, each validating in its OWN body (the AST
// chokepoint guard, validate_ast_test.go, reads each body), delegating to one private
// helper over the shared execer so the two paths cannot drift. All are declared in
// expectedMutations.

// --- insert ------------------------------------------------------------------

// InsertPhoto records a photo original and returns the stored row with its
// server-assigned uid, seq (= max(seq)+1 per owner) and created stamp filled in — the
// caller needs the uid to name the file it is about to write. The uid is
// server-generated and never taken from the caller (ADR-009).
func (s *Store) InsertPhoto(p model.Photo) (model.Photo, error) {
	if err := p.Validate(); err != nil {
		return model.Photo{}, err
	}
	return insertPhoto(s.db, p)
}

// InsertPhoto records a photo original inside the transaction. See *Store.InsertPhoto.
// The upload path uses this: the row commits in one WithTx, then the temp original is
// renamed into place — so a committed row always has its original on disk.
func (tx *Tx) InsertPhoto(p model.Photo) (model.Photo, error) {
	if err := p.Validate(); err != nil {
		return model.Photo{}, err
	}
	return insertPhoto(tx.db, p)
}

func insertPhoto(x execer, p model.Photo) (model.Photo, error) {
	// seq = max(seq)+1 per owner, over ALL rows including inactive ones — a new photo
	// must never reuse a soft-deleted photo's slot. First photo lands at seq 1.
	var seq int64
	if err := x.QueryRow(
		`SELECT COALESCE(MAX(seq),0)+1 FROM photos WHERE owner_kind=? AND owner_uid=?`,
		p.OwnerKind, p.OwnerUID).Scan(&seq); err != nil {
		return model.Photo{}, fmt.Errorf("photo seq: %w", err)
	}
	uid := newUID()
	// A blank role is DEFAULTED, never stored — a NULL/'' role would evaluate out of the
	// gallery's role filter and lose the photo (the 0009 trap). ext is normalized to the
	// lowercase form it is pathed in.
	role := strings.TrimSpace(p.Role)
	if role == "" {
		role = "detail"
	}
	ext := strings.ToLower(strings.TrimSpace(p.Ext))
	created := time.Now().UTC().Format(time.RFC3339)
	res, err := x.Exec(
		`INSERT INTO photos (uid, owner_kind, owner_uid, role, seq, ext, caption, created, inactive)
		 VALUES (?,?,?,?,?,?,?,?,0)`,
		uid, p.OwnerKind, p.OwnerUID, role, seq, ext, nullStr(p.Caption), created)
	if err != nil {
		return model.Photo{}, fmt.Errorf("insert photo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Photo{}, err
	}
	out := p
	out.ID, out.UID, out.Role, out.Seq, out.Ext, out.Created, out.Inactive = id, uid, role, seq, ext, created, false
	return out, nil
}

// OwnerExists reports whether the lot / roll_txn named by (ownerKind, ownerUID) is present.
// The upload path re-checks this INSIDE its WithTx before inserting the photo row: the
// handler's early check races a concurrent DeleteHolding (there is no FK from photos to the
// owner), and without the in-tx re-check that race would leave an ACTIVE photo pointing at a
// deleted lot. ownerKind is already constrained to "lot"/"roll_txn" by Photo.Validate. This
// is deliberately NOT folded into insertPhoto: the export tests seed photos with synthetic
// owner_uids, and the store helper must stay permissive — the integrity gate is the caller
// that owns a real coin (the upload), which is where the race lives.
func (tx *Tx) OwnerExists(ownerKind, ownerUID string) (bool, error) {
	return photoOwnerExists(tx.db, ownerKind, ownerUID)
}

func photoOwnerExists(x execer, ownerKind, ownerUID string) (bool, error) {
	table := "lots"
	if ownerKind == "roll_txn" {
		table = "roll_txns"
	}
	// table is chosen from a closed set above, never from user input.
	var one int
	switch err := x.QueryRow(`SELECT 1 FROM `+table+` WHERE uid=? LIMIT 1`, ownerUID).Scan(&one); err {
	case nil:
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, err
	}
}

// --- update (role / seq / caption) -------------------------------------------

// UpdatePhoto changes a photo's role, seq or caption — and NOTHING that touches its
// path (uid/owner_kind/owner_uid/ext are immutable), so a re-order or re-role changes
// no file on disk (AC7). The caller loads the stored row (PhotoByID), overlays the
// edited fields, and passes the whole row so Validate sees a complete photo.
func (s *Store) UpdatePhoto(id int64, p model.Photo) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return updatePhoto(s.db, id, p)
}

// UpdatePhoto changes a photo's role/seq/caption inside the transaction. See
// *Store.UpdatePhoto.
func (tx *Tx) UpdatePhoto(id int64, p model.Photo) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return updatePhoto(tx.db, id, p)
}

func updatePhoto(x execer, id int64, p model.Photo) error {
	role := strings.TrimSpace(p.Role)
	if role == "" {
		role = "detail"
	}
	res, err := x.Exec(
		`UPDATE photos SET role=?, seq=?, caption=? WHERE id=?`,
		role, p.Seq, nullStr(p.Caption), id)
	return affected(res, err, "update photo")
}

// --- soft delete -------------------------------------------------------------

// UpdatePhotoInactive is the soft delete (f/N3): it flags a photo trashed (or
// un-trashes it) WITHOUT moving or removing the file — the original stays on disk and
// export still carries it. It loads the row and validates before writing, so a
// soft-delete is a validated write like every other (om-1czp chokepoint) and the AST
// guard covers it; the flag itself is separate from the model, so it is set directly.
func (s *Store) UpdatePhotoInactive(id int64, inactive bool) error {
	p, err := photoByID(s.db, id)
	if err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	return setPhotoInactive(s.db, id, inactive)
}

// UpdatePhotoInactive soft-deletes a photo inside the transaction. See
// *Store.UpdatePhotoInactive.
func (tx *Tx) UpdatePhotoInactive(id int64, inactive bool) error {
	p, err := photoByID(tx.db, id)
	if err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	return setPhotoInactive(tx.db, id, inactive)
}

func setPhotoInactive(x execer, id int64, inactive bool) error {
	res, err := x.Exec(`UPDATE photos SET inactive=? WHERE id=?`, b2i(inactive), id)
	return affected(res, err, "soft-delete photo")
}

// --- reads -------------------------------------------------------------------

// ListPhotos returns an owner's ACTIVE photos, ordered (seq, uid) — the total order
// idx_photos_owner covers, deterministic across repeated reads even when every row has
// the default seq 0 (0009). Trashed (inactive) photos are hidden from the gallery.
func (s *Store) ListPhotos(ownerKind, ownerUID string) ([]model.Photo, error) {
	rows, err := s.db.Query(
		`SELECT id, uid, owner_kind, owner_uid, role, seq, ext, caption, created, inactive
		 FROM photos WHERE owner_kind=? AND owner_uid=? AND inactive=0 ORDER BY seq, uid`,
		ownerKind, ownerUID)
	if err != nil {
		return nil, fmt.Errorf("list photos: %w", err)
	}
	defer rows.Close()
	var out []model.Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PhotoByID loads one photo row by its integer id (for the update/soft-delete merge).
func (s *Store) PhotoByID(id int64) (model.Photo, error) { return photoByID(s.db, id) }

// PhotoByUID looks a photo up by its stable uid — the whitelist step the serve route
// runs BEFORE it builds any path, so a request can only ever reach a file the DB
// already vouches for (AC8). Returns ErrNotFound when no row matches. Trashed photos
// are still resolvable here (the file survives); the gallery is what hides them.
func (s *Store) PhotoByUID(uid string) (model.Photo, error) {
	return scanPhotoRow(s.db.QueryRow(
		`SELECT id, uid, owner_kind, owner_uid, role, seq, ext, caption, created, inactive
		 FROM photos WHERE uid=?`, uid))
}

func photoByID(x execer, id int64) (model.Photo, error) {
	return scanPhotoRow(x.QueryRow(
		`SELECT id, uid, owner_kind, owner_uid, role, seq, ext, caption, created, inactive
		 FROM photos WHERE id=?`, id))
}

// rowScanner is the read surface *sql.Row and *sql.Rows share, so scanPhoto serves
// both the single-row lookups and the list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanPhotoRow(r rowScanner) (model.Photo, error) {
	p, err := scanPhoto(r)
	if err == sql.ErrNoRows {
		return model.Photo{}, ErrNotFound
	}
	return p, err
}

func scanPhoto(r rowScanner) (model.Photo, error) {
	var p model.Photo
	var caption, created sql.NullString
	var inactive int64
	if err := r.Scan(&p.ID, &p.UID, &p.OwnerKind, &p.OwnerUID, &p.Role, &p.Seq, &p.Ext,
		&caption, &created, &inactive); err != nil {
		return model.Photo{}, err
	}
	p.Caption, p.Created, p.Inactive = caption.String, created.String, inactive != 0
	return p, nil
}
