package daemon_test

// D10 — the ladder could not tell "behind" from "no embedder", and `cairn
// status` could not tell an operator why retrieval was lexical-only.
//
// Observed on the dev node: 1,242 revisions unembedded because no venv was
// provisioned, read as debt, putting an idle laptop at rung 2 under zero load
// — and `message_summaries` held 5 rows for 1,242 messages, which is that
// shedding actually happening. With no embedder the pending counter is
// monotonic in CORPUS SIZE, not in load, so it only ever gets worse.
//
// The negative test is the second one: with an embedder present, a real
// backlog must still climb the rungs exactly as before (TestP21... covers the
// full ladder; this pins that the zeroing is conditional, not unconditional).

import (
	"errors"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/maintenance"
)

// brokenEmbedder is configured, loads, and fails every call — the third cause
// of lexical-only, and the one that used to be indistinguishable from the
// other two.
type brokenEmbedder struct{}

func (brokenEmbedder) ModelID() string { return "broken-v1" }
func (brokenEmbedder) Dim() int        { return config.EmbeddingDim }
func (brokenEmbedder) Close() error    { return nil }
func (brokenEmbedder) Embed([]string) ([][]float32, error) {
	return nil, errors.New("embed worker exited: no such file or directory")
}

// Acceptance: a daemon with no embedder and a large unembedded corpus reports
// Healthy on the backlog axis, and therefore keeps writing summaries and
// auto-links.
func TestD10NoEmbedderIsNotBacklogDebt(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// thresholds low enough that a handful of messages would trip every rung
	// if the pending count were being read at all
	d.SetLadderThresholdsForTest(maintenance.Thresholds{
		DelayLinks: 1, DelaySummaries: 2, DelayEmbeddings: 3, LexicalOnly: 4,
	})
	for i := 0; i < 12; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "unembeddable message"}); err != nil {
			t.Fatal(err)
		}
	}
	lvl := d.AssessDegradationForTest()
	if lvl != maintenance.Healthy {
		t.Fatalf("no embedder + %d unembedded revisions ⇒ level %v, want healthy "+
			"(nothing can work that backlog off, so shedding derived work buys nothing)", 12, lvl)
	}
	if lvl.SkipAutoLinks() || lvl.SkipSummaries() {
		t.Fatal("derived work is still being shed with no embedder configured")
	}
}

// The negative half: an embedder IS present, so the same backlog is real debt
// and the ladder climbs exactly as it did before D10.
func TestD10EmbedderPresentStillClimbsTheLadder(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetLadderThresholdsForTest(maintenance.Thresholds{
		DelayLinks: 1, DelaySummaries: 2, DelayEmbeddings: 3, LexicalOnly: 4,
	})
	for i, want := range []maintenance.Level{
		maintenance.DelayAutoLinks, maintenance.DelaySummaries,
		maintenance.DelayEmbeddings, maintenance.LexicalOnly,
	} {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "real debt"}); err != nil {
			t.Fatal(err)
		}
		if got := d.AssessDegradationForTest(); got != want {
			t.Fatalf("after %d pending with an embedder: level %v, want %v", i+1, got, want)
		}
	}

	// ...and an embedder that fails does NOT zero the axis either: it is
	// configured, the work is real, and the backlog is genuine debt.
	d.SetEmbedderForTest(brokenEmbedder{})
	if got := d.AssessDegradationForTest(); got != maintenance.LexicalOnly {
		t.Fatalf("failing embedder: level %v, want the backlog still counted", got)
	}
}

// Acceptance: `cairn status` distinguishes the causes of lexical-only, and a
// test pins each one.
func TestD10StatusNamesTheCauseOfLexicalOnly(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// 1. no embedder configured — the state that used to be reported
	//    identically to rung 4
	got := d.RetrievalStatus()
	if got.Mode != "lexical_only" || got.Cause != "no_embedder" {
		t.Fatalf("no embedder: %+v", got)
	}
	if got.Detail == "" {
		t.Fatal("no embedder: no remedy named")
	}

	// 2. embedder present and healthy → hybrid
	d.SetEmbedderForTest(embed.BagOfWords{})
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "searchable body"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EnrichOnce(8); err != nil {
		t.Fatal(err)
	}
	if got := d.RetrievalStatus(); got.Mode != "hybrid" || got.Cause != "" {
		t.Fatalf("healthy embedder: %+v", got)
	}

	// 3. ladder rung 4 — an embedder is present, the backlog is shedding the
	//    vector query. A different cause, a different remedy.
	d.SetDegradeLevelForTest(maintenance.LexicalOnly)
	got = d.RetrievalStatus()
	if got.Mode != "lexical_only" || got.Cause != "ladder_rung_4" {
		t.Fatalf("rung 4: %+v", got)
	}
	d.SetDegradeLevelForTest(maintenance.Healthy)

	// 4. embedder configured but failing — invisible before D10, because a
	//    failed query embed just fell through to lexical results
	d.SetEmbedderForTest(brokenEmbedder{})
	if _, err := d.Search(daemon.SearchOptions{Query: "searchable", K: 5}); err != nil {
		t.Fatal(err)
	}
	got = d.RetrievalStatus()
	if got.Mode != "lexical_only" || got.Cause != "embedder_failing" {
		t.Fatalf("failing embedder: %+v", got)
	}
	if got.Detail == "" {
		t.Fatal("failing embedder: the error was not reported")
	}

	// and it recovers: a working embedder reports hybrid again on its next
	// success (health is the last call, not a sticky flag)
	d.SetEmbedderForTest(embed.BagOfWords{})
	if _, err := d.Search(daemon.SearchOptions{Query: "searchable", K: 5}); err != nil {
		t.Fatal(err)
	}
	if got := d.RetrievalStatus(); got.Mode != "hybrid" {
		t.Fatalf("recovered embedder: %+v", got)
	}
}
