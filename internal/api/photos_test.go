package api_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/api"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"

	"net/http/httptest"
)

// A store + serve stack wired to real on-disk photo/cache dirs, with one lot to hang
// photos off. Everything the om-6hlp acceptance criteria touch runs against this.
type photoEnv struct {
	srv       *httptest.Server
	s         *store.Store
	photosDir string
	cacheDir  string
	ownerUID  string
}

func newPhotoEnv(t *testing.T) photoEnv {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
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
	photosDir, cacheDir := filepath.Join(t.TempDir(), "photos"), filepath.Join(t.TempDir(), "photos-cache")
	srv := httptest.NewServer(api.Handler(s, nil, photosDir, cacheDir))
	t.Cleanup(func() { srv.Close(); s.Close() })
	return photoEnv{srv: srv, s: s, photosDir: photosDir, cacheDir: cacheDir, ownerUID: owner}
}

// uploadPhoto posts one multipart image and returns the response + decoded photo (when 201).
func (e photoEnv) uploadPhoto(t *testing.T, owner, role, filename string, data []byte) (*http.Response, model.Photo) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("owner_kind", "lot")
	_ = mw.WriteField("owner_uid", owner)
	if role != "" {
		_ = mw.WriteField("role", role)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	req, err := http.NewRequest("POST", e.srv.URL+"/api/photos", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var p model.Photo
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
	}
	resp.Body.Close()
	return resp, p
}

// AC1/AC2/AC3: two uploads land at sequence 1 then 2; the ORIGINAL lands in the originals
// tree under <owner>/<uid>.<ext> (the filename is the uid, never the image's role); thumb +
// display live in the SEPARATE cache dir; deleting the cache and re-fetching regenerates.
func TestUploadStoresOriginalAndRegenerableDerivatives(t *testing.T) {
	e := newPhotoEnv(t)
	png4 := smallPNG(t, 8, 8)

	resp, first := e.uploadPhoto(t, e.ownerUID, "obverse", "IMG_1234.PNG", png4)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status %d", resp.StatusCode)
	}
	if first.Seq != 1 {
		t.Errorf("first seq = %d, want 1", first.Seq)
	}
	_, second := e.uploadPhoto(t, e.ownerUID, "", "x.png", png4)
	if second.Seq != 2 {
		t.Errorf("second seq = %d, want 2", second.Seq)
	}

	// AC2: original at <owner>/<uid>.png inside the originals tree; the filename is the uid.
	orig := filepath.Join(e.photosDir, e.ownerUID, first.UID+".png")
	if _, err := os.Stat(orig); err != nil {
		t.Fatalf("original not at the derived path %s: %v", orig, err)
	}
	if _, err := os.Stat(filepath.Join(e.photosDir, e.ownerUID, "obverse.png")); err == nil {
		t.Error("a file was named by the ROLE, not the uid — role belongs in the DB, not the path")
	}

	// AC3: thumb + display in the SEPARATE cache dir, generated at ingest.
	for _, v := range []string{"thumb", "display"} {
		if _, err := os.Stat(filepath.Join(e.cacheDir, e.ownerUID, first.UID+"-"+v+".jpg")); err != nil {
			t.Errorf("%s derivative missing from the cache dir: %v", v, err)
		}
	}
	// No derivative may live in the originals tree.
	entries, _ := os.ReadDir(filepath.Join(e.photosDir, e.ownerUID))
	for _, en := range entries {
		if filepath.Ext(en.Name()) == ".jpg" && en.Name() != first.UID+".jpg" {
			t.Errorf("a derivative leaked into the originals tree: %s", en.Name())
		}
	}

	// AC3 regen: delete the whole cache dir, then GET the display variant → it comes back.
	if err := os.RemoveAll(e.cacheDir); err != nil {
		t.Fatal(err)
	}
	r, err := http.Get(e.srv.URL + "/api/photos/" + first.UID + "/file?variant=display")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("display fetch after cache wipe: status %d", r.StatusCode)
	}
	if ct := r.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("regenerated display content-type = %q, want image/jpeg", ct)
	}
	if _, err := os.Stat(filepath.Join(e.cacheDir, e.ownerUID, first.UID+"-display.jpg")); err != nil {
		t.Errorf("display was not re-cached on the miss: %v", err)
	}
}

// AC4: a decompression bomb (a PNG header claiming absurd dimensions) is rejected 4xx
// BEFORE any full decode, and leaves NO row and NO file (atomicity: a failed upload has
// no residue).
func TestUploadRejectsDecompressionBomb(t *testing.T) {
	e := newPhotoEnv(t)
	resp, _ := e.uploadPhoto(t, e.ownerUID, "", "bomb.png", bombPNG(60000, 60000))
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("bomb upload status %d, want 4xx", resp.StatusCode)
	}
	assertNoResidue(t, e)
}

// AC4: non-image bytes (a disallowed type by magic-byte sniff) are refused, no residue.
func TestUploadRejectsNonImage(t *testing.T) {
	e := newPhotoEnv(t)
	resp, _ := e.uploadPhoto(t, e.ownerUID, "", "notes.txt", []byte("this is definitely not an image"))
	if resp.StatusCode != 400 {
		t.Fatalf("non-image upload status %d, want 400", resp.StatusCode)
	}
	assertNoResidue(t, e)
}

// An upload against a uid no lot owns is refused (no orphan photo), and leaves no residue.
func TestUploadRejectsUnknownOwner(t *testing.T) {
	e := newPhotoEnv(t)
	resp, _ := e.uploadPhoto(t, "00000000-0000-4000-8000-000000000000", "", "x.png", smallPNG(t, 4, 4))
	if resp.StatusCode != 404 {
		t.Fatalf("unknown-owner upload status %d, want 404", resp.StatusCode)
	}
	assertNoResidue(t, e)
}

// AC8 (write-path twin of the serve whitelist): an upload whose owner_kind is not one we
// support must be refused BEFORE any filesystem path is built from owner_uid — otherwise a
// traversal owner_uid escapes photosDir. The pre-fix hole: ownerExists ran only for
// owner_kind in {lot,roll_txn}, so an unknown kind fell through to MkdirAll(photosDir/<uid>)
// and InsertPhoto's Validate rejected it only AFTER a directory was created outside photosDir.
// assertNoResidue can't see that (it only inspects photosDir/<owner>), so check the escaped
// sibling explicitly.
func TestUploadRejectsOwnerKindEscapeBeforeTouchingDisk(t *testing.T) {
	e := newPhotoEnv(t)
	// The path the handler would build from an unvalidated owner_uid, cleaned exactly as
	// filepath.Join(photosDir, ownerUID) would clean it — one level OUTSIDE photosDir.
	escaped := filepath.Join(e.photosDir, "../../ESCAPED")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("owner_kind", "notavalidkind")
	_ = mw.WriteField("owner_uid", "../../ESCAPED")
	fw, err := mw.CreateFormFile("file", "x.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(smallPNG(t, 4, 4)); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	req, err := http.NewRequest("POST", e.srv.URL+"/api/photos", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// (a) an unknown owner_kind is a 400 — not a 404/500/201, and not after a side effect.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("escape upload status %d, want 400", resp.StatusCode)
	}
	// (b) the real point: NOTHING was created outside photosDir.
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Errorf("an upload with a traversal owner_uid created %s OUTSIDE photosDir (stat err=%v) — "+
			"owner_uid reached the filesystem before it was validated", escaped, err)
	}
	// And no residue inside photosDir either.
	assertNoResidue(t, e)
}

// AC8: the serve route rejects a traversal / a non-v4 / an unknown v4 with 404 — and NEVER
// falls through to the SPA's index.html-with-200. (webFS is nil here, but the point is the
// status + non-HTML body.)
func TestServeRejectsTraversalAndNonV4(t *testing.T) {
	e := newPhotoEnv(t)
	for _, uid := range []string{
		"not-a-uid",
		"..",
		"11111111-1111-1111-1111-111111111111",           // well-formed but not v4 (version nibble)
		"12345678-1234-4234-8234-123456789abc",           // valid v4 shape, but no such row
		"../../etc/passwd",
	} {
		r, err := http.Get(e.srv.URL + "/api/photos/" + uid + "/file")
		if err != nil {
			continue // a client-side URL parse refusal is also a non-serve; move on
		}
		body := make([]byte, 64)
		n, _ := r.Body.Read(body)
		r.Body.Close()
		if r.StatusCode == 200 {
			t.Errorf("uid %q served with 200 (body %q) — a miss must 404, never fall through to HTML", uid, body[:n])
		}
		if ct := r.Header.Get("Content-Type"); bytes.Contains([]byte(ct), []byte("html")) {
			t.Errorf("uid %q returned HTML (%s) — the serve route must not reach spaHandler", uid, ct)
		}
	}
}

// AC7: a PUT that changes role and seq changes NO byte of the original (mtime + size
// identical afterwards).
func TestUpdatePhotoDoesNotTouchTheFile(t *testing.T) {
	e := newPhotoEnv(t)
	_, p := e.uploadPhoto(t, e.ownerUID, "detail", "x.png", smallPNG(t, 8, 8))
	orig := filepath.Join(e.photosDir, e.ownerUID, p.UID+".png")
	before, err := os.Stat(orig)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"role": "obverse", "seq": 5, "caption": "hi"})
	req, _ := http.NewRequest("PUT", e.srv.URL+"/api/photos/"+itoa(p.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("PUT status %d", r.StatusCode)
	}
	after, err := os.Stat(orig)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Error("the original file was touched by a metadata-only PUT (re-order/re-role must not rewrite bytes)")
	}
	got, _ := e.s.PhotoByID(p.ID)
	if got.Role != "obverse" || got.Seq != 5 {
		t.Errorf("the metadata edit did not land: %+v", got)
	}
}

// AC9: DELETE soft-deletes — row inactive=1, file STILL on disk.
func TestDeletePhotoIsSoftAndKeepsTheFile(t *testing.T) {
	e := newPhotoEnv(t)
	_, p := e.uploadPhoto(t, e.ownerUID, "", "x.png", smallPNG(t, 8, 8))
	orig := filepath.Join(e.photosDir, e.ownerUID, p.UID+".png")

	req, _ := http.NewRequest("DELETE", e.srv.URL+"/api/photos/"+itoa(p.ID), nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 204 {
		t.Fatalf("DELETE status %d, want 204", r.StatusCode)
	}
	got, err := e.s.PhotoByID(p.ID)
	if err != nil {
		t.Fatalf("the row was hard-deleted, not soft: %v", err)
	}
	if !got.Inactive {
		t.Error("inactive flag not set")
	}
	if _, err := os.Stat(orig); err != nil {
		t.Errorf("the original file was removed on delete — soft delete must keep bytes: %v", err)
	}
}

// AC5: EXIF is KEPT by default and STRIPPED when the global setting says so — future
// imports only, and only the metadata (the image data survives).
func TestEXIFStrippedOnlyWhenSettingEnabled(t *testing.T) {
	e := newPhotoEnv(t)
	withExif := jpegWithEXIF(t)

	// Default (KEEP): the stored original still carries the APP1/EXIF marker.
	_, kept := e.uploadPhoto(t, e.ownerUID, "", "keep.jpg", withExif)
	keptBytes, err := os.ReadFile(filepath.Join(e.photosDir, e.ownerUID, kept.UID+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(keptBytes, []byte("Exif")) {
		t.Error("default is KEEP, but the stored original lost its EXIF")
	}

	// Enable strip, upload again: the NEW original has no EXIF.
	if err := e.s.PutSettings(withStrip(t, e.s)); err != nil {
		t.Fatal(err)
	}
	_, stripped := e.uploadPhoto(t, e.ownerUID, "", "strip.jpg", withExif)
	strippedBytes, err := os.ReadFile(filepath.Join(e.photosDir, e.ownerUID, stripped.UID+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(strippedBytes, []byte("Exif")) {
		t.Error("strip is enabled, but the newly imported original still carries EXIF")
	}
	// The already-imported one is unchanged (future-imports-only).
	if !bytes.Contains(keptBytes, []byte("Exif")) {
		t.Error("enabling strip must not rewrite an already-imported original")
	}
}

// --- helpers -----------------------------------------------------------------

func withStrip(t *testing.T, s *store.Store) model.Settings {
	t.Helper()
	cfg, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.StripEXIFOnImport = true
	return cfg
}

// assertNoResidue: a rejected upload wrote neither a row nor a file for the owner.
func assertNoResidue(t *testing.T, e photoEnv) {
	t.Helper()
	ps, err := e.s.ListPhotos("lot", e.ownerUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Errorf("a rejected upload left %d photo row(s)", len(ps))
	}
	if entries, err := os.ReadDir(filepath.Join(e.photosDir, e.ownerUID)); err == nil {
		for _, en := range entries {
			t.Errorf("a rejected upload left a file behind: %s", en.Name())
		}
	}
}

func smallPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 20), uint8(y * 20), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func smallJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// jpegWithEXIF returns a valid JPEG carrying an APP1 (EXIF) segment right after the SOI —
// exactly what a phone camera writes, and what the strip setting removes.
func jpegWithEXIF(t *testing.T) []byte {
	t.Helper()
	base := smallJPEG(t) // starts FF D8 (SOI)
	payload := append([]byte("Exif\x00\x00"), []byte("MMxx-fake-tiff-gps-here")...)
	seg := make([]byte, 0, len(payload)+4)
	seg = append(seg, 0xFF, 0xE1)
	segLen := len(payload) + 2
	seg = append(seg, byte(segLen>>8), byte(segLen))
	seg = append(seg, payload...)
	out := append([]byte{0xFF, 0xD8}, seg...)
	return append(out, base[2:]...)
}

// bombPNG builds a minimal, VALID PNG header (correct IHDR CRC) claiming w×h dimensions,
// with no image data — enough for image.DecodeConfig to read the size and for the bomb
// guard to refuse it before any pixel buffer is allocated.
func bombPNG(w, h int) []byte {
	sig := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // color type: truecolor
	// 10,11,12 = compression/filter/interlace = 0
	chunk := append([]byte("IHDR"), ihdr...)
	crc := crc32.ChecksumIEEE(chunk)
	out := append([]byte(nil), sig...)
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, 13)
	out = append(out, lenBuf...)
	out = append(out, chunk...)
	crcBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBuf, crc)
	return append(out, crcBuf...)
}

func itoa(n int64) string { return jsonNumber(n) }

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
