package backend

import (
	"context"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/cairnctl"
)

// B5 through the SAME interface as the file-backed baselines. The point is
// not that Cairn returns good results — that is E4's question, and it is not
// asked here — but that the uniform interface really is uniform: same Open,
// same Write, same Retrieve, same item-id mapping back to ground truth.
func TestCairnBackendRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a real cairn daemon")
	}
	ctx := context.Background()
	bin, err := cairnctl.FindBinary(ctx)
	if err != nil {
		t.Skipf("no cairn binary available: %v", err)
	}

	b, err := New(B5Cairn)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ctx, Config{WorkDir: t.TempDir(), Binary: bin}); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close(ctx) })

	for _, it := range PlumbingFixture() {
		rec, err := b.Write(ctx, it)
		if err != nil {
			t.Fatalf("write %s: %v", it.ID, err)
		}
		if rec.NativeID == "" {
			t.Fatalf("no cairn message id for %s", it.ID)
		}
	}

	q := PlumbingQueries()[0]
	resp, err := b.Retrieve(ctx, Request{Surface: SurfaceSearch, Query: q.Query, K: 5, BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Hits) == 0 {
		t.Fatalf("no hits at all for %q — the plumbing is broken, whatever the ranking does", q.Query)
	}
	// Every hit must resolve to a corpus item: without that mapping the
	// harness cannot score anything against ground truth later.
	for _, h := range resp.Hits {
		if h.ItemID == "" {
			t.Fatalf("hit %s did not map back to a corpus item", h.NativeID)
		}
	}
	if resp.RetrievalMode == "" {
		t.Fatal("no retrieval_mode recorded; full vs lexical_only answers different claims")
	}

	dig, err := b.Retrieve(ctx, Request{Surface: SurfaceDigest, BudgetChars: 1500})
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if dig.Payload == "" {
		t.Fatal("empty digest payload")
	}
	if len(dig.Hits) == 0 {
		t.Fatalf("digest payload carried no recoverable corpus items:\n%s", dig.Payload)
	}
}
