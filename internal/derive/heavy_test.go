package derive

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func tinyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSniffHeavy(t *testing.T) {
	if SniffHeavy(tinyPNG(t, 2, 2)) != "image" {
		t.Fatal("PNG not sniffed as image")
	}
	if SniffHeavy([]byte{0xFF, 0xD8, 0xFF, 0xE0}) != "image" {
		t.Fatal("JPEG magic not sniffed as image")
	}
	if SniffHeavy([]byte("OggS....")) != "audio" {
		t.Fatal("Ogg not sniffed as audio")
	}
	if SniffHeavy([]byte("just plain text")) != "" {
		t.Fatal("plain text wrongly sniffed as heavy content")
	}
}

func TestExtractHeavyImageMetadataAlwaysAvailable(t *testing.T) {
	// tesseract may or may not be installed; either way the pure-Go metadata
	// extractor guarantees a result for an image.
	res, err := ExtractHeavy(context.Background(), tinyPNG(t, 7, 11))
	if err != nil {
		t.Fatalf("ExtractHeavy(image): %v", err)
	}
	if res.DerivativeType != "ocr_text" && res.DerivativeType != "image_metadata" {
		t.Fatalf("unexpected derivative type %q", res.DerivativeType)
	}
	if res.DerivativeType == "image_metadata" {
		if !strings.Contains(res.Text, "7x11") || !strings.Contains(res.Text, "png") {
			t.Fatalf("image metadata text wrong: %q", res.Text)
		}
		if res.Extractor != "cairn-image-metadata" {
			t.Fatalf("extractor %q", res.Extractor)
		}
	}
}

func TestExtractHeavyUnsupportedContent(t *testing.T) {
	if _, err := ExtractHeavy(context.Background(), []byte("plain text, not heavy")); err != ErrUnsupported {
		t.Fatalf("plain text should be ErrUnsupported, got %v", err)
	}
}
