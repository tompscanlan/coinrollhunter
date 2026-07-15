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
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"strings"

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
	// bytes of work instead of gigabytes of RAM (N2 REQUIRED GUARD, AC4). The cap bounds the
	// DECODE ALLOCATION, not just the header: image.Decode allocates ~4 bytes/pixel (RGBA),
	// so 50 MP holds a single decode near ~200 MB — and that decode recurs on every lazy
	// regen cache miss, so a too-generous ceiling is a standing cost, not a one-off. 50 MP
	// still comfortably covers a 48/50 MP phone photo; a small compressed file whose header
	// claims more is refused before a byte of pixel buffer is touched.
	maxPixels    = 50_000_000 // 50 megapixels (~200 MB RGBA decode ceiling)
	maxDimension = 20000      // and no single edge past this
)

// ErrTooLarge is returned by CheckConfig when a decoded image would exceed the bomb
// ceiling. ErrUnsupported is returned by Sniff for bytes that are not an accepted image.
var (
	ErrTooLarge    = errors.New("image dimensions exceed the safe limit")
	ErrUnsupported = errors.New("unsupported image type")
)

// Sniff returns the canonical extension for the bytes, by MAGIC BYTES
// (http.DetectContentType), never the client's filename — ext is a path segment and must
// not be user-controlled text (c/N2 sub-decision). It recognizes the accepted IMAGE types
// (jpg|png|webp) AND the document types (pdf, om-9o4n.2); a non-image/non-document, or a
// gif/bmp/… we do not accept, returns ErrUnsupported. The caller branches on IsDocument to
// decide whether the byte stream enters the imaging pipeline or is stored as-is.
func Sniff(data []byte) (string, error) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "jpg", nil
	case "image/png":
		return "png", nil
	case "image/webp":
		return "webp", nil
	case "application/pdf":
		// A PDF is a DOCUMENT attachment (a receipt scan/invoice), not decodable pixels.
		// http.DetectContentType matches the "%PDF-" magic, so we recognize it here and
		// return "pdf"; the upload/serve handlers then branch on IsDocument and SKIP the
		// whole decode/bomb-guard/derivative pipeline, which assumes an image (om-9o4n.2).
		return "pdf", nil
	default:
		return "", ErrUnsupported
	}
}

// docExts is the CLOSED set of attachment types that are STORED + LINKED but never imaged.
// A PDF is the only member in v1: it has no decodable pixels, so CheckConfig, Derive and
// StripJPEGMetadata (all of which assume an image) must never see it. Mirrors the ext
// whitelist in model.Photo.Validate — ext is a path segment, so the set stays closed, not
// arbitrary text. Add a new document type in BOTH places (here and validate) deliberately.
var docExts = map[string]bool{"pdf": true}

// IsDocument reports whether ext names a document attachment (a PDF today) rather than a
// decodable image. Callers branch on THIS, not on a "pdf" string literal, so the "skip
// imaging for a doc" decision lives in one place per concern (upload branch, serve branch).
// Case-folded because contentTypeForExt and the stored ext are compared lowercase.
func IsDocument(ext string) bool {
	return docExts[strings.ToLower(ext)]
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
	// Re-run the bomb guard on EVERY decode, not just at ingest. This is also the lazy
	// regen path (serveDerivative reads an original off disk on a cache miss), and an
	// original that arrived by backup-restore, hand-copy, or corruption never passed
	// through ingest's CheckConfig — so a small compressed file claiming huge dimensions
	// would otherwise force a full-size RGBA allocation here on every cache miss. The check
	// is header-only and cheap; making Derive self-guard means no decode path can forget it.
	if _, _, err := CheckConfig(data); err != nil {
		return nil, err
	}
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

// StripJPEGMetadata removes embedded camera metadata (EXIF / GPS / XMP) from an
// uploaded image, leaving the actual image data intact — the "keep the raw original,
// minus the camera metadata" strip the EXIF setting asks for (N4). Despite the name
// (kept for the api/photos.go call site, om-65nv), it now covers ALL THREE accepted
// formats: it dispatches on the magic bytes and drops each container's metadata
// segments —
//
//   - JPEG: the APP1 (EXIF/XMP) marker segments.
//   - PNG:  the eXIf / tEXt / zTXt / iTXt ancillary chunks.
//   - WebP: the 'EXIF' and 'XMP ' RIFF chunks (recomputing the top-level RIFF size).
//
// Everything else is copied through byte-for-byte. Anything that is NOT a recognizable
// JPEG/PNG/WebP — or that desyncs mid-parse — is returned UNCHANGED (never corrupt bytes
// we do not understand), so the caller can apply it unconditionally when the setting is
// on. Only metadata is removed, so every stripped output still decodes at the same
// dimensions.
func StripJPEGMetadata(data []byte) []byte {
	switch {
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8:
		return stripJPEG(data)
	case bytes.HasPrefix(data, pngSignature):
		return stripPNG(data)
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return stripWebP(data)
	default:
		return data // not an accepted image — nothing to strip here
	}
}

// stripJPEG walks the JPEG marker structure and drops only APP1 (EXIF/XMP); everything
// else (JFIF/APP0, quantization tables, the entropy-coded scan) is copied through
// untouched. A desync copies the remainder verbatim — the pre-om-65nv behavior, kept.
func stripJPEG(data []byte) []byte {
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

// pngSignature is the 8-byte PNG file magic.
var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// pngMetaChunks are the ancillary chunk types that carry camera/EXIF/GPS/text
// metadata. Dropping them removes the data; the remaining chunks are independent and
// keep their existing CRCs (chunk CRCs cover only that chunk, so no recompute is needed).
var pngMetaChunks = map[string]bool{"eXIf": true, "tEXt": true, "zTXt": true, "iTXt": true}

// stripPNG walks the PNG chunk stream (8-byte signature, then repeating
// length(4)+type(4)+data+crc(4)) and drops the metadata chunks, copying every other
// chunk (IHDR/PLTE/IDAT/IEND/…) byte-identically. Any malformed/oversized chunk length
// is treated as a desync and the ORIGINAL bytes are returned unchanged.
func stripPNG(data []byte) []byte {
	out := make([]byte, 0, len(data))
	out = append(out, data[:8]...) // signature
	i, dropped := 8, false
	for i+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[i:]))
		end := i + 12 + length // len(4) + type(4) + data(length) + crc(4)
		if length < 0 || end < i || end > len(data) {
			return data // desync — do not corrupt bytes we cannot parse
		}
		ctype := string(data[i+4 : i+8])
		if pngMetaChunks[ctype] {
			dropped = true
		} else {
			out = append(out, data[i:end]...)
		}
		i = end
		if ctype == "IEND" {
			break // IEND is the final chunk; stop walking
		}
	}
	if !dropped {
		return data // no metadata chunk present — leave the file byte-identical
	}
	if i < len(data) {
		out = append(out, data[i:]...) // preserve any trailing bytes verbatim
	}
	return out
}

// webpMetaChunks are the RIFF chunk fourccs that carry metadata. Dropping the chunk is
// what removes the data (clearing the VP8X flag bits is optional, om-65nv).
var webpMetaChunks = map[string]bool{"EXIF": true, "XMP ": true}

// stripWebP walks the WebP RIFF container ('RIFF''WEBP' then repeating fourcc(4)+u32-LE
// size+payload+pad-to-even) and drops the metadata chunks, then RECOMPUTES the top-level
// RIFF size to reflect the removed bytes. A chunk whose payload overruns the buffer is a
// desync and the ORIGINAL bytes are returned unchanged.
func stripWebP(data []byte) []byte {
	out := make([]byte, 0, len(data))
	out = append(out, data[:12]...) // 'RIFF' + size (fixed up below) + 'WEBP'
	i, dropped := 12, false
	for i+8 <= len(data) {
		size := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		dataEnd := i + 8 + size
		if size < 0 || dataEnd < i || dataEnd > len(data) {
			return data // desync — do not corrupt bytes we cannot parse
		}
		// RIFF pads odd-sized payloads to even alignment; the final chunk may omit the
		// pad byte, so only consume it when it is actually present.
		chunkEnd := dataEnd
		if size&1 == 1 && dataEnd < len(data) {
			chunkEnd++
		}
		if webpMetaChunks[string(data[i:i+4])] {
			dropped = true
		} else {
			out = append(out, data[i:chunkEnd]...)
		}
		i = chunkEnd
	}
	if !dropped {
		return data // no metadata chunk present — leave the file byte-identical
	}
	if i < len(data) {
		out = append(out, data[i:]...) // preserve any trailing bytes verbatim
	}
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8)) // RIFF size = bytes after it
	return out
}
