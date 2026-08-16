package growth

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/corpus"
	"github.com/ggoosen/cairn/eval/internal/experiment"
)

func base(t *testing.T) experiment.Material {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "corpora", "sample-plumbing-v1"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := corpus.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := experiment.FromCorpus(c, corpus.SplitDev)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The curve's origin must be the untouched corpus, measured by the same path
// as every point after it.
func TestScaleOneIsTheCorpusItself(t *testing.T) {
	b := base(t)
	got, err := Grow(b, 1, Neutral, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != len(b.Items) {
		t.Fatalf("scale 1 changed the corpus: %d vs %d items", len(got.Items), len(b.Items))
	}
}

func TestGrowthMultipliesTheCorpusAndLeavesGroundTruthAlone(t *testing.T) {
	b := base(t)
	for _, scale := range []int{10, 100} {
		got, err := Grow(b, scale, Contending, 7)
		if err != nil {
			t.Fatal(err)
		}
		if want := scale * len(b.Items); len(got.Items) != want {
			t.Fatalf("x%d produced %d items, want %d", scale, len(got.Items), want)
		}
		if len(got.Queries) != len(b.Queries) {
			t.Fatalf("x%d changed the query set", scale)
		}
		// Ground truth must still point at real items only. A filler item that
		// entered a relevance set would make the curve measure the corpus's
		// corruption instead of the system's selectivity.
		ids := map[string]bool{}
		for _, it := range got.Items {
			ids[it.ID] = true
		}
		for _, q := range got.Queries {
			for _, r := range q.Relevant {
				if strings.HasPrefix(r, FillerIDPrefix) {
					t.Fatalf("query %s judges a filler item relevant", q.ID)
				}
				if !ids[r] {
					t.Fatalf("query %s lost its target %s", q.ID, r)
				}
			}
		}
	}
}

// The same seed must give the same corpus. A growth curve that cannot be
// regenerated cannot be disputed.
func TestGrowthIsDeterministic(t *testing.T) {
	b := base(t)
	a, _ := Grow(b, 10, Contending, 42)
	c, _ := Grow(b, 10, Contending, 42)
	for i := range a.Items {
		if a.Items[i].Body != c.Items[i].Body {
			t.Fatalf("item %d differs between two runs at the same seed", i)
		}
	}
	d, _ := Grow(b, 10, Contending, 43)
	same := true
	for i := range a.Items {
		if a.Items[i].Body != d.Items[i].Body {
			same = false
			break
		}
	}
	if same {
		t.Fatal("changing the seed changed nothing; the filler is not actually sampled")
	}
}

// The two generators must genuinely differ: neutral filler must not contain
// query vocabulary, contending filler must. If they coincided, the curve would
// report one bound twice and call it a bracket.
func TestGeneratorsBracketRatherThanCoincide(t *testing.T) {
	b := base(t)
	neutral, err := Grow(b, 20, Neutral, 3)
	if err != nil {
		t.Fatal(err)
	}
	contending, err := Grow(b, 20, Contending, 3)
	if err != nil {
		t.Fatal(err)
	}

	// "retried" appears in sq-001 ("how many times does a failed batch get
	// retried"), so it is query vocabulary.
	const term = "retried"
	if countTerm(neutral, term) != 0 {
		t.Fatalf("neutral filler contains query vocabulary %q", term)
	}
	if countTerm(contending, term) == 0 {
		t.Fatalf("contending filler contains no query vocabulary at all; it is not contending for anything")
	}
}

func countTerm(m experiment.Material, term string) int {
	n := 0
	for _, it := range m.Items {
		if !strings.HasPrefix(it.ID, FillerIDPrefix) {
			continue
		}
		n += strings.Count(strings.ToLower(it.Title+" "+it.Body), term)
	}
	return n
}

// Filler must be timestamped after the real material: long-term memory is
// asked to find OLD things in a big NEW corpus, and filler dated before the
// answers would test the opposite.
func TestFillerIsNewerThanTheMaterialItBuries(t *testing.T) {
	b := base(t)
	got, _ := Grow(b, 10, Neutral, 1)
	newestReal := baseTime(b)
	for _, it := range got.Items {
		if strings.HasPrefix(it.ID, FillerIDPrefix) && !it.CreatedAt.After(newestReal) {
			t.Fatalf("filler %s is not newer than the real corpus", it.ID)
		}
	}
}

// A corpus too small to generate distinguishable filler from must be REFUSED,
// not filled with near-duplicates of its own answers. Near-duplicate filler
// would not measure interference; it would plant a second copy of each answer
// and then report that recall held up.
func TestTinyCorpusIsRefused(t *testing.T) {
	tiny := experiment.Material{
		Items: []backend.Item{
			{ID: "t-1", Title: "one", Body: "alpha beta gamma"},
			{ID: "t-2", Title: "two", Body: "delta epsilon zeta"},
		},
		Queries: []experiment.Query{{ID: "q1", Text: "alpha", Relevant: []string{"t-1"}}},
	}
	_, err := Grow(tiny, 10, Neutral, 1)
	if err == nil {
		t.Fatal("a corpus with almost no vocabulary produced filler anyway")
	}
	if !strings.Contains(err.Error(), "ground truth") {
		t.Fatalf("the refusal does not say why it matters: %v", err)
	}
}
