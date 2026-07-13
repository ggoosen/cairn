package rank

import (
	"math"
	"testing"
	"time"
)

// P2-3: the full additive profile (§9.1). With equal relevance/freshness, the
// salience + intent + novelty terms decide order — a P0 profile (which ignores
// them) would tie/flip.
func TestP2ProfileUsesSalienceIntentNovelty(t *testing.T) {
	now := time.Now()
	// two candidates, IDENTICAL retrieval position + age → same R, F under any
	// profile. Only S/I/N differ.
	cands := []Candidate{
		{MessageID: "hot", EventID: "a", CreatedAt: now, LexRank: 1, Salience: 0.9, Intent: 1.0, Novelty: 0.8},
		{MessageID: "cold", EventID: "b", CreatedAt: now, LexRank: 1, Salience: 0.0, Intent: 0.0, Novelty: 0.0},
	}
	// P0 search ignores S/I/N → tie broken only by event_id (a<b → hot first by luck).
	// P2 search MUST rank hot strictly above cold on score.
	p2 := Rank(cands, ProfileSearchP2, now)
	if p2[0].MessageID != "hot" {
		t.Fatalf("P2 search did not rank the high-salience item first: %v", p2)
	}
	if !(p2[0].Score > p2[1].Score) {
		t.Fatalf("P2 scores should differ by S/I/N: %v vs %v", p2[0].Score, p2[1].Score)
	}
	// verify the exact additive score for hot: 0.75·R + 0.08·S + 0.04·F + 0.10·I + 0.03·N
	// both have LexRank 1 → RRF equal → percentile R: hot & cold tie in RRF, so
	// each has (#strictly below)/(n-1)=0/1=0 → R=0. F≈1 (age 0).
	want := 0.75*p2[0].R + 0.08*0.9 + 0.04*p2[0].F + 0.10*1.0 + 0.03*0.8
	if math.Abs(p2[0].Score-want) > 1e-9 {
		t.Fatalf("P2 additive score = %v, want %v", p2[0].Score, want)
	}
}

func TestP2ProfileFlagAndNovelty(t *testing.T) {
	if !ProfileSearchP2.IsP2() || !ProfileDigestP2.IsP2() {
		t.Fatal("P2 profiles must report IsP2")
	}
	if ProfileSearch.IsP2() || ProfileDigest.IsP2() {
		t.Fatal("P0 profiles must not report IsP2")
	}
	if NoveltyFromExposure(0) != 1.0 {
		t.Fatal("unseen item should be maximally novel")
	}
	if NoveltyFromExposure(100) >= NoveltyFromExposure(1) {
		t.Fatal("heavy exposure should reduce novelty")
	}
}

// P0 profiles must be unchanged by the P2 refactor (S/I/N default 0).
func TestP0ProfilesUnchangedByP2Refactor(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		{MessageID: "x", EventID: "a", CreatedAt: now, LexRank: 1, Priority: 3},
	}
	got := Rank(cands, ProfileSearch, now)[0]
	// single candidate: R=1, F≈1, Peff=priority/3=1 → 0.90+0.07·F+0.03·1
	want := config_SearchScore(got.F)
	if math.Abs(got.Score-want) > 1e-9 {
		t.Fatalf("P0 search score drifted: %v want %v", got.Score, want)
	}
}

func config_SearchScore(f float64) float64 { return 0.90*1.0 + 0.07*f + 0.03*1.0 }
