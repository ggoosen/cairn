package derive

// P2-7: heavy derivatives (spec §8.3) — OCR, image captioning, audio
// transcription, multimodal summarisation. OPT-IN (the daemon gates on the
// operator setting), sandboxed (MIME sniffed from bytes not filename; size +
// timeout caps; external tools get no network; the derived text is untrusted
// and tied to the source hash). This file ships the framework + two reference
// image extractors: a pure-Go metadata extractor (always available) and a
// tesseract OCR extractor (used when the binary is installed, else it degrades
// cleanly to the next extractor).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register decoders for DecodeConfig
	_ "image/jpeg" // (config-only; no full decode)
	_ "image/png"
	"os"
	"os/exec"
	"strings"

	"github.com/ggoosen/cairn/internal/config"
)

// ErrToolUnavailable signals a heavy extractor whose external tool is not
// installed on this node — ExtractHeavy skips to the next candidate.
var ErrToolUnavailable = errors.New("heavy-derivative tool not installed on this node")

// HeavyExtractor is one opt-in P2 extractor.
type HeavyExtractor interface {
	Handles(kind string) bool
	Extract(ctx context.Context, data []byte) (*Result, error)
}

// heavyRegistry is tried in order; the first SUCCESS wins. Tesseract (real OCR)
// is preferred; the pure-Go metadata extractor is the always-available floor.
//
// DEFERRED HARDENING — READ BEFORE ADDING AN EXTRACTOR (P2H7 / shakedown FIND-2):
// network isolation for the current extractors is BY CONSTRUCTION — tesseract
// and the pure-Go metadata reader make no network calls — NOT OS-enforced. There
// is no sandbox-exec / seccomp / network namespace around the subprocess. Before
// registering ANY network-capable or heavier ML extractor here (a captioning
// model, a transcription service client, anything that could open a socket),
// wrap the subprocess in an OS-level network/process sandbox first; the
// by-construction guarantee does not survive an extractor that CAN network. The
// per-process WaitDelay (see tesseractExtractor.Extract) and stripped env are
// also best-effort, not a jail.
var heavyRegistry = []HeavyExtractor{tesseractExtractor{}, imageMetadataExtractor{}}

// SniffHeavy detects P2 heavy-derivative content kinds from the BYTES.
func SniffHeavy(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")),
		bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}),
		bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")),
		bytes.HasPrefix(data, []byte("RIFF")) && len(data) > 12 && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image"
	case bytes.HasPrefix(data, []byte("OggS")), bytes.HasPrefix(data, []byte("RIFF")) && len(data) > 12 && bytes.Equal(data[8:12], []byte("WAVE")),
		bytes.HasPrefix(data, []byte("ID3")), bytes.HasPrefix(data, []byte("fLaC")):
		return "audio"
	}
	return ""
}

// ExtractHeavy runs the first registered heavy extractor that handles the
// sniffed kind and succeeds (opt-in — the CALLER gates on the operator
// setting). ErrUnsupported when nothing handles the content.
func ExtractHeavy(ctx context.Context, data []byte) (*Result, error) {
	if len(data) > config.DeriveMaxBytes {
		return nil, fmt.Errorf("blob %d bytes exceeds the %d-byte extraction cap", len(data), config.DeriveMaxBytes)
	}
	kind := SniffHeavy(data)
	if kind == "" {
		return nil, ErrUnsupported
	}
	// FIX-H5 (R48): pre-flight bomb/dimension guards BEFORE any extractor runs.
	// tesseract (the first registered extractor) decodes the FULL image, so an
	// adversarial attachment must be rejected here — from the header only, no
	// pixel allocation — never inside a decoder that would already have OOMed.
	if kind == "image" {
		if err := preflightImage(data); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, config.HeavyDeriveTimeout)
	defer cancel()

	var lastErr error
	for _, ex := range heavyRegistry {
		if !ex.Handles(kind) {
			continue
		}
		res, err := runContained(ctx, ex, data)
		if err != nil {
			lastErr = err
			continue // ErrToolUnavailable / extractor error → try the next
		}
		if len(res.Text) > config.DeriveMaxTextBytes {
			res.Text = res.Text[:config.DeriveMaxTextBytes]
		}
		if strings.TrimSpace(res.Text) == "" {
			lastErr = fmt.Errorf("empty heavy extraction (%s)", res.DerivativeType)
			continue
		}
		return res, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrUnsupported
}

// preflightImage rejects a bomb/malformed/oversized image from its HEADER only
// (image.DecodeConfig reads dimensions without allocating pixels), so no
// extractor ever decodes an adversarial image (FIX-H5 / R48). A rejection is a
// plain error (not ErrUnsupported), so the caller records a clean derivative.fail.
func preflightImage(data []byte) error {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("preflight: undecodable %s image header: %w", format, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("preflight: non-positive image dimensions %dx%d", cfg.Width, cfg.Height)
	}
	if cfg.Width > config.HeavyMaxImageDimension || cfg.Height > config.HeavyMaxImageDimension {
		return fmt.Errorf("preflight: image %dx%d exceeds the %d px per-side cap (dimension bomb)",
			cfg.Width, cfg.Height, config.HeavyMaxImageDimension)
	}
	pixels := int64(cfg.Width) * int64(cfg.Height)
	if pixels > config.HeavyMaxImagePixels {
		return fmt.Errorf("preflight: %d pixels exceeds the %d-pixel ceiling (pixel-flood bomb)",
			pixels, int64(config.HeavyMaxImagePixels))
	}
	// decompression ratio: decoded RGBA bytes vs the compressed input. A tiny
	// file that decodes to a huge raster is the classic decompression bomb.
	if len(data) > 0 {
		if ratio := (pixels * 4) / int64(len(data)); ratio > config.HeavyMaxDecompressionRatio {
			return fmt.Errorf("preflight: decompression ratio %d exceeds %d (decompression bomb)",
				ratio, config.HeavyMaxDecompressionRatio)
		}
	}
	return nil
}

// runContained runs an extractor with panic containment (the timeout lives on
// the passed ctx; external tools honor it via CommandContext).
func runContained(ctx context.Context, ex HeavyExtractor, data []byte) (res *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("heavy extractor panic contained: %v", r)
		}
	}()
	return ex.Extract(ctx, data)
}

// --- reference extractor 1: pure-Go image metadata (always available) --------

type imageMetadataExtractor struct{}

func (imageMetadataExtractor) Handles(kind string) bool { return kind == "image" }

func (imageMetadataExtractor) Extract(_ context.Context, data []byte) (*Result, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("image metadata: %w", err)
	}
	return &Result{
		DerivativeType: "image_metadata",
		Text:           fmt.Sprintf("image %s %dx%d pixels", format, cfg.Width, cfg.Height),
		Extractor:      "cairn-image-metadata",
		Version:        config.HeavyExtractorVersion,
	}, nil
}

// --- reference extractor 2: tesseract OCR (opt-in, external, degrades) --------

type tesseractExtractor struct{}

func (tesseractExtractor) Handles(kind string) bool { return kind == "image" }

func (tesseractExtractor) Extract(ctx context.Context, data []byte) (*Result, error) {
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		return nil, ErrToolUnavailable
	}
	f, err := os.CreateTemp("", "cairn-ocr-*.img")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	// `tesseract <in> stdout` — no network by construction; ctx bounds runtime.
	cmd := exec.CommandContext(ctx, bin, f.Name(), "stdout")
	cmd.Env = []string{} // no inherited environment (best-effort isolation)
	// P2H7 / shakedown FIND-3: bound cleanup after a ctx-kill so a child that
	// inherited stdout could not keep Output() blocked past the deadline. Inert
	// for tesseract (no child); load-bearing for any future child-spawning
	// extractor. See config.HeavyExtractorWaitDelay.
	cmd.WaitDelay = config.HeavyExtractorWaitDelay
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tesseract OCR failed: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return nil, errors.New("tesseract found no text")
	}
	return &Result{
		DerivativeType: "ocr_text",
		Text:           text,
		Extractor:      "tesseract-ocr",
		Version:        config.HeavyExtractorVersion,
	}, nil
}
