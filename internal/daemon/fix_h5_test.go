package daemon_test

// FIX-H5 (R48): the enricher survives an adversarial image attachment. With the
// heavy pipeline enabled, a decompression-bomb image (tiny file, enormous
// declared dimensions) must produce a clean derivative.fail — no OOM, no hang —
// and the daemon keeps working. The pre-flight guard rejects it from the header
// before any extractor (tesseract) decodes it.

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

// bombPNGHeader is a ~45-byte PNG (signature + IHDR only) declaring width×height
// — a decompression bomb that never allocates its pixels.
func bombPNGHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // bit depth 8, truecolor RGB
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

func TestH5EnricherSurvivesImageBomb(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetHeavyDerivativesForTest(true)

	pub, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "adversarial screenshot",
		Attachments: []daemon.AttachmentIn{{Data: bombPNGHeader(60000, 60000), Filename: "bomb.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// must complete without OOM/hang; the guard rejects before tesseract decodes.
	deriveDrain(t, d)

	derivs, err := d.Projection().DerivativesForMessage(pub.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, dv := range derivs {
		if dv.DerivativeType == "ocr_text" || dv.DerivativeType == "image_metadata" {
			t.Fatalf("bomb produced a successful derivative (guard did not fire): %+v", dv)
		}
		if dv.Status == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("bomb did not record a clean derivative.fail: %+v", derivs)
	}

	// the daemon is unharmed: an ordinary send + search still works.
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzalive after bomb"}); err != nil {
		t.Fatalf("daemon broken after bomb: %v", err)
	}
	if got := searchCount(t, d, "zzalive"); got != 1 {
		t.Fatalf("search broken after bomb: got %d/1", got)
	}
}
