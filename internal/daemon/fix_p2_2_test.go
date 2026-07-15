package daemon_test

// FIX-P2-2 (hardens FIX-H5 / R48) — daemon leg: the grayscale-bomb repro (a
// 60000×60000 grayscale PNG whose RGBA expansion is 14.4 GB) and a grayscale
// pixel-flood each produce a clean derivative.fail with BOUNDED memory — the
// enricher never allocates the raster — and the daemon keeps working. With
// the heavy gate off (no CAIRN_HEAVY_DERIVATIVES=1) the same attachments are
// inert: no heavy pipeline runs at all, and nothing OOMs.

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"runtime"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

// buildPNGHeader is bombPNGHeader with a selectable IHDR color type (0 =
// grayscale, 3 = paletted) and bit depth: the source bytes-per-pixel differ,
// the RGBA decode target does not — the guard must budget the target.
func buildPNGHeader(w, h uint32, colorType, bitDepth byte) []byte {
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

func attachAndDrainBounded(t *testing.T, d *daemon.Daemon, name string, data []byte, maxAllocBytes uint64) string {
	t.Helper()
	pub, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "adversarial attachment " + name,
		Attachments: []daemon.AttachmentIn{{Data: data, Filename: name}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	deriveDrain(t, d)
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > maxAllocBytes {
		t.Fatalf("%s: enrichment allocated %d bytes (> %d) — decoded memory not bounded", name, alloc, maxAllocBytes)
	}
	return pub.MessageID
}

func assertCleanFail(t *testing.T, d *daemon.Daemon, msgID, name string) {
	t.Helper()
	derivs, err := d.Projection().DerivativesForMessage(msgID)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, dv := range derivs {
		if dv.DerivativeType == "ocr_text" || dv.DerivativeType == "image_metadata" {
			t.Fatalf("%s produced a successful derivative (guard did not fire): %+v", name, dv)
		}
		if dv.Status == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("%s did not record a clean derivative.fail: %+v", name, derivs)
	}
}

func TestP2GrayscaleBombDaemonSurvivesBounded(t *testing.T) {
	const maxAlloc = 32 << 20 // event/projection overhead only; rasters are GBs

	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetHeavyDerivativesForTest(true)

	// the repro: 60000×60000 grayscale; and a grayscale pixel-flood within the
	// per-side cap. Each must fail cleanly with bounded allocation.
	bombID := attachAndDrainBounded(t, d, "gray-bomb.png", buildPNGHeader(60000, 60000, 0, 8), maxAlloc)
	floodID := attachAndDrainBounded(t, d, "gray-flood.png", buildPNGHeader(8000, 8000, 0, 8), maxAlloc)
	assertCleanFail(t, d, bombID, "grayscale bomb")
	assertCleanFail(t, d, floodID, "grayscale pixel-flood")

	// daemon unharmed: ordinary send + search still work.
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzgraybombalive"}); err != nil {
		t.Fatalf("daemon broken after bombs: %v", err)
	}
	if got := searchCount(t, d, "zzgraybombalive"); got != 1 {
		t.Fatalf("search broken after bombs: got %d/1", got)
	}
}

// With the gate off (default: CAIRN_HEAVY_DERIVATIVES unset) the bomb never
// reaches the heavy pipeline at all — genuinely off, and still bounded.
func TestP2GrayscaleBombGateOffInert(t *testing.T) {
	const maxAlloc = 32 << 20

	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// heavy gate deliberately NOT enabled.

	msgID := attachAndDrainBounded(t, d, "gray-bomb-gated.png", buildPNGHeader(60000, 60000, 0, 8), maxAlloc)
	assertCleanFail(t, d, msgID, "grayscale bomb (gate off)")
}
