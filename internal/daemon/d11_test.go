package daemon_test

// D11 at the surface an agent actually uses: `search` answers a
// natural-language question, the widened match stays reconcilable (R47/R51),
// and the extra candidates it admits never cost more than the budget.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/rank"
)

// The build-plan reproduction, end to end on a one-message mesh: the keyword
// query and the question must return the SAME document. Before D11 the second
// returned "results": null.
func TestD11SearchAnswersANaturalLanguageQuestion(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator",
		Body: "The council approved the new drainage levy at the March meeting, subject to a 30-day objection period."})
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.Publish(daemon.PublishRequest{Actor: "operator",
		Body: "Minutes record apologies from two councillors and a late start."})
	if err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"council approved", "what did the council decide about approval"} {
		out, err := d.Search(daemon.SearchOptions{Query: q, K: 5})
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(out.Results) == 0 || out.Results[0].MessageID != pub.MessageID {
			t.Fatalf("%q returned %d results, want the published message first", q, len(out.Results))
		}
		if out.LexicalQuery == nil || len(out.LexicalQuery.Terms) == 0 {
			t.Fatalf("%q: search did not report which terms it searched: %+v", q, out.LexicalQuery)
		}
	}

	// Terms spread across two documents bring back both — the property a
	// conjunction cannot have, since no document contains "levy" AND
	// "apologies".
	out, err := d.Search(daemon.SearchOptions{Query: "levy apologies", K: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 2 {
		t.Fatalf("terms spread across two documents returned %d results, want 2", len(out.Results))
	}
	seen := map[string]bool{}
	for _, r := range out.Results {
		seen[r.MessageID] = true
	}
	if !seen[pub.MessageID] || !seen[other.MessageID] {
		t.Fatalf("both documents must come back: %+v", out.Results)
	}

	// A query whose terms the mesh has never seen still answers nothing —
	// disjunction widens matching, it does not abolish it.
	out, err = d.Search(daemon.SearchOptions{Query: "quokka wombat bandicoot", K: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 0 {
		t.Fatalf("query about absent subject matter returned %d results", len(out.Results))
	}
}

// The same emptiness at corpus scale, where the term filter is doing the work:
// a natural-language question about a subject the mesh has never heard of
// shares only its function words with the corpus, and those are dropped, so
// nothing is searched and nothing comes back.
//
// On a ONE-document mesh the same question does return that document, because
// there "the" is the only term the index has and dropping it would leave the
// query unanswerable — the all-common fallback, working as designed. The
// distinction is worth stating: emptiness here is a property of a corpus large
// enough for a term to be measurably uninformative, not of the query.
func TestD11AbsentSubjectMatterReturnsNothing(t *testing.T) {
	d, _ := buildCorpusDaemon(t)
	// lexical-only: the semantic arm always returns its nearest neighbours, so
	// leaving it on would measure the embedder rather than the disjunction.
	d.SetEmbedderForTest(nil)

	const q = "what did the quokka decide about wombats"
	out, err := d.Search(daemon.SearchOptions{Query: q, K: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 0 {
		t.Fatalf("question about absent subject matter returned %d results: %+v", len(out.Results), out.LexicalQuery)
	}
	if len(out.LexicalQuery.Terms) != 0 {
		t.Fatalf("terms were searched that no document contains: %+v", out.LexicalQuery)
	}
	if !containsTerm(out.LexicalQuery.Common, "the") {
		t.Fatalf(`the query's one common term was not reported as dropped: %+v`, out.LexicalQuery)
	}
}

func containsTerm(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// R47/R51 under the widened match: a multi-term query now admits many more
// candidates, and EVERY one of them must still print arithmetic that an
// external recompute reproduces bit-exactly — under both profiles.
func TestD11WhyRankedReconcilesForAMultiTermQuery(t *testing.T) {
	for _, p2 := range []bool{false, true} {
		name := "P0"
		if p2 {
			name = "P2"
		}
		t.Run(name, func(t *testing.T) {
			d, _ := buildCorpusDaemon(t)
			d.SetRankProfileP2ForTest(p2)

			// stopword-heavy, multi-term, natural language — the query shape the
			// conjunctive matcher answered with silence.
			out, err := d.Search(daemon.SearchOptions{
				Query: "what did the team decide about the authentication token rotation", K: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Results) < 5 {
				t.Fatalf("only %d results: the query no longer exercises a wide candidate set", len(out.Results))
			}
			for _, r := range out.Results {
				text, err := d.WhyRanked(out.InteractionID, r.MessageID)
				if err != nil {
					t.Fatalf("why-ranked %s: %v", r.MessageID, err)
				}
				reconcileAgainstReturned(t, text, r.Score)
			}
		})
	}
}

// The second guard rail: more candidates must not mean a budget overrun. The
// queries here are the ones D11 widened — a stopword-heavy question, and a
// query of nothing BUT common terms, which matches the whole corpus.
func TestD11WideMatchesStayInsideTheBudget(t *testing.T) {
	d, _ := buildCorpusDaemon(t)

	queries := []string{
		"what did the team decide about the authentication token rotation",
		"the project",
		"the",
	}
	for _, mode := range []string{rank.BudgetModeChars, rank.BudgetModeTokens} {
		counter, err := rank.CounterFor(mode)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range queries {
			for _, b := range []int{1, 17, 120, 1000, 9000} {
				chars, tokens := b, 0
				if mode == rank.BudgetModeTokens {
					chars, tokens = 0, b
				}
				out, err := d.Search(daemon.SearchOptions{Query: q, K: 100,
					BudgetChars: chars, BudgetTokens: tokens})
				if err != nil {
					t.Fatalf("%q %s %d: %v", q, mode, b, err)
				}
				if got := counter.Count(out.Payload); got > b {
					t.Fatalf("%q: payload %d %s exceeds budget %d", q, got, mode, b)
				}
				if out.Budget.Used > b {
					t.Fatalf("%q: reported budget_used %d exceeds budget %d", q, out.Budget.Used, b)
				}
			}
		}
	}
}

// The reason the term filter exists: without it, any query containing a word
// the corpus uses everywhere would make every document a candidate. The
// assertion is on the CANDIDATE POOL, not on the top-K, because that is where
// the cost lands — ranking would otherwise be deciding between the whole
// corpus on the strength of the one term that meant something.
func TestD11StopwordsDoNotDragInTheCorpus(t *testing.T) {
	d, keyToID := buildCorpusDaemon(t)

	out, err := d.Search(daemon.SearchOptions{
		Query: "what did the team decide about the authentication token rotation", K: 10})
	if err != nil {
		t.Fatal(err)
	}
	if out.LexicalQuery == nil {
		t.Fatal("no lexical query plan reported")
	}
	if !containsTerm(out.LexicalQuery.Common, "the") {
		t.Fatalf(`"the" was not dropped as non-discriminating: %+v`, out.LexicalQuery)
	}
	if out.Results[0].MessageID != keyToID["zebra-auth-fix"] {
		t.Fatalf("top hit is %s, want the authentication-rotation decision", out.Results[0].MessageID)
	}
	// the pool must be a fraction of the corpus, not all of it
	msgs, _ := goldenCorpus()
	pool, _, err := d.Projection().LexicalTopKPlan(
		"what did the team decide about the authentication token rotation", 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) > len(msgs)/2 {
		t.Fatalf("candidate pool is %d of %d documents — the common-term filter is not holding",
			len(pool), len(msgs))
	}
}
