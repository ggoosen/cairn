package derive

// FIX-P2-2 (hardens FIX-H5 / R48): the derivative pre-flight must budget
// decoded memory by the decoder's ACTUAL target format — RGBA (4 bytes/pixel)
// regardless of the source channel count — and enforce absolute per-side and
// total-pixel ceilings, never only a byte-product budget. A guard that budgets
// by SOURCE bytes-per-pixel lets a grayscale (1 B/px) or paletted image
// declare a raster whose RGBA expansion OOMs the enricher. The repro class:
// a 60000×60000 grayscale PNG (3.6 Gpx → 14.4 GB as RGBA).
//
// Every rejection must be a clean error (a derivative.fail at the daemon
// layer) with BOUNDED memory: the assertions below cap total allocations
// during the attempt, so a guard regression that lets any decoder allocate
// the raster fails loudly here rather than OOMing CI.

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"runtime"
	"strings"
	"testing"
)

// grayPNGHeader builds a minimal PNG (signature + IHDR) declaring width×height
// with the given color type and bit depth. DecodeConfig reads dimensions from
// IHDR alone, so these are complete adversarial inputs in ~45 bytes.
func grayPNGHeader(w, h uint32, colorType, bitDepth byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = bitDepth, colorType
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, 13)
	b.Write(length)
	b.WriteString("IHDR")
	b.Write(ihdr)
	cb := make([]byte, 4)
	binary.BigEndian.PutUint32(cb, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	b.Write(cb)
	return b.Bytes()
}

// rejectedBounded asserts the input is rejected by pre-flight AND that the
// attempt allocated less than maxAllocBytes in total — i.e. no decoder ever
// touched the declared raster.
func rejectedBounded(t *testing.T, name string, data []byte, maxAllocBytes uint64) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := ExtractHeavy(context.Background(), data)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatalf("%s: expected rejection, got a result (guard did not fire)", name)
	}
	if err == ErrUnsupported {
		t.Fatalf("%s: rejected as unsupported, want a bomb/guard error", name)
	}
	if !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("%s: rejected, but not by the pre-flight guard (a decoder ran): %v", name, err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > maxAllocBytes {
		t.Fatalf("%s: attempt allocated %d bytes (> %d) — decoded memory not bounded", name, alloc, maxAllocBytes)
	}
}

func TestP2GrayscaleBombRejectedBounded(t *testing.T) {
	const maxAlloc = 8 << 20 // 8 MiB — orders of magnitude below any raster

	// THE repro: 60000×60000 grayscale (color type 0, 1 B/px at source). A
	// source-format byte budget reads it as 3.6 GB; the decoder's RGBA target
	// is 14.4 GB. Must die in pre-flight on the absolute ceilings.
	rejectedBounded(t, "grayscale-bomb", grayPNGHeader(60000, 60000, 0, 8), maxAlloc)

	// grayscale pixel-flood inside the per-side cap: 8000×8000 gray.
	rejectedBounded(t, "grayscale-pixel-flood", grayPNGHeader(8000, 8000, 0, 8), maxAlloc)

	// 1-bit paletted variant: source budget 450 MB at 1 bpp reads even
	// smaller; RGBA target is identical. Same ceilings must fire.
	rejectedBounded(t, "paletted-bomb", grayPNGHeader(60000, 60000, 3, 1), maxAlloc)

	// 16-bit grayscale+alpha — a >4 B/px source; ceilings still govern.
	rejectedBounded(t, "gray16-alpha-bomb", grayPNGHeader(60000, 60000, 4, 16), maxAlloc)
}

// TestP2PreflightBudgetsRGBATarget pins the budgeting rule itself: the
// decompression-ratio guard must compute the DECODED size as pixels × 4
// (RGBA target) — so a grayscale image with the same dimensions is budgeted
// identically to a truecolor one, never at 1 byte per source pixel.
func TestP2PreflightBudgetsRGBATarget(t *testing.T) {
	gray := grayPNGHeader(6000, 6000, 0, 8)  // 36 MP, inside side+pixel caps
	color := grayPNGHeader(6000, 6000, 2, 8) // identical dims, truecolor
	gerr := preflightImage(gray)
	cerr := preflightImage(color)
	if gerr == nil || cerr == nil {
		t.Fatalf("36 MP header-only bombs must trip the ratio guard: gray=%v color=%v", gerr, cerr)
	}
	if !strings.Contains(gerr.Error(), "ratio") || !strings.Contains(cerr.Error(), "ratio") {
		t.Fatalf("expected decompression-ratio rejections, got gray=%v color=%v", gerr, cerr)
	}
	// the computed ratio must be channel-independent: identical for identical
	// dimensions, regardless of source color type.
	gratio, cratio := ratioFromErr(t, gerr.Error()), ratioFromErr(t, cerr.Error())
	if gratio != cratio {
		t.Fatalf("ratio budget depends on source channels (gray %s vs color %s) — must budget the RGBA target", gratio, cratio)
	}
}

func ratioFromErr(t *testing.T, msg string) string {
	t.Helper()
	f := strings.Fields(msg)
	for i, w := range f {
		if w == "ratio" && i+1 < len(f) {
			return f[i+1]
		}
	}
	t.Fatalf("no ratio in %q", msg)
	return ""
}
