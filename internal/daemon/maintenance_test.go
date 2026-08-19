package daemon_test

// P2-1: the degradation ladder wired into the daemon. An embedder is
// configured but nothing runs the enricher (Serve is never called), so every
// published revision is a real pending embedding and a low threshold trips the
// ladder deterministically. Asserts the level rises with the backlog, gates
// enrichment rungs in order, and is reported by `status`.
//
// D10 CHANGED ONE LINE HERE, deliberately and visibly: this test used to rely
// on there being NO embedder to manufacture its backlog, which is exactly the
// state D10 says is not debt (nothing can work that backlog off, so shedding
// derived work buys nothing and never ends). The assertions — every rung, in
// order, with its gate flags — are untouched: a daemon WITH an embedder and a
// real backlog still climbs the ladder exactly as it did.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/maintenance"
)

func TestP21DegradationLadderWired(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// low thresholds: 1 pending → delay-links, 2 → summaries, 3 → embeddings,
	// 4 → lexical-only.
	d.SetLadderThresholdsForTest(maintenance.Thresholds{
		DelayLinks: 1, DelaySummaries: 2, DelayEmbeddings: 3, LexicalOnly: 4,
	})

	if got := d.AssessDegradationForTest(); got != maintenance.Healthy {
		t.Fatalf("empty corpus should be healthy, got %v", got)
	}

	want := []maintenance.Level{
		maintenance.DelayAutoLinks,  // 1 pending
		maintenance.DelaySummaries,  // 2 pending
		maintenance.DelayEmbeddings, // 3 pending
		maintenance.LexicalOnly,     // 4 pending
	}
	for i, w := range want {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "ladder debt message"}); err != nil {
			t.Fatal(err)
		}
		if got := d.AssessDegradationForTest(); got != w {
			t.Fatalf("after %d pending: level %v, want %v", i+1, got, w)
		}
	}

	// the level is reported by `status`
	lvl := d.DegradationLevel()
	if lvl != maintenance.LexicalOnly {
		t.Fatalf("DegradationLevel = %v, want lexical-only", lvl)
	}
	if lvl.String() != "lexical-only" {
		t.Fatalf("level string = %q", lvl.String())
	}
}
