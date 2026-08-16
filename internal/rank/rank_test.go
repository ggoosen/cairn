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

// D4: the hard-budget property holds in BOTH modes. Parameterised over the
// spec constructor rather than over raw ints, so the mode under test is the
// one a caller can actually ask for.
func TestTakeWithinBudgetNeverExceeds(t *testing.T) {
	items := []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n"}
	render := func(i int) string { return items[i] }
	br := BudgetRender{Header: "HDR\n", Marker: "…\n"}

	for _, mode := range []string{BudgetModeChars, BudgetModeTokens} {
		for budget := 1; budget <= 30; budget++ {
			var spec Spec
			var err error
			if mode == BudgetModeChars {
				spec, err = NewSpec(budget, 0)
			} else {
				spec, err = NewSpec(0, budget)
			}
			if err != nil {
				t.Fatal(err)
			}
			limits := spec.Limits()
			n, payload := TakeWithinBudget(len(items), limits, br, render)
			if cerr := limits.Complies(payload); cerr != nil {
				t.Fatalf("%s budget %d: %v (n=%d)", mode, budget, cerr, n)
			}
			// no item is ever cut in half: the payload is the header plus a
			// PREFIX of the rendered items, possibly plus the marker
			want := br.Header
			for i := 0; i < n; i++ {
				want += items[i]
			}
			if payload != want && payload != want+br.Marker && !(n == 0 && payload == "") {
				t.Fatalf("%s budget %d: payload is not header+prefix[+marker]: %q", mode, budget, payload)
			}
			if n < len(items) && n > 0 && !contains(payload, "…") {
				if Limits(limits).Complies(payload+br.Marker) == nil {
					t.Fatalf("%s budget %d: truncated without marker despite room", mode, budget)
				}
			}
			// the report is self-consistent with the payload it describes
			rep := spec.Report(payload)
			if rep.Mode != mode || rep.Limit != budget || rep.Tokenizer == "" || rep.Used > rep.Limit {
				t.Fatalf("%s budget %d: bad report %+v", mode, budget, rep)
			}
		}
	}

	// full-fit case: everything included, no marker
	spec, _ := NewSpec(1000, 0)
	n, payload := TakeWithinBudget(len(items), spec.Limits(), br, render)
	if n != len(items) || contains(payload, "…") {
		t.Fatalf("full fit wrong: n=%d", n)
	}
	// unbudgeted: no limits at all means everything, still no marker
	n, payload = TakeWithinBudget(len(items), Spec{}.Limits(), br, render)
	if n != len(items) || contains(payload, "…") {
		t.Fatalf("unbudgeted wrong: n=%d payload=%q", n, payload)
	}
}

// D4: exactly one budget. Both is a REFUSAL, not a precedence rule.
func TestSpecRefusesBothBudgets(t *testing.T) {
	if _, err := NewSpec(100, 50); err == nil {
		t.Fatal("budget_chars + budget_tokens together must be refused")
	}
	if _, err := NewSpec(-1, 0); err == nil {
		t.Fatal("negative budget must be refused")
	}
	for _, c := range []struct {
		chars, tokens int
		mode          string
	}{{100, 0, BudgetModeChars}, {0, 100, BudgetModeTokens}, {0, 0, BudgetModeUnbudgeted}} {
		spec, err := NewSpec(c.chars, c.tokens)
		if err != nil {
			t.Fatal(err)
		}
		if spec.Mode() != c.mode {
			t.Fatalf("NewSpec(%d,%d) mode = %s, want %s", c.chars, c.tokens, spec.Mode(), c.mode)
		}
	}
}

// D4: the capability ceiling is a SECOND limit, never a conversion. A token
// budget under a char cap must satisfy both.
func TestCeilingIsASecondLimit(t *testing.T) {
	items := make([]string, 20)
	for i := range items {
		items[i] = "some ordinary words of prose here\n"
	}
	render := func(i int) string { return items[i] }
	br := BudgetRender{Header: "H\n", Marker: "…\n"}

	spec, _ := NewSpec(0, 1000) // a token budget far larger than the ceiling
	spec.Ceiling = 120
	limits := spec.Limits()
	if len(limits) != 2 {
		t.Fatalf("expected the token budget AND the char ceiling, got %d limit(s)", len(limits))
	}
	_, payload := TakeWithinBudget(len(items), limits, br, render)
	if BudgetChars(payload) > 120 {
		t.Fatalf("char ceiling ignored: %d chars", BudgetChars(payload))
	}
	if err := limits.Complies(payload); err != nil {
		t.Fatal(err)
	}
	rep := spec.Report(payload)
	if rep.Mode != BudgetModeTokens || rep.CeilingChars != 120 {
		t.Fatalf("the ceiling must be reported, got %+v", rep)
	}
}

// D4: the approximate counter is named honestly and behaves as documented.
func TestApproxTokenCounter(t *testing.T) {
	c := ApproxTokenCounter()
	if c.Name() != config.TokenizerApprox {
		t.Fatalf("tokenizer name %q must be the approximate one", c.Name())
	}
	if !contains(c.Name(), "approx") {
		t.Fatalf("an approximate counter must SAY so in its name, got %q", c.Name())
	}
	if CharCounter().Name() != config.TokenizerChars {
		t.Fatal("char counter name")
	}
	if c.Count("") != 0 {
		t.Fatal("empty string costs nothing")
	}
	// monotone in its input: appending text never lowers the count
	prev := 0
	acc := ""
	for _, w := range []string{"the", " quick", " brown", " fox 12345", " — ünïcödé", "\n\nend."} {
		acc += w
		n := c.Count(acc)
		if n < prev {
			t.Fatalf("count fell from %d to %d at %q", prev, n, acc)
		}
		prev = n
	}
	// deterministic
	if c.Count(acc) != prev {
		t.Fatal("counter is not deterministic")
	}
	// over-estimates plain English prose relative to the ~4 chars/token
	// rule of thumb the previous advice told callers to apply by hand
	prose := "The quick brown fox jumps over the lazy dog and then considers its position carefully."
	if got, rough := c.Count(prose), BudgetChars(prose)/4; got < rough {
		t.Fatalf("approx counter under-estimates prose: %d < %d — it is meant to be generous", got, rough)
	}
	// a mode name round-trips to its counter
	if got, err := CounterFor(BudgetModeTokens); err != nil || got.Name() != config.TokenizerApprox {
		t.Fatalf("CounterFor(tokens) = %v, %v", got, err)
	}
	if _, err := CounterFor("furlongs"); err == nil {
		t.Fatal("unknown mode must not resolve to a counter")
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
