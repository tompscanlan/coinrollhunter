package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
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
