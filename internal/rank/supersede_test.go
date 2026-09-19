package rank

// D16 unit tests over the scorer alone — no daemon, no SQLite. These pin the
// arithmetic properties the daemon-level tests then observe end to end.

import (
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// supFixture is a FIXED anchor: every candidate shares one CreatedAt, so
// freshness and priority decay are identical across candidates and across
// calls, and the only thing that can move a score is the term under test. (An
// earlier version called time.Now() per candidate and moved F by nanoseconds
// between the two Rank calls it was comparing — a fixture bug that reads
// exactly like a scorer bug.)
var supFixture = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

// supCand makes a candidate that appears at `lex` in the lexical list only.
func supCand(id string, lex int, supBy string) Candidate {
	return Candidate{
		MessageID: id, EventID: id, CreatedAt: supFixture.Add(-time.Hour),
		LexRank: lex, SupersededBy: supBy, SupersededAt: "2026-08-19T00:00:00Z",
	}
}

// supTied makes two candidates that score IDENTICALLY before supersession:
// same age, same priority, and the same RRF (one is lexical-rank-1, the other
// vector-rank-1), so R ties too. They separate only on the deterministic
// event-id tiebreak, which puts "a" first. That is the shape D16 is actually
// about — two messages saying near-identical things about one subject, which a
// query retrieves at near-identical relevance — and it is where a capped
// demotion decides the order.
func supTied() []Candidate {
	a := Candidate{MessageID: "a", EventID: "a", CreatedAt: supFixture.Add(-time.Hour), LexRank: 1}
	b := Candidate{MessageID: "b", EventID: "b", CreatedAt: supFixture.Add(-time.Hour), VecRank: 1}
	return []Candidate{a, b}
}

// The demotion is exactly the cap, no more and no less: a superseded candidate
// scores its unsuperseded self minus SupersessionPenaltyCap. If it were a clamp
// applied after summing, or a scale applied to another term, this would not
// hold to the last bit.
func TestD16DemotionIsExactlyTheCap(t *testing.T) {
	now := supFixture
	for _, p := range []Profile{ProfileSearchP2, ProfileDigestP2} {
		plain := Rank([]Candidate{supCand("a", 1, ""), supCand("b", 2, "")}, p, now)
		sup := Rank([]Candidate{supCand("a", 1, "b"), supCand("b", 2, "")}, p, now)
		var before, after float64
		for _, s := range plain {
			if s.MessageID == "a" {
				before = s.Score
			}
		}
		for _, s := range sup {
			if s.MessageID == "a" {
				after = s.Score
			}
		}
		want := float64(before) + float64(config.SupersededPenaltyValue*-config.SupersessionPenaltyCap)
		if after != want {
			t.Fatalf("%s: superseded score %v, want %v (unsuperseded %v minus the cap)", p, after, want, before)
		}
	}
}

// The whole acceptance, at the scorer: a fact superseded by a later one stops
// outranking it. `a` wins on lexical rank and would otherwise be first.
func TestD16SupersededStopsOutranking(t *testing.T) {
	now := supFixture
	for _, p := range []Profile{ProfileSearchP2, ProfileDigestP2} {
		base := Rank(supTied(), p, now)
		if base[0].MessageID != "a" || base[0].Score != base[1].Score {
			t.Fatalf("%s precondition: expected a tie led by a, got %s/%v then %s/%v",
				p, base[0].MessageID, base[0].Score, base[1].MessageID, base[1].Score)
		}
		cands := supTied()
		cands[0].SupersededBy, cands[0].SupersededAt = "b", "2026-08-19T00:00:00Z"
		got := Rank(cands, p, now)
		if got[0].MessageID != "b" {
			t.Fatalf("%s: superseded message still outranks its successor: order %s, %s", p, got[0].MessageID, got[1].MessageID)
		}
		// demoted, NOT removed — the whole point
		if len(got) != 2 {
			t.Fatalf("%s: superseded candidate was dropped (%d results); D16 is demotion, not deletion", p, len(got))
		}
	}
}

// The boundary, pinned rather than left to be discovered later: the demotion is
// CAPPED, so it changes the order only when the two are within the cap of each
// other. A superseded message that beats its successor on relevance by more
// than SupersessionPenaltyCap still leads.
//
// That is a deliberate property of a bounded demotion, not an oversight. Under
// search-P2 the R weight is 0.75, so a full percentile gap is 0.75 — five times
// the cap — and a message that is the ONLY good match for a query is not
// displaced by a successor the query barely matches. It is the case D16 is
// about (two near-identical facts about one subject) that the cap decides. If
// the author rules that supersession should dominate relevance outright, this
// is the test that changes, and the constant with it.
func TestD16TheDemotionIsCappedAndDoesNotAlwaysInvert(t *testing.T) {
	now := supFixture
	// a is lexical rank 1 and b is absent from every list: a full percentile
	// gap, worth 0.75 under search-P2 against a 0.15 cap.
	cands := []Candidate{supCand("a", 1, "b"), supCand("b", 0, "")}
	got := Rank(cands, ProfileSearchP2, now)
	if got[0].MessageID != "a" {
		t.Fatalf("a 0.15 demotion overturned a 0.75 relevance gap — the cap is not being applied as a bounded weight: %s led", got[0].MessageID)
	}
	gap := got[0].Score - got[1].Score
	if gap <= 0 || gap >= config.SearchP2WeightR {
		t.Fatalf("unexpected gap %v; expected 0 < gap < %v (the R weight)", gap, config.SearchP2WeightR)
	}
}

// P0 profiles must be byte-identical to their pre-D16 selves. Pinned as an
// equality against the same corpus scored with and without the relation, under
// both P0 profiles and both entry points.
func TestD16P0ProfilesUnaffected(t *testing.T) {
	now := supFixture
	for _, p := range []Profile{ProfileSearch, ProfileDigest} {
		for _, uniform := range []bool{false, true} {
			rankFn := Rank
			if uniform {
				rankFn = RankUniformR
			}
			plain := rankFn([]Candidate{supCand("a", 1, ""), supCand("b", 2, "")}, p, now)
			sup := rankFn([]Candidate{supCand("a", 1, "b"), supCand("b", 2, "")}, p, now)
			if len(plain) != len(sup) {
				t.Fatalf("%s: candidate count changed", p)
			}
			for i := range plain {
				if plain[i].MessageID != sup[i].MessageID || plain[i].Score != sup[i].Score {
					t.Fatalf("%s (uniform=%v): supersession changed a P0 result: %s/%v became %s/%v",
						p, uniform, plain[i].MessageID, plain[i].Score, sup[i].MessageID, sup[i].Score)
				}
				if sup[i].Sup != 0 {
					t.Fatalf("%s: P0 scored a supersession feature (%v); the pass must be skipped, not zero-weighted",
						p, sup[i].Sup)
				}
			}
			// but the EVIDENCE is still carried, or a P0 trace would say
			// "not superseded" about a message a live successor replaced
			for _, s := range sup {
				if s.MessageID == "a" && s.SupBy != "b" {
					t.Fatalf("%s: P0 dropped the supersession evidence; the trace would state a falsehood", p)
				}
			}
		}
	}
}

// SUP is intrinsic, not positional (unlike S8's penalties), so the result is a
// function of the candidate SET and not of the slice's incoming order.
func TestD16SupersessionIsOrderIndependent(t *testing.T) {
	now := supFixture
	forward := Rank([]Candidate{supCand("a", 1, "b"), supCand("b", 2, ""), supCand("c", 3, "b")}, ProfileSearchP2, now)
	reverse := Rank([]Candidate{supCand("c", 3, "b"), supCand("b", 2, ""), supCand("a", 1, "b")}, ProfileSearchP2, now)
	for i := range forward {
		if forward[i].MessageID != reverse[i].MessageID || forward[i].Score != reverse[i].Score {
			t.Fatalf("order-dependent result at %d: %s/%v vs %s/%v",
				i, forward[i].MessageID, forward[i].Score, reverse[i].MessageID, reverse[i].Score)
		}
	}
}

// The term is summed LAST. A trailing +0 is exact in IEEE-754, so introducing
// the term must reproduce every pre-D16 score bit-for-bit on a corpus with no
// supersessions — which is what keeps the golden corpus still.
func TestD16UnsupersededScoresAreBitIdentical(t *testing.T) {
	for _, p := range []Profile{ProfileSearch, ProfileDigest, ProfileSearchP2, ProfileDigestP2} {
		w := p.weights()
		c := Components{R: 0.3, S: 0.4, F: 0.5, Peff: 0.6, I: 0.7, N: 0.8, Dup: 1, Sat: 0.333}
		withoutSup := float64(w.R*c.R) + float64(w.S*c.S) + float64(w.F*c.F) +
			float64(w.P*c.Peff) + float64(w.I*c.I) + float64(w.N*c.N) +
			float64(w.Dup*c.Dup) + float64(w.Sat*c.Sat)
		if got := w.score(c); got != withoutSup {
			t.Fatalf("%s: adding the SUP term moved an unsuperseded score: %v vs %v", p, got, withoutSup)
		}
	}
}
