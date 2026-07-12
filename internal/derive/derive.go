// Package derive implements N4 deterministic derivatives (spec §8.3):
// sandboxed blob → searchable-text extraction for the P1 set — plain/
// source/markdown, sanitised HTML, text-layer PDFs, office (docx) text.
//
// Sandbox contract (spec §8.3): MIME is SNIFFED from content, never trusted
// from filenames; inputs are size-capped, PDFs page-capped, extraction runs
// under a hard timeout with panics contained; output text is size-capped.
// No network by construction: every extractor is a pure function over the
// input bytes — none of the code paths perform any I/O. No macros or
// scripts are ever executed; HTML script/style bodies are dropped.
//
// Determinism: identical input bytes produce identical output text on every
// node (pinned pure-Go extractors, no environment dependence). Extractor
// name+version ride on every derivative event so a future extractor bump
// can invalidate and regenerate.
package derive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	pdf "github.com/ledongthuc/pdf"
	xhtml "golang.org/x/net/html"

	"github.com/ggoosen/cairn/internal/config"
)

// Result is one successful extraction.
type Result struct {
	DerivativeType string // plain | sanitized_html | text_layer | office_text
	Text           string
	Extractor      string
	Version        string
}

// Extractor identities (pinned; bumping a version is an invalidation event).
const (
	extractorPlain = "cairn-plain"
	extractorHTML  = "cairn-html-sanitize"
	extractorPDF   = "go-pdf-textlayer"
	extractorDocx  = "cairn-docx-text"

	// ExtractorVersion pins the whole deterministic extractor set.
	ExtractorVersion = "1"
)

// ErrUnsupported: the sniffed type has no P1 extractor (images, audio,
// encrypted archives, ... are P2 territory).
var ErrUnsupported = errors.New("no deterministic extractor for this content type")

// Sniff returns the detected content class from the BYTES (never the
// filename): pdf | docx | html | text | "" (unsupported/binary).
func Sniff(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "pdf"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")) && isDocx(data):
		return "docx"
	}
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	trimmed := bytes.TrimLeft(head, " \t\r\n")
	lower := bytes.ToLower(trimmed)
	if bytes.HasPrefix(lower, []byte("<!doctype html")) || bytes.HasPrefix(lower, []byte("<html")) ||
		bytes.Contains(lower, []byte("<body")) || bytes.Contains(lower, []byte("<div")) ||
		bytes.Contains(lower, []byte("<p>")) {
		return "html"
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		return "text"
	}
	return ""
}

// Extract runs the sandboxed pipeline over one blob. The timeout contains
// runaway parsers; panics inside an extractor are converted to errors.
func Extract(ctx context.Context, data []byte) (*Result, error) {
	if len(data) > config.DeriveMaxBytes {
		return nil, fmt.Errorf("blob %d bytes exceeds the %d-byte extraction cap", len(data), config.DeriveMaxBytes)
	}
	kind := Sniff(data)
	if kind == "" {
		return nil, ErrUnsupported
	}

	ctx, cancel := context.WithTimeout(ctx, config.DeriveTimeout)
	defer cancel()
	type outcome struct {
		res *Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- outcome{err: fmt.Errorf("extractor panic contained: %v", r)}
			}
		}()
		res, err := extract(kind, data)
		ch <- outcome{res: res, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("extraction timed out after %v", config.DeriveTimeout)
	case o := <-ch:
		if o.err != nil {
			return nil, o.err
		}
		if len(o.res.Text) > config.DeriveMaxTextBytes {
			o.res.Text = o.res.Text[:config.DeriveMaxTextBytes]
		}
		if strings.TrimSpace(o.res.Text) == "" {
			return nil, fmt.Errorf("no extractable text (%s)", o.res.DerivativeType)
		}
		return o.res, nil
	}
}

func extract(kind string, data []byte) (*Result, error) {
	switch kind {
	case "text":
		return &Result{DerivativeType: "plain", Text: string(data),
			Extractor: extractorPlain, Version: ExtractorVersion}, nil
	case "html":
		text, err := htmlText(data)
		if err != nil {
			return nil, err
		}
		return &Result{DerivativeType: "sanitized_html", Text: text,
			Extractor: extractorHTML, Version: ExtractorVersion}, nil
	case "pdf":
		text, err := pdfText(data)
		if err != nil {
			return nil, err
		}
		return &Result{DerivativeType: "text_layer", Text: text,
			Extractor: extractorPDF, Version: ExtractorVersion}, nil
	case "docx":
		text, err := docxText(data)
		if err != nil {
			return nil, err
		}
		return &Result{DerivativeType: "office_text", Text: text,
			Extractor: extractorDocx, Version: ExtractorVersion}, nil
	}
	return nil, ErrUnsupported
}

// htmlText tokenizes and keeps text nodes, dropping script/style/noscript
// bodies entirely (sanitised: markup and executable content never survive).
func htmlText(data []byte) (string, error) {
	tok := xhtml.NewTokenizer(bytes.NewReader(data))
	var b strings.Builder
	skip := 0
	for {
		switch tok.Next() {
		case xhtml.ErrorToken:
			if errors.Is(tok.Err(), io.EOF) {
				return strings.TrimSpace(b.String()), nil
			}
			return "", tok.Err()
		case xhtml.StartTagToken:
			name, _ := tok.TagName()
			switch string(name) {
			case "script", "style", "noscript":
				skip++
			case "p", "div", "br", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteByte('\n')
			}
		case xhtml.EndTagToken:
			name, _ := tok.TagName()
			switch string(name) {
			case "script", "style", "noscript":
				if skip > 0 {
					skip--
				}
			}
		case xhtml.TextToken:
			if skip == 0 {
				b.Write(tok.Text())
			}
		}
	}
}

// pdfText extracts the text layer page by page (page-capped). Encrypted or
// image-only PDFs fail here and become derivative.fail events.
func pdfText(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf open: %w", err)
	}
	pages := r.NumPage()
	if pages > config.DeriveMaxPages {
		pages = config.DeriveMaxPages
	}
	var b strings.Builder
	for i := 1; i <= pages; i++ {
		content := r.Page(i).Content()
		var lastY float64
		for _, t := range content.Text {
			if lastY != 0 && t.Y != lastY {
				b.WriteByte('\n')
			}
			b.WriteString(t.S)
			lastY = t.Y
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String()), nil
}

func isDocx(data []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			return true
		}
	}
	return false
}

// docxText pulls the w:t runs out of word/document.xml (stdlib only; no
// macro or embedded-object evaluation of any kind).
func docxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		dec := xml.NewDecoder(io.LimitReader(rc, int64(config.DeriveMaxTextBytes)*4))
		var b strings.Builder
		var inText bool
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				return strings.TrimSpace(b.String()), nil
			}
			if err != nil {
				return "", err
			}
			switch el := tok.(type) {
			case xml.StartElement:
				if el.Name.Local == "t" {
					inText = true
				}
			case xml.EndElement:
				switch el.Name.Local {
				case "t":
					inText = false
				case "p":
					b.WriteByte('\n')
				}
			case xml.CharData:
				if inText {
					b.Write(el)
				}
			}
		}
	}
	return "", errors.New("docx: word/document.xml missing")
}
