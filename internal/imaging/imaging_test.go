package imaging

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// Derive must run the bomb guard ITSELF on every decode, not trust the caller — because the
// lazy regen path (serveDerivative) decodes originals read off disk that never passed
// ingest's CheckConfig (backup-restore / hand-copy / corruption). A tiny compressed PNG
// claiming absurd dimensions must be refused BEFORE image.Decode allocates the pixel buffer.
// (Codex review finding, om-usga.)
func TestDeriveGuardsAgainstBombOnEveryDecode(t *testing.T) {
	// 60000x60000 = 3.6 gigapixels declared in a few bytes; over both maxDimension and maxPixels.
	// Assert the specific ErrTooLarge sentinel, not merely "an error": a header-only bomb also
	// fails image.Decode (no pixel data), so only ErrTooLarge proves the CheckConfig guard ran
	// FIRST — which is the whole point (reject before allocating), and what fails pre-fix.
	if _, err := Derive(bombPNG(60000, 60000), ThumbEdge); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Derive on a bomb returned %v, want ErrTooLarge — the header guard must run before decode", err)
	}
	// A normal image still derives fine — the guard must not reject legitimate photos.
	if _, err := Derive(smallPNG(t, 8, 8), ThumbEdge); err != nil {
		t.Fatalf("Derive rejected a normal 8x8 image: %v", err)
	}
}

func smallPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 8), uint8(y * 8), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// --- document (PDF) attachments (om-9o4n.2) ----------------------------------
//
// A receipt can be a PDF: a document that is STORED + LINKED but never imaged. Sniff must
// recognize it by magic bytes (like the image types) and return "pdf", and IsDocument must
// classify it so the upload/serve handlers branch AWAY from the image-only pipeline.

// minimalPDF returns bytes http.DetectContentType classifies as application/pdf. The server
// never decodes a document, so the "%PDF-" magic is all the sniff (and these tests) need — it
// need not be a fully-valid PDF, only PDF-sniffable.
func minimalPDF() []byte {
	return []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n")
}

// TestSniffRecognizesPDF pins AC1: a %PDF blob sniffs to "pdf"; a type we accept as neither
// image nor document is still ErrUnsupported.
func TestSniffRecognizesPDF(t *testing.T) {
	ext, err := Sniff(minimalPDF())
	if err != nil {
		t.Fatalf("Sniff(pdf) error: %v", err)
	}
	if ext != "pdf" {
		t.Errorf("Sniff(pdf) = %q, want \"pdf\"", ext)
	}
	// A GIF (image we do not accept) and plain text are both still refused.
	if _, err := Sniff([]byte("GIF89a\x00\x00")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Sniff(gif) err = %v, want ErrUnsupported", err)
	}
	if _, err := Sniff([]byte("just some text, not any known type")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Sniff(text) err = %v, want ErrUnsupported", err)
	}
}

// TestIsDocumentAndDocSkipsImaging pins that IsDocument identifies a PDF (case-folded) and
// only a PDF — and the WHY behind the branch: a document has no decodable pixels, so the
// image-only guards (CheckConfig, Derive) ERROR on it. The upload/serve paths must branch on
// IsDocument and never hand a doc to those, which this proves by exercising them directly.
func TestIsDocumentAndDocSkipsImaging(t *testing.T) {
	if !IsDocument("pdf") || !IsDocument("PDF") {
		t.Error("IsDocument must classify pdf/PDF as a document (case-insensitive)")
	}
	for _, img := range []string{"jpg", "jpeg", "png", "webp", "", "gif"} {
		if IsDocument(img) {
			t.Errorf("IsDocument(%q) = true, want false — only a document skips imaging", img)
		}
	}
	if _, _, err := CheckConfig(minimalPDF()); err == nil {
		t.Error("CheckConfig(pdf) succeeded — a PDF has no image header; a doc MUST skip CheckConfig")
	}
	if _, err := Derive(minimalPDF(), ThumbEdge); err == nil {
		t.Error("Derive(pdf) succeeded — a PDF cannot be decoded; a doc MUST skip Derive")
	}
}

// --- strip-all-formats coverage (om-65nv) ------------------------------------
//
// PROVEN-FAIL-PRE-FIX: before this bead, StripJPEGMetadata handled only JPEG and
// returned PNG/WebP UNCHANGED (the data[0]!=0xFF||data[1]!=0xD8 guard fell
// through). So TestStripRemovesMetadataAllFormats/png and /webp FAILED on main
// @ f0ac6b5 — the gpsMarker was still present after "strip" — while /jpeg passed.
// Recorded raw pre-fix output in the om-65nv FIRED-RESULT comment.

// gpsMarker is a recognizable sentinel buried inside each format's metadata
// segment/chunk. Asserting it is PRESENT before strip and ABSENT after proves the
// GPS-bearing metadata was actually removed — not merely that some bytes changed.
var gpsMarker = []byte("GPS-om65nv-lat37.7749-lon-122.4194")

// TestStripRemovesMetadataAllFormats builds a real JPEG, PNG and WebP, each
// carrying an EXIF/GPS marker, and asserts StripJPEGMetadata removes the marker
// while the image still decodes at its original dimensions (AC1, AC2, AC4).
func TestStripRemovesMetadataAllFormats(t *testing.T) {
	cases := []struct {
		name         string
		build        func(t *testing.T) []byte
		wantW, wantH int
	}{
		{"jpeg", func(t *testing.T) []byte { return jpegWithEXIF(t, 16, 12) }, 16, 12},
		{"png", func(t *testing.T) []byte { return pngWithMetadata(t, 16, 12) }, 16, 12},
		{"webp", func(t *testing.T) []byte { return webpWithEXIF(t) }, 8, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.build(t)
			// The fixture must be a real, decodable image carrying the marker.
			if !bytes.Contains(in, gpsMarker) {
				t.Fatalf("%s fixture is missing the GPS marker before strip", tc.name)
			}
			cfg0, _, err := image.DecodeConfig(bytes.NewReader(in))
			if err != nil {
				t.Fatalf("%s fixture does not decode: %v", tc.name, err)
			}
			if cfg0.Width != tc.wantW || cfg0.Height != tc.wantH {
				t.Fatalf("%s fixture dims %dx%d, want %dx%d", tc.name, cfg0.Width, cfg0.Height, tc.wantW, tc.wantH)
			}

			out := StripJPEGMetadata(in)

			if bytes.Contains(out, gpsMarker) {
				t.Fatalf("%s: GPS marker still present after strip — metadata not removed", tc.name)
			}
			// Dimensions preserved on both the header read and a full decode (AC2).
			cfg1, _, err := image.DecodeConfig(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("%s: stripped output no longer decodes (DecodeConfig): %v", tc.name, err)
			}
			if cfg1.Width != tc.wantW || cfg1.Height != tc.wantH {
				t.Fatalf("%s: stripped dims %dx%d, want %dx%d", tc.name, cfg1.Width, cfg1.Height, tc.wantW, tc.wantH)
			}
			if _, _, err := image.Decode(bytes.NewReader(out)); err != nil {
				t.Fatalf("%s: full decode of stripped output failed: %v", tc.name, err)
			}
		})
	}
}

// TestStripLeavesUnknownBytesUnchanged pins the safety posture (AC1 final bullet):
// anything that is not a recognizable JPEG/PNG/WebP, or that desyncs mid-parse, is
// returned byte-for-byte unchanged — we never corrupt bytes we do not understand.
func TestStripLeavesUnknownBytesUnchanged(t *testing.T) {
	cases := map[string][]byte{
		"nil":              nil,
		"plain-text":       []byte("this is not an image at all"),
		"truncated-png":    {0x89, 'P', 'N', 'G', 0x0d, 0x0a}, // PNG sig, cut short
		"riff-not-webp":    []byte("RIFF\x10\x00\x00\x00AVI short"),
		"webp-short":       []byte("RIFFxxxxWEBP"),          // header only, no chunks
		"png-bogus-length": pngWithBogusChunkLength(t),      // valid sig, chunk claims huge len
		"webp-bogus-size":  webpWithBogusChunkSize(t),       // valid header, chunk overruns
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out := StripJPEGMetadata(in)
			if !bytes.Equal(out, in) {
				t.Fatalf("%s: input was modified; strip must return desync/unknown input unchanged\n in=%x\nout=%x", name, in, out)
			}
		})
	}
}

// jpegWithEXIF encodes a real JPEG and splices an APP1 (Exif) segment carrying
// gpsMarker immediately after the SOI.
func jpegWithEXIF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 8), uint8(y * 8), 64, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	src := buf.Bytes()
	payload := append([]byte("Exif\x00\x00"), gpsMarker...)
	seg := []byte{0xFF, 0xE1}
	segLen := len(payload) + 2 // the 2 length bytes are part of the count
	seg = append(seg, byte(segLen>>8), byte(segLen))
	seg = append(seg, payload...)
	out := append([]byte{}, src[:2]...) // SOI
	out = append(out, seg...)           // our APP1 EXIF
	out = append(out, src[2:]...)       // the rest of the JPEG
	return out
}

// pngWithMetadata encodes a real PNG and injects an eXIf and a tEXt chunk (both
// carrying gpsMarker) between IHDR and the image data.
func pngWithMetadata(t *testing.T, w, h int) []byte {
	t.Helper()
	base := smallPNG(t, w, h)
	exif := pngChunk("eXIf", append([]byte("Exif\x00\x00"), gpsMarker...))
	text := pngChunk("tEXt", append([]byte("Comment\x00"), gpsMarker...))
	const ihdrEnd = 8 + 12 + 13 // 8-byte sig + IHDR (len+type+13 data+crc)
	out := append([]byte{}, base[:ihdrEnd]...)
	out = append(out, exif...)
	out = append(out, text...)
	out = append(out, base[ihdrEnd:]...)
	return out
}

// pngChunk builds a well-formed PNG chunk (length + type + data + CRC-32).
func pngChunk(typ string, data []byte) []byte {
	out := make([]byte, 0, len(data)+12)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(data)))
	out = append(out, l[:]...)
	out = append(out, typ...)
	out = append(out, data...)
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], crc32.ChecksumIEEE(append([]byte(typ), data...)))
	return append(out, c[:]...)
}

// baseWebPB64 is a real 8x8 lossless WebP (RIFF/WEBP/VP8L) with no metadata,
// generated with ImageMagick. The tests inject/strip an EXIF chunk around it.
const baseWebPB64 = "UklGRhwAAABXRUJQVlA4TA8AAAAvB8ABAAcQUcD+ByKi/wEA"

func decodeBaseWebP(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(baseWebPB64)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// webpWithEXIF appends an 'EXIF' RIFF chunk (carrying gpsMarker) after the image
// chunk and fixes the top-level RIFF size. The decoder returns at the VP8L chunk,
// so the fixture decodes with or without the EXIF chunk.
func webpWithEXIF(t *testing.T) []byte {
	t.Helper()
	out := decodeBaseWebP(t)
	out = append(out, webpChunk("EXIF", append([]byte("Exif\x00\x00"), gpsMarker...))...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out
}

// webpChunk builds a RIFF chunk (fourcc + u32-LE size + payload, padded to even).
func webpChunk(fourcc string, payload []byte) []byte {
	out := make([]byte, 0, len(payload)+9)
	out = append(out, fourcc...)
	var s [4]byte
	binary.LittleEndian.PutUint32(s[:], uint32(len(payload)))
	out = append(out, s[:]...)
	out = append(out, payload...)
	if len(payload)&1 == 1 {
		out = append(out, 0) // pad to even
	}
	return out
}

// pngWithBogusChunkLength returns a valid PNG signature followed by a chunk header
// that claims a length far past the buffer — a desync the stripper must not act on.
func pngWithBogusChunkLength(t *testing.T) []byte {
	t.Helper()
	sig := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	out := append([]byte{}, sig...)
	out = append(out, 0xFF, 0xFF, 0xFF, 0xF0) // absurd length
	out = append(out, "eXIf"...)
	return append(out, 0x00, 0x01, 0x02) // a few bytes, nowhere near the claim
}

// webpWithBogusChunkSize returns a valid WebP header whose first chunk claims a
// payload larger than the file — the stripper must return it unchanged.
func webpWithBogusChunkSize(t *testing.T) []byte {
	t.Helper()
	out := append([]byte{}, "RIFF"...)
	out = append(out, 0x14, 0x00, 0x00, 0x00) // riff size (unused by the guard)
	out = append(out, "WEBP"...)
	out = append(out, "EXIF"...)
	out = append(out, 0xF0, 0xFF, 0xFF, 0xFF) // chunk size overruns the buffer
	return append(out, 0x00, 0x01)
}

// bombPNG builds a minimal VALID PNG header (correct IHDR CRC) claiming w×h dimensions with no
// pixel data — enough for image.DecodeConfig to read the size and refuse it before a decode.
func bombPNG(w, h int) []byte {
	sig := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // color type: truecolor
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
