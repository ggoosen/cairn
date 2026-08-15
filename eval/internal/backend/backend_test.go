package backend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These are HARNESS self-tests. They check that a backend can be opened,
// written to and queried, and that a hit maps back to the corpus item that
// produced it. They are not evaluation: the fixture is three synthetic
// documents written by this project, and nothing here computes a metric or
// compares one backend against another.

func TestFileBackedBackendsRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, id := range []ID{B0NoMemory, B1GrepTranscript, B2FlatNotes} {
		t.Run(string(id), func(t *testing.T) {
			b, err := New(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Open(ctx, Config{WorkDir: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = b.Close(ctx) })

			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for n, it := range PlumbingFixture() {
				it.CreatedAt = base.Add(time.Duration(n) * time.Hour)
				rec, err := b.Write(ctx, it)
				if err != nil {
					t.Fatalf("write %s: %v", it.ID, err)
				}
				if rec.ItemID != it.ID {
					t.Fatalf("receipt item id %q, want %q", rec.ItemID, it.ID)
				}
			}

			q := PlumbingQueries()[0]
			resp, err := b.Retrieve(ctx, Request{Surface: SurfaceSearch, Query: q.Query, K: 5, BudgetChars: 2000})
			if err != nil {
				t.Fatal(err)
			}
			if id == B0NoMemory {
				if len(resp.Hits) != 0 {
					t.Fatalf("B0 is the no-memory control; it returned %d hits", len(resp.Hits))
				}
				return
			}
			if len(resp.Hits) == 0 {
				t.Fatalf("no hits for %q — the plumbing fixture puts that phrase in exactly one item", q.Query)
			}
			if resp.Hits[0].ItemID != q.Expected {
				t.Fatalf("hit maps to %q, want %q (item-id mapping is what makes ground truth checkable)",
					resp.Hits[0].ItemID, q.Expected)
			}
			if resp.Payload == "" {
				t.Fatal("empty payload: the payload is what an agent would be handed, and what a budget applies to")
			}
		})
	}
}

func TestUnsupportedSurfaceIsDeclaredNotEmpty(t *testing.T) {
	ctx := context.Background()
	b, err := New(B1GrepTranscript)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ctx, Config{WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	// A transcript pile has no digest surface. It must SAY so: an empty
	// result would silently become a zero in a table one day.
	if _, err := b.Retrieve(ctx, Request{Surface: SurfaceDigest, Query: "anything"}); !errors.Is(err, ErrUnsupportedSurface) {
		t.Fatalf("digest on B1 returned %v, want ErrUnsupportedSurface", err)
	}
}

func TestBudgetIsHonouredInRunes(t *testing.T) {
	ctx := context.Background()
	b, err := New(B2FlatNotes)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ctx, Config{WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, it := range PlumbingFixture() {
		if _, err := b.Write(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	const budget = 40
	for _, surface := range []Surface{SurfaceSearch, SurfaceDigest} {
		resp, err := b.Retrieve(ctx, Request{Surface: surface, Query: "widget quokka lighthouse", BudgetChars: budget})
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(resp.Payload)); n > budget {
			t.Fatalf("%s payload %d runes over budget %d", surface, n, budget)
		}
	}
}

func TestStubsFailLoudly(t *testing.T) {
	ctx := context.Background()
	for _, id := range []ID{B3VectorRAG, B4FullContext} {
		b, err := New(id)
		if err != nil {
			t.Fatal(err)
		}
		if !IsStub(b) {
			t.Fatalf("%s should be a declared stub", id)
		}
		if err := b.Open(ctx, Config{WorkDir: t.TempDir()}); !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("%s Open returned %v, want ErrNotImplemented — an unimplemented baseline must never look like an empty one", id, err)
		}
		if _, err := b.Retrieve(ctx, Request{Surface: SurfaceSearch}); !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("%s Retrieve returned %v, want ErrNotImplemented", id, err)
		}
		if !strings.Contains(b.Capabilities().Notes, "NOT IMPLEMENTED") {
			t.Fatalf("%s capabilities do not say it is unimplemented: %q", id, b.Capabilities().Notes)
		}
	}
}

func TestEveryPlanBaselineIsRegistered(t *testing.T) {
	// EVAL-PLAN §5-E4 names six conditions. A baseline that quietly went
	// missing would narrow the comparison without anyone noticing.
	want := map[ID]bool{B0NoMemory: false, B1GrepTranscript: false, B2FlatNotes: false,
		B3VectorRAG: false, B4FullContext: false, B5Cairn: false}
	for _, id := range All() {
		if _, ok := want[id]; !ok {
			t.Fatalf("unexpected backend %q", id)
		}
		want[id] = true
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("baseline %s is not registered", id)
		}
	}
}

func TestTokenizeIsStableAndDeduplicated(t *testing.T) {
	got := tokenize("Purple  widget, purple WIDGET! a 42")
	wantJoined := "purple widget 42"
	if strings.Join(got, " ") != wantJoined {
		t.Fatalf("tokenize = %v, want %q", got, wantJoined)
	}
}
