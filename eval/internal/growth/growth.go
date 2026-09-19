// Package growth builds the corpora for BUILD-PLAN §3.4 E9's
// RECALL-UNDER-GROWTH curve: the same query set, with the surrounding corpus
// grown 10× → 100× → 1000× while the budget is HELD FIXED.
//
// Why this is the experiment to run first. Selectivity demand rises with N: at
// a thousand messages almost anything plausible surfaces, at a hundred thousand
// the budget buys a vanishing slice of the corpus. The claim "a ranked,
// budget-capped digest remains useful as the mesh grows" (claims.yaml
// LONG-scales-with-corpus) has never been stated as a bound, let alone tested,
// and this costs nothing to run — T0, no agent, no LLM, no money. §3.4 calls it
// the scariest curve in the plan and the most likely to find a real limit.
//
// THE FILLER IS A MODEL, AND THE MODEL IS THE ASSUMPTION. Real corpus growth
// is not synthetic text. A filler generator that produced obvious noise would
// make the curve flat and the result worthless; one that accidentally answered
// a query would corrupt the ground truth and make the curve collapse for the
// wrong reason. Neither failure is visible in the resulting number, so this
// package does two things about it:
//
//  1. It generates filler from the CORPUS'S OWN language (a seeded bigram
//     model over the real items), so distractors are lexically plausible
//     rather than trivially separable.
//
//  2. It offers TWO generators and refuses to average them, because they
//     bracket the answer rather than estimating it:
//
//     Neutral      — query terms removed from the filler vocabulary. Models a
//     mesh filling up with UNRELATED work. A LOWER BOUND on
//     interference.
//     Contending   — query terms retained, so filler competes for the same
//     lexical ground without containing the answer. Models a
//     mesh filling up with work in the SAME AREA, which is
//     what actually happens to a project's memory. An UPPER
//     BOUND on interference.
//
// The generator is recorded on every section. A curve whose generator is not
// stated is not interpretable.
package growth

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/experiment"
	"github.com/ggoosen/cairn/eval/internal/score"
)

// Generator names the filler model.
type Generator string

const (
	// Neutral excludes query vocabulary: growth in unrelated areas.
	Neutral Generator = "neutral"
	// Contending retains query vocabulary: growth in the same area.
	Contending Generator = "contending"
)

// Generators returns both, in the order they should be reported.
func Generators() []Generator { return []Generator{Neutral, Contending} }

// DefaultScales is the curve §3.4 asks for. Held here rather than at a call
// site so no run can quietly choose the scales that flatter the system.
var DefaultScales = []int{1, 10, 100, 1000}

// FillerTopic is the topic every filler item is published under. A distinct
// topic is deliberate: it makes filler removable and countable after the fact,
// and it means a topic-scoped query could exclude it — which is a thing a real
// user can do, and therefore a thing worth being able to measure separately.
const FillerTopic = "eval/growth-filler"

// FillerIDPrefix marks a synthetic item. Ground truth never references one, so
// any filler appearing in a top-k is by definition an interference hit.
const FillerIDPrefix = "growth-filler-"

// minVocabulary is the smallest bigram vocabulary that produces filler worth
// generating. Below it the model degenerates into repeating the source items
// almost verbatim, which would plant near-duplicates of the answers into the
// corpus and destroy the ground truth rather than testing it.
const minVocabulary = 40

// Grow returns material whose query set and ground truth are IDENTICAL to the
// input's, with (scale-1)×len(items) synthetic filler items added around it.
//
// scale == 1 returns the input unchanged: the curve needs its own origin, and
// an origin measured by a different code path would not be comparable to the
// points after it.
func Grow(base experiment.Material, scale int, gen Generator, seed int64) (experiment.Material, error) {
	if scale < 1 {
		return experiment.Material{}, fmt.Errorf("growth scale must be >= 1, got %d", scale)
	}
	out := base
	out.CorpusRef.ID = fmt.Sprintf("%s+growth-x%d-%s", base.CorpusRef.ID, scale, gen)
	if scale == 1 {
		return out, nil
	}

	model, err := newBigramModel(base, gen)
	if err != nil {
		return experiment.Material{}, err
	}
	want := (scale - 1) * len(base.Items)
	rng := rand.New(rand.NewSource(seed))

	// The filler is appended AFTER the real items and carries increasing
	// timestamps, so a chronological backend sees the corpus grow around
	// material that is now older. That is the real shape of the problem:
	// long-term memory is asked to find old things in a big new corpus.
	base0 := baseTime(base)
	items := make([]backend.Item, 0, len(base.Items)+want)
	items = append(items, base.Items...)
	for i := 0; i < want; i++ {
		id := fmt.Sprintf("%s%06d", FillerIDPrefix, i)
		items = append(items, backend.Item{
			ID:        id,
			Title:     model.sentence(rng, 6),
			Body:      model.paragraph(rng),
			Topics:    []string{FillerTopic},
			CreatedAt: base0.Add(time.Duration(i+1) * time.Minute),
		})
	}
	out.Items = items
	return out, nil
}

func baseTime(m experiment.Material) time.Time {
	latest := time.Time{}
	for _, it := range m.Items {
		if it.CreatedAt.After(latest) {
			latest = it.CreatedAt
		}
	}
	if latest.IsZero() {
		return time.Now().UTC().Add(-24 * time.Hour)
	}
	return latest
}

// ---------------------------------------------------------------------------
// The filler model
// ---------------------------------------------------------------------------

// bigramModel is a first-order Markov chain over the corpus's own words.
// Deliberately crude: the point is lexical plausibility, not fluency, and a
// more convincing generator would be a bigger unstated assumption, not a
// smaller one.
type bigramModel struct {
	next  map[string][]string
	words []string
	gen   Generator
}

func newBigramModel(m experiment.Material, gen Generator) (*bigramModel, error) {
	drop := map[string]bool{}
	if gen == Neutral {
		for _, q := range m.Queries {
			for _, w := range words(q.Text) {
				drop[w] = true
			}
		}
	}
	model := &bigramModel{next: map[string][]string{}, gen: gen}
	seen := map[string]bool{}
	for _, it := range m.Items {
		ws := words(it.Title + " " + it.Body)
		var prev string
		for _, w := range ws {
			if drop[w] {
				// Break the chain rather than skipping the word: splicing
				// across a removed term would manufacture bigrams the corpus
				// never contained.
				prev = ""
				continue
			}
			if !seen[w] {
				seen[w] = true
				model.words = append(model.words, w)
			}
			if prev != "" {
				model.next[prev] = append(model.next[prev], w)
			}
			prev = w
		}
	}
	sort.Strings(model.words)
	for k := range model.next {
		sort.Strings(model.next[k])
	}
	if len(model.words) < minVocabulary {
		return nil, fmt.Errorf("corpus vocabulary is only %d distinct words after building the %s filler model (need %d): filler generated from it would be near-duplicates of the answers, which corrupts ground truth rather than testing it",
			len(model.words), gen, minVocabulary)
	}
	return model, nil
}

func (m *bigramModel) word(rng *rand.Rand, prev string) string {
	if opts := m.next[prev]; len(opts) > 0 {
		return opts[rng.Intn(len(opts))]
	}
	return m.words[rng.Intn(len(m.words))]
}

func (m *bigramModel) sentence(rng *rand.Rand, n int) string {
	var b []string
	prev := ""
	for i := 0; i < n; i++ {
		w := m.word(rng, prev)
		b = append(b, w)
		prev = w
	}
	return strings.Join(b, " ")
}

func (m *bigramModel) paragraph(rng *rand.Rand) string {
	var b strings.Builder
	for i := 0; i < 4; i++ {
		s := m.sentence(rng, 8+rng.Intn(12))
		b.WriteString(strings.ToUpper(s[:1]) + s[1:] + ". ")
	}
	return strings.TrimSpace(b.String())
}

func words(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if len(f) >= 2 {
			out = append(out, f)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The curve
// ---------------------------------------------------------------------------

// Point is one measured point of the curve, kept alongside the scorecard
// section so the section can be read as "this arm, at this corpus size".
type Point struct {
	Scale     int
	Generator Generator
	Items     int
}

// ArmFor renders a curve point as a scorecard arm. The scale is part of the
// arm's identity, because a number without its corpus size is not a point on a
// curve — it is just a number.
func ArmFor(b backend.ID, surface backend.Surface, p Point, limits string) score.Arm {
	return score.Arm{
		Backend:  string(b),
		Ablation: fmt.Sprintf("growth-x%d-%s", p.Scale, p.Generator),
		Surface:  string(surface),
		Fidelity: "native",
		Limits:   limits,
	}
}

// Limits is the honest ceiling on every growth measurement, carried into each
// section so it cannot be separated from the numbers.
const Limits = "SYNTHETIC FILLER. The surrounding corpus is generated by a seeded bigram model over the " +
	"real corpus's own vocabulary, so it is lexically plausible but it is not real messages: real growth " +
	"has structure, duplication and topic drift this does not model. The two generators BRACKET the " +
	"answer and must never be averaged — 'neutral' (query vocabulary removed) is a LOWER bound on " +
	"interference, 'contending' (query vocabulary retained) an UPPER bound. Budget and k are held fixed " +
	"across every scale, which is the point; anything that varied them would measure something else."
