package daemon_test

// P2-1: the degradation ladder wired into the daemon. With no embedder (the
// test default), every published revision is a pending embedding, so a low
// threshold trips the ladder deterministically. Asserts the level rises with
// the backlog, gates enrichment rungs in order, and is reported by `status`.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/maintenance"
)

func TestP21DegradationLadderWired(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
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
