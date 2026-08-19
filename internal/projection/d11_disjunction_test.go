package projection_test

// D11 — lexical matching is DISJUNCTIVE with preference, not conjunctive.
//
// The defect these tests lock down: FTSQuery joined the query's terms with
// FTS5's implicit AND, so a document qualified only by containing EVERY term.
// On a one-message mesh `search "council approved"` returned the document and
// `search "what did the council decide about approval"` returned nothing —
// Cairn did not rank worse than grep on natural-language queries, it declined
// to answer them. The properties asserted here are the ones that must hold for
// the widening to be a ranking change rather than a recall free-for-all:
//
//	1. a multi-term natural-language query returns what a single term finds;
//	2. a document matching MORE of the query outranks one matching less;
//	3. a query whose terms appear nowhere still returns nothing;
//	4. a term the index says cannot order results ("the") is dropped instead of
//	   making every document a candidate — and the drop is reported;
//	5. when every term is like that, the query is answered anyway.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/fsx"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/testutil"
)

// buildBodyCorpus writes a log of plain publishes, one per body, and returns the
// projection plus each body's message id (by index). Purpose-built so the
// document frequency of every term is exactly what the test says it is.
func buildBodyCorpus(t *testing.T, bodies ...string) (*projection.Projection, []string) {
	t.Helper()
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	store := object.NewStore(m, "/p")
	c, genv, grec := testutil.NewChain(t)
	lg, err := cairnlog.Create(m, "/p", cairnlog.Origin{DeviceID: c.DeviceID, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append(grec, genv); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(bodies))
	for i, body := range bodies {
		msgID := fmt.Sprintf("0190a1b2-c3d4-7e5f-8901-1000000%05d", i)
		revID := fmt.Sprintf("0190a1b2-c3d4-7e5f-8901-2000000%05d", i)
		publish(t, c, store, lg, msgID, revID, body, "canonical")
		ids[i] = msgID
	}
	lg.Close()
	return openAndReplay(t, m, filepath.Join(t.TempDir(), "index.sqlite"), store), ids
}

// The reproduction from the build plan, at the projection layer: the query an
// agent actually writes must reach the document that the keyword query reaches.
func TestD11NaturalLanguageQueryReachesTheDocument(t *testing.T) {
	p, ids := buildBodyCorpus(t,
		"The council approved the new drainage levy at the March meeting.",
		"Minutes record apologies from two councillors and a late start.")
	defer p.Close()

	for _, q := range []string{
		"council approved",
		"what did the council decide about approval",
		"council",
	} {
		got, err := p.LexicalTopK(q, 25, false)
		if err != nil {
			t.Fatal(err)
		}
		// (the second document reaches "council" only as a trigram substring of
		// "councillors", so it may follow; the assertion is on the best match)
		if len(got) == 0 || got[0] != ids[0] {
			t.Fatalf("%q returned %v, want %s first — conjunctive matching is back", q, got, ids[0])
		}
	}

	// The property conjunction cannot have: terms spread across DIFFERENT
	// documents each bring their own document back. Under an implicit AND no
	// document contains both "levy" and "apologies", so the query answers
	// nothing at all.
	got, err := p.LexicalTopK("levy apologies", 25, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("terms spread across two documents returned %v, want both — this is the AND, not a ranking", got)
	}
}

// The literal reproduction from the build plan was run on a ONE-message mesh,
// and that is the corpus where the term filter is most dangerous: with a single
// document every term it contains is in 100% of the corpus, so a filter reading
// bm25's ratio alone would call all of them uninformative and answer nothing —
// the original defect, wearing the fix's clothes.
func TestD11OneDocumentMeshStillAnswers(t *testing.T) {
	p, ids := buildBodyCorpus(t,
		"The council approved the new drainage levy at the March meeting.")
	defer p.Close()

	for _, q := range []string{
		"council approved",
		"what did the council decide about approval",
		"drainage levy",
	} {
		got, _, err := p.LexicalTopKPlan(q, 25, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != ids[0] {
			t.Fatalf("%q on a one-document mesh returned %v, want [%s]", q, got, ids[0])
		}
	}
}

// Precision comes from ranking: the document containing more of the query
// (and the rarer parts of it) must come first, not merely be present.
func TestD11MoreMatchedTermsRanksHigher(t *testing.T) {
	bodies := []string{
		"drainage levy objection period recorded", // 3 of the query's terms
		"drainage works scheduled for the winter", // 1
	}
	for i := 0; i < 6; i++ { // unrelated bulk, so a term in 2 documents still discriminates
		bodies = append(bodies, fmt.Sprintf("library roof tender %d awarded to a local contractor", i))
	}
	p, ids := buildBodyCorpus(t, bodies...)
	defer p.Close()

	got, err := p.LexicalTopK("drainage levy objection", 25, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates %v, want the two documents that share a term", got)
	}
	if got[0] != ids[0] {
		t.Fatalf("ranked %v first; the document matching THREE query terms must outrank the one matching one", got)
	}
}

// A disjunction is not a match-anything: terms present in no document return
// no document, and terms that tokenize to nothing stay inert inside the OR
// (an empty FTS5 phrase matches no row — this asserts it stays that way).
func TestD11DisjunctionIsNotMatchAnything(t *testing.T) {
	p, ids := buildBodyCorpus(t,
		"the council approved the drainage levy",
		"the planning committee deferred the question")
	defer p.Close()

	for _, q := range []string{"quokka wombat bandicoot", "quokka", "--- ***"} {
		got, err := p.LexicalTopK(q, 25, false)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(got) != 0 {
			t.Fatalf("%q matched %v — an OR over terms nothing contains must return nothing", q, got)
		}
	}
	// ... and a term that tokenizes to nothing must not widen a real query
	got, err := p.LexicalTopK("council ---", 25, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != ids[0] {
		t.Fatalf(`"council ---" returned %v, want only %s`, got, ids[0])
	}
}

// The stopword guard rail: a term the index says cannot order results is
// dropped from the disjunction, so a natural-language query does not drag in
// every document containing "the" — and the drop is REPORTED, not silent.
func TestD11NonDiscriminatingTermsAreDroppedAndReported(t *testing.T) {
	bodies := []string{"the council approved the drainage levy in March"}
	for i := 0; i < 9; i++ {
		bodies = append(bodies, fmt.Sprintf("the routine note %d about the ordinary business of the day", i))
	}
	p, ids := buildBodyCorpus(t, bodies...) // "the" in 10/10, "council" in 1/10
	defer p.Close()

	got, plan, err := p.LexicalTopKPlan("what did the council decide about the drainage levy", 25, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("candidates %v, want only %s — a common term widened the pool to the corpus", got, ids[0])
	}
	if !contains(plan.Common, "the") {
		t.Fatalf("plan did not report dropping %q: %+v", "the", plan)
	}
	if contains(plan.Terms, "the") {
		t.Fatalf("plan searched %q even though it cannot order results: %+v", "the", plan)
	}
	for _, want := range []string{"council", "drainage", "levy"} {
		if !contains(plan.Terms, want) {
			t.Fatalf("discriminating term %q was not searched: %+v", want, plan)
		}
	}
	if !contains(plan.Unmatched, "what") {
		t.Fatalf("plan did not report %q as matching nothing: %+v", "what", plan)
	}
	if plan.AllCommon {
		t.Fatalf("plan claims every term was common: %+v", plan)
	}
}

// The boundary: a term in EXACTLY half the corpus is kept. bm25's idf is zero
// there, so it cannot order results — but it can still name which half, and a
// disjunction that keeps it loses nothing by doing so. Non-discriminating means
// strictly more than half, and this pins that reading.
func TestD11HalfCorpusTermIsStillSearched(t *testing.T) {
	p, _ := buildBodyCorpus(t,
		"the alpha drainage report",
		"the alpha parking report",
		"the beta library report",
		"the gamma roof report")
	defer p.Close()

	got, plan, err := p.LexicalTopKPlan("alpha beta", 25, false) // alpha in 2 of 4
	if err != nil {
		t.Fatal(err)
	}
	if !contains(plan.Terms, "alpha") || len(plan.Common) != 0 {
		t.Fatalf("a term in exactly half the corpus was dropped: %+v", plan)
	}
	if len(got) != 3 {
		t.Fatalf("candidates %v, want the two alpha documents and the beta one", got)
	}
}

// The degenerate case: when EVERY term of the query is non-discriminating —
// the normal state of a very small mesh, and the shape of a query like "the
// project" — the query is answered with all of them rather than refused.
func TestD11EveryTermCommonStillAnswers(t *testing.T) {
	p, _ := buildBodyCorpus(t,
		"the drainage project notes",
		"the drainage project summary")
	defer p.Close()

	got, plan, err := p.LexicalTopKPlan("the project", 25, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates %v, want both documents — a query of only common terms must still be answered", got)
	}
	if !plan.AllCommon {
		t.Fatalf("plan did not report the all-common fallback: %+v", plan)
	}
	if len(plan.Common) != 0 {
		t.Fatalf("plan reported terms as dropped that it actually searched: %+v", plan)
	}
	if !contains(plan.Terms, "the") || !contains(plan.Terms, "project") {
		t.Fatalf("fallback did not search the query's terms: %+v", plan)
	}
}

// FTSQuery itself: the expression must be a disjunction of quoted phrases.
// This is the unit that a mutation back to " " (implicit AND) fails first.
func TestD11FTSQueryIsADisjunction(t *testing.T) {
	if got, want := projection.FTSQuery("council approved levy"), `"council" OR "approved" OR "levy"`; got != want {
		t.Fatalf("FTSQuery = %q, want %q", got, want)
	}
	if got := projection.FTSQuery(`he said "no"`); !strings.Contains(got, `"""no"""`) {
		t.Fatalf("embedded quotes not escaped: %q", got)
	}
	if got, want := projection.FTSQuery("   "), `""`; got != want {
		t.Fatalf("empty query = %q, want %q (an empty phrase matches nothing)", got, want)
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
