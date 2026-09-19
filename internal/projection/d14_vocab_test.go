package projection_test

// D14 — the D11 term-discrimination probe is O(corpus) per query.
//
// The probe decided which query terms are too common to order results by
// COUNTING matching rowids under `LIMIT cutoff`, where cutoff is half the
// indexed corpus. D14 replaces the count with a lookup in the index's
// fts5vocab companion. The decision itself — the cutoff arithmetic, the
// single-document floor, the all-common fallback, the deliberate asymmetry
// when unmatched terms are present — is untouched.
//
// What these tests are FOR: proving the decision did not move. The old probe
// is kept as the oracle (LexicalPlansForTest computes both), so every
// assertion here is a differential against the implementation D14 replaced,
// not against a transcript of what it used to answer. That matters because
// this decision selects the candidate pool, and every ranking explanation
// reconciled under R47/R51 is an explanation of candidates it chose.

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ggoosen/cairn/internal/bench"
	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/projection"
)

// assertSamePlan compares the vocab plan with the probe oracle's, field by
// field, and returns the plan for further assertions.
func assertSamePlan(t *testing.T, p *projection.Projection, query string, derivatives bool) projection.LexicalPlan {
	t.Helper()
	vocab, probe, err := p.LexicalPlansForTest(query, derivatives)
	if err != nil {
		t.Fatalf("plans for %q: %v", query, err)
	}
	if !reflect.DeepEqual(vocab, probe) {
		t.Fatalf("D14 moved a D11 decision for %q:\n  vocab plan = %+v\n  probe plan = %+v", query, vocab, probe)
	}
	return vocab
}

// The acceptance criterion: for EVERY query in the golden corpus, the set of
// terms judged common (and unmatched, and searched) is identical to the old
// probe's. The corpus is the same 184 messages and 30 queries `cairn bench
// golden` scores and the ≥0.96 ratchet guards, loaded here at the projection
// layer so the comparison is of the decision alone.
func TestD14GoldenCorpusDecisionsAreIdentical(t *testing.T) {
	msgs, queries, err := bench.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	bodies := make([]string, 0, len(msgs))
	for _, m := range msgs {
		bodies = append(bodies, m.Body)
	}
	p, _ := buildBodyCorpus(t, bodies...)
	defer p.Close()

	var withCommon, withUnmatched, allCommon int
	for _, q := range queries {
		plan := assertSamePlan(t, p, q.Query, false)
		if len(plan.Common) > 0 {
			withCommon++
		}
		if len(plan.Unmatched) > 0 {
			withUnmatched++
		}
		if plan.AllCommon {
			allCommon++
		}
	}
	// A differential that never exercises a branch proves nothing about it, so
	// what the corpus reached is asserted rather than assumed. The golden
	// corpus is 184 diverse messages: no term of any of its 30 queries reaches
	// the half-corpus cutoff, so it exercises the UNMATCHED branch and the
	// ordinary kept-term branch, and TestD14ScorecardShapedCorpusDecisions
	// below carries the common-term branch on a corpus that has one.
	if withUnmatched == 0 {
		t.Fatalf("golden corpus dropped no unmatched term in %d queries — the differential is vacuous", len(queries))
	}
	if withCommon != 0 || allCommon != 0 {
		t.Fatalf("golden corpus now judges terms common (%d queries, %d all-common) — that is new, and the D14 differential must be re-read before it is believed",
			withCommon, allCommon)
	}
	t.Logf("%d golden queries: %d dropped a common term, %d an unmatched term, %d hit the all-common fallback",
		len(queries), withCommon, withUnmatched, allCommon)
}

// The awkward terms: everything about a query term that could make a Go-side
// fold disagree with SQLite's tokenizer. Each is checked against the oracle on
// a corpus built to make the term's document frequency knowable.
func TestD14AwkwardTermsDecideIdentically(t *testing.T) {
	bodies := []string{
		"the council approved the drainage levy in March",
		"Café naïve façade ÅNGSTRÖM Straße — accented words in a body",
		"identifiers like foo_bar and foo-bar and #tag and @who and topic-5",
		"punctuation: approval? approval! (approval) approval's approval.",
		"CamelCase MiXeD case UPPER lower",
		"日本語 のテキスト and 中文 text",
	}
	for i := 0; i < 8; i++ {
		bodies = append(bodies, fmt.Sprintf("the routine note %d about the ordinary business of the day", i))
	}
	p, _ := buildBodyCorpus(t, bodies...)
	defer p.Close()

	for _, q := range []string{
		"the council levy",                    // plain: one common, two rare
		"CAFÉ naïve straße",                   // non-ASCII, mixed case
		"café council",                        // non-ASCII beside ASCII
		"approval? the levy!",                 // trailing punctuation
		"approval's the council",              // an apostrophe splits a term in two
		"foo.bar the council",                 // a separator splits a term in two
		"foo_bar foo-bar #tag @who the",       // every tokenchar, plus a common term
		"TOPIC-5 the Council",                 // uppercase with a tokenchar
		"--- the council",                     // tokenchars only: a term matching nothing
		"... the council",                     // no token at all
		"日本語 中文 the",                          // CJK
		"the ordinary routine business day",   // every term common: the fallback
		"zzz qqq the",                         // unmatched terms present: the asymmetry
		"the",                                 // single term: the probes are skipped
		"the the the council",                 // duplicates deduped before probing
		strings.Repeat("x", 500) + " council", // a term longer than any token
	} {
		assertSamePlan(t, p, q, false)
	}
}

// A one-document mesh (the single-document floor) and an empty index (n == 0)
// are the two corpus sizes where the cutoff arithmetic is degenerate, so both
// get the differential too.
func TestD14DegenerateCorpusSizesDecideIdentically(t *testing.T) {
	one, _ := buildBodyCorpus(t, "the drainage levy was approved")
	defer one.Close()
	for _, q := range []string{"the drainage levy", "the levy zzz", "the"} {
		assertSamePlan(t, one, q, false)
	}

	two, _ := buildBodyCorpus(t, "alpha beta", "alpha gamma")
	defer two.Close()
	for _, q := range []string{"alpha beta", "alpha gamma zzz", "beta gamma"} {
		assertSamePlan(t, two, q, false)
	}
}

// indexToken's contract, pinned directly: it folds ASCII exactly as
// `unicode61 tokenchars '_-#@'` does, and refuses everything else so the
// bounded probe answers instead. A term it folds WRONG would read another
// term's document frequency and move a ranking decision silently — the one
// failure mode this change could introduce.
func TestD14IndexTokenIsASCIIOnlyAndExact(t *testing.T) {
	for _, tc := range []struct {
		term string
		want string
		ok   bool
	}{
		{"council", "council", true},
		{"COUNCIL", "council", true},
		{"CoUnCiL", "council", true},
		{"topic-5", "topic-5", true},    // '-' is a tokenchar
		{"foo_bar", "foo_bar", true},    // '_' is a tokenchar
		{"#tag", "#tag", true},          // '#' is a tokenchar
		{"@who", "@who", true},          // '@' is a tokenchar
		{"approval?", "approval", true}, // trailing punctuation separates
		{"(approval)", "approval", true},
		{"---", "---", true}, // tokenchars only: still one token
		{"9", "9", true},
		{"", "", false},           // no token
		{"...", "", false},        // no token
		{"foo.bar", "", false},    // two tokens: a phrase, not a term
		{"don't", "", false},      // two tokens
		{"a b", "", false},        // two tokens (fields are split before this, but be exact)
		{"café", "", false},       // non-ASCII: the tokenizer is the authority
		{"Straße", "", false},     // non-ASCII
		{"日本語", "", false},        // non-ASCII
		{"cafe\u0301", "", false}, // ASCII letters plus a combining mark
	} {
		got, ok := projection.IndexTokenForTest(tc.term)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("IndexTokenForTest(%q) = (%q, %v), want (%q, %v)", tc.term, got, ok, tc.want, tc.ok)
		}
	}
}

// The fold above is written against config.FTSTokenChars; the DDL carries the
// tokenizer as a literal because schema.sql cannot interpolate a constant.
// If the two ever drift, query terms get folded by rules the index never
// applied — silently, and only for terms containing the drifted character.
func TestD14TokenizerConstantMatchesTheSchema(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)
	if !strings.Contains(schema, config.FTSTokenize) {
		t.Fatalf("schema.sql does not use config.FTSTokenize (%q) — the D14 fold would not match the index", config.FTSTokenize)
	}
	for _, c := range []byte(config.FTSTokenChars) {
		if !strings.Contains(config.FTSTokenize, string(c)) {
			t.Fatalf("FTSTokenChars contains %q, which FTSTokenize does not declare", string(c))
		}
	}
	// every FTS index over body text must use it — a companion created with a
	// different tokenizer would need a different fold
	if n := strings.Count(schema, config.FTSTokenize); n != 2 {
		t.Fatalf("expected exactly 2 unicode61 indexes (revisions, derivatives); schema declares %d", n)
	}
}

// The scorecard's corpus shape, where the common branch actually fires: every
// body carries the word "routine", so every query carries exactly one term the
// index says cannot order results — the case D14 was raised from, and the one
// the golden corpus does not contain.
func TestD14ScorecardShapedCorpusDecisions(t *testing.T) {
	var bodies []string
	for i := 0; i < 400; i++ {
		bodies = append(bodies, fmt.Sprintf(
			"scorecard message %d: synthetic corpus entry about topic-%d with routine detail", i, i%97))
	}
	p, _ := buildBodyCorpus(t, bodies...)
	defer p.Close()

	var withCommon int
	for i := 0; i < 97; i++ {
		q := fmt.Sprintf("topic-%d routine", i)
		plan := assertSamePlan(t, p, q, false)
		if len(plan.Common) > 0 {
			withCommon++
		}
		if !contains(plan.Common, "routine") {
			t.Fatalf("%q: %q is in every document and was not dropped: %+v", q, "routine", plan)
		}
		if !contains(plan.Terms, fmt.Sprintf("topic-%d", i)) {
			t.Fatalf("%q: the discriminating term was not searched: %+v", q, plan)
		}
	}
	if withCommon != 97 {
		t.Fatalf("only %d of 97 queries dropped a common term", withCommon)
	}
	// and the all-common fallback, on the same corpus
	plan := assertSamePlan(t, p, "routine scorecard message", false)
	if !plan.AllCommon {
		t.Fatalf("every term of %q is in every document; the fallback did not fire: %+v", "routine scorecard message", plan)
	}
}

// A randomized differential over the same rule: the fold is the risky part of
// D14, so the terms it is fed should not all be ones a human thought of. Bodies
// and queries are drawn from an alphabet chosen to straddle every boundary the
// fold has — ASCII letters and digits, the four tokenchars, separators, Latin
// accents, sharp s, CJK, and a combining mark — at document frequencies from
// "one document" to "every document". The seed is fixed so a failure is
// reproducible; a difference here is D11's semantics moving.
func TestD14RandomizedDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(20260818))
	alphabet := []string{
		"the", "council", "levy", "drainage", "routine", "approval", "note",
		"topic-5", "foo_bar", "#tag", "@who", "a-b-c", "x9", "9",
		"café", "naïve", "Straße", "ÅNGSTRÖM", "日本語", "中文", "café",
		"foo.bar", "don't", "(brackets)", "trailing?", "---", "...", "ALLCAPS",
		"MiXeD", "zzz", "qqq",
	}
	for round := 0; round < 25; round++ {
		nDocs := 1 + rng.Intn(24)
		bodies := make([]string, nDocs)
		for i := range bodies {
			var words []string
			// "the" everywhere, so at least one term is reliably common
			words = append(words, "the")
			for j := 0; j < 3+rng.Intn(6); j++ {
				words = append(words, alphabet[rng.Intn(len(alphabet))])
			}
			bodies[i] = strings.Join(words, " ")
		}
		p, _ := buildBodyCorpus(t, bodies...)
		for q := 0; q < 12; q++ {
			var terms []string
			for j := 0; j < 1+rng.Intn(4); j++ {
				terms = append(terms, alphabet[rng.Intn(len(alphabet))])
			}
			assertSamePlan(t, p, strings.Join(terms, " "), false)
		}
		p.Close()
	}
}

// The invariant D14 rests on, tested against SQLite itself rather than argued
// in a comment: whenever indexToken claims a term folds to one token, the
// index's own tokenizer produces exactly that token for it. Everything else —
// the vocabulary lookup, the fallback, the identical decisions above — follows
// from this holding. It is checked over every ASCII byte (the range the fold
// claims), over terms built from them, and over non-ASCII terms, where the
// fold must refuse rather than answer.
func TestD14FoldMatchesTheTokenizer(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE tok USING fts5(body, content='', tokenize="` +
		config.FTSTokenize + `")`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE tok_vocab USING fts5vocab('tok','row')`); err != nil {
		t.Fatal(err)
	}
	// tokenize returns the tokens SQLite produced for one term, in index order.
	tokenize := func(term string) []string {
		// a contentless index has no DELETE; 'delete-all' is FTS5's command for
		// emptying one, and it is what keeps each term measured in isolation
		if _, err := db.Exec(`INSERT INTO tok(tok) VALUES('delete-all')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO tok(rowid, body) VALUES(1, ?)`, term); err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query(`SELECT term FROM tok_vocab ORDER BY term`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}

	var terms []string
	for b := 0; b < 128; b++ { // every ASCII byte, alone and inside a word
		terms = append(terms, string(rune(b)), "ab"+string(rune(b))+"cd", string(rune(b))+"lead")
	}
	terms = append(terms,
		"council", "COUNCIL", "CoUnCiL", "topic-5", "foo_bar", "#tag", "@who",
		"approval?", "(approval)", "---", "9", "x9", "foo.bar", "don't",
		"café", "CAFÉ", "naïve", "Straße", "ÅNGSTRÖM", "日本語", "中文", "café",
		"ﬀ", "ǅ", "İ", "ß", "Ⅻ", "𝔘nicode",
	)
	folded := 0
	for _, term := range terms {
		tok, ok := projection.IndexTokenForTest(term)
		if !ok {
			continue // refused: the probe answers, and cannot be wrong
		}
		folded++
		got := tokenize(term)
		if len(got) != 1 || got[0] != tok {
			t.Fatalf("fold disagrees with the tokenizer for %q: fold=%q, tokenizer=%q — a vocabulary lookup would read another term's document frequency",
				term, tok, got)
		}
	}
	if folded < 100 {
		t.Fatalf("only %d terms took the fold path; the property was barely exercised", folded)
	}
	t.Logf("%d of %d terms folded; the rest fall back to the probe", folded, len(terms))
}

// D14's second half: the derivative and trigram unions can only fill slots the
// word index left empty, so when the word index already returned k candidates
// their hits are discarded — and on a large corpus the trigram query is the
// most expensive thing a search does (53 ms of a 62 ms search at 100k). Skipping
// it must change no answer, in both directions: a full pool must return exactly
// the word hits and run no companion query, and a pool with room must still get
// the trigram-only hit that C2 exists for.
func TestD14UnionsSkippedOnlyWhenTheyCannotContribute(t *testing.T) {
	var bodies []string
	for i := 0; i < 12; i++ {
		bodies = append(bodies, fmt.Sprintf("council minutes %d approving the drainage levy", i))
	}
	// reachable ONLY as a substring: "ounci" is not a token
	bodies = append(bodies, "the recouncilment ledger, an invented word")
	p, ids := buildBodyCorpus(t, bodies...)
	defer p.Close()
	substringOnly := ids[len(ids)-1]

	// pool with room: the trigram companion runs and contributes
	d0, t0 := p.UnionQueriesForTest()
	got, _, err := p.LexicalTopKPlan("ounci", 25, false)
	if err != nil {
		t.Fatal(err)
	}
	d1, t1 := p.UnionQueriesForTest()
	if t1 != t0+1 || d1 != d0+1 {
		t.Fatalf("companion queries were skipped with room in the pool (derivative %d→%d, trigram %d→%d)", d0, d1, t0, t1)
	}
	if !contains(got, substringOnly) {
		t.Fatalf("substring-only hit missing: %v", got)
	}

	// the boundary that matters: ONE free slot left. The word index answers
	// "council" with its 12 documents; the thirteenth contains the string only
	// inside "recouncilment", so it is the trigram companion's hit and it must
	// still arrive. A guard that skipped here would silently shorten every
	// result list by its last entry.
	d0, t0 = p.UnionQueriesForTest()
	edge, _, err := p.LexicalTopKPlan("council", 13, false)
	if err != nil {
		t.Fatal(err)
	}
	d1, t1 = p.UnionQueriesForTest()
	if t1 != t0+1 || d1 != d0+1 {
		t.Fatalf("companion queries skipped with one slot free (derivative %d→%d, trigram %d→%d)", d0, d1, t0, t1)
	}
	if len(edge) != 13 || !contains(edge, substringOnly) {
		t.Fatalf("the last slot lost its trigram-only hit: %v", edge)
	}

	// full pool: the word index alone fills k, so the companions cannot
	// contribute — and must not run
	d0, t0 = p.UnionQueriesForTest()
	full, _, err := p.LexicalTopKPlan("council", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	d1, t1 = p.UnionQueriesForTest()
	if len(full) != 3 {
		t.Fatalf("expected a full pool of 3, got %v", full)
	}
	if d1 != d0 || t1 != t0 {
		t.Fatalf("a companion query ran for a full pool (derivative %d→%d, trigram %d→%d) — its results could only be discarded", d0, d1, t0, t1)
	}
	// and the answer is the word index's own top-k, unchanged by the skip
	want, err := p.SearchLexical("council", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range want {
		if full[i] != r.MessageID {
			t.Fatalf("skipping the companions changed the answer: %v vs %v", full, want)
		}
	}
}
