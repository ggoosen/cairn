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
