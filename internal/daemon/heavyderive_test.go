package daemon_test

// P2-7: heavy derivatives (opt-in). With the pipeline enabled, an image
// attachment gains a heavy derivative (OCR when tesseract is present, else the
// pure-Go image-metadata floor) — searchable, provenance-bound. With it
// disabled, images have no deterministic extractor and produce no derivative
// text (the default; heavy work is opt-in).

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{9, 9, 9, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func deriveDrain(t *testing.T, d *daemon.Daemon) {
	t.Helper()
	for {
		n, err := d.DeriveOnce(64)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

func TestP27HeavyDerivativesOptIn(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetHeavyDerivativesForTest(true)

	pub, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "here is a screenshot",
		Attachments: []daemon.AttachmentIn{{Data: pngBytes(t, 13, 17), Filename: "shot.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deriveDrain(t, d)

	derivs, err := d.Projection().DerivativesForMessage(pub.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	var got *string
	for i := range derivs {
		dt := derivs[i].DerivativeType
		if dt == "image_metadata" || dt == "ocr_text" {
			got = &derivs[i].DerivativeType
		}
	}
	if got == nil {
		t.Fatalf("no heavy derivative produced for the image: %+v", derivs)
	}
}

func TestP27HeavyDerivativesDisabledByDefault(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// heavyDerive defaults off (no CAIRN_HEAVY_DERIVATIVES).

	pub, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "screenshot, no heavy pipeline",
		Attachments: []daemon.AttachmentIn{{Data: pngBytes(t, 5, 5), Filename: "x.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deriveDrain(t, d)

	derivs, err := d.Projection().DerivativesForMessage(pub.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, dv := range derivs {
		if dv.DerivativeType == "image_metadata" || dv.DerivativeType == "ocr_text" {
			t.Fatalf("heavy derivative produced while opt-out: %+v", dv)
		}
	}
}
