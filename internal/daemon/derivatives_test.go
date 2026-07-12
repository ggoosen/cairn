package daemon_test

// N4 acceptance: a PDF attachment becomes searchable via its derivative
// with FULL provenance (message → attachment blob_hash → derivative record
// with extractor identity → text_hash object); a misleading sender summary
// is flagged and locally re-summarised. Plus: fail path, invalidate +
// regenerate, and replay-through-reindex.

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/projection"
)

// pdfFixture builds a minimal valid single-page text-layer PDF.
func pdfFixture(text string) []byte {
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

func deriveAll(t *testing.T, d *daemon.Daemon) {
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

func TestN4PDFSearchableViaDerivativeWithProvenance(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir)

	pub, err := d.Publish(daemon.PublishRequest{
		Actor: "operator",
		Body:  "attached the inspection paperwork",
		Attachments: []daemon.AttachmentIn{
			{Data: pdfFixture("quarterly fire safety compliance audit for the roastery premises"), Filename: "audit.pdf"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// not searchable yet: the phrase lives only inside the PDF
	out, err := d.Search(daemon.SearchOptions{Query: "fire safety compliance"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 0 {
		t.Fatalf("pdf text searchable before derivation")
	}

	deriveAll(t, d)

	out, err = d.Search(daemon.SearchOptions{Query: "fire safety compliance"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].MessageID != pub.MessageID {
		t.Fatalf("pdf not searchable via derivative: %+v", out.Results)
	}

	// FULL provenance chain: message → attachment blob → derivative record
	derivs, err := d.Projection().DerivativesForMessage(pub.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(derivs) != 1 {
		t.Fatalf("derivative records: %+v", derivs)
	}
	rec := derivs[0]
	if rec.Status != "ok" || rec.DerivativeType != "text_layer" ||
		rec.Extractor != "go-pdf-textlayer" || rec.Version == "" || rec.TextHash == "" {
		t.Fatalf("provenance incomplete: %+v", rec)
	}
	text, err := d.Store().Get(rec.TextHash)
	if err != nil {
		t.Fatalf("text object missing: %v", err)
	}
	if !strings.Contains(string(text), "fire safety compliance audit") {
		t.Fatalf("text object does not contain the extracted layer: %q", text)
	}

	// invalidate drops it from search; the next pass regenerates
	if _, err := d.DerivativeInvalidate(rec.DerivativeID, "test", "operator"); err != nil {
		t.Fatal(err)
	}
	out, _ = d.Search(daemon.SearchOptions{Query: "fire safety compliance"})
	if len(out.Results) != 0 {
		t.Fatalf("invalidated derivative still searchable")
	}
	deriveAll(t, d)
	out, _ = d.Search(daemon.SearchOptions{Query: "fire safety compliance"})
	if len(out.Results) != 1 {
		t.Fatalf("regeneration after invalidate failed")
	}
}

func TestN4FailPathRecordedOnce(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir)

	if _, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "binary junk attached",
		Attachments: []daemon.AttachmentIn{{Data: []byte{0x00, 0xff, 0x88, 0x00, 0x13}, Filename: "junk.bin"}},
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := d.DeriveOnce(64); err != nil || n != 1 {
		t.Fatalf("first pass: n=%d err=%v", n, err)
	}
	// the failure is recorded as an event — the queue drains, no retry loop
	if n, err := d.DeriveOnce(64); err != nil || n != 0 {
		t.Fatalf("failed blob re-queued: n=%d err=%v", n, err)
	}
}

func TestN4MisleadingSummaryFlaggedAndLocallySummarised(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir) // semanticStub embedder (subs_test.go)

	// body is planning-approval concept; the sender CLAIMS it's about
	// grinder maintenance — semantically divergent
	misleading, err := d.Publish(daemon.PublishRequest{
		Actor:         "operator",
		Body:          "the shire approved the development application for the espresso bar extension. Works may commence in October.",
		SenderSummary: "notes about burr grinder maintenance",
	})
	if err != nil {
		t.Fatal(err)
	}
	honest, err := d.Publish(daemon.PublishRequest{
		Actor:         "operator",
		Body:          "council planning meeting minutes for the espresso bar",
		SenderSummary: "shire development application status",
	})
	if err != nil {
		t.Fatal(err)
	}

	for {
		n, err := d.SummaryCheckOnce(16)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}

	bad, err := d.Projection().SummaryForMessage(misleading.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !bad.Checked || !bad.Disagree {
		t.Fatalf("misleading summary not flagged: %+v", bad)
	}
	if !strings.Contains(bad.LocalSummary, "the shire approved the development application") {
		t.Fatalf("no local extractive summary: %+v", bad)
	}
	if !strings.Contains(bad.LocalMethod, "extractive-lead-v1") {
		t.Fatalf("local summary lacks method provenance: %+v", bad)
	}

	good, err := d.Projection().SummaryForMessage(honest.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !good.Checked || good.Disagree {
		t.Fatalf("honest summary flagged: %+v", good)
	}

	// the digest carries the visible disagreement marker on the right entry
	out := digestFor(t, d, "operator")
	if !strings.Contains(out.Payload, misleading.MessageID+" [summary-disputed]") {
		t.Fatalf("digest lacks the disputed marker:\n%s", out.Payload)
	}
	if strings.Contains(out.Payload, honest.MessageID+" [summary-disputed]") {
		t.Fatalf("honest entry marked:\n%s", out.Payload)
	}
}

func TestN4ReplayThroughReindex(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir)

	pub, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "audit attached",
		SenderSummary: "burr grinder notes", // divergent on purpose
		Attachments: []daemon.AttachmentIn{
			{Data: pdfFixture("underground water tank certification"), Filename: "tank.pdf"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deriveAll(t, d)
	d.Close()

	dbPath := projection.DBPath(dir)
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	d2 := startSubDaemon(t, dir)

	// derivative events replayed → FTS rebuilt → still searchable
	out, err := d2.Search(daemon.SearchOptions{Query: "water tank certification"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].MessageID != pub.MessageID {
		t.Fatalf("derivative search lost in replay: %+v", out.Results)
	}
	// no NEW derivative work invented (the derivative.publish event replayed)
	if n, _ := d2.DeriveOnce(64); n != 0 {
		t.Fatalf("replay re-derived %d blobs", n)
	}
	// summary rows replay; the CHECK is enrichment-class and re-runs
	row, err := d2.Projection().SummaryForMessage(pub.MessageID)
	if err != nil || row == nil {
		t.Fatalf("sender summary lost in replay: %v %+v", err, row)
	}
	if row.Checked {
		t.Fatalf("check state should be enrichment-class (reset on rebuild): %+v", row)
	}
	if _, err := d2.SummaryCheckOnce(16); err != nil {
		t.Fatal(err)
	}
	row, _ = d2.Projection().SummaryForMessage(pub.MessageID)
	if !row.Checked || !row.Disagree {
		t.Fatalf("post-replay recheck failed: %+v", row)
	}
	var parked []projection.ParkedEvent
	if parked, err = d2.Projection().ParkedEvents(); err != nil {
		t.Fatal(err)
	}
	if len(parked) != 0 {
		t.Fatalf("replay parked events: %+v", parked)
	}
}
