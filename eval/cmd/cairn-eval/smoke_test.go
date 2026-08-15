package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/result"
)

// The smoke command is the apparatus proving itself. This test runs the
// file-backed conditions only (no daemon, no build) so it stays offline and
// fast; the Cairn condition is covered by the backend package's own test.
func TestSmokeWritesAPlumbingRecord(t *testing.T) {
	dir := t.TempDir()
	if err := runSmoke(context.Background(), []string{"-backends", "B0,B1,B2", "-out", dir, "-seed", "3"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("wrote %d records, want 3", len(entries))
	}
	for _, e := range entries {
		run, err := result.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if run.Kind != result.KindPlumbing {
			t.Fatalf("%s recorded kind %q — an apparatus check must never be labelled a measurement",
				e.Name(), run.Kind)
		}
		if !strings.Contains(run.Corpus.LabelSource, "SYNTHETIC") {
			t.Fatalf("%s does not declare its labels synthetic: %q", e.Name(), run.Corpus.LabelSource)
		}
		if run.Seed != 3 {
			t.Fatalf("%s lost the seed", e.Name())
		}
		if len(run.Outcomes) == 0 {
			t.Fatalf("%s recorded no outcomes", e.Name())
		}
	}
}

func TestSmokeRefusesStubs(t *testing.T) {
	err := runSmoke(context.Background(), []string{"-backends", "B3", "-out", t.TempDir()})
	if err == nil {
		t.Fatal("smoke over an unimplemented baseline must fail, not produce an empty record")
	}
}

// The E3 loader must be reachable from the harness — and must REFUSE to run
// an independent corpus through it. Pushing mined human ground truth through
// the backends is the first half of a measurement, and measurement waits for
// the operator signoffs in eval/claims.yaml (EVAL-PLAN §5-E1).
func TestSmokeAcceptsTheSampleCorpusAndRefusesIndependentOnes(t *testing.T) {
	sample, err := filepath.Abs(filepath.Join("..", "..", "corpora", "sample-plumbing-v1"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := runSmoke(context.Background(), []string{"-backends", "B2", "-corpus", sample, "-out", out}); err != nil {
		t.Fatalf("sample corpus should run: %v", err)
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one record, got %v (%v)", entries, err)
	}
	run, err := result.ReadFile(filepath.Join(out, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if run.Corpus.ID != "sample-plumbing" || run.Corpus.Checksum == "" {
		t.Fatalf("the corpus was not pinned in the record: %+v", run.Corpus)
	}

	// A corpus that claims independent labels must be refused here.
	independent := t.TempDir()
	src, err := os.ReadFile(filepath.Join(sample, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"items.jsonl", "queries.jsonl"} {
		blob, err := os.ReadFile(filepath.Join(sample, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(independent, name), blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	flipped := strings.Replace(string(src), `"independent": false`, `"independent": true`, 1)
	if err := os.WriteFile(filepath.Join(independent, "manifest.json"), []byte(flipped), 0o600); err != nil {
		t.Fatal(err)
	}
	err = runSmoke(context.Background(), []string{"-backends", "B2", "-corpus", independent, "-out", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "measurement") {
		t.Fatalf("an independent corpus must be refused by the plumbing command; got %v", err)
	}
}
