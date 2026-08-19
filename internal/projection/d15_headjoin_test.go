package projection_test

// D15 — the candidate query's per-match cost.
//
// After D14 the largest single cost in a 100k search was the word-index
// candidate query: every MATCHING document (not every returned one) was joined
// fts → revisions → messages to test headness and retraction before bm25 cut
// the pool to k. Two of those three seeks answered a question the messages row
// had already answered, because a revision belongs to exactly one message and
// a message's head_revision_id names a revision of that same message.
//
// Dropping a join from a FILTER is a claim about supersession and retraction —
// the two things that must never surface — so the pre-D15 shape is kept as the
// oracle and every assertion here is differential against it, the way D11's
// probe is D14's oracle and brute-force cosine is the vector index's
// (rulings §7).

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/testutil"
)

// d15Queries covers the fixture's live bodies, its SUPERSEDED body, its
// RETRACTED body, identifier fragments (the trigram companion's reason to
// exist), a term in every document, and terms in none.
var d15Queries = []string{
	"zebra", "zebras", "migrations", "gamma", "alpha document",
	"the zebra crossings", "ECONNREFUSED", "PeerAdd", "0190a1b2", "wiki",
	"document about zebra migrations", "nothing here matches", "the", "",
}

// assertCandidatesAgree drives both candidate queries and fails on any
// difference in the ids OR their order.
func assertCandidatesAgree(t *testing.T, p *projection.Projection, label string) (headHits, trigramHits int) {
	t.Helper()
	for _, q := range d15Queries {
		for _, incl := range []bool{false, true} {
			for _, k := range []int{1, 2, 5, 100} {
				direct, oracle, err := p.LexicalCandidatesForTest(q, k, incl)
				if err != nil {
					t.Fatalf("%s: lexical candidates %q k=%d incl=%v: %v", label, q, k, incl, err)
				}
				if !reflect.DeepEqual(direct, oracle) {
					t.Fatalf("%s: D15 moved a candidate set for %q k=%d incl=%v:\n  direct = %v\n  oracle = %v",
						label, q, k, incl, direct, oracle)
				}
				headHits += len(direct)
				tdirect, toracle, err := p.TrigramCandidatesForTest(q, k, incl)
				if err != nil {
					t.Fatalf("%s: trigram candidates %q k=%d incl=%v: %v", label, q, k, incl, err)
				}
				if !reflect.DeepEqual(tdirect, toracle) {
					t.Fatalf("%s: D15 moved a TRIGRAM candidate set for %q k=%d incl=%v:\n  direct = %v\n  oracle = %v",
						label, q, k, incl, tdirect, toracle)
				}
				trigramHits += len(tdirect)
			}
		}
	}
	return headHits, trigramHits
}

// assertHeadOwnership is the invariant the one-join filter rests on: a
// message's head_revision_id always names a revision OF THAT MESSAGE, and that
// revision exists. The writer holds it by construction (applyPayload inserts
// the revision and sets the head in one transaction, and revision_id is a
// primary key so no two messages can claim the same revision), which is what
// makes "this revision is some message's head" equivalent to the three-join
// "this revision belongs to m and is m's head".
func assertHeadOwnership(t *testing.T, p *projection.Projection, label string) {
	t.Helper()
	var orphan, foreign int
	db := p.DBForTest()
	if err := db.QueryRow(`SELECT count(*) FROM messages m
		WHERE NOT EXISTS (SELECT 1 FROM revisions r WHERE r.revision_id = m.head_revision_id)`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM messages m JOIN revisions r ON r.revision_id = m.head_revision_id
		WHERE r.message_id <> m.message_id`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if orphan != 0 || foreign != 0 {
		t.Fatalf("%s: head-revision ownership broken: %d heads with no revision, %d heads owned by another message",
			label, orphan, foreign)
	}
}

// The representative fixture: a superseded revision, a retracted message,
// identifier bodies, and a term the corpus says nowhere.
func TestD15CandidateQueriesAgreeWithTheThreeJoinOracle(t *testing.T) {
	m, _, store := buildCorpus(t)
	p := openAndReplay(t, m, filepath.Join(t.TempDir(), "index.sqlite"), store)
	defer p.Close()

	assertHeadOwnership(t, p, "fixture")
	head, tri := assertCandidatesAgree(t, p, "fixture")
	if head == 0 || tri == 0 {
		t.Fatalf("the differential never returned a candidate (word %d, trigram %d) — it would agree vacuously", head, tri)
	}

	// The filter must still be doing its job, not merely agreeing with itself:
	// the corpus HAS an FTS row for the superseded body and one for the
	// retracted message, and neither may reach the candidate pool.
	superseded, _, err := p.LexicalCandidatesForTest("migrations", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(superseded) != 0 {
		t.Fatalf("a superseded revision reached the candidate pool: %v", superseded)
	}
	retracted, _, err := p.LexicalCandidatesForTest("gamma", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(retracted) != 0 {
		t.Fatalf("a retracted message reached the candidate pool: %v", retracted)
	}
	shown, _, err := p.LexicalCandidatesForTest("gamma", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 {
		t.Fatalf("include_retracted did not restore the retracted message: %v", shown)
	}
}

// The differential is only worth having if it can FAIL. Break the invariant by
// hand — point a message's head at another message's revision, which the event
// writer cannot do — and the two queries must disagree. This is the drift the
// one-join filter would be blind to, made visible.
func TestD15OracleCatchesABrokenHeadOwnership(t *testing.T) {
	m, _, store := buildCorpus(t)
	p := openAndReplay(t, m, filepath.Join(t.TempDir(), "index.sqlite"), store)
	defer p.Close()

	if _, err := p.DBForTest().Exec(
		`UPDATE messages SET head_revision_id = ? WHERE message_id = ?`, revA1, msgB); err != nil {
		t.Fatal(err)
	}
	direct, oracle, err := p.LexicalCandidatesForTest("zebra migrations", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(direct, oracle) {
		t.Fatalf("a head pointing at ANOTHER message's revision changed nothing (%v) — "+
			"the differential cannot see the drift it exists to see", direct)
	}
}

// Randomized differential: many small meshes of publishes, revisions (one and
// two at a time, the merge shape), and retractions, so the filter is exercised
// against corpora with many superseded revisions rather than the fixture's one.
func TestD15RandomizedDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(20260819))
	words := []string{"zebra", "drainage", "levy", "routine", "council", "roastery", "invoice", "the"}
	uuid := func(n int) string { return fmt.Sprintf("0190a1b2-c3d4-7e5f-8901-%012d", n) }

	for corpus := 0; corpus < 12; corpus++ {
		mem := fsx.NewMemFS()
		mem.MkdirAll("/p", 0o700)
		store := object.NewStore(mem, "/p")
		c, genv, grec := testutil.NewChain(t)
		lg, err := cairnlog.Create(mem, "/p", cairnlog.Origin{DeviceID: c.DeviceID, Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := lg.Append(grec, genv); err != nil {
			t.Fatal(err)
		}
		app := func(eventType, objType, objID string, payload map[string]any) {
			env, rec := c.Event(t, eventType, objType, objID, payload, "operator")
			if err := lg.Append(rec, env); err != nil {
				t.Fatal(err)
			}
		}
		body := func() string {
			var b string
			for i := 0; i < 3+rng.Intn(5); i++ {
				b += words[rng.Intn(len(words))] + " "
			}
			return b
		}
		put := func(text string) string {
			h, err := store.Put([]byte(text))
			if err != nil {
				t.Fatal(err)
			}
			return h
		}
		ids := 0
		var msgs []string
		for i := 0; i < 8+rng.Intn(12); i++ {
			ids++
			msg, rev := uuid(ids), uuid(ids+10000)
			text := body()
			app("message.publish", "message", msg, map[string]any{
				"message_id": msg, "revision_id": rev, "body_hash": put(text),
				"body_bytes": text, "body_len": len(text), "body_mime": "text/markdown",
				"text_class": "canonical", "declared_priority": rng.Intn(4),
			})
			msgs = append(msgs, msg)
		}
		for _, msg := range msgs {
			switch rng.Intn(4) {
			case 0: // one new revision
				ids++
				text := body()
				app("message.revise_body", "message", msg, map[string]any{
					"message_id": msg,
					"revisions": []map[string]any{{
						"revision_id": uuid(ids + 10000), "body_hash": put(text),
						"body_bytes": text, "body_len": len(text),
					}},
				})
			case 1: // two revisions in one event — the last one becomes the head
				ids++
				t1, t2 := body(), body()
				r1, r2 := uuid(ids+10000), uuid(ids+20000)
				app("message.revise_body", "message", msg, map[string]any{
					"message_id": msg,
					"revisions": []map[string]any{
						{"revision_id": r1, "body_hash": put(t1), "body_bytes": t1, "body_len": len(t1)},
						{"revision_id": r2, "parent_revision_ids": []string{r1}, "body_hash": put(t2),
							"body_bytes": t2, "body_len": len(t2), "machine_merged": true},
					},
				})
			case 2: // retract
				app("message.retract", "message", msg, map[string]any{"message_id": msg, "reason": "draft"})
			}
		}
		lg.Close()

		p := openAndReplay(t, mem, filepath.Join(t.TempDir(), "index.sqlite"), store)
		label := fmt.Sprintf("random corpus %d", corpus)
		assertHeadOwnership(t, p, label)
		assertCandidatesAgree(t, p, label)
		for _, q := range words {
			for _, incl := range []bool{false, true} {
				direct, oracle, err := p.LexicalCandidatesForTest(q+" "+words[rng.Intn(len(words))], 10, incl)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(direct, oracle) {
					t.Fatalf("%s: %q incl=%v: direct %v, oracle %v", label, q, incl, direct, oracle)
				}
			}
		}
		p.Close()
	}
}

// A projection rebuilt by `reindex --lexical` populates fts_map from the
// revisions table rather than from the append path, so the filter has to agree
// there too — a rebuilt index is the state most meshes will actually query.
func TestD15AgreesAfterLexicalReindex(t *testing.T) {
	m, _, store := buildCorpus(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.sqlite")
	p := openAndReplay(t, m, dbPath, store)
	before, _, err := p.LexicalCandidatesForTest("zebra", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	if _, err := projection.ReindexLexical(m, "/p", dbPath,
		func() cairnlog.VerifyFunc { return identity.NewChainVerifier().Verify }, nil); err != nil {
		t.Fatal(err)
	}
	p2, err := projection.Open(dbPath, projection.StoreBodyFetch(store))
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	assertHeadOwnership(t, p2, "after reindex")
	assertCandidatesAgree(t, p2, "after reindex")
	after, _, err := p2.LexicalCandidatesForTest("zebra", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("reindex changed the candidate set: before %v, after %v", before, after)
	}
}

// D15 moved the candidate query's ORDER BY into shared code, so the
// determinism requirement (rulings §7: ties break by message_id) is pinned
// here rather than left implicit. Two documents with IDENTICAL text score
// identically under bm25, and their message ids are published in DESCENDING
// order so that insertion order — which is what an unspecified tie would fall
// back to — is the opposite of the required one.
func TestD15TiedCandidatesBreakByMessageID(t *testing.T) {
	mem := fsx.NewMemFS()
	mem.MkdirAll("/p", 0o700)
	store := object.NewStore(mem, "/p")
	c, genv, grec := testutil.NewChain(t)
	lg, err := cairnlog.Create(mem, "/p", cairnlog.Origin{DeviceID: c.DeviceID, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append(grec, genv); err != nil {
		t.Fatal(err)
	}
	const body = "drainage levy identical body"
	ids := []string{
		"0190a1b2-c3d4-7e5f-8901-00000000ff09",
		"0190a1b2-c3d4-7e5f-8901-00000000ff05",
		"0190a1b2-c3d4-7e5f-8901-00000000ff01",
	}
	for i, id := range ids {
		publish(t, c, store, lg, id, fmt.Sprintf("0190a1b2-c3d4-7e5f-8901-0000000ee00%d", i), body, "canonical")
	}
	lg.Close()

	p := openAndReplay(t, mem, filepath.Join(t.TempDir(), "index.sqlite"), store)
	defer p.Close()
	got, oracle, err := p.LexicalCandidatesForTest("drainage levy", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ids[2], ids[1], ids[0]} // ascending message_id, not insertion order
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tied candidates were not ordered by message_id:\n  got  %v\n  want %v", got, want)
	}
	if !reflect.DeepEqual(got, oracle) {
		t.Fatalf("direct %v, oracle %v", got, oracle)
	}
}
