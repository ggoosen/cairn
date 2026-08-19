package projection_test

// D1 — the vec0 index against its oracle.
//
// The acceptance criterion that matters here is EQUIVALENCE: the sqlite-vec
// path and the brute-force cosine scan must return the identical top-K on a
// seeded corpus. A vector path with no independent check is a
// silent-corruption surface, so brute force stays in the tree as the oracle
// and every case below asks both.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/projection"
)

const (
	vecModelA = "dev-model-a"
	vecModelB = "dev-model-b"
	vecDim    = 32
)

// seedVectors builds a synthetic corpus directly in the projection tables.
// The events/messages/revisions rows are fixture scaffolding (the projection's
// own replay is covered elsewhere); the vectors go in through the REAL
// InsertVector, which is the write path D1 changed.
func seedVectors(t *testing.T, p *projection.Projection, n int, model string, seed int64, dupEvery int) ([]string, map[string][]float32) {
	t.Helper()
	db := p.DBForTest()
	rng := rand.New(rand.NewSource(seed))
	ids := make([]string, 0, n)
	vecs := map[string][]float32{}
	var dup []float32
	for i := 0; i < n; i++ {
		msg := fmt.Sprintf("%s-m%06d", model, i)
		rev := fmt.Sprintf("%s-r%06d", model, i)
		ev := fmt.Sprintf("%s-e%06d", model, i)
		retracted := 0
		if i%17 == 0 {
			retracted = 1
		}
		if _, err := db.Exec(`INSERT INTO events(event_id, event_type, origin_device_id, origin_generation,
			origin_sequence, wall_time, payload_json) VALUES (?,?,?,?,?,?,?)`,
			ev, "message.publish", model, 1, int64(i)+1, "2026-01-01T00:00:00.000000Z", "{}"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages(message_id, head_revision_id, declared_priority, text_class,
			created_event_id, created_at, retracted) VALUES (?,?,?,?,?,?,?)`,
			msg, rev, 1, "canonical", ev, "2026-01-01T00:00:00.000000Z", retracted); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO revisions(revision_id, message_id, body_hash, body_len, body_mime,
			created_event_id, created_at) VALUES (?,?,?,?,?,?,?)`,
			rev, msg, "hash", 1, "text/markdown", ev, "2026-01-01T00:00:00.000000Z"); err != nil {
			t.Fatal(err)
		}
		// Exact duplicates every dupEvery items: identical similarities are
		// the case where "identical top-K" stops being automatic and starts
		// depending on both paths agreeing on the tiebreak.
		var v []float32
		if dupEvery > 0 && i%dupEvery == 0 && dup != nil {
			v = append([]float32(nil), dup...)
		} else {
			v = randUnit(rng)
			if dup == nil {
				dup = append([]float32(nil), v...)
			}
		}
		if err := p.InsertVector(rev, model, v); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg)
		vecs[msg] = v
	}
	return ids, vecs
}

func randUnit(rng *rand.Rand) []float32 {
	v := make([]float32, vecDim)
	var norm float64
	for i := range v {
		x := rng.NormFloat64()
		v[i] = float32(x)
		norm += x * x
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func openVecProjection(t *testing.T, path string) *projection.Projection {
	t.Helper()
	p, err := projection.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func requireVecIndex(t *testing.T, p *projection.Projection) {
	t.Helper()
	if !p.VectorIndexActive() {
		t.Skipf("sqlite-vec not available in this build (%s) — the fallback tests still run", p.VectorIndexNote())
	}
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestVecEquivalence: identical top-K, unscoped and scoped, with and without
// retracted messages, with exact-duplicate vectors forcing ties.
func TestVecEquivalence(t *testing.T) {
	p := openVecProjection(t, filepath.Join(t.TempDir(), "index.sqlite"))
	requireVecIndex(t, p)
	ids, _ := seedVectors(t, p, 400, vecModelA, 1, 7)

	rng := rand.New(rand.NewSource(99))
	scopeEvery := map[string]bool{}
	for i, id := range ids {
		if i%3 == 0 {
			scopeEvery[id] = true
		}
	}
	empty := map[string]bool{}

	cases := []struct {
		name       string
		k          int
		retracted  bool
		scope      map[string]bool
		wantNonNil bool
	}{
		{name: "unscoped k10", k: 10},
		{name: "unscoped k100", k: 100},
		{name: "unscoped k1", k: 1},
		{name: "unscoped include-retracted", k: 50, retracted: true},
		{name: "scoped third", k: 25, scope: scopeEvery},
		{name: "scoped third include-retracted", k: 25, scope: scopeEvery, retracted: true},
		{name: "scoped empty", k: 10, scope: empty},
		{name: "k beyond corpus, within the index ceiling", k: config.VectorIndexMaxK},
	}
	for _, tc := range cases {
		for trial := 0; trial < 5; trial++ {
			q := randUnit(rng)
			fast, err := p.VectorTopKIndexed(vecModelA, q, tc.k, tc.retracted, tc.scope)
			if err != nil {
				t.Fatalf("%s: vec0: %v", tc.name, err)
			}
			slow, err := p.VectorTopKBruteForce(vecModelA, q, tc.k, tc.retracted, tc.scope)
			if err != nil {
				t.Fatalf("%s: brute force: %v", tc.name, err)
			}
			if !sameIDs(fast, slow) {
				t.Fatalf("%s trial %d: vec0 and brute force disagree\n vec0: %v\nbrute: %v", tc.name, trial, fast, slow)
			}
		}
	}
	// k ABOVE sqlite-vec's KNN ceiling: the routed entry point must answer
	// from the oracle instead of failing.
	bigQ := randUnit(rng)
	routed := mustIDs(p.VectorTopK(vecModelA, bigQ, config.VectorIndexMaxK+1, false, nil))
	oracle := mustIDs(p.VectorTopKBruteForce(vecModelA, bigQ, config.VectorIndexMaxK+1, false, nil))
	if !sameIDs(routed, oracle) || len(routed) == 0 {
		t.Fatal("a k above the index ceiling did not fall back to the oracle")
	}
	if _, err := p.VectorTopKIndexed(vecModelA, bigQ, config.VectorIndexMaxK+1, false, nil); err == nil {
		t.Fatal("the index accepted a k above its own ceiling")
	}

	// scoped-empty must mean NOTHING, never "unfiltered"
	got, err := p.VectorTopKIndexed(vecModelA, randUnit(rng), 10, false, empty)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty scope returned %v (err %v) — an empty grant must return nothing", got, err)
	}
}

// TestVecEquivalenceAtScale drives the corpus past config.BruteForceMaxCandidates
// — the cliff D1 exists to remove — and asserts both that the answers still
// match and that the fast path does NOT pull every vector into the process.
func TestVecEquivalenceAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale corpus; -short")
	}
	p := openVecProjection(t, filepath.Join(t.TempDir(), "index.sqlite"))
	requireVecIndex(t, p)
	n := config.BruteForceMaxCandidates + 1000
	seedVectors(t, p, n, vecModelA, 7, 0)

	rng := rand.New(rand.NewSource(4242))
	for trial := 0; trial < 3; trial++ {
		q := randUnit(rng)
		fast, err := p.VectorTopKIndexed(vecModelA, q, config.FusionCandidatesVector, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		slow, err := p.VectorTopKBruteForce(vecModelA, q, config.FusionCandidatesVector, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !sameIDs(fast, slow) {
			t.Fatalf("trial %d over %d vectors: paths disagree\n vec0: %v\nbrute: %v", trial, n, fast[:10], slow[:10])
		}
	}

	// The allocation bound is the point of the whole item: brute force must
	// materialize every head vector, the index must not.
	q := randUnit(rng)
	fastBytes := allocBytes(t, func() {
		if _, err := p.VectorTopKIndexed(vecModelA, q, config.FusionCandidatesVector, false, nil); err != nil {
			t.Fatal(err)
		}
	})
	slowBytes := allocBytes(t, func() {
		if _, err := p.VectorTopKBruteForce(vecModelA, q, config.FusionCandidatesVector, false, nil); err != nil {
			t.Fatal(err)
		}
	})
	corpusBytes := uint64(n) * uint64(vecDim) * 4
	t.Logf("allocated: vec0 %d B, brute force %d B, corpus %d B", fastBytes, slowBytes, corpusBytes)
	if fastBytes > corpusBytes/4 {
		t.Fatalf("vec0 path allocated %d B for a %d B corpus — it is still loading every vector", fastBytes, corpusBytes)
	}
	if slowBytes < corpusBytes {
		t.Fatalf("brute force allocated only %d B for a %d B corpus — the oracle is not doing the work it claims", slowBytes, corpusBytes)
	}
}

func allocBytes(t *testing.T, fn func()) uint64 {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestVecNeverCrossesModels: vectors are keyed (revision_id, model) and must
// never be compared across models (schema.sql). The vec0 design keeps that by
// partitioning on the model id, so another model's rows are not merely
// filtered out — they are in another partition.
func TestVecNeverCrossesModels(t *testing.T) {
	p := openVecProjection(t, filepath.Join(t.TempDir(), "index.sqlite"))
	requireVecIndex(t, p)
	seedVectors(t, p, 60, vecModelA, 11, 0)
	seedVectors(t, p, 60, vecModelB, 11, 0) // SAME seed: identical vectors, different model

	q := randUnit(rand.New(rand.NewSource(11)))
	for _, model := range []string{vecModelA, vecModelB} {
		for _, got := range [][]string{
			mustIDs(p.VectorTopKIndexed(model, q, 20, true, nil)),
			mustIDs(p.VectorTopKBruteForce(model, q, 20, true, nil)),
		} {
			if len(got) == 0 {
				t.Fatalf("%s: no results", model)
			}
			for _, id := range got {
				if len(id) < len(model) || id[:len(model)] != model {
					t.Fatalf("%s query returned %q — a vector from another model", model, id)
				}
			}
		}
	}
}

// mustIDs takes the two return values directly so a query can be inlined; a
// failure here is a broken fixture, not a result worth reporting gently.
func mustIDs(ids []string, err error) []string {
	if err != nil {
		panic("vector query failed: " + err.Error())
	}
	return ids
}

// TestVecAbsentStillServes: with the extension unavailable, Open SUCCEEDS,
// the capability reports the fallback, and queries answer from brute force.
// Rulings §7 makes this a supported state, not a fault.
func TestVecAbsentStillServes(t *testing.T) {
	t.Setenv(projection.EnvVectorIndex, "off")
	p := openVecProjection(t, filepath.Join(t.TempDir(), "index.sqlite"))
	if p.VectorIndexActive() {
		t.Fatal("the disable switch did not take: the index is still active")
	}
	if p.VectorIndexNote() == "" {
		t.Fatal("no operator-readable reason for the fallback")
	}
	ids, _ := seedVectors(t, p, 120, vecModelA, 3, 5)
	if len(ids) != 120 {
		t.Fatal("seed failed")
	}
	q := randUnit(rand.New(rand.NewSource(3)))
	routed := mustIDs(p.VectorTopK(vecModelA, q, 10, false, nil))
	oracle := mustIDs(p.VectorTopKBruteForce(vecModelA, q, 10, false, nil))
	if !sameIDs(routed, oracle) {
		t.Fatalf("fallback routing wrong: %v vs %v", routed, oracle)
	}
	if len(routed) == 0 {
		t.Fatal("no results with the extension absent — the fallback must still answer")
	}
}

// TestVecIndexRebuiltAfterExtensionlessWrites: a projection written by a build
// (or a run) WITHOUT the index accumulates vectors it never saw. The next run
// with the index must reconcile from the source of truth rather than answer
// from a half-filled index.
func TestVecIndexRebuiltAfterExtensionlessWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.sqlite")

	// Phase 1: extension off. Vectors land in `vectors`; nothing indexes them.
	os.Setenv(projection.EnvVectorIndex, "off")
	p1, err := projection.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedVectors(t, p1, 150, vecModelA, 5, 4)
	p1.Close()
	os.Unsetenv(projection.EnvVectorIndex)

	// Phase 2: extension on. Open must reconcile.
	p2 := openVecProjection(t, path)
	requireVecIndex(t, p2)
	var mapped, stored int
	db := p2.DBForTest()
	if err := db.QueryRow(`SELECT count(*) FROM vec_map`).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM vectors`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if mapped != stored || stored != 150 {
		t.Fatalf("index not reconciled at Open: %d indexed vs %d stored", mapped, stored)
	}
	q := randUnit(rand.New(rand.NewSource(5)))
	if !sameIDs(mustIDs(p2.VectorTopKIndexed(vecModelA, q, 20, false, nil)),
		mustIDs(p2.VectorTopKBruteForce(vecModelA, q, 20, false, nil))) {
		t.Fatal("rebuilt index disagrees with the oracle")
	}
}

// TestVecInvalidateClearsIndex: a model migration empties the source of truth,
// so the derived index must go with it — a stale neighbour from the old model
// is exactly what "never compare across models" forbids.
func TestVecInvalidateClearsIndex(t *testing.T) {
	p := openVecProjection(t, filepath.Join(t.TempDir(), "index.sqlite"))
	requireVecIndex(t, p)
	seedVectors(t, p, 40, vecModelA, 8, 0)
	if err := p.InvalidateVectors(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := p.DBForTest().QueryRow(`SELECT count(*) FROM vec_map`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("vec_map still holds %d rows after invalidate", n)
	}
	q := randUnit(rand.New(rand.NewSource(8)))
	got := mustIDs(p.VectorTopK(vecModelA, q, 10, false, nil))
	if len(got) != 0 {
		t.Fatalf("invalidated projection still returns vectors: %v", got)
	}
	// and the index re-creates itself on the next write, at the new width
	seedVectors(t, p, 10, vecModelB, 9, 0)
	if !sameIDs(mustIDs(p.VectorTopKIndexed(vecModelB, q, 5, true, nil)),
		mustIDs(p.VectorTopKBruteForce(vecModelB, q, 5, true, nil))) {
		t.Fatal("index rebuilt after invalidate disagrees with the oracle")
	}
}

// TestVecReembedIsIdempotent: re-embedding the same revision must replace its
// index entry, not duplicate it (vec0 rejects INSERT OR REPLACE, so this is a
// real hazard rather than a hypothetical one).
func TestVecReembedIsIdempotent(t *testing.T) {
	p := openVecProjection(t, filepath.Join(t.TempDir(), "index.sqlite"))
	requireVecIndex(t, p)
	seedVectors(t, p, 5, vecModelA, 12, 0)
	rng := rand.New(rand.NewSource(13))
	rev := fmt.Sprintf("%s-r%06d", vecModelA, 1)
	newVec := randUnit(rng)
	for i := 0; i < 3; i++ {
		if err := p.InsertVector(rev, vecModelA, newVec); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := p.DBForTest().QueryRow(`SELECT count(*) FROM vec_map`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 5 {
		t.Fatalf("re-embedding grew the index to %d rows for 5 revisions", rows)
	}
	// the updated vector must be the one the index answers with
	got := mustIDs(p.VectorTopKIndexed(vecModelA, newVec, 1, true, nil))
	if len(got) != 1 || got[0] != fmt.Sprintf("%s-m%06d", vecModelA, 1) {
		t.Fatalf("re-embedded vector not reflected in the index: %v", got)
	}
}

// TestVecMirrorsInsideOneTransaction: the index write commits with the vector
// write. If InsertVector's transaction fails, neither may be visible.
func TestVecMirrorsInsideOneTransaction(t *testing.T) {
	p := openVecProjection(t, filepath.Join(t.TempDir(), "index.sqlite"))
	requireVecIndex(t, p)
	seedVectors(t, p, 3, vecModelA, 14, 0)
	db := p.DBForTest()
	var mapped, stored int
	if err := db.QueryRow(`SELECT count(*) FROM vec_map`).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM vectors`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if mapped != stored {
		t.Fatalf("index (%d) and vectors (%d) diverged", mapped, stored)
	}
	// A vector of the wrong width must be REFUSED, loudly, and leave nothing
	// behind — the fixed-width index cannot hold it and a silent skip would
	// desynchronize the two.
	short := make([]float32, vecDim/2)
	short[0] = 1
	if err := p.InsertVector("wrong-width-rev", vecModelA, short); err == nil {
		t.Fatal("a wrong-width vector was accepted")
	}
	var after int
	if err := db.QueryRow(`SELECT count(*) FROM vectors`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != stored {
		t.Fatalf("the refused write left %d vectors behind (was %d)", after, stored)
	}
}
