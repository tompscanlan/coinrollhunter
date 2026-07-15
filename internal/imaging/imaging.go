// Package imaging is the app's first UNTRUSTED-BYTES surface (om-6hlp): it sniffs,
// bomb-guards, resizes and metadata-strips image bytes a user (or, once the app is
// reachable, an attacker) uploads. It is pure Go — image/jpeg + image/png are stdlib,
// golang.org/x/image/{webp,draw} are pure Go — so the binary stays CGO_ENABLED=0 and
// cross-compiles to win/mac exactly as before (N2, om-9p0l intact).
//
// The store never calls this: the store keeps ONE row per photo (the original) and is
// blind to derivatives, which are a regenerable cache (ADR-009 amendment (e)). The API
// upload/serve handlers orchestrate; this package is the decode/guard/resize kernel.
package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"

	// Register the decoders. image/jpeg + image/png self-register on import; x/image/webp
	// registers "webp" for image.Decode/DecodeConfig the same way. Blank imports because we
	// only need the side-effect registration — the sniff picks the format.
	_ "image/png"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// MaxUploadBytes caps a single upload's ORIGINAL — enforced with http.MaxBytesReader
	// (a real limit on the wire, not advice). 10MB comfortably holds a phone photo; it is
	// the DoS/typo guard (d, om-6hlp), applied to the big one now that we keep the raw
	// original (N2 reversed client-side resize).
	MaxUploadBytes = 10 << 20

	// DisplayEdge / ThumbEdge are the longest-edge caps for the two derivatives. The
	// display variant backs the detail drawer; the thumb backs the grid/feed.
	DisplayEdge = 1600
	ThumbEdge   = 320

	// The decompression-bomb ceiling. DecodeConfig reads these from the header BEFORE any
	// pixel buffer is allocated, so an image claiming absurd dimensions is refused for a few
	// bytes of work instead of gigabytes of RAM (N2 REQUIRED GUARD, AC4).
	maxPixels    = 100_000_000 // 100 megapixels
	maxDimension = 30000       // and no single edge past this
)

// ErrTooLarge is returned by CheckConfig when a decoded image would exceed the bomb
// ceiling. ErrUnsupported is returned by Sniff for bytes that are not an accepted image.
var (
	ErrTooLarge    = errors.New("image dimensions exceed the safe limit")
	ErrUnsupported = errors.New("unsupported image type")
)

// Sniff returns the canonical extension (jpg|png|webp) for the image bytes, by MAGIC
// BYTES (http.DetectContentType), never the client's filename — ext is a path segment
// and must not be user-controlled text (c/N2 sub-decision). A non-image, or a gif/bmp/…
// we do not accept, returns ErrUnsupported.
func Sniff(data []byte) (string, error) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "jpg", nil
	case "image/png":
		return "png", nil
	case "image/webp":
		return "webp", nil
	default:
		return "", ErrUnsupported
	}
}

// CheckConfig reads the image header ONLY (image.DecodeConfig — no full decode, no pixel
// allocation) and refuses dimensions past the bomb ceiling. This runs BEFORE any Derive,
// so a malicious header can never make the server allocate the buffer it is lying about
// (AC4). Returns the width and height on success.
func CheckConfig(data []byte) (w, h int, err error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("decode image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > maxDimension || cfg.Height > maxDimension ||
		int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return cfg.Width, cfg.Height, ErrTooLarge
	}
	return cfg.Width, cfg.Height, nil
}

// Derive decodes the (already guarded) image, downscales it so its longest edge is at
// most maxEdge (never upscales), and re-encodes it as JPEG. Derivatives are always JPEG
// regardless of the original's format — one cache format, small and universally
// renderable. Best-effort by contract: the caller treats a failure as non-fatal and
// falls back to the original, because derivatives are regenerable (N2).
func Derive(data []byte, maxEdge int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	b := src.Bounds()
	nw, nh := fit(b.Dx(), b.Dy(), maxEdge)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// CatmullRom is a high-quality resampler; the images are small and this runs once at
	// ingest (and lazily on a cache miss), so quality beats speed here.
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, fmt.Errorf("encode derivative: %w", err)
	}
	return buf.Bytes(), nil
}

// fit returns the target dimensions that fit (w,h) within a maxEdge box, preserving
// aspect ratio and only ever shrinking.
func fit(w, h, maxEdge int) (int, int) {
	if w <= maxEdge && h <= maxEdge {
		return w, h
	}
	if w >= h {
		nh := h * maxEdge / w
		if nh < 1 {
			nh = 1
		}
		return maxEdge, nh
	}
	nw := w * maxEdge / h
	if nw < 1 {
		nw = 1
	}
	return nw, maxEdge
}

// StripJPEGMetadata removes the APP1 (EXIF / XMP) segments from a JPEG byte stream,
// leaving the image data byte-identical — the "keep the raw original, minus the camera
// metadata" strip the EXIF setting asks for (N4). It walks the marker structure and
// drops only APP1; everything else (JFIF/APP0, quantization tables, the entropy-coded
// scan) is copied through untouched. Non-JPEG input (PNG/WebP, where embedded EXIF is
// rare) is returned unchanged — the magic check below simply falls through — so the
// caller can apply it unconditionally when the setting is on.
func StripJPEGMetadata(data []byte) []byte {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return data // not a JPEG (SOI missing) — nothing to strip here
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8) // SOI
	i := 2
	for i+1 < len(data) {
		if data[i] != 0xFF {
			return append(out, data[i:]...) // desync — copy the remainder verbatim
		}
		marker := data[i+1]
		switch {
		case marker == 0xD9 || marker == 0x01 || marker == 0x00 || marker == 0xFF ||
			(marker >= 0xD0 && marker <= 0xD7):
			// Standalone markers carry no length.
			out = append(out, data[i], data[i+1])
			i += 2
			continue
		case marker == 0xDA:
			// Start of scan: the compressed image data (and EOI) follow with no more
			// parseable segments — copy everything from here to the end.
			return append(out, data[i:]...)
		}
		if i+3 >= len(data) {
			return append(out, data[i:]...)
		}
		segLen := int(data[i+2])<<8 | int(data[i+3]) // includes the 2 length bytes
		if segLen < 2 || i+2+segLen > len(data) {
			return append(out, data[i:]...)
		}
		if marker != 0xE1 { // keep everything except APP1 (EXIF/XMP)
			out = append(out, data[i:i+2+segLen]...)
		}
		i += 2 + segLen
	}
	return out
}
