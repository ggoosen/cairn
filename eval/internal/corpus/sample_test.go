package corpus

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The checked-in sample corpus and the generator that produces it live
// together, so the committed bytes can never drift from the code that made
// them. Regenerate with:
//
//	CAIRN_EVAL_REGEN_SAMPLE=1 go test ./internal/corpus/ -run TestSampleCorpus
//
// WHAT THE SAMPLE IS FOR: exercising the format, the checksum verification
// and the harness loader offline. It is NOT evidence — six documents and six
// questions written by this project is exactly the circularity EVAL-PLAN
// §2.2 exists to break — and it declares itself non-independent so that a
// future measurement pass can refuse it for anything but plumbing.

const sampleDirName = "sample-plumbing-v1"

func sampleCorpusDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "corpora", sampleDirName))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func sampleItems() []Item {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(d int) string { return base.AddDate(0, 0, d).Format(time.RFC3339) }
	return []Item{
		{ID: "s-001", Title: "retry policy for the ingest worker",
			Body:   "The ingest worker retries a failed batch three times with exponential backoff, then parks it. Parked batches are never retried automatically.",
			Topics: []string{"corpus/sample"}, CreatedAt: at(0)},
		{ID: "s-002", Title: "why the ledger uses integer cents",
			Body:   "Amounts are stored as integer cents because binary floating point cannot represent 0.10 exactly, and a rounding drift in a ledger is unrecoverable.",
			Topics: []string{"corpus/sample"}, CreatedAt: at(3)},
		{ID: "s-003", Title: "deploying the scheduler to the staging cluster",
			Body:   "The scheduler is deployed with a rolling restart. Drain the queue first: an in-flight job whose worker disappears is retried from the start.",
			Topics: []string{"corpus/sample"}, CreatedAt: at(9)},
		{ID: "s-004", Title: "timezone handling in the report generator",
			Body:   "Reports are generated in UTC and rendered in the viewer's timezone. Storing local times was the cause of the duplicated midnight rows.",
			Topics: []string{"corpus/sample"}, CreatedAt: at(14)},
		{ID: "s-005", Title: "connection pool sizing",
			Body:   "The pool is capped at thirty-two connections; the database refuses more than sixty-four and two processes share it.",
			Topics: []string{"corpus/sample"}, CreatedAt: at(21)},
		{ID: "s-006", Title: "log retention window",
			Body:   "Application logs are kept for fourteen days. Anything needed longer must be summarized into the weekly digest before it ages out.",
			Topics: []string{"corpus/sample"}, CreatedAt: at(30)},
	}
}

func sampleQueries() []Query {
	qs := []Query{
		{ID: "sq-001", Query: "how many times does a failed batch get retried", Relevant: []string{"s-001"}, LabelKind: LabelSynthetic},
		{ID: "sq-002", Query: "why are money amounts not floats", Relevant: []string{"s-002"}, LabelKind: LabelSynthetic},
		{ID: "sq-003", Query: "do I need to drain the queue before restarting the scheduler", Relevant: []string{"s-003"}, LabelKind: LabelSynthetic},
		{ID: "sq-004", Query: "what caused the duplicated midnight rows", Relevant: []string{"s-004"}, LabelKind: LabelSynthetic},
		{ID: "sq-005", Query: "maximum number of database connections", Relevant: []string{"s-005"}, LabelKind: LabelSynthetic},
		{ID: "sq-006", Query: "how long are logs kept", Relevant: []string{"s-006"}, LabelKind: LabelSynthetic},
	}
	AssignSplits(qs)
	return qs
}

func sampleManifest(notes []string) Manifest {
	return Manifest{
		ID:        "sample-plumbing",
		Version:   "1",
		CreatedAt: "2026-08-15T00:00:00Z",
		Source: Source{
			Kind:    "synthetic",
			Origin:  "written by this project for apparatus tests",
			Command: "CAIRN_EVAL_REGEN_SAMPLE=1 go test ./internal/corpus/ -run TestSampleCorpus",
			License: "same as the repository",
		},
		Labels: Labels{
			Provenance:  "SYNTHETIC — content and judgments both authored by this project. Exists to test the LOADER, not Cairn.",
			Independent: false,
		},
		Notes: notes,
	}
}

var sampleNotes = []string{
	"NOT EVIDENCE. Six documents and six questions written by the project author: exactly the circularity EVAL-PLAN §2.2 exists to break.",
	"Its only job is to give the corpus format, the checksum verification and the harness loader something to chew on offline.",
	"Real corpora are MINED (github-duplicate-issues, stackoverflow-duplicates, doc-cross-references) and are acquired by the operator, not committed here.",
}

func TestSampleCorpusIsLoadableAndMatchesItsGenerator(t *testing.T) {
	dir := sampleCorpusDir(t)
	if os.Getenv("CAIRN_EVAL_REGEN_SAMPLE") != "" {
		if _, err := Write(dir, sampleManifest(sampleNotes), sampleItems(), sampleQueries()); err != nil {
			t.Fatalf("regenerating the sample corpus: %v", err)
		}
		t.Log("regenerated", dir)
	}

	c, err := Load(dir)
	if err != nil {
		t.Fatalf("loading the checked-in sample corpus: %v", err)
	}
	if c.Manifest.Labels.Independent {
		t.Fatal("the sample corpus must declare itself NOT independent; it is authored by this project")
	}

	// Regenerate into a temp dir and compare: the committed bytes must be
	// exactly what the generator produces today.
	tmp := t.TempDir()
	m, err := Write(tmp, sampleManifest(sampleNotes), sampleItems(), sampleQueries())
	if err != nil {
		t.Fatal(err)
	}
	if m.Checksum != c.Manifest.Checksum {
		t.Fatalf("checked-in sample checksum %s, generator produces %s — regenerate with CAIRN_EVAL_REGEN_SAMPLE=1",
			c.Manifest.Checksum[:16], m.Checksum[:16])
	}
	if !reflect.DeepEqual(c.Items, sampleItems()) {
		t.Fatal("checked-in sample items differ from the generator")
	}
}

func TestSampleCorpusConvertsToBackendItems(t *testing.T) {
	c, err := Load(sampleCorpusDir(t))
	if err != nil {
		t.Fatal(err)
	}
	items := c.BackendItems()
	if len(items) != len(c.Items) {
		t.Fatalf("converted %d of %d items", len(items), len(c.Items))
	}
	for _, it := range items {
		if it.ID == "" || it.Body == "" {
			t.Fatalf("degenerate converted item: %+v", it)
		}
		if it.CreatedAt.IsZero() {
			t.Fatalf("item %s lost its timestamp; E9 replays a corpus chronologically", it.ID)
		}
	}
}
