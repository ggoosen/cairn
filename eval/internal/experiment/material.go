// Package experiment drives a memory condition over a body of material and
// records what happened — the shared engine underneath E4's ablations and
// baselines and E9's recall-under-growth curve.
//
// The division of labour is deliberate and is the reason the apparatus can be
// built dark:
//
//	experiment  runs things and records OBSERVATIONS (internal/result)
//	score       derives numbers from observations, and gates their reporting
//
// Nothing in this package computes a metric or compares two conditions. It
// produces run records and per-query rankings; turning those into a comparison
// is score's job, and score refuses while the kill criteria are unsigned.
package experiment

import (
	"fmt"

	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/corpus"
	"github.com/ggoosen/cairn/eval/internal/result"
	"github.com/ggoosen/cairn/eval/internal/score"
)

// Query is one information need with its human-made ground truth.
type Query struct {
	ID       string
	Text     string
	Relevant []string
}

// Material is everything an experiment writes and asks about. It is the same
// shape whether it came from a corpus directory or was synthesized (E9's
// growth filler), so the runner never needs to know which.
type Material struct {
	Items   []backend.Item
	Queries []Query

	// CorpusRef pins what this material is, for both the run record and the
	// scorecard. Independent is the field that decides whether any number over
	// it could ever be evidence.
	CorpusRef score.CorpusRef
}

// FromCorpus converts a loaded corpus, optionally restricted to one split.
//
// BUILD-PLAN §3.7 forbids tuning on the evaluation set: weights are calibrated
// on dev, and holdout answers the claim. The split is therefore an explicit
// argument with no default — a caller that does not say which half it is using
// has not thought about the question.
func FromCorpus(c *corpus.Corpus, split string) (Material, error) {
	m := Material{
		Items: c.BackendItems(),
		CorpusRef: score.CorpusRef{
			ID:          c.Manifest.ID,
			Version:     c.Manifest.Version,
			Checksum:    c.Manifest.Checksum,
			LabelSource: c.Manifest.Labels.Provenance,
			Independent: c.Manifest.Labels.Independent,
		},
	}
	var qs []corpus.Query
	switch split {
	case corpus.SplitDev, corpus.SplitHoldout:
		qs = c.Split(split)
	case "all":
		qs = c.Queries
	default:
		return Material{}, fmt.Errorf("split must be %q, %q or \"all\" (BUILD-PLAN §3.7: calibrate on dev, answer the claim on holdout); got %q",
			corpus.SplitDev, corpus.SplitHoldout, split)
	}
	if len(qs) == 0 {
		return Material{}, fmt.Errorf("corpus %s has no queries in split %q", c.Manifest.ID, split)
	}
	for _, q := range qs {
		m.Queries = append(m.Queries, Query{ID: q.ID, Text: q.Query, Relevant: q.Relevant})
	}
	return m, nil
}

// ResultCorpus renders the material's identity for a run record.
func (m Material) ResultCorpus() result.Corpus {
	return result.Corpus{
		ID:          m.CorpusRef.ID,
		Version:     m.CorpusRef.Version,
		Checksum:    m.CorpusRef.Checksum,
		ItemCount:   len(m.Items),
		QueryCount:  len(m.Queries),
		LabelSource: m.CorpusRef.LabelSource,
	}
}
