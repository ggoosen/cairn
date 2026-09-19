// Package score turns recorded observations into a ScoreCard — and refuses to
// let anyone read it as evidence before the operator has signed the kill
// criterion it would bear on.
//
// THE SEPARATION THAT MAKES THIS HONEST. internal/result records what
// happened, and a test there asserts that no metric field ever appears in a run
// record. That is not tidiness: an observation and a judgment have different
// lifetimes and different trust. Observations are facts about a run and stay
// true forever; a score is an interpretation, and an interpretation computed
// before its falsification criterion was fixed is worth nothing, because
// nobody can now show that the criterion was not chosen to fit it. So scores
// live in a SEPARATE artifact, derived from observations, and every scorecard
// carries the signoff state of the claims it speaks to at the moment it was
// computed.
//
// THE GATE. BUILD-PLAN §5-E1: apparatus may be built ahead of sign-off; no
// measurement may be reported as evidence before its kill criterion is signed.
// Reportable() implements exactly that sentence. While any bearing claim is
// unsigned a ScoreCard may be COMPUTED and WRITTEN — the plumbing has to be
// provable — but no caller may render it as a comparison, a ranking, a
// verdict, or a claim-supporting summary. The scorecard says so in its own
// bytes (Evidence=false, NotEvidenceReason), so a file that escapes into a
// slide deck argues against itself.
//
// An unfalsifiable number is worse than no number, because it looks like
// evidence.
package score

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/eval/internal/claims"
	"github.com/ggoosen/cairn/eval/internal/metric"
	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// SchemaVersion versions the scorecard artifact independently of the run
// record: a scorecard is derived and may be recomputed from observations,
// while a run record is primary and never is.
const SchemaVersion = 1

// ClaimState is the signoff status of one claim AT THE MOMENT the card was
// computed. Copied in rather than referenced so an archived card can be judged
// without also archiving the register at that revision.
type ClaimState struct {
	ID            string `json:"id"`
	Signoff       string `json:"signoff"`
	Signed        bool   `json:"signed"`
	KillCriterion string `json:"kill_criterion,omitempty"`
}

// Arm identifies one measured condition: which memory backend, under which
// ablation, on which surface. All three are needed to name a number.
type Arm struct {
	Backend  string `json:"backend"`
	Ablation string `json:"ablation,omitempty"`
	Surface  string `json:"surface"`
	// Fidelity says HOW the arm was produced — a natively configured system,
	// or a re-ranking recomputed from stored ranking arithmetic. A recomputed
	// arm cannot retrieve what retrieval never returned, and a table that mixes
	// the two without saying so is misleading by construction.
	Fidelity string `json:"fidelity,omitempty"`
	// Limits is the arm's stated ceiling, verbatim from the ablation
	// catalogue. It travels with the numbers, not in a footnote.
	Limits string `json:"limits,omitempty"`
	// RetrievalMode records full vs lexical_only as the system actually
	// reported it, so an arm that failed to take effect is visible.
	RetrievalMode string `json:"retrieval_mode,omitempty"`
}

// String renders an arm for a table row.
func (a Arm) String() string {
	s := a.Backend
	if a.Ablation != "" {
		s += "/" + a.Ablation
	}
	return s + " (" + a.Surface + ")"
}

// Section is one arm's metrics.
type Section struct {
	Arm     Arm                `json:"arm"`
	Runs    []string           `json:"run_ids"`
	Metrics []metric.Aggregate `json:"metrics"`
	// Errors counts outcomes that could not be scored at all. A section with
	// errors is not comparable to one without, and the count is what makes
	// that visible.
	Errors int      `json:"errors"`
	Notes  []string `json:"notes,omitempty"`
}

// Get returns one aggregate by metric and cutoff.
func (s Section) Get(name metric.Name, k int) (metric.Aggregate, bool) {
	for _, m := range s.Metrics {
		if m.Metric == name && m.K == k {
			return m, true
		}
	}
	return metric.Aggregate{}, false
}

// ScoreCard is a derived, verdict-gated artifact.
type ScoreCard struct {
	SchemaVersion int    `json:"schema_version"`
	ComputedAt    string `json:"computed_at"`
	// Experiment names what was run (e4-ablations, e9-growth, e6-adversarial).
	Experiment string `json:"experiment"`
	Seed       int64  `json:"seed"`

	Corpus   CorpusRef    `json:"corpus"`
	Sections []Section    `json:"sections"`
	BearsOn  []ClaimState `json:"bears_on"`

	// Evidence is FALSE whenever any bearing claim is unsigned. It is written
	// into the file so a stray scorecard cannot be quoted without the
	// disclaimer travelling with it.
	Evidence          bool   `json:"evidence"`
	NotEvidenceReason string `json:"not_evidence_reason,omitempty"`

	Notes []string `json:"notes,omitempty"`
}

// CorpusRef pins the material a card was computed over. A number whose corpus
// cannot be identified by checksum is not reproducible, and a card computed
// over the synthetic plumbing sample is not about Cairn at all.
type CorpusRef struct {
	ID          string `json:"id"`
	Version     string `json:"version,omitempty"`
	Checksum    string `json:"checksum,omitempty"`
	LabelSource string `json:"label_source,omitempty"`
	Independent bool   `json:"independent"`
}

// New starts a card and stamps the signoff state of every claim it bears on.
func New(experiment string, seed int64, corpus CorpusRef, reg *claims.Register, bearsOn ...string) *ScoreCard {
	card := &ScoreCard{
		SchemaVersion: SchemaVersion,
		ComputedAt:    time.Now().UTC().Format(time.RFC3339),
		Experiment:    experiment,
		Seed:          seed,
		Corpus:        corpus,
	}
	sort.Strings(bearsOn)
	for _, id := range bearsOn {
		st := ClaimState{ID: id, Signoff: "NOT IN THE REGISTER"}
		if c, ok := reg.Get(id); ok {
			st.Signoff, st.Signed, st.KillCriterion = c.Signoff, c.Signed(), c.KillCriterion
		}
		card.BearsOn = append(card.BearsOn, st)
	}
	card.Evidence = card.Reportable() == nil
	if !card.Evidence {
		card.NotEvidenceReason = card.Reportable().Error()
	}
	return card
}

// Add appends a section and refreshes the evidence stamp.
func (c *ScoreCard) Add(s Section) {
	c.Sections = append(c.Sections, s)
	c.Evidence = c.Reportable() == nil
	if !c.Evidence {
		c.NotEvidenceReason = c.Reportable().Error()
	}
}

// Note appends a caveat.
func (c *ScoreCard) Note(format string, args ...any) {
	c.Notes = append(c.Notes, fmt.Sprintf(format, args...))
}

// ErrNotReportable is returned by Reportable while the gate is shut. Callers
// branch on it to print the refusal instead of the numbers.
type ErrNotReportable struct {
	Blocked []string
	Reason  string
}

func (e *ErrNotReportable) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return "kill criteria not signed off: " + strings.Join(e.Blocked, ", ")
}

// Reportable returns nil only when every bearing claim carries a dated
// operator signoff AND the corpus carries independent labels. Both halves
// matter and neither is sufficient:
//
//   - Unsigned criteria mean the falsification threshold could still be moved
//     after seeing the number. That is the failure E1 exists to prevent.
//   - A non-independent corpus means the content, the queries and the
//     judgments came from the project being evaluated. A number over the
//     synthetic sample is a statement about the harness, not about Cairn, no
//     matter how many criteria are signed.
//
// A caller that gets an error here must print the error, not the numbers.
func (c *ScoreCard) Reportable() error {
	var blocked []string
	for _, s := range c.BearsOn {
		if !s.Signed {
			blocked = append(blocked, s.ID+" (signoff: "+s.Signoff+")")
		}
	}
	if len(blocked) > 0 {
		return &ErrNotReportable{Blocked: blocked}
	}
	if !c.Corpus.Independent {
		return &ErrNotReportable{Reason: fmt.Sprintf(
			"corpus %q does not carry independent labels (%s): these numbers describe the apparatus, not Cairn",
			c.Corpus.ID, c.Corpus.LabelSource)}
	}
	return nil
}

// WriteFile persists the card atomically. Writing is always permitted —
// computing and storing observations-plus-derived-numbers is how the apparatus
// proves itself. REPORTING is what Reportable gates, and the file states its
// own status in the Evidence field so the two can never drift apart.
func (c *ScoreCard) WriteFile(path string) error {
	blob, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadFile loads a card and refuses a schema it does not understand.
func ReadFile(path string) (*ScoreCard, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c ScoreCard
	if err := json.Unmarshal(blob, &c); err != nil {
		return nil, err
	}
	if c.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("scorecard %s has schema version %d, this harness reads %d", path, c.SchemaVersion, SchemaVersion)
	}
	return &c, nil
}

// Summary is the ONE thing a caller may print about a gated card: what was
// run, over what, and why no number is shown. It deliberately contains no
// metric value, no arm ordering and no adjective — a "for what it's worth"
// number is exactly the thing that ends up screenshotted.
func (c *ScoreCard) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "experiment: %s\n", c.Experiment)
	fmt.Fprintf(&b, "corpus:     %s", c.Corpus.ID)
	if c.Corpus.Version != "" {
		fmt.Fprintf(&b, " v%s", c.Corpus.Version)
	}
	if c.Corpus.Checksum != "" {
		fmt.Fprintf(&b, " (%s)", c.Corpus.Checksum[:12])
	}
	fmt.Fprintf(&b, "\nlabels:     %s\n", c.Corpus.LabelSource)
	fmt.Fprintf(&b, "arms run:   %d\n", len(c.Sections))
	for _, s := range c.Sections {
		fmt.Fprintf(&b, "  - %-34s queries=%d errors=%d fidelity=%s\n",
			s.Arm.String(), sectionN(s), s.Errors, orNone(s.Arm.Fidelity))
		// Section notes are OBSERVATIONS about how the run went (an arm that
		// returned nothing at all, a surface that was skipped). They carry no
		// metric value and are the one thing worth reading before the gate
		// opens, so they print even in a gated summary.
		for _, n := range s.Notes {
			if strings.HasPrefix(n, "RETURNED NOTHING") {
				fmt.Fprintf(&b, "      ! %s\n", n)
			}
		}
	}
	if err := c.Reportable(); err != nil {
		b.WriteString("\nNO NUMBERS ARE SHOWN. " + err.Error() + "\n")
		b.WriteString("Metrics were computed and written to the scorecard file; they are not\n")
		b.WriteString("evidence and must not be reported as any. BUILD-PLAN §5-E1: a kill\n")
		b.WriteString("criterion chosen after seeing the result is not a kill criterion.\n")
	}
	return b.String()
}

func sectionN(s Section) int {
	for _, m := range s.Metrics {
		return m.N + m.Excluded
	}
	return 0
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Cutoffs are the rank cutoffs every experiment reports at. Fixed in one place
// so no experiment can quietly choose the k that flatters it, and chosen for
// reasons rather than roundness:
//
//	@1  what a one-shot agent actually acts on
//	@5  reconciles with spec §11's existing Success@5 gate
//	@10 the CLI's default k, i.e. what an agent is handed by default
var Cutoffs = []int{1, 5, tunables.DefaultK}

// Compute builds a Section from per-query samples for each metric.
func Compute(arm Arm, runIDs []string, samplesByK map[int][]metric.Sample, seed int64) Section {
	sec := Section{Arm: arm, Runs: runIDs}
	for _, k := range Cutoffs {
		samples := samplesByK[k]
		if len(samples) == 0 {
			continue
		}
		for _, name := range []metric.Name{metric.NDCGAt, metric.MRRAt, metric.RecallAt, metric.PrecAt, metric.SuccessAt} {
			vals := make([]metric.Sample, len(samples))
			copy(vals, samples)
			sec.Metrics = append(sec.Metrics, name.Aggregate(k, vals, seed))
		}
	}
	for _, s := range samplesByK[Cutoffs[0]] {
		if s.Excluded {
			sec.Errors++
		}
	}
	return sec
}

// Round trims a metric to six decimals so a persisted card is byte-stable
// across recomputation on the same inputs.
func Round(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	return math.Round(v*1e6) / 1e6
}
