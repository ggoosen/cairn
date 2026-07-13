package maintenance

import "testing"

func TestLadderRungsEngageInOrder(t *testing.T) {
	th := Thresholds{DelayLinks: 200, DelaySummaries: 1000, DelayEmbeddings: 5000, LexicalOnly: 20000}
	cases := []struct {
		name  string
		debt  Debt
		want  Level
		skips [4]bool // links, summaries, embeddings, lexicalOnly
	}{
		{"healthy", Debt{PendingEmbeddings: 0}, Healthy, [4]bool{false, false, false, false}},
		{"just under rung1", Debt{PendingEmbeddings: 199}, Healthy, [4]bool{false, false, false, false}},
		{"rung1 delay links", Debt{PendingEmbeddings: 200}, DelayAutoLinks, [4]bool{true, false, false, false}},
		{"rung2 delay summaries", Debt{PendingEmbeddings: 1000}, DelaySummaries, [4]bool{true, true, false, false}},
		{"rung3 delay embeddings", Debt{PendingEmbeddings: 5000}, DelayEmbeddings, [4]bool{true, true, true, false}},
		{"rung4 lexical only", Debt{PendingEmbeddings: 20000}, LexicalOnly, [4]bool{true, true, true, true}},
		{"way over stays lexical", Debt{PendingEmbeddings: 10_000_000}, LexicalOnly, [4]bool{true, true, true, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Assess(c.debt, th)
			if got != c.want {
				t.Fatalf("Assess = %v (%d), want %v (%d)", got, got, c.want, c.want)
			}
			if got.SkipAutoLinks() != c.skips[0] || got.SkipSummaries() != c.skips[1] ||
				got.SkipEmbeddings() != c.skips[2] || got.LexicalOnlyForced() != c.skips[3] {
				t.Fatalf("gate flags wrong at %v: links=%v sum=%v emb=%v lex=%v",
					got, got.SkipAutoLinks(), got.SkipSummaries(), got.SkipEmbeddings(), got.LexicalOnlyForced())
			}
		})
	}
}

func TestLadderDiskAxisTakesPrecedenceWhenSevere(t *testing.T) {
	th := DefaultThresholds()
	// low backlog but disk pressure → rung 5
	if got := Assess(Debt{PendingEmbeddings: 0, DiskPressure: true}, th); got != DelayBlobRepl {
		t.Fatalf("disk pressure = %v, want delay-blob-replication", got)
	}
	// critical disk → rung 6 (reject low-prio blobs)
	if got := Assess(Debt{DiskCritical: true}, th); got != RejectLowPrioBlobs {
		t.Fatalf("disk critical = %v, want reject-low-priority-blobs", got)
	}
	// quota exhausted → rung 7
	if got := Assess(Debt{QuotaExhausted: true}, th); got != RejectSmallText {
		t.Fatalf("quota exhausted = %v, want reject-small-text", got)
	}
	// the MORE severe of the two axes wins: huge backlog (rung4) + quota (rung7) → rung7
	if got := Assess(Debt{PendingEmbeddings: 1 << 30, QuotaExhausted: true}, th); got != RejectSmallText {
		t.Fatalf("combined = %v, want reject-small-text (most severe)", got)
	}
	// backlog rung4 beats mild disk pressure rung... no: rung4 (lexical) < rung5 (blob). disk wins.
	if got := Assess(Debt{PendingEmbeddings: 1 << 30, DiskPressure: true}, th); got != DelayBlobRepl {
		t.Fatalf("lexical+diskpressure = %v, want delay-blob-replication (rung5 > rung4)", got)
	}
}

func TestDefaultThresholdsFromConfig(t *testing.T) {
	th := DefaultThresholds()
	if th.DelayLinks <= 0 || th.DelaySummaries <= th.DelayLinks ||
		th.DelayEmbeddings <= th.DelaySummaries || th.LexicalOnly <= th.DelayEmbeddings {
		t.Fatalf("config thresholds not monotonic: %+v", th)
	}
}
