package metric

import (
	"math"
	"testing"
)

func approx(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %.12f, want %.12f", what, got, want)
	}
}

// Hand-worked vectors. A metric implementation nobody checked by hand is a
// number generator.
func TestHandWorkedVectors(t *testing.T) {
	rel := NewRelevanceSet([]string{"a", "b"})

	// perfect ranking: both relevant items first
	perfect := Ranking{"a", "b", "x", "y", "z"}
	// DCG = 1/log2(2) + 1/log2(3) = 1 + 0.6309297535714574
	approx(t, DCG(perfect, rel, 5), 1+1/math.Log2(3), "DCG(perfect)")
	approx(t, NDCG(perfect, rel, 5), 1, "nDCG(perfect)")
	approx(t, ReciprocalRank(perfect, rel, 5), 1, "MRR(perfect)")
	approx(t, Recall(perfect, rel, 5), 1, "Recall(perfect)")
	approx(t, Precision(perfect, rel, 5), 2.0/5, "P@5(perfect)")
	approx(t, Success(perfect, rel, 5), 1, "Success(perfect)")

	// the answer is at rank 3, the second relevant item at rank 5
	mid := Ranking{"x", "y", "a", "z", "b"}
	// DCG = 1/log2(4) + 1/log2(6) = 0.5 + 0.38685280723454163
	approx(t, DCG(mid, rel, 5), 0.5+1/math.Log2(6), "DCG(mid)")
	approx(t, NDCG(mid, rel, 5), (0.5+1/math.Log2(6))/(1+1/math.Log2(3)), "nDCG(mid)")
	approx(t, ReciprocalRank(mid, rel, 5), 1.0/3, "MRR(mid)")
	approx(t, Recall(mid, rel, 5), 1, "Recall(mid)")

	// nothing relevant
	miss := Ranking{"x", "y", "z"}
	approx(t, NDCG(miss, rel, 5), 0, "nDCG(miss)")
	approx(t, ReciprocalRank(miss, rel, 5), 0, "MRR(miss)")
	approx(t, Recall(miss, rel, 5), 0, "Recall(miss)")
	approx(t, Success(miss, rel, 5), 0, "Success(miss)")
}

// The cutoff must actually cut. A relevant document below k is a miss, not a
// small success — the agent never sees it.
func TestCutoffCuts(t *testing.T) {
	rel := NewRelevanceSet([]string{"a"})
	r := Ranking{"x", "y", "z", "a"}
	approx(t, Success(r, rel, 3), 0, "Success@3")
	approx(t, Success(r, rel, 4), 1, "Success@4")
	approx(t, ReciprocalRank(r, rel, 3), 0, "MRR@3")
	approx(t, Recall(r, rel, 3), 0, "Recall@3")
}

// Precision must be denominated by the rank slots asked for, not by what the
// backend happened to return; otherwise returning less is rewarded.
func TestPrecisionIsDenominatedByK(t *testing.T) {
	rel := NewRelevanceSet([]string{"a"})
	terse := Ranking{"a"}
	verbose := Ranking{"a", "x", "y", "z", "w"}
	if Precision(terse, rel, 5) != Precision(verbose, rel, 5) {
		t.Fatalf("returning fewer results changed P@5 (%v vs %v): a system would be rewarded for withholding",
			Precision(terse, rel, 5), Precision(verbose, rel, 5))
	}
}

// Unmappable hits (the backend returned something the harness cannot resolve
// to a corpus item) must occupy a rank slot as a non-relevant result. Dropping
// them would silently promote everything below.
func TestUnmappableHitsOccupyTheirSlot(t *testing.T) {
	rel := NewRelevanceSet([]string{"a"})
	withGap := Ranking{"", "", "a"}
	approx(t, ReciprocalRank(withGap, rel, 5), 1.0/3, "MRR with unmapped hits")
}

// An error is not a zero. This is the property that decides whether a
// comparison is honest, so it is asserted rather than commented.
func TestExcludedSamplesAreNotZeros(t *testing.T) {
	samples := []Sample{
		{QueryID: "q1", Value: 1},
		{QueryID: "q2", Value: 1},
		{QueryID: "q3", Excluded: true, Reason: "backend does not implement this surface"},
	}
	agg := NDCGAt.Aggregate(5, samples, 1)
	if agg.Mean != 1 {
		t.Fatalf("mean %v — an excluded query was averaged in as a zero", agg.Mean)
	}
	if agg.N != 2 || agg.Excluded != 1 {
		t.Fatalf("counts wrong: N=%d excluded=%d", agg.N, agg.Excluded)
	}
	if len(agg.ExcludedReasons) != 1 {
		t.Fatalf("the exclusion reason was dropped: %v", agg.ExcludedReasons)
	}
}

// NaN values (an undefined metric for a degenerate query) are exclusions, not
// contributions — a NaN in the sum would poison the whole mean silently.
func TestNaNIsAnExclusion(t *testing.T) {
	agg := RecallAt.Aggregate(5, []Sample{{QueryID: "q1", Value: math.NaN()}, {QueryID: "q2", Value: 0.5}}, 1)
	if agg.N != 1 || agg.Excluded != 1 || math.IsNaN(agg.Mean) {
		t.Fatalf("NaN leaked into the aggregate: %+v", agg)
	}
}

// Below MinBootstrapN no interval is drawn. Six queries resampled two thousand
// times still only knows about six queries.
func TestSmallSamplesGetNoInterval(t *testing.T) {
	var small []Sample
	for i := 0; i < MinBootstrapN-1; i++ {
		small = append(small, Sample{QueryID: "q", Value: 0.5})
	}
	if agg := NDCGAt.Aggregate(5, small, 1); agg.Bootstrapped {
		t.Fatalf("an interval was drawn from %d queries", agg.N)
	}
	var big []Sample
	for i := 0; i < MinBootstrapN; i++ {
		big = append(big, Sample{QueryID: "q", Value: float64(i % 2)})
	}
	agg := NDCGAt.Aggregate(5, big, 1)
	if !agg.Bootstrapped || agg.CI95Low > agg.Mean || agg.CI95High < agg.Mean {
		t.Fatalf("interval does not bracket the mean: %+v", agg)
	}
}

// The interval must be a property of the data plus the seed, not of the clock.
func TestBootstrapIsReproducible(t *testing.T) {
	var s []Sample
	for i := 0; i < 40; i++ {
		s = append(s, Sample{QueryID: "q", Value: float64(i) / 40})
	}
	a := NDCGAt.Aggregate(10, s, 12345)
	b := NDCGAt.Aggregate(10, s, 12345)
	if a.CI95Low != b.CI95Low || a.CI95High != b.CI95High {
		t.Fatalf("bootstrap not reproducible: %v vs %v", a, b)
	}
	c := NDCGAt.Aggregate(10, s, 999)
	if a.CI95Low == c.CI95Low && a.CI95High == c.CI95High {
		t.Fatal("changing the seed changed nothing; the interval is not actually resampling")
	}
}
