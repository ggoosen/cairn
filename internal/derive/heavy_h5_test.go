package derive

// FIX-H5 (R48): pre-flight bomb/pixel guards before any heavy extractor. Mesh
// attachments are untrusted and tesseract decodes the FULL image, so an
// adversarial image (tiny file, enormous declared dimensions) must be rejected
// from the HEADER — no pixel allocation, no OOM — and surface as a clean
// failure, never a hang or crash.

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// pngHeader builds a minimal PNG (signature + IHDR) declaring width×height.
// image.DecodeConfig reads the dimensions from IHDR without needing IDAT, so a
// 60000×60000 "image" is a decompression bomb in ~45 bytes.
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // color type: truecolor RGB
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, 13)
	b.Write(length)
	b.WriteString("IHDR")
	b.Write(ihdr)
	crc := crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...))
	cb := make([]byte, 4)
	binary.BigEndian.PutUint32(cb, crc)
	b.Write(cb)
	return b.Bytes()
}

func rejected(t *testing.T, name string, data []byte) {
	t.Helper()
	_, err := ExtractHeavy(context.Background(), data)
	if err == nil {
		t.Fatalf("%s: expected rejection, got a result (guard did not fire)", name)
	}
	if err == ErrUnsupported {
		t.Fatalf("%s: rejected as unsupported, want a bomb/malformed error", name)
	}
}

func TestH5PreflightRejectsBombsAndMalformed(t *testing.T) {
	// dimension bomb: 60000×60000 trips the per-side cap (and ratio) — from ~45 B.
	rejected(t, "dimension-bomb", pngHeader(60000, 60000))
	// pixel-flood: 8000×8000 (each side under the per-side cap) trips the pixel
	// ceiling and the decompression ratio.
	rejected(t, "pixel-flood", pngHeader(8000, 8000))
	// malformed: PNG magic then garbage — DecodeConfig fails, so it's rejected.
	rejected(t, "malformed", append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("not a real IHDR chunk............")...))
}

// a LEGIT small image still passes preflight and yields a derivative — the guard
// only rejects adversarial input, never ordinary attachments.
func TestH5PreflightPassesLegitImage(t *testing.T) {
	res, err := ExtractHeavy(context.Background(), tinyPNG(t, 24, 36))
	if err != nil {
		t.Fatalf("legit 24x36 image rejected by preflight: %v", err)
	}
	if res.DerivativeType != "image_metadata" && res.DerivativeType != "ocr_text" {
		t.Fatalf("unexpected derivative type %q", res.DerivativeType)
	}
}
