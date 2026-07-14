package daemon_test

// FIX-H6 (spec §8.2 / R45 spirit): the degradation ladder must actually ladder.
// Rungs 1–3 were wired; these tests pin the newly-wired rungs 4 (search forced
// lexical-only) and 5 (proactive blob replication delayed). Rungs 6–7 are
// reject paths, computed + reported but enforcement deferred (documented) — not
// asserted here.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/maintenance"
)

// rung 4: with an embedder present, a healthy daemon serves "full"; once the
// ladder is at LexicalOnly, search sheds the vector query and serves lexical.
func TestH6Rung4ForcesLexicalOnly(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zz ladder target decision"}); err != nil {
		t.Fatal(err)
	}
	// embed the corpus so a healthy search is "full".
	if _, err := d.ReindexSemantic(); err != nil {
		t.Fatal(err)
	}

	healthy, err := d.Search(daemon.SearchOptions{Query: "ladder"})
	if err != nil {
		t.Fatal(err)
	}
	if healthy.RetrievalMode != "full" {
		t.Fatalf("healthy search should be full with an embedder, got %q", healthy.RetrievalMode)
	}

	d.SetDegradeLevelForTest(maintenance.LexicalOnly)
	shed, err := d.Search(daemon.SearchOptions{Query: "ladder"})
	if err != nil {
		t.Fatal(err)
	}
	if shed.RetrievalMode != "lexical_only" {
		t.Fatalf("rung 4 must force lexical_only, got %q", shed.RetrievalMode)
	}
}

// rung 5: at DelayBlobRepl the blob phase returns early without touching the
// connection (proactive replication is delayed under disk pressure). Passing a
// nil conn proves the short-circuit precedes any protocol I/O.
func TestH6Rung5DelaysBlobReplication(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	d.SetDegradeLevelForTest(maintenance.DelayBlobRepl)
	if err := d.BlobPhaseForTest(); err != nil {
		t.Fatalf("rung 5 blob phase should no-op cleanly, got %v", err)
	}
}
