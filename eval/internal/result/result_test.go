package result

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRoundTrip(t *testing.T) {
	run := NewRun(KindPlumbing, 7, Backend{ID: "B1", Notes: "modelling note"},
		Corpus{ID: "fixture", Version: "1", Checksum: "abc", ItemCount: 3, QueryCount: 3,
			LabelSource: "SYNTHETIC"})
	run.Add(Outcome{QueryID: "q1", Query: "zephyr", Surface: "search",
		Returned: []string{"plumb-001"}, Relevant: []string{"plumb-001"}, PayloadChars: 12, ElapsedMS: 1})
	run.Note("apparatus check only")

	path := filepath.Join(t.TempDir(), "run.json")
	if err := run.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	back, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.RunID != run.RunID || back.Seed != 7 || len(back.Outcomes) != 1 {
		t.Fatalf("round trip lost data: %+v", back)
	}
	if back.Kind != KindPlumbing {
		t.Fatalf("kind %q — a plumbing record that loses its label can be mistaken for evidence", back.Kind)
	}
	if back.FinishedAt == "" {
		t.Fatal("FinishedAt not stamped")
	}
	if back.Env.GOOS == "" || back.Env.GoVersion == "" {
		t.Fatal("environment not captured; latency claims are machine-specific")
	}
}

func TestUnknownSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.json")
	blob := []byte(`{"schema_version": 9999, "run_id": "x"}`)
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path); err == nil {
		t.Fatal("a result written by a future schema must be refused, not silently misread")
	}
}

// The recorder must stay verdict-free until E4 and its operator signoffs.
// A metric field appearing here is how "apparatus" quietly becomes
// "evidence", so the shape is asserted rather than trusted.
func TestRecordCarriesNoMetrics(t *testing.T) {
	run := NewRun(KindPlumbing, 1, Backend{ID: "B0"}, Corpus{ID: "fixture"})
	run.Add(Outcome{QueryID: "q1", Surface: "search"})
	blob, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	lowered := strings.ToLower(string(blob))
	for _, forbidden := range []string{"ndcg", "mrr", "recall", "success_at", "precision", "score", "verdict", "passed"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("run record contains %q — metrics belong to E4, after the kill criteria are signed off", forbidden)
		}
	}
}

func TestWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "run.json")
	run := NewRun(KindPlumbing, 1, Backend{ID: "B0"}, Corpus{ID: "fixture"})
	if err := run.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file %s left behind", e.Name())
		}
	}
}
