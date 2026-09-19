package experiment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/eval/internal/ablation"
	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/cairnctl"
	"github.com/ggoosen/cairn/eval/internal/explain"
	"github.com/ggoosen/cairn/eval/internal/metric"
	"github.com/ggoosen/cairn/eval/internal/result"
	"github.com/ggoosen/cairn/eval/internal/score"
	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// Condition is one cell of the experiment matrix: a memory backend under an
// ablation arm, on one retrieval surface.
type Condition struct {
	Backend backend.ID
	Arm     ablation.Arm
	Surface backend.Surface
}

func (c Condition) String() string {
	return fmt.Sprintf("%s/%s (%s)", c.Backend, c.Arm.ID, c.Surface)
}

// Options configure a run.
type Options struct {
	OutDir      string
	Seed        int64
	K           int
	BudgetChars int
	Binary      cairnctl.Binary
	// Label is written into every run record so a batch can be found again.
	Label string
}

func (o Options) k() int {
	if o.K > 0 {
		return o.K
	}
	return tunables.DefaultK
}

func (o Options) budget() int {
	if o.BudgetChars > 0 {
		return o.BudgetChars
	}
	return tunables.DefaultBudgetChars
}

// Outcome is one condition's result: the run record path, the ranking each
// query produced (after any recomputed ablation), and the honest reasons any
// query could not be scored.
type Outcome struct {
	Condition Condition
	RunID     string
	RunPath   string
	// Rankings is per query id, in rank order, of corpus item ids.
	Rankings map[string]metric.Ranking
	// Excluded is per query id, the reason it cannot be scored at all.
	Excluded map[string]string
	// RetrievalMode is what the system reported (full | lexical_only).
	RetrievalMode string
	// Profile is the ranking profile observed in the published arithmetic,
	// empty when no explanation was read.
	Profile string
	// EmptyForEveryQuery marks a condition that returned nothing at all. See
	// Run: a uniform zero is a real score and an alarm at the same time.
	EmptyForEveryQuery bool
	Notes              []string

	// askedIDs is every query the condition was asked, in order, so that a
	// query which produced no ranking entry can be told from one that was
	// never asked.
	askedIDs []string
}

// emptyQueries counts how many scoreable queries came back with no results.
func (o *Outcome) emptyQueries() (empty, total int) {
	for id, r := range o.Rankings {
		if _, excluded := o.Excluded[id]; excluded {
			continue
		}
		total++
		if len(r) == 0 {
			empty++
		}
	}
	// A query that produced no ranking entry at all never appears in Rankings,
	// so count those too — otherwise "returned nothing" would look like
	// "was never asked".
	for _, id := range o.askedIDs {
		if _, seen := o.Rankings[id]; seen {
			continue
		}
		if _, excluded := o.Excluded[id]; excluded {
			continue
		}
		total++
		empty++
	}
	return empty, total
}

// ErrConditionUnrunnable is returned when a condition cannot be run honestly:
// an unavailable arm, an arm the backend cannot realize, a surface the backend
// does not have, or an arm that failed to take effect. Every one of these is
// reported rather than substituted, because the substitute would be a number.
var ErrConditionUnrunnable = errors.New("condition cannot be run honestly")

// Applicable reports whether a condition is even meaningful, without running
// it. Used by the CLI to explain a matrix before executing it.
func Applicable(c Condition) error {
	if err := c.Arm.Err(); err != nil {
		// Both sentinels stay matchable: the caller may branch on "cannot run"
		// generally or on "no black-box route" specifically.
		return fmt.Errorf("%w: %w", ErrConditionUnrunnable, err)
	}
	if !c.Arm.AppliesTo(c.Surface) {
		return fmt.Errorf("%w: arm %q is not meaningful on the %s surface", ErrConditionUnrunnable, c.Arm.ID, c.Surface)
	}
	if c.Arm.ID != ablation.AsShipped && c.Backend != backend.B5Cairn {
		return fmt.Errorf("%w: %s is a baseline — it has no ranking profile, no embedder and no inclusion classes, so ablating it is meaningless; ablations apply to %s only",
			ErrConditionUnrunnable, c.Backend, backend.B5Cairn)
	}
	return nil
}

// Run executes one condition end to end: provision, write every item, ask
// every query, record. It computes nothing.
func Run(ctx context.Context, opts Options, cond Condition, mat Material) (*Outcome, error) {
	if err := Applicable(cond); err != nil {
		return nil, err
	}
	b, err := backend.New(cond.Backend)
	if err != nil {
		return nil, err
	}
	if backend.IsStub(b) {
		// B3/B4 fail loudly rather than returning empty results; carrying that
		// through here rather than skipping is the whole point.
		return nil, fmt.Errorf("%w: %s is a declared stub: %s", ErrConditionUnrunnable, cond.Backend, b.Capabilities().Notes)
	}
	if !b.Capabilities().Supports(cond.Surface) {
		return nil, fmt.Errorf("%w: %s has no %s surface", ErrConditionUnrunnable, cond.Backend, cond.Surface)
	}

	work, err := os.MkdirTemp("", "cairn-eval-"+string(cond.Backend)+"-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()

	cfg := backend.Config{WorkDir: work, Seed: opts.Seed, Arm: cond.Arm.BackendConfig()}
	meta := result.Backend{ID: string(cond.Backend), Notes: b.Capabilities().Notes}
	if cond.Backend == backend.B5Cairn {
		bin := opts.Binary
		if bin.Path == "" {
			bin, err = cairnctl.FindBinary(ctx)
			if err != nil {
				return nil, err
			}
		}
		cfg.Binary = bin
		meta.CairnVersion, meta.CairnBuildTags = bin.Version, bin.Tags
	}
	if err := b.Open(ctx, cfg); err != nil {
		return nil, err
	}
	defer func() { _ = b.Close(ctx) }()

	for _, it := range mat.Items {
		if _, err := b.Write(ctx, it); err != nil {
			return nil, fmt.Errorf("write %s: %w", it.ID, err)
		}
	}

	run := result.NewRun(result.KindMeasurement, opts.Seed, meta, mat.ResultCorpus())
	run.Label = opts.Label
	run.Note("condition %s; arm mechanism: %s", cond, cond.Arm.Mechanism)
	if cond.Arm.Limits != "" {
		run.Note("arm limits: %s", cond.Arm.Limits)
	}

	out := &Outcome{
		Condition: cond,
		RunID:     run.RunID,
		Rankings:  map[string]metric.Ranking{},
		Excluded:  map[string]string{},
	}
	explainer, _ := b.(backend.Explainer)
	needExplanations := cond.Arm.Rerank != nil || cond.Arm.ExpectProfile != "" || cond.Arm.RequiresEmbedder

	for _, q := range mat.Queries {
		out.askedIDs = append(out.askedIDs, q.ID)
		rec := result.Outcome{
			QueryID: q.ID, Query: q.Text, Surface: string(cond.Surface),
			Relevant: q.Relevant, BudgetChars: opts.budget(),
		}
		resp, err := b.Retrieve(ctx, backend.Request{
			Surface: cond.Surface, Query: q.Text, K: opts.k(), BudgetChars: opts.budget(),
		})
		switch {
		case errors.Is(err, backend.ErrUnsupportedSurface):
			rec.Error = "backend does not implement this surface"
			out.Excluded[q.ID] = rec.Error
			run.Add(rec)
			continue
		case err != nil:
			return nil, fmt.Errorf("retrieve %s: %w", q.ID, err)
		}
		if resp.RetrievalMode != "" {
			out.RetrievalMode = resp.RetrievalMode
			run.Backend.RetrievalMode = resp.RetrievalMode
		}

		ranked := make([]ablation.Ranked, 0, len(resp.Hits))
		for _, h := range resp.Hits {
			ranked = append(ranked, ablation.Ranked{Hit: h})
		}
		if needExplanations && explainer != nil && resp.InteractionID != "" {
			traces, profile, err := readExplanations(ctx, explainer, resp.InteractionID, ranked)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", q.ID, err)
			}
			rec.Explanations = traces
			if profile != "" {
				out.Profile = profile
			}
		}
		if cond.Arm.Rerank != nil {
			if err := requireExplanations(cond, ranked); err != nil {
				return nil, err
			}
			ranked = cond.Arm.Rerank(ranked)
		}

		for _, r := range ranked {
			rec.Returned = append(rec.Returned, r.Hit.ItemID)
			rec.ReturnedNative = append(rec.ReturnedNative, r.Hit.NativeID)
			out.Rankings[q.ID] = append(out.Rankings[q.ID], r.Hit.ItemID)
		}
		rec.PayloadChars = len([]rune(resp.Payload))
		rec.ElapsedMS = resp.Elapsed.Milliseconds()
		rec.Partial, rec.PartialReason = resp.Partial, resp.PartialReason
		rec.InteractionID = resp.InteractionID
		rec.Raw = resp.Raw
		run.Add(rec)
	}

	if err := verifyArmTook(cond.Arm, out); err != nil {
		return nil, err
	}
	// A condition that returned NOTHING for every query is a legitimate score
	// of zero — the system really did fail to retrieve — but it is also the
	// signature of a condition that was never properly exercised (a query
	// language mismatch, an empty index, a filter that excluded everything).
	// Those two look identical in a mean, so the distinction is recorded here
	// rather than left for someone to notice. It is an OBSERVATION about the
	// run, not a judgment about the system.
	if empty, total := out.emptyQueries(); total > 0 && empty == total {
		out.EmptyForEveryQuery = true
		run.Note("EVERY QUERY RETURNED ZERO RESULTS on this condition (%d/%d). This scores as zero, and it may genuinely be zero — but it is also what a query-language mismatch or an unexercised index looks like. Read the raw output before treating the zeros as a retrieval result.", empty, total)
	}
	out.Notes = run.Notes

	path := filepath.Join(opts.OutDir, fmt.Sprintf("%s-%s-%s-%s.json",
		opts.Label, cond.Backend, cond.Arm.ID, time.Now().UTC().Format("20060102T150405.000Z")))
	if err := run.WriteFile(path); err != nil {
		return nil, err
	}
	out.RunPath = path
	return out, nil
}

// readExplanations fetches and parses `cairn why-ranked` for every hit, and
// RECONCILES each one. A trace that does not reconcile means the published
// arithmetic does not describe the published score — at which point every
// recomputed ablation built on it would be measuring fiction, so it is a hard
// error rather than a warning.
func readExplanations(ctx context.Context, ex backend.Explainer, interactionID string, ranked []ablation.Ranked) (map[string]string, string, error) {
	traces := map[string]string{}
	profile := ""
	for i := range ranked {
		id := ranked[i].Hit.NativeID
		if id == "" {
			continue
		}
		raw, err := ex.Explain(ctx, interactionID, id)
		if err != nil {
			return nil, "", fmt.Errorf("why-ranked %s: %w", id, err)
		}
		e, err := explain.Parse(raw)
		if err != nil {
			return nil, "", err
		}
		if err := e.Reconcile(); err != nil {
			return nil, "", err
		}
		ranked[i].Exp = e
		traces[id] = raw
		profile = e.Profile
	}
	return traces, profile, nil
}

// requireExplanations refuses to apply a recomputed ablation to results whose
// arithmetic was never read. Re-ranking without the traces would silently
// return the original order — i.e. would report "the ablation changed nothing".
func requireExplanations(cond Condition, ranked []ablation.Ranked) error {
	for _, r := range ranked {
		if r.Exp == nil {
			return fmt.Errorf("%w: %s needs the published ranking arithmetic for every hit and has none for %q; re-ranking without it would report 'no effect'",
				ErrConditionUnrunnable, cond, r.Hit.NativeID)
		}
	}
	return nil
}

// verifyArmTook checks that the system actually entered the state the arm
// names. An arm that silently failed to take is a MISLABELLED result, which is
// worse than a missing one: it reads as "this ablation made no difference".
func verifyArmTook(arm ablation.Arm, out *Outcome) error {
	if arm.ExpectRetrievalMode != "" && out.RetrievalMode != "" && out.RetrievalMode != arm.ExpectRetrievalMode {
		return fmt.Errorf("%w: arm %q expects retrieval_mode=%q, the system reported %q",
			ablation.ErrArmNotTaken, arm.ID, arm.ExpectRetrievalMode, out.RetrievalMode)
	}
	if arm.RequiresEmbedder && out.RetrievalMode != "full" {
		return fmt.Errorf("%w: arm %q is meaningless without semantic retrieval; retrieval_mode is %q. Provision an embedder (scripts/cairn-embed-bootstrap.sh) or drop the arm — running it here would produce a vacuous result",
			ablation.ErrArmNotTaken, arm.ID, out.RetrievalMode)
	}
	// why-ranked prints "search-P0" / "digest-P2"; the arm names the family.
	if arm.ExpectProfile != "" && out.Profile != "" && !strings.Contains(out.Profile, arm.ExpectProfile) {
		return fmt.Errorf("%w: arm %q expects the %s profile, the published arithmetic reports %q",
			ablation.ErrArmNotTaken, arm.ID, arm.ExpectProfile, out.Profile)
	}
	return nil
}

// Score builds a scorecard section for this outcome: the ONE bridge from
// observation to number in the whole harness, and a pure function of the
// recorded ranking plus the corpus's own ground truth. Everything upstream is
// a fact about a run; everything downstream is gated by score.Reportable.
func (o *Outcome) Score(mat Material, seed int64) score.Section {
	arm := score.Arm{
		Backend:       string(o.Condition.Backend),
		Ablation:      o.Condition.Arm.ID,
		Surface:       string(o.Condition.Surface),
		Fidelity:      string(o.Condition.Arm.Fidelity),
		Limits:        o.Condition.Arm.Limits,
		RetrievalMode: o.RetrievalMode,
	}
	sec := score.Section{Arm: arm, Runs: []string{o.RunID}, Notes: o.Notes}
	for _, k := range score.Cutoffs {
		for name, fn := range metric.Fns() {
			var samples []metric.Sample
			for _, q := range mat.Queries {
				s := metric.Sample{QueryID: q.ID}
				if reason, bad := o.Excluded[q.ID]; bad {
					s.Excluded, s.Reason = true, reason
				} else {
					s.Value = score.Round(fn(o.Rankings[q.ID], metric.NewRelevanceSet(q.Relevant), k))
				}
				samples = append(samples, s)
			}
			sec.Metrics = append(sec.Metrics, name.Aggregate(k, samples, seed))
		}
	}
	// Fns() iterates a map, so the emitted order would otherwise vary run to
	// run and make two scorecards over identical data diff noisily.
	sort.SliceStable(sec.Metrics, func(i, j int) bool {
		if sec.Metrics[i].K != sec.Metrics[j].K {
			return sec.Metrics[i].K < sec.Metrics[j].K
		}
		return sec.Metrics[i].Metric < sec.Metrics[j].Metric
	})
	sec.Errors = len(o.Excluded)
	return sec
}
