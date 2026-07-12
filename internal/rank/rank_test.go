package rank

import (
	"math"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

func TestRRFScore(t *testing.T) {
	if got := RRFScore(1, 0); got != 1.0/61 {
		t.Fatalf("lex only: %v", got)
	}
	if got := RRFScore(0, 3); got != 1.0/63 {
		t.Fatalf("vec only: %v", got)
	}
	if got := RRFScore(1, 1); got != 2.0/61 {
		t.Fatalf("both: %v", got)
	}
	if RRFScore(0, 0) != 0 {
		t.Fatal("absent should be 0")
	}
}

func TestEffectivePDecayAndSuspension(t *testing.T) {
	// at age 0: declared/3 exactly
	if got := EffectiveP(3, 0, false); got != 1.0 {
		t.Fatalf("p3 age0: %v", got)
	}
	// one half-life (60h): halves
	if got := EffectiveP(3, config.PriorityDecayHalfLife, false); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("p3 one half-life: %v", got)
	}
	// suspension freezes decay
	if got := EffectiveP(2, 1000*time.Hour, true); got != 2.0/3.0 {
		t.Fatalf("suspended: %v", got)
	}
	if EffectiveP(0, 0, false) != 0 {
		t.Fatal("p0 must be 0")
	}
}

func TestFreshnessBonusOnly(t *testing.T) {
	if got := Freshness(0, config.DigestFreshnessHalfLife); got != 1.0 {
		t.Fatalf("age0: %v", got)
	}
	if got := Freshness(config.DigestFreshnessHalfLife, config.DigestFreshnessHalfLife); math.Abs(got-0.5) > 1e-12 {
		t.Fatalf("one half-life: %v", got)
	}
	// never negative, decays toward zero
	if got := Freshness(100*365*24*time.Hour, config.DigestFreshnessHalfLife); got < 0 || got > 1e-6 {
		t.Fatalf("ancient: %v", got)
	}
}

func TestRankPercentileAndTies(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	cands := []Candidate{
		{MessageID: "a", EventID: "e-a", CreatedAt: old, LexRank: 1, VecRank: 1}, // best RRF
		{MessageID: "b", EventID: "e-b", CreatedAt: old, LexRank: 2},
		{MessageID: "c", EventID: "e-c", CreatedAt: old, LexRank: 3},
	}
	scored := Rank(cands, ProfileSearch, now)
	if scored[0].MessageID != "a" {
		t.Fatalf("order: %v", scored[0].MessageID)
	}
	if scored[0].R != 1.0 { // 2 strictly below / (3-1)
		t.Fatalf("top percentile: %v", scored[0].R)
	}
	if scored[2].R != 0.0 {
		t.Fatalf("bottom percentile: %v", scored[2].R)
	}

	// exact ties: newer wall time wins, then event_id ascending
	newer := now.Add(-time.Minute)
	tied := []Candidate{
		{MessageID: "x", EventID: "e-2", CreatedAt: old, LexRank: 1},
		{MessageID: "y", EventID: "e-1", CreatedAt: old, LexRank: 1},
		{MessageID: "z", EventID: "e-3", CreatedAt: newer, LexRank: 1},
	}
	s2 := Rank(tied, ProfileSearch, now)
	if s2[0].MessageID != "z" {
		t.Fatalf("wall-time tiebreak: %v", s2[0].MessageID)
	}
	if s2[1].EventID != "e-1" || s2[2].EventID != "e-2" {
		t.Fatalf("event-id tiebreak: %v %v", s2[1].EventID, s2[2].EventID)
	}

	// mandatory class precedes score
	m := []Candidate{
		{MessageID: "scored", EventID: "e-s", CreatedAt: now, LexRank: 1},
		{MessageID: "pinned", EventID: "e-p", CreatedAt: old, Mandatory: "pin"},
		{MessageID: "recip", EventID: "e-r", CreatedAt: old, Mandatory: "recipient"},
	}
	s3 := Rank(m, ProfileDigest, now)
	if s3[0].MessageID != "recip" || s3[1].MessageID != "pinned" || s3[2].MessageID != "scored" {
		t.Fatalf("mandatory ordering: %v %v %v", s3[0].MessageID, s3[1].MessageID, s3[2].MessageID)
	}

	// future timestamps never boost (clamped age)
	fut := []Candidate{{MessageID: "f", EventID: "e-f", CreatedAt: now.Add(1000 * time.Hour), LexRank: 1}}
	if got := Rank(fut, ProfileSearch, now)[0].F; got != 1.0 {
		t.Fatalf("future clamped to age 0 (F=1): %v", got)
	}
}

func TestRankUniformR(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		{MessageID: "a", EventID: "1", CreatedAt: now.Add(-time.Hour)},
		{MessageID: "b", EventID: "2", CreatedAt: now},
	}
	scored := RankUniformR(cands, ProfileDigest, now)
	for _, s := range scored {
		if s.R != 1.0 {
			t.Fatalf("uniform R violated: %v", s.R)
		}
	}
	if scored[0].MessageID != "b" { // fresher wins on F
		t.Fatalf("freshness ordering under uniform R: %v", scored[0].MessageID)
	}
}

func TestDecRoundTripExact(t *testing.T) {
	for _, f := range []float64{0, 1, 0.5, 1.0 / 3.0, 0.9699999997414747, math.Pi} {
		if ParseDec(Dec(f)) != f {
			t.Fatalf("round trip loses precision: %v", f)
		}
	}
}

func TestBudgetCharsUnicode(t *testing.T) {
	if BudgetChars("héllo") != 5 {
		t.Fatal("unicode scalars, not bytes")
	}
	if BudgetChars("🦴🦴") != 2 {
		t.Fatal("astral scalars count as 1 each")
	}
}

func TestTakeWithinBudgetNeverExceeds(t *testing.T) {
	items := []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n"}
	render := func(i int) string { return items[i] }
	br := BudgetRender{Header: "HDR\n", Marker: "…\n"}
	for budget := 0; budget <= 30; budget++ {
		n, payload := TakeWithinBudget(len(items), budget, br, render)
		if got := BudgetChars(payload); got > budget {
			t.Fatalf("budget %d exceeded: %d chars (n=%d)", budget, got, n)
		}
		if n < len(items) && n > 0 &&
			BudgetChars(payload)+BudgetChars(br.Marker) <= budget && !contains(payload, "…") {
			t.Fatalf("budget %d: truncated without marker despite room", budget)
		}
	}
	// unbudgeted-fit case: everything included, no marker
	n, payload := TakeWithinBudget(len(items), 1000, br, render)
	if n != len(items) || contains(payload, "…") {
		t.Fatalf("full fit wrong: n=%d", n)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
