package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/tunables"
)

func minimalCorpus() ([]Item, []Query) {
	items := []Item{
		{ID: "i-1", Title: "one", Body: "the first document"},
		{ID: "i-2", Title: "two", Body: "the second document"},
	}
	queries := []Query{
		{ID: "q-1", Query: "first", Relevant: []string{"i-1"}, LabelKind: LabelSynthetic, Split: SplitDev},
	}
	return items, queries
}

func TestWriteLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	items, queries := minimalCorpus()
	m, err := Write(dir, Manifest{ID: "t", Version: "1"}, items, queries)
	if err != nil {
		t.Fatal(err)
	}
	if m.Checksum == "" || len(m.Files) != 2 {
		t.Fatalf("manifest not pinned: %+v", m)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Items) != 2 || len(c.Queries) != 1 {
		t.Fatalf("loaded %d items / %d queries", len(c.Items), len(c.Queries))
	}
	if c.Manifest.Labels.Kinds[LabelSynthetic] != 1 {
		t.Fatalf("label kinds not counted: %+v", c.Manifest.Labels.Kinds)
	}
}

// A corpus whose bytes drifted makes every result citing it unreproducible.
// The loader must refuse it rather than quietly measuring something else.
func TestTamperedCorpusIsRefused(t *testing.T) {
	dir := t.TempDir()
	items, queries := minimalCorpus()
	if _, err := Write(dir, Manifest{ID: "t", Version: "1"}, items, queries); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ItemsFile)
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(blob, []byte("{\"id\":\"i-3\",\"body\":\"snuck in\"}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("a modified corpus loaded without complaint")
	}
}

// A judgment pointing at an absent item would read as a retrieval failure —
// the system would be blamed for the corpus's mistake.
func TestJudgmentPointingAtNothingIsRefused(t *testing.T) {
	dir := t.TempDir()
	items, _ := minimalCorpus()
	queries := []Query{{ID: "q-1", Query: "x", Relevant: []string{"i-404"}, LabelKind: LabelSynthetic}}
	if _, err := Write(dir, Manifest{ID: "t", Version: "1"}, items, queries); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "no such item") {
		t.Fatalf("expected a dangling-judgment error, got %v", err)
	}
}

func TestUnlabelledJudgmentIsRefused(t *testing.T) {
	dir := t.TempDir()
	items, _ := minimalCorpus()
	queries := []Query{{ID: "q-1", Query: "x", Relevant: []string{"i-1"}}}
	if _, err := Write(dir, Manifest{ID: "t", Version: "1"}, items, queries); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("a judgment with no stated provenance was accepted; whose judgment it is IS the question E3 asks")
	}
}

func TestUnknownSchemaIsRefused(t *testing.T) {
	dir := t.TempDir()
	items, queries := minimalCorpus()
	if _, err := Write(dir, Manifest{ID: "t", Version: "1"}, items, queries); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ManifestFile)
	blob, _ := os.ReadFile(path)
	bumped := strings.Replace(string(blob), fmt.Sprintf("\"schema_version\": %d", tunables.CorpusSchemaVersion), "\"schema_version\": 9999", 1)
	if err := os.WriteFile(path, []byte(bumped), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("a future corpus schema was read as if it were understood")
	}
}

// The dev/holdout split must be deterministic (anyone can verify it from the
// query ids) and roughly the declared proportion. A split that could be
// re-rolled could be re-rolled until the holdout flattered the system.
func TestSplitAssignmentIsDeterministicAndProportionate(t *testing.T) {
	const n = 4000
	queries := make([]Query, n)
	for i := range queries {
		queries[i] = Query{ID: fmt.Sprintf("q-%04d", i)}
	}
	AssignSplits(queries)

	again := make([]Query, n)
	for i := range again {
		again[i] = Query{ID: fmt.Sprintf("q-%04d", i)}
	}
	AssignSplits(again)

	holdout := 0
	for i := range queries {
		if queries[i].Split != again[i].Split {
			t.Fatalf("query %s landed in %q then %q", queries[i].ID, queries[i].Split, again[i].Split)
		}
		if queries[i].Split == SplitHoldout {
			holdout++
		}
	}
	got := float64(holdout) / n
	want := float64(tunables.HoldoutFractionPerMyriad) / 10000
	if got < want-0.03 || got > want+0.03 {
		t.Fatalf("holdout share %.3f, want ~%.3f", got, want)
	}
}

func TestSplitSelection(t *testing.T) {
	c := &Corpus{Queries: []Query{
		{ID: "a", Split: SplitDev}, {ID: "b", Split: SplitHoldout}, {ID: "c", Split: SplitDev},
	}}
	if len(c.Split(SplitDev)) != 2 || len(c.Split(SplitHoldout)) != 1 {
		t.Fatalf("split selection wrong: %d dev, %d holdout", len(c.Split(SplitDev)), len(c.Split(SplitHoldout)))
	}
}
