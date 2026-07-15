package api

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/imaging"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// Photos (om-6hlp): multipart upload IN, a DB-lookup serve route OUT. This is the app's
// first surface that writes/serves user files off the filesystem, so two structural
// safeguards run everything:
//
//   - The serve route validates the uid against the DB BEFORE it builds any path
//     (PhotoByUID). The path is then assembled ONLY from columns the DB vouches for
//     (owner_uid, ext) plus the validated uid — a WHITELIST, not a sanitizer, so a
//     traversal ('..', an absolute path, a non-v4 uid) is structurally impossible and a
//     miss 404s in JSON, never falling through to the SPA's index.html-200 (AC8).
//   - The upload is FILE-FIRST with a temp+rename: the temp original is written, the row
//     is INSERTed in one WithTx, and only then is the temp renamed into place — so once the
//     rename lands a committed row has its original on disk, and the tolerable residue of a
//     FAILED upload is an orphan FILE with no row (invisible, reapable later), never a row
//     with no original. The one remaining gap is a hard crash BETWEEN the commit and the
//     rename: the bytes survive under the .upload-*.part temp name but not yet at the final
//     path (the uid, hence the final name, is assigned inside the tx, so the rename must
//     follow the commit). Recorded honestly; closing it — mint the uid before the insert, or
//     a reaper — is om-9occ. Derivatives (thumb/display) are a best-effort, regenerable cache.
//
// photosDir is where originals live (photos/<owner_uid>/<uid>.<ext>); cacheDir is the
// separate, gitignored, backup/export-excluded derivative tree. Both may be "" (a store
// with no on-disk home, e.g. an in-memory test store) — the routes then 404/500 rather
// than panic.
type photoHandler struct {
	s         *store.Store
	photosDir string
	cacheDir  string
}

// uidV4RE is the strict shape a serve-route uid must match before it is allowed anywhere
// near a filesystem path: a lowercase RFC-4122 v4. It cannot contain '/', '.', or '..',
// so a request path can never climb out of photosDir.
var uidV4RE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func registerPhotos(mux *http.ServeMux, s *store.Store, photosDir, cacheDir string) {
	h := &photoHandler{s: s, photosDir: photosDir, cacheDir: cacheDir}
	mux.HandleFunc("POST /api/photos", h.upload)
	mux.HandleFunc("GET /api/photos", h.list)
	mux.HandleFunc("GET /api/photos/{uid}/file", h.serve)
	mux.HandleFunc("PUT /api/photos/{id}", h.update)
	mux.HandleFunc("DELETE /api/photos/{id}", h.del)
}

// upload ingests one multipart photo for an owner (owner_kind + owner_uid), with an
// optional role and caption. The bytes are capped, sniffed, and bomb-guarded before a
// single byte touches the disk; the ORIGINAL is stored verbatim (minus EXIF iff the
// global setting says strip); the derivatives are generated best-effort.
func (h *photoHandler) upload(w http.ResponseWriter, r *http.Request) {
	if h.photosDir == "" {
		writeJSON(w, http.StatusInternalServerError, errBody(errors.New("photos are not available on this store")))
		return
	}
	// A real limit on the wire (http.MaxBytesReader), not advice — the DoS/typo guard (d).
	r.Body = http.MaxBytesReader(w, r.Body, imaging.MaxUploadBytes)
	if err := r.ParseMultipartForm(imaging.MaxUploadBytes); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("photo exceeds the %d MB limit", imaging.MaxUploadBytes>>20)))
			return
		}
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	ownerKind := strings.TrimSpace(r.FormValue("owner_kind"))
	ownerUID := strings.TrimSpace(r.FormValue("owner_uid"))
	role := strings.TrimSpace(r.FormValue("role"))
	caption := r.FormValue("caption")

	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("no file part: %w", err)))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Errorf("photo exceeds the %d MB limit", imaging.MaxUploadBytes>>20)))
			return
		}
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}

	// Validate the owner FULLY before any filesystem path is built from owner_uid, so the
	// write path keeps the SAME structural guarantee as the serve route: a path segment is
	// only ever a value the DB vouches for. owner_kind must be one we support; owner_uid must
	// be a well-formed v4 (it names a directory, so '..' / '/' can never reach filepath.Join —
	// a whitelist, not a sanitizer); and the owner must exist (no orphan photo). An unknown
	// kind or a malformed/absent owner is refused HERE — never after MkdirAll has already
	// created a directory outside photosDir from a traversal owner_uid.
	if ownerKind != "lot" && ownerKind != "roll_txn" {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("owner_kind %q is not valid (accepted: lot, roll_txn)", ownerKind)))
		return
	}
	if !uidV4RE.MatchString(ownerUID) {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("owner_uid must be a valid uid")))
		return
	}
	ok, err := h.ownerExists(ownerKind, ownerUID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, errBody(fmt.Errorf("no %s with uid %q", ownerKind, ownerUID)))
		return
	}

	// Sniff the type from the MAGIC BYTES (never the filename) and refuse anything but
	// jpg/png/webp; then bomb-guard the dimensions from the header BEFORE any full decode.
	ext, err := imaging.Sniff(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("unsupported image type (accepted: jpg, png, webp)")))
		return
	}
	if _, _, err := imaging.CheckConfig(data); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("rejected image: %w", err)))
		return
	}

	// EXIF: strip at ingest only when the global setting says so (default KEEP), and only
	// for this NEW import — already-imported originals are never rewritten (N4).
	cfg, err := h.s.GetSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	if cfg.StripEXIFOnImport {
		data = imaging.StripJPEGMetadata(data)
	}

	// --- file-first: temp original -> INSERT (WithTx) -> rename -> derivatives ---
	ownerDir := filepath.Join(h.photosDir, ownerUID)
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		writeErr(w, err)
		return
	}
	tmp, err := os.CreateTemp(ownerDir, ".upload-*.part")
	if err != nil {
		writeErr(w, err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		writeErr(w, err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		writeErr(w, err)
		return
	}

	var photo model.Photo
	err = h.s.WithTx(func(tx *store.Tx) error {
		p, err := tx.InsertPhoto(model.Photo{
			OwnerKind: ownerKind, OwnerUID: ownerUID, Role: role, Ext: ext, Caption: caption,
		})
		if err != nil {
			return err
		}
		photo = p
		return nil
	})
	if err != nil {
		// Nothing committed → the only residue is the temp file, which we remove: a failed
		// upload leaves neither a row nor a stray final original.
		os.Remove(tmpName)
		writeErr(w, err)
		return
	}

	// The row is committed; publish its original by renaming the temp into place (same
	// directory → atomic). Only after this does "row exists" imply "original on disk".
	final := h.originalPath(photo.OwnerUID, photo.UID, photo.Ext)
	if err := os.Rename(tmpName, final); err != nil {
		// A committed row whose rename failed is the one case we cannot silently undo (the
		// row is durable). Leave the bytes on disk under the temp name and shout — do NOT
		// remove them, which would guarantee the loss this whole feature exists to prevent.
		log.Printf("photos: committed row %s but could not place its original (%s -> %s): %v",
			photo.UID, tmpName, final, err)
		writeJSON(w, http.StatusInternalServerError, errBody(fmt.Errorf("stored the photo record but could not write its file: %w", err)))
		return
	}

	// Derivatives are a regenerable cache: a failure here is logged, never fatal, and the
	// serve route will lazily regenerate on a miss anyway.
	h.generateDerivatives(photo.OwnerUID, photo.UID, data)

	writeJSON(w, http.StatusCreated, photo)
}

// list returns an owner's active photos, ordered (seq, uid).
func (h *photoHandler) list(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("owner_kind"))
	uid := strings.TrimSpace(r.URL.Query().Get("owner_uid"))
	if kind == "" || uid == "" {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("owner_kind and owner_uid are required")))
		return
	}
	ps, err := h.s.ListPhotos(kind, uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(ps))
}

// serve streams a photo's original or one of its derivatives. It looks the uid up in the
// DB FIRST (whitelist), so the path is built only from vouched-for values and a miss is a
// plain 404 — never the SPA fallback.
func (h *photoHandler) serve(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	// Reject '..', an absolute path, or anything that is not a lowercase v4 — a segment
	// that could reach the filesystem never gets past here (AC8).
	if !uidV4RE.MatchString(uid) {
		notFound(w)
		return
	}
	photo, err := h.s.PhotoByUID(uid)
	if err != nil {
		notFound(w) // ErrNotFound (or any read error) → 404, never HTML
		return
	}
	switch r.URL.Query().Get("variant") {
	case "", "original":
		h.serveFile(w, r, h.originalPath(photo.OwnerUID, photo.UID, photo.Ext), contentTypeForExt(photo.Ext))
	case "thumb":
		h.serveDerivative(w, r, photo, "thumb", imaging.ThumbEdge)
	case "display":
		h.serveDerivative(w, r, photo, "display", imaging.DisplayEdge)
	default:
		notFound(w)
	}
}

// serveDerivative serves a cached thumb/display; on a cache MISS it lazily regenerates
// from the original (and caches the result), falling back to serving the original itself
// if regeneration fails — derivatives are never the source of truth (R2).
func (h *photoHandler) serveDerivative(w http.ResponseWriter, r *http.Request, photo model.Photo, variant string, maxEdge int) {
	if h.cacheDir != "" {
		if f, err := os.Open(h.cachePath(photo.OwnerUID, photo.UID, variant)); err == nil {
			defer f.Close()
			h.serveOpen(w, r, f, "image/jpeg")
			return
		}
	}
	// MISS: regenerate from the original.
	orig := h.originalPath(photo.OwnerUID, photo.UID, photo.Ext)
	data, err := os.ReadFile(orig)
	if err != nil {
		notFound(w) // no original either — nothing to serve or regenerate
		return
	}
	deriv, err := imaging.Derive(data, maxEdge)
	if err != nil {
		// Regeneration failed (a format we can decode-config but not fully decode, say):
		// serve the original rather than 500. The client still gets a usable image.
		w.Header().Set("Content-Type", contentTypeForExt(photo.Ext))
		http.ServeContent(w, r, photo.UID+"."+photo.Ext, time.Time{}, bytes.NewReader(data))
		return
	}
	if h.cacheDir != "" {
		cp := h.cachePath(photo.OwnerUID, photo.UID, variant)
		if err := os.MkdirAll(filepath.Dir(cp), 0o755); err == nil {
			_ = os.WriteFile(cp, deriv, 0o644) // best-effort cache fill
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, photo.UID+"-"+variant+".jpg", time.Time{}, bytes.NewReader(deriv))
}

func (h *photoHandler) serveFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	f, err := os.Open(path)
	if err != nil {
		notFound(w)
		return
	}
	defer f.Close()
	h.serveOpen(w, r, f, contentType)
}

func (h *photoHandler) serveOpen(w http.ResponseWriter, r *http.Request, f *os.File, contentType string) {
	fi, err := f.Stat()
	if err != nil {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// update merges role/seq/caption onto the stored photo — never anything that would move
// its file (uid/owner/ext are immutable through updatePhoto), so a re-order or re-role
// touches no bytes on disk (AC7). It is a read-modify-write, so it runs under the store
// write lock like the generic PUT merge.
func (h *photoHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	var badBody error
	err = h.s.WithWrite(func() error {
		cur, err := h.s.PhotoByID(id)
		if err != nil {
			return err
		}
		merged, err := decodeOnto(r, cur)
		if err != nil {
			badBody = err
			return err
		}
		return h.s.UpdatePhoto(id, merged)
	})
	if badBody != nil {
		writeJSON(w, http.StatusBadRequest, errBody(badBody))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// del is the SOFT delete: it flags the photo trashed (inactive=1) and moves NO file —
// the original survives on disk and export still carries it (f/N3, AC9).
func (h *photoHandler) del(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if err := h.s.UpdatePhotoInactive(id, true); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers -----------------------------------------------------------------

func (h *photoHandler) originalPath(ownerUID, uid, ext string) string {
	return filepath.Join(h.photosDir, ownerUID, uid+"."+ext)
}

func (h *photoHandler) cachePath(ownerUID, uid, variant string) string {
	return filepath.Join(h.cacheDir, ownerUID, uid+"-"+variant+".jpg")
}

// generateDerivatives writes the thumb + display JPEGs into the cache tree. Best-effort:
// every failure is logged and swallowed, because derivatives are regenerable by
// definition and must never fail an upload whose original is already safely on disk.
func (h *photoHandler) generateDerivatives(ownerUID, uid string, data []byte) {
	if h.cacheDir == "" {
		return
	}
	dir := filepath.Join(h.cacheDir, ownerUID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("photos: cache dir %s: %v (derivatives skipped, will lazy-regen)", dir, err)
		return
	}
	for _, v := range []struct {
		name string
		edge int
	}{{"thumb", imaging.ThumbEdge}, {"display", imaging.DisplayEdge}} {
		d, err := imaging.Derive(data, v.edge)
		if err != nil {
			log.Printf("photos: derive %s for %s: %v (will lazy-regen on serve)", v.name, uid, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, uid+"-"+v.name+".jpg"), d, 0o644); err != nil {
			log.Printf("photos: write %s cache for %s: %v", v.name, uid, err)
		}
	}
}

// ownerExists reports whether a lot/roll_txn with that uid is in the DB — the whitelist
// that keeps an upload from creating a photo for a coin that does not exist.
func (h *photoHandler) ownerExists(ownerKind, ownerUID string) (bool, error) {
	table := "lots"
	if ownerKind == "roll_txn" {
		table = "roll_txns"
	}
	// table is a literal chosen above, never user input.
	var one int
	err := h.s.DB().QueryRow(`SELECT 1 FROM `+table+` WHERE uid=? LIMIT 1`, ownerUID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// notFound writes a JSON 404 — NOT the SPA's index.html. The whole point of the DB-lookup
// serve route is that a missing image is an honest 404, not a 200 with an HTML page the
// <img> renders as broken (AC8, the spaHandler T1 trap).
func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, errBody(errors.New("not found")))
}

func contentTypeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
