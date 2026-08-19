// Package explain parses `cairn why-ranked` — the exact stored ranking
// arithmetic for one result — into structured terms the harness can work with.
//
// WHY THIS EXISTS, AND WHY IT IS NOT CHEATING. BUILD-PLAN §3.4 E4 requires
// ablations: ±freshness, ±priority decay, vector-only. Cairn exposes no CLI
// switch for any of them, and eval/ is a separate module precisely so it can
// never reach inside and flip one. The only black-box route to those arms is
// the surface Cairn already publishes for auditors: why-ranked prints every
// additive term with its value, its weight and their product, and R47/R51
// guarantee that an external recompute reconciles exactly. So the harness does
// what any sceptical third party could do — reads the published arithmetic and
// recomputes the ranking with a term removed.
//
// WHAT THAT CAN AND CANNOT MEASURE, STATED ONCE AND CARRIED EVERYWHERE. A
// recomputed ablation REORDERS the result set retrieval already produced. It
// cannot make retrieval return a document the fusion stage never surfaced.
// Consequences, which every recomputed arm's Limits string repeats:
//
//   - ordering metrics (nDCG, MRR, precision at small k) move honestly;
//   - Recall@K for K equal to the requested k does not move AT ALL, and any
//     reading of it as "the ablation did not hurt recall" is false;
//   - the true no-freshness system might have retrieved a different candidate
//     set, so a recomputed arm is a LOWER BOUND on the ablation's effect.
//
// A native arm (one where the system was actually configured differently) is
// strictly better evidence, and the catalogue in internal/ablation marks which
// is which. This package exists for the arms where no native route exists.
package explain

import (
	"bufio"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Term is one additive component of a score.
type Term struct {
	Name    string  // R | S | F | P_eff | I | N | DUP | SAT
	Value   float64 // the component (DUP/SAT: the penalty FEATURE, still in [0,1])
	Weight  float64 // the profile weight (DUP/SAT: negative — the §9.1 cap)
	Product float64 // value × weight, as Cairn printed it
}

// Explanation is one message's parsed ranking arithmetic.
type Explanation struct {
	MessageID string
	Rank      int
	Profile   string

	Terms map[string]Term
	Total float64

	// LexRank / VecRank are 1-based positions in each retriever's candidate
	// list; 0 means the retriever never returned this message. VecRank == 0
	// across every result is how the harness detects a lexical-only run
	// masquerading as a hybrid one.
	LexRank int
	VecRank int
	RRF     float64

	// Mandatory is the inclusion class ("recipient" | "pin" | "subscription"),
	// empty when the item earned its place on score alone. It is NOT an
	// additive term — it sorts ahead of score entirely — which is why the
	// ±mandatory ablation cannot be done by zeroing a weight.
	Mandatory string

	Raw string
}

// Term names, as why-ranked prints them.
const (
	TermR    = "R"
	TermS    = "S"
	TermF    = "F"
	TermPeff = "P_eff"
	TermI    = "I"
	TermN    = "N"
	// S8 (spec §9.1): the duplicate and thread-saturation penalties. They are
	// ordinary additive terms whose WEIGHT is negative and equal to the §9.1
	// cap, so they need no special handling here — but they must be summed, and
	// summed LAST, or the recompute stops matching the score.
	TermDup = "DUP"
	TermSat = "SAT"
)

// AllTerms lists every additive term in the order why-ranked prints them,
// which is also the order rank.weightSet.score sums them (R51: the sum order
// is load-bearing for exact reconciliation).
var AllTerms = []string{TermR, TermS, TermF, TermPeff, TermI, TermN, TermDup, TermSat}

// Parse reads one `cairn why-ranked` output.
func Parse(out string) (*Explanation, error) {
	e := &Explanation{Terms: map[string]Term{}, Raw: out}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "total "):
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(trimmed, "total ")), 64)
			if err != nil {
				return nil, fmt.Errorf("why-ranked: unparseable total %q", trimmed)
			}
			e.Total = v
		case strings.HasPrefix(trimmed, "mandatory:"):
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "mandatory:"))
			if i := strings.Index(rest, " "); i > 0 {
				rest = rest[:i]
			}
			e.Mandatory = rest
		case strings.Contains(trimmed, " × ") && strings.Contains(trimmed, " = "):
			if err := e.parseTerm(trimmed); err != nil {
				return nil, err
			}
		case strings.Contains(trimmed, "rank ") && strings.Contains(trimmed, "(profile="):
			if err := e.parseHeader(trimmed); err != nil {
				return nil, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if e.MessageID == "" || len(e.Terms) == 0 {
		return nil, fmt.Errorf("why-ranked output did not parse as an explanation:\n%s", out)
	}
	return e, nil
}

// header: "<message-id>  rank 3 (profile=search-P0)"
func (e *Explanation) parseHeader(line string) error {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return fmt.Errorf("why-ranked: unparseable header %q", line)
	}
	e.MessageID = fields[0]
	n, err := strconv.Atoi(fields[2])
	if err != nil {
		return fmt.Errorf("why-ranked: unparseable rank in %q", line)
	}
	e.Rank = n
	p := fields[3]
	p = strings.TrimPrefix(p, "(profile=")
	e.Profile = strings.TrimSuffix(p, ")")
	return nil
}

// term: "R     0.123 × 0.9 = 0.1107   (lex rank 1, vec rank 0, RRF 0.0163)"
func (e *Explanation) parseTerm(line string) error {
	// split off the trailing parenthetical annotation first
	annot := ""
	if i := strings.Index(line, "("); i >= 0 {
		annot = line[i:]
		line = strings.TrimSpace(line[:i])
	}
	fields := strings.Fields(line)
	// name value × weight = product
	if len(fields) != 6 || fields[2] != "×" || fields[4] != "=" {
		return fmt.Errorf("why-ranked: unparseable term %q", line)
	}
	v, err1 := strconv.ParseFloat(fields[1], 64)
	w, err2 := strconv.ParseFloat(fields[3], 64)
	p, err3 := strconv.ParseFloat(fields[5], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return fmt.Errorf("why-ranked: unparseable numbers in %q", line)
	}
	e.Terms[fields[0]] = Term{Name: fields[0], Value: v, Weight: w, Product: p}

	if fields[0] == TermR && annot != "" {
		e.parseRAnnotation(annot)
	}
	return nil
}

// "(lex rank 1, vec rank 0, RRF 0.016393)"
func (e *Explanation) parseRAnnotation(annot string) {
	annot = strings.Trim(annot, "()")
	for _, part := range strings.Split(annot, ",") {
		f := strings.Fields(part)
		switch {
		case len(f) == 3 && f[0] == "lex" && f[1] == "rank":
			e.LexRank, _ = strconv.Atoi(f[2])
		case len(f) == 3 && f[0] == "vec" && f[1] == "rank":
			e.VecRank, _ = strconv.Atoi(f[2])
		case len(f) == 2 && f[0] == "RRF":
			e.RRF, _ = strconv.ParseFloat(f[1], 64)
		}
	}
}

// ReconcileTolerance bounds the difference between the sum of the printed
// products and the printed total. R51 makes the recompute EXACT in IEEE-754,
// so this is only absorbing decimal-print round-tripping, not arithmetic
// slack. A failure here is not a rounding nuisance: it means the published
// explanation does not describe the published score, and every recomputed
// ablation built on it would be measuring fiction.
const ReconcileTolerance = 1e-9

// Reconcile checks that the printed products sum to the printed total. The
// harness calls this on every parsed explanation before using one, because a
// silent parse failure (a term dropped by a format change) would look exactly
// like a term whose weight is zero — i.e. exactly like a successful ablation.
func (e *Explanation) Reconcile() error {
	var sum float64
	for _, name := range AllTerms {
		sum += e.Terms[name].Product
	}
	if math.Abs(sum-e.Total) > ReconcileTolerance {
		return fmt.Errorf("why-ranked for %s does not reconcile: terms sum to %v, total printed as %v (parse drift or an unpublished term)",
			e.MessageID, sum, e.Total)
	}
	return nil
}

// ScoreWithout recomputes the additive score with the named terms removed.
// This is the ablation arithmetic, and it is deliberately the same shape as
// rank.weightSet.score: value × weight summed in AllTerms order.
func (e *Explanation) ScoreWithout(drop ...string) float64 {
	dropped := map[string]bool{}
	for _, d := range drop {
		dropped[d] = true
	}
	var sum float64
	for _, name := range AllTerms {
		if dropped[name] {
			continue
		}
		t := e.Terms[name]
		sum += float64(t.Value * t.Weight)
	}
	return sum
}

// ScoreOnly recomputes the score from ONLY the named terms.
func (e *Explanation) ScoreOnly(keep ...string) float64 {
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	var sum float64
	for _, name := range AllTerms {
		if !kept[name] {
			continue
		}
		t := e.Terms[name]
		sum += float64(t.Value * t.Weight)
	}
	return sum
}
