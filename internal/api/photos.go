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
//   - The upload is FILE-FIRST with NO rename (om-9occ): the uid is minted with store.NewUID
//     BEFORE the transaction, so the final path is known up front; the original is written
//     straight to that path with O_EXCL, then the row is INSERTed in one WithTx carrying that
//     same pre-minted uid, and on ANY tx failure the file is removed. Because there is no
//     post-commit rename, a committed row has its original at its final path across a PROCESS
//     crash (SIGKILL at any instruction) — the write precedes the commit — which the old
//     commit→rename window did not survive (it stranded the bytes under a .upload-*.part temp
//     name). One residual remains, narrower and honestly stated: the file is not fsync'd before
//     the tx commits, so a POWER LOSS inside the OS writeback window can leave a durable row
//     whose not-yet-flushed original is missing — closing that needs an fsync of the file+dir
//     before commit (om-0j33). The only tolerable residue of a FAILED upload is an orphan FILE
//     with no row (invisible, reapable later), never a row with no original.
//     Derivatives (thumb/display) are a best-effort, regenerable cache.
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

// errOwnerDeletedDuringUpload aborts (and rolls back) an upload whose owner was deleted
// between the handler's early existence check and the in-tx re-check. Never surfaced to the
// client directly — the handler maps it to the same 404 the early check would have returned.
var errOwnerDeletedDuringUpload = errors.New("owner deleted during upload")

func registerPhotos(mux *http.ServeMux, s *store.Store, photosDir, cacheDir string) {
	h := &photoHandler{s: s, photosDir: photosDir, cacheDir: cacheDir}
	mux.HandleFunc("POST /api/photos", h.upload)
	mux.HandleFunc("GET /api/photos", h.list)
	mux.HandleFunc("GET /api/photos/{uid}/file", h.serve)
	mux.HandleFunc("PUT /api/photos/{id}", h.update)
	mux.HandleFunc("DELETE /api/photos/{id}", h.del)
}

// upload ingests one multipart attachment for an owner (owner_kind + owner_uid), with an
// optional role and caption. The bytes are capped and sniffed before a single byte touches
// the disk. An IMAGE (jpg/png/webp) is then bomb-guarded, EXIF-stripped iff the setting says
// strip, and gets thumb/display derivatives. A DOCUMENT (pdf, om-9o4n.2) SKIPS all of that
// and is stored verbatim — same file-first ingest, no imaging step. Either way the ORIGINAL
// is the immutable source of truth at photos/<owner_uid>/<uid>.<ext>.
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

	// Sniff the type from the MAGIC BYTES (never the filename) and refuse anything but the
	// accepted image (jpg/png/webp) and document (pdf) types.
	ext, err := imaging.Sniff(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(errors.New("unsupported type (accepted: jpg, png, webp, pdf)")))
		return
	}
	// THE BRANCH POINT (om-9o4n.2): a DOCUMENT (PDF) is stored + linked WITHOUT any imaging.
	// It has no decodable pixels, so it must SKIP the image-only steps below — CheckConfig
	// (the bomb guard), the EXIF strip, and derivative generation — each of which assumes an
	// image and would error on a PDF. An image keeps the EXACT current path. The doc's whole
	// gate is the 10MB MaxBytesReader (already applied) + the %PDF magic sniff above.
	isDoc := imaging.IsDocument(ext)

	if !isDoc {
		// Bomb-guard the image dimensions from the header BEFORE any full decode.
		if _, _, err := imaging.CheckConfig(data); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("rejected image: %w", err)))
			return
		}

		// EXIF: strip at ingest only when the global setting says so (default KEEP), and only
		// for this NEW import — already-imported originals are never rewritten (N4). A document
		// carries no camera metadata and is never decoded, so the whole strip step is image-only.
		cfg, err := h.s.GetSettings()
		if err != nil {
			writeErr(w, err)
			return
		}
		if cfg.StripEXIFOnImport {
			data = imaging.StripJPEGMetadata(data)
		}
	}

	// --- file-first, no rename (om-9occ): mint uid -> write original at its FINAL path
	// (O_EXCL) -> INSERT (WithTx) that same uid -> derivatives. The uid is known before the
	// write, so there is no post-commit rename and thus no commit→rename crash window.
	uid := store.NewUID()
	photo, ownerGone, err := h.writeOriginalAndInsert(uid, ownerKind, ownerUID, role, ext, caption, data)
	if ownerGone {
		// The owner was deleted between the early check and the in-tx re-check — nothing
		// committed, the file was removed, and we report the same 404 the early check would.
		writeJSON(w, http.StatusNotFound, errBody(fmt.Errorf("no %s with uid %q", ownerKind, ownerUID)))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	// Derivatives are a regenerable cache: a failure here is logged, never fatal, and the
	// serve route will lazily regenerate on a miss anyway. A DOCUMENT (PDF) has no image to
	// derive — thumb/display of a doc 404 by design — so derivative generation is skipped for
	// it entirely (imaging.Derive would only error on a PDF).
	if !isDoc {
		h.generateDerivatives(photo.OwnerUID, photo.UID, data)
	}

	writeJSON(w, http.StatusCreated, photo)
}

// writeOriginalAndInsert is the crash-window-free ingest core (om-9occ). It writes data at
// the ORIGINAL's final path — named by the caller-minted uid, created with O_EXCL so a uid
// collision fails the create rather than overwriting an existing original — then INSERTs the
// row carrying that same uid inside one WithTx (with the in-tx owner re-check). On ANY tx
// failure OR a vanished owner it removes the file it wrote, so the only residue of a failed
// upload is nothing at all (the tolerable orphan-file case is a hard crash between the write
// and the commit, not a clean failure). There is deliberately no post-commit rename anywhere:
// because the original is at its final path before the row commits, a committed row has its
// original across a process crash — power-loss durability additionally needs an fsync before
// commit (om-0j33). Returns the stored photo; ownerGone signals the owner disappeared
// mid-upload (mapped to 404).
func (h *photoHandler) writeOriginalAndInsert(uid, ownerKind, ownerUID, role, ext, caption string, data []byte) (model.Photo, bool, error) {
	ownerDir := filepath.Join(h.photosDir, ownerUID)
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		return model.Photo{}, false, err
	}
	final := h.originalPath(ownerUID, uid, ext)
	// O_EXCL: never overwrite an existing original. A v4 collision is astronomically
	// unlikely; if it (or any create error) happens, fail rather than clobber the other file.
	f, err := os.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return model.Photo{}, false, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(final)
		return model.Photo{}, false, err
	}
	if err := f.Close(); err != nil {
		os.Remove(final)
		return model.Photo{}, false, err
	}

	var photo model.Photo
	var ownerGone bool
	err = h.s.WithTx(func(tx *store.Tx) error {
		// Re-check the owner INSIDE the tx. The early ownerExists in upload() races a
		// concurrent DeleteHolding (there is no FK from photos to the owner), so an upload
		// that began before a delete committed would otherwise land an ACTIVE photo on a
		// nonexistent lot — the exact orphan the early check exists to prevent. This re-check
		// sees the deletion (same connection, serialized by the write lock) and rolls back.
		switch ok, err := tx.OwnerExists(ownerKind, ownerUID); {
		case err != nil:
			return err
		case !ok:
			ownerGone = true
			return errOwnerDeletedDuringUpload
		}
		p, err := tx.InsertPhoto(model.Photo{
			UID: uid, OwnerKind: ownerKind, OwnerUID: ownerUID, Role: role, Ext: ext, Caption: caption,
		})
		if err != nil {
			return err
		}
		photo = p
		return nil
	})
	if ownerGone || err != nil {
		// Nothing committed → remove the original we wrote: a failed upload leaves neither a
		// row nor a stray final file. (An orphan file with no row is only ever the residue of
		// a HARD crash between the write and the commit, which is tolerable and reapable.)
		os.Remove(final)
		return model.Photo{}, ownerGone, err
	}
	return photo, false, nil
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
	// A soft-deleted (trashed) photo is deleted to a VIEWER: 404 it for every variant
	// (original/thumb/display), so a uid holder can no longer pull the bytes back after a
	// delete (om-hs1v Half A). PhotoByUID still resolves inactive rows on purpose — other
	// readers (export, restore-from-trash) depend on that — so the guard lives HERE, not in
	// the lookup.
	if photo.Inactive {
		notFound(w)
		return
	}
	// A DOCUMENT (PDF) rides the same photos row but never entered the imaging pipeline, so
	// it has no thumb/display derivative and must NOT be decoded on the serve path either
	// (om-9o4n.2). Its ORIGINAL streams with a strict content-type + nosniff; every other
	// variant is an honest 404 — never a 500 from trying to Derive() a PDF, never a fall-
	// through to the raw bytes under an image URL. This block is separate from the image
	// switch below precisely so the image paths stay byte-identical to before.
	if imaging.IsDocument(photo.Ext) {
		if v := r.URL.Query().Get("variant"); v == "" || v == "original" {
			h.serveDoc(w, r, photo)
		} else {
			notFound(w)
		}
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

// serveDoc streams a document attachment's original (a PDF today, om-9o4n.2). It never
// touches the derivative path, and reuses the same DB-vouched path assembly (owner_uid +
// validated uid + ext) as every other serve. Two headers make accepting a file we
// deliberately never decode safe (ADR-009 (f) untrusted-bytes posture):
//
//   - X-Content-Type-Options: nosniff — the browser HONORS the declared application/pdf and
//     never MIME-sniffs the un-decoded bytes into something it would render or execute.
//   - Content-Disposition: attachment — the PDF DOWNLOADS instead of rendering INLINE
//     (om-rix0). nosniff does nothing about the browser's OWN PDF viewer: a same-origin
//     pdf.js-class viewer bug (e.g. CVE-2024-4367, script execution in the viewer context)
//     would run in THIS origin, and the om-6ex5 loopback guard is inert against same-origin
//     requests — so one malicious dealer-emailed receipt could reach the unauthenticated
//     local API. Downloading moves the file out of the app's origin entirely, closing that
//     vector regardless of any future viewer vuln. The filename is built ONLY from the
//     regex-validated uid + closed-set ext (never a client-supplied name), and %q escapes it,
//     so nothing user-controlled can inject into the header.
func (h *photoHandler) serveDoc(w http.ResponseWriter, r *http.Request, photo model.Photo) {
	f, err := os.Open(h.originalPath(photo.OwnerUID, photo.UID, photo.Ext))
	if err != nil {
		notFound(w)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", contentTypeForExt(photo.Ext)) // application/pdf for a doc
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", photo.UID+"."+photo.Ext))
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
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
	case "pdf":
		return "application/pdf" // a document attachment, served with nosniff (om-9o4n.2)
	default:
		return "application/octet-stream"
	}
}
