package telemetry

import (
	"path/filepath"
	"testing"
	"time"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordOutcomeAndGates(t *testing.T) {
	s := openStore(t)
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)

	if err := s.Record(Interaction{
		InteractionID: "i-1", Kind: "search", TaskID: "t-1", AgentSurface: "operator",
		Query: "q", BudgetRequested: 100, PayloadChars: 80, ResultCount: 3,
		RetrievalMode: "full", CreatedAt: now, ResultIDs: []string{"m1", "m2", "m3"},
	}); err != nil {
		t.Fatal(err)
	}
	// budget violation row (payload > budget) — must count in gates
	if err := s.Record(Interaction{
		InteractionID: "i-2", Kind: "search", Inferred: true,
		BudgetRequested: 10, PayloadChars: 50, ResultCount: 1,
		RetrievalMode: "lexical_only", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// outcomes bind to interaction ids only; kinds validated
	if err := s.RecordOutcome("missing", "found", "", now); err == nil {
		t.Fatal("unknown interaction accepted")
	}
	if err := s.RecordOutcome("i-1", "shrug", "", now); err == nil {
		t.Fatal("bogus outcome kind accepted")
	}
	if err := s.RecordOutcome("i-1", "found", "m1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutcome("i-2", "manual_workaround", "", now); err != nil {
		t.Fatal(err)
	}

	for i, d := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 100 * time.Millisecond} {
		if err := s.RecordLatency("ack_to_lexical_visible", d, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	g, err := s.Gates()
	if err != nil {
		t.Fatal(err)
	}
	if g.Interactions != 2 || g.BudgetedCount != 2 || g.BudgetViolations != 1 {
		t.Fatalf("budget stats: %+v", g)
	}
	if g.OutcomeFound != 1 || g.OutcomeWorkaround != 1 || g.OutcomeNotFound != 0 {
		t.Fatalf("outcomes: %+v", g)
	}
	if g.LatencySamples != 3 || g.LatencyP95Micros != 100000 {
		t.Fatalf("latency P95: %+v", g)
	}
}

func TestRecordIsIdempotentByInteraction(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	it := Interaction{InteractionID: "i-1", Kind: "digest", PayloadChars: 5, ResultCount: 1,
		RetrievalMode: "full", CreatedAt: now, ResultIDs: []string{"m"}}
	if err := s.Record(it); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(it); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Gates()
	if g.Interactions != 1 {
		t.Fatalf("duplicate interaction rows: %d", g.Interactions)
	}
}
