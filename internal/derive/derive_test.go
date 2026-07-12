package derive

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

// minimalPDF builds a valid single-page text-layer PDF around the given
// ASCII text (classic hand-written xref layout).
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func minimalDocx(paragraphs ...string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("[Content_Types].xml")
	w.Write([]byte(`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`))
	w, _ = zw.Create("word/document.xml")
	var body strings.Builder
	body.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t>` + p + `</w:t></w:r></w:p>`)
	}
	body.WriteString(`</w:body></w:document>`)
	w.Write([]byte(body.String()))
	zw.Close()
	return buf.Bytes()
}

func TestExtractPDFTextLayer(t *testing.T) {
	res, err := Extract(context.Background(), minimalPDF("espresso machine maintenance schedule"))
	if err != nil {
		t.Fatal(err)
	}
	if res.DerivativeType != "text_layer" || !strings.Contains(res.Text, "espresso machine maintenance schedule") {
		t.Fatalf("pdf extraction: %+v", res)
	}
	if res.Extractor != "go-pdf-textlayer" || res.Version != ExtractorVersion {
		t.Fatalf("extractor identity: %+v", res)
	}
}

func TestExtractHTMLSanitized(t *testing.T) {
	html := `<!DOCTYPE html><html><head><style>body{color:red}</style>
	<script>alert("never run me")</script></head>
	<body><h1>Roast Log</h1><p>City roast at 218C for the Kenyan lot.</p></body></html>`
	res, err := Extract(context.Background(), []byte(html))
	if err != nil {
		t.Fatal(err)
	}
	if res.DerivativeType != "sanitized_html" {
		t.Fatalf("type: %+v", res)
	}
	if !strings.Contains(res.Text, "City roast at 218C") || !strings.Contains(res.Text, "Roast Log") {
		t.Fatalf("text lost: %q", res.Text)
	}
	if strings.Contains(res.Text, "alert") || strings.Contains(res.Text, "color:red") {
		t.Fatalf("script/style leaked into derivative: %q", res.Text)
	}
}

func TestExtractDocx(t *testing.T) {
	res, err := Extract(context.Background(), minimalDocx("Lease renewal terms", "Clause 4: rent review 2027"))
	if err != nil {
		t.Fatal(err)
	}
	if res.DerivativeType != "office_text" || !strings.Contains(res.Text, "Clause 4: rent review 2027") {
		t.Fatalf("docx extraction: %+v", res)
	}
}

func TestExtractPlainAndUnsupported(t *testing.T) {
	res, err := Extract(context.Background(), []byte("plain notes about the grinder"))
	if err != nil || res.DerivativeType != "plain" {
		t.Fatalf("plain: %v %+v", err, res)
	}
	// binary junk → unsupported, not a crash
	if _, err := Extract(context.Background(), []byte{0x00, 0xff, 0x12, 0x00, 0x99}); err == nil {
		t.Fatal("binary accepted")
	}
	// truncated/corrupt PDF → error contained (rsc.io/pdf panics internally)
	if _, err := Extract(context.Background(), []byte("%PDF-1.4 garbage")); err == nil {
		t.Fatal("corrupt pdf accepted")
	}
}

func TestExtractDeterministic(t *testing.T) {
	data := minimalPDF("same bytes same text")
	a, err := Extract(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Extract(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != b.Text || a.Extractor != b.Extractor || a.Version != b.Version {
		t.Fatal("extraction is not deterministic")
	}
}

func TestSniffNeverTrustsFilename(t *testing.T) {
	// content decides: PDF bytes are pdf no matter what anyone claims
	if k := Sniff(minimalPDF("x")); k != "pdf" {
		t.Fatalf("pdf sniff: %q", k)
	}
	if k := Sniff([]byte("hello world")); k != "text" {
		t.Fatalf("text sniff: %q", k)
	}
	if k := Sniff(minimalDocx("x")); k != "docx" {
		t.Fatalf("docx sniff: %q", k)
	}
}
