package rank

import (
	"math"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// baseCand is a candidate with no retrieval signal of its own, so any score
// difference between two of them comes from the penalties alone.
func baseCand(id, eventID, dup, thread string, at time.Time) Candidate {
	return Candidate{MessageID: id, EventID: eventID, CreatedAt: at, LexRank: 1,
		DupKey: dup, ThreadKey: thread}
}

// The duplicate penalty is positional: the FIRST occurrence of a body is free,
// and every copy after it pays the full cap. Anything else would penalise a
// unique document for existing.
func TestDuplicatePenaltyChargesEveryCopyAfterTheFirst(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		baseCand("a", "e1", "hash-same", "t-a", now),
		baseCand("b", "e2", "hash-same", "t-b", now),
		baseCand("c", "e3", "hash-other", "t-c", now),
	}
	got := Rank(cands, ProfileSearchP2, now)
	byID := map[string]Scored{}
	for _, s := range got {
		byID[s.MessageID] = s
	}
	// exactly one of the two identical bodies is charged, and the unique one is not
	charged := 0
	for _, id := range []string{"a", "b"} {
		if byID[id].Components.Dup != 0 {
			charged++
			if byID[id].Components.Dup != config.DuplicatePenaltyValue {
				t.Fatalf("%s duplicate feature %v, want the full %v", id, byID[id].Components.Dup, config.DuplicatePenaltyValue)
			}
		}
	}
	if charged != 1 {
		t.Fatalf("%d of the two identical bodies were charged, want exactly 1", charged)
	}
	if byID["c"].Components.Dup != 0 {
		t.Fatalf("a body nothing else shares was charged a duplicate penalty: %v", byID["c"].Components.Dup)
	}
	// the charge is exactly the cap, subtracted
	first, second := byID["a"], byID["b"]
	if first.Components.Dup != 0 {
		first, second = second, first
	}
	if diff := first.Score - second.Score; math.Abs(diff-config.PenaltyCap) > 1e-12 {
		t.Fatalf("duplicate cost %v, want the cap %v", diff, config.PenaltyCap)
	}
	// and the penalised copy sorts BELOW the one it duplicates
	if got[len(got)-1].MessageID != second.MessageID {
		t.Fatalf("the duplicate did not sort last: %v", ids(got))
	}
}

// Saturation grades with how much of the thread has already been shown, and
// stops at the cap — the fifth message from a thread cannot cost more than the
// fourth.
func TestThreadSaturationGradesThenStopsAtTheCap(t *testing.T) {
	now := time.Now()
	var cands []Candidate
	for i, id := range []string{"m1", "m2", "m3", "m4", "m5"} {
		// distinct bodies (no duplicate penalty), one shared thread, and
		// decreasing wall time so the base order is m1..m5
		cands = append(cands, baseCand(id, "e"+id, "hash-"+id, "t-shared", now.Add(-time.Duration(i)*time.Minute)))
	}
	got := Rank(cands, ProfileSearchP2, now)
	seen := map[string]Scored{}
	for _, s := range got {
		seen[s.MessageID] = s
	}
	want := map[string]float64{"m1": 0, "m2": 1.0 / 3, "m3": 2.0 / 3, "m4": 1, "m5": 1}
	for id, w := range want {
		if math.Abs(seen[id].Components.Sat-w) > 1e-12 {
			t.Fatalf("%s saturation feature %v, want %v", id, seen[id].Components.Sat, w)
		}
		if p := -seen[id].Components.Sat * config.PenaltyCap; p < -config.PenaltyCap-1e-12 {
			t.Fatalf("%s penalty %v exceeds the cap %v", id, p, config.PenaltyCap)
		}
	}
	// m4 and m5 are both at the cap: saturation is capped, not cumulative
	if seen["m4"].Components.Sat != seen["m5"].Components.Sat {
		t.Fatalf("saturation kept growing past the cap: m4=%v m5=%v", seen["m4"].Components.Sat, seen["m5"].Components.Sat)
	}
}

// The two penalties are independent and each capped SEPARATELY (§9.1: "each
// ... capped at 0.15"), so a result that is both a duplicate and deep in a
// saturated thread can lose 2×cap in total — but neither term alone exceeds it.
func TestPenaltiesAreCappedIndividually(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		baseCand("a", "e1", "same", "t", now),
		baseCand("b", "e2", "same", "t", now.Add(-time.Minute)),
		baseCand("c", "e3", "same", "t", now.Add(-2*time.Minute)),
		baseCand("d", "e4", "same", "t", now.Add(-3*time.Minute)),
		baseCand("e", "e5", "same", "t", now.Add(-4*time.Minute)),
	}
	got := Rank(cands, ProfileSearchP2, now)
	w := ProfileSearchP2.Weights()
	for _, s := range got {
		dup, sat := s.Components.Dup*w.Dup, s.Components.Sat*w.Sat
		if dup < -config.PenaltyCap-1e-12 || sat < -config.PenaltyCap-1e-12 {
			t.Fatalf("%s: penalties %v/%v breach the individual cap %v", s.MessageID, dup, sat, config.PenaltyCap)
		}
		if s.Components.Dup > 1 || s.Components.Sat > 1 {
			t.Fatalf("%s: penalty feature outside [0,1]: %v/%v", s.MessageID, s.Components.Dup, s.Components.Sat)
		}
	}
}

// §9.1 gives the P0 profiles no penalty term, and P0 is the profile every
// shipped default uses. Penalties must therefore be invisible there — not
// "weighted to zero" in a way a later refactor could unzero, but absent from
// the components entirely, and leaving the score bit-identical.
func TestP0ProfilesApplyNoPenalties(t *testing.T) {
	now := time.Now()
	dup := []Candidate{
		baseCand("a", "e1", "same", "t", now),
		baseCand("b", "e2", "same", "t", now.Add(-time.Minute)),
		baseCand("c", "e3", "same", "t", now.Add(-2*time.Minute)),
	}
	clean := []Candidate{
		baseCand("a", "e1", "one", "ta", now),
		baseCand("b", "e2", "two", "tb", now.Add(-time.Minute)),
		baseCand("c", "e3", "three", "tc", now.Add(-2*time.Minute)),
	}
	for _, p := range []Profile{ProfileSearch, ProfileDigest} {
		w := p.Weights()
		if w.Dup != 0 || w.Sat != 0 {
			t.Fatalf("%s carries penalty weights %v/%v — §9.1 gives P0 no penalty term", p, w.Dup, w.Sat)
		}
		withDup := Rank(dup, p, now)
		withoutDup := Rank(clean, p, now)
		for i := range withDup {
			if withDup[i].Components.Dup != 0 || withDup[i].Components.Sat != 0 {
				t.Fatalf("%s scored a penalty component: %+v", p, withDup[i].Components)
			}
			// same inputs but for the keys ⇒ bit-identical scores
			if withDup[i].Score != withoutDup[i].Score {
				t.Fatalf("%s: duplicate/thread keys changed a P0 score: %v vs %v",
					p, withDup[i].Score, withoutDup[i].Score)
			}
		}
	}
}

// Messages with no key are exempt: an empty thread key must not make every
// keyless candidate saturate against every other one.
func TestEmptyKeysAreExempt(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		baseCand("a", "e1", "", "", now),
		baseCand("b", "e2", "", "", now.Add(-time.Minute)),
		baseCand("c", "e3", "", "", now.Add(-2*time.Minute)),
	}
	for _, s := range Rank(cands, ProfileSearchP2, now) {
		if s.Components.Dup != 0 || s.Components.Sat != 0 {
			t.Fatalf("%s was penalised on an empty key: %+v", s.MessageID, s.Components)
		}
	}
}

// The penalty pass runs against the BASE ordering, so it is a function of the
// candidate set and not of the slice's incoming order. Shuffling the input must
// produce the same components and the same final ranking.
func TestPenaltiesAreOrderIndependent(t *testing.T) {
	now := time.Now()
	mk := func() []Candidate {
		return []Candidate{
			baseCand("m1", "e1", "h1", "t", now),
			baseCand("m2", "e2", "h1", "t", now.Add(-time.Minute)),
			baseCand("m3", "e3", "h2", "t", now.Add(-2*time.Minute)),
			baseCand("m4", "e4", "h3", "u", now.Add(-3*time.Minute)),
		}
	}
	forward := Rank(mk(), ProfileSearchP2, now)
	rev := mk()
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	reversed := Rank(rev, ProfileSearchP2, now)
	for i := range forward {
		if forward[i].MessageID != reversed[i].MessageID || forward[i].Score != reversed[i].Score {
			t.Fatalf("input order changed the outcome at %d: %v vs %v", i, ids(forward), ids(reversed))
		}
	}
}

// R51: the score the ranker returns must be the plain IEEE-754 sum of the eight
// printed products, in the printed order — with penalties in play.
func TestPenalisedScoreIsThePlainSumOfItsTerms(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		{MessageID: "a", EventID: "e1", CreatedAt: now, LexRank: 1, VecRank: 2,
			Salience: 0.4, Intent: 0.6, Novelty: 0.25, DupKey: "h", ThreadKey: "t"},
		{MessageID: "b", EventID: "e2", CreatedAt: now.Add(-time.Hour), LexRank: 2, VecRank: 1,
			Salience: 0.2, Intent: 0.3, Novelty: 0.5, DupKey: "h", ThreadKey: "t"},
		{MessageID: "c", EventID: "e3", CreatedAt: now.Add(-2 * time.Hour), LexRank: 3,
			Salience: 0.1, Intent: 0.1, Novelty: 0.9, DupKey: "h2", ThreadKey: "t"},
	}
	for _, p := range []Profile{ProfileSearchP2, ProfileDigestP2} {
		w := p.Weights()
		penalised := false
		for _, s := range Rank(cands, p, now) {
			c := s.Components
			sum := float64(w.R*c.R) + float64(w.S*c.S) + float64(w.F*c.F) +
				float64(w.P*c.Peff) + float64(w.I*c.I) + float64(w.N*c.N) +
				float64(w.Dup*c.Dup) + float64(w.Sat*c.Sat)
			if sum != s.Score {
				t.Fatalf("%s %s: external recompute %v != returned %v", p, s.MessageID, sum, s.Score)
			}
			if c.Dup != 0 || c.Sat != 0 {
				penalised = true
			}
		}
		if !penalised {
			t.Fatalf("%s: no penalty fired, so the reconciliation proved nothing", p)
		}
	}
}

func ids(s []Scored) []string {
	out := make([]string, len(s))
	for i := range s {
		out[i] = s[i].MessageID
	}
	return out
}
